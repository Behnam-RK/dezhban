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
```

## Enforcement — all platforms

- [ ] **Block cuts egress.** `sudo dezhban block` → general egress dies
      (`curl https://example.com` fails) but loopback works, DNS resolves (with
      `vpn.allowPhysicalDNS` on, the default), the VPN endpoint stays reachable
      so the tunnel can redial, and LAN devices still answer (with
      `vpn.allowLocalNetwork` on, the default). This is FULL BLOCK — it carries
      **no** destination allowlist; a VPN posture opens the tunnel endpoint.
- [ ] **`block --force` keeps the geo providers reachable.** `sudo dezhban block
      --force` → all egress cut except loopback and the resolved geo-API
      provider IPs, which it pins on the *physical* link before cutting so
      recovery detection still works with no daemon and no tunnel.
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

Per-OS rule inspection:

| OS | Inspect the ruleset with |
|---|---|
| macOS | `sudo pfctl -a dezhban -s rules` |
| Linux | `sudo nft list ruleset` — only the `dezhban` table should appear |
| Windows | WFP filter dump — rules under the `dezhban` group/sublayer |

## Standby and the single-mode merge

- [ ] **Fresh install, no VPN configured** → posture `standby`, **no rules
      installed** (the inspect command above shows nothing for the `dezhban`
      table/anchor), network fully open, menubar icon grey, Overview says it is
      not protecting.
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
- [ ] **`restart` applies a config change** (there is no live reload), and `start`
      and `stop` are idempotent.

## Upgrade

macOS only, privileged (`dezhban upgrade download`/`apply`). See
[upgrade.md](../usage/upgrade.md) for the full design.

- [ ] **Tunnel down.** `dezhban upgrade check` with the tunnel down fails
      cleanly and opens nothing — it inherits the guard's tunnel-only routing
      rather than getting its own firewall pass.
- [ ] **Deferred activation during FULL BLOCK.** With the guard in FULL
      BLOCK, `dezhban upgrade apply` installs the payload, refuses to
      activate, and leaves the old daemon enforcing normally.
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

## macOS app

Build and launch:

```sh
task gui:build && open dist/Dezhban.app
```

### Surfaces & window lifecycle

- [ ] **Menubar is the safety core only.** The dropdown shows exactly: one status
      line, Open Dezhban… (⌘O), Block now, Unblock, the switch item (VPN mode),
      Panic — force unblock…, Quit. Nothing else.
- [ ] **Window opening.** "Open Dezhban…" and a Dock-icon click both open/focus
      the main window; a fresh app launch opens **no** window (menubar + Dock
      only); closing the window (⌘W) leaves the app and icon running.
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
      window, and Overview switches to the guided "Protection stopped" state.

### Actions

- [ ] **Routine ops are passwordless with a live daemon.** Block/Unblock and the
      switch window complete over the control socket with **no** prompt, from both
      the menubar and Overview; the switch countdown ticks in both surfaces and
      matches.
- [ ] **Privileged actions.** Start/Stop raise a native admin prompt (Touch ID or
      password), run, and the state reflects the result.
- [ ] **Menubar panic works without the window.** From a fresh launch (main window
      never opened): Panic shows a confirmation, confirming removes the rules and
      the transcript appears in an alert; cancelling does nothing.
- [ ] **Window panic** routes its transcript to the Logs & Diagnostics pane and
      navigates there.
- [ ] **Failures are visible, not silent.** Move the CLI binary aside (or invalidate
      the config), then trigger Start/Stop → the alert shows real stderr.

### Overview degraded states

- [ ] CLI binary moved aside → Overview explains "dezhban CLI not found" (and the
      menubar status line agrees); restore it → recovers on next refresh.
- [ ] Service uninstalled → "Not protecting" with an inline **Install service…**
      that installs + starts under one prompt and shows its transcript in Logs.
- [ ] Service installed but stopped → "Protection stopped" with an inline
      **Start kill switch**.

### Settings pane

- [ ] **Start at boot** reflects whether the service is registered, flips after
      install/uninstall (one prompt each, uninstall confirms first), and the
      uninstall tears rules down before unload.
- [ ] **Launch at login** toggles `SMAppService.mainApp.status` to `.enabled`, and
      the app relaunches after a logout/login cycle.
- [ ] Protection fields seed from `dezhban config show` values; Apply raises the
      restart-warning choice; "Save only" writes without restarting.
- [ ] **Open Config File…** opens the resolved config path.

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

### Logs & Diagnostics pane

- [ ] **Diagnostics** match a hand-run `dezhban doctor --config …`.
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
