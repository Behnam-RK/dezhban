# Acceptance checks

dezhban is code-complete: every feature described in [modes.md](../concepts/modes.md)
and [cli.md](../usage/cli.md) is implemented and covered by `go test ./...`. What
remains is **privileged, on-host verification** — the checks that need root and
a real firewall, and therefore cannot run in CI.

This file is the standing checklist. Work through the section for the OS you are
on; check the boxes as you go. The "VPN interface guard" section ends with a
macOS worked example giving literal `pf` commands and expected output.

> **Run these on a host you can afford to lock out of.** Every check below arms a
> real kill switch. Keep a second terminal open and know the escape hatch:
> `sudo dezhban panic` removes all rules with no daemon running. See
> [troubleshooting.md](../usage/troubleshooting.md).

## Automated (no root) — run first

These gate everything else and should be green before you touch a firewall:

```sh
go build ./... && go vet ./... && go test ./...
GOOS=linux go build ./... && GOOS=windows go build ./...
swift build --package-path gui/macos && swift test --package-path gui/macos
```

`swift test` covers `DezhbanCore` — the pure, AppKit-free layer (Snapshot
decoding, posture→icon derivation, settings-field batching). `DezhbanMenu`
itself (the AppKit/SwiftUI executable, elevation, CLI shell-out) has no test
target — see Known gaps.

## Unit test policy

Rules for `go test ./...` — the part of the suite that runs in CI, with no
root and no real firewall. `task test:cover` enforces the coverage floors in
`.testcoverage.yml`; these rules keep what counts toward them honest.

- **No `time.Sleep` in a test.** Poll for the condition with a bounded
  deadline, or inject a clock the way `internal/redial` and `internal/decision`
  already do. A sleep either wastes wall-clock waiting past the real moment or
  races it — neither proves the behaviour happened. **Exception:** a test whose
  whole point is a wall-clock duration itself — e.g. proving a reply survives
  arriving *after* an internal deadline has passed
  (`TestSlowRunLoopStillGetsItsReplyThrough`) — may sleep past that real
  duration, because the delay is the scenario, not a wait for one. Comment why
  when you reach for this.
- **`t.Parallel()` by default.** Add it to every new test unless the test uses
  `t.Setenv` or otherwise mutates process-global state (`t.Setenv` itself
  already fails if paired with `t.Parallel()`, which is the tell).
  **`cmd/dezhban` is the standing exception — no test there may be parallel**,
  because `runCLI` swaps the process-global `os.Stdout`/`os.Stderr` and a
  parallel test anywhere in the package races it. Unlike `t.Setenv` nothing
  fails on its own here, so `TestNoTestInPackageMainIsParallel` enforces it.
- **Table-driven once there are ≥3 similar cases; a plain `t.Run` below that.**
  A table with one or two rows is indirection with nothing to show for it.
- **Assert observable behaviour, not call arguments.** Prefer "the firewall
  policy now blocks egress" over "`Apply` was called with these exact flags" —
  the latter breaks on refactors and proves nothing about correctness.
- **Every gate or refusal needs a negative case.** If code can say no
  (`CanActivate`, `canElevate`, a disabled window, a policy switch), a test
  must prove it actually says no, not just that it says yes when allowed.
- **Invariant-pin tests are named contracts — extend them, never restructure.**
  `TestWindowDisableMatrix`, `TestEveryLinkGoesSomewhere`,
  `TestPauseMaxDefaultAndDisableSentinel`, and others like them each pin one
  specific past bug. Add a case to cover new ground; don't rewrite them to "clean
  up" — the name and the shape are the point.

## Enforcement — all platforms

- [ ] **Block cuts egress.** `sudo dezhban block` → general egress dies
      (`curl https://example.com` fails) but loopback works, DNS resolves (with
      `vpn.allowPhysicalDNS` on, the default), the VPN endpoint stays reachable
      so the tunnel can redial, and LAN devices still answer (with
      `vpn.allowLocalNetwork` on, the default). This is FULL BLOCK — it carries
      **no** destination allowlist; a VPN posture opens the tunnel endpoint.
- [ ] **`block --force` is total.** `sudo dezhban block --force` with a tunnel
      up → the ruleset is loopback plus `block drop out all` and nothing else.
      Confirm **no** provider pass in either shape (no destination-only pass on
      the physical link, no tunnel-scoped one), no endpoint pass, no port-53
      rule, and no LAN pass. The tunnel drops, `vpn.allowGeoProviders` makes no
      difference either way, and the log says so. Recovery is `dezhban unblock`
      or `dezhban panic` only — verify nothing lifts it on its own.
- [ ] **`detect-vpn --json` marks an unprivileged scan partial.** Run it as your
      user with a VPN connected whose transport runs as root → `scanPrivileged:
      false`, and the app's Diagnostics pane says the scan saw only your
      sockets instead of claiming none were found. `sudo dezhban detect-vpn
      --json` → `scanPrivileged: true` and the candidate appears.
- [ ] **Status is truthful.** `dezhban status` reports `blocked: true`, with
      accurate country and service fields.
- [ ] **Block is idempotent.** Re-run `sudo dezhban block` → no duplicate rules.
- [ ] **Unblock restores everything.** `sudo dezhban unblock` → full connectivity;
      the `dezhban` anchor/table/group is empty; prior firewall state restored.
- [ ] **Teardown survives a killed process.** `kill -9` the daemon mid-block →
      network is still blocked → `sudo dezhban panic` restores connectivity and
      removes the rules. Rules live in the kernel and on disk, not in process
      memory, so a fresh invocation can always tear down.
- [ ] **Panic is idempotent.** `sudo dezhban panic` on a clean system → no-op, no
      error.
- [ ] **`--force` bypasses detection.** `block --force` / `unblock --force` act
      without consulting the geo state.
- [ ] **Enforcement verification notices a ruleset removed from outside it, and
      repairs it.** With the daemon running and enforcing (guard or a manual
      block), flush the ruleset by hand — `sudo pfctl -a dezhban -F all` (macOS),
      `sudo nft flush ruleset` (Linux; a full flush, not just the `dezhban`
      table, since this is testing that dezhban notices ANY removal), or delete
      the WFP rule group (Windows). Within one `vpn.advanced.verifyInterval`
      (default `1m`; set it lower, e.g. `5s`, to speed up the check) the daemon
      logs `dezhban's firewall rules are MISSING — ... re-applying now`,
      `dezhban status --json` shows `state.verify.missing: true` with
      `repairs` incremented, and the rule dump shows the ruleset back. Set
      `vpn.advanced.verifyInterval: "0"` and repeat — the daemon must NOT
      notice or repair (the check should stay off, not merely run slower).

Per-OS rule inspection:

| OS | Inspect the ruleset with |
|---|---|
| macOS | `sudo pfctl -a dezhban -s rules` |
| Linux | `sudo nft list ruleset` — only the `dezhban` table should appear |
| Windows | WFP filter dump — rules under the `dezhban` group/sublayer |

## Standby and the single-mode merge

- [ ] **Fresh install, no VPN configured** → posture `standby`, **no rules
      installed** (the inspect command above shows nothing for the `dezhban`
      table/anchor), network fully open, menubar icon grey, Overview says
      nothing is being blocked.
- [ ] **Arming.** Configure a tunnel and connect the VPN → the guard arms, icon
      goes green, and the GUARD ruleset appears.
- [ ] **A pre-merge config still works.** Load a config carrying `vpn.enabled`,
      `failClosed` and `allowlist` → it loads without error, `dezhban validate`
      names all three as retired with a reason, and the installed ruleset is
      **identical** to the same config with those keys deleted.
- [ ] **Retired keys are not written back.** `sudo dezhban config set logLevel debug`
      on that config → the saved file no longer contains `failClosed` or
      `allowlist`, and a re-load reports nothing retired.
- [ ] **`--mode legacy` errors by name** rather than rendering a posture that no
      longer exists: `dezhban print-rules --mode legacy` exits non-zero and points
      at ADR-0001.

## Local network access

Only a live host can prove these — CI cannot reach a printer.

- [ ] **Default on: the LAN survives arming.** With the guard armed and
      `vpn.allowLocalNetwork` unset, reach a printer / NAS / the router's admin
      page over its private IP → works. `curl -m5 https://example.com` with the
      tunnel down → still fails. That pairing is the point: LAN open, internet
      shut.
- [ ] **Discovery, not just reachability.** AirPlay/Chromecast targets and
      Bonjour printers still *appear* in their pickers, not merely respond when
      addressed directly. If they are reachable but invisible, the multicast
      ranges are not being passed.
- [ ] **Off closes it.** `vpn.allowLocalNetwork: false` → the same local device
      is unreachable, and the ruleset contains no RFC1918 prefixes.
- [ ] **It is NOT an internet path.** With LAN on and the tunnel down, confirm a
      public address is still blocked — this is the regression test for anyone
      "simplifying" the destination-scoped pass into an interface-scoped one,
      which would silently turn the kill switch off.
