# Postures

> This is the reference page: every posture and the exact rules it installs. If
> you are setting dezhban up for the first time, start with the
> [quick start](quick-start.md) instead.

dezhban has **one** enforcement model: an always-on **interface guard**. Under a
full tunnel the firewall on the physical interface sees only encrypted outer
packets to one address — the VPN endpoint — so a destination-IP allowlist is
meaningless. The guard instead controls the **interface**: which one may carry
traffic off this machine.

What varies is not the *mode* but the **posture** — the state the machine is in
right now:

| Posture | Rules installed | Means |
|---|---|---|
| [**STANDBY**](#standby) | none — network fully open | No tunnel has been observed yet. dezhban is **not protecting.** |
| [**GUARD**](#guard) | tunnel + endpoint pass, everything else blocked | The healthy resting state. A drop is cut instantly. |
| [**FULL BLOCK**](#full-block) | endpoint pass only | The VPN's exit landed in a blocked country. All user traffic is cut. |
| [**SWITCH WINDOW**](#switch-window--connecting-a-brand-new-vpn) | all outbound, bounded by a timer | The sanctioned relaxation, from one of exactly three triggers. |

> **There used to be a second mode.** A `vpn.enabled: false` country-blocklist
> fallback watched your public IP and cut egress by destination. It is gone —
> see [ADR-0001](adr/0001-single-guard-mode.md). It was never a peer of the
> guard: it applied no rules at rest, so it was "best-effort, not a zero-leak
> guarantee" by its own documentation, and it was only meaningful when the
> country you blocked was your *real physical location*. The guard already
> contains the country check — that is what FULL BLOCK is.

## STANDBY

The resting posture before any tunnel has been seen: **no rules at all, the
network is fully open, dezhban is not protecting.** A fresh install sits here, as
does a host whose VPN was uninstalled.

This is deliberate, and it is the job `vpn.enabled: false` used to do. An
always-on guard with no tunnel to pass traffic through is not "secure", it is a
host with no connectivity — so dezhban waits until it has something to guard.
It arms the moment a tunnel is both **configured and observed up**. See
[ADR-0002](adr/0002-standby-no-tunnel-posture.md).

Standby is the one posture where the UI must be loud: nothing is blocked, so the
menubar icon is **grey**, never red, and the Overview says so in words. A kill
switch that looks armed while installing no rules is worse than one that is
honestly off.

### Arming at boot

dezhban is a launchd/systemd daemon; the VPN client typically starts later, as a
user-session service. On an ordinary boot the tunnel does not exist yet when the
daemon starts, so a live presence check alone would land in STANDBY on *every*
reboot — opening the network for however long the VPN takes to connect, even on
a host that has run this guard successfully for months.

`vpn.armAtBoot` (**on by default**) closes that gap: dezhban persists the fact
that a configured tunnel has been observed up at least once on this host
(`internal/armed`), and at startup, if that fact is true **and** an endpoint is
already known, it arms directly instead of waiting for the live probe — no new
rule shape, since the guard with no tunnel present already renders as the FULL
BLOCK shape (endpoint + DNS + local-network passes, tunnel-egress pass absent).
The tunnel then simply dials in under an already-armed guard.

Both conditions are required, and neither is optional: a fresh install, or any
host whose VPN has never come up, still starts in STANDBY — arming a guard that
has never proven it can pass traffic would turn a misconfiguration into a
permanent lockout. See [ADR-0008](adr/0008-arm-at-boot.md).

`vpn.armAtBoot: false` restores the live-probe-only behavior.

## GUARD

The healthy resting state — pass loopback + tunnel egress + the endpoint
handshake; block all other physical egress. Because this is always on, a tunnel
drop is cut **instantly**: traffic never silently falls back to your real
interface. By default the cut is followed by the bounded
[automatic reconnect window](#automatic-reconnect-window) so the VPN can redial;
set `vpn.reconnectWindow: "0"` for a strict zero-leak cut.

## FULL BLOCK

The VPN's exit landed in a blocked country. Drop the tunnel-egress pass so no
user traffic can reach a forbidden exit, but **keep the endpoint handshake open**
so the encrypted transport survives and the tunnel can reconnect — a cut endpoint
would livelock the reconnect. It is GUARD minus the tunnel-egress pass.

Recovery observes the exit through a **tunnel-scoped geo-provider pass**: FULL
BLOCK keeps a rule matching the tunnel interface *and* the provider addresses, so
the exit-country lookup completes while every other byte stays cut. No rules
change to make a reading, so there is no leak.

The double scoping is the point. With the tunnel down the lookup simply fails and
the posture holds — correct, because there is no VPN exit to measure. A pass on
the *physical* link would instead succeed and report your ISP's country (a normal,
allowed one), so FULL BLOCK would never fire. See
[ADR-0006](adr/0006-geo-providers-tunnel-scoped.md).

The pass carries **no DNS rule**. A tunnel-scoped but destination-unscoped port-53
rule would send *every* application's DNS through the tunnel to the forbidden
exit's resolver, handing the exit whose country you are refusing a running log of
every hostname you look up. Provider addresses are refreshed while the guard is
healthy instead, and a mid-block rotation falls back to lift-and-probe below.

If no provider address can be resolved, recovery falls back to briefly lifting the
guard for one bounded lookup and re-cutting — a small leak, but far better than a
block that can never observe its way out. The tunnel transport survives either
path, so a genuinely-down tunnel can come back and a later probe see an allowed
exit.

```
# GUARD  — pass quick on lo0 all no state
#          pass out quick on { utun4 } all no state
#          pass out quick to { <vpn endpoint> } no state
#          block drop out all
#
# FULL BLOCK — pass quick on lo0 all no state
#              pass out quick to { <vpn endpoint> } no state       # reconnect path
#              pass out quick on { utun4 } to { <providers> } no state   # exit lookup
#              block drop out all
```

> Shown without the default-on passes (`allowLocalNetwork`, `allowPhysicalDNS`)
> so the guard itself is legible. `dezhban print-rules --mode guard` prints the
> real thing for *your* config, and applies nothing.

Configure the tunnel interface(s) and VPN endpoint IP(s) — see the `vpn` block in
[config.md](config.md). Find your tunnel interface with:

```sh
dezhban detect-vpn          # detected tunnel iface(s) + a paste-ready vpn block
```

`detect-vpn` deliberately does **not** autodetect the endpoint — a wrong endpoint
would leak physical egress — so set `vpn.endpoints` from your VPN client's own
config (or use `autoDiscoverEndpoints` on macOS). A wrong or tunnel-internal
endpoint is the #1 lockout cause; [troubleshooting.md](troubleshooting.md) has the
runbook. `panic` tears down every posture's rules.

### An unknown country holds; it never escalates

The standing GUARD rule is itself the fail-closed block for physical leaks, so an
**undeterminable** country *holds* the current posture. Only a *successful*
reading of a blocked country produces FULL BLOCK, and only a successful reading
of an allowed one restores GUARD. Escalating on an unknown would cut the tunnel's
own egress and livelock the very reconnect that could fix the lookup.

A failed lookup is fully neutral: it does not commit a pending flip, and it does
not cancel one either. A blip during a 2-of-3 hysteresis streak must not hand a
blocked exit a free reprieve.

**Not every failed lookup is a problem.** Three causes used to collapse into one
alarming message, and the most common was not a fault at all:

| Cause | How it is reported |
|---|---|
| No tunnel up — a switch/reconnect window, standby, or a drop | **Not an error.** "Exit country unknown — no tunnel is up, so there is no VPN exit to check." |
| Tunnel up, providers unreachable | **A real warning.** The exit may be censoring the providers — an Iranian exit blocking them looks exactly like this. The posture holds. |
| Tunnel up, response malformed | **A real error**, worth showing. |

The first case is why the geo providers used to look broken during every switch
window: the tunnel is *supposed* to be down then, so the lookup failing is
correct behaviour. `status --json` splits these into `exitUnknown` (expected) and
`lookupErr` (genuine).

There is **no `failClosed` setting.** It belonged to the retired fallback model,
where the firewall was open at rest and an unknown country was the only reason to
cut anything. Under the guard it had no meaning — the rules are already the
fail-closed block. DNS on the physical link stays open by default
(`vpn.allowPhysicalDNS`) so a client can re-resolve its server hostname while the
tunnel is down.

## Local network access

The guard blocks everything on the physical interface except the tunnel and the
VPN server. Taken literally that also cuts your **printer, NAS, router admin
page, AirPlay/Chromecast targets, local dev servers, and SSH to the machine next
to you** — none of which have anything to do with the threat the guard exists to
stop.

So `vpn.allowLocalNetwork` (**on by default**) passes these destinations in every
enforcing posture:

| Range | What it covers |
|---|---|
| `10/8`, `172.16/12`, `192.168/16` | ordinary private LANs |
| `100.64/10` | CGNAT (RFC6598) — Tailscale, many ISP routers |
| `169.254/16`, `fe80::/10` | link-local, incl. self-assigned addressing |
| `fc00::/7` | IPv6 unique-local — the ULA equivalent of RFC1918 |
| `224.0.0.0/24`, `239/8` | v4 multicast — mDNS/Bonjour and SSDP (local + admin scope only) |
| `ff02::/16`, `ff05::/16` | v6 multicast — link-local and site-local scope only |

Multicast is what actually makes discovery work: `224.0.0.251` / `ff02::fb`
(mDNS) and `239.255.255.250` (SSDP) are how a Mac finds printers and AirPlay
targets. Opening only the unicast ranges would leave devices *visible but
undiscoverable*, which reads as broken rather than restricted.

Only the **locally-scoped** multicast ranges are passed, not all of `224/4` and
`ff00::/8`. Multicast has globally-routable scopes — `232/8` (SSM), `233/8`
(GLOP), `ff0e::/16` (global) are designed to cross the internet — and a range
that can leave the building has no place in a pass justified by "this traffic
never leaves the building".

**Why this is safe.** The pass is scoped by **destination**, never by interface.
A packet to a public address does not match these prefixes whatever interface
carries it, so this cannot become an internet path — it is not a hole in the kill
switch. And it costs nothing against the threat model: dezhban exists to stop a
standing direct connection exposing a sanctioned-country IP to a *foreign*
service, and RFC1918 traffic never leaves the building.

**The one real cost**, stated plainly because a security setting should not hide
its downside: on an untrusted network — a café, a hotel — this lets you reach,
and be reached by, the other devices on that network. Set
`vpn.allowLocalNetwork: false` if that matters more to you than the printer.

`dezhban status` prints an `also reachable:` line naming exactly what is open on
the physical link, so you never have to infer it from the config.

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
the leak. So the safety comes from the window being (a) explicitly triggered,
(b) short (default 5s, capped 3m; `--for` extends a one-off), (c) closed early
the instant a good exit is confirmed, and (d) auto-reverting to the prior
fail-closed posture. For a household where every VPN uses a fixed port (e.g.
WireGuard on 51820) you can restrict it with
`vpn.advanced.windowProtocols`/`windowPorts`. The bounded window is the
sanctioned relaxation of the guard, and it has exactly three sanctioned
triggers: an explicit operator command, the automatic reconnect window below
(unless you opt out), and an explicit operator [pause](#pause--deliberately-using-your-real-ip).
Everything about the window (auto-revert, fail-closed expiry) is identical
across triggers — except the hard cap, which is deliberately **not** shared
between any of them: the manual trigger is capped by `advanced.switchWindowMax`
(default 3m), the automatic one by `advanced.reconnectWindowMax` (default 10m),
and pause by its own `vpn.pauseMax` (default 30m) — so a longer budget on one
trigger can never silently truncate another's. See
[ADR-0008](adr/0008-arm-at-boot.md) for why pause is a third trigger rather
than a parallel mechanism.

If a window expires before the VPN comes up, dezhban reverts to GUARD but keeps
any endpoint it learned mid-flight open — so a handshake still in progress can
finish under the guard.

**Disabling it.** `vpn.switchWindow: "0"` removes the manual trigger entirely.
This is a *tightening*: with it off, and `reconnectWindow` off too, nothing can
relax the guard. The cost is that a brand-new VPN's server must be added to
`vpn.endpoints` by hand, since there is no longer a window in which its handshake
could be observed. `dezhban switch` then refuses by name rather than failing
obscurely.

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
and a drop is cut with zero leak.

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
- Capped by its own `advanced.reconnectWindowMax` (default 10m) — not
  `switchWindowMax`, so a longer automatic budget is never truncated to the
  manual trigger's cap. The `windowProtocols`/`windowPorts` restriction applies
  exactly as it does to a manual window.

`status` shows an open auto window as `reconnect state: OPEN until …`
(`status --json`: `switch.trigger: "auto"`), and the menubar app announces
"VPN dropped — reconnect window open".

### Pause — deliberately using your real IP

Sometimes the correct traffic is the one the guard blocks: a domestic-only
service (a local bank, a government site) that refuses connections from a
foreign VPN exit. `dezhban pause [duration]` opens the same bounded window as
`switch`, but for a different purpose and a different cap — it does not wait
for a VPN, it simply gives you the real ISP-assigned IP for a while, then
re-arms itself with no further action:

```sh
dezhban pause 15m     # real IP for 15 minutes, capped by vpn.pauseMax
dezhban resume        # end it early
```

Capped by `vpn.pauseMax` (default **30m**; `"0"` disables pausing entirely),
never shared with `switchWindowMax` or `reconnectWindowMax`. Gated over the
control socket by `control.allowPauseOps` (default true), independent of
`control.allowSwitchOps` — you can turn off passwordless switching without
losing passwordless pausing, or vice versa. Refused in STANDBY (nothing is
blocked to pause) and while a switch window is already open (cancel it first).

Because pause shares the switch window's rule shape, it also shares its
early-close behavior: if a VPN happens to reconnect with a confirmed good exit
while a pause is open, the guard may re-arm before your requested duration
elapses. That is a tightening (protection resuming early), never a leak, so it
is accepted rather than engineered around — see
[ADR-0008](adr/0008-arm-at-boot.md).

### The three windows are independent

Each disables on its own, and they answer different questions:

| Setting | Off means |
|---|---|
| `vpn.switchWindow: "0"` | No manual `dezhban switch`. A brand-new VPN's server must be configured by hand. |
| `vpn.reconnectWindow: "0"` | A drop is cut with zero leak; the VPN cannot redial to an unknown server. |
| `vpn.pauseMax: "0"` | No `dezhban pause`. The real IP is never deliberately exposed. |

All three `"0"` is the strict zero-leak posture: nothing can relax the guard.

## Preview any ruleset without applying it

No root, no firewall changes:

```sh
dezhban print-rules --mode guard     --config <config>
dezhban print-rules --mode fullblock --config <config>
dezhban print-rules --mode switch    --config <config>
```

> `guard` / `fullblock` / `switch` are the stable identifiers used by
> `print-rules` and the state file. `--mode legacy` was removed with the fallback
> model and now errors by name rather than silently rendering something else.
>
> Note these previews are static config, not the runtime posture: a config with
> no tunnel previews as a full block here, while the running daemon idles
> rule-free in STANDBY until a tunnel is actually observed up.
