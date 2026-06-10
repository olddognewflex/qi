/**
 * Integration-style tests for the Worker fetch handler.
 *
 * Uses an in-process D1 mock (better-sqlite3) so we can run the full handler
 * logic without miniflare / wrangler. The mock exposes the same
 * prepare().bind().run() / .all() interface that D1 uses.
 *
 * Run: npm test
 * (requires better-sqlite3 — installed as a devDependency)
 *
 * For true D1 integration tests (exact SQL dialect, Worker runtime):
 *   wrangler dev --test   or   vitest --pool=@cloudflare/vitest-pool-workers
 */

import { describe, it, expect, beforeEach } from 'vitest';
import Database from 'better-sqlite3';
import worker from '../src/index.js';

// ---------------------------------------------------------------------------
// In-process D1 mock backed by better-sqlite3
// ---------------------------------------------------------------------------

function makeD1(db) {
  return {
    prepare(sql) {
      let boundArgs = [];
      const stmt = {
        bind(...args) {
          boundArgs = args;
          return stmt;
        },
        run() {
          const s = db.prepare(sql);
          const info = s.run(...boundArgs);
          return { meta: { changes: info.changes } };
        },
        all() {
          const s = db.prepare(sql);
          const rows = s.all(...boundArgs);
          return { results: rows };
        },
        first() {
          const s = db.prepare(sql);
          return s.get(...boundArgs) ?? null;
        },
      };
      return stmt;
    },
  };
}

// ---------------------------------------------------------------------------
// Env factory
// ---------------------------------------------------------------------------

const ENQUEUE = 'test-enqueue-token';
const DRAIN   = 'test-drain-token';
const WRONG   = 'wrong-token';

function makeEnv(db) {
  return {
    DB: makeD1(db),
    ENQUEUE_TOKEN: ENQUEUE,
    DRAIN_TOKEN:   DRAIN,
  };
}

// ---------------------------------------------------------------------------
// Request helpers
// ---------------------------------------------------------------------------

