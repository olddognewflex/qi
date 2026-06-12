/**
 * qi-queue — Cloudflare Worker + D1 queue API
 *
 * Two-token auth model:
 *   ENQUEUE_TOKEN  — phone / iOS Shortcut, authorizes POST /enqueue only.
 *   DRAIN_TOKEN    — laptop qi remote-drain, authorizes GET|POST|DELETE on
 *                    /pull, /ack, /deadletter, /stats.
 *
 * A valid token presented to a route outside its scope returns 403.
 * A missing or wrong token returns 401.
 *
 * Id format: 'qi-' + 8 lowercase hex chars (32 bits from CSPRNG).
 * This MUST remain stable — the Go client and iOS Shortcut depend on it.
 */

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const PULL_DEFAULT_LIMIT = 100;
const PULL_MAX_LIMIT     = 500;
const ACK_MAX_IDS        = 500;
const BODY_SIZE_LIMIT    = 8192; // bytes; generous for a task payload

// Which tokens are accepted on which routes.
// Key: "<METHOD> <pathname>", value: which secret env var holds the valid token.
const ROUTE_TOKEN_SCOPE = {
  'POST /enqueue':       'ENQUEUE_TOKEN',
  'GET /pull':           'DRAIN_TOKEN',
  'POST /ack':           'DRAIN_TOKEN',
  'POST /deadletter':    'DRAIN_TOKEN',
  'GET /deadletter':     'DRAIN_TOKEN',
  'DELETE /deadletter':  'DRAIN_TOKEN',
  'GET /stats':          'DRAIN_TOKEN',
};

// ---------------------------------------------------------------------------
// Timing-safe string comparison
//
// We convert both strings to UTF-8 bytes and XOR every byte. We always
// iterate max(a.length, b.length) so the loop body does not leak length
// information in branching (length difference is still observable via timing
// in degenerate cases, but secrets are fixed-length tokens so this is fine).
// ---------------------------------------------------------------------------

function timingSafeEqual(a, b) {
  const enc = new TextEncoder();
  const ab = enc.encode(a);
  const bb = enc.encode(b);
  const len = Math.max(ab.length, bb.length);
  // Pad shorter buffer with zeros so XOR covers the full length.
  const aa = new Uint8Array(len);
  const ba = new Uint8Array(len);
  aa.set(ab);
  ba.set(bb);
  let diff = 0;
  for (let i = 0; i < len; i++) {
    diff |= aa[i] ^ ba[i];
  }
  return diff === 0;
}

// ---------------------------------------------------------------------------
// Response helpers
// ---------------------------------------------------------------------------

