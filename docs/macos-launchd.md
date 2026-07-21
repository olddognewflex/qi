# Running `qid` under launchd on macOS

This guide covers running the `qid` daemon as a background launchd agent on
macOS, and the one-time code-signing setup that keeps its privacy grant alive
across rebuilds. It is macOS-specific; on Linux/systemd none of the TCC issues
below apply.

Relevant when you use the opt-in watcher (`[sync] watch = true`) or the morning
notifier (`[notify] due_today = true`) — the features that need `qid` running
continuously — and your vault lives under a TCC-protected location such as
`~/Documents`, `~/Desktop`, or iCloud Drive.

## The problem: TCC grants die on every rebuild

macOS gates access to protected folders (`~/Documents`, …) behind TCC
(Transparency, Consent, and Control). A launchd-managed `qid` that reaches a
vault under one of those folders needs a **Files and Folders** or **Full Disk
Access** grant.

TCC identifies an *ad-hoc* (unsigned) binary by its **cdhash** — a hash of the
executable's code. `go build` produces a different cdhash every time, so:

> **every `go build -o ~/.local/bin/qid` orphans the existing grant.**

After a rebuild, the next launchd start of `qid` blocks in `open(2)` awaiting
`tccd`, with no prompt a background agent can show. The symptom is subtle:
`qid` serves fine, but the **watcher never starts**, so auto-reconcile +
incremental FTS indexing silently degrade to "manual only" until you re-grant
in System Settings. (Before the watcher was moved to its own goroutine, an
unsigned rebuild could wedge the whole daemon pre-`Serve` while the socket
still looked healthy — see issue #47.)

`qi doctor` surfaces this: with `qid` alive it asks the daemon whether the
watcher actually started, and reports `[warn] qid watcher — blocked` with a
"grant Full Disk Access" hint — something a socket dial alone provably cannot
detect.

## The fix: sign with a stable identity

Signing `qid` (and `qi`, `qi-mcp`) with a **stable code-signing identity** gives
TCC a *designated requirement* keyed to the certificate rather than the cdhash.
The grant then survives rebuilds, because every rebuild re-signs with the same
identity.

### 1. Create a self-signed code-signing certificate (one time)

In **Keychain Access**:

1. Menu: **Keychain Access ▸ Certificate Assistant ▸ Create a Certificate…**
2. **Name:** something memorable, e.g. `qi-local`.
3. **Identity Type:** Self Signed Root.
4. **Certificate Type:** **Code Signing**.
5. Create it, leaving it in the **login** keychain.

The certificate name (`qi-local` here) is your `QI_SIGN_IDENTITY`. Verify it is
usable for signing:

```bash
security find-identity -v -p codesigning
```

You should see `qi-local` listed.

### 2. Build and install signed

```bash
make install QI_SIGN_IDENTITY="qi-local"
```

`make install` builds all three binaries, installs them to `$(PREFIX)/bin`
(default `~/.local/bin`), signs them with your identity, and runs
`codesign --verify --strict` to fail fast if the identity is wrong. Re-run the
exact same command after any code change — the cdhash changes but the signing
identity does not, so the TCC grant holds.

Without `QI_SIGN_IDENTITY`, `make install` still works but prints a note that the
install is unsigned and the grant will break on the next rebuild.

Confirm the signature and its authority:

```bash
codesign -dv --verbose=2 ~/.local/bin/qid 2>&1 | grep -E 'Authority|Identifier'
```

### 3. Grant Files-and-Folders / Full Disk Access (one time)

The first launchd start of the signed `qid` that touches the vault will (via
`tccd`) surface a grant request. If a background agent cannot prompt, add it
manually:

- **System Settings ▸ Privacy & Security ▸ Full Disk Access** (or **Files and
  Folders**) ▸ add `~/.local/bin/qid`.

Because `qid` is now signed with a stable identity, this grant persists across
rebuilds. You should only ever do it once.

## Running qid as a launchd agent (example)

qi does **not** ship or manage a `qid` launchd plist — this is a manual,
user-owned setup (issue #47 keeps plist management out of scope). A minimal
`KeepAlive` agent looks like:

```xml
<!-- ~/Library/LaunchAgents/com.example.qid.plist -->
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>          <string>com.example.qid</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/YOU/.local/bin/qid</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>QI_VAULT_PATH</key><string>/Users/YOU/path/to/vault</string>
    </dict>
    <key>KeepAlive</key>      <true/>
    <key>RunAtLoad</key>      <true/>
    <key>StandardOutPath</key><string>/Users/YOU/.local/state/qi/qid.log</string>
    <key>StandardErrorPath</key><string>/Users/YOU/.local/state/qi/qid.log</string>
</dict>
</plist>
```

Load / unload / restart it:

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.example.qid.plist
launchctl bootout    gui/$(id -u)/com.example.qid       # stop and keep it down
launchctl kickstart -k gui/$(id -u)/com.example.qid     # restart in place
```

### Interaction with `qi daemon stop`

Under a `KeepAlive` agent, launchd owns `qid`'s lifecycle. `qi daemon stop`
shuts the current process down but launchd relaunches it, so `stop` cannot keep
`qid` down and `restart` cannot choose its binary. `qi daemon stop` detects a
loaded `qid` agent and prints the `launchctl bootout` / `kickstart` remedy
instead of a misleading "qid stopped" (issue #69). To actually stop a supervised
`qid`, `bootout` the agent; to restart it on a freshly-signed binary,
`kickstart -k`.

## Verifying the whole setup

```bash
qi doctor
```

- `[ok  ] qid watcher — watching N dirs` — the grant is in place and the
  watcher started.
- `[warn] qid watcher — blocked` with a Full Disk Access hint — the grant is
  missing or was orphaned by an unsigned rebuild; re-grant, or re-`make install`
  with `QI_SIGN_IDENTITY` and then re-grant once.
- `[ok  ] qid binary — current` vs `stale` — whether the running daemon is the
  binary a restart would start.