function req(method, path, { token, body } = {}) {
  const headers = { 'Content-Type': 'application/json' };
  if (token) headers['Authorization'] = `Bearer ${token}`;
  return new Request(`https://worker${path}`, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
}

async function call(method, path, opts = {}) {
  const db  = opts._db;
  const env = makeEnv(db);
  const res = await worker.fetch(req(method, path, opts), env, {});
  const text = await res.text();
  let data;
  try { data = JSON.parse(text); } catch { data = text; }
  return { status: res.status, data };
}

// ---------------------------------------------------------------------------
// DB setup
// ---------------------------------------------------------------------------

let db;

function freshDB() {
  db = new Database(':memory:');
  db.exec(`
    CREATE TABLE queue (
      id         TEXT PRIMARY KEY,
      text       TEXT NOT NULL,
      project    TEXT,
      client     TEXT,
      due        TEXT,
      scheduled  TEXT,
      source     TEXT,
      created_at INTEGER NOT NULL,
      reason     TEXT,
      status     TEXT NOT NULL DEFAULT 'pending'
    );
    CREATE INDEX idx_queue_status ON queue(status, created_at);
  `);
}

// ---------------------------------------------------------------------------
// /healthz
// ---------------------------------------------------------------------------

describe('GET /healthz', () => {
  beforeEach(freshDB);

  it('returns 200 ok with no auth', async () => {
    const env = makeEnv(db);
    const res = await worker.fetch(new Request('https://worker/healthz'), env, {});
    expect(res.status).toBe(200);
    expect(await res.text()).toBe('ok');
  });
});

// ---------------------------------------------------------------------------
// Auth — 401 / 403
// ---------------------------------------------------------------------------

describe('auth', () => {
  beforeEach(freshDB);

  it('POST /enqueue with no token → 401', async () => {
    const r = await call('POST', '/enqueue', { _db: db, body: { text: 'hi' } });
    expect(r.status).toBe(401);
  });

  it('POST /enqueue with wrong token → 401', async () => {
    const r = await call('POST', '/enqueue', { _db: db, token: WRONG, body: { text: 'hi' } });
    expect(r.status).toBe(401);
  });

  it('POST /enqueue with drain token → 403 (wrong scope)', async () => {
    const r = await call('POST', '/enqueue', { _db: db, token: DRAIN, body: { text: 'hi' } });
    expect(r.status).toBe(403);
  });

  it('GET /pull with enqueue token → 403 (wrong scope)', async () => {
    const r = await call('GET', '/pull', { _db: db, token: ENQUEUE });
    expect(r.status).toBe(403);
  });

  it('GET /pull with no token → 401', async () => {
    const r = await call('GET', '/pull', { _db: db });
    expect(r.status).toBe(401);
  });

  it('GET /pull with wrong token → 401', async () => {
    const r = await call('GET', '/pull', { _db: db, token: WRONG });
    expect(r.status).toBe(401);
  });

  it('POST /ack with enqueue token → 403', async () => {
    const r = await call('POST', '/ack', { _db: db, token: ENQUEUE, body: { ids: ['qi-aabbccdd'] } });
    expect(r.status).toBe(403);
  });

  it('GET /deadletter with enqueue token → 403', async () => {
    const r = await call('GET', '/deadletter', { _db: db, token: ENQUEUE });
    expect(r.status).toBe(403);
  });
});

// ---------------------------------------------------------------------------
// POST /enqueue — validation
// ---------------------------------------------------------------------------

describe('POST /enqueue validation', () => {
  beforeEach(freshDB);

  const enqueue = (body) => call('POST', '/enqueue', { _db: db, token: ENQUEUE, body });

  it('rejects missing text → 400', async () => {
    const r = await enqueue({});
    expect(r.status).toBe(400);
  });

  it('rejects empty text after trim → 400', async () => {
    const r = await enqueue({ text: '   ' });
    expect(r.status).toBe(400);
  });

  it('rejects text with control char (newline) → 400', async () => {
    const r = await enqueue({ text: 'foo\nbar' });
    expect(r.status).toBe(400);
  });

  it('rejects text with control char (tab) → 400', async () => {
    const r = await enqueue({ text: 'foo\tbar' });
    expect(r.status).toBe(400);
  });

  it('rejects text with DEL (0x7f) → 400', async () => {
    const r = await enqueue({ text: 'foo\x7fbar' });
    expect(r.status).toBe(400);
  });

  it('rejects bad project charset → 400', async () => {
    const r = await enqueue({ text: 'x', project: 'bad project' });
    expect(r.status).toBe(400);
  });

  it('rejects project + client together → 400', async () => {
    const r = await enqueue({ text: 'x', project: 'foo', client: 'bar' });
    expect(r.status).toBe(400);
  });

  it('rejects bad date format → 400', async () => {
    const r = await enqueue({ text: 'x', due: '2026/06/15' });
    expect(r.status).toBe(400);
  });

  it('rejects unknown field → 400', async () => {
    const r = await enqueue({ text: 'x', priority: 'high' });
    expect(r.status).toBe(400);
  });

  it('accepts a valid minimal body → 201', async () => {
    const r = await enqueue({ text: 'buy oat milk' });
    expect(r.status).toBe(201);
    expect(r.data.id).toMatch(/^qi-[0-9a-f]{8}$/);
  });

  it('minted id matches qi-[0-9a-f]{8} pattern', async () => {
    const r = await enqueue({ text: 'test id format', source: 'ios-shortcut' });
    expect(r.status).toBe(201);
    expect(r.data.id).toMatch(/^qi-[0-9a-f]{8}$/);
  });

  it('accepts project, due, scheduled, source', async () => {
    const r = await enqueue({
      text: 'design review',
      project: 'work/qi',
      due: '2026-06-20',
      scheduled: '2026-06-18',
      source: 'ios-shortcut',
    });
    expect(r.status).toBe(201);
  });
});

// ---------------------------------------------------------------------------
// Enqueue → pull → ack round-trip
// ---------------------------------------------------------------------------

describe('enqueue → pull → ack round-trip', () => {
  beforeEach(freshDB);

  it('enqueued task appears in /pull and is gone after /ack', async () => {
    const env = makeEnv(db);

    // 1. Enqueue
    const enqRes = await worker.fetch(
      req('POST', '/enqueue', { token: ENQUEUE, body: { text: 'ship it', source: 'test' } }),
      env, {},
    );
    expect(enqRes.status).toBe(201);
    const { id } = await enqRes.json();
    expect(id).toMatch(/^qi-[0-9a-f]{8}$/);

    // 2. Pull — should contain the task
    const pullRes = await worker.fetch(req('GET', '/pull', { token: DRAIN }), env, {});
    expect(pullRes.status).toBe(200);
    const { tasks } = await pullRes.json();
    expect(tasks).toHaveLength(1);
    expect(tasks[0].id).toBe(id);
    expect(tasks[0].text).toBe('ship it');

    // 3. Ack — should delete the row
    const ackRes = await worker.fetch(
      req('POST', '/ack', { token: DRAIN, body: { ids: [id] } }),
      env, {},
    );
    expect(ackRes.status).toBe(200);
    const { deleted } = await ackRes.json();
    expect(deleted).toBe(1);

    // 4. Pull again — should be empty
    const pullRes2 = await worker.fetch(req('GET', '/pull', { token: DRAIN }), env, {});
    const { tasks: tasks2 } = await pullRes2.json();
    expect(tasks2).toHaveLength(0);
  });

  it('/pull does NOT delete rows', async () => {
    const env = makeEnv(db);
    await worker.fetch(
      req('POST', '/enqueue', { token: ENQUEUE, body: { text: 'persist me' } }),
      env, {},
    );
    // Pull twice — both should return the row.
    const r1 = await worker.fetch(req('GET', '/pull', { token: DRAIN }), env, {});
    const r2 = await worker.fetch(req('GET', '/pull', { token: DRAIN }), env, {});
    const { tasks: t1 } = await r1.json();
    const { tasks: t2 } = await r2.json();
    expect(t1).toHaveLength(1);
    expect(t2).toHaveLength(1);
  });
});

// ---------------------------------------------------------------------------
// POST /deadletter — sets status and appears in GET /deadletter
// ---------------------------------------------------------------------------

describe('deadletter', () => {
  beforeEach(freshDB);

  it('moves row to deadletter and lists it', async () => {
    const env = makeEnv(db);

    // Enqueue
    const enqRes = await worker.fetch(
      req('POST', '/enqueue', { token: ENQUEUE, body: { text: 'bad task' } }),
      env, {},
    );
    const { id } = await enqRes.json();

    // Deadletter it
    const dlRes = await worker.fetch(
      req('POST', '/deadletter', { token: DRAIN, body: { ids: [id], reason: 'invalid client' } }),
      env, {},
    );
    expect(dlRes.status).toBe(200);

    // Should NOT appear in /pull (status no longer pending)
    const pullRes = await worker.fetch(req('GET', '/pull', { token: DRAIN }), env, {});
    const { tasks } = await pullRes.json();
    expect(tasks).toHaveLength(0);

    // Should appear in GET /deadletter
    const listRes = await worker.fetch(req('GET', '/deadletter', { token: DRAIN }), env, {});
    expect(listRes.status).toBe(200);
    const { tasks: dlTasks } = await listRes.json();
    expect(dlTasks).toHaveLength(1);
    expect(dlTasks[0].id).toBe(id);
    expect(dlTasks[0].reason).toBe('invalid client');
  });

  it('DELETE /deadletter removes the row', async () => {
    const env = makeEnv(db);

    const enqRes = await worker.fetch(
      req('POST', '/enqueue', { token: ENQUEUE, body: { text: 'remove me' } }),
      env, {},
    );
    const { id } = await enqRes.json();

    await worker.fetch(
      req('POST', '/deadletter', { token: DRAIN, body: { ids: [id], reason: 'test' } }),
      env, {},
    );

    const delRes = await worker.fetch(
      req('DELETE', '/deadletter', { token: DRAIN, body: { ids: [id] } }),
      env, {},
    );
    expect(delRes.status).toBe(200);
    const { deleted } = await delRes.json();
    expect(deleted).toBe(1);

    const listRes = await worker.fetch(req('GET', '/deadletter', { token: DRAIN }), env, {});
    const { tasks } = await listRes.json();
    expect(tasks).toHaveLength(0);
  });

  it('/ack on a deadletter row does not delete it', async () => {
    const env = makeEnv(db);

    const enqRes = await worker.fetch(
      req('POST', '/enqueue', { token: ENQUEUE, body: { text: 'sticky' } }),
      env, {},
    );
    const { id } = await enqRes.json();

    await worker.fetch(
      req('POST', '/deadletter', { token: DRAIN, body: { ids: [id], reason: 'test' } }),
      env, {},
    );

    // ack targets status='pending', so it should not touch the deadletter row
    const ackRes = await worker.fetch(
      req('POST', '/ack', { token: DRAIN, body: { ids: [id] } }),
      env, {},
    );
    expect(ackRes.status).toBe(200);
    const { deleted } = await ackRes.json();
    expect(deleted).toBe(0);

    const listRes = await worker.fetch(req('GET', '/deadletter', { token: DRAIN }), env, {});
    const { tasks } = await listRes.json();
    expect(tasks).toHaveLength(1);
  });
});

// ---------------------------------------------------------------------------
// /pull limit param
// ---------------------------------------------------------------------------

describe('GET /pull limit', () => {
  beforeEach(async () => {
    freshDB();
    const env = makeEnv(db);
    for (let i = 0; i < 5; i++) {
      await worker.fetch(
        req('POST', '/enqueue', { token: ENQUEUE, body: { text: `task ${i}` } }),
        env, {},
      );
    }
  });

  it('respects ?limit=2', async () => {
    const env = makeEnv(db);
    const res = await worker.fetch(req('GET', '/pull?limit=2', { token: DRAIN }), env, {});
    const { tasks } = await res.json();
    expect(tasks).toHaveLength(2);
  });

  it('returns all rows when limit > count', async () => {
    const env = makeEnv(db);
    const res = await worker.fetch(req('GET', '/pull?limit=100', { token: DRAIN }), env, {});
    const { tasks } = await res.json();
    expect(tasks).toHaveLength(5);
  });
});

// ---------------------------------------------------------------------------
// 404 for unknown routes
// ---------------------------------------------------------------------------

describe('unknown routes', () => {
  beforeEach(freshDB);

  it('returns 404', async () => {
    const env = makeEnv(db);
    const res = await worker.fetch(new Request('https://worker/unknown'), env, {});
    expect(res.status).toBe(404);
  });
});
