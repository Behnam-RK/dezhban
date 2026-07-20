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
