# ADR-0007: `dezhban upgrade` discloses the activation window instead of holding a block through it

**Date**: 2026-07-21
**Status**: accepted, implemented
**Deciders**: Behnam RK

## Context

dezhban had no upgrade path at all: a user who wanted a newer version had to
notice a release existed, re-download the `.pkg` by hand, and re-run the
installer. Adding `dezhban upgrade` means designing a mechanism that
replaces a **root-owned LaunchDaemon binary** while the daemon may currently
be enforcing — for a tool whose entire purpose is that enforcement never
lapses.

`runner.Run`'s firewall teardown (`Cleanup()`) is deferred **unconditionally**
— the same invariant that guarantees a normal `dezhban stop` never leaves a
stale block-all rule also means that *any* restart, for *any* reason, briefly
has no rules installed at all, for however long teardown and startup take
(bounded by `stopTimeout`, 30s). An upgrade that restarts the daemon inherits
this gap. The question this ADR settles: what covers it.

## Decision

The restart gap is **disclosed to the operator and logged, not covered by a
second enforcement mechanism**. Three things do the actual safety work
instead:

1. **A hard activation gate** (`internal/update.CanActivate`) restricts
   *when* a restart may happen at all: only from a healthy `guard` posture
   (routing still carries traffic through the tunnel during the gap) or
   `standby` (no rules exist to lose). Never `full-block` or an open switch
   window, and never on a missing/stale snapshot.
2. **A two-phase split**: installing the `.pkg` (phase 1) opens no gap
   whatsoever — Unix file-replace semantics mean the running process keeps
   enforcing on its old, unlinked inode while the new files land on disk.
   Only the phase that actually restarts the process (activation) is
   ever at risk, and it never happens implicitly — always as an explicit,
   disclosed step.
3. **Automatic rollback**: the pre-upgrade binary/app are stashed before
   phase 1 runs, and restored automatically if the new version doesn't
   publish a healthy snapshot within 30s of the restart.

What is *not* added: a standalone "holding block" applied for the duration
of the restart, the way `panic`/`block` apply rules with no daemon running.

## Alternatives considered

### Alternative 1: A holding block during the restart

Before stopping the old daemon, apply a standalone block-all (reusing the
same rule-application code path `panic`/`block` already use without a
daemon), then lift it once the new daemon confirms enforcement.

- **Pros**: closes the gap completely rather than gating around it. A
  tunnel drop mid-restart would fail closed instead of open.
- **Cons**: a second, independent code path that installs firewall rules
  outside the daemon's own run loop and outside `internal/update`'s
  otherwise pure, unit-testable logic. It has to be torn down correctly on
  every exit path of `apply` (success, installer failure, restart failure,
  rollback failure) — each one a new way to strand a block-all rule if the
  teardown itself is missed, which is exactly the failure mode `Cleanup()`
  already exists to prevent for the *normal* stop/start path. It also
  changes user-visible behavior during the restart: for however long it
  holds, guard-mode traffic is blocked too, not just paused — an update
  would visibly interrupt browsing/calls even in the common case where
  nothing goes wrong.
- **Why not**: the activation gate already restricts *when* this can happen
  to states where the gap is provably harmless (`guard`: routing still
  carries traffic through the tunnel; `standby`: no rules to lose). A
  holding block would be solving for a risk — a tunnel dropping in the
  ~2-30s restart window, while the daemon was already healthy a moment
  earlier — that is real but narrow, at the cost of a second rule-owning
  code path with its own failure modes. Disclosure plus a short, measured
  window plus a hard gate on *when* it may occur was judged the better
  trade for a first version of this feature. This is a closed question,
  not a settled one — see Risks below.

### Alternative 2: Swap the binary/app files directly, no `.pkg`

Fetch the bare per-arch binary and app zip (smaller downloads, faster swap)
and replace them in place, rather than running `installer -pkg`.

