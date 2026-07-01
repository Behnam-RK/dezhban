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
	"strings"
	"time"

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
	// ResolveEndpoints recomputes the VPN endpoint set (literals + resolved
	// hostnames + live discovery). Called once at startup and on each
	// EndpointRefresh tick. nil → fall back to the static Endpoints above.
	ResolveEndpoints func(ctx context.Context) netdetect.EndpointSet
	// EndpointRefresh is how often ResolveEndpoints is re-run (VPN mode). <=0 → 5m.
	EndpointRefresh time.Duration
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

// postureName maps (mode, blocked) to the snapshot's posture string.
func postureName(vpn, blocked bool) string {
	switch {
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
func (o Options) publish(blocked bool, r monitor.Reading, lookupErr error, tunnels []state.Tunnel, endpoints []netip.Addr) {
	if o.Publish == nil {
		return
	}
	snap := state.Snapshot{
		Time:             time.Now(),
		Mode:             modeName(o.VPN),
		Posture:          postureName(o.VPN, blocked),
		Blocked:          blocked,
		CountryCode:      r.CountryCode,
		Provider:         r.Provider,
		BlockedCountries: o.BlockedCountries,
		Tunnels:          tunnels,
		PID:              os.Getpid(),
	}
	if r.IP.IsValid() {
		snap.IP = r.IP.String()
	}
	if lookupErr != nil {
		snap.LookupErr = lookupErr.Error()
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
	// event. The buffer absorbs any single slow write; only a sustained stall that
	// fills it reintroduces back-pressure — far better than blocking on every write.
	// Snapshots are delivered in order (no coalescing), so every posture transition
	// is published. Nil Publish stays a plain no-op.
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
		o.Publish = func(s state.Snapshot) { ch <- s }
		defer func() {
			close(ch)
			<-done // flush queued snapshots before returning
		}()
	}

	if o.VPN {
		return o.runVPN(ctx)
	}
	return o.runLegacy(ctx)
}

// runVPN installs the always-on guard immediately at startup (so a tunnel drop
// is cut even before the first poll), then toggles GUARD ↔ FULL BLOCK on each
// verdict. While in FULL BLOCK the tunnel is cut and the exit country cannot be
// observed, so recovery uses a time-windowed probe (see probe): each tick the
// guard is briefly lifted for a single lookup, then re-cut. Probe readings feed
// the same hysteresis streak in the Decider, so one allowed reading does not
// lift the block — it takes `Hysteresis` consecutive allowed probes.
func (o Options) runVPN(ctx context.Context) error {
	// Resolve the initial endpoint set (literals + hostnames + live discovery).
	set := o.resolveEndpoints(ctx)
	if len(set.Addrs) == 0 {
		return errors.New("refusing to start: no usable vpn endpoints — set vpn.endpoints (IP or hostname) " +
			"or enable vpn.autoDiscoverEndpoints with the VPN connected; a guard with no way to reach the " +
			"server can never let the tunnel reconnect")
	}
	endpoints := set.Addrs

	// A VPN endpoint must be reachable on the PHYSICAL interface. An endpoint that
	// is itself a tunnel-internal address can't be — the physical-side pass-to rule
	// is futile, and cutting the tunnel cuts the only path to the endpoint, so it
	// can never reconnect and the host locks itself out. Refuse to start BEFORE
	// touching the firewall rather than discover it at the next FULL BLOCK: a warn
	// scrolls past in a service log, but a config that cannot recover must not run.
	// (run `dezhban doctor` for the full picture, including a stale-but-public
	// endpoint this check can't see.) A read error — can't classify — is non-fatal.
	if bad, err := netdetect.CheckEndpointRouting(endpoints, o.Tunnels); err != nil {
		o.Log.Debug("could not check endpoint routing", "err", err)
	} else if len(bad) > 0 {
		for _, br := range bad {
			o.Log.Error("vpn endpoint is inside the tunnel's own subnet — guaranteed lockout if blocked; "+
				"set vpn.endpoints to the server IP reachable on the physical interface",
				"endpoint", br.Endpoint, "subnet", br.Subnet, "iface", br.Iface)
		}
		return fmt.Errorf("refusing to start: %d vpn endpoint(s) are tunnel-internal and would lock the host out on full block (run `dezhban doctor` to fix)", len(bad))
	}

	guard, fullBlock := o.vpnPolicies(endpoints)
	if err := o.Backend.Apply(guard); err != nil {
		return fmt.Errorf("install startup guard: %w", err)
	}
	o.Log.Info("vpn guard active (startup)", "tunnels", o.Tunnels, "endpoints", len(endpoints))

	blocked := false // applied posture: false = GUARD, true = FULL BLOCK

	// Last-known observation and tunnel state, retained so a tunnel edge or
	// endpoint refresh publishes a full snapshot without blanking the IP/country
	// from the most recent poll. snapshot() closes over these plus the (mutable)
	// endpoints and blocked, so it always emits the current posture.
	var lastRes monitor.Result
	var lastTun []state.Tunnel
	snapshot := func() { o.publish(blocked, lastRes.Reading, lastRes.Err, lastTun, endpoints) }

	// Observability watcher: a tunnel drop is already cut by the standing guard
	// rule (physical egress is blocked except the endpoints), so the watcher takes
	// NO firewall action here — it just surfaces the drop/restore for logging and
	// the `monitor` view. Forcing FULL BLOCK on a drop would only cut the handshake
	// the VPN needs to reconnect and leak during recovery probes.
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

	// Observe once immediately so an already-bad exit is caught at startup rather
	// than one interval later.
	lastRes = o.vpnGeoStep(ctx, guard, fullBlock, &blocked)
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
			if st.Up {
				o.Log.Info("vpn tunnel up", "detail", st.Detail)
			} else {
				o.Log.Warn("vpn tunnel down — guard holds the line (physical egress stays blocked, "+
					"endpoints open for reconnect)", "detail", st.Detail)
			}
			lastTun = tunnelSnapshot(st, o.Tunnels)
			snapshot()
		case <-epTick.C:
			next, changed := reconcileEndpoints(endpoints, o.resolveEndpoints(ctx), blocked)
			if changed {
				endpoints = next
				guard, fullBlock = o.vpnPolicies(endpoints)
				// FULL BLOCK renders no passes, so the endpoint set only matters for
				// GUARD: re-apply now if guarding, otherwise the refreshed set is used
				// the next time the probe/verdict restores the guard.
				if !blocked {
					if err := o.Backend.Apply(guard); err != nil {
						o.Log.Error("re-apply guard after endpoint refresh failed", "err", err)
					} else {
						o.Log.Info("vpn endpoints updated", "endpoints", len(endpoints))
					}
				} else {
					o.Log.Debug("vpn endpoints updated while blocked; applied on next guard", "endpoints", len(endpoints))
				}
				snapshot()
			}
		case <-geoTick.C:
			lastRes = o.vpnGeoStep(ctx, guard, fullBlock, &blocked)
			snapshot()
		}
	}
}

