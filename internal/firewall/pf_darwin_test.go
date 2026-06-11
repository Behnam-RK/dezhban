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

func TestRenderRuleset(t *testing.T) {
	a := Allowlist{
		DNS:   []netip.Addr{mustAddr(t, "1.1.1.1"), mustAddr(t, "8.8.8.8")},
		Hosts: []netip.Addr{mustAddr(t, "34.117.59.81")},
	}
	rs := renderRuleset(a)

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

	// The default-deny must be the last rule so unmatched egress is dropped.
	lines := strings.Split(strings.TrimSpace(rs), "\n")
	if last := lines[len(lines)-1]; last != "block drop out all" {
		t.Errorf("last rule = %q, want default-deny last", last)
	}
}

func TestRenderRulesetEmptyAllowlist(t *testing.T) {
	rs := renderRuleset(Allowlist{})
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
