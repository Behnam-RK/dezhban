# How dezhban works

A narrative walkthrough of what actually happens from launch to teardown — for
operators who want to understand the machine, not just drive it. The developer
view (packages, interfaces, invariants) is in
[architecture.md](../contribute/architecture.md); the ruleset reference is in
[modes.md](modes.md).

## The one idea everything hangs off

Firewalls are good at *standing rules* and bad at *reacting quickly*. dezhban
therefore inverts the usual kill-switch design: instead of noticing a problem
and then blocking (reactive — always leaks for a detection interval), it keeps a
**default-deny rule installed the whole time** and punches the narrowest holes
that normal life needs. When the VPN drops, there is nothing to react to — the
traffic that would have leaked was already blocked. Everything else in this
document is bookkeeping around that one idea.

## Startup

`dezhban run` (or the boot service — same code path):

1. **Load + validate config** (`/etc/dezhban/dezhban.json` on unix). Durations
   are parsed, defaults filled, and unsafe combinations refused — e.g. a VPN
   guard with a tunnel up but no known server address is a guaranteed blackout,
   so the daemon refuses to start and `doctor` explains why.
2. **Elevate.** Enforcement needs root; a non-root invocation re-execs itself
   under `sudo` (with Touch ID, if you've enabled `pam_tid`).
3. **Open the observability surfaces**: the state file
   (`/var/db/dezhban/state.json`, republished on every change — this is what
   `status` and the menubar app read), the persistent log
   (`/var/db/dezhban/logs/dezhban.log`, size-rotated, captured on every run),
   the root-only command file, and the admin-group control socket.
4. **Apply the resting posture before the first poll.** In VPN guard mode
   that's the GUARD ruleset (below) — applied *immediately*, so there is no
   startup gap. With `vpn.autoArm` and no tunnel present, the daemon instead
   parks in `standby` (nothing enforced) and arms itself the moment a VPN
   connects.

Everything after startup happens in **one loop, one goroutine**. Timers, tunnel
watcher events, geo-poll ticks, control-socket requests — they are all select
cases feeding the same loop, and only that loop ever touches the firewall. That
is why dezhban's postures can't race each other.

## The guard

The firewall can't usefully filter by destination under a full tunnel (it only
sees encrypted packets to one address), so the guard filters by **interface**:

```
pass on loopback
pass out on the tunnel interface(s)          # your traffic, inside the VPN
pass out to the known VPN server addresses   # the tunnel's own handshake
block everything else
```

Which holes does that leave to maintain?

- **The tunnel set.** Tunnel interfaces (`utunN`) renumber across reconnects,
  so a watcher samples the system every second (`vpn.tunnelWatch`) and the loop
  grows/prunes the guarded set live. Explicitly configured interfaces are
  pinned and never pruned.
- **The endpoint set.** The union of: endpoints you configured (IPs or
  hostnames, re-resolved every `vpn.endpointRefresh`), every profile's
  endpoints (`dezhban vpn add`/`import`), endpoints **learned** from past
  connections (stored in daemon-owned `learned.json`, never written into your
  config), and — on macOS — endpoints **auto-discovered** from the live VPN
  socket. Recently-seen discovered endpoints linger for `vpn.endpointGrace`
  after the socket dies, so a dropped VPN can redial the *same* server.

### Before there is anything to guard

A guard needs a tunnel to pass traffic through; without one it would block
everything, which is not security but a host with no connectivity. So until a
tunnel is both configured **and** observed up, the daemon rests in **STANDBY**:
no rules installed, network fully open, and the UI saying plainly that it is not
protecting. It arms itself the moment a VPN connects.

dezhban used to ship a second mode for hosts without a tunnel — a
country-blocklist that polled your public IP and cut egress by destination. It is
gone ([ADR-0001](../adr/0001-single-guard-mode.md)): it applied no rules at rest, so
it could only block *after* a poll noticed, and it was only meaningful when the
country you blocked was your real physical location. The guard already contains
the country check.

### What the country check does here

