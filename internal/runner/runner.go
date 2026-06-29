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
	"time"

	"github.com/behnam-rk/dezhban/internal/decision"
	"github.com/behnam-rk/dezhban/internal/firewall"
	"github.com/behnam-rk/dezhban/internal/monitor"
	"github.com/behnam-rk/dezhban/internal/netdetect"
)

// probeEgressBudget caps how long the VPN recovery probe may hold the guard
// lifted for one observation. It is slightly above a single provider's lookup
// timeout so a normal lookup completes, while bounding the leak window if the
// tunnel hangs or quorum fans out across several providers.
const probeEgressBudget = 8 * time.Second

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
	o.vpnGeoStep(ctx, guard, fullBlock, &blocked)

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
			}
		case <-geoTick.C:
			o.vpnGeoStep(ctx, guard, fullBlock, &blocked)
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
// takes `Hysteresis` consecutive allowed probes.
func (o Options) vpnGeoStep(ctx context.Context, guard, fullBlock firewall.Policy, blocked *bool) {
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
		case res, ok := <-results:
			if !ok {
				return nil
			}
			if res.Err != nil {
				o.Log.Warn("country lookup failed", "err", res.Err)
			}
			cc := res.Reading.CountryCode
			switch o.Decider.Evaluate(res) {
			case decision.Block:
				block("country", cc)
			case decision.Allow:
				if !blocked {
					continue
				}
				if err := o.Backend.Unblock(); err != nil {
					o.Log.Error("unblock failed", "err", err, "country", cc)
					continue
				}
				o.Log.Info("ALLOWING", "country", cc)
				blocked = false
			}
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
// the safety invariant: never apply an empty set, and while blocked only grow it
// (a removed endpoint might be the one needed to recover). Returns the set and
// whether it changed.
func reconcileEndpoints(current []netip.Addr, fresh netdetect.EndpointSet, blocked bool) ([]netip.Addr, bool) {
	if len(fresh.Addrs) == 0 {
		return current, false // never narrow to empty
	}
	if !blocked {
		if sameAddrs(current, fresh.Addrs) {
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
