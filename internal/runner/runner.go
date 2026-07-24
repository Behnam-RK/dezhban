// Package runner ties the three layers into the live daemon: it polls the
// Monitor, asks the Decider for a verdict, and drives the firewall Backend —
// always cleaning up on exit. It is platform-independent; the only OS-specific
// part is the Backend it is handed.
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/behnam-rk/dezhban/internal/command"
	"github.com/behnam-rk/dezhban/internal/control"
	"github.com/behnam-rk/dezhban/internal/decision"
	"github.com/behnam-rk/dezhban/internal/firewall"
	"github.com/behnam-rk/dezhban/internal/monitor"
	"github.com/behnam-rk/dezhban/internal/netdetect"
	"github.com/behnam-rk/dezhban/internal/state"
)

// probeEgressBudget caps how long the VPN recovery probe may hold the guard
// lifted for one observation. It is slightly above a single provider's lookup
// timeout so a normal lookup completes, while bounding the leak window if the
// tunnel hangs or quorum fans out across several providers.
const probeEgressBudget = 8 * time.Second

// windowRevertRetry re-arms the window timer after a FAILED posture revert, so
// the daemon keeps trying to close a window whose Backend.Apply is failing
// rather than leaving egress relaxed while falsely reporting the window closed.
const windowRevertRetry = 2 * time.Second

// publishBuffer sizes the background state-writer queue. It comfortably absorbs
// a transient slow write at the daemon's event rate (a poll every few tens of
// seconds); only a sustained write stall fills it and reintroduces back-pressure.
const publishBuffer = 64

// Monitor is the monitor surface the runner consumes. *monitor.Monitor
// satisfies it; an interface keeps the loop testable without real HTTP. Poll
// drives the legacy loop; Once is used by the VPN recovery probe, which has to
// interleave a single lookup between firewall transitions.
type Monitor interface {
	Poll(ctx context.Context) <-chan monitor.Result
	Once(ctx context.Context) (monitor.Reading, error)
}

// Backend is the subset of firewall.FirewallBackend the runner drives.
// *firewall backends satisfy it directly.
// Every posture the guard installs is expressed as a Policy through Apply. The
// dst-IP Block verb belonged to the retired country-blocklist model and is gone
// from this seam; `dezhban block --force` still calls it on the concrete backend.
type Backend interface {
	Apply(p firewall.Policy) error
	Unblock() error
	Cleanup() error
}

// Options bundles everything the run loop needs. main assembles it from config
// so the runner itself stays free of config and HTTP dependencies.
type Options struct {
	Monitor  Monitor
	Decider  *decision.Decider
	Backend  Backend
	Log      *slog.Logger
	Interval time.Duration // poll period; drives the VPN ticker

	// Tunnels and Endpoints describe the VPN guard. Endpoints is
	// the static fallback used only when ResolveEndpoints is nil (tests / legacy
	// callers); normal runs supply ResolveEndpoints.
	Tunnels   []string
	Endpoints []netip.Addr
	// AllowPhysicalDNS mirrors config vpn.allowPhysicalDNS onto the guard and
	// VPN full-block policies (opt-in plain-DNS pass for hostname re-resolution
	// while the tunnel is down).
	AllowPhysicalDNS bool
	// AllowLocalNetwork keeps LAN destinations reachable while the guard is armed
	// (vpn.allowLocalNetwork). Destination-scoped, so it can never become an
	// internet path.
	AllowLocalNetwork bool
	// TunnelGroups are tunnel-interface class names (e.g. "utun") passed as an
	// interface group / wildcard so a newly-appeared tunnel is covered with no
	// rule reload (pf/nft only). Optional.
	TunnelGroups []string
	// Autodetect enables runtime tunnel re-detection: the watcher samples all
	// tunnel-like interfaces and the runner grows/prunes its guard set as VPNs
	// come and go. Explicit Tunnels are pinned (never pruned). Also relaxes the
	// startup gates (an empty tunnel/endpoint set is allowed — the standing
	// posture is a total cut until the first VPN/switch window).
	Autodetect bool
	// SwitchWindow is the default duration of a MANUAL switch window;
	// SwitchWindowMax caps it. WindowProtos/WindowPorts optionally restrict the
	// window. A switch window is only available when SwitchWindow > 0 AND
	// PollCommand != nil.
	SwitchWindow    time.Duration
	SwitchWindowMax time.Duration
	WindowProtos    []string
	WindowPorts     []int
	// ReconnectWindowMax caps the AUTOMATIC reconnect window (see
	// ReconnectWindow below) and is deliberately independent of
	// SwitchWindowMax — sharing one cap between the two triggers would
	// silently truncate whichever trigger has the larger intended budget.
	// <=0 → 10m.
	ReconnectWindowMax time.Duration
	// WindowDiscoveryInterval is how often endpoints are re-resolved while a
	// switch window is open (fast, to learn the new server quickly). <=0 → 2s.
	WindowDiscoveryInterval time.Duration
	// PollCommand, when non-nil, is polled on CommandPoll for a control command
	// (open/cancel switch window). Returns (cmd, true) when one was consumed.
	PollCommand func() (command.Command, bool)
	// CommandPoll is the PollCommand cadence. <=0 → 1s.
	CommandPoll time.Duration
	// Learn, when non-nil, records discovered endpoints for a profile after a
	// switch window closes on a successful (geo-allow) verdict, so the VPN stays
	// reachable across restarts. Called from the run-loop goroutine only.
	Learn func(profile, iface string, addrs []netip.Addr)
	// Control, when non-nil, is the live control socket. Its accept goroutine only
	// FORWARDS requests to this loop over a channel — it never touches the Backend —
	// so the run-loop goroutine remains the sole caller of Backend.Apply. nil → the
	// passwordless control path is off (tests / legacy callers / Windows).
	Control *control.Server
	// AllowSwitchOps permits opening and cancelling a switch window over the control
	// socket (i.e. without root). It is the one control op that can RELAX the guard,
	// so it is a distinct switch from the socket itself: set it false to force switch
	// ops back to the root-owned command file. Mirrors config control.allowSwitchOps.
	AllowSwitchOps bool
	// PauseMax caps a bounded operator pause (`dezhban pause` / the GUI's
	// "Pause protection"): a deliberate, timed drop to the real ISP IP,
	// sharing the switch-window machinery as a third trigger
	// (state.TriggerPause) but with its own cap, never shared with
	// SwitchWindowMax or ReconnectWindowMax. <=0 → pausing is off entirely
	// (mirrors config.Disabled on vpn.pauseMax — there is no "fallback
	// default" here the way ReconnectWindowMax has one, because an absent
	// vpn.pauseMax is already filled to 30m by config.Normalize).
	PauseMax time.Duration
	// AllowPauseOps permits opening and ending a pause over the control socket
	// (i.e. without root), independently of AllowSwitchOps. Mirrors config
	// control.allowPauseOps.
	AllowPauseOps bool
	// ResolveEndpointsWith recomputes the endpoint set using an explicit live
	// tunnel set for the tunnel-internal drop filter. nil → ResolveEndpoints /
	// static fallback (the tunnel set is then ignored).
	ResolveEndpointsWith func(ctx context.Context, tunnels []string) netdetect.EndpointSet
	// ResolveProviders recomputes the geo-API provider IPs, passed tunnel-scoped
	// in FULL BLOCK so the exit-country lookup needs no guard lift. Called on the
	// same cadence as endpoints, because CDN-fronted providers rotate addresses.
	//
	// nil (or an empty result) falls back to the old lift-and-probe recovery.
	// That fallback is deliberate: losing the ability to observe the exit at all
	// would mean a FULL BLOCK that can never lift, which is worse than a bounded
	// leak.
	ResolveProviders func(ctx context.Context) []netip.Addr
	// ResolveEndpoints recomputes the VPN endpoint set (literals + resolved
	// hostnames + live discovery). Called once at startup and on each
	// EndpointRefresh tick. nil → fall back to the static Endpoints above.
	ResolveEndpoints func(ctx context.Context) netdetect.EndpointSet
	// EndpointRefresh is how often ResolveEndpoints is re-run (VPN mode). <=0 → 5m.
	EndpointRefresh time.Duration
	// EndpointGrace is how long an endpoint stays in the allowed set after its
	// last sighting once a refresh no longer reports it (VPN mode) — the window
	// in which a dropped VPN can redial the same server. <=0 → 15m.
	EndpointGrace time.Duration
	// AutoArm (vpn.autoArm): start PASSIVE (standby, no enforcement) when no
	// tunnel interface is present, and arm the guard automatically the moment
	// one appears. Arming is one-way on tunnel loss — a drop is
	// indistinguishable from the leak the kill switch exists for — so only an
	// explicit unblock with the tunnel down returns to standby.
	AutoArm bool
	// ArmAtBoot (vpn.armAtBoot): arm the guard directly at startup even when no
	// tunnel interface is present yet — provided TunnelEverUp is true and an
	// endpoint is known — instead of entering the AutoArm standby above. This
	// is what lets the network stay closed across a reboot rather than opening
	// while the VPN client (which typically starts later than this daemon)
	// has not yet brought its interface up. See docs/adr/0008-arm-at-boot.md.
	ArmAtBoot bool
	// TunnelEverUp is the persisted "a configured tunnel has been observed up
	// at least once on this host" fact (internal/armed), loaded once by the
	// caller before Run. It is the safety rail ArmAtBoot depends on: a fresh
	// install, or a host whose VPN has never worked, still starts in STANDBY
	// even with ArmAtBoot on — arming a guard that has never proven it can
	// pass traffic would turn a misconfiguration into a permanent lockout,
	// exactly the outcome ADR-0002 rejected as "Alternative 1".
	TunnelEverUp bool
	// MarkTunnelUp persists the TunnelEverUp fact (internal/armed) the first
	// time a tunnel is observed up this run. nil → the fact is never recorded
	// (tests / legacy callers); ArmAtBoot then has no effect on future boots.
	MarkTunnelUp func(time.Time)
	// ReconnectWindow (vpn.reconnectWindow): when >0, a tunnel-down edge from a
	// healthy GUARD posture (not standby, not FULL BLOCK, no window already
	// open) automatically opens a switch-window relaxation of this duration so
	// the VPN client can redial any server — including one never seen before.
	// The window closes early on a confirmed good exit (learning the new
	// endpoint) and reverts fail-closed on expiry. <=0 → no automatic window.
	ReconnectWindow time.Duration
	// ReconnectMinUptime is the anti-flap gate: the auto-window opens only if
	// the tunnel had been up at least this long, or a non-blocked exit was
	// confirmed during that uptime. <=0 → gate off.
	ReconnectMinUptime time.Duration
	// Watcher, when non-nil, emits tunnel up/down edges. In VPN mode a down edge
	// can open the automatic reconnect window (see ReconnectWindow); the standing
	// guard rule already cuts the drop itself with no leak. In legacy mode a down
	// edge triggers an immediate block — a kill switch needing only a tunnel
	// name, no endpoints.
	Watcher *netdetect.Watcher
	// Publish, when non-nil, receives a fresh snapshot of the daemon's posture
	// after each poll, verdict transition, tunnel edge, and endpoint refresh. It
	// is best-effort observability (the menubar app / `status --json` read it) and
	// must never affect enforcement — the wiring in main logs write failures at
	// debug. Run invokes it from a background writer goroutine (see publishBuffer),
	// so a slow write never stalls the enforcement loop; it may run concurrently
	// with the loop and need not be fast. nil → no-op (tests / legacy callers).
	// ReloadC delivers replacement settings to the running loop, so a config
	// edit takes effect without a restart. Nil (the default) means reloading is
	// not wired up and the loop simply never selects on it.
	//
	// Like every other case in the select, this is handled ON the run-loop
	// goroutine: the sender only hands over a value, and the loop alone touches
	// the Backend. See LiveSettings for what may travel this way and what
	// cannot.
	ReloadC <-chan LiveSettings

	// ReloadConfig re-reads the daemon's configuration from disk and derives both
	// the settings to adopt and the report of what changed. It is what the
	// control socket's reload op calls, on the run-loop goroutine, so the daemon
	// pulls the new config itself rather than trusting a caller to hand one over.
	// Nil means reloading is unavailable and the op is refused by name.
	ReloadConfig func() (LiveSettings, ReloadReport, error)

	Publish func(state.Snapshot)
	// BlockedCountries is copied verbatim into each published snapshot so an
	// observer can show what the daemon is configured to block. Informational only.
	BlockedCountries []string
}