- [ ] **IPv6 local works too.** Reach a device over its `fe80::`/`fc00::` address
      with LAN on; confirm it fails with LAN off.
- [ ] `dezhban status` reports `also reachable: local network, DNS` (and
      `(nothing — tunnel and VPN server only)` once both are disabled).

## Address families

- [ ] **A v6 VPN endpoint works end to end.** Set `vpn.endpoints` to an IPv6
      literal → the ruleset loads and the tunnel connects.
- [ ] **A mixed v4+v6 endpoint set loads.** Both families in `vpn.endpoints` →
      `pfctl -a dezhban -sr` shows an `inet` rule and an `inet6` rule, not one
      malformed list.
- [ ] **No `::ffff:` form ever reaches the ruleset.** After a switch window has
      learned an endpoint, inspect `learned.json` and `pfctl -a dezhban -sr`:
      every address must be in canonical form. A `pass out quick inet6 … to
      ::ffff:a.b.c.d` rule is the silent-lockout bug — it looks correct and
      matches nothing.
- [ ] **IPv6 egress is blocked while the guard is armed.** `curl -6 -m5` to a
      public v6 host fails; the same request through the tunnel succeeds. Test
      with real packets, not by reading rules — rule inspection would have called
      the mapped-address bug "handled".

## Recovery probe (the geo-provider pass)

- [ ] **No guard lift during recovery.** Force FULL BLOCK (block the exit's
      country), then watch the logs across several probe ticks: no
      `apply-guard` / lift-and-re-cut cycle should appear, and
      `pfctl -a dezhban -sr` must stay on the FULL BLOCK ruleset throughout.
      A lift every tick is the ~8s recurring leak this replaced.
- [ ] **The pass is tunnel-scoped.** The provider rule must read
      `pass out quick on { utunN } to { … }` — interface **and** destination.
      A destination-only rule is the unsafe variant: it would let the lookup
      succeed with the tunnel down and report your ISP's country.
- [ ] **The measurement stays honest.** Drop the tunnel while in FULL BLOCK and
      confirm the lookup **fails** rather than silently reporting the ISP's
      country. This check must never be "fixed" to pass by allowing the
      providers on the physical link — that is precisely the bug ADR-0006
      exists to prevent, and it would close switch windows early on a bogus
      "good exit".
- [ ] **The pass carries no DNS rule.** `pfctl -a dezhban -sr` in FULL BLOCK must
      show **no** port-53 rule scoped to the tunnel (`on { utunN } … port 53`).
      Such a rule is destination-unscoped, so it would send every application's
      DNS through the tunnel to the forbidden exit's resolver. A `to any port 53`
      rule with no `on` clause is the separate, opt-out `vpn.allowPhysicalDNS`
      pass on the physical link — that one is expected unless you set it `false`.
- [ ] **Rotation degrades safely, then heals.** Leave the daemon in FULL BLOCK
      longer than `vpn.endpointRefresh` and let the providers' CDN addresses
      rotate. Re-resolution has no DNS path in FULL BLOCK, so the scoped pass
      goes stale, the lookup fails, and **the posture holds** (an undeterminable
      country never escalates). Recovery falls back to lift-and-probe, which
      lifts the guard — the next refresh then succeeds and the scoped pass heals
      itself. Confirm recovery still completes.
- [ ] **The fallback survives.** Point `providers` at an unresolvable host so no
      IP resolves → the daemon logs that recovery will briefly lift the guard,
      and recovery still works via lift-and-probe. A FULL BLOCK that can never
      observe its way out would be worse than the leak.

## Country check (exit country, not physical location)

- [ ] **Blocklist trips.** Add the VPN exit's country to `blockedCountries` →
      within `hysteresis` ticks the posture escalates to `full-block`.
- [ ] **Recovery.** Remove it → the guard is restored.
- [ ] **Clean shutdown.** `Ctrl-C` while blocked → `Cleanup()` runs, connectivity
      is restored, exit 0.
- [ ] **An unknown country HOLDS — it must not escalate.** Blackhole every
      provider host (e.g. via `/etc/hosts`) while running in GUARD → the posture
      **stays** `guard` however many error-ticks pass, and the log says the exit
      country is unknown. It must never reach `full-block` on errors alone:
      that would cut the tunnel's own egress and livelock the redial.
- [ ] **An exit-IP change is observed and reported, without touching posture.**
      Switch the VPN to a different server that still exits through an allowed
      country (so `countryCode`/`blocked` are unaffected) → the daemon logs
      `exit IP changed` and `dezhban status --json` shows a fresh
      `exitIpChangedAt`. Confirm it does NOT reset on an unchanged reading, and
      that `pending`/hysteresis progress is untouched by the comparison itself.
- [ ] **An unknown country does not lift a block either.** Repeat while in
      `full-block` → it stays blocked.
- [ ] **An error mid-streak does not cancel a pending flip.** With `hysteresis: 3`,
      feed blocked/blocked/error/blocked → the block still commits on the fourth
      reading.
- [ ] **No flapping.** An alternating country sequence must NOT toggle the
      firewall until `hysteresis` consecutive readings agree.
- [ ] **Quorum.** With three providers and one disagreeing, the majority wins and
      a warning is logged.

## VPN interface guard

The guard is where a misconfiguration locks the host out. Run
`dezhban doctor --discover` first; it is designed to catch exactly that.

- [ ] **Guard is up, tunnel traffic flows.** With the VPN connected and the guard
      armed, normal browsing works.
- [ ] **A tunnel drop cuts egress with no leak.** Bring the tunnel interface down
      → all egress is cut immediately, with no physical-interface leak window.
      Bring it back → traffic resumes.
- [ ] **Rules are interface-aware,** honoring the tunnel/endpoint interface
      conditions — not merely destination IPs. Confirm in the rule dump.
- [ ] **Guard is idempotent.** Re-arming does not stack rules.
- [ ] **A forbidden country escalates to FULL BLOCK,** cutting the tunnel itself
      (`--simulate-country IR`).
- [ ] **An undeterminable country HOLDS the current posture** rather than
      escalating — escalating on an unknown would cut the tunnel's own egress and
      livelock the redial.
- [ ] **Unblock restores everything.**
- [ ] **A hung tunnel (interface up, no traffic) is diagnosed, not silently
      left cut with no signal.** With the VPN connected and the guard armed,
      block the tunnel's traffic at the OS level without bringing the
      interface down — e.g. a host-level firewall rule dropping packets on the
      tunnel interface, or disconnect the VPN server side while the client's
      interface stays configured. After `hysteresis` consecutive failed exit
      checks, the daemon logs `tunnel interface reports up, but exit lookups
      through it keep failing`, `dezhban status --json` shows
      `state.zombie.checks`, and `dezhban doctor`'s "enforcement liveness"
      section reports it. Confirm the guard itself is untouched throughout —
      still cutting egress exactly as it would for any other tunnel-up state —
      and that with `vpn.advanced.livenessRedial` at its default (`false`) NO
      switch-window rule ever appears in the rule dump. Set it to `true` and
      repeat: a switch-window pass should appear once the streak is confirmed,
      through the same `redialBudget`/`redialMinUptime` machinery an ordinary
      drop uses.

### macOS worked example (pf)

Run on the local console, not over SSH/VPN/remote — a bad config or a crash
mid-block can lock you out. Keep a second terminal open with the escape hatch
(`sudo pfctl -a dezhban -F all`) before you start. Fill in the `vpn` block
first (tunnel interface via `route -n get default | grep interface`; the VPN
endpoint from your client's own config/logs — `lsof -nP -iUDP -a -p $(pgrep -f
your-vpn-process)` finds it for UDP VPNs).

```sh
# Teardown works before you trust block:
sudo dezhban block --config <config>; sudo dezhban unblock
sudo pfctl -a dezhban -s rules              # expect: empty anchor

# Guard up, tunnel traffic flows:
sudo dezhban block --config <config>
sudo pfctl -a dezhban -s rules              # expect: pass on { utunN }, pass to { endpoint }, block drop out all
curl -m5 https://example.com                # expect: succeeds (rides the tunnel)

# Tunnel drop cuts egress, no fall-through to the physical interface:
sudo ifconfig utunN down
curl -m5 https://example.com                # expect: hangs/fails — redial the VPN to restore

# Forbidden country cuts the tunnel too (FULL BLOCK) — run in the foreground to force it:
sudo dezhban run --config <config> --simulate-country IR &
sudo pfctl -a dezhban -s rules               # expect: only `pass quick on lo0 all` + `block drop out all`