The daemon polls geo-IP providers every `pollInterval` and asks what country the
VPN's **exit** is in, with hysteresis so one bad reading can't flap the network.
The three possible outcomes — allowed, blocked, undeterminable — are covered
below in [Exit-country policing](#exit-country-policing).

## Life of a VPN drop

1. **t = 0 ms** — the tunnel interface disappears. Nothing needs to react: the
   standing rule already blocks every non-tunnel path. Established flows die;
   nothing falls back to your real interface.
2. **t ≤ ~1 s** — the watcher observes the drop. The GUI icon flips to the red
   blocked state, a notification fires, and — if the drop came from a healthy
   GUARD — the **automatic reconnect window** opens
   ([modes.md § Automatic reconnect window](modes.md#automatic-reconnect-window)):
   egress is relaxed for `vpn.reconnectWindow` (default 30s) so your VPN client
   can redial *any* server, including one dezhban has never seen. Rotating-pool
   and anti-censorship VPNs pick fresh servers constantly; without the window,
   every reconnect would need a manual `switch`.
3. **You hit reconnect** (or the client auto-reconnects). The tunnel comes up,
   the daemon discovers the new server socket, runs a geo lookup through the
   tunnel, and on a confirmed non-blocked exit **snaps the window shut early**,
   learns the endpoint, and restores GUARD. Total interaction required: zero.
4. **Or nothing reconnects.** The window expires and the guard **fail-closes
   and stays closed** — no second window until a tunnel actually comes back.
   A flapping tunnel doesn't get windows at all
   (`vpn.advanced.reconnectMinUptime`).

Prefer the original strict behavior — a drop is cut and *stays* cut with zero
relaxation? `vpn.reconnectWindow: "0"`.

## Exit-country policing

While the tunnel is healthy, the daemon periodically checks *where the tunnel
exits*: a geo lookup runs through the tunnel and the result feeds a small state
machine (hysteresis again). Three outcomes:

- **Allowed country** → stay in GUARD.
- **Undeterminable** (lookup failed) → **hold the current posture.** Escalating
  on an unknown would cut the tunnel's own egress and livelock the reconnect;
  the standing guard already covers the leak the failure might hide.
- **Confirmed blocked country** → **FULL BLOCK**: the tunnel-egress pass is
  dropped so none of your traffic reaches the forbidden exit, but the endpoint
  handshake stays open so the encrypted transport survives. Recovery needs no
  rule change at all: FULL BLOCK carries a pass scoped to the tunnel interface
  *and* the geo providers' addresses, so each lookup completes with everything
  else still cut. Once the exit reads allowed again, GUARD is restored
  automatically. Only if no provider address can be resolved does it fall back to
  the older lift-and-probe — briefly lifting the guard for one bounded lookup and
  re-cutting. See [ADR-0006](../adr/0006-geo-providers-tunnel-scoped.md).

## The switch window — the only sanctioned relaxation

Everything above never *widens* access on its own authority except through one
mechanism: the bounded switch window, opened by exactly two triggers — an
operator command (`dezhban switch`, for arming a brand-new VPN) or the
automatic reconnect flow above. Full mechanics, the independent per-trigger
caps, and the safety rails: [modes.md § Switching between
VPNs](modes.md#switching-between-vpns).

## Recovery and escape hatches

- **`dezhban unblock`** — releases a manual block; with `autoArm`, releasing
  with the tunnel down returns to standby ("my VPN is off on purpose").
- **`sudo dezhban panic`** — the lockout escape hatch. It removes every
  dezhban-tagged rule *directly*, requires no running daemon, and is
  deliberately not a control-socket op. Every rule dezhban installs carries the
  `dezhban` tag/anchor, so teardown is surgical — your own firewall rules are
  never touched.
- **Crash safety** — `Cleanup()` runs on shutdown signals, and the boot service
  restarts on crash *re-arming the guard* (a kill switch that dies open is not
  a kill switch).

## What watches the watcher

- `state.json` — the live posture snapshot; `dezhban status [--json]` and the
  menubar app are pure readers of it.
- `logs/dezhban.log` — persistent history of every decision above.
- `dezhban doctor` — pre-flight: config sanity, tunnel/endpoint routing checks,
  lockout-risk detection, Touch ID setup hint.
- `dezhban print-rules --mode guard|fullblock|switch` — shows the exact ruleset
  any posture would install, without touching the firewall. No root needed.
