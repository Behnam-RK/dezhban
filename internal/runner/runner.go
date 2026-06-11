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

	"github.com/behnam-rk/dezhban/internal/decision"
	"github.com/behnam-rk/dezhban/internal/firewall"
	"github.com/behnam-rk/dezhban/internal/monitor"
)

// Poller is the monitor surface the runner consumes. *monitor.Monitor satisfies
// it; an interface keeps the loop testable without real HTTP.
type Poller interface {
	Poll(ctx context.Context) <-chan monitor.Result
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
	Monitor Poller
	Decider *decision.Decider
	Backend Backend
	Log     *slog.Logger

	// VPN selects the interface-guard state machine (GUARD ↔ FULL BLOCK) instead
	// of the legacy dst-IP Block/Unblock model.
	VPN bool
	// Tunnels and Endpoints describe the VPN guard (VPN mode only).
	Tunnels   []string
	Endpoints []netip.Addr
	// Allowlist (re)builds the legacy dst-IP allowlist; it is called before each
	// (re)Block so rotated provider IPs are picked up. Legacy mode only.
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
// verdict. In-process state tracks the applied posture so transitions log once
// and redundant Applies are skipped (the backend is idempotent regardless).
func (o Options) runVPN(ctx context.Context) error {
	guard := firewall.Policy{
		Mode:         firewall.ModeGuard,
		TunnelIfaces: o.Tunnels,
		VPNEndpoints: o.Endpoints,
	}
	if err := o.Backend.Apply(guard); err != nil {
		return fmt.Errorf("install startup guard: %w", err)
	}
	o.Log.Info("vpn guard active (startup)", "tunnels", o.Tunnels, "endpoints", len(o.Endpoints))

	const (
		stGuard     = "guard"
		stFullBlock = "fullblock"
	)
	current := stGuard

	for res := range o.Monitor.Poll(ctx) {
		if res.Err != nil {
			o.Log.Warn("country lookup failed", "err", res.Err)
		}
		cc := res.Reading.CountryCode
		switch o.Decider.Evaluate(res) {
		case decision.Block:
			if current == stFullBlock {
				continue
			}
			// FULL BLOCK under a tunnel cuts the tunnel too: the dst-IP allowlist is
			// meaningless on encrypted outer packets, so it is omitted. Recovery
			// probing out of this state lands in Phase 4.
			fb := firewall.Policy{
				Mode:         firewall.ModeFullBlock,
				TunnelIfaces: o.Tunnels,
				VPNEndpoints: o.Endpoints,
			}
			if err := o.Backend.Apply(fb); err != nil {
				o.Log.Error("full block failed", "err", err, "country", cc)
				continue
			}
			o.Log.Info("FULL BLOCK", "country", cc)
			current = stFullBlock
		case decision.Allow:
			if current == stGuard {
				continue
			}
			if err := o.Backend.Apply(guard); err != nil {
				o.Log.Error("guard restore failed", "err", err, "country", cc)
				continue
			}
			o.Log.Info("GUARD", "country", cc)
			current = stGuard
		}
	}
	return nil
}

// runLegacy is the direct-connection model: dst-IP allowlist Block on entering a
// blocked country, Unblock on leaving. The allowlist is re-resolved at each
// Block so rotated provider IPs stay reachable for recovery detection.
func (o Options) runLegacy(ctx context.Context) error {
	blocked := false
	for res := range o.Monitor.Poll(ctx) {
		if res.Err != nil {
			o.Log.Warn("country lookup failed", "err", res.Err)
		}
		cc := res.Reading.CountryCode
		switch o.Decider.Evaluate(res) {
		case decision.Block:
			if blocked {
				continue
			}
			al := o.Allowlist()
			if err := o.Backend.Block(al); err != nil {
				o.Log.Error("block failed", "err", err, "country", cc)
				continue
			}
			o.Log.Info("BLOCKING", "country", cc, "dns_allowed", len(al.DNS), "hosts_allowed", len(al.Hosts))
			blocked = true
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
