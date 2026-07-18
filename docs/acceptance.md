# Acceptance checks

dezhban is code-complete: every feature described in [modes.md](modes.md) and
[usage.md](usage.md) is implemented and covered by `go test ./...`. What remains
is **privileged, on-host verification** — the checks that need root and a real
firewall, and therefore cannot run in CI.

This file is the standing checklist. Work through the section for the OS you are
on; check the boxes as you go. The macOS block/guard walkthrough is long enough to
live on its own — see [testing-macos-block.md](testing-macos-block.md) for the
step-by-step version with expected output at each step.

> **Run these on a host you can afford to lock out of.** Every check below arms a
> real kill switch. Keep a second terminal open and know the escape hatch:
> `sudo dezhban panic` removes all rules with no daemon running. See
> [troubleshooting.md](troubleshooting.md).

## Automated (no root) — run first

These gate everything else and should be green before you touch a firewall:

```sh
go build ./... && go vet ./... && go test ./...
GOOS=linux go build ./... && GOOS=windows go build ./...
```

## Enforcement — all platforms

- [ ] **Block cuts egress, allowlist survives.** `sudo dezhban block` → general
      egress dies (`curl https://example.com` fails) but loopback works, DNS
      resolves, and the configured geo-API host is still reachable.
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

## Country-blocklist fallback (`vpn.enabled: false`)

- [ ] **Blocklist trips.** Set `blockedCountries` to your *own* current country →
      `sudo dezhban run` → within N ticks egress is cut and the log shows
      `BLOCKING (country=XX)`.
- [ ] **Recovery.** Remove your country from the list and restart → `ALLOWING`,
      connectivity returns.
- [ ] **Clean shutdown.** `Ctrl-C` while blocked → `Cleanup()` runs, connectivity
      is restored, exit 0.
- [ ] **Fail-closed on lookup failure.** Blackhole every provider host (e.g. via
      `/etc/hosts`) while running → within N error-ticks the firewall blocks, but
      loopback, DNS, and the provider allowlist stay open so recovery can fire.
      Restore `/etc/hosts` → it recovers.
- [ ] **No flapping.** An alternating country sequence must NOT toggle the
      firewall until `hysteresis` consecutive readings agree.
- [ ] **Quorum.** With three providers and one disagreeing, the majority wins and
      a warning is logged.

## VPN interface guard (`vpn.enabled: true`)

The guard is the dangerous mode — a misconfiguration locks the host out. Run
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
      livelock the reconnect.
- [ ] **Unblock restores everything.**

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
      reconnecting to that VPN needs no window at all.
- [ ] **Import.** `dezhban vpn import` extracts the expected hosts from WireGuard,
      OpenVPN, and V2Ray configs — stripping ports, dropping private/loopback
      addresses, and rejecting garbage.
- [ ] **Dynamic tunnels.** A newly-appeared tunnel is guarded within one watcher
      tick, with no restart. Zero tunnels up = endpoints-open standing posture,
      with geo suppressed.

**Full live macOS pass:** `setup` → connect VPN A (guarded) → disconnect →
`dezhban switch` → connect self-hosted VPN B → the window learns the endpoint and
closes → `vpn promote` → reconnect to B with **no** window → `--simulate-country
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
      Reconnect → arms again.
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
      restart** — e.g. `vpn.enabled=true` with empty `endpoints`. No stop/start may
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
