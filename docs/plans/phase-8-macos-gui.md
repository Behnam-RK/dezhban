# Phase 8 — macOS menubar GUI

## Background

Through Phase 7 dezhban is CLI-only: the running daemon's posture is visible
only by running a command. This phase adds a **macOS-only menubar app**
(`Dezhban.app`) that shows live state at a glance, offers click-to-control, and
auto-launches at login — without compromising the Go binary's zero-dependency,
`CGO_ENABLED=0` promise.

**Design decisions:**

| Decision | Choice | Rationale |
|---|---|---|
| GUI language | **Swift / AppKit**, separate target | Keeps the Go binary dependency-free and CGO-off; native menubar |
| GUI ↔ daemon IPC | **Daemon writes a JSON state file** | Single source of truth = exactly what the daemon decided; stdlib-only, no second poller |
| Build | **SwiftPM + bundling script** (not Xcode project) | Builds from Command Line Tools alone (no full Xcode); `xcodebuild` needs Xcode |
| Privilege escalation | **`osascript` admin prompt** | No bundled helper / code-signing for the MVP; macOS caches auth ~5 min |
| App autostart | **`SMAppService.mainApp`** (macOS 13+) | Modern login item, no LaunchAgent plist to ship |

The kill-switch daemon's own boot autostart (launchd `RunAtLoad`, Phase 6) is
unchanged — the app's login item is a separate, per-user concern.

## What was built

### Daemon (Go) — live state export

- **`internal/state`** — `Snapshot` type (posture, IP, country, provider, tunnels,
  endpoints, mode, blocked countries, PID, timestamp) with an **atomic** writer
  (temp-file + rename, `0644` world-readable so the unprivileged user can read
  what the root daemon wrote) and a reader.
- **`internal/runner`** — an injected `Publish func(state.Snapshot)` (nil = no-op,
  same pattern as `Allowlist`/`ResolveEndpoints`). The loop publishes a full
  snapshot on every poll, verdict transition, tunnel edge, endpoint refresh, and
  at startup — carrying the last-known reading so tunnel/endpoint events don't
  blank the IP/country. `postureName` maps (mode, blocked) → `allow`/`block`/
  `guard`/`full-block`.
- **`cmd/dezhban`** — `defaultStatePath()` (`/var/db/dezhban/state.json` on unix,
  `%ProgramData%\dezhban\state.json` on Windows); wires `Publish` into the real
  `run` path only (dry-run/inspect leave it nil); adds **`status --json`** merging
  the live snapshot with service/config status. State-write failures are logged at
  debug and **never affect enforcement**.

The state export is cross-platform and stdlib-only — useful beyond macOS.

### App (Swift) — `macos-gui/`

- SwiftPM executable `DezhbanMenu` (AppKit + ServiceManagement only).
- **`AppDelegate`** — `NSStatusItem` with an SF Symbol icon (🟢 `shield.fill`
  allow/guard, 🔴 `shield.slash.fill` block/full-block, ⚪ `shield` stopped),
  tinted via `contentTintColor`. A 1 s timer reads the state file and repaints;
  the menu is rebuilt from the current snapshot on open.
- **`Snapshot`** — `Codable` mirror of the Go type; RFC3339 date decoding with and
  without fractional seconds.
- **`DezhbanCLI`** — binary path resolution (`/usr/local/bin`, `/opt/homebrew/bin`,
  `$PATH`), unprivileged `run`, and `runPrivileged` via `osascript`.
- **`LoginItem`** — `SMAppService.mainApp` register/unregister + status.
- Menu: status detail · Start/Stop · Block/Unblock · VPN-mode indicator +
  Open config · View logs · Launch at login · Quit. Items enable/disable from the
  current snapshot (e.g. Unblock only when blocked).
- **`build-app.sh`** / `make gui-macos` — builds and assembles
  `dist/Dezhban.app` (`LSUIElement=true`, menubar agent).

### Scope note

**VPN-mode live toggling is deferred.** Enabling `vpn.enabled` safely requires
tunnel/endpoint fields the user must set deliberately (a blind flip fails
`config.Validate()` and stops the daemon). The menu shows the current mode with a
checkmark and routes to **Open config…**; a validated in-app toggle (write
candidate → `dezhban validate` → elevate → restart) is a follow-up.

## Acceptance / verification

**Go (no root):** `go build ./...` · `go vet ./...` · `go test ./...`
(`internal/state` round-trip/perms/atomicity; `internal/runner` posture
transitions publish).

**Daemon end-to-end (root, safe — simulate flags, no real block needed):**
```bash
sudo ./dezhban run --config configs/dezhban.dev.json --simulate-country IR &
cat /var/db/dezhban/state.json   # posture "block", IP/country populated
./dezhban status --json          # snapshot merged with service status
```

**App (macOS 13+, Swift toolchain):**
```bash
make gui-macos && open dist/Dezhban.app
```
- Menubar icon appears; drive the daemon with `--simulate-country IR`/`US` and
  confirm 🔴/🟢 flip and menu detail update within ~1 s.
- Kill the daemon → ⚪ after the 90 s staleness window.
- Start/Stop and Block/Unblock → native admin prompt, action runs, state reflects.
- Toggle "Launch at login" → `SMAppService.mainApp.status == .enabled`; app
  relaunches after logout/login.

## Out of scope

- Code signing / notarization (runs locally unsigned; Gatekeeper right-click-open).
- SMJobBless privileged helper (persistent auth, no repeated prompts).
- In-app VPN-mode toggle (see scope note).