sudo dezhban unblock                         # expect: connectivity back, anchor empty
```

### Profiles, switching, and learned endpoints

- [ ] **Config compatibility.** A pre-profiles config still loads, validates, and
      renders identical rules; every file in `configs/` passes `dezhban validate`.
- [ ] **Union.** With two profiles, both VPNs' endpoints appear in the guard
      rules, and switching between them needs no reconfiguration:
      ```sh
      task rules MODE=guard  CONFIG=configs/dezhban.profiles.json
      task rules MODE=switch CONFIG=configs/dezhban.profiles.json
      ```
- [ ] **The switch window behaves.** `dezhban switch` opens a window (state.json
      posture `switch-window`); the daemon learns and pins the new endpoint into
      `learned.json`, and closes the window early on a verified exit. `--cancel`
      and expiry both revert to the prior fail-closed posture.
- [ ] **Promotion.** `dezhban vpn promote` makes a learned endpoint permanent, so
      redialing to that VPN needs no window at all.
- [ ] **Import.** `dezhban vpn import` extracts the expected hosts from WireGuard,
      OpenVPN, and V2Ray configs — stripping ports, dropping private/loopback
      addresses, and rejecting garbage.
- [ ] **Dynamic tunnels.** A newly-appeared tunnel is guarded within one watcher
      tick, with no restart. Zero tunnels up = endpoints-open standing posture,
      with geo suppressed.
- [ ] **Automatic redial window.** With a rotating-server VPN (e.g.
      RocketTunnel) guarded and healthy: disconnect, then hit the client's
      connect button within `vpn.redialWindow` (default 30s) — the VPN
      redials to a **fresh, never-seen server** with no operator action;
      `status` shows `redial state: OPEN` (`status --json`:
      `switch.trigger: "auto"`) while it lasts, and the menubar app posts the
      "VPN dropped — redial window open" notification.
- [ ] **Auto-window expiry fails closed.** Disconnect the VPN and let the
      window lapse with no redial: egress is cut, STAYS cut (no second
      window without a tunnel-up first), and a later client connect to a
      *known/learned* endpoint still succeeds under the standing posture.
- [ ] **No auto window from FULL BLOCK.** `--simulate-country IR` → FULL BLOCK,
      then drop the tunnel: no window opens; recovery still requires the probe
      confirming an allowed exit (or a manual `switch`).
- [ ] **Strict opt-out.** With `vpn.redialWindow: "0"`, a drop opens nothing
      and behavior matches the pre-0.3 zero-relaxation guard.

### The two windows disable independently

Run all four permutations; each setting must disable **only** its own trigger.

- [ ] `switchWindow: "0"`, `redialWindow` default → `dezhban switch` refuses
      with a message naming `vpn.switchWindow`, but a tunnel drop **still** opens
      the automatic redial window.
- [ ] `switchWindow` default, `redialWindow: "0"` → a drop opens nothing, but
      `dezhban switch` **still** works.
- [ ] **Both `"0"` — the strict zero-leak posture.** A drop is cut instantly with
      no window at all, and `dezhban switch` refuses. Nothing can relax the guard.
- [ ] **`"0"` survives a round trip.** With `switchWindow: "0"` set, run
      `dezhban config set logLevel debug` and re-load → it is still disabled, not
      silently coerced back to the 15s default. (This was a real bug: the setting
      was accepted and discarded.)

**Full live macOS pass:** `setup` → connect VPN A (guarded) → disconnect →
`dezhban switch` → connect self-hosted VPN B → the window learns the endpoint and
closes → `vpn promote` → redial to B with **no** window → `--simulate-country
IR` still escalates to FULL BLOCK → `sudo dezhban panic` restores.

## Live reload

Against a **running** daemon. The bug this replaced was silent: the file changed
and the daemon kept enforcing the old value, so every check here is about the
daemon's *behaviour* afterwards, never about what the file says.

- [ ] **A live key takes effect with no restart.** `dezhban config set
      pollInterval 5s` → the output says `Saved and applied: pollInterval`, and
      the daemon's log shows geo polls at the new cadence within seconds.
- [ ] **A restart-required key says so instead of lying.** `dezhban config set
      logLevel debug` → `Restart dezhban to apply: logLevel`, and the log level
      is genuinely unchanged until `dezhban restart`.
- [ ] **A malformed edit does not disturb enforcement.** Hand-edit the config to
      invalid JSON, then trigger a reload → the reload is refused with a parse
      error and the guard keeps enforcing the last good configuration.
- [ ] **A lowered cap binds the very next window.** With a pause open-able, set
      `vpn.pauseMax` to `1m`, then `dezhban pause 9m` → the pause ends
      after 1m. (A reload that reported the cap applied while still clamping to
      the old, larger value was a real bug.)
- [ ] **Disabling a trigger live actually disables it.** `dezhban config set
      vpn.switchWindow 0` → `dezhban switch` refuses immediately, without a
      restart, and the *other* two triggers still work.
- [ ] **Re-enabling a trigger live works too, on both paths.** Start the daemon
      with `vpn.switchWindow` and `vpn.pauseMax` both `"0"` (the strict zero-leak
      posture), then `dezhban config set vpn.switchWindow 30s` → `dezhban switch`
      opens a window with no restart, **and** so does the root command-file path
      with the daemon's control socket stopped (`control.enabled: false`). The
      command poll used to be wired only when a window was enabled at startup, so
      this reported applied and did nothing.
- [ ] **A tightening lands while traffic is cut.** Get the daemon into FULL BLOCK
      (a forbidden exit, or `dezhban block`), then `dezhban config set
      vpn.allowLocalNetwork false` → the LAN pass is gone from the live ruleset
      immediately (`pfctl -a dezhban -sr` / `nft list table inet dezhban`), not
      only after the next posture change.
- [ ] **An unrelated edit does not reset a pending flip.** With a forbidden exit
      and `hysteresis: 3`, wait for `status` to report `Escalating to full block —
      1 of 3 confirming checks.`, then `dezhban config set pollInterval 15s` → the
      count keeps climbing from where it was. Then change `blockedCountries` and
      confirm the count *does* restart, which is the one case where it should.
- [ ] **No daemon running.** The write still succeeds and says so; the values are
      picked up at the next start.

## Recovery after a redial

Privileged, on a real host with a real VPN. The point of these checks is the
*wait*: what the user sees between redialing and the guard coming back.

- [ ] **Progress is visible.** Force FULL BLOCK (`--simulate-country IR`, or a
      real forbidden exit), then redial onto an allowed exit → `dezhban status`
      shows "Restoring the guard — 1 of 2 confirming checks." and the app's
      Overview shows the same count, before the posture changes.
- [ ] **It is fast.** The guard comes back within seconds of the tunnel coming
      up, not after a full `pollInterval` × `hysteresis`.
- [ ] **Hysteresis still gates it.** With `hysteresis: 3`, a single allowed
      reading does NOT restore the guard — it still takes three.
- [ ] **No acceleration when probing would leak.** Break provider resolution (an
      unreachable `providers` URL), force FULL BLOCK, then bring the tunnel up →
      the daemon logs that it is not accelerating, and the probe cadence stays at
      `pollInterval`. Accelerating here would multiply real-IP exposure.
- [ ] **A forbidden exit that persists backs off.** Stay on a blocked exit after a
      tunnel-up edge → probing returns to the normal cadence within ~90s instead
      of hammering the geo providers.
- [ ] **Nothing is claimed in standby or during a window.** Neither reports a
      pending change: the geo state machine is not driving the posture there.

## The control token

Privileged for enroll/forget, macOS-relevant but not macOS-only.

- [ ] **Not enrolled is a refusal, not a bypass.** With no token enrolled, a
      `config-write` over the socket is refused. Confirm `dezhban token status`
      reports "not enrolled".
- [ ] **Enroll, then write without a password.** `sudo dezhban token enroll` →
      a token on stdout; a client presenting it can change a setting over the
      socket with no elevation, and the daemon adopts it in the same request.
- [ ] **A wrong or absent token is refused** even from an account in the socket's
      admin group — group membership alone must not authorise a config write.
- [ ] **The hash file is root-only.** `ls -l /var/db/dezhban/control.token` →
      mode `0600`, owned by root. Anything that can read it can forge the proof.
- [ ] **The policy switch overrides a valid token.** Set
      `control.allowConfigOps: false` → a client holding the correct token is
      still refused, and the message names the setting.
- [ ] **Re-enrolling revokes.** Enroll a second time → the first token no longer
      works. This is the revocation path for a leaked token.
- [ ] **`token forget` recovers a stranded host.** After forgetting, config
      changes fall back to `sudo` rather than being impossible.
- [ ] **The toggle works on an ordinary ad-hoc build.** On any build from
      `build-app.sh`, "Use Touch ID for settings changes" enables, enrolls with
      one password prompt, and a subsequent settings change costs a **fingerprint
      and no password**. See
      [ADR-0012](../adr/0012-app-checked-biometrics-on-unsigned-builds.md).
- [ ] **Settings never freezes waiting on the keychain.** Launch the app with the
      lid shut on an external display (no usable sensor, so the launch warm-up is
      skipped by design), then open the lid and go straight to Settings. The
      Authorization section may read "Checking…" for a moment; the window must
      stay responsive throughout. A freeze here means something reads
      `ControlToken.capability` on the main thread — use `capabilityIfKnown` plus
      `resolveCapability` instead. Worth repeating with a locked login keychain
      (`security lock-keychain`), which is what turns the block into a modal
      dialog rather than a pause.
- [ ] **Enrollment survives an app upgrade.** Enrol, then rebuild and reinstall
      the app (`task dev` is enough — an ad-hoc rebuild changes the cdhash, and
      the keychain ACL is bound to it). Toggling off and on again must succeed:
      before the fix this failed with `-25299` because the new build could
      neither read nor delete the item the old one stored. The first *read* after
      an upgrade may ask you to approve keychain access once; that is macOS, and
      approving keeps the enrollment.
- [ ] **A cancelled fingerprint falls back to sudo, never to a login password.**
      Dismiss the Touch ID prompt on a settings save: the change must fall to the
      ordinary privileged path, and the biometric prompt must never offer "Use
      Password…" as a way through — `load()` uses
      `.deviceOwnerAuthenticationWithBiometrics` precisely so it cannot.
- [ ] **The app refuses enrollment it cannot complete, for free.** Simulate a
      failing store (e.g. temporarily point `store` at an invalid attribute set):
      flipping the toggle must produce **no password prompt** and leave
      `dezhban token status` reporting "not enrolled" — the failure this checks
      for cost a password and then stranded an enrollment. See
      [ADR-0011](../adr/0011-biometric-enrollment-requires-a-signed-build.md).
- [ ] **A failed store rolls the daemon back.** If `SecItemAdd` fails after
      `token enroll` has already run, `dezhban token status` must return to
      "not enrolled" without the user intervening.
- [ ] **A failed store stops offering the stale secret — rollback or not.** With
      a leftover keychain item the app cannot replace, force BOTH branches of the
      rollback: one that succeeds, and one that fails (dismiss its admin prompt).
      In each, the About pane's "Settings changes" must read "Password — the
      stored secret is stale", and a settings save must complete through the
      password prompt rather than failing with a daemon refusal. The flag is
      session-only, so check without relaunching the app.
- [ ] **The About pane never invites an impossible retry.** With no token
      enrolled, "Settings changes" must not read "turn on Touch ID in Settings"
      unless that toggle would actually succeed.
- [ ] **An enrolled host with an unusable sensor says "Password", not "Touch
      ID".** With a token enrolled, shut the lid on an external display (or fail
      Touch ID until it locks out) and open the About pane: "Settings changes"
      must read "Password — Touch ID is unavailable right now…the enrollment is
      intact", and a settings save must complete through the sudo prompt.
      Restore the sensor, re-enter the pane, and it must read "Touch ID (control
      token enrolled)" again. The row is evaluated on appearance, so navigate
      away and back rather than watching it in place.
- [ ] **An entitlement does not silently brick the app.** If anyone adds
      `keychain-access-groups` to `build-app.sh`'s `codesign` call, the built app
      is SIGKILLed at launch rather than gaining keychain access — and
      `codesign --verify` passes on such a binary, so the signature checks do not
      catch it. The release workflow now execs the installed app and fails on a
      `137`, so this is enforced **at release**; run it by hand after any local
      change to that `codesign` line, since nothing checks a dev build.

## Service lifecycle

Per OS, privileged:

- [ ] **Install.** `dezhban install` registers the service — verify with
      `launchctl list | grep dezhban`, `systemctl status dezhban`, or
      `sc query dezhban`.
- [ ] **Start + survive reboot.** `dezhban start` → enforcement active; reboot →
      the service comes back up on its own.
- [ ] **Stop tears down.** `dezhban stop` → the run loop's `Cleanup()` fires, all
      rules are removed, connectivity is fine.
- [ ] **Uninstall.** `dezhban uninstall` → fully removed.
- [ ] **Crash recovery.** Kill the service process while blocked →
      restart-on-failure brings it back and it re-enforces.
- [ ] **`restart` applies the restart-required keys** (most keys apply live — see
      the section below), and `start` and `stop` are idempotent.
- [ ] **A second `run` refuses.** With the service running, `sudo dezhban run`
      (with or without `--no-daemon`) in a second terminal refuses immediately
      with "another dezhban is already running", and the first daemon's
      enforcement is undisturbed — no duplicate rules, no double-Apply. `kill -9`
      the first daemon, then start a second `run`: it succeeds (the OS released
      the lock with the process), confirming a killed daemon never wedges the
      next start.

## Unattended recovery (`doctor`'s boot and retention checks)

Unprivileged, but they need real machine state CI has none of — a service
manager, a reboot, and a VPN that has actually connected.

- [ ] **Boot service, honestly reported without root.** With the service
      installed and running, `dezhban doctor` **as a normal user** reports
      *boot service: registered to start at boot, and enforcing now*. This is
      the regression that matters: the check reads the unit file precisely
      because an unprivileged `launchctl` query cannot see the system domain and
      would report a live daemon as not installed.
- [ ] **Boot service, absent.** `sudo dezhban uninstall` → the check warns and
      offers `dezhban install`. If a daemon is still running by hand, it also
      says so rather than reading as "the guard is off".
- [ ] **Not at boot.** Edit `RunAtLoad` to `<false/>` in
      `/Library/LaunchDaemons/dezhban.plist` (Linux: `systemctl disable
      dezhban`) → the check warns that every reboot comes up unguarded.
      Reinstall to restore.
- [ ] **Arm at boot, precondition met.** After a VPN has been up once,
      `<state dir>/armed.json` has `tunnelEverUp: true`, the check reports the
      first/last times, and a reboot arms the guard before the VPN connects.
- [ ] **Arm at boot, precondition missing.** Remove `armed.json` → the check
      warns that no tunnel has been observed, and a reboot opens into standby.
      Connect the VPN once → the file returns and the check goes green.
- [ ] **Arm at boot, record corrupt.** Write `{` into `armed.json` → the check
      warns with the parse error and dezhban still starts (a corrupt record is
      "never armed", never a crash).
- [ ] **Learned endpoints, healthy.** After a normal drop and redial, the check
      reports addresses retained and a drop that can redial without a window.
- [ ] **Learned endpoints, aged out.** Set
      `vpn.advanced.learnedEndpointTTL=1s`, wait, re-run → the check warns they
      aged out and offers the retention knob, **not** the rotation advice.
- [ ] **Learned endpoints, rotating.** On a rotating-pool VPN (NordVPN,
      ProtonVPN), reconnect until the store fills → the check reports rotation
      and leads with the hostname fix.

## Redial budget and backoff

The ledger is unit-tested in `internal/redial` against injected instants, so
what needs a real host is the wiring: a real tunnel dropping, real timers, and
both surfaces saying the same thing about it. See
[ADR-0009](../adr/0009-redial-budget.md).

- [ ] **Healthy drop, full window.** With the tunnel up longer than
      `vpn.advanced.redialMinUptime`, disconnect the VPN → a window opens for
      the full `vpn.redialWindow`, the log says `reason=full`, and reconnecting
      snaps it shut early.
- [ ] **Early close is nearly free.** After that reconnect, drop and redial
      quickly several times. `status --json` must never show `state.redial`, and
      the guard must keep granting windows — the budget measures exposure taken,
      so fast successful redials barely touch it. If a handful of *successful*
      redials exhaust the budget, credit-on-close is broken.
- [ ] **Fast drops shorten, they do not suppress.** Force reconnects faster than
      `redialMinUptime` with no good exit in between (a deliberately wrong
      server works). Each drop must still get a window, each shorter than the
      last (`reason=backoff`, `granted` falling), with a growing cooldown. A
      drop that gets NO window at the first fast reconnect is the pre-ADR-0009
      behaviour returning.
- [ ] **A recovery clears the cooldown.** Immediately after one of those fast
      drops — while the cooldown is still running — let the tunnel come back
      properly and stay up past `redialMinUptime` (or long enough for the exit to
      be confirmed), then drop it again. That drop must get a **full-length**
      window, not `reason=cooldown`. A refusal here is the failure that pushed
      recovering links onto `dezhban switch`: the retry would eventually re-ask,
      but not before the whole remaining cooldown a recovered link never earned.
- [ ] **Exhaustion holds, and says so.** Keep flapping until the log reads
      `redial budget spent`. Traffic must stay cut, `status` must read
      *"Your VPN has dropped often enough to use up its redial budget…"* with a
      real time after "dezhban tries again at", and the menubar app must show
      the **same sentence** — it renders `display.detail`, so a difference means
      something is composing prose that shouldn't.
- [ ] **The refusal re-decides itself.** From that exhausted state, leave the
      tunnel **down and untouched** — do not reconnect, do not run anything. At
      the `nextEligible` the refusal named, a window must open on its own
      (`REDIAL WINDOW OPEN` in the log, `state.switch.trigger` = `auto`). This is
      the whole point of publishing an instant: before the retry timer existed,
      nothing acted at that time and the host stayed cut until someone ran
      `dezhban switch`. Verify with the VPN client stopped, so no reconnection
      can be confused for the cause.
- [ ] **One window per drop, even with the retry.** Let that window expire with
      the tunnel still down. Nothing may open a second one, however long you
      wait and however much budget has refilled — the next window requires a new
      drop. A repeating window here is the retry re-arming after a grant.
- [ ] **Hold suppresses the re-decision.** Reach an exhausted refusal again, then
      run `dezhban hold` while the tunnel is still down. At `nextEligible` no
      window may open (`redial retry skipped` in the log). Then reconnect and
      drop: that drop must still be covered by hold — the retry honours the flag
      but does not spend it.
- [ ] **Cancelling hold gives the re-decision back.** From that suppressed state
      — retry already skipped, tunnel still down — run `dezhban hold --cancel`
      once the budget has refilled. A window must open **immediately**, without
      waiting for another drop. Nothing opening is the failure this check exists
      for: it means the drop is stranded until an edge that cannot arrive, and
      `status` will be claiming dezhban re-checks on its own while it does not.
      Cancel *before* `nextEligible` instead and nothing should open early —
      the original timer is still running and still governs.
- [ ] **The budget refills.** Wait out `vpn.advanced.redialBudgetWindow` with
      the tunnel down, then drop again → a window opens. It must open no later
      than the `nextEligible` the refusal named.
- [ ] **Hold the line spends nothing.** `dezhban hold`, then drop → no window,
      and `status --json` shows no `state.redial` (hold suppresses ahead of the
      ledger, so nothing was refused and nothing was charged). Reconnect, drop
      again → a full-length window, proving the budget was untouched.
- [ ] **`vpn.redialWindow: "0"` still removes trigger 2 entirely**, budget
      irrelevant; the manual switch window and `pause` still work.
- [ ] **`redialBudget: "0"` is refused, not normalised.**
      `sudo dezhban config set vpn.advanced.redialBudget=0` must exit non-zero
      and leave the file unchanged — a limit has no "off", and silently storing
      `2m` for a typed `0` is the failure this project treats as worst.
- [ ] **Live reload lands on the next drop.** With the daemon running, lower
      `redialBudget`, confirm `Saved and applied` lists it, then flap until
      refusal — it must refuse against the NEW number without a restart.

## Upgrade

macOS only, privileged (`dezhban upgrade download`/`apply`). See
[upgrade.md](../usage/upgrade.md) for the full design.

- [ ] **Tunnel down.** `dezhban upgrade check` with the tunnel down fails
      cleanly and opens nothing — it inherits the guard's tunnel-only routing
      rather than getting its own firewall pass.
- [ ] **Deferred activation during FULL BLOCK.** With the guard in FULL
      BLOCK, `dezhban upgrade apply` installs the payload, refuses to
      activate, and leaves the old daemon enforcing normally.
- [ ] **Deferred activation while the guard holds a downed tunnel.** With a
      healthy guard, disconnect the VPN and wait for the redial window to
      expire, so `status` reads "VPN down — traffic cut" at posture `guard`.
      `dezhban upgrade apply` must install the payload and REFUSE to activate,
      naming the downed tunnel — the posture string is `guard`, but the rules
      about to be torn down are the only thing cutting egress. Then reconnect
      the VPN and retry: the same command must now activate. The refusal is the
      half that cannot be caught in CI, since it needs a real tunnel to drop.
- [ ] **A deferred stash is NOT cleared before activation.** From the state
      above (payload applied, activation refused, stash present), run
      `upgrade apply` again WITHOUT restarting first. It must refuse with the
      "applied but NOT yet activated" message and leave the stash intact —
      the daemon is still running the stashed version, so that stash is the
      only copy of it. Classifying against the on-disk binary here (which
      already reads as the new version) would delete it; this step is the
      on-host check for that.
- [ ] **The deferred stash then resolves itself.** Now `sudo dezhban restart`
      to activate, confirm the new version is running with `dezhban status`
      (the daemon's own snapshot — *not* `dezhban version`, which reports the
      binary you invoked), then run `upgrade apply` again for a DIFFERENT
      release — it should clear the now-obsolete stash automatically instead
      of refusing (see docs/upgrade.md, "If the restart doesn't come back
      healthy").
- [ ] **An unreachable daemon refuses rather than guesses.** With a stash
      present and the daemon stopped (`sudo dezhban stop`), `upgrade apply`
      refuses with the "could not be compared against the running version"
      message rather than clearing anything.
- [ ] **Rollback.** Force the new version to never publish a healthy
      snapshot (e.g. stop the daemon right after the restart) → `upgrade
      apply` restores the previous binary/app and restarts back into it
      within ~30s.
- [ ] **Config and learned state survive.** `/etc/dezhban/dezhban.json` and
      `/var/db/dezhban/learned.json` are byte-identical before and after a
      full upgrade.
- [ ] **The upgraded app launches.** After `upgrade apply` activates,
      confirm `/Applications/Dezhban.app` opens normally
      (`AppActions.relaunch()`'s `open` succeeds) — proves the ad-hoc
      signature survived packaging into the `.pkg` and reinstall, the same
      invariant release.yml's smoke test now asserts with `codesign
      --verify`.

## Setup wizard

- [ ] A fresh `dezhban setup` on macOS produces an autodetect + auto-discovery
      config with **zero** concrete interface names, and offers to install and
      start the service.
- [ ] Re-running it on a configured host seeds every question with what the
      config already says, and pressing Enter through the whole wizard writes a
      config identical to the one you started from (`dezhban config show` before
      and after). `internal/setup` pins this, but only the real run proves the
      forms are bound to the same answers.
- [ ] Re-running it **without** naming profile files keeps the profiles already
      imported (`dezhban vpn list`).
- [ ] `dezhban setup --questions --json` runs with no TTY, no root, and no
      config file present, and lists the same questions the wizard asks.

### First-run wizard (macOS app)

- [ ] With no VPN configured and `defaults delete com.dezhban.menu
      dezhban.firstRunCompleted`, launching the app opens the window **and** the
      wizard. With a VPN already configured from the CLI, it does not — the
      questions were already answered.
- [ ] The questions, their order, and the gating match `dezhban setup` run in a
      terminal on the same host. Declining "Configure your VPN now?" skips the
      whole VPN branch in both.
- [ ] Saving writes through one `config set` (one password prompt, or none with
      a token enrolled) and the values land in `dezhban config show`. Choosing
      automatic detection leaves `vpn.tunnelInterfaces` **empty**.
- [ ] Cancelling with "Not now" writes nothing and offers the wizard again next
      launch.
- [ ] Naming VPN config files imports them as profiles (`dezhban vpn list`);
      cancelling that second prompt leaves the config saved and only the import
      undone.
- [ ] Settings → "Run Setup Again…" reopens it seeded with current values.

## macOS app

Build and launch:

```sh
task gui:build && open dist/Dezhban.app
```

### Surfaces & window lifecycle

- [ ] **Menubar is a glance and the time-critical set only.** The dropdown shows
      exactly: one status line, Open Dezhban… (⌘O), the switch/pause item with
      its live countdown, hold the line, Quit. **No Block or Unblock** — those
      live in the window's Overview.
- [ ] **Panic is behind ⌥.** Holding Option swaps "Open Dezhban…" for "Panic —
      force unblock…"; ⌘⌥O fires it; releasing Option restores the open item. It
      still works with the daemon stopped and with the main window unable to
      open.
- [ ] **Window opening.** "Open Dezhban…" and a Dock-icon click both open/focus
      the main window; closing the window (⌘W) leaves the app and icon running.
      Both work in **every** "Open minimized" mode — the preference governs the
      launch only and must never make the window unreachable.
- [ ] **"Open minimized" honours the setting**
      ([ADR-0014](../adr/0014-login-item-launch-marker.md)). With "Open this app
      at login" on, for each mode: **Only at login** (the default) → log out and
      back in, **no** window; then launch from Finder, window opens.
      **Always** → no window either way. **Never** → window both ways. The
      marker is what makes this work, so also confirm the login launch carries
      it: `ps -o args= -p "$(pgrep -x DezhbanMenu)"` ends in `--background`
      after a login launch and does not after a Finder launch.
- [ ] **Login-item migration is one-way and never opts you in.** On an install
      that predates the agent: with login-at-launch **on**, launch once, then
      confirm `SMAppService.mainApp` is no longer registered while the agent is
      (`launchctl print gui/$UID/com.behnam-rk.dezhban.app.login` succeeds) and
      the Settings toggle still reads on. Repeat with login-at-launch **off**:
      it must still be off, and the agent must not be registered.
- [ ] **State restoration cannot reopen the window.** With the window open and
      "Close windows when quitting an application" *unchecked* in System
      Settings → Desktop & Dock, quit and relaunch in a mode that should open no
      window — it must stay closed.
- [ ] **Registering the login item does not leave two apps running.** With the
      app up, switch Settings → "Open this app at login" **off then on**. The
      agent's `RunAtLoad` execs a second copy the moment it registers, and
      launchd does not dedupe the way LaunchServices did, so this is the check
      that the session lock works: exactly **one** menubar item and one Dock
      tile afterwards, and `pgrep -x DezhbanMenu | wc -l` is 1. Repeat
      immediately after an upgrade that runs the migration.
- [ ] **Launching a fully-started app again just reopens its window.** With the
      app already running from a `--background` login launch, launch it from
      Finder. LaunchServices will not start a second copy of a running bundle, so
      this never reaches the session lock — it is
      `applicationShouldHandleReopen`, which opens the window in **every** "Open
      minimized" mode, on purpose: the preference governs the launch, and must
      never make the window unreachable. Do not expect "Always" to suppress it
      here; the hand-off path is exercised by the startup-race check below, which
      is the only way to reach it.
- [ ] **The first logout after upgrading may open the window once — and only
      once.** Expected, not a regression:
      [ADR-0014](../adr/0014-login-item-launch-marker.md) records why
      `NSApp.disableRelaunchOnLogin()` cannot cover the logout that happened before
      the new build ever ran. Upgrade, log out, log back in: a window here is
      acceptable. Log out and back in a *second* time — there must be none, and
      `pgrep -x DezhbanMenu | wc -l` must be 1.
- [ ] **"Reopen windows when logging back in" does not start a second, unmarked
      copy.** Check that box in System Settings → Desktop & Dock, leave the app
      running, log out and back in. Exactly one copy must be running and it must
      have come from the login agent, not the resume: `ps -o args= -p "$(pgrep -x
      DezhbanMenu)"` ends in `--background`, and under the default "Only at
      login" there is no window. This is `NSApp.disableRelaunchOnLogin()`; without
      it, LaunchServices relaunches the app with no arguments and races the agent
      for the lock.
