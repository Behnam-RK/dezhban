package firewall

import (
	"net/netip"
	"testing"
)

func mustCanonAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("ParseAddr(%q): %v", s, err)
	}
	return a
}

// A 4-in-6 mapped address must never survive into a Policy.
//
// This is a lockout regression test, and the failure it guards is silent. pf
// accepts `::ffff:1.2.3.4` — it does not error — and expands it to an *inet6*
// rule that real IPv4 traffic can never match. A VPN endpoint rendered that way
// looks present in `pfctl -sr` while passing nothing, so the tunnel's own
// handshake falls through to the default-deny and the VPN can never connect.
//
// Mapped addresses arrive easily: netip.AddrFromSlice on a 16-byte net.IP
// produces them, and learned.json round-trips endpoints through ParseAddr.
func TestPolicyInputUnmapsV4InV6(t *testing.T) {
	mapped := mustCanonAddr(t, "::ffff:203.0.113.9")
	if mapped.Is4() {
		t.Fatal("precondition: mapped address should not report Is4 before unmapping")
	}

	in := PolicyInput{
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{mapped},
		Allowlist: Allowlist{
			DNS:   []netip.Addr{mustCanonAddr(t, "::ffff:1.1.1.1")},
			Hosts: []netip.Addr{mustCanonAddr(t, "::ffff:9.9.9.9")},
		},
	}

	for name, pol := range map[string]Policy{
		"guard":     in.Guard(),
		"fullblock": in.FullBlock(),
		"switch":    in.SwitchWindow(),
	} {
		t.Run(name, func(t *testing.T) {
			for _, a := range pol.VPNEndpoints {
				if !a.Is4() {
					t.Errorf("endpoint %s is not canonical IPv4 — pf would emit an inet6 rule that never matches", a)
				}
				if got := a.String(); got != "203.0.113.9" {
					t.Errorf("endpoint rendered as %q, want %q", got, "203.0.113.9")
				}
			}
			for _, a := range append(pol.Allowlist.DNS, pol.Allowlist.Hosts...) {
				if !a.Is4() {
					t.Errorf("allowlist entry %s is not canonical IPv4", a)
				}
			}
		})
	}
}

// Genuine IPv6 must pass through untouched — Unmap only strips the 4-in-6
// wrapper, and a test that only proves "everything becomes v4" would pass on a
// broken implementation that mangles real v6.
func TestPolicyInputPreservesRealIPv6(t *testing.T) {
	v6 := mustCanonAddr(t, "2001:db8::1")
	in := PolicyInput{Tunnels: []string{"utun4"}, Endpoints: []netip.Addr{v6}}
	got := in.Guard().VPNEndpoints
	if len(got) != 1 || got[0] != v6 {
		t.Errorf("VPNEndpoints = %v, want [%s] unchanged", got, v6)
	}
}

// Mixed families are kept in one list deliberately. pf expands an address list
// into one rule per address and infers the family of each — verified with
// `pfctl -nvf`, `to { 1.2.3.4 2001:db8::1 }` becomes an inet rule and an inet6
// rule. So no per-family splitting is needed at this seam; nft does its own
// split inside its renderer, where the syntax actually requires it.
func TestPolicyInputKeepsMixedFamilies(t *testing.T) {
	v4, v6 := mustCanonAddr(t, "203.0.113.9"), mustCanonAddr(t, "2001:db8::1")
	in := PolicyInput{Tunnels: []string{"utun4"}, Endpoints: []netip.Addr{v4, v6}}
	got := in.Guard().VPNEndpoints
	if len(got) != 2 || got[0] != v4 || got[1] != v6 {
		t.Errorf("VPNEndpoints = %v, want [%s %s] in order", got, v4, v6)
	}
}

// An invalid address must be dropped rather than rendered. The zero netip.Addr
// stringifies to "invalid IP", which would produce a ruleset pf genuinely does
// reject — turning one bad entry into a total failure to install any rules.
func TestPolicyInputDropsInvalidAddrs(t *testing.T) {
	in := PolicyInput{
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{{}, mustCanonAddr(t, "203.0.113.9"), {}},
	}
	got := in.Guard().VPNEndpoints
	if len(got) != 1 || got[0].String() != "203.0.113.9" {
		t.Errorf("VPNEndpoints = %v, want only the valid address", got)
	}
}

