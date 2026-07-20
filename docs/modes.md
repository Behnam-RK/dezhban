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
| Leak window if the VPN drops | **instant cut**, then a bounded [reconnect window](#automatic-reconnect-window) (zero with `reconnectWindow: "0"`) | full leak — it is blind to the tunnel |
| Requires a VPN | yes | no |

## VPN guard — the primary, recommended mode

If you are behind a full-tunnel VPN, this is the mode you want. Under a full
tunnel the firewall on the physical interface sees only encrypted outer packets to
one address — the VPN endpoint — so a destination-IP allowlist is meaningless. The
guard instead controls the **interface**, and its baseline is applied
**continuously at startup**, before the first poll. Two states:

- **GUARD** (exit allowed — the healthy resting state) — pass loopback + tunnel
  egress + the endpoint handshake; block all other physical egress. Because this
  is always on, a tunnel drop is cut **instantly** — traffic never silently
  falls back to your real interface. (By default the cut is followed by the
  bounded [automatic reconnect window](#automatic-reconnect-window) so the VPN
  can redial; disable it for a strict zero leak window.)
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
still governs the fallback/legacy model below.) DNS on the physical link stays open by default
(`vpn.allowPhysicalDNS`, on since the 2026-07 defaults review) so a client can
re-resolve its server hostname while the tunnel is down.

## Switching between VPNs

The guard passes egress to the **union** of every configured profile's server
endpoints, so disconnecting one known VPN and connecting another just works —
each profile's handshake stays reachable on the physical link. Add profiles with
`dezhban vpn add` / `dezhban vpn import` (see [config.md](config.md)).

### Switch window — connecting a brand-new VPN

A VPN whose server dezhban has never seen is a chicken-and-egg: the guard blocks
everything except known endpoints, so the new client's handshake to its unknown
server is dropped, and dezhban can't learn a server it never sees connect. The
**switch window** breaks this. `dezhban switch` opens a bounded, explicitly
root-triggered window during which egress is allowed so the handshake can
complete; dezhban watches for the new tunnel and server, pins the server as a
*learned* endpoint, and snaps back to GUARD — early, the moment a non-blocked
exit is confirmed, or at the deadline.

```
# SWITCH WINDOW (default) — pass quick on lo0 all no state
#                           pass out quick all no state          # bounded by the daemon timer
#                           block drop out all
```

**Why all outbound, honestly.** During the window your real IP is exposed to
whatever you talk to. A port filter would *not* fix this: the self-hosted VPNs
this project targets deliberately run on 443 (to blend with HTTPS), and app
phone-home is overwhelmingly 443/QUIC too — any filter that admits the VPN admits
the leak. So the safety comes from the window being (a) explicitly root-triggered,
(b) short (default 15s, capped 5m; `--for` extends a one-off), (c) closed early the instant a good exit is
confirmed, and (d) auto-reverting to the prior fail-closed posture. For a
household where every VPN uses a fixed port (e.g. WireGuard on 51820) you can
restrict it with `vpn.advanced.windowProtocols`/`windowPorts`. The bounded
window is the only sanctioned relaxation of the guard, and it has exactly two
sanctioned triggers: an explicit operator command, and — unless you opt out —
the automatic reconnect window below. Everything else about the window (clamp,
cap, auto-revert, fail-closed expiry) is identical for both.

If a window expires before the VPN comes up, dezhban reverts to GUARD but keeps
any endpoint it learned mid-flight open — so a handshake still in progress can
finish under the guard.

### Automatic reconnect window — surviving a drop with zero interaction

Rotating-pool and anti-censorship VPNs pick a **fresh server on almost every
connect** (often Cloudflare-fronted on 443), so "keep the known endpoints open"
can never cover a reconnect — the redial target is an IP dezhban has never seen,
and every reconnect would need a manual `switch`. The **automatic reconnect
window** (`vpn.reconnectWindow`, default `30s`, `"0"` disables) fixes this: when
the tunnel drops while the guard is healthy, the daemon opens the same bounded
switch-window relaxation *by itself* so the client can redial anywhere — a new
server, or a different VPN app entirely. The moment a tunnel is back up with a
confirmed non-blocked exit, the window snaps shut early and the new server is
learned; if nothing reconnects, the window expires and the guard **fail-closes
and stays closed** until a tunnel returns or an operator acts.

This is a deliberate UX trade: a drop is no longer cut with a zero leak window —
the real IP may be exposed for up to `reconnectWindow` while apps retry. For the
threat model this project targets (never hold a *standing* direct connection
that exposes your real country to foreign services), a bounded burst of seconds
is acceptable; if it is not acceptable to you, set `vpn.reconnectWindow: "0"`
and the guard behaves exactly as before.

Safety rails, all non-negotiable:

- Opens **only from healthy GUARD** — never in standby, never from FULL BLOCK
  (the last confirmed exit was forbidden; relaxing from a known-bad state takes
  an explicit operator command), never while another window is already open.
- Only on an **observed** tunnel drop: a tunnel that was never actually seen up
  gets no window.
- **Anti-flap gate** (`vpn.advanced.reconnectMinUptime`, default `15s`): a
  tunnel that keeps bouncing with no confirmed exit stops earning windows, so a
  broken VPN cannot turn the guard into a sieve.
- One window per drop: expiry does not re-open; the next window needs the
  tunnel to come back up first.
- The `advanced.switchWindowMax` hard cap and the
  `windowProtocols`/`windowPorts` restriction apply exactly as they do to a
  manual window.

`status` shows an open auto window as `reconnect state: OPEN until …`
(`status --json`: `switch.trigger: "auto"`), and the menubar app announces
"VPN dropped — reconnect window open".

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

- **Yes** → **VPN guard**. GUARD is always on, so a drop is cut in **0 seconds**
  (followed, by default, by the bounded reconnect window so the VPN can redial).
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