- [ ] **A refusal's explanation survives switching away and back — and expires
      when it stops being true.** Run `dist/Dezhban.app`, click "Open this app at
      login" — it refuses and says why. Click another app and click back: the
      explanation must still be there. Only messages about a moment that has passed
      ("macOS is holding this for your approval") may be cleared by that refresh; a
      switch that snapped back with the reason erased is indistinguishable from a
      bug. Then clear the condition and return — once the switch moves, the stale
      refusal must go with it rather than sit there contradicting it.
- [ ] **The login toggle's result has its own line.** Start the service toggle
      ("Start the guard at boot"), and while its privileged sequence is running flip
      "Open this app at login". Both messages must be readable at once — the install's
      progress on the pane's status line, the login result underneath the toggle —
      and neither may erase the other.
- [ ] **Two hand-offs in quick succession both open the window.** Only reachable
      while the incumbent is still starting — once it is up, LaunchServices reopens
      rather than launching a duplicate, so there is no hand-off to debounce. Log
      out and back in, then double-click the app twice in quick succession as early
      as you can, closing the window (⌘W) in between. Both must open. Best-effort
      by nature: the deterministic coverage is
      `HandoffRequestTests.anOverlappingClaimerIsToldItLost` and
      `theClaimCarriesThePostedToken`, since it is the token — not any timing rule —
      that tells the two signals for one request from two requests.
