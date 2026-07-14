//go:build darwin

package netdetect

import (
	"net/netip"
	"testing"
)

// The bug this guards: without process attribution, ANY app's socket on the physical
// interface was reported as a VPN endpoint, and those addresses went straight into the
// guard's pass list. On a real machine that meant the kill switch punched permanent
// holes to GitHub, Cloudflare and Google while still blocking the actual VPN server.
// A false positive here is a leak; a false negative just means the user names the
// endpoint by hand. So the default answer must be "no".
func TestIsVPNTransport(t *testing.T) {
	vpn := []string{
		"/Applications/WireGuard.app/Contents/PlugIns/WireGuardNetworkExtension.appex/Contents/MacOS/WireGuardNetworkExtension",
		"/usr/local/bin/openvpn",
		"/Library/Application Support/NordVPN/nordvpnd",
		"/Applications/ProtonVPN.app/Contents/MacOS/ProtonVPN",
		"/usr/local/bin/wireguard-go",
		"/usr/local/bin/xray",
		"/usr/sbin/tailscaled",
	}
	for _, exe := range vpn {
		if !isVPNTransport(exe) {
			t.Errorf("isVPNTransport(%q) = false; a real VPN transport would go undiscovered", exe)
		}
	}

	// Every one of these was actually returned as a "VPN endpoint" by the old
	// attribution-free discovery.
	notVPN := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/usr/bin/curl",
		"/opt/homebrew/bin/gh",
		"/usr/libexec/trustd",
		"/System/Library/CoreServices/backupd",
		"", // unattributable — must never count
	}
	for _, exe := range notVPN {
		if isVPNTransport(exe) {
			t.Errorf("isVPNTransport(%q) = true; its peer would become a permanent hole in the kill switch", exe)
		}
	}
}

func TestSplitLsofAddr(t *testing.T) {
	addr, port, ok := splitLsofAddr("192.168.88.96:54540")
	if !ok || addr != netip.MustParseAddr("192.168.88.96") || port != 54540 {
		t.Fatalf("splitLsofAddr = (%v, %d, %v)", addr, port, ok)
	}
	// A listener has no peer address and must not parse into an endpoint.
	if _, _, ok := splitLsofAddr("*:443"); ok {
		t.Error("splitLsofAddr accepted a wildcard listener address")
	}
	if _, _, ok := splitLsofAddr("nonsense"); ok {
		t.Error("splitLsofAddr accepted a non-address")
	}
}