// tunnelSnapshot maps a watcher edge to the published tunnel state. Name comes
// from the interface the watcher identified; on a down/unknown edge it carries
// no name, so fall back to the configured tunnel(s) the guard is watching.
func tunnelSnapshot(st netdetect.TunnelState, tunnels []string) []state.Tunnel {
	name := st.Name
	if name == "" {
		name = strings.Join(tunnels, ",")
	}
	return []state.Tunnel{{Name: name, Up: st.Up, Detail: st.Detail}}
}

// postureName maps (blocked, window, standby) to the snapshot's posture string.
// "standby" (no tunnel observed yet) outranks only the plain guard: a full block
// or an open window always names itself.
func postureName(blocked, window, standby bool) string {
	switch {
	case window:
		return "switch-window"
	case blocked:
		return "full-block"
	case standby:
		return "standby"
	default:
		return "guard"
	}
}

// shouldArmAtBoot decides whether vpn.armAtBoot should override an
// AutoArm-computed standby=true into an armed start. Both conditions must
// hold, or the ADR-0002 safety rail (a fresh install can never lock itself
// out) is gone — see the call site in runGuard for the full rationale.
func shouldArmAtBoot(armAtBoot, tunnelEverUp bool, endpointCount int) bool {
	return armAtBoot && tunnelEverUp && endpointCount > 0
}

// anyTunnelUp reports whether at least one tunnel is currently up, i.e. whether
// there is a VPN exit whose country could meaningfully be measured at all.
func anyTunnelUp(tunnels []state.Tunnel) bool {
	for _, t := range tunnels {
		if t.Up {
			return true
		}
	}
	return false
}

// publish builds a full snapshot from the current posture and last-known reading
// and hands it to o.Publish. It no-ops when Publish is nil, so the hot path pays
// only a nil check when observability is off. Each call emits a complete snapshot
// (the file is replaced atomically), so callers pass the last-known reading even
// on tunnel/endpoint events to avoid blanking IP/country between polls.
func (o Options) publish(blocked bool, standby bool, r monitor.Reading, lookupErr error, enfErr error, tunnels []state.Tunnel, endpoints []netip.Addr, win *state.SwitchState, profile string) {
	if o.Publish == nil {
		return
	}
	windowOpen := win != nil && win.Open
	snap := state.Snapshot{
		Time:                time.Now(),
		Posture:             postureName(blocked, windowOpen, standby),
		Blocked:             blocked,
		CountryCode:         r.CountryCode,
		Provider:            r.Provider,
		BlockedCountries:    o.BlockedCountries,
		Tunnels:             tunnels,
		PollIntervalSeconds: int(o.Interval.Seconds()),
		PID:                 os.Getpid(),
		ActiveProfile:       profile,
		Switch:              win,
	}
	if r.IP.IsValid() {
		snap.IP = r.IP.String()
	}
	// Classify a failed exit-country lookup instead of reporting every one as an
	// error. Three causes currently collapse into one alarming message, and the
	// most common of them is not a fault at all:
	//
	//   no tunnel up   → EXPECTED. There is no VPN exit to measure. This is the
	//                    normal state during a switch/reconnect window (the
	//                    tunnel is down — that is why the window exists), in
	//                    standby, and across any drop. Reporting it as an error
	//                    trains people to ignore the field.
	//   tunnel up      → REAL. The exit may be censoring the providers (an
	//                    Iranian exit blocking them looks exactly like this), or
	//                    the response was malformed. Worth surfacing.
	//
	// Either way the posture HOLDS — an unknown country never escalates. The
	// difference is only in what we tell the operator, which is precisely the
	// part that was wrong.
	if lookupErr != nil {
		if anyTunnelUp(tunnels) {
			snap.LookupErr = lookupErr.Error()
		} else {
			snap.ExitUnknown = "no tunnel is up, so there is no VPN exit to check"
		}
	}
	if enfErr != nil {
		snap.EnforcementErr = enfErr.Error()
	}
	for _, e := range endpoints {
		snap.Endpoints = append(snap.Endpoints, e.String())
	}
	o.Publish(snap)
}

// Run executes the monitor→decision→enforcement loop until ctx is cancelled,
// then tears down. Cleanup is deferred so a stale block-all rule never outlives
// the daemon — that is the invariant that keeps the operator from being locked
// out of their own network.
func Run(ctx context.Context, o Options) error {
	defer func() {
		if err := o.Backend.Cleanup(); err != nil {
			o.Log.Warn("cleanup failed; rules may persist (run `dezhban panic`)", "err", err)
		} else {
			o.Log.Info("cleanup done — firewall rules removed")
		}
	}()

	// Decouple best-effort publishing from the enforcement loop: hand snapshots to
	// a background writer over a buffered channel, so a stalled disk write
	// (full/hung/NFS-backed state dir) can't delay handling the next tunnel or geo
	// event. The producer NEVER blocks: on a full buffer it drops the OLDEST queued
	// snapshot and enqueues the newest (latest-wins). Coalescing is invisible to
	// observers — they only ever read the single, atomically-renamed state file, so a
	// snapshot a later one supersedes could never be observed anyway. Nil Publish
	// stays a plain no-op.
	if o.Publish != nil {
		sink := o.Publish
		ch := make(chan state.Snapshot, publishBuffer)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for snap := range ch {
				sink(snap)
			}
		}()
		o.Publish = func(s state.Snapshot) {
			for {
				select {
				case ch <- s:
					return
				default:
					// Full: drop the oldest so the newest always lands. The drain may
					// race the consumer (both receive on ch); harmless — only "latest
					// eventually lands" matters, never an exact drop count.
					select {
					case <-ch:
					default:
					}
				}
			}
		}
		defer func() {
			close(ch)
			// Bound the flush: a wedged writer must not block this defer, because it
			// runs BEFORE the deferred Cleanup() (LIFO) — an unbounded wait here would
			// prevent rule teardown, the one invariant that keeps the host from being
			// locked out. On timeout the stuck writer goroutine leaks, acceptable on a
			// process that is exiting.
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
		}()
	}

	// The control socket's accept goroutine starts here and stops with ctx. It only
	// ever hands requests to the loop below over a channel, so no goroutine other
	// than the run loop can reach the Backend.
	if o.Control != nil {
		o.Control.Start(ctx)
		defer o.Control.Stop()
	}

	runErr := o.runGuard(ctx)
	// Publish one final "stopped" snapshot before teardown so observers (the
	// menubar app, `status --json`) flip to stopped at once on a clean shutdown
	// instead of waiting out the staleness window. It's queued on the same channel
	// and flushed by the defer above; a hard crash still relies on staleness.
	o.publishStopped(runErr)
	return runErr
}

// publishStopped emits a terminal snapshot marking the daemon as no longer
// enforcing. Cleanup (deferred) removes the rules right after. A non-nil
// runErr — a startup refusal or a loop failure — rides along as
// enforcementErr: without it the only trace of WHY the daemon went down is a
// log line under a service manager, and the state file (the one surface the
// menubar app and `status --json` actually read) would show a bare "stopped"
// indistinguishable from a deliberate one.
func (o Options) publishStopped(runErr error) {
	if o.Publish == nil {
		return
	}
	snap := state.Snapshot{
		Time:                time.Now(),
		Posture:             "stopped",
		Blocked:             false,
		PollIntervalSeconds: int(o.Interval.Seconds()),
		PID:                 os.Getpid(),
	}
	if runErr != nil {
		snap.EnforcementErr = runErr.Error()
	}
	o.Publish(snap)
}

