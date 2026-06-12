// Package netdetect discovers VPN tunnel interfaces for the interface-aware
// guard, so operators need not hand-name them. It is platform-independent: it
// reads the kernel's interface list via stdlib net and classifies entries by
// name pattern and the point-to-point flag.
//
// Scope is deliberately narrow. Tunnel-interface detection is safe to automate —
// a wrong guess at worst names an extra interface to keep open. VPN *endpoint*
// detection is NOT automated: guessing the wrong endpoint would punch a hole in
// the block (a leak) or, if missing, prevent reconnection, so endpoints stay
// explicit in config. Explicit config values always win over detection.
package netdetect

import (
	"net"
	"strings"
)

// tunnelPrefixes are case-insensitive interface-name prefixes that mark a VPN
// tunnel across platforms: macOS/iOS utun, generic tun/tap, WireGuard wg,
// IPsec/PPP, and common commercial-VPN driver names.
var tunnelPrefixes = []string{
	"utun", "tun", "tap", "wg", "ppp", "ipsec", "nordlynx", "proton", "gpd",
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

// isTunnelIface decides whether an interface is a usable tunnel: it must be up
// and non-loopback, and either match a tunnel name or carry the point-to-point
// flag (which most tunnels set). Split out from TunnelInterfaces so the
// classification is testable without a live interface list.
func isTunnelIface(name string, flags net.Flags) bool {
	if flags&net.FlagLoopback != 0 || flags&net.FlagUp == 0 {
		return false
	}
	return isTunnelName(name) || flags&net.FlagPointToPoint != 0
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