// The LAN pass is destination-scoped, and that is the whole safety argument —
// it is why allowing it by default costs nothing against the threat model.
//
// A pass scoped to an INTERFACE ("allow the LAN interface") would pass
// everything routed out of it, including the internet, which would quietly turn
// the kill switch off. A pass scoped to DESTINATION PREFIXES cannot: a packet to
// a public address does not match them whatever interface carries it. This test
// pins that distinction by asserting no rendered LAN prefix contains a public
// address, so a future edit that swaps in an interface match or a `to any`
// fails here rather than in the field.
func TestLocalNetworkPrefixesAreAllPrivate(t *testing.T) {
	public := []netip.Addr{
		mustCanonAddr(t, "8.8.8.8"),
		mustCanonAddr(t, "1.1.1.1"),
		mustCanonAddr(t, "203.0.113.9"),
		mustCanonAddr(t, "2001:4860:4860::8888"),
		// Multicast has globally-routable scopes, and a unicast-only sample would
		// never catch them: 224/4 and ff00::/8 are NOT safe shorthands for "local".
		// These are designed to cross the internet, so a pass justified by "this
		// traffic never leaves the building" must exclude them.
		mustCanonAddr(t, "232.1.2.3"),  // source-specific multicast (RFC4607)
		mustCanonAddr(t, "233.1.2.3"),  // GLOP, globally assigned (RFC3180)
		mustCanonAddr(t, "ff0e::1234"), // IPv6 global scope
		mustCanonAddr(t, "ff0e::c"),    // SSDP at global scope
	}
	for _, raw := range LocalNetworkPrefixes {
		pfx, err := netip.ParsePrefix(raw)
		if err != nil {
			t.Fatalf("LocalNetworkPrefixes entry %q does not parse: %v", raw, err)
		}
		for _, a := range public {
			if pfx.Contains(a) {
				t.Errorf("prefix %s contains public address %s — the LAN pass would become an internet path", raw, a)
			}
		}
	}
}

// Both families must be represented. A v4-only LAN pass would silently fail on
// v6-capable networks — the exact retrofit trap worth avoiding, since mDNS on a
// modern Mac uses ff02::fb as readily as 224.0.0.251.
func TestLocalNetworkPrefixesCoverBothFamilies(t *testing.T) {
	v4, v6 := LocalNetworkPrefixesFor(false), LocalNetworkPrefixesFor(true)
	if len(v4) == 0 || len(v6) == 0 {
		t.Fatalf("need both families: got %d v4, %d v6", len(v4), len(v6))
	}
	if len(v4)+len(v6) != len(LocalNetworkPrefixes) {
		t.Errorf("split lost or duplicated entries: %d + %d != %d", len(v4), len(v6), len(LocalNetworkPrefixes))
	}
	for _, p := range v4 {
		if netip.MustParsePrefix(p).Addr().Is6() {
			t.Errorf("v4 split contains v6 prefix %s", p)
		}
	}
	for _, p := range v6 {
		if netip.MustParsePrefix(p).Addr().Is4() {
			t.Errorf("v6 split contains v4 prefix %s", p)
		}
	}
}

// Discovery must work, not just unicast reachability. Opening the private
// unicast ranges alone leaves printers and Chromecasts "visible but
// undiscoverable", which a user experiences as broken rather than restricted.
func TestLocalNetworkCoversMulticastDiscovery(t *testing.T) {
	// mDNS/Bonjour v4 + v6, and SSDP — how macOS finds printers and AirPlay.
	for _, s := range []string{"224.0.0.251", "239.255.255.250", "ff02::fb"} {
		a := mustCanonAddr(t, s)
		var covered bool
		for _, raw := range LocalNetworkPrefixes {
			if netip.MustParsePrefix(raw).Contains(a) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("discovery address %s is not covered — devices would be visible but undiscoverable", s)
		}
	}
}

// The LAN setting must reach every enforcing posture. Missing it on FULL BLOCK
// would mean a blocked exit country also takes out the printer, which is not
// what the country check is for.
func TestAllowLocalNetworkReachesEveryPosture(t *testing.T) {
	in := PolicyInput{
		Tunnels:           []string{"utun4"},
		Endpoints:         []netip.Addr{mustCanonAddr(t, "203.0.113.9")},
		AllowLocalNetwork: true,
		// A RESTRICTED window: the unrestricted one already passes everything, so
		// only this form has anything to carry.
		WindowProtos: []string{"udp"},
		WindowPorts:  []int{51820},
	}
	for name, pol := range map[string]Policy{
		"guard":     in.Guard(),
		"fullblock": in.FullBlock(),
		"switch":    in.SwitchWindow(),
	} {
		if !pol.AllowLocalNetwork {
			t.Errorf("%s posture dropped AllowLocalNetwork", name)
		}
	}
}