// runGuard installs the always-on guard immediately at startup (so a tunnel drop
// is cut even before the first poll), then toggles GUARD ↔ FULL BLOCK on each
// verdict. While in FULL BLOCK the tunnel is cut and the exit country cannot be
// observed, so recovery uses a time-windowed probe (see probe): each tick the
// guard is briefly lifted for a single lookup, then re-cut. Probe readings feed
// the same hysteresis streak in the Decider, so one allowed reading does not
// lift the block — it takes `Hysteresis` consecutive allowed probes.
func (o Options) runGuard(ctx context.Context) error {
	switchEnabled := o.SwitchWindow > 0 && o.PollCommand != nil
	// relaxed startup gates: with runtime autodetect or an available switch
	// window, an empty tunnel/endpoint set is legal — the standing posture is a
	// total cut (lo0 + block-all) until the first VPN connects or a switch window
	// learns one. A plain pinned config keeps the strict gates.
	relaxed := o.Autodetect || switchEnabled

	// Mutable tunnel set; explicit config names are pinned (never pruned).
	tunnels := append([]string(nil), o.Tunnels...)
	pinned := make(map[string]bool, len(o.Tunnels))
	for _, t := range o.Tunnels {
		pinned[t] = true
	}

	set := o.resolveEndpointsWith(ctx, tunnels)
	endpoints := set.Addrs
	lastSet := set
	// Sighting times for endpoint retention (see reconcileWithGrace): a VPN
	// client reconnecting after a drop dials a server whose live socket vanished
	// with the tunnel, so endpoints must outlive their sockets for a bounded
	// grace or the guard walls off the very reconnect it is holding the line for.
	epGrace := o.EndpointGrace
	if epGrace <= 0 {
		epGrace = 15 * time.Minute
	}
	epLastSeen := make(map[netip.Addr]time.Time, len(endpoints))
	for _, a := range endpoints {
		epLastSeen[a] = time.Now()
	}
	// vpn.autoArm: when no tunnel interface is actually present at startup, run
	// PASSIVE (standby: no rules, no enforcement) and arm the guard the moment
	// one appears — instead of arming the zero-tunnel standing posture, a total
	// cut that makes "VPN off on purpose" mean "no network at all". Presence is
	// probed directly (netdetect), not inferred from the configured tunnel set:
	// explicit vpn.tunnelInterfaces names are pinned whether or not the
	// interface exists. A failed probe arms immediately — fail-closed.
	standby := false
	if o.AutoArm {
		if present, derr := netdetect.TunnelInterfaces(); derr != nil {
			o.Log.Warn("auto-arm: tunnel detection failed — arming immediately (fail-closed)", "err", derr)
		} else {
			standby = len(present) == 0
		}
	}
	// vpn.armAtBoot: override the live-probe standby above and arm directly,
	// even with no tunnel interface present yet. This is the boot race
	// ArmAtBoot exists for — dezhban is a launchd/systemd daemon and the VPN
	// client typically starts later, so the live probe above finds nothing on
	// every normal boot and, left alone, would open the network via the
	// Backend.Unblock() below. Both conditions must hold or the ADR-0002
	// safety rail (a fresh install can never lock itself out) is gone:
	//   1. an endpoint is already known — arming with nothing to dial is a
	//      permanent lockout, not a kill switch;
	//   2. TunnelEverUp is true — a tunnel has proven, on this host, that it
	//      can actually pass traffic. Without this, ArmAtBoot degenerates to
	//      ADR-0002's rejected "Alternative 1" (fail closed until a VPN
	//      exists), which turns first-run into a lockout event by design.
	if standby && shouldArmAtBoot(o.ArmAtBoot, o.TunnelEverUp, len(endpoints)) {
		standby = false
		o.Log.Info("arming at boot (vpn.armAtBoot) — tunnel not observed yet, but this host has connected "+
			"before and an endpoint is known; blocking until it reconnects", "endpoints", len(endpoints))
	}
	if len(endpoints) == 0 && !relaxed && !standby {
		return errors.New("refusing to start: no usable vpn endpoints — set vpn.endpoints (IP or hostname), " +
			"vpn.profiles, or enable vpn.autoDiscoverEndpoints/vpn.autodetect; a guard with no way to reach the " +
			"server can never let the tunnel reconnect")
	}

	// A tunnel is UP and we do not know its server. `relaxed` does NOT cover this.
	//
	// The relaxed allowance exists for the zero-tunnel case: no VPN connected, so the
	// standing posture is a total cut, and that is both correct (nothing may leak) and
	// recoverable (a switch window opens egress so a VPN can connect and be learned).
	// With a tunnel already up it is neither. The guard's block-all covers the physical
	// interface, which carries the tunnel's OWN encrypted transport — so arming it cuts
	// every packet, including the VPN's handshake and keepalives. The tunnel dies, and
	// because its socket dies with it, endpoint discovery can never learn the server
	// either. That is not a kill switch; it is an unrecoverable blackout that we inflict
	// on a working VPN.
	//
	// Refuse, and say exactly how to fix it. Discovery reads CONNECTED sockets from
	// netstat, and WireGuard (like other NetworkExtension clients) sends from an
	// unconnected UDP socket — it never appears as a connected flow, so autodiscovery
	// cannot see it and never will. Naming the server is the fix.
	// In standby no tunnel is actually present (probed above) — the configured
	// names are just pinned — so this up-tunnel hazard cannot apply; arming out
	// of standby re-checks endpoints at arm time (tryAutoArm).
	if !standby && len(tunnels) > 0 && len(endpoints) == 0 {
		return fmt.Errorf("refusing to start: the VPN tunnel (%s) is up but dezhban does not know its server "+
			"address, and the guard blocks all egress on the physical link — including the tunnel's own encrypted "+
			"transport. Arming it would cut ALL traffic, and the tunnel could never re-handshake. "+
			"Auto-discovery reads connected sockets, and WireGuard/NetworkExtension clients use an unconnected UDP "+
			"socket, so there is nothing for it to find. Name the server instead:\n"+
			"    dezhban vpn import <wg0.conf|client.ovpn>   (reads the endpoint from your VPN's own config)\n"+
			"    dezhban vpn add <name> --endpoint <host-or-ip>\n"+
			"    dezhban config set vpn.endpoints=<server-ip>\n"+
			"Then run `dezhban doctor` to confirm.", strings.Join(tunnels, ", "))
	}

	// A VPN endpoint must be reachable on the PHYSICAL interface. A tunnel-internal
	// endpoint can't be, and blocking cuts the only path to it — a guaranteed
	// lockout. Refuse to start rather than discover it at the next FULL BLOCK.
	if len(endpoints) > 0 {
		if bad, err := netdetect.CheckEndpointRouting(endpoints, tunnels); err != nil {
			o.Log.Debug("could not check endpoint routing", "err", err)
		} else if len(bad) > 0 {
			for _, br := range bad {
				o.Log.Error("vpn endpoint is inside the tunnel's own subnet — guaranteed lockout if blocked; "+
					"set vpn.endpoints to the server IP reachable on the physical interface",
					"endpoint", br.Endpoint, "subnet", br.Subnet, "iface", br.Iface)
			}
			return fmt.Errorf("refusing to start: %d vpn endpoint(s) are tunnel-internal and would lock the host out on full block (run `dezhban doctor` to fix)", len(bad))
		}
	}

	// Geo-provider IPs, passed tunnel-scoped in FULL BLOCK so the exit-country
	// lookup can traverse the tunnel without lifting the guard. Resolved here at
	// startup and refreshed on the endpoint cadence, because CDN-fronted
	// providers rotate addresses. An empty set is not fatal: the recovery probe
	// falls back to the old lift-and-probe, which leaks briefly but still
	// recovers — strictly better than a FULL BLOCK that can never lift.
	var providers []netip.Addr
	if o.ResolveProviders != nil {
		providers = o.ResolveProviders(ctx)
		if len(providers) == 0 {
			o.Log.Warn("no geo-provider IPs resolved — recovery will briefly lift the guard to observe the exit")
		}
	}

	guard, fullBlock := o.vpnPolicies(tunnels, endpoints, providers)
	if standby {
		// Passive start: no rules. Clear any stale dezhban rules from a prior
		// run so "standby" is what it claims — best-effort, since there may be
		// nothing to remove.
		if err := o.Backend.Unblock(); err != nil {
			o.Log.Warn("standby: clearing prior rules failed", "err", err)
		}
		o.Log.Info("vpn standby (vpn.autoArm) — not enforcing; the guard arms when a VPN connects",
			"tunnels", tunnels, "switch", switchEnabled)
	} else {
		if err := o.Backend.Apply(guard); err != nil {
			return fmt.Errorf("install startup guard: %w", err)
		}
		// guard is the standing posture: usually ModeGuard, but the zero-tunnel
		// standing posture is a ModeFullBlock shape — log the actual applied mode
		// rather than claiming "guard" unconditionally.
		o.Log.Info("vpn posture active (startup)", "mode", guard.Mode, "tunnels", tunnels, "endpoints", len(endpoints), "switch", switchEnabled)
	}

	blocked := false // applied posture: false = GUARD, true = FULL BLOCK

	// manualBlock records that an operator asked for FULL BLOCK over the control
	// socket. It HOLDS the block: while set, the geo state machine is suspended, so
	// a subsequent allowed reading cannot silently lift a block the operator asked
	// for. Only an explicit unblock (or a daemon restart) clears it.
	manualBlock := false

	// Switch-window state. windowActive is the authoritative flag; the timer +
	// deadline enforce the bound (belt and braces).
	var (
		windowActive      bool
		windowStart       time.Time // when the current window first opened; anchors the hard cap
		windowDeadline    time.Time
		windowPrevBlocked bool
		windowProfile     string
		windowTrigger     string // state.TriggerManual or state.TriggerAuto
		activeProfile     string // last profile a switch window verified onto; sticky
		windowTimer       *time.Timer
		windowTimerC      <-chan time.Time
		winDiscTick       *time.Ticker
		winDiscC          <-chan time.Time
	)

	tunnelUp := !standby // armed start presumes up until the watcher says otherwise
	var lastRes monitor.Result
	var lastTun []state.Tunnel
	var enfErr error

	// Automatic reconnect-window tracking. sawTunnelUp distinguishes an OBSERVED
	// healthy tunnel (watcher up sample, or a confirmed exit reading) from the
	// armed start's presumption of up — an auto-window must never open for a
	// tunnel that was never actually there. tunnelUpSince/goodExitThisUp feed the
	// anti-flap gate; a zero tunnelUpSince with sawTunnelUp set means "up since
	// before we started watching", which counts as long uptime.
	var (
		sawTunnelUp    bool
		tunnelUpSince  time.Time
		goodExitThisUp bool
	)

	// everUpRecorded mirrors o.TunnelEverUp but tracks whether THIS run has
	// already written it, so a host that armed-at-boot from a prior
	// observation never re-writes armed.json every run. markTunnelEverUp is
	// called from every sawTunnelUp=true site below; only the first call per
	// process actually persists anything.
	everUpRecorded := o.TunnelEverUp
	markTunnelEverUp := func(now time.Time) {
		if everUpRecorded || o.MarkTunnelUp == nil {
			return
		}
		everUpRecorded = true
		o.MarkTunnelUp(now)
	}

	winInterval := o.WindowDiscoveryInterval
	if winInterval <= 0 {
		winInterval = 2 * time.Second
	}

	switchState := func() *state.SwitchState {
		if !windowActive {
			return nil
		}
		return &state.SwitchState{Open: true, Until: windowDeadline, Profile: windowProfile, Trigger: windowTrigger}
	}
	snapshot := func() {
		o.publish(blocked, standby, lastRes.Reading, lastRes.Err, enfErr, lastTun, endpoints, switchState(), activeProfile)
	}
	rebuild := func() { guard, fullBlock = o.vpnPolicies(tunnels, endpoints, providers) }

	// reapplyStanding re-applies the guard after a tunnel/endpoint change, unless a
	// window owns the rules, we are in FULL BLOCK (which renders no tunnel pass —
	// the new set lands on the next guard restore), or standby (nothing applied).
	reapplyStanding := func(reason string) {
		rebuild()
		if windowActive || blocked || standby {
			return
		}
		// The standing posture is usually ModeGuard but is ModeFullBlock in the
		// zero-tunnel standing case — log the actual mode so autodetect/zero-tunnel
		// runs aren't misreported as "guard".
		if err := o.Backend.Apply(guard); err != nil {
			enfErr = err
			o.Log.Error("re-apply standing posture failed", "reason", reason, "mode", guard.Mode, "err", err)
		} else {
			enfErr = nil
			o.Log.Info("vpn standing posture updated", "reason", reason, "mode", guard.Mode, "tunnels", tunnels, "endpoints", len(endpoints))
		}
	}

	// reapplyWindow re-applies the switch-window policy after a tunnel/endpoint
	// change while a window is open, but ONLY when the window is restricted: an
	// unrestricted window already passes all outbound, so a new tunnel/endpoint
	// needs no rule update. A restricted window filters by proto/port and must
	// learn the new tunnel/endpoint, or that traffic stays blocked and the
	// verified early-close can never succeed.
	reapplyWindow := func(reason string) {
		if !windowActive || !o.windowRestricted() {
			return
		}
		if err := o.Backend.Apply(o.windowPolicy(tunnels, endpoints)); err != nil {
			enfErr = err
			o.Log.Error("re-apply switch window failed", "reason", reason, "err", err)
		} else {
			enfErr = nil
			o.Log.Info("switch window updated", "reason", reason, "tunnels", tunnels, "endpoints", len(endpoints))
		}
	}

	stopWindowTimers := func() {
		if windowTimer != nil {
			windowTimer.Stop()
			windowTimer = nil
		}
		windowTimerC = nil
		if winDiscTick != nil {
			winDiscTick.Stop()
			winDiscTick = nil
		}
		winDiscC = nil
	}

	// manualWindowMax / autoWindowMax are the absolute caps on real-IP exposure
	// for a manual vs. an automatic-reconnect episode, kept separate so one
	// trigger's budget can never silently truncate the other's (see
	// Options.ReconnectWindowMax's doc comment). windowMax below is fixed to
	// whichever applies for the CURRENT episode the instant it first opens (in
	// openWindow's first-open branch) and anchors to that open (windowStart), so
	// repeated "open" commands can never extend a single window past it.
	manualWindowMax := o.SwitchWindowMax
	if manualWindowMax <= 0 {
		manualWindowMax = 3 * time.Minute
	}
	autoWindowMax := o.ReconnectWindowMax
	if autoWindowMax <= 0 {
		autoWindowMax = 10 * time.Minute
	}
	// pauseWindowMax caps a THIRD, independently-gated relaxation (see
	// Options.PauseMax's doc comment): a deliberate operator pause, sharing
	// this same bounded-timer machinery but never sharing a cap with the
	// other two triggers. pauseEnabled gates both control-socket ops and the
	// command-poll cases below — a Disabled (<=0) PauseMax means pausing is
	// off entirely, not "use some fallback duration".
	pauseWindowMax := o.PauseMax
	pauseEnabled := pauseWindowMax > 0 && o.PollCommand != nil
	var windowMax time.Duration // set at first open; see openWindow

	// windowNoun names the open episode in operator-facing logs by its trigger —
	// a pause expiring must not read as a "switch window" closing.
	windowNoun := func() string {
		if windowTrigger == state.TriggerPause {
			return "pause"
		}
		return "switch window"
	}

	openWindow := func(now time.Time, dur time.Duration, profile, trigger string) {
		if windowActive {
			// A manual command takes over an auto window's attribution (the
			// operator is now driving); an auto trigger never fires while a window
			// is open, so the reverse cannot happen, and a manual open is refused
			// at every call site while a pause is open (a pause is ended by
			// resume, never rebranded). Attribution changes, but the episode's
			// exposure cap (windowMax, fixed at first open) does not — taking
			// over does not grant the longer auto budget to a manual window or
			// vice versa.
			if trigger == state.TriggerManual {
				windowTrigger = trigger
			}
			// Extend the deadline, but never past windowStart+windowMax — the hard
			// cap on exposure holds across repeated opens.
			hardCap := windowStart.Add(windowMax)
			newDeadline := now.Add(dur)
			if newDeadline.After(hardCap) {
				newDeadline = hardCap
			}
			// Only ever push the deadline OUT — a repeated `switch` with a shorter
			// --for must not shorten an already-open window's remaining time.
			if newDeadline.After(windowDeadline) {
				windowDeadline = newDeadline
			}
			// Keep the prior attribution when the new command names no profile.
			if profile != "" {
				windowProfile = profile
			}
			remaining := windowDeadline.Sub(now)
			if remaining < 0 {
				remaining = 0
			}
			if windowTimer != nil {
				windowTimer.Stop()
			}
			windowTimer = time.NewTimer(remaining)
			windowTimerC = windowTimer.C
			o.Log.Info(windowNoun()+" extended", "until", windowDeadline, "profile", windowProfile)
			snapshot()
			return
		}
		if err := o.Backend.Apply(o.windowPolicy(tunnels, endpoints)); err != nil {
			enfErr = err
			o.Log.Error("open switch window failed — staying in prior posture", "err", err)
			snapshot()
			return
		}
		windowPrevBlocked = blocked
		windowActive = true
		windowStart = now
		windowProfile = profile
		windowTrigger = trigger
		switch trigger {
		case state.TriggerAuto:
			windowMax = autoWindowMax
		case state.TriggerPause:
			windowMax = pauseWindowMax
		default:
			windowMax = manualWindowMax
		}
		windowDeadline = now.Add(dur)
		windowTimer = time.NewTimer(dur)
		windowTimerC = windowTimer.C
		winDiscTick = time.NewTicker(winInterval)
		winDiscC = winDiscTick.C
		enfErr = nil
		relaxation := "all outbound allowed"
		if o.windowRestricted() {
			relaxation = "egress relaxed to configured protocols/ports"
		}
		switch trigger {
		case state.TriggerAuto:
			o.Log.Warn("RECONNECT WINDOW OPEN — "+relaxation+"; tunnel dropped, redial any VPN now (real IP may be exposed until it closes)",
				"until", windowDeadline)
		case state.TriggerPause:
			o.Log.Warn("PAUSED — "+relaxation+"; protection resumes automatically at the deadline (real IP is exposed until then)",
				"until", windowDeadline)
		default:
			o.Log.Warn("SWITCH WINDOW OPEN — "+relaxation+"; connect your VPN now (real IP may be exposed until it closes)",
				"until", windowDeadline, "profile", profile)
		}
		snapshot()
	}

	// maybeAutoWindow opens the automatic reconnect window on a tunnel up→down
	// edge. Only from a healthy standing GUARD: never in standby (egress already
	// open), never from FULL BLOCK (the last known exit was forbidden — relaxing
	// from a known-bad state needs an explicit operator command), never while a
	// window is already open, and never for a tunnel that was only ever presumed
	// up. The anti-flap gate keeps a flapping VPN from chaining windows.
	maybeAutoWindow := func(now time.Time, detail string) {
		if o.ReconnectWindow <= 0 || windowActive || standby || blocked || !sawTunnelUp {
			return
		}
		if minUp := o.ReconnectMinUptime; minUp > 0 && !goodExitThisUp &&
			!tunnelUpSince.IsZero() && now.Sub(tunnelUpSince) < minUp {
			o.Log.Warn("vpn tunnel down — reconnect window suppressed (flap guard: tunnel up "+
				now.Sub(tunnelUpSince).Round(time.Second).String()+" with no confirmed exit); guard holds",
				"minUptime", minUp, "detail", detail)
			return
		}
		openWindow(now, o.ReconnectWindow, "", state.TriggerAuto)
	}

	// closeWindowRevert reverts to the prior posture (expiry / cancel). Session-
	// discovered endpoints stay in `endpoints` (grow-only during the window), so if
	// a handshake was mid-flight the restored guard holds its endpoint open and the
	// tunnel can still complete under GUARD.
	closeWindowRevert := func(reason string) {
		rebuild()
		target := guard
		if windowPrevBlocked {
			target = fullBlock
		}
		if err := o.Backend.Apply(target); err != nil {
			// The revert failed, so the firewall may still be in switch-window
			// posture. Do NOT report the window closed or unsuppress the geo state
			// machine — keep it active and re-arm a short retry so the daemon keeps
			// trying to enforce the deadline instead of leaving egress relaxed.
			enfErr = err
			o.Log.Error("restore posture after "+windowNoun()+" failed — holding window open, will retry",
				"reason", reason, "err", err)
			if windowTimer != nil {
				windowTimer.Stop()
			}
			windowTimer = time.NewTimer(windowRevertRetry)
			windowTimerC = windowTimer.C
			snapshot()
			return
		}
		stopWindowTimers()
		windowActive = false
		blocked = windowPrevBlocked
		enfErr = nil
		o.Log.Info(windowNoun()+" closed", "reason", reason, "posture", postureName(blocked, false, standby))
		snapshot()
	}

	// Early success close of a switch window: a tunnel is up, a VPN server socket
	// has actually been DISCOVERED this window, AND a bounded geo lookup reads a
	// NON-blocked exit. Only then do we close toward GUARD and learn the
	// discovered endpoint. A blocked or failed reading keeps the window open
	// (it may be a physical-path leak reading, or the exit really is forbidden —
	// either way, not safe to trust yet). We never feed this reading into the
	// Decider's hysteresis streak (path-ambiguous).
	//
	// The check is split in two so the geo lookup — the only slow part — never
	// runs on the run loop. Inline it blocked every select case for up to
	// probeEgressBudget at the 2s discovery cadence, so control-socket requests
	// (notably `switch --cancel`, the one op an operator needs mid-window) hit
	// "daemon busy" precisely while a window was open with a VPN mid-connect.
	// maybeStartCloseProbe only OBSERVES (Monitor.Once off-loop, single-flight);
	// finishCloseProbe runs back on the loop with the outcome, re-validates the
	// preconditions (the world may have changed during the probe), and alone
	// touches the Backend — the single-goroutine Apply invariant holds.
	//
	// CAVEAT: o.Monitor.Once uses the OS default route (no interface binding), and
	// while the window is open ALL egress is allowed — so a non-blocked reading is
	// not by itself proof the exit was reached through the tunnel (a full-tunnel
	// VPN mid-handshake may not own the default route yet, and a benign physical
	// exit reads as "allowed"). We reduce that false-positive by requiring live
	// socket-discovery evidence (a connection to a VPN-server endpoint exists this
	// window) rather than trusting a stale static endpoint. The residual is bounded
	// and safe: an early close lands on GUARD, which keeps the discovered endpoint
	// open so the handshake still completes, and the next GUARD-posture geo tick
	// re-checks the exit through the Decider. The learned endpoints come from the
	// socket table, not the geo reading, so attribution stays correct regardless.
	type probeOutcome struct {
		r   monitor.Reading
		err error
	}
	probeResC := make(chan probeOutcome, 1)
	probeInFlight := false

	maybeStartCloseProbe := func() {
		if probeInFlight || !windowActive || len(tunnels) == 0 {
			return
		}
		if len(discoveredAddrs(lastSet)) == 0 {
			return // no live VPN socket yet — a static endpoint alone can't confirm a connect
		}
		probeInFlight = true
		go func() {
			pctx, cancel := context.WithTimeout(ctx, probeEgressBudget)
			r, err := o.Monitor.Once(pctx)
			cancel()
			select {
			case probeResC <- probeOutcome{r: r, err: err}:
			case <-ctx.Done(): // loop is gone; don't leak this goroutine on the send
			}
		}()
	}

	finishCloseProbe := func(p probeOutcome) {
		probeInFlight = false
		if !windowActive || len(tunnels) == 0 {
			return
		}
		disc := discoveredAddrs(lastSet)
		if len(disc) == 0 {
			return // the socket vanished mid-probe — evidence gone, hold the window
		}
		r, err := p.r, p.err
		if err != nil || r.CountryCode == "" || blockedContains(o.BlockedCountries, r.CountryCode) {
			o.Log.Debug("switch window: exit not yet confirmed allowed, holding window open",
				"country", r.CountryCode, "err", err)
			return
		}
		rebuild()
		if err := o.Backend.Apply(guard); err != nil {
			// Guard apply failed — the firewall may still be in switch-window
			// posture. Keep the window active (timers still running, so the
			// winDisc ticker retries this) and do NOT close state, learn, or set
			// the active profile: reporting a close now would unsuppress geo and
			// claim attribution while egress may still be relaxed.
			enfErr = err
			o.Log.Error("close switch window to guard failed — holding window open, will retry", "err", err)
			return
		}
		stopWindowTimers()
		windowActive = false
		blocked = false
		enfErr = nil
		lastRes = monitor.Result{Reading: r}
		// The window verified a good exit for this profile — it is now the active
		// one, and stays so (sticky) until the next switch names another.
		if windowProfile != "" {
			activeProfile = windowProfile
		}
		if o.Learn != nil {
			o.Learn(windowProfile, firstOr(tunnels), disc)
			o.Log.Info("learned vpn endpoint(s) from switch window", "profile", windowProfile, "count", len(disc))
		}
		o.Log.Info(windowNoun()+" closed early (exit verified)", "country", r.CountryCode, "tunnels", tunnels)
		snapshot()
	}

	// tryAutoArm arms the guard out of standby once a tunnel is up (vpn.autoArm).
	// Endpoints are refreshed first, and arming is refused while none are known:
	// with the tunnel up, the guard's block-all covers the tunnel's own encrypted
	// transport, so arming endpoint-less would black out the working VPN — the
	// same hazard the startup refusal explains. Discoverable VPNs surface their
	// socket right after connect, so the refresh normally resolves this on the
	// very tunnel-up event that triggered arming; retried on endpoint refreshes.
	tryAutoArm := func(detail string) {
		if !standby {
			return
		}
		fresh := o.resolveEndpointsWith(ctx, tunnels)
		lastSet = fresh
		if next, changed := reconcileWithGrace(endpoints, fresh, false, epLastSeen, time.Now(), epGrace); changed {
			endpoints = next
		}
		if len(endpoints) == 0 {
			o.Log.Warn("auto-arm: tunnel is up but no VPN server endpoint is known — staying in standby. "+
				"Name the server (dezhban vpn import/add, or config set vpn.endpoints=...) so the guard can arm.",
				"detail", detail)
			return
		}
		rebuild()
		if err := o.Backend.Apply(guard); err != nil {
			enfErr = err
			o.Log.Error("auto-arm: applying the guard failed — staying in standby", "err", err)
			return
		}
		standby = false
		enfErr = nil
		o.Log.Info("AUTO-ARMED — vpn connected, guard active",
			"mode", guard.Mode, "tunnels", tunnels, "endpoints", len(endpoints), "detail", detail)
	}

	// handleControl services one control-socket request. It runs INLINE on the run
	// loop (as a select case), which is what lets it call Backend.Apply at all —
	// the accept goroutine never does. Every path returns a Response; the server is
	// waiting on it under a timeout.
	// Forward declaration: applyLive needs the tickers built further down, but
	// handleControl below needs to call it. Assigned exactly once, before the
	// select loop that can invoke either.
	var applyLive func(LiveSettings)

	handleControl := func(req control.Request) control.Response {
		reply := func(ok bool, msg string) control.Response {
			return control.Response{
				OK:      ok,
				Error:   msg,
				Posture: postureName(blocked, windowActive, standby),
				Blocked: blocked,
			}
		}
		switch req.Op {
		case control.OpStatus:
			return reply(true, "")

		case control.OpReload:
			// Re-reads the root-owned config file the daemon already trusts, so
			// this grants the caller no authority they did not have — it only
			// asks the daemon to notice a change. The reply names both halves of
			// the outcome so nothing downstream can claim a key took effect when
			// it is still being enforced at its old value.
			if o.ReloadConfig == nil {
				return reply(false, "this daemon cannot reload its configuration")
			}
			ls, report, rerr := o.ReloadConfig()
			if rerr != nil {
				o.Log.Warn("control: config reload failed; continuing on the running configuration", "err", rerr)
				return reply(false, "reload failed: "+rerr.Error())
			}
			applyLive(ls)
			resp := reply(true, "")
			resp.Applied = report.Applied
			resp.NeedsRestart = report.NeedsRestart
			return resp

		case control.OpBlock:
			if windowActive {
				// A switch window owns the rules. Silently tearing it down here would
				// contradict the operator's other explicit request; make them choose.
				return reply(false, "switch window is open — cancel it first")
			}
			if blocked {
				manualBlock = true // already blocked by geo: adopt and hold it
				return reply(true, "")
			}
			rebuild()
			if err := o.Backend.Apply(fullBlock); err != nil {
				enfErr = err
				o.Log.Error("control: full block failed", "err", err)
				snapshot()
				return reply(false, "full block failed: "+err.Error())
			}
			blocked = true
			manualBlock = true
			standby = false // a manual block arms enforcement out of standby
			enfErr = nil
			o.Log.Warn("FULL BLOCK (manual, via control socket) — held until unblock")
			snapshot()
			return reply(true, "")

		case control.OpUnblock:
			if windowActive {
				return reply(false, "switch window is open — cancel it first")
			}
			manualBlock = false
			// vpn.autoArm: with the tunnel DOWN, an explicit unblock is the
			// operator saying "the VPN is off on purpose — release the line".
			// Return to standby (no enforcement) instead of restoring a guard
			// that, with no tunnel, is a total cut. With the tunnel up the
			// normal guard restore below is what they want.
			if o.AutoArm && !tunnelUp && !standby {
				if err := o.Backend.Unblock(); err != nil {
					enfErr = err
					o.Log.Error("control: standby release failed", "err", err)
					snapshot()
					return reply(false, "standby release failed: "+err.Error())
				}
				standby = true
				blocked = false
				enfErr = nil
				o.Log.Info("STANDBY (manual unblock, vpn.autoArm) — guard released; re-arms when a VPN connects")
				snapshot()
				return reply(true, "")
			}
			if !blocked {
				return reply(true, "")
			}
			rebuild()
			if err := o.Backend.Apply(guard); err != nil {
				enfErr = err
				o.Log.Error("control: guard restore failed", "err", err)
				snapshot()
				return reply(false, "guard restore failed: "+err.Error())
			}
			blocked = false
			enfErr = nil
			o.Log.Info("GUARD (manual unblock, via control socket) — geo state machine resumed")
			snapshot()
			return reply(true, "")

		case control.OpOpenSwitch:
			if standby {
				// Nothing to relax: standby enforces nothing, so the VPN can
				// already connect freely — and the guard arms itself when it does.
				return reply(false, "standby — egress is already open; connect your VPN and the guard arms itself")
			}
			if !o.AllowSwitchOps {
				return reply(false, "switch ops over the control socket are disabled (control.allowSwitchOps)")
			}
			if !switchEnabled {
				return reply(false, "switch window unavailable (vpn.switchWindow not configured)")
			}
			if windowActive && windowTrigger == state.TriggerPause {
				// openWindow's manual-takeover path would silently rebrand the
				// pause as a switch window — after which resume no-ops ("already
				// closed") while egress stays open. Symmetric with OpPause's
				// refusal while a switch window is open: the two relaxations
				// never stomp each other's attribution.
				return reply(false, "a pause is open — resume it first")
			}
			openWindow(time.Now(), o.clampWindow(req.Duration), req.Profile, state.TriggerManual)
			if !windowActive {
				return reply(false, "open switch window failed")
			}
			// An operator opening a window is taking over from any manual block: the
			// window's own revert (windowPrevBlocked) restores the posture, and leaving
			// manualBlock set would suspend geo forever after it closes.
			manualBlock = false
			return reply(true, "")

		case control.OpCancelSwitch:
			if !o.AllowSwitchOps {
				return reply(false, "switch ops over the control socket are disabled (control.allowSwitchOps)")
			}
			if !windowActive {
				return reply(true, "") // already closed — the caller's intent already holds
			}
			if windowTrigger == state.TriggerPause {
				return reply(false, "a pause is open, not a switch window — use resume instead")
			}
			closeWindowRevert("cancelled (control socket)")
			if windowActive {
				return reply(false, "cancel failed — window held open, revert is being retried")
			}
			return reply(true, "")

		case control.OpPause:
			if standby {
				// Nothing to relax: standby already has no rules, so there is
				// nothing to pause.
				return reply(false, "standby — egress is already open; nothing to pause")
			}
			if !o.AllowPauseOps {
				return reply(false, "pause ops over the control socket are disabled (control.allowPauseOps)")
			}
			if !pauseEnabled {
				return reply(false, "pause unavailable (vpn.pauseMax: \"0\" — disabled)")
			}
			if windowActive && windowTrigger != state.TriggerPause {
				return reply(false, "a switch window is open — cancel it first")
			}
			openWindow(time.Now(), o.clampPause(req.Duration), "", state.TriggerPause)
			if !windowActive {
				return reply(false, "open pause failed")
			}
			// Mirrors OpOpenSwitch: an operator pausing is taking over from any
			// manual block, and the window's own revert restores the posture.
			manualBlock = false
			return reply(true, "")

		case control.OpResume:
			if !o.AllowPauseOps {
				return reply(false, "pause ops over the control socket are disabled (control.allowPauseOps)")
			}
			if !windowActive || windowTrigger != state.TriggerPause {
				return reply(true, "") // already closed — the caller's intent already holds
			}
			closeWindowRevert("resumed (control socket)")
			if windowActive {
				return reply(false, "resume failed — pause held open, revert is being retried")
			}
			return reply(true, "")
		}
		return reply(false, fmt.Sprintf("unsupported op %q", req.Op))
	}

	var ctlC <-chan control.ConnRequest
	if o.Control != nil {
		ctlC = o.Control.Requests()
	}

	var tunCh <-chan netdetect.TunnelState
	if o.Watcher != nil {
		tunCh = o.Watcher.Watch(ctx)
	}

	epInterval := o.EndpointRefresh
	if epInterval <= 0 {
		epInterval = time.Minute
	}
	epTick := time.NewTicker(epInterval)
	defer epTick.Stop()
	geoTick := time.NewTicker(o.Interval)
	defer geoTick.Stop()

	// applyLive adopts replacement settings on the run-loop goroutine. It updates
	// `o` (a per-call copy, so nothing is shared with another run) plus the
	// locals derived from it at startup, and reinstalls the standing rules when
	// a change actually alters them.
	//
	// What it deliberately does NOT touch is an open window: `windowMax` and the
	// running deadline are fixed at first open, and a reload arriving mid-episode
	// must not be able to lengthen a live relaxation of the guard. New window
	// settings apply to the next one.
	applyLive = func(ls LiveSettings) {
		policyChanged := ls.AllowPhysicalDNS != o.AllowPhysicalDNS ||
			ls.AllowLocalNetwork != o.AllowLocalNetwork

		// Durations are only adopted when they are actually usable. A zero here
		// would mean a caller sent a partly-filled struct, and honouring it would
		// stop the poll ticker outright — keeping the running value is the safe
		// reading of "not specified".
		if ls.Interval > 0 {
			if ls.Interval != o.Interval {
				geoTick.Reset(ls.Interval)
			}
			o.Interval = ls.Interval
		}

		if ls.Decider != nil {
			o.Decider = ls.Decider
		}
		o.BlockedCountries = ls.BlockedCountries
		o.Autodetect = ls.Autodetect
		o.AllowPhysicalDNS = ls.AllowPhysicalDNS
		o.AllowLocalNetwork = ls.AllowLocalNetwork
		o.AutoArm = ls.AutoArm
		o.AllowSwitchOps = ls.AllowSwitchOps
		o.AllowPauseOps = ls.AllowPauseOps
		o.ReconnectWindow = ls.ReconnectWindow
		o.ReconnectMinUptime = ls.ReconnectMinUptime
		o.EndpointGrace = ls.EndpointGrace
		o.SwitchWindow = ls.SwitchWindow
		o.WindowDiscoveryInterval = ls.WindowDiscoveryInterval

		// Whether each trigger is available at all is recomputed here, so
		// setting a window to "0" disables it live — and the other two triggers
		// stay independent, exactly as they are at startup.
		switchEnabled = o.SwitchWindow > 0 && o.PollCommand != nil
		if ls.SwitchWindowMax > 0 {
			manualWindowMax = ls.SwitchWindowMax
		}
		if ls.ReconnectWindowMax > 0 {
			autoWindowMax = ls.ReconnectWindowMax
		}
		pauseWindowMax = ls.PauseMax
		pauseEnabled = pauseWindowMax > 0 && o.PollCommand != nil

		if ls.EndpointRefresh > 0 {
			if ls.EndpointRefresh != o.EndpointRefresh {
				epTick.Reset(ls.EndpointRefresh)
			}
			o.EndpointRefresh = ls.EndpointRefresh
		}

		o.Log.Info("configuration reloaded",
			"interval", o.Interval,
			"blocked_countries", o.BlockedCountries,
			"switch_window", o.SwitchWindow,
			"reconnect_window", o.ReconnectWindow,
			"pause_max", o.PauseMax,
		)
		if policyChanged {
			reapplyStanding("configuration reloaded")
		}
		snapshot()
	}

	var cmdC <-chan time.Time
	if switchEnabled || pauseEnabled {
		cmdInterval := o.CommandPoll
		if cmdInterval <= 0 {
			cmdInterval = time.Second
		}
		cmdTick := time.NewTicker(cmdInterval)
		defer cmdTick.Stop()
		cmdC = cmdTick.C
		// A stale command file left by a prior run is the CLI's responsibility to
		// discard at startup; the poll here only acts on fresh commands.
	}

	// Startup observation: only meaningful with a tunnel up and an endpoint known.
	// With zero tunnels (standing posture) a lookup egresses nowhere useful.
	if len(tunnels) > 0 && len(endpoints) > 0 {
		lastRes, enfErr = o.vpnGeoStep(ctx, guard, fullBlock, &blocked, tunnelUp)
		if lastRes.Err == nil && !blocked {
			// A confirmed allowed exit proves the tunnel is carrying traffic.
			goodExitThisUp = true
			// But with a watcher, up/down is the watcher's to report: this
			// startup reading only presumes up, and had the tunnel actually been
			// down it could have egressed the allowlisted physical path. Let the
			// watcher's own up sample set sawTunnelUp, so an auto reconnect window
			// never opens for a tunnel it never observed up.
			if o.Watcher == nil {
				sawTunnelUp = true
				markTunnelEverUp(time.Now())
			}
		}
	}
	snapshot()

	for {
		select {
		case <-ctx.Done():
			return nil
		case st, ok := <-tunCh:
			if !ok {
				tunCh = nil
				continue
			}
			if st.Unknown {
				// Interface enumeration hiccup, not a real edge: hold the last known
				// tunnel state. Treating it as an up/down transition would misreport
				// the tunnel and wrongly gate the tunnel-down geo-skip. The guard's
				// standing rule still covers a genuine leak.
				o.Log.Debug("ignoring tunnel sample with unknown state", "detail", st.Detail)
				continue
			}
			wasUp := tunnelUp
			tunnelUp = st.Up
			if st.Up {
				sawTunnelUp = true
				markTunnelEverUp(time.Now())
				if !wasUp {
					tunnelUpSince = time.Now()
					goodExitThisUp = false
				}
			}
			switch {
			case st.Up:
				o.Log.Info("vpn tunnel up", "detail", st.Detail)
			case standby:
				o.Log.Debug("tunnel down (standby — not enforcing)", "detail", st.Detail)
			default:
				o.Log.Warn("vpn tunnel down — guard holds the line (physical egress stays blocked, "+
					"endpoints open for reconnect)", "detail", st.Detail)
			}
			if next, changed := reconcileTunnels(tunnels, st.Names, pinned); changed {
				tunnels = next
				reapplyStanding("tunnel set changed")
				reapplyWindow("tunnel set changed")
			}
			lastTun = tunnelSnapshot(st, tunnels)
			if standby && st.Up {
				tryAutoArm(st.Detail)
			}
			if wasUp && !st.Up {
				maybeAutoWindow(time.Now(), st.Detail)
			}
			if windowActive {
				maybeStartCloseProbe()
			}
			snapshot()
		case ls := <-o.ReloadC:
			applyLive(ls)
		case <-cmdC:
			cmd, ok := o.PollCommand()
			if !ok {
				continue
			}
			now := time.Now()
			switch cmd.Op {
			case command.OpOpenSwitchWindow:
				if standby {
					o.Log.Info("ignoring switch-window command in standby — egress is already open; " +
						"connect your VPN and the guard arms itself")
					continue
				}
				if windowActive && windowTrigger == state.TriggerPause {
					// Same rail as the socket path: a manual open must not
					// rebrand an open pause via openWindow's takeover.
					o.Log.Warn("ignoring switch-window command — a pause is open (resume it first)")
					continue
				}
				dur := o.clampWindow(cmd.Duration)
				if dur <= 0 {
					// Unreachable while the command poll only ticks when
					// switchEnabled, but never open a window the operator
					// disabled — this is the guard's only relaxation.
					o.Log.Warn("ignoring switch-window command — manual switch windows are disabled (vpn.switchWindow: \"0\")")
					continue
				}
				openWindow(now, dur, cmd.Profile, state.TriggerManual)
			case command.OpCancelSwitchWindow:
				if windowActive && windowTrigger == state.TriggerPause {
					// Mirror the socket path's refusal: a pause is ended by
					// resume, and the resume command-file op works for root.
					o.Log.Warn("ignoring switch-cancel command — a pause is open (use resume)")
					continue
				}
				if windowActive {
					closeWindowRevert("cancelled")
				}
			case command.OpPause:
				if standby {
					o.Log.Info("ignoring pause command in standby — egress is already open; nothing to pause")
					continue
				}
				if windowActive && windowTrigger != state.TriggerPause {
					o.Log.Warn("ignoring pause command — a switch window is already open (cancel it first)")
					continue
				}
				dur := o.clampPause(cmd.Duration)
				if dur <= 0 {
					// Unreachable while the command poll only ticks when pauseEnabled,
					// but never open a pause the operator disabled.
					o.Log.Warn("ignoring pause command — pausing is disabled (vpn.pauseMax: \"0\")")
					continue
				}
				openWindow(now, dur, "", state.TriggerPause)
				manualBlock = false
			case command.OpResume:
				if windowActive && windowTrigger == state.TriggerPause {
					closeWindowRevert("resumed")
				}
			default:
				o.Log.Debug("ignoring unsupported command", "op", cmd.Op)
			}
		case cr := <-ctlC:
			cr.Reply <- handleControl(cr.Req)
		case <-windowTimerC:
			if windowActive {
				closeWindowRevert("expired")
			}
		case p := <-probeResC:
			finishCloseProbe(p)
		case <-winDiscC:
			// Fast in-window discovery: grow the endpoint set as the new server's
			// socket appears, then try to close.
			fresh := o.resolveEndpointsWith(ctx, tunnels)
			lastSet = fresh
			if next, changed := reconcileWithGrace(endpoints, fresh, true, epLastSeen, time.Now(), epGrace); changed {
				endpoints = next
				reapplyWindow("in-window endpoint discovery")
			}
			maybeStartCloseProbe()
		case <-epTick.C:
			// Refresh the provider IPs on the same cadence. CDN-fronted providers
			// rotate addresses, and a stale set means the tunnel-scoped pass no
			// longer covers where the lookup actually connects — which would send
			// recovery back to lift-and-probe without anyone noticing.
			//
			// This refresh is expected to SUCCEED in GUARD and FAIL in FULL BLOCK:
			// the provider pass deliberately carries no DNS rule (an unscoped one
			// would leak every lookup to the forbidden exit — see ADR-0006), so
			// there is no resolution path while cut. That is why the branch below
			// fires rarely. A rotation mid-block therefore degrades to
			// lift-and-probe, whose guard lift lets the next refresh through and
			// heals the scoped pass.
			if o.ResolveProviders != nil {
				if fresh := o.ResolveProviders(ctx); len(fresh) > 0 && !sameAddrs(fresh, providers) {
					providers = fresh
					reapplyStanding("provider refresh")
					if blocked {
						// FULL BLOCK is the posture that carries these rules, and
						// reapplyStanding deliberately skips it. Re-apply directly so a
						// rotated provider IP becomes reachable without waiting for the
						// exit to change.
						if err := o.Backend.Apply(fullBlock); err != nil {
							o.Log.Error("provider refresh: re-applying full block failed", "err", err)
						}
					}
				}
			}
			fresh := o.resolveEndpointsWith(ctx, tunnels)
			lastSet = fresh
			// An open window is grow-only like a block: a plain refresh must not
			// replace (and thus drop) the session/in-window endpoints the window is
			// keeping open until it closes or reverts.
			growOnly := blocked || windowActive
			if next, changed := reconcileWithGrace(endpoints, fresh, growOnly, epLastSeen, time.Now(), epGrace); changed {
				endpoints = next
				reapplyStanding("endpoint refresh")
				reapplyWindow("endpoint refresh")
				snapshot()
			}
			// A standby daemon whose earlier arm attempt found no endpoint
			// retries here — the refresh may have surfaced one.
			if standby && tunnelUp {
				tryAutoArm("endpoint refresh")
				snapshot()
			}
		case <-geoTick.C:
			if standby {
				continue // not enforcing — nothing to decide, nothing to protect a probe with
			}
			if windowActive {
				continue // window suppresses the geo state machine
			}
			if manualBlock {
				// An operator asked for this block. Recovery must not lift it behind
				// their back — including the probe, which would briefly open egress to
				// observe a country nobody is going to act on. Held until `unblock`.
				o.Log.Debug("manual block held — skipping geo lookup (run `dezhban unblock` to resume)")
				continue
			}
			if len(tunnels) == 0 {
				continue // standing posture: nothing to observe until a tunnel exists
			}
			if o.Watcher != nil && !tunnelUp && !blocked {
				o.Log.Debug("vpn tunnel down — skipping geo lookup (guard holds, endpoints open for reconnect)")
				continue
			}
			lastRes, enfErr = o.vpnGeoStep(ctx, guard, fullBlock, &blocked, tunnelUp)
			if lastRes.Err == nil && !blocked {
				goodExitThisUp, sawTunnelUp = true, true // confirmed exit through the tunnel
				markTunnelEverUp(time.Now())
			}
			snapshot()
		}
	}
}

