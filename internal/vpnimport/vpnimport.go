// Package vpnimport extracts VPN server endpoints from common client config
// formats so a profile can be created without hand-copying the server address.
// It reads ONLY endpoints — never keys, credentials, or full server lists — and
// strips ports (dezhban's enforcement is host-scoped). Everything is stdlib.
package vpnimport

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

// Extract reads the file at path, sniffs its format (by extension then content),
// and returns the deduplicated host endpoints found (ports stripped), plus the
// format name. Loopback and unspecified addresses are filtered out.
func Extract(path string) (endpoints []string, format string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	format = sniff(path, data)
	var hosts []string
	switch format {
	case "wireguard":
		hosts, err = extractWireGuard(strings.NewReader(string(data)))
	case "openvpn":
		hosts, err = extractOpenVPN(strings.NewReader(string(data)))
	case "v2ray":
		hosts, err = extractJSON(data)
	default:
		return nil, "", fmt.Errorf("unrecognized config format (expected WireGuard .conf, OpenVPN .ovpn, or V2Ray/sing-box JSON)")
	}
	if err != nil {
		return nil, format, err
	}
	hosts = dedupeFilter(hosts)
	if len(hosts) == 0 {
		return nil, format, fmt.Errorf("no usable server endpoints found in %s config", format)
	}
	return hosts, format, nil
}

func sniff(path string, data []byte) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".conf":
		return "wireguard"
	case ".ovpn":
		return "openvpn"
	case ".json":
		return "v2ray"
	}
	trimmed := strings.TrimSpace(string(data))
	switch {
	case strings.HasPrefix(trimmed, "{"):
		return "v2ray"
	case strings.Contains(trimmed, "[Interface]") || strings.Contains(trimmed, "[Peer]"):
		return "wireguard"
	case strings.Contains(trimmed, "remote "):
		return "openvpn"
	}
	return ""
}

// maxConfigLine caps a single scanned line. WireGuard/OpenVPN configs can embed
// long lines (inline certs/keys/blobs) well past bufio.Scanner's default 64K
// token limit; without a larger buffer, scanning aborts with ErrTooLong and the
// import fails even when the endpoint line itself is short.
const maxConfigLine = 8 << 20 // 8 MiB

// newConfigScanner returns a line scanner with a raised token limit.
func newConfigScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxConfigLine)
	return sc
}

// extractWireGuard reads `Endpoint = host:port` lines from [Peer] sections.
func extractWireGuard(r io.Reader) ([]string, error) {
	var out []string
	sc := newConfigScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(strings.ToLower(line), "endpoint") {
			continue
		}
		_, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if h := hostOnly(strings.TrimSpace(val)); h != "" {
			out = append(out, h)
		}
	}
	return out, sc.Err()
}

// extractOpenVPN reads `remote <host> [port] [proto]` lines.
func extractOpenVPN(r io.Reader) ([]string, error) {
	var out []string
	sc := newConfigScanner(r)
	for sc.Scan() {
		fields := strings.Fields(strings.TrimSpace(sc.Text()))
		if len(fields) < 2 || strings.ToLower(fields[0]) != "remote" {
			continue
		}
		if h := hostOnly(fields[1]); h != "" {
			out = append(out, h)
		}
	}
	return out, sc.Err()
}

// extractJSON walks a V2Ray/Xray/sing-box config, collecting string values under
// "address" or "server" keys that have a numeric port sibling. The generic walk
// is robust to the schema variance across forks.
func extractJSON(data []byte) ([]string, error) {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	var out []string
	walkJSON(root, &out)
	return out, nil
}

func walkJSON(v any, out *[]string) {
	switch t := v.(type) {
	case map[string]any:
		hasPort := false
		for _, k := range []string{"port", "server_port"} {
			if _, ok := t[k]; ok {
				hasPort = true
			}
		}
		if hasPort {
			for _, k := range []string{"address", "server"} {
				if s, ok := t[k].(string); ok {
					if h := hostOnly(s); h != "" {
						*out = append(*out, h)
					}
				}
			}
		}
		for _, child := range t {
			walkJSON(child, out)
		}
	case []any:
		for _, child := range t {
			walkJSON(child, out)
		}
	}
}

// hostOnly strips a :port suffix if present and returns the bare host/IP, or ""
// if the value is empty.
func hostOnly(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	// A bracketed IPv6 literal with no :port suffix (e.g. OpenVPN
	// `remote [2001:db8::1] 443`, where the port is a separate token) isn't valid
	// host:port, so SplitHostPort fails above. Strip the surrounding brackets so
	// the address round-trips through netip and is valid for later consumers.
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return s[1 : len(s)-1]
	}
	return s
}

// dedupeFilter removes duplicates and drops loopback/unspecified/private-range
// noise (a private IP is never a public VPN server; it is almost always a LAN or
// tunnel-internal address that would be useless — or a lockout — as an endpoint).
func dedupeFilter(hosts []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, h := range hosts {
		if h == "" || seen[h] {
			continue
		}
		if a, err := netip.ParseAddr(h); err == nil {
			if a.IsLoopback() || a.IsUnspecified() || a.IsPrivate() || a.IsLinkLocalUnicast() {
				continue
			}
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}
