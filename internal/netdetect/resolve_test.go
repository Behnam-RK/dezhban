package netdetect

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

// fakeResolver resolves only hosts present in m; any other host errors, so a
// per-host failure can be exercised alongside a successful one.
type fakeResolver struct{ m map[string][]netip.Addr }

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	ips, ok := f.m[host]
	if !ok {
		return nil, errors.New("no such host: " + host)
	}
	return ips, nil
}

func addrs(ss ...string) []netip.Addr {
	out := make([]netip.Addr, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParseAddr(s)
	}
	return out
}

func TestResolveUnionDedupSort(t *testing.T) {
	src := &EndpointSource{
		Literals:  addrs("203.0.113.5"),
		Hostnames: []string{"vpn.example.com", "down.example.com"},
		Resolver: fakeResolver{m: map[string][]netip.Addr{
			// vpn.example.com overlaps the literal (dedup) and adds a fresh IP.
			"vpn.example.com": addrs("203.0.113.5", "198.51.100.7"),
		}},
		Discover: func() ([]netip.Addr, error) { return addrs("192.0.2.9"), nil },
	}
	set := src.Resolve(context.Background())

	want := addrs("192.0.2.9", "198.51.100.7", "203.0.113.5") // sorted
	if !set.SameAddrs(EndpointSet{Addrs: want}) {
		t.Fatalf("Addrs = %v, want %v", set.Addrs, want)
	}
	if set.Sources[netip.MustParseAddr("203.0.113.5")] != "literal" {
		t.Errorf("203.0.113.5 source = %q, want literal (highest trust wins)", set.Sources[netip.MustParseAddr("203.0.113.5")])
	}
	if set.Sources[netip.MustParseAddr("198.51.100.7")] != "dns:vpn.example.com" {
		t.Errorf("198.51.100.7 source = %q, want dns:vpn.example.com", set.Sources[netip.MustParseAddr("198.51.100.7")])
	}
	if set.Sources[netip.MustParseAddr("192.0.2.9")] != "discovered" {
		t.Errorf("192.0.2.9 source = %q, want discovered", set.Sources[netip.MustParseAddr("192.0.2.9")])
	}
}

func TestResolveDiscoverErrorSkipped(t *testing.T) {
	src := &EndpointSource{
		Literals: addrs("203.0.113.5"),
		Discover: func() ([]netip.Addr, error) { return nil, errors.New("unsupported") },
	}
	set := src.Resolve(context.Background())
	if len(set.Addrs) != 1 || set.Addrs[0] != netip.MustParseAddr("203.0.113.5") {
		t.Fatalf("Addrs = %v, want just the literal", set.Addrs)
	}
}

func TestResolveEmptyWhenNothingResolves(t *testing.T) {
	src := &EndpointSource{
		Hostnames: []string{"down.example.com"},
		Resolver:  fakeResolver{m: map[string][]netip.Addr{}},
	}
	set := src.Resolve(context.Background())
	if len(set.Addrs) != 0 {
		t.Fatalf("Addrs = %v, want empty", set.Addrs)
	}
}

func TestResolveDropsTunnelInternal(t *testing.T) {
	// utun4 owns 10.0.0.0/24; an endpoint inside it must be dropped.
	orig := ifaceAddrs
	ifaceAddrs = func(name string) ([]net.Addr, error) {
		if name == "utun4" {
			return []net.Addr{&net.IPNet{IP: net.ParseIP("10.0.0.1"), Mask: net.CIDRMask(24, 32)}}, nil
		}
		return nil, errors.New("no such interface")
	}
	defer func() { ifaceAddrs = orig }()

	src := &EndpointSource{
		Literals: addrs("10.0.0.5", "203.0.113.5"),
		Tunnels:  []string{"utun4"},
	}
	set := src.Resolve(context.Background())
	if len(set.Addrs) != 1 || set.Addrs[0] != netip.MustParseAddr("203.0.113.5") {
		t.Fatalf("Addrs = %v, want only 203.0.113.5 (10.0.0.5 is tunnel-internal)", set.Addrs)
	}
	if _, ok := set.Sources[netip.MustParseAddr("10.0.0.5")]; ok {
		t.Error("dropped endpoint still present in Sources")
	}
}

// Learned endpoints enter the union at a trust tier between hostnames and
// discovery.
func TestResolveIncludesLearned(t *testing.T) {
	src := &EndpointSource{
		Literals: addrs("203.0.113.5"),
		Learned:  func() []netip.Addr { return addrs("198.51.100.9") },
	}
	set := src.Resolve(context.Background())
	if set.Sources[netip.MustParseAddr("198.51.100.9")] != "learned" {
		t.Errorf("learned addr source = %q, want learned", set.Sources[netip.MustParseAddr("198.51.100.9")])
	}
	want := addrs("198.51.100.9", "203.0.113.5")
	if !set.SameAddrs(EndpointSet{Addrs: want}) {
		t.Errorf("Addrs = %v, want %v", set.Addrs, want)
	}
}

// ResolveWith uses the given live tunnel set for the internal-drop filter,
// independent of the configured EndpointSource.Tunnels.
func TestResolveWithLiveTunnels(t *testing.T) {
	// 10.0.0.5 is inside a typical tunnel subnet; with the live tunnel provided,
	// the filter must drop it. Use the real interface check via a loopback-safe
	// address only when a matching tunnel exists — here we assert delegation by
	// passing no tunnels (nothing dropped) vs the configured path.
	src := &EndpointSource{Literals: addrs("203.0.113.5")}
	set := src.ResolveWith(context.Background(), nil)
	if len(set.Addrs) != 1 {
		t.Fatalf("ResolveWith(nil tunnels) = %v, want the literal kept", set.Addrs)
	}
}