function json(data, status = 200) {
  return new Response(JSON.stringify(data), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function err(msg, status) {
  return json({ error: msg }, status);
}

// ---------------------------------------------------------------------------
// Id minting — MUST stay in sync with vault.MintID() on the Go side.
// Format: qi- + 8 lowercase hex chars = qi-xxxxxxxx
// ---------------------------------------------------------------------------

function mintID() {
  const b = crypto.getRandomValues(new Uint8Array(4));
  return 'qi-' + [...b].map(x => x.toString(16).padStart(2, '0')).join('');
}

// ---------------------------------------------------------------------------
// Input validation helpers
// ---------------------------------------------------------------------------

const RE_PROJECT  = /^[A-Za-z0-9_\-/]+$/;
const RE_DATE     = /^\d{4}-\d{2}-\d{2}$/;
const VALID_KINDS = new Set(['task', 'note', 'capture']);

/**
 * Returns a string error message or null if valid.
 * Validates the JSON body for POST /enqueue per spec §1.
 */
function validateEnqueueBody(body) {
  if (typeof body !== 'object' || body === null || Array.isArray(body)) {
    return 'body must be a JSON object';
  }

  // Reject unknown fields to surface Shortcut misconfiguration quickly.
  const allowed = new Set(['text', 'kind', 'project', 'client', 'due', 'scheduled', 'source']);
  for (const k of Object.keys(body)) {
    if (!allowed.has(k)) return `unknown field: ${k}`;
  }

  // text: required, non-empty after trim, no control chars
  if (body.text === undefined || body.text === null) return 'text is required';
  if (typeof body.text !== 'string') return 'text must be a string';
  const trimmed = body.text.trim();
  if (trimmed.length === 0) return 'text must not be empty';
  for (let i = 0; i < body.text.length; i++) {
    const c = body.text.charCodeAt(i);
    if (c < 0x20 || c === 0x7f) return 'text must not contain control characters';
  }

  // kind: optional; defaults to 'task' (absent/null is allowed). When present it
  // must name a known kind so the laptop drain can route the row.
  if (body.kind !== undefined && body.kind !== null) {
    if (typeof body.kind !== 'string' || !VALID_KINDS.has(body.kind)) {
      return 'kind must be one of task, note, capture';
    }
  }

  // project / client: optional, mutually exclusive
  const hasProject = body.project !== undefined && body.project !== null;
  const hasClient  = body.client  !== undefined && body.client  !== null;
  if (hasProject && hasClient) return 'project and client are mutually exclusive';

  if (hasProject) {
    if (typeof body.project !== 'string') return 'project must be a string';
    if (!RE_PROJECT.test(body.project)) {
      return 'project must match ^[A-Za-z0-9_\\-/]+$';
    }
  }

  if (hasClient) {
    if (typeof body.client !== 'string') return 'client must be a string';
  }

  // due / scheduled: optional date strings
  if (body.due !== undefined && body.due !== null) {
    if (typeof body.due !== 'string' || !RE_DATE.test(body.due)) {
      return 'due must be YYYY-MM-DD';
    }
  }
  if (body.scheduled !== undefined && body.scheduled !== null) {
    if (typeof body.scheduled !== 'string' || !RE_DATE.test(body.scheduled)) {
      return 'scheduled must be YYYY-MM-DD';
    }
  }

  // source: optional, no validation (audit field)
  if (body.source !== undefined && body.source !== null) {
    if (typeof body.source !== 'string') return 'source must be a string';
  }

  return null; // valid
}

/**
 * Validate that ids is a non-empty array of strings and not too long.
 */
function validateIDs(ids) {
  if (!Array.isArray(ids) || ids.length === 0) return 'ids must be a non-empty array';
  if (ids.length > ACK_MAX_IDS) return `ids must contain at most ${ACK_MAX_IDS} entries`;
  for (const id of ids) {
    if (typeof id !== 'string' || id.length === 0) return 'each id must be a non-empty string';
  }
  return null;
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

/**
 * Check the Authorization header and return:
 *   { ok: true, token }   on success
 *   { ok: false, status } on failure
 *
 * status 401 = missing or wrong token
 * status 403 = valid token but wrong scope for this route
 */
function checkAuth(request, env, method, pathname) {
  const routeKey = `${method} ${pathname}`;
  const requiredScope = ROUTE_TOKEN_SCOPE[routeKey];

  // Route not in the scope map — should not happen; treat as 404 elsewhere.
  if (!requiredScope) return { ok: true };

  const authHeader = request.headers.get('Authorization') || '';
  const bearer = authHeader.startsWith('Bearer ') ? authHeader.slice(7) : '';

  if (!bearer) return { ok: false, status: 401 };

  const enqueueToken = env.ENQUEUE_TOKEN || '';
  const drainToken   = env.DRAIN_TOKEN   || '';

  const isEnqueue = enqueueToken && timingSafeEqual(bearer, enqueueToken);
  const isDrain   = drainToken   && timingSafeEqual(bearer, drainToken);

  if (!isEnqueue && !isDrain) {
    // Token provided but matches neither secret.
    return { ok: false, status: 401 };
  }

  // Token is valid — now check scope.
  const presentedScope = isEnqueue ? 'ENQUEUE_TOKEN' : 'DRAIN_TOKEN';
  if (presentedScope !== requiredScope) {
    // A valid token used on a route outside its scope.
    return { ok: false, status: 403 };
  }

  return { ok: true };
}

// ---------------------------------------------------------------------------
// Route handlers
// ---------------------------------------------------------------------------

async function handleEnqueue(request, env) {
  // Cap body size before parsing.
  const contentLength = parseInt(request.headers.get('Content-Length') || '0', 10);
  if (contentLength > BODY_SIZE_LIMIT) {
    return err('request body too large', 413);
  }

  let body;
  try {
    const text = await request.text();
    if (text.length > BODY_SIZE_LIMIT) return err('request body too large', 413);
    body = JSON.parse(text);
  } catch {
    return err('invalid JSON body', 400);
  }

  const validationError = validateEnqueueBody(body);
  if (validationError) return err(validationError, 400);

  const id         = mintID();
  const createdAt  = Math.floor(Date.now() / 1000);

  await env.DB.prepare(
    `INSERT INTO queue (id, text, kind, project, client, due, scheduled, source, created_at, status)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending')`
  ).bind(
    id,
    body.text.trim(),
    body.kind      ?? 'task',
    body.project   ?? null,
    body.client    ?? null,
    body.due       ?? null,
    body.scheduled ?? null,
    body.source    ?? null,
    createdAt,
  ).run();

  return json({ id }, 201);
}

async function handlePull(request, env) {
  const url   = new URL(request.url);
  let   limit = parseInt(url.searchParams.get('limit') || String(PULL_DEFAULT_LIMIT), 10);
  if (isNaN(limit) || limit < 1) limit = PULL_DEFAULT_LIMIT;
  if (limit > PULL_MAX_LIMIT)    limit = PULL_MAX_LIMIT;

  const { results } = await env.DB.prepare(
    `SELECT id, text, kind, project, client, due, scheduled, source, created_at
     FROM queue
     WHERE status = 'pending'
     ORDER BY created_at ASC
     LIMIT ?`
  ).bind(limit).all();

  return json({ tasks: results ?? [] });
}

async function handleAck(request, env) {
  let body;
  try { body = await request.json(); } catch { return err('invalid JSON body', 400); }

  const idError = validateIDs(body?.ids);
  if (idError) return err(idError, 400);

  // Build a parameterized IN clause — no string interpolation of user data.
  const placeholders = body.ids.map(() => '?').join(', ');
  const result = await env.DB.prepare(
    `DELETE FROM queue WHERE id IN (${placeholders}) AND status = 'pending'`
  ).bind(...body.ids).run();

  return json({ deleted: result.meta?.changes ?? 0 });
}

async function handleDeadletterPost(request, env) {
  let body;
  try { body = await request.json(); } catch { return err('invalid JSON body', 400); }

  const idError = validateIDs(body?.ids);
  if (idError) return err(idError, 400);

  const reason = typeof body?.reason === 'string' ? body.reason : null;

  const placeholders = body.ids.map(() => '?').join(', ');
  const result = await env.DB.prepare(
    `UPDATE queue SET status = 'deadletter', reason = ? WHERE id IN (${placeholders})`
  ).bind(reason, ...body.ids).run();

  return json({ updated: result.meta?.changes ?? 0 });
}

async function handleDeadletterGet(_request, env) {
  const { results } = await env.DB.prepare(
    `SELECT id, text, kind, project, client, due, scheduled, source, created_at, reason
     FROM queue
     WHERE status = 'deadletter'
     ORDER BY created_at ASC`
  ).all();

  return json({ tasks: results ?? [] });
}

async function handleDeadletterDelete(request, env) {
  let body;
  try { body = await request.json(); } catch { return err('invalid JSON body', 400); }

  const idError = validateIDs(body?.ids);
  if (idError) return err(idError, 400);

  const placeholders = body.ids.map(() => '?').join(', ');
  const result = await env.DB.prepare(
    `DELETE FROM queue WHERE id IN (${placeholders}) AND status = 'deadletter'`
  ).bind(...body.ids).run();

  return json({ deleted: result.meta?.changes ?? 0 });
}

async function handleStats(_request, env) {
  // One grouped scan over the status index returns both counts.
  const { results } = await env.DB.prepare(
    `SELECT status, COUNT(*) AS count FROM queue GROUP BY status`
  ).all();

  const counts = { pending: 0, deadletter: 0 };
  for (const row of results ?? []) {
    if (row.status in counts) counts[row.status] = row.count;
  }
  return json(counts);
}

// ---------------------------------------------------------------------------
// Scheduled handler — purge stale pending rows (> 7 days) to deadletter.
// Enable by uncommenting [triggers] crons in wrangler.toml.
// ---------------------------------------------------------------------------

async function handleScheduled(_event, env) {
  const cutoff = Math.floor(Date.now() / 1000) - 7 * 24 * 60 * 60; // 7 days ago
  const result = await env.DB.prepare(
    `UPDATE queue
     SET status = 'deadletter', reason = 'stale: not drained within 7 days'
     WHERE status = 'pending' AND created_at < ?`
  ).bind(cutoff).run();
  console.log(`[cron] purged ${result.meta?.changes ?? 0} stale pending rows to deadletter`);
}

// ---------------------------------------------------------------------------
// Main fetch handler
// ---------------------------------------------------------------------------

export default {
  async fetch(request, env, _ctx) {
    const url      = new URL(request.url);
    const method   = request.method.toUpperCase();
    const pathname = url.pathname.replace(/\/$/, '') || '/'; // strip trailing slash

    // Health check — no auth.
    if (method === 'GET' && pathname === '/healthz') {
      return new Response('ok', { status: 200 });
    }

    // Auth gate.
    const routeKey = `${method} ${pathname}`;
    if (ROUTE_TOKEN_SCOPE[routeKey] !== undefined) {
      const auth = checkAuth(request, env, method, pathname);
      if (!auth.ok) {
        return err(
          auth.status === 403 ? 'token not authorized for this route' : 'unauthorized',
          auth.status,
        );
      }
    }

    // Route dispatch.
    if (method === 'POST' && pathname === '/enqueue') {
      return handleEnqueue(request, env);
    }
    if (method === 'GET' && pathname === '/pull') {
      return handlePull(request, env);
    }
    if (method === 'POST' && pathname === '/ack') {
      return handleAck(request, env);
    }
    if (method === 'POST' && pathname === '/deadletter') {
      return handleDeadletterPost(request, env);
    }
    if (method === 'GET' && pathname === '/deadletter') {
      return handleDeadletterGet(request, env);
    }
    if (method === 'DELETE' && pathname === '/deadletter') {
      return handleDeadletterDelete(request, env);
    }
    if (method === 'GET' && pathname === '/stats') {
      return handleStats(request, env);
    }

    return err('not found', 404);
  },

  async scheduled(event, env, _ctx) {
    await handleScheduled(event, env);
  },
};
