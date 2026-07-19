# Remote capture setup (iPhone → vault)

How to capture tasks (and notes/captures) into the qi vault from an iPhone, even
while the laptop is offline. This is the operator's guide for the design in
[`cloud-queue-spec.md`](./cloud-queue-spec.md); the Cloudflare Worker lives in
[`../worker/`](../worker/README.md).

```
iPhone Shortcut ──POST /enqueue (ENQUEUE_TOKEN)──▶ Worker ──mints qi-id──▶ D1 queue
                                                                              │
laptop launchd timer (every 5 min) ── qi remote-drain ──GET /pull (DRAIN_TOKEN)┘
   validate → idempotent write → ^qi-xxxxxxxx into vault/10-tasks/*.md → POST /ack
```

The queue is **inbound-only**: remote → cloud → laptop. Tasks created on the
laptop never touch it. The Worker mints the canonical `qi-xxxxxxxx` id at enqueue
time (byte-identical to `vault.MintID`), so the id the phone gets back is the same
`^qi-id` block ref that lands in the markdown — no id reconciliation needed, and a
re-drained task is idempotent on that id.

Prerequisite: the Worker is deployed and `[remote_queue]` is configured in
`~/.config/qi/config.toml`. See [`../worker/README.md`](../worker/README.md) for
the deploy sequence and the two-token model.

---

## Part 1 — the laptop drain timer (launchd)

`qi remote-drain` is a periodic one-shot: it pulls pending rows, writes the valid
ones into the vault, acks what it wrote, and deadletters what failed. A launchd
`StartInterval` timer runs it every 5 minutes (and once on login/wake, catching up
firings missed while the Mac slept).

### Token handling

The DRAIN token is **not** stored in the plist. A LaunchAgent plist in
`~/Library/LaunchAgents` is `0644` (world-readable on a multi-user Mac), so the
token — and the vault path — are read from `~/.config/qi/config.toml`, which is
`0600`. This is why the template deliberately has no `EnvironmentVariables` block
(cloud-queue-spec §2e). Confirm the config perms:

```sh
chmod 600 ~/.config/qi/config.toml
stat -f '%Sp %N' ~/.config/qi/config.toml   # -> -rw------- .../config.toml
```

`config.toml` must carry `vault_path` and, under `[remote_queue]`, `enabled`,
`url`, and `token` (the DRAIN token). Alternatively export `QI_QUEUE_TOKEN` /
`QI_VAULT_PATH` — but launchd does not load your shell/direnv environment, so if
you go the env route you must add an `EnvironmentVariables` block to the plist
yourself. The config-file route is simpler and keeps the token off a
world-readable file.

### Install

The template is [`../deploy/launchd/com.olddognewflex.qi-drain.plist`](../deploy/launchd/com.olddognewflex.qi-drain.plist).
launchd does not expand `~` or `$HOME`, so the `__HOME__` tokens must be replaced
with an absolute path at install time:

```sh
cd /path/to/qi   # repo root
DST=~/Library/LaunchAgents/com.olddognewflex.qi-drain.plist

sed "s|__HOME__|$HOME|g" deploy/launchd/com.olddognewflex.qi-drain.plist > "$DST"
plutil -lint "$DST"                                  # -> OK

launchctl bootout   gui/$(id -u)/com.olddognewflex.qi-drain 2>/dev/null || true
launchctl bootstrap gui/$(id -u) "$DST"
```

The template assumes the `qi` binary is at `~/.local/bin/qi`. If yours lives
elsewhere (e.g. `~/go/bin/qi`), edit the first `ProgramArguments` entry in the
template before the `sed`, or adjust `$DST` after.

### Verify

```sh
# Force a run now and check it exited 0.
launchctl kickstart -k gui/$(id -u)/com.olddognewflex.qi-drain
sleep 6
launchctl list com.olddognewflex.qi-drain | grep LastExitStatus   # -> = 0;

# Confirm the queue is reachable.
qi remote-status                                     # -> pending N, deadletter M
tail -n 3 ~/.local/state/qi/drain.log                # -> "drained X, rejected Y"
```

