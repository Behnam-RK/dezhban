//go:build darwin

package netdetect

import (
	"bufio"
	"context"
	"net"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Candidate is a VPN server endpoint observed on the physical interface: the far
// side of an encrypted-transport socket that a VPN client opened directly on the
// WAN link (bypassing its own tunnel). VPN names the connected service it is
// attributed to, when known; Process is the executable that owns the socket, which
// is what makes the attribution credible rather than a guess.
type Candidate struct {
	VPN     string
	Server  netip.Addr
	Port    int
	Process string
}

// DiscoverEndpoints finds the real server address(es) of the currently-connected
// VPN on macOS by looking for sockets that (a) are bound to a PHYSICAL interface IP,
// (b) talk to a PUBLIC address, and (c) are owned by a process that is plausibly a
// VPN transport.
//
// (c) is not optional, and leaving it out was a security bug. The original premise —
// "a full-tunnel VPN routes all app traffic over the tunnel, so the only physical-side
// public socket IS the VPN" — is false on a real machine: apps bind to the physical
// interface all the time (a socket opened before the tunnel came up, anything scoped to
// en0, any process that isn't routed through the tunnel). Observed in the wild, this
// returned GitHub, Cloudflare and Google as "VPN endpoints". Those addresses then went
// straight into the guard's pass list, which is the whole ballgame: the kill switch
// would punch permanent holes to arbitrary hosts (a leak) while still blocking the real
// VPN server (a blackout). A diagnostic-grade heuristic was wired into an
// enforcement-grade allowlist.
//
// So: an unattributable socket is NOT an endpoint. When nothing can be attributed we
// return nothing, and the caller refuses to arm a guard it cannot build correctly —
// which is the safe direction. The user names the server explicitly (`vpn import` reads
// it out of the VPN's own config), and that is the reliable path regardless.
//
// Note this cannot find WireGuard at all, by construction: WireGuard sends from an
// UNCONNECTED UDP socket, so it has no foreign address for any socket table to report.
// No amount of retrying will surface it. Best-effort, macOS-only, IPv4.
func DiscoverEndpoints() ([]Candidate, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	phys, err := physicalIPv4s()
	if err != nil {
		return nil, err
	}
	vpn := connectedVPNName(ctx) // "" if none/unreadable; non-fatal

	socks, err := physicalSockets(ctx, phys)
	if err != nil {
		return nil, err
	}

	var cands []Candidate
	seen := map[string]bool{}
	exeCache := map[int]string{}
	for _, s := range socks {
		exe, ok := exeCache[s.PID]
		if !ok {
			exe = processPath(ctx, s.PID)
			exeCache[s.PID] = exe
		}
		if !isVPNTransport(exe) {
			continue // some app's socket on the physical link, not the VPN's
		}
		key := s.Server.String() + ":" + strconv.Itoa(s.Port)
		if seen[key] {
			continue
		}
		seen[key] = true
		cands = append(cands, Candidate{VPN: vpn, Server: s.Server, Port: s.Port, Process: exe})
	}
	return cands, nil
}

// socket is one connected socket on the physical link, with the pid that owns it.
type socket struct {
	Server netip.Addr
	Port   int
	PID    int
}

// physicalSockets lists connected sockets whose local address is a physical interface
// IP and whose peer is public, via `lsof`. lsof (unlike netstat) reports the owning
// pid in a machine-readable form, which is the whole point — see DiscoverEndpoints.
// Run as root (the daemon is) it sees every process's sockets; run as an unprivileged
// user it sees only that user's, which can only make discovery quieter, never wrong.
func physicalSockets(ctx context.Context, phys map[netip.Addr]bool) ([]socket, error) {
	// -F pn: machine-readable, pid ("p") then name ("n") per socket. -nP: no DNS or
	// port-name lookups (fast, and no reverse-DNS traffic from a firewall tool).
	out, err := exec.CommandContext(ctx, "lsof", "-nP", "-i4", "-F", "pn").Output()
	if err != nil {
		// lsof exits 1 when it finds nothing; that is not an error for us. Any output
		// we did get is still parsed below.
		if len(out) == 0 {
			return nil, nil
		}
	}

	var socks []socket
	pid := 0
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			// Connected sockets only: "10.0.0.2:54849->160.79.104.10:443". A listener
			// ("*:443") has no peer and cannot tell us a server address.
			l, r, ok := strings.Cut(line[1:], "->")
			if !ok {
				continue
			}
			local, _, ok := splitLsofAddr(l)
			if !ok || !phys[local] {
				continue
			}
			server, port, ok := splitLsofAddr(r)
			if !ok || port == 0 {
				continue
			}
			if server.IsGlobalUnicast() && !server.IsPrivate() {
				socks = append(socks, socket{Server: server, Port: port, PID: pid})
			}
		}
	}
	return socks, nil
}