// vpnPolicies builds the GUARD and FULL BLOCK policies for the given endpoint
// set. FULL BLOCK under a tunnel cuts the tunnel too: the dst-IP allowlist is
// meaningless on encrypted outer packets, so it is omitted.
// vpnPolicies builds the GUARD (standing) and FULL BLOCK policies for the given
// tunnel and endpoint sets. The GUARD side is the standing posture: normal
// ModeGuard when at least one tunnel exists, else a ModeFullBlock endpoints-open
// shape (physically fail-closed, handshake paths open) so the daemon can run
// before any VPN is connected without the backend rejecting an empty-iface
// guard. FULL BLOCK cuts the tunnel too — the dst-IP allowlist is meaningless on
// encrypted outer packets, so it is omitted.
func (o Options) vpnPolicies(tunnels []string, endpoints, providers []netip.Addr) (guard, fullBlock firewall.Policy) {
	in := o.policyInput(tunnels, endpoints, providers)
	return in.Guard(), in.FullBlock()
}

// policyInput gathers the posture-shaping options into the firewall package's
// shared constructor input, so the run loop and print-rules build postures from
// one definition (firewall.PolicyInput).
//
// Allowlist is deliberately left zero: a VPN posture opens endpoints, not a
// physical dst-IP allowlist, which is meaningless against encrypted outer
// packets. The runner's separate Allowlist hook feeds the legacy Block path only.
//
// Invalid addresses are dropped by the constructor (they would otherwise render
// as "invalid IP" and make pf reject the whole ruleset). That drop is silent by
// design at the seam, so report it here, where a logger exists: a dropped
// endpoint means a tunnel that cannot handshake, and the operator should not
// have to infer that from a VPN that merely fails to connect.
func (o Options) policyInput(tunnels []string, endpoints, providers []netip.Addr) firewall.PolicyInput {
	if o.Log != nil {
		if n := firewall.CountInvalid(endpoints); n > 0 {
			o.Log.Warn("dropping invalid vpn endpoint address(es) from the ruleset — the tunnel may be unable to handshake",
				"dropped", n, "endpoints", len(endpoints))
		}
		if n := firewall.CountInvalid(providers); n > 0 {
			o.Log.Warn("dropping invalid geo-provider address(es) from the ruleset — recovery may fall back to lift-and-probe",
				"dropped", n, "providers", len(providers))
		}
	}
	return firewall.PolicyInput{
		Tunnels:           tunnels,
		TunnelGroups:      o.TunnelGroups,
		Endpoints:         endpoints,
		AllowPhysicalDNS:  o.AllowPhysicalDNS,
		AllowLocalNetwork: o.AllowLocalNetwork,
		WindowProtos:      o.WindowProtos,
		WindowPorts:       o.WindowPorts,
		ProviderAddrs:     providers,
	}
}

