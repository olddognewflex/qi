# qi-queue — Cloudflare Worker + D1

A tiny queue API that lets the iPhone capture tasks to qi even when the laptop
is offline. The Worker is the cloud-side of the `qi remote-drain` flow
described in `docs/cloud-queue-spec.md`.

## Two-token auth model

| Token | Holder | Authorized routes |
|-------|--------|-------------------|
| `ENQUEUE_TOKEN` | iPhone / iOS Shortcut | `POST /enqueue` only |
| `DRAIN_TOKEN`   | Laptop (`qi remote-drain`) | `GET /pull`, `POST /ack`, `POST /deadletter`, `GET /deadletter`, `DELETE /deadletter` |

A leaked phone token cannot drain or delete the queue.
A missing or wrong token → 401. A valid token on the wrong scope → 403.

## Prerequisites

- Cloudflare account (free tier is more than enough)
- Node 18+
- `npm install` in this directory
- `wrangler login` (once, to authenticate your Cloudflare account)

## Deploy sequence

### 1. Create the D1 database

```sh
wrangler d1 create qi-queue
```

Copy the `database_id` from the output and paste it into `wrangler.toml`:

```toml
[[d1_databases]]
database_id = "<paste here>"
```

### 2. Apply the schema

```sh
npm run db:init
# expands to: wrangler d1 execute qi-queue --file=./schema.sql --remote
```

### 3. Set the two secrets

```sh
wrangler secret put ENQUEUE_TOKEN   # phone token — paste a secure random string
wrangler secret put DRAIN_TOKEN     # laptop token — different secure random string
```

Generate each with e.g. `openssl rand -hex 32`.

### 4. Deploy

```sh
wrangler deploy
```

Note the Worker URL printed at the end (`https://qi-queue.<subdomain>.workers.dev`).
Put that URL in `config.toml` under `[remote_queue].url`.

## Smoke tests

Replace `$URL`, `$ENQUEUE`, and `$DRAIN` with your actual values.

```sh
# Health (no auth)
curl $URL/healthz
# → ok

# Enqueue a task (phone token)
curl -s -X POST $URL/enqueue \
  -H "Authorization: Bearer $ENQUEUE" \
  -H "Content-Type: application/json" \
  -d '{"text":"test task from curl","source":"smoke-test"}' | jq
# → {"id":"qi-xxxxxxxx"}

# Pull pending tasks (laptop token)
curl -s "$URL/pull?limit=10" \
  -H "Authorization: Bearer $DRAIN" | jq
# → {"tasks":[{"id":"qi-xxxxxxxx","text":"test task from curl",...}]}

# Ack (delete) after drain
curl -s -X POST $URL/ack \
  -H "Authorization: Bearer $DRAIN" \
  -H "Content-Type: application/json" \
  -d '{"ids":["qi-xxxxxxxx"]}' | jq
# → {"deleted":1}

# Scope enforcement — drain token rejected on /enqueue
curl -s -X POST $URL/enqueue \
  -H "Authorization: Bearer $DRAIN" \
  -H "Content-Type: application/json" \
  -d '{"text":"should be rejected"}' | jq
# → 403 {"error":"token not authorized for this route"}

# Deadletter (manually move a row if drain-side validation fails)
curl -s -X POST $URL/deadletter \
  -H "Authorization: Bearer $DRAIN" \
  -H "Content-Type: application/json" \
  -d '{"ids":["qi-xxxxxxxx"],"reason":"unknown client"}' | jq

# List deadletter rows
curl -s $URL/deadletter \
  -H "Authorization: Bearer $DRAIN" | jq

# Delete (purge) a deadletter row after review
curl -s -X DELETE $URL/deadletter \
  -H "Authorization: Bearer $DRAIN" \
  -H "Content-Type: application/json" \
  -d '{"ids":["qi-xxxxxxxx"]}' | jq
```

## Local development

```sh
npm run dev
# wrangler dev — serves on http://localhost:8787
# Uses a local D1 replica; first run applies schema automatically from wrangler.toml.
```

## Running tests

```sh
npm test
# Runs vitest against test/helpers.test.js (pure helpers) and
# test/handler.test.js (full handler with in-process D1 mock via better-sqlite3).
# No Cloudflare account required.
```

For true D1 integration tests against the Workers runtime:

```sh
# Option A: manual smoke test against wrangler dev
wrangler dev &
curl http://localhost:8787/healthz

# Option B: vitest-pool-workers (requires @cloudflare/vitest-pool-workers;
#   not wired here to keep the setup lightweight — add it if needed)
```

## Optional: stale-row cron

Uncomment the `[triggers]` section in `wrangler.toml` to enable a daily cron
that moves `pending` rows older than 7 days to `deadletter`.
