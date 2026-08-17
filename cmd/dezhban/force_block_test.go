package main

import (
	"testing"

	"github.com/behnam-rk/dezhban/internal/firewall"
)

// TestForceBlockIsTotal pins that `block --force` passes NOTHING but loopback.
//
// It used to carry a geo-provider pass, justified as keeping recovery detection
// reachable. Two things were wrong with that and the second is why there is no
// pass here at all rather than a correctly-scoped one:
//
//   - The rule was destination-scoped on the PHYSICAL link, the half-scoping
//     ADR-0006 forbids, and it ignored vpn.allowGeoProviders — the key that
//     turns the ruleset's only destination-scoped hole off.
//   - Re-scoping it the ADR-0006 way cannot work here: a tunnel-scoped rule
//     needs a live tunnel, and this posture drops the endpoint the tunnel
//     handshakes to, so the tunnel dies and the rule can never match. A pass
//     that cannot carry a packet is worse than none — it makes the ruleset, the
//     log line and the docs claim a recovery path that does not exist.
//
// An operator who wants endpoints open and a working tunnel-scoped provider pass
// wants plain `block`, which blockPlan renders.
func TestForceBlockIsTotal(t *testing.T) {
	pol := forceBlockPolicy()

	if pol.Mode != firewall.ModeFullBlock {
		t.Errorf("Mode = %v, want ModeFullBlock", pol.Mode)
	}
	if len(pol.ProviderAddrs) != 0 {
		t.Errorf("ProviderAddrs = %v, want none — a tunnel-scoped pass cannot match with the endpoint cut", pol.ProviderAddrs)
	}
	if len(pol.VPNEndpoints) != 0 {
		t.Errorf("VPNEndpoints = %v, want none — --force cuts the handshake too", pol.VPNEndpoints)
	}
	if len(pol.TunnelIfaces) != 0 || len(pol.TunnelGroups) != 0 {
		t.Errorf("tunnel set = %v/%v, want empty — nothing here is scoped to a tunnel", pol.TunnelIfaces, pol.TunnelGroups)
	}
	if pol.AllowPhysicalDNS {
		t.Error("AllowPhysicalDNS = true, want false — --force opens no DNS")
	}
	if pol.AllowLocalNetwork {
		t.Error("AllowLocalNetwork = true, want false — --force opens no LAN")
	}
}
