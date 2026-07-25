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
	for line := range strings.SplitSeq(strings.TrimSpace(rs), "\n") {
		if strings.HasPrefix(line, "pass") && !strings.HasSuffix(line, "no state") {
			t.Errorf("guard pass rule is not stateless (drops mid-stream flows): %q", line)
		}
	}
	assertDefaultDenyLast(t, rs)
}

func TestApplyGuardRequiresTunnelIface(t *testing.T) {
	// The guard check runs before any pfctl call, so this is safe without root:
	// a guard policy with no tunnel interface would render a total lockout.
	if err := (&pfBackend{}).Apply(Policy{Mode: ModeGuard}); err == nil {
		t.Fatal("Apply(guard, no tunnel ifaces) = nil, want error (would be a total lockout)")
	}
}

func TestRenderRulesetVPNFullBlockCutsTunnelKeepsEndpoints(t *testing.T) {
	// VPN full block cuts the tunnel-interface pass (no user leak) but keeps the
	// endpoint pass so the encrypted handshake reaches the server and the tunnel
	// can redial. The dst-IP allowlist stays meaningless under a tunnel.
	p := Policy{
		Mode:         ModeFullBlock,
		TunnelIfaces: []string{"utun4"},
		VPNEndpoints: []netip.Addr{mustAddr(t, "203.0.113.5")},
		Allowlist:    Allowlist{Hosts: []netip.Addr{mustAddr(t, "34.117.59.81")}},
	}
	rs := renderRuleset(p)

	// The endpoint pass stays open (redial path).
	if !strings.Contains(rs, "203.0.113.5") {
		t.Errorf("VPN full block must keep the endpoint pass (redial path):\n%s", rs)
	}
	// No tunnel-interface pass: the iface name appears only in that pass rule.
	if strings.Contains(rs, "utun4") {
		t.Errorf("VPN full block must NOT pass the tunnel interface (user egress cut):\n%s", rs)
	}
	// The dst-IP allowlist host is still omitted under a tunnel.
	if strings.Contains(rs, "34.117.59.81") {
		t.Errorf("VPN full block must not emit the dst-IP allowlist host:\n%s", rs)
	}
	if !strings.Contains(rs, "pass quick on lo0 all") {
		t.Errorf("loopback must still pass:\n%s", rs)
	}
	assertDefaultDenyLast(t, rs)
}

func TestRenderRulesetAllowPhysicalDNS(t *testing.T) {
	const dnsRule = "pass out quick proto { udp tcp } to any port 53 no state"

	// Guard + AllowPhysicalDNS: the DNS pass appears.
	guard := renderRuleset(Policy{
		Mode:             ModeGuard,
		TunnelIfaces:     []string{"utun4"},
		VPNEndpoints:     []netip.Addr{mustAddr(t, "203.0.113.5")},
		AllowPhysicalDNS: true,
	})
	if !strings.Contains(guard, dnsRule) {
		t.Errorf("guard+allowPhysicalDNS must emit the DNS pass:\n%s", guard)
	}
	assertDefaultDenyLast(t, guard)

	// VPN full block + AllowPhysicalDNS: the DNS pass appears (redial aid).
	fb := renderRuleset(Policy{
		Mode:             ModeFullBlock,
		TunnelIfaces:     []string{"utun4"},
		VPNEndpoints:     []netip.Addr{mustAddr(t, "203.0.113.5")},
		AllowPhysicalDNS: true,
	})
	if !strings.Contains(fb, dnsRule) {
		t.Errorf("vpn-full-block+allowPhysicalDNS must emit the DNS pass:\n%s", fb)
	}

	// Off by default: no DNS pass.
	off := renderRuleset(Policy{
		Mode:         ModeGuard,
		TunnelIfaces: []string{"utun4"},
		VPNEndpoints: []netip.Addr{mustAddr(t, "203.0.113.5")},
	})
	if strings.Contains(off, "port 53") {
		t.Errorf("guard without allowPhysicalDNS must NOT emit a DNS pass:\n%s", off)
	}
}

func TestRenderRulesetSwitchWindowUnrestricted(t *testing.T) {
	rs := renderRuleset(Policy{Mode: ModeSwitchWindow})
	if !strings.Contains(rs, "pass out quick all no state") {
		t.Errorf("unrestricted switch window must pass all outbound:\n%s", rs)
	}
	assertDefaultDenyLast(t, rs) // block-all still last so the anchor is never empty
}

func TestRenderRulesetSwitchWindowRestricted(t *testing.T) {
	rs := renderRuleset(Policy{
		Mode:         ModeSwitchWindow,
		VPNEndpoints: []netip.Addr{mustAddr(t, "203.0.113.5")},
		WindowProtos: []string{"udp"},
		WindowPorts:  []int{51820},
	})
	for _, w := range []string{
		"pass out quick to { 203.0.113.5 }",
		"port 53",
		"proto { udp } to any port { 51820 }",
	} {
		if !strings.Contains(rs, w) {
			t.Errorf("restricted switch window missing %q\n--- got ---\n%s", w, rs)
		}
	}
	if strings.Contains(rs, "pass out quick all no state") {
		t.Errorf("restricted switch window must NOT pass all outbound:\n%s", rs)
	}
	assertDefaultDenyLast(t, rs)
}