- [ ] **A hand-off that arrives before the app is observing still works.** The
      race the `HandoffRequest` file exists for: log out and back in and
      double-click the app in `/Applications` as early as you can, while it is
      still starting from the login agent. The window must open — within about
      half a second if the notification missed it, from the bounded backstop that
      runs for the first few seconds. This must hold on a *slow* login too: the
      request is honoured however long the incumbent took to finish starting,
      because the file can only have been written after it took the lock. It must
      open **once**: no second activation a moment later, and a window you close
      right after must stay closed. Then
      confirm no `.handoff` file is left in `~/Library/Application
      Support/com.behnam-rk.dezhban.app/`.
- [ ] **An app filed into a subfolder of /Applications still migrates.** Move
      `Dezhban.app` into `/Applications/Utilities/`, launch it on a pre-agent
      install with login-at-launch on: it must migrate. Anywhere under an
      Applications directory counts as a place the app will stay; comparing only the
      immediate parent left that user's legacy item running with no marker
      permanently, reported nowhere.
- [ ] **A copy run from outside /Applications cannot claim *or release* the login
      item.** Run `dist/Dezhban.app` and switch "Open this app at login" on: it must
      refuse, with the line naming where it is running from, and
      `launchctl print gui/$UID/com.behnam-rk.dezhban.app.login` must still fail.
      Only the registering bundle can ever retract a registration, so a dev build
      that claimed it would leave an orphan nothing can remove once `dist` is
      rebuilt.

      Then the other direction, which matters more because it is silent: with the
      *installed* copy's login item **on**, run `dist/Dezhban.app` — the switch reads
      ON, since the agent is registered under a label every copy shares — and click
      it **off**. It must refuse, and `launchctl print
      gui/$UID/com.behnam-rk.dezhban.app.login` must still succeed afterwards. A dev
      build retracting the installed app's registration is bad enough; doing it
      without even recording the user's "off" is worse.
