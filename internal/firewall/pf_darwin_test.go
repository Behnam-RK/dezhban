//go:build darwin

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

// assertDefaultDenyLast checks the surgical invariant shared by every posture:
// `block drop out all` is the final rule so any unmatched egress is dropped.
func assertDefaultDenyLast(t *testing.T, rs string) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(rs), "\n")
	if last := lines[len(lines)-1]; last != "block drop out all" {
		t.Errorf("last rule = %q, want default-deny last\n--- got ---\n%s", last, rs)
	}
}

func TestRenderRulesetLegacyFullBlock(t *testing.T) {
	p := Policy{
		Mode: ModeFullBlock,
		Allowlist: Allowlist{
			DNS:   []netip.Addr{mustAddr(t, "1.1.1.1"), mustAddr(t, "8.8.8.8")},
			Hosts: []netip.Addr{mustAddr(t, "34.117.59.81")},
		},
	}
	rs := renderRuleset(p)

	wantContains := []string{
		"pass quick on lo0 all",
		"pass out quick proto { udp tcp } to { 1.1.1.1 8.8.8.8 } port 53",
		"pass out quick to { 34.117.59.81 }",
		"block drop out all",
	}
	for _, w := range wantContains {
		if !strings.Contains(rs, w) {
			t.Errorf("ruleset missing %q\n--- got ---\n%s", w, rs)
		}
	}
	assertDefaultDenyLast(t, rs)
}

func TestRenderRulesetEmptyAllowlist(t *testing.T) {
	rs := renderRuleset(Policy{Mode: ModeFullBlock})
	if strings.Contains(rs, "to {  }") || strings.Contains(rs, "to { }") {
		t.Errorf("empty allowlist produced an invalid empty address list:\n%s", rs)
	}
	if !strings.Contains(rs, "block drop out all") {
		t.Errorf("ruleset missing default-deny:\n%s", rs)
	}
	// With no DNS/hosts, neither pass-out rule should appear.
	if strings.Contains(rs, "pass out quick") {
		t.Errorf("empty allowlist should emit no egress pass rules:\n%s", rs)
	}
}

func TestRenderRulesetGuard(t *testing.T) {
	p := Policy{
		Mode:         ModeGuard,
		TunnelIfaces: []string{"utun4", "utun5"},
		VPNEndpoints: []netip.Addr{mustAddr(t, "203.0.113.5")},
		// Allowlist present but must be ignored in guard mode (dst-IP is
		// meaningless under a tunnel).
		Allowlist: Allowlist{DNS: []netip.Addr{mustAddr(t, "1.1.1.1")}},
	}
	rs := renderRuleset(p)

	wantContains := []string{
		"pass quick on lo0 all",
		"pass out quick on { utun4 utun5 } all",
		"pass out quick to { 203.0.113.5 }",
		"block drop out all",
	}
	for _, w := range wantContains {
		if !strings.Contains(rs, w) {
			t.Errorf("guard ruleset missing %q\n--- got ---\n%s", w, rs)
		}
	}
	// The dst-IP allowlist must NOT leak into guard rules.
	if strings.Contains(rs, "port 53") || strings.Contains(rs, "1.1.1.1") {
		t.Errorf("guard ruleset must not emit the dst-IP allowlist:\n%s", rs)
	}
	// Passes must be stateless so a tunnel transport connection already open at
	// block time isn't dropped by pf's default `flags S/SA keep state`.
	for _, line := range strings.Split(strings.TrimSpace(rs), "\n") {
		if strings.HasPrefix(line, "pass") && !strings.HasSuffix(line, "no state") {
			t.Errorf("guard pass rule is not stateless (drops mid-stream flows): %q", line)
		}
	}
	assertDefaultDenyLast(t, rs)
}

func TestRenderRulesetVPNFullBlockCutsTunnel(t *testing.T) {
	// VPN full block carries tunnel ifaces as a mode signal but emits no passes:
	// the tunnel is cut and the dst-IP allowlist is deliberately omitted.
	p := Policy{
		Mode:         ModeFullBlock,
		TunnelIfaces: []string{"utun4"},
		VPNEndpoints: []netip.Addr{mustAddr(t, "203.0.113.5")},
		Allowlist:    Allowlist{Hosts: []netip.Addr{mustAddr(t, "34.117.59.81")}},
	}
	rs := renderRuleset(p)

	if strings.Contains(rs, "pass out quick") {
		t.Errorf("VPN full block must emit no egress pass rules (tunnel cut):\n%s", rs)
	}
	if !strings.Contains(rs, "pass quick on lo0 all") {
		t.Errorf("loopback must still pass:\n%s", rs)
	}
	assertDefaultDenyLast(t, rs)
}
