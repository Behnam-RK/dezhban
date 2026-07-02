//go:build windows

package firewall

import (
	"net/netip"
	"strings"
	"testing"
)

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return a
}

// assertDefaultDenyLast checks the surgical invariant: the outbound default is
// flipped to Block as the final step, after the allow rules are in place.
func assertDefaultDenyLast(t *testing.T, script string) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(script), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if !strings.Contains(last, "DefaultOutboundAction Block") {
		t.Errorf("last line = %q, want DefaultOutboundAction Block last\n--- got ---\n%s", last, script)
	}
}

func TestRenderBlockScriptLegacyFullBlock(t *testing.T) {
	p := Policy{
		Mode: ModeFullBlock,
		Allowlist: Allowlist{
			DNS:   []netip.Addr{mustAddr(t, "1.1.1.1"), mustAddr(t, "8.8.8.8")},
			Hosts: []netip.Addr{mustAddr(t, "34.117.59.81")},
		},
	}
	s := renderBlockScript(p)

	wantContains := []string{
		"Remove-NetFirewallRule -Group dezhban",
		"-RemoteAddress 127.0.0.1,::1",
		"-Protocol UDP -RemotePort 53 -RemoteAddress 1.1.1.1,8.8.8.8",
		"-Protocol TCP -RemotePort 53 -RemoteAddress 1.1.1.1,8.8.8.8",
		"-RemoteAddress 34.117.59.81",
	}
	for _, w := range wantContains {
		if !strings.Contains(s, w) {
			t.Errorf("script missing %q\n--- got ---\n%s", w, s)
		}
	}
	assertDefaultDenyLast(t, s)
}

func TestRenderBlockScriptEmptyAllowlist(t *testing.T) {
	s := renderBlockScript(Policy{Mode: ModeFullBlock})
	// No DNS/hosts → only loopback allow, then the Block default.
	if strings.Contains(s, "RemotePort 53") {
		t.Errorf("empty allowlist should emit no DNS rule:\n%s", s)
	}
	if strings.Contains(s, "-RemoteAddress \n") || strings.Contains(s, "-RemoteAddress |") {
		t.Errorf("empty allowlist produced a rule with no address:\n%s", s)
	}
	assertDefaultDenyLast(t, s)
}

func TestRenderBlockScriptGuard(t *testing.T) {
	p := Policy{
		Mode:         ModeGuard,
		TunnelIfaces: []string{"WireGuard tunnel", "utun4"},
		VPNEndpoints: []netip.Addr{mustAddr(t, "203.0.113.5")},
		Allowlist:    Allowlist{DNS: []netip.Addr{mustAddr(t, "1.1.1.1")}},
	}
	s := renderBlockScript(p)

	wantContains := []string{
		"-InterfaceAlias 'WireGuard tunnel','utun4'",
		"-RemoteAddress 203.0.113.5",
	}
	for _, w := range wantContains {
		if !strings.Contains(s, w) {
			t.Errorf("guard script missing %q\n--- got ---\n%s", w, s)
		}
	}
	// The dst-IP allowlist must NOT leak into guard rules.
	if strings.Contains(s, "RemotePort 53") || strings.Contains(s, "1.1.1.1") {
		t.Errorf("guard script must not emit the dst-IP allowlist:\n%s", s)
	}
	assertDefaultDenyLast(t, s)
}

func TestRenderBlockScriptVPNFullBlockCutsTunnelKeepsEndpoints(t *testing.T) {
	p := Policy{
		Mode:         ModeFullBlock,
		TunnelIfaces: []string{"utun4"},
		VPNEndpoints: []netip.Addr{mustAddr(t, "203.0.113.5")},
		Allowlist:    Allowlist{Hosts: []netip.Addr{mustAddr(t, "34.117.59.81")}},
	}
	s := renderBlockScript(p)

	// The endpoint allow stays open so the tunnel can reconnect.
	if !strings.Contains(s, "203.0.113.5") {
		t.Errorf("VPN full block must keep the endpoint allow (reconnect path):\n%s", s)
	}
	// No tunnel-interface allow: the iface name appears only in that rule's alias.
	if strings.Contains(s, "utun4") {
		t.Errorf("VPN full block must NOT allow the tunnel interface (user egress cut):\n%s", s)
	}
	// The dst-IP allowlist host is still omitted under a tunnel.
	if strings.Contains(s, "34.117.59.81") {
		t.Errorf("VPN full block must not emit the dst-IP allowlist host:\n%s", s)
	}
	if !strings.Contains(s, "-RemoteAddress 127.0.0.1,::1") {
		t.Errorf("loopback must still be allowed:\n%s", s)
	}
	assertDefaultDenyLast(t, s)
}

func TestApplyWfpGuardRequiresTunnelIface(t *testing.T) {
	// The guard check runs before any state I/O or powershell call.
	if err := (&wfpBackend{}).Apply(Policy{Mode: ModeGuard}); err == nil {
		t.Fatal("Apply(guard, no tunnel ifaces) = nil, want error (would be a total lockout)")
	}
}
