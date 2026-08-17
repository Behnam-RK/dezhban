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
	for line := range strings.SplitSeq(rs, "\n") {
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

// TestOutputChainPolicyIsDrop exercises IsBlocked's drift-detection substring
// match against the shape `nft list table inet dezhban` actually renders.
// Captured by hand rather than run against a live kernel — nft requires
// root/CAP_NET_ADMIN and this package has no test seam to fake it, same as
// pf_darwin's TestMainRulesetReferencesAnchor and wfp_windows'
// TestParseProfileQuery.
func TestOutputChainPolicyIsDrop(t *testing.T) {
	const dropPolicy = `table inet dezhban {
	chain output {
		type filter hook output priority 0; policy drop;
		oifname "lo" accept
		ip daddr 10.0.0.1 accept
	}
}`
	if !outputChainPolicyIsDrop(dropPolicy) {
		t.Errorf("expected policy drop to be found in:\n%s", dropPolicy)
	}

	// A chain's hook policy can be rewritten in place (`nft add chain inet
	// dezhban output { policy accept; }`) without touching a single accept
	// rule or deleting the table — the exact drift this check exists to catch.
	const accept = `table inet dezhban {
	chain output {
		type filter hook output priority 0; policy accept;
		oifname "lo" accept
		ip daddr 10.0.0.1 accept
	}
}`
	if outputChainPolicyIsDrop(accept) {
		t.Errorf("expected no policy drop to be found in:\n%s", accept)
	}

	// The check must be scoped to the "output" chain specifically, not a
	// table-wide substring search: some OTHER chain still saying "policy
	// drop" must never paper over the output chain itself having drifted to
	// accept — that is the exact drift this function exists to catch.
	const otherChainStillDrops = `table inet dezhban {
	chain output {
		type filter hook output priority 0; policy accept;
		oifname "lo" accept
	}
	chain unrelated {
		type filter hook input priority 0; policy drop;
	}
}`
	if outputChainPolicyIsDrop(otherChainStillDrops) {
		t.Errorf("expected no policy drop — the output chain itself is accept, "+
			"even though another chain still says drop:\n%s", otherChainStillDrops)
	}
}

func TestRenderNftLegacyFullBlock(t *testing.T) {
	p := Policy{
		Mode:         ModeFullBlock,
		VPNEndpoints: []netip.Addr{mustAddr(t, "203.0.113.5")},
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

// A zero-valued FULL BLOCK is `block --force`: every optional field empty, so
// nothing but loopback may egress. It used to carry a destination-IP allowlist
// here; that pass is gone — see forceBlockPolicy for why no correctly-scoped
// replacement is possible in this posture.
func TestRenderNftForceBlockPassesNothing(t *testing.T) {
	rs := renderNftRuleset(Policy{Mode: ModeFullBlock})
	if strings.Contains(rs, "{  }") || strings.Contains(rs, "{ }") {
		t.Errorf("an empty set rendered as a rule:\n%s", rs)
	}
	for line := range strings.SplitSeq(rs, "\n") {
		if strings.Contains(line, "daddr") {
			t.Errorf("--force emitted a daddr accept rule: %q", line)
		}
		if strings.Contains(line, "dport 53") {
			t.Errorf("--force emitted a DNS rule: %q", line)
		}
	}
	assertDefaultDrop(t, rs)
}

// nft matches destinations per family (`ip daddr` vs `ip6 daddr`), so a mixed
// address list must render as two rules, never one merged set that silently
// matches nothing.
func TestRenderNftV6Split(t *testing.T) {
	p := Policy{
		Mode:         ModeFullBlock,
		VPNEndpoints: []netip.Addr{mustAddr(t, "1.1.1.1"), mustAddr(t, "2606:4700:4700::1111")},
	}
	rs := renderNftRuleset(p)
	if !strings.Contains(rs, "ip daddr { 1.1.1.1 } accept") {
		t.Errorf("v4 endpoint rule missing or merged with v6:\n%s", rs)
	}
	if !strings.Contains(rs, "ip6 daddr { 2606:4700:4700::1111 } accept") {
		t.Errorf("v6 endpoint rule missing or merged with v4:\n%s", rs)
	}
}

func TestRenderNftGuard(t *testing.T) {
	p := Policy{
		Mode:         ModeGuard,
		TunnelIfaces: []string{"utun4", "wg0"},
		VPNEndpoints: []netip.Addr{mustAddr(t, "203.0.113.5")},
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
	// No DNS rule: AllowPhysicalDNS is off here, and nothing else may emit one.
	if strings.Contains(rs, "dport 53") {
		t.Errorf("guard ruleset emitted an unrequested DNS rule:\n%s", rs)
	}
	assertDefaultDrop(t, rs)
}

func TestRenderNftVPNFullBlockCutsTunnelKeepsEndpoints(t *testing.T) {
	p := Policy{
		Mode:         ModeFullBlock,
		TunnelIfaces: []string{"utun4"},
		VPNEndpoints: []netip.Addr{mustAddr(t, "203.0.113.5")},
	}
	rs := renderNftRuleset(p)

	// The endpoint pass stays open so the encrypted handshake reaches the server
	// and the tunnel can redial — a cut endpoint would livelock recovery.
	if !strings.Contains(rs, "203.0.113.5") {
		t.Errorf("VPN full block must keep the endpoint accept (redial path):\n%s", rs)
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

func TestRenderNftAllowPhysicalDNS(t *testing.T) {
	guard := renderNftRuleset(Policy{
		Mode:             ModeGuard,
		TunnelIfaces:     []string{"utun4"},
		VPNEndpoints:     []netip.Addr{mustAddr(t, "203.0.113.5")},
		AllowPhysicalDNS: true,
	})
	for _, w := range []string{"udp dport 53 accept", "tcp dport 53 accept"} {
		if !strings.Contains(guard, w) {
			t.Errorf("guard+allowPhysicalDNS must emit %q:\n%s", w, guard)
		}
	}

	fb := renderNftRuleset(Policy{
		Mode:             ModeFullBlock,
		TunnelIfaces:     []string{"utun4"},
		VPNEndpoints:     []netip.Addr{mustAddr(t, "203.0.113.5")},
		AllowPhysicalDNS: true,
	})
	if !strings.Contains(fb, "udp dport 53 accept") {
		t.Errorf("vpn-full-block+allowPhysicalDNS must emit the DNS accept:\n%s", fb)
	}

	off := renderNftRuleset(Policy{
		Mode:         ModeGuard,
		TunnelIfaces: []string{"utun4"},
		VPNEndpoints: []netip.Addr{mustAddr(t, "203.0.113.5")},
	})
	if strings.Contains(off, "dport 53") {
		t.Errorf("guard without allowPhysicalDNS must NOT emit a DNS accept:\n%s", off)
	}
}

func TestRenderNftSwitchWindowUnrestricted(t *testing.T) {
	rs := renderNftRuleset(Policy{Mode: ModeSwitchWindow})
	if !strings.Contains(rs, "add rule inet dezhban output accept") {
		t.Errorf("unrestricted switch window must accept all outbound:\n%s", rs)
	}
}

func TestRenderNftSwitchWindowRestricted(t *testing.T) {
	rs := renderNftRuleset(Policy{
		Mode:         ModeSwitchWindow,
		VPNEndpoints: []netip.Addr{mustAddr(t, "203.0.113.5")},
		WindowProtos: []string{"udp"},
		WindowPorts:  []int{51820},
	})
	if !strings.Contains(rs, "udp dport 51820 accept") {
		t.Errorf("restricted switch window missing port accept:\n%s", rs)
	}
	if strings.Contains(rs, "output accept\n") {
		t.Errorf("restricted switch window must not accept all outbound:\n%s", rs)
	}
}

func TestRenderNftTunnelGroups(t *testing.T) {
	rs := renderNftRuleset(Policy{Mode: ModeGuard, TunnelGroups: []string{"utun"}})
	if !strings.Contains(rs, `oifname "utun*" accept`) {
		t.Errorf("guard with tunnel group must emit wildcard oifname:\n%s", rs)
	}
}

func TestRenderNftZeroTunnelStandingPosture(t *testing.T) {
	rs := renderNftRuleset(Policy{
		Mode:         ModeFullBlock,
		VPNEndpoints: []netip.Addr{mustAddr(t, "203.0.113.5")},
	})
	if !strings.Contains(rs, "203.0.113.5") {
		t.Errorf("zero-tunnel standing posture must keep endpoints:\n%s", rs)
	}
}

// LAN passes must be emitted per family: nft's `ip daddr` rejects a v6 prefix
// and `ip6 daddr` rejects a v4 one, so a single mixed list would be a ruleset
// that fails to load — and on a kill switch, failing to load means failing to
// protect.
func TestRenderNftLocalNetwork(t *testing.T) {
	for _, mode := range []Mode{ModeGuard, ModeFullBlock} {
		rs := renderNftRuleset(Policy{
			Mode:              mode,
			TunnelIfaces:      []string{"utun4"},
			VPNEndpoints:      []netip.Addr{mustAddr(t, "203.0.113.5")},
			AllowLocalNetwork: true,
		})
		for _, w := range []string{
			"ip daddr { 10.0.0.0/8", // v4 family matcher
			"ip6 daddr { fc00::/7",  // v6 family matcher
		} {
			if !strings.Contains(rs, w) {
				t.Errorf("mode %s with allowLocalNetwork must emit %q:\n%s", mode, w, rs)
			}
		}
		// No v6 prefix may appear inside an `ip daddr` rule (or vice versa).
		for line := range strings.SplitSeq(rs, "\n") {
			if strings.Contains(line, "ip daddr") && strings.Contains(line, "::") {
				t.Errorf("v6 prefix inside an `ip daddr` rule — nft will reject this:\n%s", line)
			}
			if strings.Contains(line, "ip6 daddr") && strings.Contains(line, "10.0.0.0/8") {
				t.Errorf("v4 prefix inside an `ip6 daddr` rule — nft will reject this:\n%s", line)
			}
		}
	}

	// Off by request: no LAN rules at all.
	off := renderNftRuleset(Policy{
		Mode:         ModeGuard,
		TunnelIfaces: []string{"utun4"},
		VPNEndpoints: []netip.Addr{mustAddr(t, "203.0.113.5")},
	})
	if strings.Contains(off, "10.0.0.0/8") {
		t.Errorf("allowLocalNetwork=false must emit no LAN pass:\n%s", off)
	}
}

// The LAN pass must survive a FULL BLOCK with no tunnels — see the pf twin in
// pf_darwin_test.go for the full rationale. Nested inside the VPN branch,
// allowLocalNetwork was silently discarded for a FULL BLOCK with no tunnels, no
// endpoints and allowPhysicalDNS off.
func TestRenderNftLocalNetworkSurvivesNonVPNFullBlock(t *testing.T) {
	p := PolicyInput{AllowLocalNetwork: true}.FullBlock()
	rs := renderNftRuleset(p)
	if !strings.Contains(rs, "10.0.0.0/8") {
		t.Errorf("LAN pass dropped in non-VPN full block despite allowLocalNetwork=true:\n%s", rs)
	}

	// `block --force` must be unaffected.
	forced := renderNftRuleset(Policy{Mode: ModeFullBlock})
	if strings.Contains(forced, "10.0.0.0/8") {
		t.Errorf("block --force must not gain a LAN pass:\n%s", forced)
	}
}

// The provider pass must be tunnel-scoped, must cover every tunnel matcher, and
// must not drag a blanket DNS rule along with it. See tunnelProviderRules in
// pf_darwin.go for why an unscoped `dport 53` would leak every hostname this
// host resolves to the very exit FULL BLOCK is refusing.
func TestRenderNftTunnelScopedProviders(t *testing.T) {
	rs := renderNftRuleset(Policy{
		Mode:          ModeFullBlock,
		TunnelIfaces:  []string{"utun4"},
		TunnelGroups:  []string{"wg"},
		VPNEndpoints:  []netip.Addr{mustAddr(t, "203.0.113.5")},
		ProviderAddrs: []netip.Addr{mustAddr(t, "104.16.1.1")},
	})
	// Both the concrete interface and the class wildcard get the pass: taking
	// only one would leave the lookup unable to reach a tunnel the guard passes.
	for _, w := range []string{`oifname { "utun4" } ip daddr`, `oifname "wg*" ip daddr`} {
		if !strings.Contains(rs, w) {
			t.Errorf("FULL BLOCK must scope the provider pass with %q:\n%s", w, rs)
		}
	}
	for line := range strings.SplitSeq(rs, "\n") {
		if strings.Contains(line, "104.16.1.1") && !strings.Contains(line, "oifname") {
			t.Errorf("provider pass is not tunnel-scoped — it would measure the ISP's country with the tunnel down:\n%s", line)
		}
		// Narrowly the TUNNEL-scoped form: allowPhysicalDNS legitimately renders a
		// bare `udp dport 53 accept` on the physical link.
		if strings.Contains(line, "dport 53") && strings.Contains(line, "oifname") {
			t.Errorf("FULL BLOCK emits a tunnel-scoped DNS pass — every lookup would leak to the forbidden exit:\n%s", line)
		}
	}

	// No tunnel to scope to → emit nothing rather than an unscoped pass.
	noTun := renderNftRuleset(Policy{
		Mode:          ModeFullBlock,
		VPNEndpoints:  []netip.Addr{mustAddr(t, "203.0.113.5")},
		ProviderAddrs: []netip.Addr{mustAddr(t, "104.16.1.1")},
	})
	if strings.Contains(noTun, "104.16.1.1") {
		t.Errorf("with no tunnel the provider pass must be omitted, not emitted unscoped:\n%s", noTun)
	}
}
