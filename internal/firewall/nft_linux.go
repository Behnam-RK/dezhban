//go:build linux

package firewall

import (
	"fmt"
	"net/netip"
	"strings"
)

// Linux enforcement via nftables.
//
// Backend choice (mirrors the macOS pf rationale, see pf_darwin.go):
// we shell out to `nft -f -` with a self-contained ruleset rather than linking
// google/nftables. The plan tentatively preferred the pure-Go library but
// explicitly sanctioned shelling as the fallback; we take it because:
//   - the kill switch's correctness is too important to ride on a library whose
//     own README calls its API early-stage, and
//   - shelling keeps dezhban dependency-light and cross-compiles cleanly (the
//     foreign-OS backend files are simply excluded by build tags).
//
// All state lives in the kernel: a dedicated `inet dezhban` table. The table's
// existence IS the block — there is no enable/disable step as on pf — so the
// backend holds no in-memory state and teardown survives across separate
// `dezhban` invocations (acceptance: unblock works even if the blocker died).
const tableName = "dezhban"

// nftBackend is the Linux FirewallBackend. Stateless: the authoritative state is
// the presence of the `inet dezhban` table in the kernel.
type nftBackend struct{}

// New returns the Linux nftables backend.
func New() (FirewallBackend, error) {
	return &nftBackend{}, nil
}

// Block is the legacy direct-connection entry point: a full block whose only
// exceptions are loopback and the dst-IP allowlist. It is Apply with
// ModeFullBlock and no tunnel interfaces.
func (b *nftBackend) Block(a Allowlist) error {
	return b.Apply(Policy{Mode: ModeFullBlock, Allowlist: a})
}

// Apply installs the ruleset for p as the `inet dezhban` table. The whole
// ruleset is loaded atomically in one `nft -f -` transaction that first replaces
// any existing table, so re-applying never stacks duplicate rules (idempotent)
// and a malformed ruleset fails without touching live state (all-or-nothing).
func (b *nftBackend) Apply(p Policy) error {
	// Guard mode with no tunnel interface would render only loopback + a default
	// drop — a total lockout with no guard at all. Reject it at the backend seam
	// so a programmatic caller (the daemon) cannot self-inflict it, mirroring pf.
	if p.Mode == ModeGuard && len(p.TunnelIfaces) == 0 && len(p.TunnelGroups) == 0 {
		return fmt.Errorf("guard mode requires at least one tunnel interface or group")
	}
	ruleset := renderNftRuleset(p)
	// `nft -c -f -` checks the ruleset without committing it, so a bad allowlist
	// can't half-apply, exactly like pf's `-n` validate step.
	if _, err := nft(ruleset, "-c", "-f", "-"); err != nil {
		return fmt.Errorf("invalid block ruleset: %w", err)
	}
	if _, err := nft(ruleset, "-f", "-"); err != nil {
		return fmt.Errorf("load dezhban table: %w", err)
	}
	return nil
}

// Unblock removes ONLY dezhban's table (surgical — touches nothing else). A
// missing table is not an error: unblock must be safe to run when nothing is
// blocked.
func (b *nftBackend) Unblock() error {
	if blocked, err := b.IsBlocked(); err != nil {
		return err
	} else if !blocked {
		return nil
	}
	if _, err := nft("", "delete", "table", "inet", tableName); err != nil {
		return fmt.Errorf("delete dezhban table: %w", err)
	}
	return nil
}

