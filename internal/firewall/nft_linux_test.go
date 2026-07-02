//go:build linux

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

// assertAtomicReplace checks the surgical idempotency invariant: the ruleset
// opens with add/delete/add so loading replaces any prior table in one
// transaction rather than stacking rules.
func assertAtomicReplace(t *testing.T, rs string) {
	t.Helper()
	want := []string{
		"add table inet dezhban",
		"delete table inet dezhban",
		"add table inet dezhban",
	}
	idx := 0
	for _, line := range strings.Split(rs, "\n") {
		if idx < len(want) && strings.TrimSpace(line) == want[idx] {
			idx++
		}
	}
	if idx != len(want) {
		t.Errorf("ruleset missing atomic add/delete/add prelude\n--- got ---\n%s", rs)
	}
}

// assertDefaultDrop checks the chain hooks output with policy drop — the nft
// equivalent of pf's trailing `block drop out all`.
func assertDefaultDrop(t *testing.T, rs string) {
	t.Helper()
	if !strings.Contains(rs, "type filter hook output priority 0; policy drop;") {
		t.Errorf("output chain missing default-drop policy\n--- got ---\n%s", rs)
	}
}

func TestRenderNftLegacyFullBlock(t *testing.T) {
	p := Policy{
		Mode: ModeFullBlock,
		Allowlist: Allowlist{
			DNS:   []netip.Addr{mustAddr(t, "1.1.1.1"), mustAddr(t, "8.8.8.8")},
			Hosts: []netip.Addr{mustAddr(t, "34.117.59.81")},
		},
	}
	rs := renderNftRuleset(p)

	wantContains := []string{
		`oifname "lo" accept`,
		"ip daddr { 1.1.1.1, 8.8.8.8 } udp dport 53 accept",
		"ip daddr { 1.1.1.1, 8.8.8.8 } tcp dport 53 accept",
		"ip daddr { 34.117.59.81 } accept",
	}
	for _, w := range wantContains {
		if !strings.Contains(rs, w) {
			t.Errorf("ruleset missing %q\n--- got ---\n%s", w, rs)
		}
	}
	assertAtomicReplace(t, rs)
	assertDefaultDrop(t, rs)
}

func TestRenderNftEmptyAllowlist(t *testing.T) {
	rs := renderNftRuleset(Policy{Mode: ModeFullBlock})
	if strings.Contains(rs, "{  }") || strings.Contains(rs, "{ }") {
		t.Errorf("empty allowlist produced an invalid empty set:\n%s", rs)
	}
	// Only loopback should be accepted with no DNS/hosts.
	for _, line := range strings.Split(rs, "\n") {
		if strings.Contains(line, "daddr") {
			t.Errorf("empty allowlist should emit no daddr accept rules: %q", line)
		}
	}
	assertDefaultDrop(t, rs)
}

func TestRenderNftV6Split(t *testing.T) {
	p := Policy{
		Mode: ModeFullBlock,
		Allowlist: Allowlist{
			DNS: []netip.Addr{mustAddr(t, "1.1.1.1"), mustAddr(t, "2606:4700:4700::1111")},
		},
	}
	rs := renderNftRuleset(p)
	if !strings.Contains(rs, "ip daddr { 1.1.1.1 } udp dport 53 accept") {
		t.Errorf("v4 DNS rule missing or merged with v6:\n%s", rs)
	}
	if !strings.Contains(rs, "ip6 daddr { 2606:4700:4700::1111 } udp dport 53 accept") {
		t.Errorf("v6 DNS rule missing or merged with v4:\n%s", rs)
	}
}

func TestRenderNftGuard(t *testing.T) {
	p := Policy{
		Mode:         ModeGuard,
		TunnelIfaces: []string{"utun4", "wg0"},
		VPNEndpoints: []netip.Addr{mustAddr(t, "203.0.113.5")},
		// Allowlist present but must be ignored in guard mode.
		Allowlist: Allowlist{DNS: []netip.Addr{mustAddr(t, "1.1.1.1")}},
	}
	rs := renderNftRuleset(p)

	wantContains := []string{
		`oifname "lo" accept`,
		`oifname { "utun4", "wg0" } accept`,
		"ip daddr { 203.0.113.5 } accept",
	}
	for _, w := range wantContains {
		if !strings.Contains(rs, w) {
			t.Errorf("guard ruleset missing %q\n--- got ---\n%s", w, rs)
		}
	}
	// The dst-IP allowlist must NOT leak into guard rules.
	if strings.Contains(rs, "dport 53") || strings.Contains(rs, "1.1.1.1") {
		t.Errorf("guard ruleset must not emit the dst-IP allowlist:\n%s", rs)
	}
	assertDefaultDrop(t, rs)
}

func TestRenderNftVPNFullBlockCutsTunnelKeepsEndpoints(t *testing.T) {
	p := Policy{
		Mode:         ModeFullBlock,
		TunnelIfaces: []string{"utun4"},
		VPNEndpoints: []netip.Addr{mustAddr(t, "203.0.113.5")},
		Allowlist:    Allowlist{Hosts: []netip.Addr{mustAddr(t, "34.117.59.81")}},
	}
	rs := renderNftRuleset(p)

	// The endpoint pass stays open so the encrypted handshake reaches the server
	// and the tunnel can reconnect — a cut endpoint would livelock recovery.
	if !strings.Contains(rs, "203.0.113.5") {
		t.Errorf("VPN full block must keep the endpoint accept (reconnect path):\n%s", rs)
	}
	// But the tunnel-interface accept is dropped: no user traffic may leak to a
	// forbidden exit. The tunnel iface name appears only in that accept rule.
	if strings.Contains(rs, "utun4") {
		t.Errorf("VPN full block must NOT pass the tunnel interface (user egress cut):\n%s", rs)
	}
	// The dst-IP allowlist is still meaningless under a tunnel and is omitted.
	if strings.Contains(rs, "34.117.59.81") {
		t.Errorf("VPN full block must not emit the dst-IP allowlist host:\n%s", rs)
	}
	if !strings.Contains(rs, `oifname "lo" accept`) {
		t.Errorf("loopback must still pass:\n%s", rs)
	}
	assertDefaultDrop(t, rs)
}

func TestApplyNftGuardRequiresTunnelIface(t *testing.T) {
	// The guard check runs before any nft call, so this is safe without root.
	if err := (&nftBackend{}).Apply(Policy{Mode: ModeGuard}); err == nil {
		t.Fatal("Apply(guard, no tunnel ifaces) = nil, want error (would be a total lockout)")
	}
}