// vpnPolicies builds the GUARD and FULL BLOCK policies for the given endpoint
// set. FULL BLOCK under a tunnel cuts the tunnel too: the dst-IP allowlist is
// meaningless on encrypted outer packets, so it is omitted.
func (o Options) vpnPolicies(endpoints []netip.Addr) (guard, fullBlock firewall.Policy) {
	guard = firewall.Policy{Mode: firewall.ModeGuard, TunnelIfaces: o.Tunnels, VPNEndpoints: endpoints}
	fullBlock = firewall.Policy{Mode: firewall.ModeFullBlock, TunnelIfaces: o.Tunnels, VPNEndpoints: endpoints}
	return guard, fullBlock
}

// vpnGeoStep performs one geo observation and applies the resulting transition.
// While blocked it observes through the bounded recovery probe; otherwise it
// reads directly. Probe readings feed the same hysteresis streak, so recovery
// takes `Hysteresis` consecutive allowed probes. It returns the observed result
// so the caller can publish the last-known reading.
func (o Options) vpnGeoStep(ctx context.Context, guard, fullBlock firewall.Policy, blocked *bool) monitor.Result {
	var res monitor.Result
	if *blocked {
		res = o.probe(ctx, guard, fullBlock)
	} else {
		r, err := o.Monitor.Once(ctx)
		res = monitor.Result{Reading: r, Err: err}
	}
	if res.Err != nil {
		o.Log.Warn("country lookup failed", "err", res.Err)
	}
	cc := res.Reading.CountryCode

	switch o.Decider.Evaluate(res) {
	case decision.Block:
		if !*blocked {
			if err := o.Backend.Apply(fullBlock); err != nil {
				o.Log.Error("full block failed", "err", err, "country", cc)
			} else {
				o.Log.Info("FULL BLOCK", "country", cc)
				*blocked = true
			}
		}
		// Already blocked: the probe above re-cut to FULL BLOCK, nothing to do.
	case decision.Allow:
		if *blocked {
			if err := o.Backend.Apply(guard); err != nil {
				o.Log.Error("guard restore failed", "err", err, "country", cc)
			} else {
				o.Log.Info("GUARD", "country", cc)
				*blocked = false
			}
		}
	}
	return res
}