func TestRenderRulesetTunnelGroups(t *testing.T) {
	rs := renderRuleset(Policy{Mode: ModeGuard, TunnelGroups: []string{"utun"}})
	if !strings.Contains(rs, "pass out quick on { utun } all no state") {
		t.Errorf("guard with tunnel group must pass the group:\n%s", rs)
	}
	assertDefaultDenyLast(t, rs)
}

func TestRenderRulesetZeroTunnelStandingPosture(t *testing.T) {
	// FULL BLOCK with endpoints but NO tunnel ifaces (daemon-before-VPN standing
	// posture): endpoints stay open so a known VPN can handshake, everything else
	// blocked. Must NOT fall through to the legacy dst-IP allowlist path.
	rs := renderRuleset(Policy{
		Mode:         ModeFullBlock,
		VPNEndpoints: []netip.Addr{mustAddr(t, "203.0.113.5")},
		Allowlist:    Allowlist{DNS: []netip.Addr{mustAddr(t, "1.1.1.1")}},
	})
	if !strings.Contains(rs, "pass out quick to { 203.0.113.5 }") {
		t.Errorf("zero-tunnel standing posture must keep endpoints open:\n%s", rs)
	}
	if strings.Contains(rs, "1.1.1.1") {
		t.Errorf("zero-tunnel standing posture must NOT emit the legacy allowlist:\n%s", rs)
	}
	assertDefaultDenyLast(t, rs)
}

func TestApplyGuardAcceptsTunnelGroupOnly(t *testing.T) {
	// A guard with only a tunnel group (no explicit iface) is valid — the group
	// pass covers current and future tunnels.
	rs := renderRuleset(Policy{Mode: ModeGuard, TunnelGroups: []string{"utun"}})
	if !strings.Contains(rs, "block drop out all") {
		t.Errorf("group-only guard should still render:\n%s", rs)
	}
}

// pf infers each address's family from the address itself, so v4 and v6 LAN
// prefixes share one list — verified with `pfctl -nvf`, a mixed list expands to
// an inet rule plus per-prefix inet6 rules. (nft is the one that needs a split.)
func TestRenderLocalNetwork(t *testing.T) {
	for _, mode := range []Mode{ModeGuard, ModeFullBlock} {
		rs := renderRuleset(Policy{
			Mode:              mode,
			TunnelIfaces:      []string{"utun4"},
			VPNEndpoints:      []netip.Addr{mustAddr(t, "203.0.113.5")},
			AllowLocalNetwork: true,
		})
		for _, w := range []string{"10.0.0.0/8", "192.168.0.0/16", "fc00::/7", "224.0.0.0/24", "239.0.0.0/8"} {
			if !strings.Contains(rs, w) {
				t.Errorf("mode %s with allowLocalNetwork must pass %s:\n%s", mode, w, rs)
			}
		}
		// Destination-scoped, never interface-scoped: an `on <iface>` LAN pass
		// would carry internet traffic too and silently disable the kill switch.
		for line := range strings.SplitSeq(rs, "\n") {
			if strings.Contains(line, "10.0.0.0/8") && strings.Contains(line, " on ") {
				t.Errorf("LAN pass is interface-scoped — it would become an internet path:\n%s", line)
			}
		}
		assertDefaultDenyLast(t, rs)
	}

	off := renderRuleset(Policy{
		Mode:         ModeGuard,
		TunnelIfaces: []string{"utun4"},
		VPNEndpoints: []netip.Addr{mustAddr(t, "203.0.113.5")},
	})
	if strings.Contains(off, "10.0.0.0/8") {
		t.Errorf("allowLocalNetwork=false must emit no LAN pass:\n%s", off)
	}
}

// The LAN pass must not depend on isVPNPolicy. A FULL BLOCK with no tunnels, no
// endpoints and allowPhysicalDNS off takes the renderer's legacy branch, and the
// LAN emission used to be nested inside the VPN branch — so allowLocalNetwork
// was silently discarded in exactly that shape, contradicting ADR-0005 ("a
// blocked exit country should not also take out the printer").
//
// Reachable for real: the daemon's `relaxed` start path applies this posture
// when no endpoints are known yet, and `print-rules --mode fullblock` renders
// it. Every other LAN test sets TunnelIfaces+VPNEndpoints, which forces
// isVPNPolicy true and hides the bug.
func TestRenderLocalNetworkSurvivesNonVPNFullBlock(t *testing.T) {
	p := PolicyInput{AllowLocalNetwork: true}.FullBlock()
	if isVPNPolicy(p) {
		t.Fatal("precondition: this policy must take the non-VPN branch")
	}
	rs := renderRuleset(p)
	if !strings.Contains(rs, "10.0.0.0/8") {
		t.Errorf("LAN pass dropped in non-VPN full block despite allowLocalNetwork=true:\n%s", rs)
	}
	assertDefaultDenyLast(t, rs)

	// `block --force` must be unaffected: it never sets AllowLocalNetwork, so it
	// still cuts everything but loopback and its geo-provider allowlist.
	forced := renderRuleset(Policy{
		Mode:      ModeFullBlock,
		Allowlist: Allowlist{Hosts: []netip.Addr{mustAddr(t, "34.117.59.81")}},
	})
	if strings.Contains(forced, "10.0.0.0/8") {
		t.Errorf("block --force must not gain a LAN pass:\n%s", forced)
	}
}

