// Package runner ties the three layers into the live daemon: it polls the
// Monitor, asks the Decider for a verdict, and drives the firewall Backend —
// always cleaning up on exit. It is platform-independent; the only OS-specific
// part is the Backend it is handed.
package runner

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/behnam-rk/dezhban/internal/decision"
	"github.com/behnam-rk/dezhban/internal/firewall"
	"github.com/behnam-rk/dezhban/internal/monitor"
)

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
	// Tunnels and Endpoints describe the VPN guard (VPN mode only).
	Tunnels   []string
	Endpoints []netip.Addr
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
	guard := firewall.Policy{
		Mode:         firewall.ModeGuard,
		TunnelIfaces: o.Tunnels,
		VPNEndpoints: o.Endpoints,
	}
	// FULL BLOCK under a tunnel cuts the tunnel too: the dst-IP allowlist is
	// meaningless on encrypted outer packets, so it is omitted.
	fullBlock := firewall.Policy{
		Mode:         firewall.ModeFullBlock,
		TunnelIfaces: o.Tunnels,
		VPNEndpoints: o.Endpoints,
	}
	if err := o.Backend.Apply(guard); err != nil {
		return fmt.Errorf("install startup guard: %w", err)
	}
	o.Log.Info("vpn guard active (startup)", "tunnels", o.Tunnels, "endpoints", len(o.Endpoints))

	blocked := false // applied posture: false = GUARD, true = FULL BLOCK
	tick := time.NewTicker(o.Interval)
	defer tick.Stop()

	for {
		var res monitor.Result
		if blocked {
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
			if !blocked {
				if err := o.Backend.Apply(fullBlock); err != nil {
					o.Log.Error("full block failed", "err", err, "country", cc)
				} else {
					o.Log.Info("FULL BLOCK", "country", cc)
					blocked = true
				}
			}
			// Already blocked: the probe above re-cut to FULL BLOCK, nothing to do.
		case decision.Allow:
			if blocked {
				if err := o.Backend.Apply(guard); err != nil {
					o.Log.Error("guard restore failed", "err", err, "country", cc)
				} else {
					o.Log.Info("GUARD", "country", cc)
					blocked = false
				}
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// probe is the VPN recovery probe: briefly lift the guard so a single geo lookup
// can traverse the tunnel, then re-cut to FULL BLOCK immediately. The egress
// window is one lookup — the accepted recovery semantics. A failed re-cut leaves
// egress open; it is logged at error and the next tick re-applies the block.
func (o Options) probe(ctx context.Context, guard, fullBlock firewall.Policy) monitor.Result {
	if err := o.Backend.Apply(guard); err != nil {
		// Could not open the tunnel to look — report as a lookup failure so the
		// Decider treats the country as undeterminable (fail-closed keeps blocking).
		o.Log.Error("recovery probe: lift guard failed", "err", err)
		return monitor.Result{Err: fmt.Errorf("probe lift failed: %w", err)}
	}
	r, err := o.Monitor.Once(ctx)
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
	for res := range o.Monitor.Poll(ctx) {
		if res.Err != nil {
			o.Log.Warn("country lookup failed", "err", res.Err)
		}
		cc := res.Reading.CountryCode
		switch o.Decider.Evaluate(res) {
		case decision.Block:
			al := o.Allowlist() // re-resolve every tick so rotated provider IPs stay open
			if err := o.Backend.Block(al); err != nil {
				o.Log.Error("block failed", "err", err, "country", cc)
				continue
			}
			if !blocked {
				o.Log.Info("BLOCKING", "country", cc, "dns_allowed", len(al.DNS), "hosts_allowed", len(al.Hosts))
				blocked = true
			} else {
				o.Log.Debug("allowlist refreshed under block", "hosts_allowed", len(al.Hosts))
			}
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
	return nil
}
