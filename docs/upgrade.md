# Upgrading dezhban

```sh
dezhban upgrade check              # no root — is a newer release out?
sudo dezhban upgrade download       # macOS only — fetch + verify the .pkg
sudo dezhban upgrade apply           # macOS only — install it, then activate
```

Or from the menubar app: **About Dezhban → Updates**, which does the same
thing with one confirmation and relaunches the app for you afterward.

This page explains what each step actually does and why it's split the way
it is — the split is the whole safety story.

## Why check, download, and apply are three separate steps

`dezhban upgrade check` is the **only network call anywhere in this path**,
and it runs somewhere very specific: the GUI, in your user session, on
launch and every ~24h (or the CLI, on demand). **Never the root daemon.**
Two reasons:

- The daemon's egress is deliberately geo-providers-only. An update check is
  not exempt from that — if it ran in the daemon, it would need its own
  firewall pass, and unlike the geo-provider pass (tightly scoped, tunnel *and*
  destination), a `pass to github.com` would be reachable even during **FULL
  BLOCK**. So it doesn't get one. If the tunnel is down when you ask, the
  check just fails — it does not punch a hole to succeed anyway.
- dezhban is a kill switch with a forbidden-country feature. A root daemon
  polling GitHub on a fixed schedule is a stable fingerprint, for exactly the
  audience that has the most reason to care about one. A check that only
  happens when a human is actually looking at the app, on no fixed clock
  the daemon itself keeps, doesn't have that property.

`download` fetches the release, verifies it, and stages it — nothing on your
running system changes yet. It needs root too, and not just because `apply`
does: the staging directory lives under `/var/db/dezhban` (root-owned), on
purpose — a world-writable staging area would let any local, unprivileged
user swap the verified `.pkg` for something else in the gap between
`download` and `apply`, which would quietly defeat the entire point of
verifying it in the first place. `apply` is where things actually happen,
and *that* splits again internally:

## Applying is two phases, and only the second one is risky

**Phase 1 — install the `.pkg`.** This opens **no enforcement gap at all**.
`installer(8)` doesn't stop the daemon, and on Unix, replacing a file that's
currently running doesn't touch the running process — it keeps executing
off its old, now-unlinked inode. The new binary and app land on disk; the
old daemon process keeps enforcing exactly as before, unaware anything
changed.

**Phase 2 — activate.** This is a restart, and a restart is the *only*
moment this whole process touches enforcement: `runner.Run`'s firewall
teardown is unconditional (the same invariant that guarantees a normal
`dezhban stop` never leaves a stale block-all rule also means a restart
briefly has *no* rules installed at all, for however long teardown +
startup takes).

## What has to be true before that restart happens

The activation gate (`internal/update.CanActivate`) checks the *live* daemon
posture, re-read at the instant of activation — not whatever it was when you
ran `download` five minutes ago:

| Posture | Allowed to activate? | Why |
|---|---|---|
| **guard**, healthy | ✅ | Routing still carries your traffic through the tunnel during the gap — nothing is trying to use the physical link in the first place |
| **standby** | ✅ | No rules are installed at all. Nothing to interrupt |
| **full-block** | ❌ | Tearing this down would unblock a host sitting on a forbidden-country exit — the one thing this entire tool exists to prevent, caused by the updater |
| **switch-window** (open) | ❌ | A restart would cancel it mid-use rather than let it close on its own terms |
| missing / stale / unreadable snapshot | ❌ | Unknown is not assumed safe — same rule `decision.Evaluate` already applies to an undeterminable country reading: hold, never escalate on a guess |

If the gate refuses, `apply` still succeeds — the new files are already on
disk from phase 1 — it just doesn't restart. You'll see this:

```
applied, but NOT activated: FULL BLOCK is active — restarting would lift the block on a forbidden-country exit
the previous version is still running normally. retry activation later with: sudo dezhban restart
(the rollback stash is kept until activation actually succeeds)
```

Retry with `sudo dezhban restart` once the posture clears, whenever that is.

## The restart window is disclosed, not hidden or blocked

A deliberate choice: rather than adding a *second* firewall mechanism (a
standalone holding block) to cover the restart gap, the window is simply
told to you plainly before it happens:

```
activating: restarting the daemon into the new version.
enforcement pauses for the duration of the restart — typically ~2s, up to 30s if teardown is slow.
```

That 30s isn't a guess — it's `stopTimeout`, the same bound the daemon's own
shutdown already waits on before giving up and exiting anyway (see
`internal/svc/program.go`). The window opening and closing is also written
into dezhban's own persistent log (`dezhban.log`), so a tunnel drop that
happens to land inside it is auditable after the fact, not silently lost.
The accepted risk, stated plainly: if the tunnel drops in that window, there
is briefly no enforcement and no daemon to restore it. The gate above (only
"guard, healthy" or "standby") and the short duration are the mitigation;
they are not a guarantee that nothing can go wrong in those seconds.

## If the restart doesn't come back healthy