// FULL BLOCK must carry the tunnel GROUPS, not just the concrete interfaces.
// It installs no tunnel pass, so the groups look unused here — but the
// geo-provider rule is scoped to the tunnel, and a host that names only a class
// ("utun") has nothing else to scope to. Dropping them made the backends'
// group-scoping branches unreachable and silently degraded such a host to
// lift-and-probe: the leak this posture exists to remove.
func TestFullBlockCarriesTunnelGroups(t *testing.T) {
	in := PolicyInput{
		TunnelGroups:  []string{"utun"},
		Endpoints:     []netip.Addr{mustCanonAddr(t, "203.0.113.9")},
		ProviderAddrs: []netip.Addr{mustCanonAddr(t, "104.16.1.1")},
	}
	fb := in.FullBlock()
	if len(fb.TunnelGroups) != 1 || fb.TunnelGroups[0] != "utun" {
		t.Errorf("FULL BLOCK dropped TunnelGroups: %v — the provider pass would have nothing to scope to", fb.TunnelGroups)
	}
	// A group-only guard degrades to the FullBlock shape; it must keep them too.
	if g := in.Guard(); len(g.TunnelGroups) != 1 {
		t.Errorf("group-only Guard dropped TunnelGroups: %v", g.TunnelGroups)
	}
}

// CountInvalid must agree with what the constructor actually drops, since it is
// the only signal an operator gets that an endpoint vanished from the ruleset.
func TestCountInvalidMatchesDroppedAddrs(t *testing.T) {
	addrs := []netip.Addr{
		mustCanonAddr(t, "203.0.113.9"),
		{}, // zero value: renders as "invalid IP", which pf rejects wholesale
		mustCanonAddr(t, "198.51.100.4"),
		{},
	}
	if n := CountInvalid(addrs); n != 2 {
		t.Errorf("CountInvalid = %d, want 2", n)
	}
	kept := PolicyInput{Tunnels: []string{"utun4"}, Endpoints: addrs}.Guard().VPNEndpoints
	if len(kept) != len(addrs)-CountInvalid(addrs) {
		t.Errorf("kept %d endpoints, but CountInvalid implies %d", len(kept), len(addrs)-CountInvalid(addrs))
	}
}

// The provider pass belongs to FULL BLOCK only. ModeGuard already passes all
// tunnel egress, so emitting it there would be redundant noise; the switch
// window passes everything anyway.
func TestProviderAddrsOnlyOnFullBlock(t *testing.T) {
	in := PolicyInput{
		Tunnels:       []string{"utun4"},
		Endpoints:     []netip.Addr{mustCanonAddr(t, "203.0.113.9")},
		ProviderAddrs: []netip.Addr{mustCanonAddr(t, "104.16.1.1")},
	}
	if got := in.FullBlock().ProviderAddrs; len(got) != 1 {
		t.Errorf("FullBlock dropped the provider addresses: %v", got)
	}
	if got := in.Guard().ProviderAddrs; len(got) != 0 {
		t.Errorf("Guard carries provider addresses (%v) — redundant, it already passes all tunnel egress", got)
	}
	if got := in.SwitchWindow().ProviderAddrs; len(got) != 0 {
		t.Errorf("SwitchWindow carries provider addresses (%v) — it passes everything already", got)
	}
}

// Provider addresses go through the same canonicalisation as everything else: a
// mapped address here would render as an inet6 rule that never matches, silently
// sending recovery back to lift-and-probe.
func TestProviderAddrsAreCanonicalised(t *testing.T) {
	in := PolicyInput{
		Tunnels:       []string{"utun4"},
		ProviderAddrs: []netip.Addr{mustCanonAddr(t, "::ffff:104.16.1.1"), {}},
	}
	got := in.FullBlock().ProviderAddrs
	if len(got) != 1 || !got[0].Is4() || got[0].String() != "104.16.1.1" {
		t.Errorf("ProviderAddrs = %v, want [104.16.1.1] canonical with the invalid entry dropped", got)
	}
}