- [ ] **A copy run from outside /Applications does not migrate the login item.**
      Unzip `Dezhban-macos.app.zip` to `~/Downloads` on a Mac with a pre-agent
      install and login-at-launch on, run it once, quit. The legacy login item
      must still be there and `defaults read com.behnam-rk.dezhban.app
      dezhban.loginItemMigratedToAgent` must be absent — otherwise the account is
      marked done with the agent pointing into `~/Downloads`. Then run the copy in
      `/Applications`: that one must migrate.
- [ ] **A login item the user turned off in System Settings stays off across an
      upgrade — and is cleared, not left dormant.** With a pre-agent build, switch
      Dezhban off under System Settings → General → Login Items (this leaves
      `mainApp` at `.requiresApproval`, not unregistered), then upgrade and launch.
      Login-at-launch must still be off and no agent registered — *and* the entry
      must be gone from Login Items. A dormant `.requiresApproval` item is live:
      re-approving it there would start the app at login with no marker, and the
      migration will not run again to fix it.
- [ ] **Two quick clicks on the login switch settle on the second one.** Click it
      off then immediately on (and the reverse). The final switch state must match
      the last click *and* the actual registration —
      `launchctl print gui/$UID/com.behnam-rk.dezhban.app.login` agreeing with what
      the switch shows. Then reopen the pane to confirm it still agrees.
- [ ] **Switching login-at-launch off from a login-started session.** Log out
      and back in so the agent starts the app, then switch Settings → "Open this
      app at login" **off**. `SMAppService.unregister()` unloads the launchd job
      and launchd terminates a loaded job's process — which here is the app — so
      watch for the app quitting instead of showing the status line. Recorded as
      an open risk in [ADR-0014](../adr/0014-login-item-launch-marker.md); if it
      reproduces, note whether the registration was still retracted.
- [ ] **The migration retries a failed agent registration.** Hard to provoke
      honestly: with a pre-agent install and login-at-launch on, make
      `register()` fail once (an unsigned bundle is the easiest way), launch, and
      confirm the log says it will retry. Then fix the bundle and launch again —
      the agent must register. `defaults read com.behnam-rk.dezhban.app
      dezhban.loginItemMigratedToAgent` must be absent or 0 between the two.
- [ ] **An awaiting-approval registration is explained on a fresh pane open.** With
      the agent registered but switched off under System Settings → General → Login
      Items, quit Dezhban and reopen Settings *without touching the switch*. It reads
      ON (an awaiting-approval registration counts as registered), so the line
      explaining that must be there unprompted — nothing else can express the
      difference.
- [ ] **The uninstall errand does not cry wolf.** On an install where
      login-at-launch was never switched on, run
      `Dezhban.app/Contents/MacOS/DezhbanMenu --unregister-login-item; echo $?` — it
      must be **0**. `SMAppService` reports `.notFound` for an agent that was never
      registered, not only for a plist it cannot resolve, so anything treating that
      status as "cannot tell" warns on every ordinary uninstall.
- [ ] **The approval prompt's own guidance survives it.** Turn the login item off
      *in System Settings*, then switch Dezhban's "Open this app at login" on: the
      line says macOS is holding it for your approval. Click away and back **without
      approving** — the line must still be there, because the switch reads ON either
      way and cannot show the difference. Then approve it and return: the line must
      go.
- [ ] **An awaiting-approval registration can still be switched off.** Turn the
      login item off *in System Settings* (not in Dezhban), then switch Dezhban's
      "Open this app at login" on: the status line must say macOS is holding it
      for approval, and the switch must then turn **off** again on the next click
      rather than re-registering. `launchctl print
      gui/$UID/com.behnam-rk.dezhban.app.login` must fail afterwards.
- [ ] **The dev build is not deduped against the installed one.** With
      `/Applications/Dezhban.app` running, `task gui:build && open
      dist/Dezhban.app`. Both must run — the lock is keyed on the bundle path
      precisely so every other manual check on this list tests the build you just
      made rather than silently testing the installed copy.