// windowRestricted reports whether the switch window filters egress by
// protocol/port. The default (unrestricted) window passes ALL outbound, so a
// tunnel/endpoint change during it needs no rule update; a restricted window
// must be re-applied to admit a newly-appeared tunnel or endpoint.
func (o Options) windowRestricted() bool {
	return len(o.WindowProtos) > 0 || len(o.WindowPorts) > 0
}

// windowPolicy builds the switch-window policy from the current tunnel/endpoint
// sets plus any configured restriction knobs.
func (o Options) windowPolicy(tunnels []string, endpoints []netip.Addr) firewall.Policy {
	return o.policyInput(tunnels, endpoints, nil).SwitchWindow()
}

// reconcileTunnels merges the observed tunnel set into the current one: every
// pinned (explicit-config) name is always kept; observed names are added; and
// non-pinned names no longer observed are dropped (the watcher already debounced
// the shrink). The result is never empty — an empty next keeps the current set.
// Returns the new set and whether it changed.
func reconcileTunnels(current, observed []string, pinned map[string]bool) ([]string, bool) {
	seen := make(map[string]bool)
	var next []string
	add := func(n string) {
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		next = append(next, n)
	}
	for _, n := range current {
		if pinned[n] {
			add(n)
		}
	}
	for _, n := range observed {
		add(n)
	}
	sort.Strings(next)
	if len(next) == 0 {
		return current, false
	}
	if sameStrings(current, next) {
		return current, false
	}
	return next, true
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// vpnGeoStep performs one geo observation and applies the resulting transition.
// While blocked it observes through the bounded recovery probe; otherwise it
// reads directly. Probe readings feed the same hysteresis streak, so recovery
// takes `Hysteresis` consecutive allowed probes. It returns the observed result
// so the caller can publish the last-known reading. The second return is the last
// firewall-action failure (a failed FULL BLOCK / guard restore, or a probe re-cut
// that left egress open), or nil when the intended posture was achieved.
//
// tunnelUp only classifies how a FAILED lookup is reported — it never changes
// enforcement. With no tunnel there is no exit to measure, so a failure is
// expected rather than a fault.
func (o Options) vpnGeoStep(ctx context.Context, guard, fullBlock firewall.Policy, blocked *bool, tunnelUp bool) (monitor.Result, error) {
	var res monitor.Result
	var enfErr error
	if *blocked {
		res, enfErr = o.probe(ctx, guard, fullBlock)
	} else {
		r, err := o.Monitor.Once(ctx)
		res = monitor.Result{Reading: r, Err: err}
	}
	if res.Err != nil {
		// Say which of these it is. A lookup that fails because there is no
		// tunnel to measure through is not a fault — it is the normal state
		// during a switch/reconnect window (the tunnel is down; that is why the
		// window exists), in standby, and across any drop. Logging that at Warn
		// alongside genuine failures is what made the geo providers look broken.
		if tunnelUp {
			o.Log.Warn("exit-country lookup failed with the tunnel up — the exit may be blocking the geo providers; guard holds", "err", res.Err)
		} else {
			o.Log.Debug("exit country unknown — no tunnel is up, so there is no VPN exit to check", "err", res.Err)
		}
		// Either way: hold the current posture. The standing guard already blocks
		// physical leaks, so an unknown must not escalate GUARD→FULL BLOCK (which
		// cuts tunnel egress and livelocks the reconnect) nor lift an active FULL
		// BLOCK on a blip. Only a *successful* reading moves the state machine.
		return res, enfErr
	}
	cc := res.Reading.CountryCode

	switch o.Decider.Evaluate(res) {
	case decision.Block:
		if !*blocked {
			if err := o.Backend.Apply(fullBlock); err != nil {
				o.Log.Error("full block failed", "err", err, "country", cc)
				enfErr = err
			} else {
				o.Log.Info("FULL BLOCK", "country", cc)
				*blocked = true
				enfErr = nil
			}
		}
		// Already blocked: the probe above re-cut to FULL BLOCK; enfErr carries any
		// re-cut failure it reported.
	case decision.Allow:
		if *blocked {
			if err := o.Backend.Apply(guard); err != nil {
				o.Log.Error("guard restore failed", "err", err, "country", cc)
				enfErr = err
			} else {
				o.Log.Info("GUARD", "country", cc)
				*blocked = false
				enfErr = nil
			}
		}
	}
	return res, enfErr
}

// probe is the VPN recovery probe: observe the exit country while in FULL BLOCK,
// so a tunnel that returns to an allowed country can lift the block.
//
// Two paths. When the FULL BLOCK ruleset carries tunnel-scoped provider passes
// (the normal case) the lookup just runs — no rule change, no leak. Only when
// those are unavailable does it fall back to the historical behaviour below:
// briefly lift the guard, observe, and re-cut immediately, with the egress
// window bounded by probeEgressBudget. A failed re-cut leaves egress open; it is
// logged at error and the next tick re-applies the block.
// Both GUARD and FULL BLOCK keep the endpoint passes open, so the encrypted
// tunnel transport survives the re-cut — the probe toggles only the tunnel's
// user-egress, never tearing down a tunnel that has reconnected. That is what
// lets a genuinely-down tunnel come back and a later probe observe an allowed
// country, instead of the block livelocking the reconnect.
// It returns the observed result plus any re-cut failure: a failed re-cut leaves
// egress open until the next tick, so it is surfaced as an enforcement error. A
// failed guard LIFT is not — the guard still holds, egress stays cut — so that path
// returns a nil enforcement error and reports the miss as a lookup failure instead.
func (o Options) probe(ctx context.Context, guard, fullBlock firewall.Policy) (monitor.Result, error) {
	// Fast path: FULL BLOCK already passes the geo providers through the tunnel,
	// so the lookup needs no guard lift at all. This is the whole point of the
	// tunnel-scoped provider rule — the old path opened FULL tunnel egress for up
	// to probeEgressBudget on EVERY probe tick just to make one HTTP request, a
	// recurring leak measured in seconds per tick for the entire time a forbidden
	// exit persisted.
	//
	// The measurement stays honest because the pass is scoped to the tunnel: with
	// the tunnel down the lookup simply fails and the posture holds, exactly as it
	// should. A physical-link pass would instead succeed and report the ISP's
	// country, silently defeating the check (docs/adr/0006).
	if len(fullBlock.ProviderAddrs) > 0 {
		pctx, cancel := context.WithTimeout(ctx, probeEgressBudget)
		r, err := o.Monitor.Once(pctx)
		cancel()
		return monitor.Result{Reading: r, Err: err}, nil
	}
	// Fallback: no provider IPs resolved (resolution failed, or the backend could
	// not express the rule). Lift briefly, observe, re-cut — a bounded leak, but
	// far better than a FULL BLOCK that can never observe its way out.
	if err := o.Backend.Apply(guard); err != nil {
		// Could not open the tunnel to look — report as a lookup failure so the
		// Decider treats the country as undeterminable (fail-closed keeps blocking).
		// The guard still holds, so this is not an open-egress enforcement failure.
		o.Log.Error("recovery probe: lift guard failed", "err", err)
		return monitor.Result{Err: fmt.Errorf("probe lift failed: %w", err)}, nil
	}
	// Bound the open-guard window: while the guard is lifted, egress flows to the
	// (possibly forbidden) exit. A hung tunnel or a quorum lookup over several
	// providers would otherwise keep it open for the full lookup timeout(s). Cap
	// the window — a bounded leak that retries next tick beats an open guard. If
	// the probe times out it reports an error → fail-closed keeps the block.
	pctx, cancel := context.WithTimeout(ctx, probeEgressBudget)
	r, err := o.Monitor.Once(pctx)
	cancel()
	var enfErr error
	if cerr := o.Backend.Apply(fullBlock); cerr != nil {
		o.Log.Error("recovery probe: re-cut to full block failed — egress may be open until next tick", "err", cerr)
		enfErr = cerr
	}
	return monitor.Result{Reading: r, Err: err}, enfErr
}

// resolveEndpoints returns the current endpoint set, using ResolveEndpoints when
// supplied or falling back to the static Endpoints (tests / legacy callers).
func (o Options) resolveEndpoints(ctx context.Context) netdetect.EndpointSet {
	if o.ResolveEndpoints != nil {
		return o.ResolveEndpoints(ctx)
	}
	set := netdetect.EndpointSet{Sources: map[netip.Addr]string{}}
	for _, a := range o.Endpoints {
		set.Addrs = append(set.Addrs, a)
		set.Sources[a] = "literal"
	}
	return set
}

// resolveEndpointsWith resolves the endpoint set using the given live tunnel set
// for the tunnel-internal drop filter. Prefers ResolveEndpointsWith (wired by
// main), falls back to ResolveEndpoints, then the static list.
func (o Options) resolveEndpointsWith(ctx context.Context, tunnels []string) netdetect.EndpointSet {
	if o.ResolveEndpointsWith != nil {
		return o.ResolveEndpointsWith(ctx, tunnels)
	}
	return o.resolveEndpoints(ctx)
}

// clampWindow parses a requested switch-window duration and caps it at
// SwitchWindowMax (no floor). An empty/invalid request falls back to the
// configured default SwitchWindow.
func (o Options) clampWindow(req string) time.Duration {
	// Refuse outright when manual switch windows are disabled. Both triggers
	// (socket op, command file) are already gated on switchEnabled, so this is
	// unreachable today — but failing closed here means a future caller that
	// skipped the gate can never turn "disabled" into a real relaxation of the
	// guard, whatever value req parses to.
	if o.SwitchWindow <= 0 {
		return 0
	}
	dur := o.SwitchWindow
	if req != "" {
		if d, err := time.ParseDuration(req); err == nil && d > 0 {
			dur = d
		}
	}
	// No floor (2026-07-22 defaults review): any positive request is honored,
	// capped only by SwitchWindowMax.
	max := o.SwitchWindowMax
	if max <= 0 {
		max = 3 * time.Minute
	}
	if dur > max {
		dur = max
	}
	return dur
}

// defaultPauseDuration is the fallback length of a pause request that names no
// duration. Unlike the switch window, pausing has no separate config default —
// vpn.pauseMax is the cap and the disable-gate; the requested duration always
// comes from the caller (CLI flag / GUI preset).
const defaultPauseDuration = 15 * time.Minute

// clampPause parses a requested pause duration and caps it at PauseMax (no
// floor). An empty/invalid request falls back to defaultPauseDuration. Returns
// 0 when pausing is disabled (PauseMax <= 0) — callers (control-socket ops,
// command-poll cases) are already gated on pauseEnabled, but failing closed
// here means a future caller that skipped the gate can never turn "disabled"
// into a real relaxation of the guard, whatever value req parses to.
func (o Options) clampPause(req string) time.Duration {
	if o.PauseMax <= 0 {
		return 0
	}
	dur := defaultPauseDuration
	if req != "" {
		if d, err := time.ParseDuration(req); err == nil && d > 0 {
			dur = d
		}
	}
	if dur > o.PauseMax {
		dur = o.PauseMax
	}
	return dur
}

// blockedContains reports whether cc (case-insensitive) is in the blocked list.
func blockedContains(blocked []string, cc string) bool {
	for _, b := range blocked {
		if strings.EqualFold(b, cc) {
			return true
		}
	}
	return false
}

// discoveredAddrs returns the addresses in set whose source is live discovery —
// the ones worth persisting as learned endpoints after a verified switch.
func discoveredAddrs(set netdetect.EndpointSet) []netip.Addr {
	var out []netip.Addr
	for _, a := range set.Addrs {
		if set.Sources[a] == "discovered" {
			out = append(out, a)
		}
	}
	return out
}

// firstOr returns the first element of s, or "" if empty.
func firstOr(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// reconcileEndpoints decides the endpoint set to use after a refresh, enforcing
// the safety invariant: never apply an empty set; while blocked only grow the set
// (a removed endpoint might be the one needed to recover); and while guarding
// never let a loss-only refresh drop an endpoint the tunnel still needs. Returns
// the set and whether it changed.
// reconcileWithGrace wraps reconcileEndpoints with bounded retention: every
// fresh address is stamped as seen now, and current addresses MISSING from
// fresh ride along as if still fresh while they are within grace of their last
// sighting. Rationale: autodiscovered endpoints are only observable while their
// socket lives, and the socket dies with the tunnel — pruning them at the next
// refresh would wall off exactly the reconnect the guard keeps endpoints open
// for. A genuinely rotated-away server ages out once unseen past the grace.
// Entries neither current nor fresh are dropped from lastSeen so it can't grow
// without bound. growOnly (an active block or switch window) is unchanged —
// retention there is unconditional, as before.
func reconcileWithGrace(current []netip.Addr, fresh netdetect.EndpointSet, growOnly bool,
	lastSeen map[netip.Addr]time.Time, now time.Time, grace time.Duration) ([]netip.Addr, bool) {
	for _, a := range fresh.Addrs {
		lastSeen[a] = now
	}
	keep := make(map[netip.Addr]bool, len(current)+len(fresh.Addrs))
	for _, a := range current {
		keep[a] = true
	}
	augmented := fresh
	if !growOnly {
		for _, a := range current {
			if t, ok := lastSeen[a]; ok && now.Sub(t) <= grace {
				augmented.Addrs = unionAddrs(augmented.Addrs, []netip.Addr{a})
			}
		}
	}
	for _, a := range augmented.Addrs {
		keep[a] = true
	}
	for a := range lastSeen {
		if !keep[a] {
			delete(lastSeen, a)
		}
	}
	return reconcileEndpoints(current, augmented, growOnly)
}

func reconcileEndpoints(current []netip.Addr, fresh netdetect.EndpointSet, blocked bool) ([]netip.Addr, bool) {
	if len(fresh.Addrs) == 0 {
		return current, false // never narrow to empty
	}
	if !blocked {
		// A guard-time refresh that brings nothing new is either identical (no-op)
		// or a loss-only shrink — the signature of a transient DNS/discovery flake.
		// Keep the current set: dropping a still-needed server endpoint here and
		// then taking a geo BLOCK would restore a guard that can't reconnect. A
		// genuine rotation surfaces a new address, so it still replaces.
		if sameAddrs(current, unionAddrs(current, fresh.Addrs)) {
			return current, false
		}
		return fresh.Addrs, true
	}
	merged := unionAddrs(current, fresh.Addrs)
	if sameAddrs(current, merged) {
		return current, false
	}
	return merged, true
}

func sameAddrs(a, b []netip.Addr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// unionAddrs returns current plus any address in extra not already present,
// preserving current's order then appending newcomers in their given order.
func unionAddrs(current, extra []netip.Addr) []netip.Addr {
	seen := make(map[netip.Addr]bool, len(current))
	for _, a := range current {
		seen[a] = true
	}
	out := append([]netip.Addr(nil), current...)
	for _, a := range extra {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}
