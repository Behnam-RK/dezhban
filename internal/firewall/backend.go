// Package firewall drives the OS firewall to cut and restore network egress.
//
// The platform-independent rest of dezhban talks only to the FirewallBackend
// interface; each OS provides one implementation selected by build tags. This
// is the seam that keeps ~90% of the code shared — see CLAUDE.md.
package firewall

import "net/netip"

// Allowlist names the destinations that must stay reachable while blocking, so
// recovery detection (geo-API lookups) keeps working and the machine cannot
// lock itself out. Loopback is always allowed implicitly by every backend.
type Allowlist struct {
	// DNS resolvers that must stay reachable so hostnames can be re-resolved.
	DNS []netip.Addr
	// Hosts are extra egress IPs to always allow (geo-API provider IPs).
	Hosts []netip.Addr
}

// Mode is the enforcement posture a Policy installs.
type Mode int

const (
	// ModeFullBlock cuts outbound egress except loopback. On a direct connection
	// (no tunnel interfaces) it additionally passes the dst-IP Allowlist. Under a
	// VPN it drops the tunnel-interface pass — so no user traffic egresses to a
	// forbidden exit — but KEEPS the VPN endpoint passes, so the encrypted
	// handshake still reaches the server and the tunnel can reconnect (a cut
	// endpoint would livelock recovery: the tunnel could never re-establish to be
	// re-evaluated). It is therefore ModeGuard minus the tunnel-interface pass.
	// The dst-IP Allowlist stays meaningless under a tunnel and is omitted.
	ModeFullBlock Mode = iota
	// ModeGuard is the always-on VPN guard: pass egress on the tunnel
	// interface(s) plus the handshake to the VPN endpoint(s), and block all
	// other outbound — so a tunnel drop cuts traffic with no leak window.
	ModeGuard
	// ModeSwitchWindow is a bounded, explicitly-triggered relaxation used when
	// connecting a brand-new VPN whose server address isn't known yet: by default
	// it passes ALL outbound (so any VPN's handshake to any server can complete),
	// relying on the daemon's short timer + early-close for safety rather than a
	// port filter (a filter that admits the VPNs this project targets — many on
	// 443 — necessarily admits phone-home leaks too; see docs/modes.md). When
	// WindowProtos/WindowPorts are set it instead passes loopback + tunnel +
	// endpoints + DNS + the given proto/port set. The daemon reverts to the prior
	// posture when the window closes.
	ModeSwitchWindow
)

// String returns the stable mode identifier used in logs; it matches the
// print-rules --mode names ("fullblock", "guard", "switch"). Note that a VPN
// full block and the legacy direct block are both ModeFullBlock.
func (m Mode) String() string {
	switch m {
	case ModeFullBlock:
		return "fullblock"
	case ModeGuard:
		return "guard"
	case ModeSwitchWindow:
		return "switch"
	default:
		return "unknown"
	}
}

// Policy describes one enforcement state for a backend to Apply. It generalizes
// the original dst-IP Block so the same backend can drive both the legacy direct
// model and the VPN-aware interface guard. See docs/plans VPN mode.
type Policy struct {
	// Mode selects the posture (ModeFullBlock or ModeGuard).
	Mode Mode
	// Allowlist is used in legacy ModeFullBlock (no tunnel) and during the
	// recovery probe; it is the DNS + geo-API egress IPs.
	Allowlist Allowlist
	// TunnelIfaces are the VPN tunnel interface names (e.g. "utun4"). Their
	// presence marks VPN mode even in ModeFullBlock.
	TunnelIfaces []string
	// VPNEndpoints are the VPN server IPs reachable on the physical interface,
	// kept open so the tunnel can stay up / reconnect.
	VPNEndpoints []netip.Addr
	// AllowPhysicalDNS adds a plain-DNS (port 53) egress pass to guard and VPN
	// full-block rulesets so a VPN client can re-resolve its server hostname
	// while the tunnel is down. Deliberately `to any`: resolution must work
	// regardless of which resolver the system uses on reconnect. The residual
	// leak is DNS-query metadata only, gated behind a default-off config flag.
	AllowPhysicalDNS bool
	// AllowLocalNetwork adds destination-scoped passes for private, link-local
	// and multicast ranges (see LocalNetworkPrefixes) to every enforcing posture,
	// so LAN devices stay reachable while the guard is armed. Destination-scoped,
	// never interface-scoped: it cannot become an internet path, because packets
	// to public addresses remain blocked whatever the next hop is.
	AllowLocalNetwork bool
	// TunnelGroups are tunnel-interface *class* names (e.g. "utun") rendered as a
	// pf interface group / nft wildcard ("utun*") so every current and future
	// interface of that class is passed in ModeGuard without a rule reload when a
	// new tunnel appears. Safe: a tunnel re-encapsulates onto the physical
	// interface, where egress is still blocked unless the destination is an
	// allowed endpoint. Windows (exact InterfaceAlias only) ignores this.
	TunnelGroups []string
	// WindowProtos / WindowPorts optionally restrict ModeSwitchWindow to the given
	// protocols ("udp"/"tcp") and destination ports instead of passing all
	// outbound. Empty (the default) = pass all outbound for the window.
	WindowProtos []string
	WindowPorts  []int
}

// isVPNPolicy reports whether a ModeFullBlock policy is a VPN posture (endpoints
// open) rather than the legacy direct model (dst-IP allowlist). True when the
// policy carries tunnel interfaces, endpoints, or the physical-DNS pass — the
// zero-tunnel standing posture (endpoints, no ifaces) still counts. Shared by
// the pf and nft renderers.
func isVPNPolicy(p Policy) bool {
	return len(p.TunnelIfaces) > 0 || len(p.VPNEndpoints) > 0 || p.AllowPhysicalDNS
}

// FirewallBackend is the per-OS firewall driver. Implementations must be
// idempotent and surgical: they touch only rules tagged "dezhban" and never
// disturb unrelated firewall state.
type FirewallBackend interface {
	// Apply installs the ruleset for the given Policy (full block or VPN guard).
	// Idempotent: re-applying the same or a different policy replaces the rules,
	// never stacks them.
	Apply(p Policy) error
	// Block installs a default-deny-outbound ruleset, passing only the
	// allowlist (plus loopback). Only outbound is filtered, so return traffic is
	// unaffected.
	// Re-blocking must not stack duplicate rules. Equivalent to Apply with
	// ModeFullBlock and no tunnel interfaces (the legacy direct model).
	Block(a Allowlist) error
	// Unblock removes ONLY dezhban's rules and restores prior firewall state.
	Unblock() error
	// IsBlocked reports whether dezhban's block is currently installed.
	IsBlocked() (bool, error)
	// Cleanup is an always-safe, best-effort teardown for shutdown/panic. It
	// never returns fatally; failures are the caller's to log.
	Cleanup() error
}