// processPath returns the executable path of pid, or "" if it can't be read.
func processPath(ctx context.Context, pid int) string {
	if pid <= 0 {
		return ""
	}
	out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// isVPNTransport reports whether an executable path plausibly belongs to a VPN's
// transport. It is deliberately a NAMED allowlist rather than "anything we can't rule
// out": the cost of a false positive is a permanent hole in the kill switch for
// whatever that process happened to be talking to, so the default answer must be no.
//
// A false NEGATIVE is cheap and safe by comparison — discovery finds nothing, the
// daemon refuses to arm a guard it can't build, and the user names the endpoint
// explicitly (which is the dependable path anyway). If your VPN client isn't matched
// here, set vpn.endpoints; don't loosen this.
func isVPNTransport(exe string) bool {
	if exe == "" {
		return false
	}
	l := strings.ToLower(exe)
	// Most commercial macOS VPNs (and WireGuard) ship their transport as a
	// NetworkExtension provider bundle.
	if strings.Contains(l, "networkextension") || strings.Contains(l, ".appex") {
		return true
	}
	for _, k := range []string{
		"vpn",       // openvpn, nordvpnd, ProtonVPN, expressvpn, …
		"wireguard", // wireguard-go, wg-quick
		"tailscaled",
		"mullvad",
		"lightway",
		"xray", "v2ray", "sing-box", "hysteria", "shadowsocks", "clash",
		"tunnelblick",
	} {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

// splitLsofAddr parses lsof's "IP:PORT" form.
func splitLsofAddr(s string) (netip.Addr, int, bool) {
	host, portStr, ok := strings.Cut(s, ":")
	if !ok {
		return netip.Addr{}, 0, false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return netip.Addr{}, 0, false
	}
	return addr.Unmap(), port, true
}

// physicalIPv4s collects the IPv4 addresses of every up, non-loopback,
// non-tunnel interface — the source addresses a VPN's WAN transport would bind.
func physicalIPv4s() (map[netip.Addr]bool, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := map[netip.Addr]bool{}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagUp == 0 {
			continue
		}
		if isTunnelName(ifc.Name) {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip, ok := netip.AddrFromSlice(ipnet.IP); ok {
				ip = ip.Unmap()
				if ip.Is4() {
					out[ip] = true
				}
			}
		}
	}
	return out, nil
}

// splitHostPort parses macOS netstat's IP.PORT form (e.g. "192.168.88.112.64656"
// or "*.443"). Returns ok=false for wildcards and unparyable addresses.
func splitHostPort(s string) (netip.Addr, int, bool) {
	i := strings.LastIndex(s, ".")
	if i < 0 {
		return netip.Addr{}, 0, false
	}
	host, portStr := s[:i], s[i+1:]
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return netip.Addr{}, 0, false
	}
	return addr.Unmap(), port, true
}

// connectedVPNName returns the name of the first Connected service in
// `scutil --nc list`, or "" if none/unavailable.
func connectedVPNName(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "scutil", "--nc", "list").Output()
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, "(Connected)") {
			continue
		}
		// The friendly name is the first double-quoted field on the line.
		if a := strings.IndexByte(line, '"'); a >= 0 {
			if b := strings.IndexByte(line[a+1:], '"'); b >= 0 {
				return line[a+1 : a+1+b]
			}
		}
	}
	return ""
}
