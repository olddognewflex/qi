-- qi-queue D1 schema
--
-- Apply with:
--   wrangler d1 execute qi-queue --file=./schema.sql
-- Or in non-interactive mode (CI / first deploy):
--   wrangler d1 execute qi-queue --file=./schema.sql --remote
--
-- Schema is idempotent (CREATE TABLE IF NOT EXISTS) so it is safe to re-run.

CREATE TABLE IF NOT EXISTS queue (
  id          TEXT PRIMARY KEY,               -- qi-xxxxxxxx (8 lowercase hex), minted by the Worker
  text        TEXT NOT NULL,
  kind        TEXT NOT NULL DEFAULT 'task',    -- 'task' | 'note' | 'capture'; drives drain routing
  project     TEXT,                           -- free-form tag, or NULL (tasks only)
  client      TEXT,                           -- configured client name, or NULL
  due         TEXT,                           -- 'YYYY-MM-DD' or NULL
  scheduled   TEXT,                           -- 'YYYY-MM-DD' or NULL
  source      TEXT,                           -- 'ios-shortcut' etc. (audit only)
  created_at  INTEGER NOT NULL,               -- unix seconds (Math.floor(Date.now()/1000))
  reason      TEXT,                           -- deadletter reason, NULL while pending
  status      TEXT NOT NULL DEFAULT 'pending' -- 'pending' | 'deadletter'
);

-- Primary query pattern: fetch pending rows oldest-first.
CREATE INDEX IF NOT EXISTS idx_queue_status ON queue(status, created_at);
