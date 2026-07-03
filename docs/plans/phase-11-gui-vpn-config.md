# Phase 11 — GUI VPN config panel

Builds on [Phase 10](./phase-10-gui-diagnostics.md) (output capture, panic,
install/uninstall, about, logs) and closes the scope note left open by
[Phase 8](./phase-8-macos-gui.md): *"VPN-mode live toggling is deferred... a
validated in-app toggle (write candidate → `dezhban validate` → elevate →
restart) is a follow-up."* This is that follow-up, revised from an earlier
draft that proposed a new `dezhban config apply` Go subcommand — see
[Revisions from review](#revisions-from-review) below for why that was cut.

## Background

Today "Open config…" (`AppDelegate.swift:210`) just hands the raw JSON file to
the default app (`NSWorkspace.shared.open`). Flipping `vpn.enabled` safely
requires also setting `vpn.tunnelInterfaces` / `vpn.endpoints` correctly, or
`config.Validate()` rejects the result and the daemon won't start — exactly
the self-lockout risk the scope note flagged. A blind text-edit workflow gives
the user no guardrail against that.

`cmd/dezhban/config_cmd.go` already has everything the panel needs on the Go
side: a `configFields` map of dotted keys (`get`/`set` pairs, including all six
`vpn.*` fields) driving `dezhban config get/set/show/edit`, each `set` call
validating before it writes, and a `saveError` that gives a clear sudo hint on
`fs.ErrPermission`. `config set` writes to `writeTargetPath`, which resolves to
`defaultConfigPath()` (`/etc/dezhban/dezhban.json`) when nothing else is
specified — the same system path `install-local.sh` installs into. There is no
gap here that a new command needs to fill.

## Revisions from review

The instruction resuming this plan specified three concrete corrections,
applied as follows:

1. **Drop the new `config apply` Go command; use privileged `config set`
   against the system path instead.** The earlier draft proposed a
   `dezhban config apply <file>` command that would take a whole candidate
   config, validate it, and swap it in atomically — a second write path
   parallel to `config set`/`config edit`, with its own atomicity and
   validation story to build and audit. That's unnecessary: `config set`
   already validates-then-writes one field at a time, and the CLI already has
   two working precedents to copy (`configSet` for scripted single-field
   writes, `configEdit` for "seed unprivileged temp → edit → validate →
   privileged write" round trips). The panel drives `config set` once per
   changed field through the existing `runPrivileged` (Phase 10), so the Go
   side of this phase is **zero new commands** — pure reuse.
2. **Raw-file seeding.** The panel's initial values must come from
   `dezhban config show`'s output (the marshaled, already-`Normalize`d
   `config.Config`) — i.e., the same bytes `config.Marshal` produces — not
   from a second, hand-maintained Swift struct that mirrors the full config
   schema. The GUI already has one such mirror for `state.json`
   (`Snapshot.swift`, deliberately kept in sync with `internal/state`); adding
   a second full-schema mirror for the config would double the surface that
   drifts when a field is added on the Go side. So the panel treats config the
   same opaque, dotted-key way `configFields` does: it only ever reads/writes
   the specific keys it renders a control for, via `config get <key>` /
   `config set <key> <value>`, never a bulk parse of the JSON blob into a
   Swift model. "Seeding" a field means one `config get vpn.enabled` (etc.)
   call per control when the panel opens — the raw file is the source of
   truth on every open, never cached stale in the app.
3. **Explicit, non-open decision on the stop→start fail-open restart
   window.** See [below](#restart-window-decision) — called out on purpose
   rather than left as an implicit gap.

The other items from the original critique did not survive the connection
error that truncated the earlier session (see the note in
[Phase 10](./phase-10-gui-diagnostics.md#note-on-how-this-plan-was-produced)). If
any of them are not in fact covered by the above, or by the general principle
applied throughout this doc — no new privileged Go surface, no duplicated
validation logic, no silent failure paths — say so and they'll be folded in
explicitly.

## Design decisions

| Decision | Choice | Rationale |
|---|---|---|
| Write path | Reuse `dezhban config set <key> <value>` per field, privileged via Phase 10's `runPrivileged` | No new Go command; validation stays single-sourced in `config.Validate` |
| Seeding | `dezhban config get <key>` per rendered field, on every panel open | Raw file is truth; no second full-schema Swift model to keep in sync |
| Scope of editable fields | The `vpn.*` keys already in `configFields` (`enabled`, `tunnelInterfaces`, `endpoints`, `autodetect`, `autoDiscoverEndpoints`, `endpointRefresh`, `tunnelWatch`) | Matches the scope note this phase closes; a general config editor for non-VPN fields is a further follow-up, not bundled here |
| Pre-apply gate | Final `dezhban validate --config <path>` after all `config set` calls, before offering restart | Belt-and-suspenders: each `config set` validates its own field, but a cross-field invariant (e.g. `vpn.enabled=true` with an empty `endpoints`) is only checkable once the whole set is applied |
| Restart window | **Not closed.** Made unmissable instead — see below | Closing it (an atomic reload / rule-preserving restart) is real firewall-backend work, out of scope for a GUI phase; papering over it with a false "seamless" restart claim would be worse than admitting the gap |

## Restart window decision

Applying a change that needs the daemon restarted (`vpn.enabled`, tunnel
interfaces, endpoints — anything the guard state machine reads only at
startup) requires `dezhban stop` then `dezhban start`. `kardianos/service`
(Phase 6) doesn't expose an atomic reload/SIGHUP-style reconfigure, and
Phase 7's `panic`/`Cleanup` teardown is deliberately the same
rules-come-down operation `stop` triggers — by design, per `CLAUDE.md`:
*"`Cleanup()` must always be safe to call... A stale block-all rule can lock
the user out."* That means between `stop` completing and `start` re-arming
the guard, physical-interface egress is **unguarded** — a real fail-open
window, not a bug to be quietly fixed by this phase.

**Decision: this phase does not attempt to close that window.** Building a
"swap rules in place" or "pause-in-place" restart mode is firewall-backend
work spanning three OS backends (`pf_darwin.go`/`nft_linux.go`/
`wfp_windows.go`) — out of proportion to a GUI config panel and better suited
to its own phase if ever undertaken. Instead:

- Before restart, the panel shows a modal that says plainly: *"Applying this
  change restarts dezhban. Network filtering is briefly disabled while it
  restarts (usually under a few seconds). Continue?"* — no euphemism, no
  implied atomicity.
- The restart sequence is `config set` (all fields) → `validate` →
  confirmation modal → privileged `stop` → privileged `start` → poll `status
  --json` until a posture is reported (bounded retries, e.g. 10× at 500ms).
- The menubar icon already has a ⚪ "stopped" state (Phase 8); during this
  window it shows that state rather than the last-known 🟢/🔴, so the user is
  never shown a stale "protected" icon while unprotected.
- If `start` doesn't come back with a posture within the retry budget, the
  panel reports failure explicitly (via Phase 10's output panel, showing
  `start`'s captured stderr) rather than assuming success — a config that
  passed `validate` should start, but a service-manager–level failure (e.g.
  launchd rejecting the plist) is still possible and must not be swallowed.
- Anyone who wants the window to not exist at all has the existing fallback:
  edit the file directly (still offered as "Open config file…") and restart
  by hand at a moment of their choosing, same as today.

## What this phase builds

### `cmd/dezhban` (Go) — none

No new commands. If the cross-field validate-after-set gate above turns up a
gap in `config.Validate()` covering `vpn.enabled=true` + empty
`endpoints`/`tunnelInterfaces`, fix that in `internal/config` as a bugfix, not
as new plan scope — it should already be caught, since `install-local.sh`
relies on the same `validate` call to prevent shipping a self-locking config.

### `macos-gui/Sources/DezhbanMenu/` (Swift)

- **`VPNConfigPanel`** (new file) — a window with:
  - a toggle for `vpn.enabled`,
  - text fields for `vpn.tunnelInterfaces` / `vpn.endpoints` (comma-separated,
    matching the CLI's own `splitList` convention so round-tripping through
    `config set` behaves identically to editing via the CLI),
  - toggles for `autodetect` / `autoDiscoverEndpoints`,
  - duration fields for `endpointRefresh` / `tunnelWatch` (validated
    client-side only as "looks like a Go duration string"; `config set`'s
    `setDuration` remains the authority),
  - an "Apply" button that runs the sequence in
    [Restart window decision](#restart-window-decision).
- **"Open config file…"** kept as an escape hatch alongside the new panel
  (rename from "Open config…" to disambiguate from the new panel).
- Menu wiring: the existing "VPN guard mode" checkmark item now opens
  `VPNConfigPanel` instead of the raw file.

## Out of scope

- A general (non-VPN) config editor — only the `vpn.*` fields already in
  `configFields` are exposed.
- Closing the restart fail-open window (see decision above).
- Autodetect-assisted UI (e.g. running `detect-vpn` and pre-filling suggested
  values) — `detect-vpn` exists on the CLI already and remains a manual step
  for now; wiring it into the panel is a natural next follow-up, not bundled
  here to keep this phase's diff reviewable.

## Acceptance / verification

**Go:** `go build ./...` / `go vet ./...` / `go test ./...` — expect no diff
unless the cross-field `Validate()` gap above turns up real; then a small,
separately-reviewable fix + test.

**App (macOS 13+, Swift toolchain, root available):**
```bash
make gui-macos && open dist/Dezhban.app
```
- Open the VPN panel with the service stopped and `vpn.enabled=false`; confirm
  seeded values match `dezhban config show`.
- Toggle `vpn.enabled=true` with plausible `tunnelInterfaces`/`endpoints`,
  Apply; confirm the restart-warning modal appears, confirm through it, and
  confirm: (a) `config set` calls land (`dezhban config show` reflects them),
  (b) the icon shows ⚪ during the stop/start gap, (c) it resolves to
  🟢/🔴 reflecting the new guard mode afterward.
- Repeat with a value that fails cross-field validation (e.g.
  `vpn.enabled=true`, empty `endpoints`) and confirm Apply is refused **before**
  any restart — no stop/start happens on a config that would fail to start.
- Kill the daemon mid-restart (simulate a `start` failure) and confirm the
  panel reports failure via the output panel rather than reporting success.