- [ ] **The login item is attributable.** System Settings → General → Login
      Items shows the entry as **Dezhban**, not as
      `com.behnam-rk.dezhban.app.login` (that is `AssociatedBundleIdentifiers`
      doing its job — this is the switch a user reaches for to stop the app
      starting at login, and it is useless if nobody can tell what it governs).
- [ ] **No `.claiming-*` files accumulate.** After exercising hand-offs, and after a
      forced quit during one, `ls -a ~/Library/Application Support/com.behnam-rk.dezhban.app/`
      must show no `.claiming-*` leftovers — a process killed between the rename and
      the read leaves one, and only the next session owner's sweep removes it.
- [ ] **Uninstall handles two installs.** Put a copy in `/Applications` *and* one in
      `~/Applications`, let the second register the login agent, then run the
      uninstaller. Both bundles must be gone, the agent registration must be
      retracted (`launchctl print gui/$UID/com.behnam-rk.dezhban.app.login` fails,
      still after a reboot), and no clean-removal message may print over a survivor.
      Only the registering bundle can retract, so each copy's errand has to run from
      that copy while it still exists.
- [ ] **Uninstall finds the app where it actually is.** Move `Dezhban.app` into
      `/Applications/Utilities/`, let it register the login agent, then run the
      uninstaller. It must locate the bundle there, retract the agent, delete it,
      and print **no** "app bundle was already gone" warning — the app is allowed to
      register from anywhere under an Applications directory, so the uninstaller has
      to look in the same places.
- [ ] **Uninstall with nobody logged in says so.** From an ssh session on a Mac
      sitting at the login window, run the uninstaller. It must finish *and* warn
      that the per-user leftovers could not be removed — every step of that
      teardown needs the user's own launchd session, and reporting a clean removal
      would hide both a surviving login item and the migration flag that makes a
      later reinstall skip the migration.
- [ ] **Uninstall over an already-trashed app says so.** Drag
      `/Applications/Dezhban.app` to the Trash, then run the uninstaller. It must
      finish *and* print the warning naming System Settings — only the app can
      retract its own registration, so with the bundle gone the entry cannot be
      removed by anything and reporting a clean uninstall would hide it.
- [ ] **Uninstall retracts the registration, not just the running job.** With
      login-at-launch on, run `sudo sh /usr/local/share/dezhban/uninstall.sh`,
      then confirm `launchctl print gui/$UID/com.behnam-rk.dezhban.app.login`
      fails **and** the System Settings → General → Login Items entry is gone,
      **and** that it is still gone after a reboot. A per-user launchd agent does
      not go away with its bundle the way a LaunchServices login item did, and
      `launchctl bootout` alone only unloads it for the current boot — the
      reboot is what distinguishes a real retraction (the
      `--unregister-login-item` errand the script runs as the console user) from
      an unload that comes back. `defaults read com.behnam-rk.dezhban.app` must
      also fail afterwards: a surviving migration flag means a later install is
      never moved onto the agent. And if macOS *does* refuse the retraction, the
      script must say so — the closing warning naming System Settings, not a clean
      "files deleted".
- [ ] **The login agent registers from an ad-hoc-signed build.** Only reachable
      on a real install: `build-app.sh` signs with `codesign -s -`, and
      `SMAppService.agent` registration goes through launchd's validation of the
      bundle, which `SMAppService.mainApp` never needed. If ad-hoc does not
      satisfy it, login-at-launch fails as a silent `.notFound`/`.requiresApproval`
      status rather than a crash — so check
      `launchctl print gui/$UID/com.behnam-rk.dezhban.app.login` on a build
      installed the way users get it (`.pkg` or the app zip), not on
      `dist/Dezhban.app` run in place.
- [ ] **Posture tracking.** Drive the daemon with `--simulate-country IR` / `US`
      and confirm the menu bar icon *and* the Dock tile flip red/teal and the
      window's Overview updates within ~1 s.
- [ ] **Auto-arm (`vpn.autoArm: true`).** Start the daemon with the VPN off →
      posture `standby`, egress open, gray icon. Connect the VPN → `guard`
      within a few seconds ("AUTO-ARMED" in the log) and a "Guard armed"
      notification. Disconnect → guard HOLDS (red blocked icon, egress cut).
      **Unblock** (menubar or Overview) → back to `standby`, egress open.
      Redial → arms again.
- [ ] **Essential notifications.** With notifications on (Settings pane), the
      armed/blocked/warning/standby/stopped transitions each notify once; no
      notification at app launch or on routine country/endpoint updates.
- [ ] **Staleness.** Kill the daemon → the icon goes gray after the 90 s staleness
      window, and Overview switches to the guided "Stopped" state.

### Actions

- [ ] **The action row explains itself before the click.** Titles are short
      (Block / Unblock / Switch VPN… / Pause / Guard down). Panic… is *not* part of
      this row — it sits below with its own fixed caption, so hovering it changes
      nothing, which is correct rather than a failure. Move the pointer across the
      row: the caption line beneath it changes to that
      control's sentence — in particular, hovering **Pause** must say it uses
      your real ISP IP, which is the warning its old title carried — and the whole
      sentence must be readable, including the password expectation at its tail,
      which is what a single truncated line used to cut. Moving off the row restores
      the posture headline.

      Tab through the row with Full Keyboard Access on and confirm focus drives the
      caption too, **including with the pointer left resting on a different
      button** — focus is meant to supersede a parked pointer. Then move the pointer
      onto a control (the same one or another) and confirm it takes over again:
      re-entering is what hands it back, deliberately, since jiggling inside the
      control you are already on aims at nothing new. The line must never go blank or change
      height, which would reflow the row under the pointer.

      Two more, both about a caption outliving what it described. With the pointer
      parked on **Pause**, open a switch window from the menubar: the button under
      the pointer becomes **Cancel**, and the caption must stop describing Pause
      without waiting for the mouse to move. And with the pointer parked on
      **Block**, stop the daemon: the caption's password clause must follow the
      tooltip's rather than keeping the answer it was given at hover-enter.
- [ ] **VoiceOver still hears what each button does.** With VoiceOver on, move
      through the action row: each control announces its short title *and* its
      consequence as a hint — Cancel in particular must say whether it closes the
      automatic redial window or one you opened, since the titles no longer carry
      that and the caption line is hidden from VoiceOver to avoid reading it twice.
- [ ] **The degraded states keep their long panic title.** With the CLI missing
      or the service not installed, the panic button still reads "Panic — force
      unblock…" — there is no caption line there to carry the explanation.
- [ ] **Routine ops are passwordless with a live daemon.** Block/Unblock and the
      switch window complete over the control socket with **no** prompt, from both
      the menubar and Overview; the switch countdown ticks in both surfaces and
      matches.
- [ ] **Pause and Resume, from both surfaces.** Pause opens with no password
      (`control.allowPauseOps` default true); Overview shows "Resume (m:ss left)"
      and the menubar "Resume now (m:ss left)" in place of the switch-window
      Cancel item, and the countdown agrees between the two. Resuming early re-arms the guard immediately.
      Letting a pause expire re-arms it with no action needed. With
      `vpn.pauseMax: "0"`, Pause is disabled in both surfaces with a reason
      ("vpn.pauseMax is \"0\""), not just a silent no-op.
- [ ] **Profile picker.** With `configs/dezhban.profiles.json`, Overview's
      details grid lists every configured profile and marks the one that
      matched (`(active)`), matching `dezhban vpn list`; "Switch VPN…"
      becomes a menu with "Any known VPN" plus one item per profile, and
      picking a profile passes `--name <profile>` (`dezhban vpn list` shows the
      learned endpoint attributed to it afterward). With no profiles
      configured, it's a plain button, not a one-item menu.
- [ ] **Privileged actions.** Start/Stop raise a native admin prompt (Touch ID or
      password), run, and the state reflects the result.
- [ ] **Menubar panic works without the window.** From a fresh launch (main window
      never opened): Panic shows a confirmation, confirming removes the rules and
      the transcript appears in an alert; cancelling does nothing.
- [ ] **Window panic** routes its transcript to the Logs pane and
      navigates there.
- [ ] **Failures are visible, not silent.** Move the CLI binary aside (or invalidate
      the config), then trigger Start/Stop → the alert shows real stderr.

### Elevation & Touch ID

These need a Mac with Touch ID and `pam_tid` enabled in `/etc/pam.d/sudo_local`.
CI cannot run any of them — a biometric prompt has no headless form — and the
failure they guard against is precisely the one that made every Touch ID user
end up typing a password.

- [ ] **A Touch ID miss offers a password, not a dead end.** Trigger a privileged
      action (Start/Stop), then deliberately fail the biometric read three times
      (a non-enrolled finger works). Expect the bundled askpass dialog asking for
      the administrator password, and the action completing after a correct one.
      **Before the `SUDO_ASKPASS` fix, the first miss dropped straight to a
      password-only Authorization Services dialog.**
