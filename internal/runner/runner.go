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
type Backend interface {
	Apply(p firewall.Policy) error
	Block(a firewall.Allowlist) error
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

	// VPN selects the interface-guard state machine (GUARD ↔ FULL BLOCK) instead
	// of the legacy dst-IP Block/Unblock model.
	VPN bool
	// Tunnels and Endpoints describe the VPN guard (VPN mode only). Endpoints is
	// the static fallback used only when ResolveEndpoints is nil (tests / legacy
	// callers); normal runs supply ResolveEndpoints.
	Tunnels   []string
	Endpoints []netip.Addr
	// AllowPhysicalDNS mirrors config vpn.allowPhysicalDNS onto the guard and
	// VPN full-block policies (opt-in plain-DNS pass for hostname re-resolution
	// while the tunnel is down).
	AllowPhysicalDNS bool
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
	// SwitchWindow is the default duration of a switch window; SwitchWindowMax
	// caps it. WindowProtos/WindowPorts optionally restrict the window. A switch
	// window is only available when SwitchWindow > 0 AND PollCommand != nil.
	SwitchWindow    time.Duration
	SwitchWindowMax time.Duration
	WindowProtos    []string
	WindowPorts     []int
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
	// ResolveEndpointsWith recomputes the endpoint set using an explicit live
	// tunnel set for the tunnel-internal drop filter. nil → ResolveEndpoints /
	// static fallback (the tunnel set is then ignored).
	ResolveEndpointsWith func(ctx context.Context, tunnels []string) netdetect.EndpointSet
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
	// Watcher, when non-nil, emits tunnel up/down edges. In VPN mode it drives
	// observability logging only (the standing guard rule already cuts a drop with
	// no leak). In legacy mode a down edge triggers an immediate block — a kill
	// switch needing only a tunnel name, no endpoints.
	Watcher *netdetect.Watcher
	// Allowlist (re)builds the legacy dst-IP allowlist. It is called before every
	// Block — including each tick while blocked — so rotated provider IPs stay
	// reachable for recovery detection. Legacy mode only.
	Allowlist func() firewall.Allowlist

	// Publish, when non-nil, receives a fresh snapshot of the daemon's posture
	// after each poll, verdict transition, tunnel edge, and endpoint refresh. It
	// is best-effort observability (the menubar app / `status --json` read it) and
	// must never affect enforcement — the wiring in main logs write failures at
	// debug. Run invokes it from a background writer goroutine (see publishBuffer),
	// so a slow write never stalls the enforcement loop; it may run concurrently
	// with the loop and need not be fast. nil → no-op (tests / legacy callers).
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

// modeName is the snapshot's mode string.
func modeName(vpn bool) string {
	if vpn {
		return "vpn"
	}
	return "legacy"
}

// postureName maps (mode, blocked, window) to the snapshot's posture string.
func postureName(vpn, blocked, window bool) string {
	switch {
	case vpn && window:
		return "switch-window"
	case vpn && blocked:
		return "full-block"
	case vpn:
		return "guard"
	case blocked:
		return "block"
	default:
		return "allow"
	}
}

// publish builds a full snapshot from the current posture and last-known reading
// and hands it to o.Publish. It no-ops when Publish is nil, so the hot path pays
// only a nil check when observability is off. Each call emits a complete snapshot
// (the file is replaced atomically), so callers pass the last-known reading even
// on tunnel/endpoint events to avoid blanking IP/country between polls.
func (o Options) publish(blocked bool, r monitor.Reading, lookupErr error, enfErr error, tunnels []state.Tunnel, endpoints []netip.Addr, win *state.SwitchState, profile string) {
	if o.Publish == nil {
		return
	}
	windowOpen := win != nil && win.Open
	snap := state.Snapshot{
		Time:                time.Now(),
		Mode:                modeName(o.VPN),
		Posture:             postureName(o.VPN, blocked, windowOpen),
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
	if lookupErr != nil {
		snap.LookupErr = lookupErr.Error()
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

	var runErr error
	if o.VPN {
		runErr = o.runVPN(ctx)
	} else {
		runErr = o.runLegacy(ctx)
	}
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
		Mode:                modeName(o.VPN),
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

// runVPN installs the always-on guard immediately at startup (so a tunnel drop
// is cut even before the first poll), then toggles GUARD ↔ FULL BLOCK on each
// verdict. While in FULL BLOCK the tunnel is cut and the exit country cannot be
// observed, so recovery uses a time-windowed probe (see probe): each tick the
// guard is briefly lifted for a single lookup, then re-cut. Probe readings feed
// the same hysteresis streak in the Decider, so one allowed reading does not
// lift the block — it takes `Hysteresis` consecutive allowed probes.
func (o Options) runVPN(ctx context.Context) error {
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
	if len(endpoints) == 0 && !relaxed {
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
	if len(tunnels) > 0 && len(endpoints) == 0 {
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

	guard, fullBlock := o.vpnPolicies(tunnels, endpoints)
	if err := o.Backend.Apply(guard); err != nil {
		return fmt.Errorf("install startup guard: %w", err)
	}
	// guard is the standing posture: usually ModeGuard, but the zero-tunnel
	// standing posture is a ModeFullBlock shape — log the actual applied mode
	// rather than claiming "guard" unconditionally.
	o.Log.Info("vpn posture active (startup)", "mode", guard.Mode, "tunnels", tunnels, "endpoints", len(endpoints), "switch", switchEnabled)

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
		activeProfile     string // last profile a switch window verified onto; sticky
		windowTimer       *time.Timer
		windowTimerC      <-chan time.Time
		winDiscTick       *time.Ticker
		winDiscC          <-chan time.Time
	)

	tunnelUp := true
	var lastRes monitor.Result
	var lastTun []state.Tunnel
	var enfErr error

	winInterval := o.WindowDiscoveryInterval
	if winInterval <= 0 {
		winInterval = 2 * time.Second
	}

	switchState := func() *state.SwitchState {
		if !windowActive {
			return nil
		}
		return &state.SwitchState{Open: true, Until: windowDeadline, Profile: windowProfile}
	}
	snapshot := func() {
		o.publish(blocked, lastRes.Reading, lastRes.Err, enfErr, lastTun, endpoints, switchState(), activeProfile)
	}
	rebuild := func() { guard, fullBlock = o.vpnPolicies(tunnels, endpoints) }

	// reapplyStanding re-applies the guard after a tunnel/endpoint change, unless a
	// window owns the rules or we are in FULL BLOCK (which renders no tunnel pass —
	// the new set lands on the next guard restore).
	reapplyStanding := func(reason string) {
		rebuild()
		if windowActive || blocked {
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

	// windowMax is the absolute cap on real-IP exposure. It anchors to the FIRST
	// open (windowStart), so repeated "open" commands can never extend a single
	// window past it — each command's duration is clamped, but without this anchor
	// their deadlines would stack.
	windowMax := o.SwitchWindowMax
	if windowMax <= 0 {
		windowMax = 5 * time.Minute
	}

	openWindow := func(now time.Time, dur time.Duration, profile string) {
		if windowActive {
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
			o.Log.Info("switch window extended", "until", windowDeadline, "profile", windowProfile)
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
		o.Log.Warn("SWITCH WINDOW OPEN — "+relaxation+"; connect your VPN now (real IP may be exposed until it closes)",
			"until", windowDeadline, "profile", profile)
		snapshot()
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
			o.Log.Error("restore posture after switch window failed — holding window open, will retry",
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
		o.Log.Info("switch window closed", "reason", reason, "posture", postureName(true, blocked, false))
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
		o.Log.Info("switch window closed early (exit verified)", "country", r.CountryCode, "tunnels", tunnels)
		snapshot()
	}

	// handleControl services one control-socket request. It runs INLINE on the run
	// loop (as a select case), which is what lets it call Backend.Apply at all —
	// the accept goroutine never does. Every path returns a Response; the server is
	// waiting on it under a timeout.
	handleControl := func(req control.Request) control.Response {
		reply := func(ok bool, msg string) control.Response {
			return control.Response{
				OK:      ok,
				Error:   msg,
				Mode:    modeName(true),
				Posture: postureName(true, blocked, windowActive),
				Blocked: blocked,
			}
		}
		switch req.Op {
		case control.OpStatus:
			return reply(true, "")

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
			enfErr = nil
			o.Log.Warn("FULL BLOCK (manual, via control socket) — held until unblock")
			snapshot()
			return reply(true, "")

		case control.OpUnblock:
			if windowActive {
				return reply(false, "switch window is open — cancel it first")
			}
			manualBlock = false
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
			if !o.AllowSwitchOps {
				return reply(false, "switch ops over the control socket are disabled (control.allowSwitchOps)")
			}
			if !switchEnabled {
				return reply(false, "switch window unavailable (vpn.switchWindow not configured)")
			}
			openWindow(time.Now(), o.clampWindow(req.Duration), req.Profile)
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
			closeWindowRevert("cancelled (control socket)")
			if windowActive {
				return reply(false, "cancel failed — window held open, revert is being retried")
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
		epInterval = 5 * time.Minute
	}
	epTick := time.NewTicker(epInterval)
	defer epTick.Stop()
	geoTick := time.NewTicker(o.Interval)
	defer geoTick.Stop()

	var cmdC <-chan time.Time
	if switchEnabled {
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
		lastRes, enfErr = o.vpnGeoStep(ctx, guard, fullBlock, &blocked)
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
			tunnelUp = st.Up
			if st.Up {
				o.Log.Info("vpn tunnel up", "detail", st.Detail)
			} else {
				o.Log.Warn("vpn tunnel down — guard holds the line (physical egress stays blocked, "+
					"endpoints open for reconnect)", "detail", st.Detail)
			}
			if next, changed := reconcileTunnels(tunnels, st.Names, pinned); changed {
				tunnels = next
				reapplyStanding("tunnel set changed")
				reapplyWindow("tunnel set changed")
			}
			lastTun = tunnelSnapshot(st, tunnels)
			if windowActive {
				maybeStartCloseProbe()
			}
			snapshot()
		case <-cmdC:
			cmd, ok := o.PollCommand()
			if !ok {
				continue
			}
			now := time.Now()
			switch cmd.Op {
			case command.OpOpenSwitchWindow:
				dur := o.clampWindow(cmd.Duration)
				openWindow(now, dur, cmd.Profile)
			case command.OpCancelSwitchWindow:
				if windowActive {
					closeWindowRevert("cancelled")
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
		case <-geoTick.C:
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
			lastRes, enfErr = o.vpnGeoStep(ctx, guard, fullBlock, &blocked)
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
func (o Options) vpnPolicies(tunnels []string, endpoints []netip.Addr) (guard, fullBlock firewall.Policy) {
	fullBlock = firewall.Policy{Mode: firewall.ModeFullBlock, TunnelIfaces: tunnels, VPNEndpoints: endpoints, AllowPhysicalDNS: o.AllowPhysicalDNS}
	if len(tunnels) == 0 && len(o.TunnelGroups) == 0 {
		// Zero-tunnel standing posture: reuse the FULL BLOCK shape (endpoints open,
		// everything else cut). Applying ModeGuard with no ifaces would be rejected
		// at the backend seam (a total-lockout guard).
		guard = fullBlock
		return guard, fullBlock
	}
	guard = firewall.Policy{Mode: firewall.ModeGuard, TunnelIfaces: tunnels, TunnelGroups: o.TunnelGroups, VPNEndpoints: endpoints, AllowPhysicalDNS: o.AllowPhysicalDNS}
	return guard, fullBlock
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
	return firewall.Policy{
		Mode:         firewall.ModeSwitchWindow,
		TunnelIfaces: tunnels,
		TunnelGroups: o.TunnelGroups,
		VPNEndpoints: endpoints,
		WindowProtos: o.WindowProtos,
		WindowPorts:  o.WindowPorts,
	}
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
func (o Options) vpnGeoStep(ctx context.Context, guard, fullBlock firewall.Policy, blocked *bool) (monitor.Result, error) {
	var res monitor.Result
	var enfErr error
	if *blocked {
		res, enfErr = o.probe(ctx, guard, fullBlock)
	} else {
		r, err := o.Monitor.Once(ctx)
		res = monitor.Result{Reading: r, Err: err}
	}
	if res.Err != nil {
		o.Log.Warn("country lookup failed", "err", res.Err)
		// Undeterminable country: hold the current posture. The standing guard
		// already blocks physical leaks, so an unknown must not escalate
		// GUARD→FULL BLOCK (which cuts tunnel egress and livelocks the
		// reconnect) nor lift an active FULL BLOCK on a blip. Only a
		// *successful* reading moves the state machine. (This makes failClosed
		// a no-op in VPN guard mode — the guard itself is the fail-closed
		// block for physical leaks.)
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

// probe is the VPN recovery probe: briefly lift the guard so a single geo lookup
// can traverse the tunnel, then re-cut to FULL BLOCK immediately. The egress
// window is one bounded lookup (probeEgressBudget) — the accepted recovery
// semantics. A failed re-cut leaves egress open; it is logged at error and the
// next tick re-applies the block.
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

// runLegacy is the direct-connection model: dst-IP allowlist Block on entering a
// blocked country, Unblock on leaving. The allowlist is re-resolved on every
// Block — including each tick while still blocked — so a provider that rotates
// its CDN IP mid-block stays reachable for recovery detection. Block is
// idempotent, so re-applying the refreshed allowlist never stacks rules.
func (o Options) runLegacy(ctx context.Context) error {
	blocked := false
	// enfErr is the last firewall-action failure, published so observers don't read a
	// failed block as a healthy "allow". Sticky: set on a Block/Unblock error, cleared
	// on the next success, and carried through non-enforcing (tunnel/geo) snapshots.
	var enfErr error

	// block applies (or refreshes) the dst-IP allowlist block. The allowlist is
	// re-resolved on every call so a provider that rotates its CDN IP mid-block
	// stays reachable. A mid-block refresh that resolves no provider IPs (transient
	// DNS failure) must NOT narrow an existing block — that would strand the monitor
	// with no geo-API egress and make the block permanent; keep the rules in force
	// and retry next tick. On first entry we still block even with an empty list
	// (buildAllowlist warns separately).
	block := func(reason, cc string) {
		al := o.Allowlist()
		if blocked && len(al.Hosts) == 0 {
			// Deliberate safety no-op (keep the existing block), not a failure — leave
			// enfErr untouched.
			o.Log.Warn("allowlist refresh resolved no provider IPs; keeping existing block", "reason", reason, "country", cc)
			return
		}
		if err := o.Backend.Block(al); err != nil {
			o.Log.Error("block failed", "err", err, "reason", reason, "country", cc)
			enfErr = err
			return
		}
		enfErr = nil
		if !blocked {
			o.Log.Info("BLOCKING", "reason", reason, "country", cc, "dns_allowed", len(al.DNS), "hosts_allowed", len(al.Hosts))
			blocked = true
		} else {
			o.Log.Debug("allowlist refreshed under block", "hosts_allowed", len(al.Hosts))
		}
	}

	// Tunnel watcher: a drop cuts the network at once (kill switch) instead of
	// leaking until the next geo poll detects the reverted country. A tunnel coming
	// back up does NOT auto-unblock — recovery stays governed by the geo verdict's
	// hysteresis, so a flapping tunnel can't strobe the network open.
	var tunCh <-chan netdetect.TunnelState
	if o.Watcher != nil {
		tunCh = o.Watcher.Watch(ctx)
	}

	// Last-known reading and tunnel state, retained so a tunnel edge publishes a
	// full snapshot without blanking the IP/country from the most recent poll.
	var last monitor.Result
	var lastTun []state.Tunnel
	snapshot := func() { o.publish(blocked, last.Reading, last.Err, enfErr, lastTun, nil, nil, "") }

	// manualBlock holds an operator-requested block across allowed verdicts, so the
	// geo poll cannot unblock what an operator explicitly blocked. Cleared only by
	// an explicit unblock.
	manualBlock := false

	// handleControl services a control-socket request inline on this loop — the
	// same reason as in runVPN: the run-loop goroutine is the only one allowed to
	// drive the Backend. Switch ops are VPN-mode only and are refused here.
	handleControl := func(req control.Request) control.Response {
		reply := func(ok bool, msg string) control.Response {
			return control.Response{
				OK:      ok,
				Error:   msg,
				Mode:    modeName(false),
				Posture: postureName(false, blocked, false),
				Blocked: blocked,
			}
		}
		switch req.Op {
		case control.OpStatus:
			return reply(true, "")
		case control.OpBlock:
			if blocked {
				manualBlock = true
				return reply(true, "")
			}
			block("manual (control socket)", last.Reading.CountryCode)
			if !blocked {
				return reply(false, "block failed")
			}
			manualBlock = true
			return reply(true, "")
		case control.OpUnblock:
			manualBlock = false
			if !blocked {
				return reply(true, "")
			}
			if err := o.Backend.Unblock(); err != nil {
				enfErr = err
				o.Log.Error("control: unblock failed", "err", err)
				snapshot()
				return reply(false, "unblock failed: "+err.Error())
			}
			blocked = false
			enfErr = nil
			o.Log.Info("ALLOWING (manual unblock, via control socket)")
			snapshot()
			return reply(true, "")
		case control.OpOpenSwitch, control.OpCancelSwitch:
			return reply(false, "switch windows are a vpn-mode feature (vpn.enabled is false)")
		}
		return reply(false, fmt.Sprintf("unsupported op %q", req.Op))
	}

	var ctlC <-chan control.ConnRequest
	if o.Control != nil {
		ctlC = o.Control.Requests()
	}

	results := o.Monitor.Poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case cr := <-ctlC:
			cr.Reply <- handleControl(cr.Req)
		case st, ok := <-tunCh:
			if !ok {
				tunCh = nil
				continue
			}
			if st.Up {
				o.Log.Info("vpn tunnel up", "detail", st.Detail)
			} else {
				o.Log.Warn("vpn tunnel down — blocking immediately (kill switch)", "detail", st.Detail)
				block("tunnel-down", "")
			}
			lastTun = tunnelSnapshot(st, o.Tunnels)
			snapshot()
		case res, ok := <-results:
			if !ok {
				return nil
			}
			if res.Err != nil {
				o.Log.Warn("country lookup failed", "err", res.Err)
			}
			last = res
			cc := res.Reading.CountryCode
			switch o.Decider.Evaluate(res) {
			case decision.Block:
				block("country", cc)
			case decision.Allow:
				// Only act when currently blocked; an allowed reading while already
				// allowing is a no-op. An unblock error leaves blocked=true so the
				// next allowed reading retries — same posture as before publishing.
				if blocked && manualBlock {
					// The operator asked for this block; an allowed country does not
					// override them. `dezhban unblock` is the only way out.
					o.Log.Debug("manual block held — not unblocking on allowed verdict", "country", cc)
				} else if blocked {
					if err := o.Backend.Unblock(); err != nil {
						o.Log.Error("unblock failed", "err", err, "country", cc)
						enfErr = err
					} else {
						enfErr = nil
						o.Log.Info("ALLOWING", "country", cc)
						blocked = false
					}
				}
			}
			snapshot()
		}
	}
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

// clampWindow parses a requested switch-window duration and clamps it to
// [minSwitchWindow, SwitchWindowMax]. An empty/invalid request falls back to the
// configured default SwitchWindow.
func (o Options) clampWindow(req string) time.Duration {
	dur := o.SwitchWindow
	if req != "" {
		if d, err := time.ParseDuration(req); err == nil && d > 0 {
			dur = d
		}
	}
	const minSwitchWindow = 10 * time.Second
	max := o.SwitchWindowMax
	if max <= 0 {
		max = 5 * time.Minute
	}
	if dur < minSwitchWindow {
		dur = minSwitchWindow
	}
	if dur > max {
		dur = max
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
