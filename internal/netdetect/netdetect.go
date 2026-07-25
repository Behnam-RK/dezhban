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
// flag alone, since ordinary WAN links carry it too. A tunnel name is also not
// sufficient on its own: macOS spawns system utun interfaces (utun0–utun3 for
// Handoff, AirDrop, iCloud Private Relay) that match the name pattern but carry
// no routable address. Guarding those would cut ordinary traffic while missing
// the real VPN, so we additionally require a global-unicast address — the mark
// of an interface that actually routes. VPN *endpoint* detection is
// likewise not automated: a wrong endpoint punches a hole in the block, or if
// missing prevents redial, so endpoints stay explicit in config. Explicit
// config values always win over detection.
package netdetect

import (
	"net"
	"net/netip"
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

// hasGlobalUnicast reports whether any of addrs is a global-unicast IP — the
// signature of an interface that actually carries routable traffic. It excludes
// loopback, IPv4 link-local (169.254/16), IPv6 link-local (fe80::/10), multicast
// and the unspecified address. macOS system utun interfaces (utun0–utun3) carry
// only IPv6 link-local or nothing, so this filters them out while keeping every
// real full-tunnel VPN, which must assign a routable address (often private,
// e.g. 10.x — still global-unicast) to move traffic.
func hasGlobalUnicast(addrs []net.Addr) bool {
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip != nil && ip.IsGlobalUnicast() {
			return true
		}
	}
	return false
}

// isTunnelIface decides whether an interface is a usable tunnel: it must be up,
// non-loopback, match a tunnel name, and carry a global-unicast address. The
// point-to-point flag is intentionally NOT sufficient on its own — physical WAN
// links (PPPoE, cellular) carry it too, and trusting it would keep a physical
// uplink open in guard mode (a leak). The address check is what separates a real
// VPN utun from a macOS system utun that shares the name pattern but routes
// nothing. Split out from TunnelInterfaces so the classification is testable
// without a live interface list.
func isTunnelIface(name string, flags net.Flags, addrs []net.Addr) bool {
	if flags&net.FlagLoopback != 0 || flags&net.FlagUp == 0 {
		return false
	}
	if !isTunnelName(name) {
		return false
	}
	return hasGlobalUnicast(addrs)
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
		// Can't read the addresses → can't confirm the iface routes → skip it.
		// Never guard an interface we cannot verify (a false guard is a leak or
		// a lockout); leaving it out is the safe failure.
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		if isTunnelIface(ifc.Name, ifc.Flags, addrs) {
			out = append(out, ifc.Name)
		}
	}
	return out, nil
}

// TunnelNet pairs a tunnel interface with one of its on-link subnets — the
// network its assigned address sits in (e.g. utun4 with inet 10.0.0.1/24 →
// 10.0.0.0/24). It is the unit CheckEndpointRouting and `doctor` reason over.
type TunnelNet struct {
	Iface  string
	Subnet netip.Prefix
}

// ifaceAddrs reads the addresses of a named interface. A package var so tests
// can supply synthetic interfaces without a live network.
var ifaceAddrs = func(name string) ([]net.Addr, error) {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	return ifc.Addrs()
}

// prefixFromIPNet converts a *net.IPNet to a normalized (network-address)
// netip.Prefix. Returns ok=false for addresses or masks it cannot represent.
func prefixFromIPNet(n *net.IPNet) (netip.Prefix, bool) {
	ip, ok := netip.AddrFromSlice(n.IP)
	if !ok {
		return netip.Prefix{}, false
	}
	ip = ip.Unmap()
	ones, bits := n.Mask.Size()
	// Size returns (0,0) for a non-contiguous mask it cannot express. Accept a
	// real /0 only if the mask length matches the address family; otherwise reject
	// — treating an unrepresentable mask as 0.0.0.0/0 would make Contains match
	// EVERY endpoint and falsely flag them all as tunnel-internal.
	if ones == 0 && bits == 0 {
		return netip.Prefix{}, false
	}
	pfx := netip.PrefixFrom(ip, ones)
	if !pfx.IsValid() {
		return netip.Prefix{}, false
	}
	return pfx.Masked(), true
}

// TunnelSubnets returns the on-link subnets of the given tunnel interfaces. An
// interface that is absent or down is skipped (not an error) — it simply
// contributes no subnet. Pure stdlib, fully portable, sends no packets.
func TunnelSubnets(tunnels []string) ([]TunnelNet, error) {
	var out []TunnelNet
	for _, name := range tunnels {
		addrs, err := ifaceAddrs(name)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if pfx, ok := prefixFromIPNet(ipnet); ok {
				out = append(out, TunnelNet{Iface: name, Subnet: pfx})
			}
		}
	}
	return out, nil
}

// EndpointRoute records a misconfigured VPN endpoint: one that falls inside a
// tunnel interface's own subnet, naming the tunnel and the subnet that contains it.
type EndpointRoute struct {
	Endpoint netip.Addr
	Iface    string
	Subnet   netip.Prefix
}

// CheckEndpointRouting flags every endpoint that sits INSIDE a tunnel's own
// subnet. A VPN endpoint must be the server's address reachable on the PHYSICAL
// interface: the guard keeps it open there so the encrypted transport survives a
// FULL BLOCK or tunnel drop and the host can recover. An endpoint that is itself
// a tunnel-internal address (e.g. the tunnel's peer at 10.0.0.x) can never serve
// that role — pinning a physical-side `pass to <endpoint>` for it is futile, the
// tunnel can't redial through itself, and the host locks itself out.
//
// This is deterministic and has no false positives under a full-tunnel VPN: a
// full tunnel owns the default route, so a route-table probe would report EVERY
// public endpoint as "via the tunnel" — useless. Subnet containment instead only
// fires on addresses that are genuinely internal to the tunnel. It does NOT catch
// a wrong-but-public endpoint (a stale server IP); only observing the live VPN's
// real socket does — that is what `doctor --discover` is for.
func CheckEndpointRouting(endpoints []netip.Addr, tunnels []string) ([]EndpointRoute, error) {
	nets, err := TunnelSubnets(tunnels)
	if err != nil {
		return nil, err
	}
	var bad []EndpointRoute
	for _, ep := range endpoints {
		for _, tn := range nets {
			if tn.Subnet.Contains(ep) {
				bad = append(bad, EndpointRoute{Endpoint: ep, Iface: tn.Iface, Subnet: tn.Subnet})
				break
			}
		}
	}
	return bad, nil
}
