package netdetect

import (
	"net"
	"testing"
)

func TestIsTunnelName(t *testing.T) {
	tunnels := []string{
		"utun4", "tun0", "tap0", "wg0", "ppp0", "ipsec0",
		"WireGuard Tunnel", "OpenVPN TAP-Windows Adapter", "Proton VPN",
		"UTUN5", // case-insensitive
	}
	for _, n := range tunnels {
		if !isTunnelName(n) {
			t.Errorf("isTunnelName(%q) = false, want true", n)
		}
	}

	notTunnels := []string{"", "lo", "lo0", "eth0", "en0", "wlan0", "Ethernet", "Wi-Fi"}
	for _, n := range notTunnels {
		if isTunnelName(n) {
			t.Errorf("isTunnelName(%q) = true, want false", n)
		}
	}
}

func TestIsTunnelIface(t *testing.T) {
	cases := []struct {
		name  string
		flags net.Flags
		want  bool
	}{
		{"utun4", net.FlagUp, true},
		{"eth0", net.FlagUp | net.FlagPointToPoint, true}, // p2p flag alone qualifies
		{"utun4", 0, false},                               // down: skip
		{"lo0", net.FlagUp | net.FlagLoopback, false},     // loopback: skip
		{"wg0", net.FlagUp | net.FlagLoopback, false},     // loopback wins even with tunnel name
		{"eth0", net.FlagUp, false},                       // plain iface, no p2p
	}
	for _, c := range cases {
		if got := isTunnelIface(c.name, c.flags); got != c.want {
			t.Errorf("isTunnelIface(%q, %v) = %v, want %v", c.name, c.flags, got, c.want)
		}
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