// probe is the VPN recovery probe: briefly lift the guard so a single geo lookup
// can traverse the tunnel, then re-cut to FULL BLOCK immediately. The egress
// window is one bounded lookup (probeEgressBudget) — the accepted recovery
// semantics. A failed re-cut leaves egress open; it is logged at error and the
// next tick re-applies the block.
func (o Options) probe(ctx context.Context, guard, fullBlock firewall.Policy) monitor.Result {
	if err := o.Backend.Apply(guard); err != nil {
		// Could not open the tunnel to look — report as a lookup failure so the
		// Decider treats the country as undeterminable (fail-closed keeps blocking).
		o.Log.Error("recovery probe: lift guard failed", "err", err)
		return monitor.Result{Err: fmt.Errorf("probe lift failed: %w", err)}
	}
	// Bound the open-guard window: while the guard is lifted, egress flows to the
	// (possibly forbidden) exit. A hung tunnel or a quorum lookup over several
	// providers would otherwise keep it open for the full lookup timeout(s). Cap
	// the window — a bounded leak that retries next tick beats an open guard. If
	// the probe times out it reports an error → fail-closed keeps the block.
	pctx, cancel := context.WithTimeout(ctx, probeEgressBudget)
	r, err := o.Monitor.Once(pctx)
	cancel()
	if cerr := o.Backend.Apply(fullBlock); cerr != nil {
		o.Log.Error("recovery probe: re-cut to full block failed — egress may be open until next tick", "err", cerr)
	}
	return monitor.Result{Reading: r, Err: err}
}

// runLegacy is the direct-connection model: dst-IP allowlist Block on entering a
// blocked country, Unblock on leaving. The allowlist is re-resolved on every
// Block — including each tick while still blocked — so a provider that rotates
// its CDN IP mid-block stays reachable for recovery detection. Block is
// idempotent, so re-applying the refreshed allowlist never stacks rules.
func (o Options) runLegacy(ctx context.Context) error {
	blocked := false

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
			o.Log.Warn("allowlist refresh resolved no provider IPs; keeping existing block", "reason", reason, "country", cc)
			return
		}
		if err := o.Backend.Block(al); err != nil {
			o.Log.Error("block failed", "err", err, "reason", reason, "country", cc)
			return
		}
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
	snapshot := func() { o.publish(blocked, last.Reading, last.Err, lastTun, nil) }

	results := o.Monitor.Poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
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
				if blocked {
					if err := o.Backend.Unblock(); err != nil {
						o.Log.Error("unblock failed", "err", err, "country", cc)
					} else {
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

// reconcileEndpoints decides the endpoint set to use after a refresh, enforcing
// the safety invariant: never apply an empty set; while blocked only grow the set
// (a removed endpoint might be the one needed to recover); and while guarding
// never let a loss-only refresh drop an endpoint the tunnel still needs. Returns
// the set and whether it changed.
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
