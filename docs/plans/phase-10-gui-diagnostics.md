# Phase 10 — GUI hardening: diagnostics, output capture, panic, install/uninstall, about, logs

## Note on how this plan was produced

A connection error truncated the session that originally drafted and critiqued
this plan; the prose of that original plan and the reviewer's six-item critique
did not survive in recoverable session state — only tool-call artifacts did
(a repo-tree listing and greps of the current `macos-gui/` Swift sources). This
document was reconstructed from those artifacts plus the concrete resolutions
the follow-up instruction specified verbatim (drop the new `config apply` Go
command in favor of the existing privileged `config set`; seed the VPN panel
from the raw on-disk config; make an explicit, non-silent call on the
stop→start fail-open restart window). Those three are addressed directly below
and in [Phase 11](./phase-11-gui-vpn-config.md). The other three critique items
are not recoverable verbatim — rather than guess at invented text, this plan
applies the same standard of scrutiny to every surface the critique's known
concerns imply (new privileged surface area, duplicated validation logic,
silently-swallowed failure modes) and calls out where judgment calls were made
so they're easy to challenge if they miss the mark.

## Background

[Phase 8](./phase-8-macos-gui.md) shipped the menubar app: live status via the
daemon's `state.json`, Start/Stop/Block/Unblock, a login item, and a scope note
deferring in-app VPN config editing. Auditing the current
`macos-gui/Sources/DezhbanMenu/` against that scope note surfaces gaps that
have nothing to do with VPN mode and can ship on their own:

- **`DezhbanCLI.runPrivileged` discards all output.** It returns a bare `Bool`
  (`DezhbanCLI.swift:28`); on failure the user gets silence — no stderr, no
  exit code, nothing to paste into a bug report.
- **No Panic control in the menu.** The daemon has a `panic` subcommand
  specifically for "remove rules even with no daemon running" emergencies
  (see `CLAUDE.md` rules-that-must-not-be-broken), but the GUI has no button
  for it.
- **No install/uninstall from the app.** Today that's `scripts/install-local.sh`
  / `uninstall-local.sh` only; someone who launches `Dezhban.app` without
  having run those has no in-app path to register the service.
- **"About" is inert menu text**, not a real panel — no version, no build info,
  no resolved paths.
- **"View logs…" just opens `Console.app`** (`AppDelegate.swift:215`) with no
  filter — the user lands in the full unified-log firehose and has to
  hand-write a predicate for `process == "dezhban"` themselves.

None of this requires touching the daemon, the config schema, or VPN mode — it
is a GUI-only, additive change, so it ships as a self-contained phase before
[Phase 11](./phase-11-gui-vpn-config.md)'s config panel, which does need daemon
coordination (restart windows, validation).

## Design decisions

| Decision | Choice | Rationale |
|---|---|---|
| Output capture | `runPrivileged` returns `(status, stdout, stderr)` instead of `Bool` | Every action (existing and new) can show the user what actually happened, not just pass/fail |
| Panic / install / uninstall | Thin wrappers over the **existing** `dezhban panic` / `install` / `uninstall` / `start` CLI subcommands | No new Go surface, no new privileged logic to audit — the GUI is a front end over what the scripts already call |
| Install/uninstall gating | Read installed/registered state from `dezhban status --json` (already merges service status per Phase 8) | Avoids a second, GUI-side notion of "is this installed" that can drift from the CLI's |
| About panel | Real window: `dezhban version`, resolved config path, binary path, service status | Replaces static `addInfo` menu lines that carry no live data |
| Logs | Shell to `/usr/bin/log show`/`log stream` with a `process == "dezhban"` predicate, captured into the same output panel as diagnostics | Reuses one output-capture code path instead of building a second, log-specific viewer; no new dependency (stdlib `Process` call to a system binary already used by `DezhbanCLI`) |

## What this phase builds

### `DezhbanCLI.swift` — output capture (foundational for everything else)

- Change `runPrivileged(_:) -> Bool` to `runPrivileged(_:) -> (ok: Bool, output: String)`,
  capturing `do shell script` output (AppleScript's `executeAndReturnError`
  return value holds stdout on success; the `NSDictionary` error info holds
  the failure message on non-zero exit) instead of only checking whether
  `errInfo == nil`.