- [ ] **Cancelling means cancelled.** Dismiss the Touch ID prompt, then dismiss
      the password dialog. The action must report failure and change nothing — not
      silently retry, and not leave a half-applied state.
- [ ] **Clamshell / no sensor.** With the lid closed on an external display, a
      privileged action still reaches a usable password prompt.
- [ ] **The timestamp cache still works.** Two privileged actions in quick
      succession prompt once, not twice.
- [ ] **No `pam_tid`, no regression.** Comment out `pam_tid` in
      `/etc/pam.d/sudo_local` → privileged actions fall back to the system
      Authorization Services dialog exactly as before, not to the askpass dialog.
- [ ] **The askpass helper is where it should be.** `ls -l
      /Applications/Dezhban.app/Contents/Resources/askpass.sh` is present, mode
      `0755`, and inside the bundle — never a user-writable path, since sudo
      executes whatever `SUDO_ASKPASS` names.

### Overview degraded states

- [ ] CLI binary moved aside → Overview explains "dezhban CLI not found" (and the
      menubar status line agrees); restore it → recovers on next refresh.
- [ ] Service uninstalled → "Guard not installed" with an inline **Install service…**
      that installs + starts under one prompt and shows its transcript in Logs.
- [ ] Service installed but stopped → "Stopped" with an inline
      **Guard up**.

### Settings pane

- [ ] **Start at boot** reflects whether the service is registered, flips after
      install/uninstall (one prompt each, uninstall confirms first), and the
      uninstall tears rules down before unload.
- [ ] **Launch at login** — the login-item checks live with the launch-marker
      block earlier in this file, since they are the same mechanism; the switch
      registers the *agent* and a correct run leaves `SMAppService.mainApp`
      unregistered.
- [ ] Guard fields seed from `dezhban config show` values; Apply raises the
      restart-warning choice; "Save only" writes without restarting.
- [ ] **Restart dezhban…** works with nothing else pending: a plain "are you
      sure?" during GUARD or STANDBY, but a stronger, `.critical` warning during
      FULL BLOCK or an open switch/redial/pause window that says enforcement is
      briefly lifted and your real IP may be exposed. Cancelling changes nothing.
- [ ] **Open Config File…** opens the resolved config path.
- [ ] **Preset picker.** With a freshly-installed (default) config, Balanced
      shows checked and its summary/cost text; clicking Strict shows the cost
      in the confirmation, and after applying, Strict is checked and Balanced
      is not. Hand-edit one key afterward (e.g. `pollInterval`) and reopen the
      pane — no preset is checked, "Custom" shows, and the disclosure lists
      exactly the keys that differ from the nearest preset (matches `dezhban
      config preset diff`).
- [ ] **Advanced disclosure.** Collapsed by default; expanding it seeds from
      `dezhban config show`'s `vpn.advanced` block, and Apply writes changes
      there through the same batched write as every other field (one prompt,
      not a second one for Advanced).

#### Use Touch ID for settings changes

- [ ] **Opening the pane never prompts.** The toggle shows the right state
      without a biometric prompt — enrollment is checked without reading the
      token, and reading it is the only thing that should ever prompt.
- [ ] **Turning it on** asks for a password once, then reports success. `dezhban
      token status` in a terminal agrees that a token is enrolled.
- [ ] **Applying a change then costs a Touch ID tap, not a password**, and the
      change is in force when the pane says "Saved and applied".
- [ ] **Cancelling the Touch ID prompt falls back to the password path** rather
      than failing the save — a cancelled biometric is not a refusal.
- [ ] **A daemon refusal is not escalated.** Set `control.allowConfigOps: false`
      and restart; a save reports the refusal and must NOT then show an admin
      password prompt that would perform it anyway.
- [ ] **Turning it off removes both copies.** The toggle goes off, `dezhban token
      status` reports "not enrolled", and saves ask for a password again.
- [ ] **Changing your fingerprints does NOT invalidate the stored token.** Add or
      remove a fingerprint, then save → it still costs a Touch ID tap and still
      succeeds. `.biometryCurrentSet` is gone with
      [ADR-0012](../adr/0012-app-checked-biometrics-on-unsigned-builds.md) — the
      keychain item is ordinary and the check is the app's, so there is nothing
      for a fingerprint change to invalidate. Adding a finger already needs the
      login password, which already grants `sudo`. Recorded here so the loss is
      re-confirmed on each pass rather than rediscovered as a surprise.
- [ ] **On a Mac without Touch ID** the toggle is disabled and explains why;
      settings changes keep working through the password path.

### VPN Guard pane

- [ ] Opening the pane with the service stopped seeds values matching
      `dezhban config show`.
- [ ] Applying a valid change raises the restart-warning modal, then: the `config
      set` calls land, the icon goes ⚪ across the stop/start gap, and it resolves
      to 🟢/🔴 for the new mode.
- [ ] **A change that fails cross-field validation is refused *before* any
      restart** — e.g. a profile with no valid endpoint. No stop/start may
      happen on a config that would fail to start.
- [ ] Killing the daemon mid-restart makes the pane report failure, not success.
- [ ] One prompt per apply — not one per field.

### Diagnostics pane

- [ ] Rows and their statuses match a hand-run `dezhban doctor --config …`;
      checking "Find my VPN's server" matches `dezhban doctor --discover`.
- [ ] A `fail`/`warn` row's fix text is readable and matches the fix text
      `dezhban doctor` prints in a terminal.
- [ ] CLI missing → the guided "dezhban CLI not found" state, not a blank list.

### Help pane

The pane's whole reason for existing is that it works while the guard has cut
traffic, so the check that matters is the one CI cannot run: with egress gone.

- [ ] With **FULL BLOCK active** (or the tunnel down and no window open), open
      Help: pages render fully, the sidebar and search work, and nothing is
      blank or spinning. Confirm no request leaves the machine — Little Snitch,
      or `tcpdump`/`pfctl -s state` showing nothing new from `DezhbanMenu`.
- [ ] A link in a page to another bundled page moves the sidebar selection with
      it, so the highlighted row is the page being read.
- [ ] A link that points off the bundle (an `https://` one in a doc) is refused
      in the pane and reported with a Copy link button — it must not navigate.
- [ ] A link to a doc that is **not** bundled — the ADR references in Postures,
      say — reports a `https://github.com/…` URL, not a `file:///…/Contents/…`
      path. Every such link was a dead click reporting an internal path before
      the renderer rewrote them.
- [ ] Pages are **styled** — headings, table borders, code backgrounds, and the
      dark-mode palette. The bundled pages carry a `Content-Security-Policy`, and
      a `file:` origin is opaque, so a CSP that is too strict would silently drop
      `help.css` and leave the pane readable but unstyled. Only a real WKWebView
      shows this; the Go tests cannot.
- [ ] Built from a checkout whose `docs/` was renamed under it, `task gui:build`
      **fails** rather than producing an app whose Help pane is missing a page.
- [ ] The **?** beside a Settings field opens Help scrolled to **that key's own
      table row** — not to the section heading it shares with dozens of other
      keys, and not to the top of the reference. Spot-check one field per
      section, including one under Advanced (whose rows are anchored on the
      fully-qualified `vpn.advanced.*` name). Its **tooltip** says what the button
      does ("Open the documentation for …"): the key's own one-line help is already
      on the control beside it, so repeating it here left nothing telling a pointer
      user that the button navigates at all.
- [ ] **A CLI newer than the bundled help still lands on the section.** The **?**
      offers the key's row first and its section second, so an app bundle predating
      row ids must land on the section heading rather than the top of the page.
      Reproduce by running the built app against a help bundle from before this
      change, or by checking that `dezhban config schema` prints a `docs:` anchor
      that resolves as a heading on GitHub — that is the one a CLI reader follows,
      and row ids exist only in the app's rendered help.
- [ ] Against a CLI too old to know `config schema`, the **?** buttons are absent
      rather than present and inert.

### Logs pane

- [ ] "Show last hour" matches a hand-run `log show --last 1h --predicate
      'process == "dezhban"'`. "Stream live" updates live; Stop — or closing the
      window mid-stream — ends the child process (no orphaned `log stream` in `ps`).
- [ ] **About** shows a version matching `dezhban version` and paths matching
      `dezhban config path`.

## Known gaps

These are deliberate, not oversights:

- **Code signing / notarization.** The `.pkg` and the app are unsigned (no Apple
  Developer certificate); `build-pkg.sh` carries the signing seams. Gatekeeper
  needs a right-click → Open on first launch.
- **SMJobBless privileged helper.** Not implemented; the app elevates per action
  through Authorization Services instead (which does cache, so consecutive actions
  are usually silent).
- **Offline mmdb country lookup.** Deferred — country resolution is online-only.
- **The app's AppKit/SwiftUI layer (`DezhbanMenu`) is untested.** Only the pure
  layer split into `DezhbanCore` (Snapshot decoding, posture→icon derivation,
  settings-field batching) has a test target; the views, elevation, and CLI
  shell-out are still verified only by the manual checklists above.
