package netdetect

import (
	"fmt"
	"net"
	"net/netip"
	"testing"
)

func TestIsTunnelName(t *testing.T) {
	tunnels := []string{
		"utun4", "tun0", "tap0", "wg0", "ipsec0",
		"WireGuard Tunnel", "OpenVPN TAP-Windows Adapter", "Proton VPN",
		"UTUN5", // case-insensitive
	}
	for _, n := range tunnels {
		if !isTunnelName(n) {
			t.Errorf("isTunnelName(%q) = false, want true", n)
		}
	}

	// "ppp0" is NOT a tunnel: it is routinely the physical DSL/cellular WAN.
	notTunnels := []string{"", "lo", "lo0", "eth0", "en0", "wlan0", "Ethernet", "Wi-Fi", "ppp0"}
	for _, n := range notTunnels {
		if isTunnelName(n) {
			t.Errorf("isTunnelName(%q) = true, want false", n)
		}
	}
}

func TestIsTunnelIface(t *testing.T) {
	// addr fixtures: a routable (global-unicast) address vs link-local / none.
	routable := []net.Addr{&net.IPNet{IP: net.ParseIP("10.8.0.2"), Mask: net.CIDRMask(24, 32)}}
	linkLocalOnly := []net.Addr{&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)}}
	var noAddr []net.Addr

	cases := []struct {
		name  string
		flags net.Flags
		addrs []net.Addr
		want  bool
	}{
		{"utun4", net.FlagUp, routable, true},                        // real VPN: tunnel name + routable addr
		{"utun0", net.FlagUp, linkLocalOnly, false},                  // macOS system utun: link-local only
		{"utun1", net.FlagUp, noAddr, false},                         // macOS system utun: no address
		{"eth0", net.FlagUp | net.FlagPointToPoint, routable, false}, // p2p alone is NOT enough (WAN links carry it)
		{"ppp0", net.FlagUp | net.FlagPointToPoint, routable, false}, // physical PPPoE/cellular WAN: not a tunnel
		{"utun4", 0, routable, false},                                // down: skip
		{"lo0", net.FlagUp | net.FlagLoopback, routable, false},      // loopback: skip
		{"wg0", net.FlagUp | net.FlagLoopback, routable, false},      // loopback wins even with tunnel name
		{"eth0", net.FlagUp, routable, false},                        // plain iface
	}
	for _, c := range cases {
		if got := isTunnelIface(c.name, c.flags, c.addrs); got != c.want {
			t.Errorf("isTunnelIface(%q, %v, %v) = %v, want %v", c.name, c.flags, c.addrs, got, c.want)
		}
	}
}

func TestCheckEndpointRouting(t *testing.T) {
	// Stub the interface reader so the test needs no live network. utun4 carries a
	// tunnel-internal /24 plus an IPv6 link-local (which must NOT match anything);
	// utun9 is "down"/absent (read error) → contributes no subnet.
	addrs := map[string][]net.Addr{
		"utun4": {
			&net.IPNet{IP: net.ParseIP("10.0.0.1"), Mask: net.CIDRMask(24, 32)},
			&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)},
		},
	}
	orig := ifaceAddrs
	ifaceAddrs = func(name string) ([]net.Addr, error) {
		a, ok := addrs[name]
		if !ok {
			return nil, fmt.Errorf("no such interface %s", name)
		}
		return a, nil
	}
	defer func() { ifaceAddrs = orig }()

	eps := []netip.Addr{
		netip.MustParseAddr("10.0.0.5"),     // INTERNAL to utun4's 10.0.0.0/24 → flag
		netip.MustParseAddr("5.253.65.186"), // public (stale server) → NOT subnet-detectable, pass
		netip.MustParseAddr("5.253.65.43"),  // public (correct server) → pass
	}
	bad, err := CheckEndpointRouting(eps, []string{"utun4", "utun9"})
	if err != nil {
		t.Fatalf("CheckEndpointRouting error: %v", err)
	}

	if len(bad) != 1 {
		t.Fatalf("flagged %v, want exactly 10.0.0.5", bad)
	}
	if bad[0].Endpoint.String() != "10.0.0.5" || bad[0].Iface != "utun4" {
		t.Errorf("flagged %+v, want endpoint 10.0.0.5 on utun4", bad[0])
	}
	if bad[0].Subnet.String() != "10.0.0.0/24" {
		t.Errorf("subnet = %s, want 10.0.0.0/24", bad[0].Subnet)
	}
}

func TestTunnelInterfacesNeverErrorsAndExcludesLoopback(t *testing.T) {
	got, err := TunnelInterfaces()
	if err != nil {
		t.Fatalf("TunnelInterfaces() error: %v", err)
	}
	for _, n := range got {
		if n == "lo" || n == "lo0" {
			t.Errorf("TunnelInterfaces() returned loopback %q", n)
		}
	}
}
