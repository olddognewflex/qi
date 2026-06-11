/**
 * Re-exports of the pure helper functions from src/index.js so tests can
 * import them without pulling in the full Worker module binding (env, etc.).
 *
 * Keeping these as named exports here lets the main Worker file remain a
 * single readable module while still being unit-testable without miniflare.
 */

// ---------------------------------------------------------------------------
// timingSafeEqual
// ---------------------------------------------------------------------------

export function timingSafeEqual(a, b) {
  const enc = new TextEncoder();
  const ab = enc.encode(a);
  const bb = enc.encode(b);
  const len = Math.max(ab.length, bb.length);
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
// mintID
// ---------------------------------------------------------------------------

export function mintID() {
  const b = crypto.getRandomValues(new Uint8Array(4));
  return 'qi-' + [...b].map(x => x.toString(16).padStart(2, '0')).join('');
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

const RE_PROJECT = /^[A-Za-z0-9_\-/]+$/;
const RE_DATE    = /^\d{4}-\d{2}-\d{2}$/;
const ACK_MAX_IDS = 500;

export function validateEnqueueBody(body) {
  if (typeof body !== 'object' || body === null || Array.isArray(body)) {
    return 'body must be a JSON object';
  }

  const allowed = new Set(['text', 'project', 'client', 'due', 'scheduled', 'source']);
  for (const k of Object.keys(body)) {
    if (!allowed.has(k)) return `unknown field: ${k}`;
  }

  if (body.text === undefined || body.text === null) return 'text is required';
  if (typeof body.text !== 'string') return 'text must be a string';
  const trimmed = body.text.trim();
  if (trimmed.length === 0) return 'text must not be empty';
  for (let i = 0; i < body.text.length; i++) {
    const c = body.text.charCodeAt(i);
    if (c < 0x20 || c === 0x7f) return 'text must not contain control characters';
  }

  const hasProject = body.project !== undefined && body.project !== null;
  const hasClient  = body.client  !== undefined && body.client  !== null;
  if (hasProject && hasClient) return 'project and client are mutually exclusive';

  if (hasProject) {
    if (typeof body.project !== 'string') return 'project must be a string';
    if (!RE_PROJECT.test(body.project)) return 'project must match ^[A-Za-z0-9_\\-/]+$';
  }

  if (hasClient) {
    if (typeof body.client !== 'string') return 'client must be a string';
  }

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

  if (body.source !== undefined && body.source !== null) {
    if (typeof body.source !== 'string') return 'source must be a string';
  }

  return null;
}

export function validateIDs(ids) {
  if (!Array.isArray(ids) || ids.length === 0) return 'ids must be a non-empty array';
  if (ids.length > ACK_MAX_IDS) return `ids must contain at most ${ACK_MAX_IDS} entries`;
  for (const id of ids) {
    if (typeof id !== 'string' || id.length === 0) return 'each id must be a non-empty string';
  }
  return null;
}
