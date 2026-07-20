# Safety

dezhban is a kill switch; treat every `block` as potentially self-inflicting until
teardown is proven **on your machine**. This page is the principles. For recovering
from an actual lockout, go straight to the [troubleshooting.md](troubleshooting.md)
runbook.

> **The escape hatch is `sudo dezhban panic`.** It removes dezhban's tagged rules
> and restores the saved prior state, with no daemon running. It is idempotent and a
> no-op on a clean system. Know it before you need it.

## Before you arm it the first time

Do these in order, on a machine you are sitting in front of:

1. **Prove teardown before you prove blocking.** `sudo dezhban block`, then
   immediately `sudo dezhban unblock` (or `panic`), and confirm the rules are gone —
   on macOS, `sudo pfctl -a dezhban -s rules` should come back empty. If you cannot
   reliably tear down, do not go on.
2. **Inspect the rules without applying them.** `task rules MODE=guard CONFIG=…`
   prints the exact ruleset and touches nothing.
3. **Run the lockout check.** `dezhban doctor --discover` exists specifically to
   catch the misconfigurations that lock hosts out. Do not skip it before enabling
   the VPN guard.
4. **Watch it decide, without letting it act.** `dezhban monitor` shows live
   IP/country/tunnel/verdict and never touches the firewall.
5. **Then arm it.** `sudo dezhban run`.

The full step-by-step walkthrough, with the output you should expect at each step,
is [testing-macos-block.md](testing-macos-block.md).

## Principles

- **Test on the local console, not over SSH or a remote session** — a lockout
  should not also kill your way back in. This is the single most common way people
  turn a recoverable mistake into an unrecoverable one.
- **The allowlist must include** loopback (implicit), DNS, and the geo-API egress
  IPs. Without them, recovery can never fire and the block becomes permanent.
- **Allowlist IPs are pinned at block time.** A provider behind a rotating CDN may
  later resolve to an IP that isn't allowed, breaking recovery. Prefer providers with
  stable IPs, or pin a wide-enough `allowlist.hosts` range. (The `run` loop refreshes
  the allowlist live; a manual `block` is static.)
- **A wrong `vpn.endpoints` value is the #1 lockout cause.** If it is wrong, or
  points at a tunnel-internal address, the guard blocks the tunnel's own transport
  and drops the tunnel it was supposed to protect. Verify with `dezhban doctor
  --discover` *before* enabling. See [modes.md](modes.md).
- **Fail-closed means different things in the two modes,** and the difference is a
  safety property, not a detail: in the country-blocklist fallback an undeterminable
  country **blocks**; under the VPN guard it **holds** the current posture, because
  escalating on an unknown would cut the tunnel's own egress and livelock the
  reconnect. See [architecture.md](architecture.md#rules-that-must-not-be-broken).

## Who can relax the guard

The bounded **switch window** is the only sanctioned relaxation of the guard, and it
has exactly two sanctioned triggers: an explicit operator command (default 15s), and
the [automatic reconnect window](modes.md#automatic-reconnect-window)
(`vpn.reconnectWindow`, default 30s) that opens on a tunnel drop from healthy GUARD
so the VPN can redial — set it to `"0"` to make the window operator-only again.
Either way it is capped (5m) and auto-reverts to the prior fail-closed posture on
cancel or expiry.

By default it can be opened by any member of the `control.group` (macOS: `admin`)
over the control socket — **without a password**. That is a deliberate trade for
usability, and the cost is worth saying plainly: it means *any process running as an
admin user*, not just the human, can relax the guard for up to five minutes. If you
don't want that trade, `control.allowSwitchOps: false` forces the guard-relaxing op
back to root-only while keeping passwordless block/unblock. The full reasoning is in
[architecture.md](architecture.md#control-channels).

## Teardown mechanics

Teardown is surgical on every platform: each rule carries the unique tag `dezhban`,
so removal never touches rules dezhban did not create.

| OS | What `block` does | What `unblock` / `panic` does |
|---|---|---|
| macOS | Appends one `anchor "dezhban"` line to `/etc/pf.conf` (backing it up to `/etc/pf.conf.dezhban.bak` first) and loads rules into the kernel `dezhban` anchor | Flushes the anchor and restores the backup |
| Linux | Creates a dedicated `dezhban` nftables table | Deletes the whole table |
| Windows | Adds filters under a tagged WFP sublayer | Removes the sublayer |

Because the rules live in the kernel — not in the daemon's memory — **teardown works
even if the blocking process was killed.** That is what makes `panic` able to rescue
a machine whose daemon is long gone.
