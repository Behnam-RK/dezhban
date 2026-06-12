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

// Candidate is a guessed VPN server endpoint observed on the physical interface:
// the far side of an encrypted-transport socket that a VPN client opened directly
// on the WAN link (bypassing its own tunnel). VPN names the connected service it
// is attributed to, when known.
type Candidate struct {
	VPN    string
	Server netip.Addr
	Port   int
}

// DiscoverEndpoints heuristically finds the real server address(es) of the
// currently-connected VPN on macOS — automating the manual scutil/netstat/lsof
// hunt. The signal: a full-tunnel VPN routes all app traffic over its tunnel
// (local address = the tunnel's IP), so the ONLY sockets whose local address is a
// PHYSICAL interface IP and whose foreign address is PUBLIC are the VPN's own
// encrypted transport to its server. We read those from `netstat` and attribute
// them to the connected service named by `scutil --nc list`.
//
// Best-effort and macOS-only: it shells out, parses human output, and focuses on
// IPv4. Treat results as candidates to verify against your VPN client's config,
// not gospel — other daemons can also hold a physical-side public socket.
func DiscoverEndpoints() ([]Candidate, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	phys, err := physicalIPv4s()
	if err != nil {
		return nil, err
	}
	vpn := connectedVPNName(ctx) // "" if none/unreadable; non-fatal

	var cands []Candidate
	seen := map[string]bool{}
	for _, proto := range []string{"tcp", "udp"} {
		out, err := exec.CommandContext(ctx, "netstat", "-anv", "-p", proto).Output()
		if err != nil {
			continue // proto table unavailable; try the other
		}
		for _, c := range parseNetstat(string(out), phys) {
			key := c.Server.String() + ":" + strconv.Itoa(c.Port)
			if seen[key] {
				continue
			}
			seen[key] = true
			c.VPN = vpn
			cands = append(cands, c)
		}
	}
	return cands, nil
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

// parseNetstat extracts (foreign public IPv4, port) pairs from `netstat -anv`
// output where the local address is one of our physical interface IPs. macOS
// formats addresses as IP.PORT (dot before the port), so the port is the segment
// after the final dot.
func parseNetstat(out string, phys map[netip.Addr]bool) []Candidate {
	var cands []Candidate
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 {
			continue
		}
		// Column 0 is the protocol (tcp4/tcp6/udp4/…); only IPv4 rows here.
		if !strings.HasSuffix(f[0], "4") {
			continue
		}
		local, _, ok := splitHostPort(f[3])
		if !ok || !phys[local] {
			continue
		}
		foreign, port, ok := splitHostPort(f[4])
		if !ok || port == 0 {
			continue
		}
		// The server side must be a routable public address — this is what
		// separates the VPN's WAN transport from LAN chatter to the gateway.
		if foreign.IsGlobalUnicast() && !foreign.IsPrivate() {
			cands = append(cands, Candidate{Server: foreign, Port: port})
		}
	}
	return cands
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
