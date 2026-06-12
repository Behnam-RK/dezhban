// Package netdetect discovers VPN tunnel interfaces for the interface-aware
// guard, so operators need not hand-name them. It is platform-independent: it
// reads the kernel's interface list via stdlib net and classifies entries by
// name pattern.
//
// Scope is deliberately narrow. A false positive here is NOT harmless: in guard
// mode every detected interface gets an egress accept rule, so misclassifying a
// physical uplink (a PPPoE/cellular WAN, which is point-to-point and not a VPN)
// would keep that uplink open and leak unencrypted traffic past the kill switch.
// We therefore match on tunnel name only and do NOT trust the point-to-point
// flag alone, since ordinary WAN links carry it too. VPN *endpoint* detection is
// likewise not automated: a wrong endpoint punches a hole in the block, or if
// missing prevents reconnection, so endpoints stay explicit in config. Explicit
// config values always win over detection.
package netdetect

import (
	"net"
	"strings"
)

// tunnelPrefixes are case-insensitive interface-name prefixes that mark a VPN
// tunnel across platforms: macOS/iOS utun, generic tun/tap, WireGuard wg,
// IPsec, and common commercial-VPN driver names. "ppp" is deliberately absent:
// ppp0 is routinely the physical DSL/cellular WAN, not a VPN, and classifying it
// as a tunnel would keep the uplink open in guard mode (a leak).
var tunnelPrefixes = []string{
	"utun", "tun", "tap", "wg", "ipsec", "nordlynx", "proton", "gpd",
}

// tunnelKeywords are substrings found in the friendly interface aliases Windows
// assigns to VPN adapters (e.g. "WireGuard Tunnel", "OpenVPN TAP-Windows").
var tunnelKeywords = []string{"wireguard", "openvpn", "tap-windows", "tunnel", "vpn"}

// isTunnelName reports whether an interface name looks like a VPN tunnel by its
// name alone. Pure and case-insensitive so it is deterministically testable.
func isTunnelName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	for _, p := range tunnelPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	for _, kw := range tunnelKeywords {
		if strings.Contains(n, kw) {
			return true
		}
	}
	return false
}

// isTunnelIface decides whether an interface is a usable tunnel: it must be up,
// non-loopback, and match a tunnel name. The point-to-point flag is intentionally
// NOT sufficient on its own — physical WAN links (PPPoE, cellular) carry it too,
// and trusting it would keep a physical uplink open in guard mode (a leak). Split
// out from TunnelInterfaces so the classification is testable without a live
// interface list.
func isTunnelIface(name string, flags net.Flags) bool {
	if flags&net.FlagLoopback != 0 || flags&net.FlagUp == 0 {
		return false
	}
	return isTunnelName(name)
}

// TunnelInterfaces returns the names of up, non-loopback interfaces that look
// like VPN tunnels. The result feeds the guard's TunnelIfaces when the operator
// left them unset and enabled autodetect. An empty result is not an error — the
// caller decides whether that is safe (it is not, for an enabled guard).
func TunnelInterfaces() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ifc := range ifaces {
		if isTunnelIface(ifc.Name, ifc.Flags) {
			out = append(out, ifc.Name)
		}
	}
	return out, nil
}