// The provider pass must be scoped to BOTH the tunnel interface AND the provider
// destinations. Either half alone breaks it, in opposite directions:
//
//   - destination only (a pass on the PHYSICAL link) lets the lookup succeed with
//     the tunnel down and report the ISP's country — a normal, allowed one — so
//     FULL BLOCK would never fire and a switch window would close early on a
//     bogus "good exit". That is the unsafe variant ADR-0006 exists to prevent.
//   - interface only is just ModeGuard: all tunnel egress, i.e. no block at all.
func TestRenderTunnelScopedProviders(t *testing.T) {
	rs := renderRuleset(Policy{
		Mode:          ModeFullBlock,
		TunnelIfaces:  []string{"utun4"},
		VPNEndpoints:  []netip.Addr{mustAddr(t, "203.0.113.5")},
		ProviderAddrs: []netip.Addr{mustAddr(t, "104.16.1.1")},
	})
	var found bool
	for line := range strings.SplitSeq(rs, "\n") {
		if !strings.Contains(line, "104.16.1.1") {
			continue
		}
		found = true
		if !strings.Contains(line, "on { utun4 }") {
			t.Errorf("provider pass is not tunnel-scoped — it would measure the ISP's country with the tunnel down:\n%s", line)
		}
	}
	if !found {
		t.Errorf("FULL BLOCK with providers must pass them through the tunnel:\n%s", rs)
	}
	assertDefaultDenyLast(t, rs)

	// The provider pass must NOT drag a blanket DNS rule along with it. An
	// earlier draft emitted `on <tunnel> proto { udp tcp } to any port 53` so
	// provider hostnames could be re-resolved; `to any` is destination-unscoped,
	// so it passed every application's DNS through the tunnel to the forbidden
	// exit's resolver — handing the exit we are refusing a continuous log of
	// every hostname this host looks up, for as long as FULL BLOCK lasted.
	// Matched narrowly on the TUNNEL-scoped form: `allowPhysicalDNS` legitimately
	// renders `to any port 53` on the physical link, and this assertion must not
	// blame that rule for a leak it is not responsible for.
	for line := range strings.SplitSeq(rs, "\n") {
		if strings.Contains(line, "port 53") && strings.Contains(line, "on {") {
			t.Errorf("FULL BLOCK emits a tunnel-scoped DNS pass — every lookup would leak to the forbidden exit:\n%s", line)
		}
	}

	// No tunnel to scope to → emit nothing rather than an unscoped pass. The
	// daemon falls back to lift-and-probe; a physical-link pass would be worse
	// than the leak it replaces.
	noTun := renderRuleset(Policy{
		Mode:          ModeFullBlock,
		VPNEndpoints:  []netip.Addr{mustAddr(t, "203.0.113.5")},
		ProviderAddrs: []netip.Addr{mustAddr(t, "104.16.1.1")},
	})
	if strings.Contains(noTun, "104.16.1.1") {
		t.Errorf("with no tunnel the provider pass must be omitted, not emitted unscoped:\n%s", noTun)
	}

	// A group-only host must still get a scoped provider pass rather than
	// silently degrading to lift-and-probe.
	grp := renderRuleset(Policy{
		Mode:          ModeFullBlock,
		TunnelGroups:  []string{"utun"},
		VPNEndpoints:  []netip.Addr{mustAddr(t, "203.0.113.5")},
		ProviderAddrs: []netip.Addr{mustAddr(t, "104.16.1.1")},
	})
	if !strings.Contains(grp, "on { utun }") || !strings.Contains(grp, "104.16.1.1") {
		t.Errorf("group-only FULL BLOCK must scope the provider pass to the group:\n%s", grp)
	}

	// GUARD already passes all tunnel egress; no provider rule needed.
	g := renderRuleset(Policy{
		Mode:          ModeGuard,
		TunnelIfaces:  []string{"utun4"},
		VPNEndpoints:  []netip.Addr{mustAddr(t, "203.0.113.5")},
		ProviderAddrs: []netip.Addr{mustAddr(t, "104.16.1.1")},
	})
	if strings.Contains(g, "104.16.1.1") {
		t.Errorf("GUARD should not emit a provider pass — it already passes all tunnel egress:\n%s", g)
	}
}