### Uninstall

```sh
launchctl bootout gui/$(id -u)/com.olddognewflex.qi-drain
rm ~/Library/LaunchAgents/com.olddognewflex.qi-drain.plist
```

### Troubleshooting

- **`launchctl list` shows `LastExitStatus = 78` (EX_CONFIG).** That field is
  sticky — it records the last *abnormal* exit, not the last exit. Check the real
  cause in `~/.local/state/qi/drain.err.log`. The most common one is a transient
  `GET /pull ... context deadline exceeded` (the Worker was briefly slow or the
  laptop's network dropped). This is expected and self-healing: the drain exits
  non-zero, nothing is written, and the next interval retries. A run that later
  succeeds resets `LastExitStatus` to `0`. Only investigate if `drain.err.log`
  shows a *fresh, repeating* error.
- **`remote queue disabled ... nothing to drain`.** `[remote_queue].enabled` is
  false (or `QI_QUEUE_ENABLED` unset). Enable it in `config.toml`.
- **`token is empty` / 401 / 403 in the err log.** The DRAIN token in
  `config.toml` is missing or wrong. A 403 specifically means you pasted the
  *enqueue* token where the drain token belongs — they are scoped separately.
- **Rows stuck in deadletter.** Review with `qi remote-drain --show-failed`. A bad
  `client`/`project` from the phone is validated at drain and deadlettered (never
  silently dropped), so fix the Shortcut's field and purge the row via the
  Worker's `DELETE /deadletter`.

---

## Part 2 — the iOS Shortcut

Build this once in the **Shortcuts** app on the iPhone. It captures text, POSTs it
to the Worker's `/enqueue` route with the ENQUEUE token, and shows the returned id.

### The enqueue contract

- **Method / URL:** `POST https://tasks.qi-queue.workers.dev/enqueue`
  (use your own Worker URL if different — the `url` in `config.toml`, but the
  phone hits `/enqueue`, not `/pull`).
- **Headers:**
  - `Authorization: Bearer <ENQUEUE_TOKEN>`
  - `Content-Type: application/json`
- **Body (JSON):**

  ```json
  { "text": "buy milk", "project": "home", "source": "ios-shortcut" }
  ```

  - `text` — **required**, non-empty, no control characters.
  - `project` — optional free-form tag (`[A-Za-z0-9_-/]`), becomes the task's
    first `#tag`. Omit it for an untagged task.
  - `client` — optional, but **mutually exclusive with `project`** (send one or
    neither, never both). Must match a configured client name or the row
    deadletters at drain.
  - `kind` — optional, one of `task` (default) / `note` / `capture`. Omit for a
    task. Note/capture are not deduplicated on re-drain, so prefer `task`.
  - `due` / `scheduled` — optional `YYYY-MM-DD`.
  - `source` — optional audit label; use `"ios-shortcut"`.
- **Response `201`:** `{ "id": "qi-xxxxxxxx" }`. The id is informational — the
  laptop is authoritative — but handy to confirm the capture landed.

> **Where the token goes:** the `<ENQUEUE_TOKEN>` above is the phone-scoped secret
> you set with `wrangler secret put ENQUEUE_TOKEN`. Paste that value into the
> Authorization header in the Shortcut (step 4 below). Do **not** commit it or put
> it in any repo file. Anyone with your unlocked phone can read it; rotate it with
> `wrangler secret put ENQUEUE_TOKEN` if the phone is lost — that does not affect
> the laptop's DRAIN token.

### Build it — action by action

1. **New Shortcut**, name it e.g. *"qi capture"*.
2. Add **Ask for Input**
   - *Input Type:* **Text**
   - *Prompt:* `Task?`
   - (This is the task text. To also tag a project, add a second **Ask for Input**
     with prompt `Project? (optional)` — or skip it and hardcode nothing.)
3. Add **Get Contents of URL**
   - *URL:* `https://tasks.qi-queue.workers.dev/enqueue`
   - Tap **Show More**.
   - *Method:* **POST**
   - *Headers:* add two —
     - `Authorization` → `Bearer <ENQUEUE_TOKEN>` (paste your enqueue token in
       place of `<ENQUEUE_TOKEN>`, keeping the word `Bearer` and one space)
     - `Content-Type` → `application/json`
   - *Request Body:* **JSON**, then add fields:
     - `text` (Text) → tap the value and insert the **Provided Input** / **Ask for
       Input** variable from step 2.
     - `project` (Text) → the project variable, if you added one. Otherwise omit
       this field entirely.
     - `source` (Text) → `ios-shortcut`
4. Add **Get Dictionary Value**
   - *Get:* **Value for** `id`
   - *in:* **Contents of URL** (the output of step 3)
5. Add **Show Notification** (or **Show Result**)
   - *Text:* `Captured `, then insert the **Dictionary Value** from step 4.

Run it: you should get a notification like `Captured qi-1a2b3c4d`. Confirm the
round trip on the laptop within ~5 minutes (or force it):

```sh
qi remote-status    # pending should tick up, then back to 0 after the drain
qi task list        # the task appears with its ^qi-id
```

### Share Sheet variant (capture from any app)

Extend the same *qi capture* Shortcut so it works both ways: launched directly
(prompts for text) **and** from the Share Sheet (uses the selected text/link).
The trick is to funnel both input sources into one variable the POST references.

1. **Enable Share Sheet input.** Open the Shortcut → tap **ⓘ** (settings) →
   enable **Show in Share Sheet** → tap **Share Sheet Types** and turn off
   everything except **Text** (add **URLs** if you want to capture links too).

2. **Branch on the launch source.** At the **top** of the actions (above the
   existing *Ask for Input*), add an **If** action set to:
   *Shortcut Input* **has any value**. You now have `If / Otherwise / End If`.

3. **Feed both branches into one variable** named `taskText`:
   - **If** branch (came from the Share Sheet): add **Set Variable** →
     `taskText` = **Shortcut Input**.
   - **Otherwise** branch (launched directly): move the existing **Ask for
     Input** here, then add **Set Variable** → `taskText` = that **Provided
     Input**.

4. **Point the request at `taskText`.** In **Get Contents of URL**, set the JSON
   `text` field's value to the **`taskText`** variable (replacing the direct
   *Ask for Input* reference).

