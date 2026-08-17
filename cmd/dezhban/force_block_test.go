package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/behnam-rk/dezhban/internal/config"
	"github.com/behnam-rk/dezhban/internal/firewall"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// forceCfg is a config with a pinned tunnel so resolveTunnels cannot depend on
// whatever interfaces this host happens to have.
func forceCfg(t *testing.T, allowGeo bool) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.VPN.TunnelInterfaces = []string{"utun9"}
	cfg.VPN.AllowGeoProviders = allowGeo
	return &cfg
}

// TestForceBlockHonoursAllowGeoProviders pins the security key `block --force`
// used to ignore. vpn.allowGeoProviders off means the geo-provider pass — the
// ruleset's only destination-scoped hole — is not installed, by any path.
// Accepting the setting and silently discarding it in the one command that
// bypasses the daemon is the failure this test exists to prevent.
func TestForceBlockHonoursAllowGeoProviders(t *testing.T) {
	pol, tunnels := forceBlockPolicy(forceCfg(t, false), quietLogger())

	if len(pol.ProviderAddrs) != 0 {
		t.Errorf("ProviderAddrs = %v with vpn.allowGeoProviders off, want none", pol.ProviderAddrs)
	}
	if len(pol.Allowlist.Hosts) != 0 || len(pol.Allowlist.DNS) != 0 {
		t.Errorf("Allowlist = %+v, want empty — the destination-only pass is what ADR-0006 forbids", pol.Allowlist)
	}
	if pol.Mode != firewall.ModeFullBlock {
		t.Errorf("Mode = %v, want ModeFullBlock", pol.Mode)
	}
	if len(tunnels) != 1 || tunnels[0] != "utun9" {
		t.Errorf("tunnels = %v, want [utun9]", tunnels)
	}
}

// TestForceBlockScopesTheProviderPassToTheTunnel pins ADR-0006's double scoping
// for the --force path: the pass must carry the tunnel interface so the
// renderer can bind it to one, and must never appear as a bare destination
// allowlist. A destination-only pass rides the PHYSICAL link, where the lookup
// succeeds with the tunnel down and reports the ISP's country instead of the
// exit's.
func TestForceBlockScopesTheProviderPassToTheTunnel(t *testing.T) {
	pol, _ := forceBlockPolicy(forceCfg(t, true), quietLogger())

	if len(pol.TunnelIfaces) != 1 || pol.TunnelIfaces[0] != "utun9" {
		t.Errorf("TunnelIfaces = %v, want [utun9] — an interface-scoped rule needs an interface", pol.TunnelIfaces)
	}
	if len(pol.Allowlist.Hosts) != 0 {
		t.Errorf("Allowlist.Hosts = %v, want none — providers belong in ProviderAddrs, which is tunnel-scoped", pol.Allowlist.Hosts)
	}
	// --force cuts the handshake too, unlike the daemon's FULL BLOCK.
	if len(pol.VPNEndpoints) != 0 {
		t.Errorf("VPNEndpoints = %v, want none — --force is an unconditional hard block", pol.VPNEndpoints)
	}
	if pol.AllowPhysicalDNS {
		t.Error("AllowPhysicalDNS = true, want false — --force opens no DNS")
	}
	if pol.AllowLocalNetwork {
		t.Error("AllowLocalNetwork = true, want false — --force opens no LAN")
	}
}