- Add an unprivileged capture helper (the private `exec` already in the file
  is unprivileged and already captures output — promote it to `internal`/reuse
  it directly for the new read-only commands below instead of writing a
  second exec wrapper).
- All existing call sites (Start/Stop/Block/Unblock) get the captured output
  threaded into a failure alert instead of silently no-op'ing.

### New menu items, all reusing the capture plumbing above

- **"Run diagnostics…"** → unprivileged `dezhban doctor --config <resolvedConfigPath>`
  (already read-only, no-root per `CLAUDE.md`) → output panel.
- **"Panic — force unblock"** → confirmation dialog ("This immediately removes
  all dezhban firewall rules, including VPN-guard rules. Continue?") →
  privileged `dezhban panic` → output panel. Confirmation is required because,
  unlike Block/Unblock, panic is explicitly the last-resort override and
  should never fire on a stray click.
- **"Install service" / "Uninstall service"** — mutually exclusized on
  `status --json`'s service-installed flag: privileged `dezhban install
  --config <resolvedConfigPath>` + `dezhban start`, or `dezhban panic` (rules
  teardown first, matching `uninstall-local.sh`'s ordering and its comment
  about macOS launchd unload sometimes failing) + `dezhban stop` + `dezhban
  uninstall` → output panel.
- **"About Dezhban…"** — a small panel: `dezhban version` output, resolved
  config path, binary path (`DezhbanCLI.binaryPath()`), current service status
  from the last-read snapshot. Read-only, no new CLI calls beyond `version`
  (everything else is already fetched for the main menu).
- **"View logs…"** replaced with a submenu: **"Show last hour"** (unprivileged
  `/usr/bin/log show --last 1h --predicate 'process == "dezhban"'` → output
  panel) and **"Stream live…"** (`/usr/bin/log stream --predicate 'process ==
  "dezhban"'`, piped into the output panel with a Stop button that terminates
  the child process — the one place this phase needs a cancellable running
  process rather than a run-to-completion capture). Keep the old "Open in
  Console.app" as a secondary item for anyone who wants the full app instead
  of the in-menu panel.

### Output panel

One shared `NSWindow` (or `NSAlert` with an expanded/scrollable accessory view
for short output, escalating to a real window for `log stream`'s unbounded
output) that every action above feeds text into. Built once, reused — not a
bespoke window per action.

## Explicitly out of scope for this phase

- Anything that edits `vpn.*` or other config fields — that is
  [Phase 11](./phase-11-gui-vpn-config.md) entirely.
- A persistent/tailing log view that survives app relaunch — "Stream live…"
  runs only while its window is open.
- Code signing / notarization / a privileged helper (unchanged from Phase 8's
  out-of-scope list).

## Acceptance / verification

**Go:** unaffected — this phase touches no Go code. `go build ./...` / `go vet
./...` / `go test ./...` should be a no-op diff-wise; run them anyway as a
regression check.

**App (macOS 13+, Swift toolchain):**
```bash
make gui-macos && open dist/Dezhban.app
```
- **Output capture:** trigger a Start/Stop while the CLI binary is
  intentionally moved aside (or the config is invalid) and confirm the failure
  alert shows real stderr, not silence.
- **Diagnostics:** "Run diagnostics…" shows `doctor` output matching `./dezhban
  doctor --config ...` run by hand.
- **Panic:** confirmation dialog appears; confirming removes rules (verify via
  `sudo ./dezhban status` showing unblocked / no dezhban rules); cancelling
  does nothing.
- **Install/uninstall:** on a host with no service registered, "Install
  service" appears and "Uninstall service" is hidden/disabled (or vice versa);
  after installing, `dezhban status --json` shows it registered and the menu
  flips.
- **About:** version matches `dezhban version`; paths match
  `dezhban config path` / `DezhbanCLI.binaryPath()`.
- **Logs:** "Show last hour" output matches a hand-run `log show --last 1h
  --predicate 'process == "dezhban"'`; "Stream live…" updates live while
  `dezhban run` logs something, and its Stop button ends the child process
  (verify no orphaned `log stream` in `ps` after closing the window).