- **Pros**: smaller download, marginally faster.
- **Cons**: `app-component.plist`'s `BundleOverwriteAction: upgrade` means
  the macOS Installer consults `pkgutil` receipt versions to decide what
  counts as an upgrade. A file swap leaves the receipt at the *old*
  version, so a later official `.pkg` for an *older* release would read as
  a legitimate upgrade over the (receipt's) older version and **silently
  downgrade** the host. It also gets none of `release.yml`'s existing
  install/uninstall smoke test, which already proves the exact `.pkg`
  being applied here installs cleanly, registers without arming, and
  leaves nothing behind on removal.
- **Why not**: the failure mode (silent downgrade) is worse than the
  benefit (a faster download) is valuable, and applying the `.pkg` gets an
  entire layer of existing test coverage for free.

### Alternative 3: Check for updates from the root daemon

Have the always-on daemon poll GitHub on a fixed interval and publish
availability into `state.json`, so it works headless and the GUI need not
run at all.

- **Pros**: works without the GUI open; one clock instead of GUI-launch-plus-
  timer.
- **Cons**: the daemon's egress is deliberately geo-providers-only — an
  update check would need a `pass to github.com`, and unlike the tightly
  scoped geo-provider pass (tunnel *and* destination, ADR-0006), that hole
  would be reachable even during FULL BLOCK. dezhban is also a kill switch
  with a forbidden-country feature; a root process making an outbound
  connection to a fixed external host on a fixed schedule is a stable
  fingerprint for exactly the userbase most likely to care about that
  property.
- **Why not**: the cost (a new, permanently-open destination pass, on a
  tool whose whole pitch is having as few of those as possible) outweighs
  the convenience. `upgrade check` instead runs only in the GUI (user
  context, on launch + ~24h) or the CLI on demand — inheriting the guard's
  tunnel-only routing for free, and simply failing rather than opening
  anything when the tunnel is down.

## Consequences

### Positive

- No new firewall-rule-owning code path outside the daemon's existing
  `Cleanup()`/`Apply()` machinery — `internal/update` stays pure Go logic
  (version comparison, signature/checksum verification, file
  stash/restore), independently unit-testable with no root and no real
  service.
- The two-phase split means the **common case has zero enforcement
  impact**: most of what "installing an update" means (bytes landing on
  disk, receipts updating, the retired-key check running) carries no risk
  at all. Only the explicit, disclosed restart does.
- Rollback is automatic and self-clearing: the stash exists only for the
  actual risk window (between restart and the new version proving
  healthy), not as permanent disk residue.

### Negative

- The disclosed window is a **real, accepted gap**, not a solved one: if
  the tunnel drops in those ~2–30 seconds, there is briefly no enforcement
  and no daemon running to restore it. The gate (guard-healthy or standby
  only) and the short duration are mitigation, not elimination.
- `upgrade download`/`upgrade apply` both require root, including
  `download` — its staging directory is under the daemon's root-owned state
  directory on purpose (a writable-by-anyone staging area would let a
  local unprivileged user swap the verified `.pkg` before `apply` installs
  it), which means there is no fully-unprivileged "pre-fetch as a normal
  user" path.

### Risks

- **The accepted gap in "Negative" above is revisited if evidence suggests
  it matters in practice** — e.g. a real report of a tunnel dropping during
  an activation window. The mitigation at that point is Alternative 1
  (a holding block), which this ADR rejected for a *first* version on
  complexity grounds, not because it's wrong.
- **Someone "simplifies" the two-phase split into one step.** Mitigated by
  this record and by the CLAUDE.md invariant stating the upgrader never
  gets its own firewall pass and activation is gated — collapsing apply and
  activate into one unconditional restart would silently reintroduce the
  FULL BLOCK problem this design exists to prevent.
- **The 30s health-check timeout is untuned against a real slow restart.**
  It mirrors `stopTimeout` (the daemon's own shutdown bound) rather than
  fresh measurement; if real-world activations routinely take close to
  that long, the timeout may need widening, and the "typically ~2s, up to
  30s" disclosure text will need the real number instead.