// IsBlocked reports whether the `inet dezhban` table exists. Unlike pf there is
// no separate enabled/disabled state: a present table is always enforcing.
func (b *nftBackend) IsBlocked() (bool, error) {
	if _, err := nft("", "list", "table", "inet", tableName); err != nil {
		// nft exits non-zero when the table does not exist. Distinguish that
		// (not blocked) from a real failure by matching the kernel's message.
		if strings.Contains(err.Error(), "No such file or directory") ||
			strings.Contains(err.Error(), "does not exist") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Cleanup is best-effort teardown for shutdown/panic. It is just Unblock; any
// error is returned for the caller to log, never treated as fatal.
func (b *nftBackend) Cleanup() error {
	return b.Unblock()
}

// renderNftRuleset builds the self-contained ruleset loaded via `nft -f -`. It is
// the Linux twin of pf's renderRuleset and follows the same postures:
//
//   - ModeGuard: accept egress on the tunnel interface(s) and the handshake to
//     the VPN endpoint(s); everything else is dropped by the chain's default
//     policy, so a tunnel drop cuts traffic with no physical leak.
//   - ModeFullBlock, direct (no tunnel ifaces): accept the dst-IP DNS + geo-API
//     allowlist — the legacy model, valid only off-VPN.
//   - ModeFullBlock, VPN (tunnel ifaces present): drop the tunnel-iface accept
//     so no user traffic leaks to a forbidden exit, but KEEP the endpoint
//     accepts open so the encrypted handshake reaches the server and the tunnel
//     can reconnect. Identical to ModeGuard minus the tunnel-iface accept. The
//     dst-IP allowlist is still meaningless under a tunnel, so it is omitted;
//     the daemon opens a brief guard window to probe for recovery (Phase 4).
//
// The ruleset begins `add table; delete table; add table` so loading is an
// atomic replace: the first `add` makes `delete` safe when no table exists yet,
// then a fresh table is built. The output chain hooks `output` with `policy
// drop`, so any unmatched egress is the default-denied case — the structural
// equivalent of pf's trailing `block drop out all`.
func renderNftRuleset(p Policy) string {
	var b strings.Builder
	b.WriteString("# dezhban ruleset — default-deny outbound (nftables).\n")
	fmt.Fprintf(&b, "add table inet %s\n", tableName)
	fmt.Fprintf(&b, "delete table inet %s\n", tableName)
	fmt.Fprintf(&b, "add table inet %s\n", tableName)
	fmt.Fprintf(&b, "add chain inet %s output { type filter hook output priority 0; policy drop; }\n", tableName)

	rule := func(expr string) {
		fmt.Fprintf(&b, "add rule inet %s output %s\n", tableName, expr)
	}

	// Loopback always passes, on every posture.
	rule(`oifname "lo" accept`)

	switch p.Mode {
	case ModeSwitchWindow:
		if len(p.WindowProtos) == 0 && len(p.WindowPorts) == 0 {
			rule("accept") // unrestricted: pass all outbound for the bounded window
		} else {
			if len(p.TunnelIfaces) > 0 {
				rule(fmt.Sprintf("oifname %s accept", nftIfaceSet(p.TunnelIfaces)))
			}
			for _, g := range p.TunnelGroups {
				rule(fmt.Sprintf("oifname %q accept", g+"*"))
			}
			emitDaddrAccepts(rule, p.VPNEndpoints, "")
			rule("udp dport 53 accept")
			rule("tcp dport 53 accept")
			emitWindowPortAccepts(rule, p)
		}
	case ModeGuard:
		if len(p.TunnelIfaces) > 0 {
			rule(fmt.Sprintf("oifname %s accept", nftIfaceSet(p.TunnelIfaces)))
		}
		for _, g := range p.TunnelGroups {
			// Wildcard oifname: matches every current/future interface of this
			// class (utun*), so a new tunnel needs no rule reload. Safe — tunnels
			// re-encapsulate onto the still-blocked physical interface.
			rule(fmt.Sprintf("oifname %q accept", g+"*"))
		}
		emitDaddrAccepts(rule, p.VPNEndpoints, "")
		emitAllowPhysicalDNS(rule, p)
	default: // ModeFullBlock
		if isVPNPolicy(p) {
			// VPN full block (including the zero-tunnel standing posture): drop the
			// tunnel-iface accept so no user traffic can egress to a forbidden exit,
			// but KEEP the endpoint accepts so the encrypted handshake still reaches
			// the server and the tunnel can reconnect. A cut endpoint would livelock
			// recovery (the VPN could never re-establish to be re-evaluated).
			emitDaddrAccepts(rule, p.VPNEndpoints, "")
			emitAllowPhysicalDNS(rule, p)
		} else {
			// Legacy direct model: dst-IP allowlist over udp and tcp port 53.
			emitDaddrAccepts(rule, p.Allowlist.DNS, "udp dport 53")
			emitDaddrAccepts(rule, p.Allowlist.DNS, "tcp dport 53")
			emitDaddrAccepts(rule, p.Allowlist.Hosts, "")
		}
	}
	return b.String()
}

// emitWindowPortAccepts renders the proto/port passes for a restricted switch
// window (nft). Protocols default to udp+tcp when unspecified.
func emitWindowPortAccepts(rule func(string), p Policy) {
	protos := p.WindowProtos
	if len(protos) == 0 {
		protos = []string{"udp", "tcp"}
	}
	for _, proto := range protos {
		for _, port := range p.WindowPorts {
			rule(fmt.Sprintf("%s dport %d accept", proto, port))
		}
	}
}

// emitAllowPhysicalDNS renders the opt-in plain-DNS pass (vpn.allowPhysicalDNS)
// so a VPN client can re-resolve its server hostname while the tunnel is down.
// Deliberately unscoped (`to any`): resolution must work regardless of which
// resolver the system uses on reconnect.
func emitAllowPhysicalDNS(rule func(string), p Policy) {
	if !p.AllowPhysicalDNS {
		return
	}
	rule("udp dport 53 accept")
	rule("tcp dport 53 accept")
}

// emitDaddrAccepts emits accept rules for addrs, split by family (nft needs
// `ip daddr` for v4 and `ip6 daddr` for v6) and suffixed with an optional match
// (e.g. "udp dport 53"). No rule is emitted for an empty family, so an empty
// allowlist produces no rules — never an invalid empty set `{ }`.
func emitDaddrAccepts(rule func(string), addrs []netip.Addr, suffix string) {
	v4, v6 := splitAddrFamilies(addrs)
	tail := ""
	if suffix != "" {
		tail = " " + suffix
	}
	if len(v4) > 0 {
		rule(fmt.Sprintf("ip daddr %s%s accept", nftAddrSet(v4), tail))
	}
	if len(v6) > 0 {
		rule(fmt.Sprintf("ip6 daddr %s%s accept", nftAddrSet(v6), tail))
	}
}

// splitAddrFamilies partitions addrs into IPv4 and IPv6, unmapping v4-in-v6.
func splitAddrFamilies(addrs []netip.Addr) (v4, v6 []netip.Addr) {
	for _, a := range addrs {
		a = a.Unmap()
		if a.Is4() {
			v4 = append(v4, a)
		} else if a.Is6() {
			v6 = append(v6, a)
		}
	}
	return v4, v6
}

// nftAddrSet renders addresses as an nft anonymous set: "{ 1.1.1.1, 8.8.8.8 }".
func nftAddrSet(addrs []netip.Addr) string {
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = a.String()
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// nftIfaceSet renders interface names as a quoted nft set: `{ "utun4", "utun5" }`.
func nftIfaceSet(ifaces []string) string {
	parts := make([]string, len(ifaces))
	for i, n := range ifaces {
		parts[i] = fmt.Sprintf("%q", n)
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}