The final action order:

```
If  [Shortcut Input]  has any value
    Set Variable  taskText = Shortcut Input
Otherwise
    Ask for Input  (Text, "Task?")
    Set Variable  taskText = Provided Input
End If
Get Contents of URL   (POST /enqueue, text = taskText)
Get Dictionary Value  id  in  Contents of URL
Show Notification     "Captured " + id
```

5. **Use it.** In any app, select text (or a link) → **Share** → **qi capture**.
   The selection is enqueued and drained into the vault with a proper id, no
   prompt. Launching the Shortcut directly still prompts as before.

> Referencing **Shortcut Input** directly in the body (skipping `taskText`) works
> from the Share Sheet but sends an empty `text` on a direct launch — the
> variable is what makes one Shortcut serve both paths.

### Notes & limits

- **Double-taps duplicate.** Two runs = two rows = two ids = two tasks
  (phone-side idempotency is out of scope). Tasks are otherwise safe against
  *drain-side* re-processing.
- **Plaintext in the cloud, briefly.** Task text sits in D1 until the next drain
  deletes it (delete-on-ack). Don't put secrets in task text.
- **Offline of the laptop is fine**; offline of the *phone* is not — the Shortcut
  needs the phone's own internet to reach the Worker, and errors if it can't
  (nothing is queued locally on the phone).