Before phase 1 ever runs, the current binary and app are copied aside
(`internal/update`'s stash). If the new version doesn't publish a live,
non-stopped snapshot within 30 seconds of the restart, `dezhban upgrade
apply` **automatically restores the stash and restarts back into the
version that was known good** — then reports the failure clearly.

The stash is deleted the moment the new version *does* come back healthy —
it exists only for this one risk window, not as permanent leftover disk.

It does, however, outlive an apply that **deferred** activation, and that is
correct rather than a fault: with `--no-activate`, or when the gate refused
because FULL BLOCK or a switch window was up, the new version is on disk but
the old one is still running, so the rollback copy is still live and must be
kept. The documented next step there is `sudo dezhban restart` — which
activates the new version but knows nothing about the stash and does not
clear it. So after a deferred upgrade you are expected to find
`/var/db/dezhban/upgrade-stash/` still present, and the next `upgrade apply`
will refuse until it is dealt with.

Either way the resolution is the same, and `upgrade apply` prints it: confirm
which version is actually running (`dezhban version`). If it is the one you
want, discard the stash with `sudo rm -rf /var/db/dezhban/upgrade-stash` and
retry. If it is not — something interrupted an attempt before it could
resolve either way — finish restoring from the stash by hand first.

## Config, preferences, and everything else that must survive

- `/etc/dezhban/dezhban.json` (your config) and `/var/db/dezhban/` (learned
  VPN endpoints, state) are **never touched**. The upgrade path never
  invokes `uninstall.sh` — that script is only for an actual uninstall, and
  it removes config unless `KEEP_CONFIG=1`.
- The menubar app's own preferences (notifications, the update-check toggle
  itself) live in `UserDefaults`, keyed by the app's bundle identifier —
  untouched by any of this.
- **Retired config keys are never silently dropped.** Before activating,
  `apply` runs the *new* binary's own `dezhban validate` against your
  existing config and surfaces anything it now considers retired — the same
  rule that already keeps `vpn.enabled`/`failClosed`/`allowlist` parsed and
  reported rather than silently ignored.

## Why the payload is the whole `.pkg`, not a smaller file swap

A file swap would be smaller and faster, but it creates two real problems
this design avoids:

- **Stale `pkgutil` receipts.** The `.pkg`'s app component sets
  `BundleOverwriteAction: upgrade`, so macOS's Installer consults receipt
  versions to decide what counts as an upgrade. Swap files in place and the
  receipt still says the *old* version — a later official `.pkg` for an
  *older* release would then look like a legitimate upgrade and silently
  **downgrade** you.
- **No test coverage.** `release.yml` already installs the `.pkg` on a CI
  runner, asserts the service registers without arming, and asserts the
  shipped uninstaller leaves nothing behind. Applying the same `.pkg` here
  means the upgrade path inherits all of that for free. A hand-rolled file
  swap would get none of it.

Applying the `.pkg` also converges the two install paths: running it on a
machine that was originally set up via `install.sh` (no receipts at all)
simply creates the receipts it was missing.

## Platform scope

**macOS**: the full flow above, self-serve. **Linux and Windows**: `upgrade
check` works everywhere, but neither self-applies. A distro-packaged
install (`.deb`/`.rpm`) is tracked by `apt`/`dnf`, and a self-swap would
create exactly the receipt-drift problem described above, one layer down —
the next `apt upgrade` would clobber or downgrade it. So Linux/Windows
report an available version and point you at the platform's own path
(`apt upgrade` / `dnf upgrade` / re-running `install.ps1`) instead.

## The menubar app's side of this

**About Dezhban → Updates** shows the current status, a **Download and
Install** button when one's available (with a confirmation — this restarts
the app unconditionally, and may briefly restart enforcement per the gate
above), a link to the release notes, and a **Check Now** for an on-demand
poll outside the ~24h background cadence. **Settings → "Check for updates
automatically"** turns the background cadence off entirely if you'd rather
this host never contact GitHub on its own schedule — `Check Now` still
works either way.

Clicking **Download and Install** runs `upgrade download` then `upgrade
apply` under a single admin prompt (the same pattern install/uninstall
already use — one password for a multi-step operation, not one per step).
On success, the app **relaunches itself**: `Dezhban.app` was just replaced
on disk regardless of whether daemon activation happened immediately or was
deferred by the gate, so the running process's own code is already stale,
and a fresh launch from the new bundle is the only clean way back.

## What actually protects the payload

`dezhban upgrade download` verifies an **ed25519 signature** over
`SHA256SUMS` (`internal/update.VerifySignature`, checked against the public
key in `internal/update/sig.go`) *and* the `.pkg`'s own checksum, before
anything is ever staged for `apply`. This is not optional and there's no
flag to skip it: an update replaces a root-owned system service, so
"whatever the CDN served" is not an acceptable trust model here — see
[docs/releasing.md](releasing.md#unsigned-artifacts-signed-checksums) for
why this is ed25519 rather than cosign/sigstore, and
[docs/install.md](install.md) for the (deliberately weaker, checksum-only)
guarantee the *first* install gets instead.
