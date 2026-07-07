# Enforcement modes

> Prefer a styled, single-page view (state-machine diagrams + rulesets)? Open
> [modes.html](modes.html) in a browser.

dezhban enforces in one of two modes. They are **not** two flavours of the same
thing — they use different firewall primitives and behave differently *even when
nothing is wrong*. The whole distinction lives in the **resting state**:

| | **VPN guard** (primary) | **Country-blocklist** (fallback) |
|---|---|---|
| Config | `vpn.enabled: true` | `vpn.enabled: false` |
| Blocks by | network **interface** (which iface may egress) | **destination** IP (an allowlist) |
| Watches | tunnel up/down + VPN exit country | public IP's country |
| Rules when healthy | **GUARD always applied** — only the tunnel may egress | **none** — network fully open |
| Leak window if the VPN drops | **zero** (guard is always on) | full leak — it is blind to the tunnel |
| Requires a VPN | yes | no |

## VPN guard — the primary, recommended mode

If you are behind a full-tunnel VPN, this is the mode you want. Under a full
tunnel the firewall on the physical interface sees only encrypted outer packets to
one address — the VPN endpoint — so a destination-IP allowlist is meaningless. The
guard instead controls the **interface**, and its baseline is applied
**continuously at startup**, before the first poll. Two states:

- **GUARD** (exit allowed — the healthy resting state) — pass loopback + tunnel
  egress + the endpoint handshake; block all other physical egress. Because this
  is always on, a tunnel drop is cut with a **zero leak window** — traffic can
  never fall back to your real interface.
- **FULL BLOCK** (exit forbidden / country unknown) — drop the tunnel-egress pass
  so no user traffic can reach a forbidden exit, but **keep the endpoint handshake
  open** so the encrypted transport survives and the tunnel can reconnect (a cut
  endpoint would livelock the reconnect). It is GUARD minus the tunnel-egress pass.
  Recovery uses a time-windowed probe: each tick the guard is briefly lifted for
  one geo lookup through the tunnel, then re-cut — the tunnel transport is never
  torn down across the re-cut, so a genuinely-down tunnel can come back and a later
  probe observe an allowed exit.

```
# GUARD  — pass quick on lo0 all no state
#          pass out quick on { utun4 } all no state
#          pass out quick to { <vpn endpoint> } no state
#          block drop out all
#
# FULL BLOCK — pass quick on lo0 all no state
#              pass out quick to { <vpn endpoint> } no state   # reconnect path
#              block drop out all
```

Enable it with the tunnel interface(s) and VPN endpoint IP(s) — see the `vpn`
block in [config.md](config.md). Find your tunnel interface with:

```sh
dezhban detect-vpn          # detected tunnel iface(s) + a paste-ready vpn block
```

`detect-vpn` deliberately does **not** autodetect the endpoint — a wrong endpoint
would leak physical egress — so set `vpn.endpoints` from your VPN client's own
config (or use `autoDiscoverEndpoints` on macOS). A wrong or tunnel-internal
endpoint is the #1 lockout cause; [troubleshooting.md](troubleshooting.md) has the
runbook. `panic` tears down both GUARD and FULL-BLOCK rules.

**Fail-closed in guard mode.** In guard mode the standing GUARD rule is itself
the fail-closed block for physical leaks, so an **undeterminable** country
*holds* the current posture — it never escalates GUARD→FULL BLOCK. Escalating on
an unknown would cut the tunnel's own egress and livelock the reconnect. Only a
*successful* reading of a blocked country produces FULL BLOCK. (`failClosed`
still governs the fallback/legacy model below.) If your endpoints are hostnames,
set `vpn.allowPhysicalDNS: true` so the client can re-resolve its server while
the tunnel is down.

## Country-blocklist — the fallback mode

For hosts **not** behind a full tunnel. dezhban watches your public IP's country
and, when it matches `blockedCountries`, cuts egress by destination — keeping DNS
and the geo-API providers open so recovery can still fire. Two states:

- **ALLOW** (country not blocked — resting) — **no rules at all**; the network is
  fully open.
- **BLOCK** (country blocked, or unknown while `failClosed`) — default-deny with
  only the allowlist open.

```
# ALLOW  — ∅  no rules installed
#
# BLOCK  — pass quick on lo0 all no state
#          pass out quick proto { udp tcp } to { <dns> } port 53 no state
#          pass out quick to { <geo-API provider IPs> } no state
#          block drop out all
```

This mode is **best-effort, not a zero-leak guarantee.** It is *reactive*: a
transition to a blocked country commits only after `hysteresis × pollInterval`
agreeing readings, and while the country looks fine it applies no rules at all.
Lowering `pollInterval` (even to a few seconds) shrinks the window but **cannot
close it** — there is always a gap between a change and the next poll. Only the
always-on VPN guard has a leak window of zero by construction.

## Which mode do I want?

The deciding question:

> **If your VPN silently drops, do you care that traffic keeps flowing on your
> real connection — exposing your real IP?**

- **Yes** → **VPN guard**. GUARD is always on, so a drop is cut in **0 seconds**.
  This holds regardless of whether your provider ever routes you through a blocked
  country — that only affects whether FULL BLOCK ever fires; otherwise you simply
  stay in GUARD, and lose nothing by choosing this mode. (Keep `blockedCountries`
  listed anyway as a free backstop.)
- **No** → **Country-blocklist**. Only meaningful if the country you block is your
  **real physical location** — on a VPN drop it blocks late (real country blocked)
  or never (real country allowed).

Don't run VPN mode *without* a VPN: GUARD needs a tunnel to pass traffic through,
or you have no connectivity. This is why `vpn.enabled` defaults to `false` — it is
a **safety opt-in** (a misconfigured guard, or one with no tunnel, can lock a host
out), not a statement that the fallback is the normal mode.

## Preview any ruleset without applying it

No root, no firewall changes:

```sh
dezhban print-rules --mode guard     --config <config>
dezhban print-rules --mode fullblock --config <config>
dezhban print-rules --mode legacy    --config <config>
```

> The `guard` / `fullblock` / `legacy` mode names are the stable identifiers used
> by `print-rules` and the state file; "primary" and "fallback" are descriptive
> only.
