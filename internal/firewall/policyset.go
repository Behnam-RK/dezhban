package firewall

import (
	"net/netip"
	"strings"
)

// PolicyInput is the raw material every dezhban posture is built from: the
// current tunnel and endpoint sets plus the knobs that shape them. The three
// constructors below turn it into the Policy for each posture.
//
// This type exists because the postures used to be built in TWO places — the run
// loop (runner.vpnPolicies / runner.windowPolicy) and print-rules
// (cmd/dezhban.policyForMode) — with a comment in the latter asking a future
// refactor to unify them. Two constructors for one concept is a correctness
// problem, not a tidiness one: print-rules exists to show the operator exactly
// what the daemon would install, so any drift between the two makes the preview
// lie. It had already drifted (print-rules dropped TunnelGroups entirely and
// tested only len(Tunnels) when degrading a guard), and only stayed harmless
// because nothing populates TunnelGroups yet.
//
// Anything that decides what a posture looks like belongs here, so both callers
// inherit it.
type PolicyInput struct {
	// Tunnels are the concrete VPN tunnel interface names (e.g. "utun4").
	Tunnels []string
	// TunnelGroups are tunnel-interface class names (e.g. "utun") rendered as an
	// interface group / wildcard, so a newly-appeared tunnel of that class is
	// passed with no rule reload.
	TunnelGroups []string
	// Endpoints are the VPN server addresses reachable on the physical interface.
	Endpoints []netip.Addr
	// AllowPhysicalDNS opens plain DNS on the physical link so a VPN client can
	// re-resolve its server hostname while the tunnel is down.
	AllowPhysicalDNS bool
	// AllowLocalNetwork keeps LAN destinations reachable while the guard is armed.
	AllowLocalNetwork bool
	// ProviderAddrs are the resolved geo-API provider IPs, passed tunnel-scoped
	// in FULL BLOCK so the exit-country lookup needs no guard lift.
	ProviderAddrs []netip.Addr
	// WindowProtos / WindowPorts optionally restrict the switch window instead of
	// passing all outbound.
	WindowProtos []string
	WindowPorts  []int
	// Allowlist is a dst-IP allowlist. A VPN posture opens endpoints rather than
	// a physical allowlist, so every current caller leaves this empty: the run
	// loop never sets it, and print-rules no longer does either now that
	// `--mode legacy` is gone.
	//
	// The field is kept rather than deleted because Policy.Allowlist is still
	// live — `dezhban block --force` builds a Policy directly with a resolved
	// geo-provider allowlist so recovery detection survives a manual hard block.
	// Keeping the seam here means that if --force is ever routed through
	// PolicyInput (which would be an improvement — it is the last posture that
	// bypasses the single constructor), it inherits canonAddrs' v4-in-v6
	// normalisation instead of re-learning that lesson.
	Allowlist Allowlist
}

// canonAddrs returns addrs with every IPv4-in-IPv6 address unmapped, dropping
// invalid entries. Every posture's addresses go through here.
//
// This is a LOCKOUT fix, and the failure mode is worth stating exactly, because
// it is silent rather than loud. pf does not reject `::ffff:1.2.3.4` — verified
// with `pfctl -nvf`, it accepts the rule and expands it to:
//
//	pass out quick inet6 from any to ::ffff:1.2.3.4 no state
//
// An *inet6* rule. Real IPv4 traffic to 1.2.3.4 never matches it, so the pass is
// effectively absent while looking perfectly present in `pfctl -sr`. If that
// address is a VPN endpoint, the tunnel's own handshake is blocked by the
// default-deny below it and the VPN can never connect — a lockout whose ruleset
// looks correct to anyone inspecting it.
//
// Reaching the renderer mapped is easy: `netip.AddrFromSlice` on a 16-byte
// `net.IP` yields the mapped form, and `netip.ParseAddr` preserves whatever text
// it was given (learned.json round-trips through both). Callers used to each
// remember `.Unmap()`; the learned-endpoint path did not, which is exactly how a
// per-caller convention fails. Normalising once here means no backend and no
// caller has to defend itself.
//
// Linux already unmapped inside its own renderer (splitAddrFamilies); doing it
// at the seam makes that defence redundant rather than load-bearing.
func canonAddrs(addrs []netip.Addr) []netip.Addr {
	if len(addrs) == 0 {
		return nil
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		if a.IsValid() {
			out = append(out, a.Unmap())
		}
	}
	return out
}

// canonAllowlist applies canonAddrs to both halves of an Allowlist.
func canonAllowlist(a Allowlist) Allowlist {
	return Allowlist{DNS: canonAddrs(a.DNS), Hosts: canonAddrs(a.Hosts)}
}

// CountInvalid reports how many of addrs canonAddrs would silently drop.
//
// Dropping is the right behaviour at the seam — the zero netip.Addr stringifies
// to "invalid IP", a ruleset pf genuinely rejects, so rendering one bad entry
// would fail to install ANY rules — but a silent drop is a bad failure mode of
// its own: if the dropped address is a VPN endpoint, the symptom is a tunnel
// that will not handshake, with nothing in the log connecting the two. Callers
// that hold a logger use this to say so.
func CountInvalid(addrs []netip.Addr) int {
	n := 0
	for _, a := range addrs {
		if !a.IsValid() {
			n++
		}
	}
	return n
}

// FullBlock is the cut posture: no tunnel-interface pass, so no user traffic can
// reach a forbidden exit — but the endpoint pass stays open so the encrypted
// handshake still reaches the server and the tunnel can reconnect. Cutting the
// endpoint too would livelock recovery.
//
// The dst-IP allowlist is meaningless against a tunnel's encrypted outer packets,
// which is why the run loop never sets it here.
func (in PolicyInput) FullBlock() Policy {
	return Policy{
		Mode:         ModeFullBlock,
		Allowlist:    canonAllowlist(in.Allowlist),
		TunnelIfaces: in.Tunnels,
		// Carried even though FULL BLOCK installs no tunnel pass: the geo-provider
		// rule below is scoped to the tunnel, and a config that names only a
		// tunnel GROUP (e.g. "utun") has no concrete iface to scope to. Dropping
		// the groups here made the backends' group-scoping branches unreachable,
		// silently degrading such a host to lift-and-probe — the leak this posture
		// exists to remove.
		TunnelGroups:      in.TunnelGroups,
		VPNEndpoints:      canonAddrs(in.Endpoints),
		AllowPhysicalDNS:  in.AllowPhysicalDNS,
		AllowLocalNetwork: in.AllowLocalNetwork,
		// Only FULL BLOCK carries these: ModeGuard already passes all tunnel
		// egress, so a tunnel-scoped provider rule there would be redundant.
		ProviderAddrs: canonAddrs(in.ProviderAddrs),
	}
}

// Guard is the standing posture: only the tunnel may carry traffic off this
// machine.
//
// With no tunnel to pass — neither a concrete interface nor a group — it degrades
// to the FullBlock shape instead. ModeGuard with an empty interface set is
// rejected at the backend seam (pf_darwin.go, nft_linux.go, wfp_windows.go all
// refuse it) because it would pass nothing at all: a total lockout wearing the
// name of the healthy posture. The endpoints-open FullBlock shape is physically
// fail-closed while still letting the daemon run before any VPN has connected.
func (in PolicyInput) Guard() Policy {
	if len(in.Tunnels) == 0 && len(in.TunnelGroups) == 0 {
		return in.FullBlock()
	}
	return Policy{
		Mode:              ModeGuard,
		Allowlist:         canonAllowlist(in.Allowlist),
		TunnelIfaces:      in.Tunnels,
		TunnelGroups:      in.TunnelGroups,
		VPNEndpoints:      canonAddrs(in.Endpoints),
		AllowPhysicalDNS:  in.AllowPhysicalDNS,
		AllowLocalNetwork: in.AllowLocalNetwork,
	}
}

// SwitchWindow is the bounded relaxation that lets a brand-new VPN's handshake
// complete. Unrestricted by default (all outbound), which is why the daemon —
// never this constructor — is responsible for bounding it in time.
//
// No Allowlist and no AllowPhysicalDNS: an unrestricted window already passes
// everything, and the restricted form renders its own DNS pass unconditionally.
func (in PolicyInput) SwitchWindow() Policy {
	return Policy{
		Mode:         ModeSwitchWindow,
		TunnelIfaces: in.Tunnels,
		TunnelGroups: in.TunnelGroups,
		VPNEndpoints: canonAddrs(in.Endpoints),
		WindowProtos: in.WindowProtos,
		WindowPorts:  in.WindowPorts,
		// Matters only for a RESTRICTED window: an unrestricted one already
		// passes all outbound. Carrying it means a restricted window does not
		// silently break the LAN that GUARD on either side of it keeps working.
		AllowLocalNetwork: in.AllowLocalNetwork,
	}
}

// LocalNetworkPrefixes are the destination ranges opened by AllowLocalNetwork.
// Shared by all three backends so "local network" means the same thing on every
// OS — a per-backend list would drift, and a range present on one platform but
// not another is the kind of difference nobody notices until a printer stops
// working on exactly one machine.
//
// Deliberately destination ranges, never an interface match. Passing "the LAN
// interface" would pass everything routed out of it, including the internet;
// passing these prefixes cannot, because a packet to a public address does not
// match them regardless of which interface carries it.
//
// What is here and why:
//
//   - RFC1918 (10/8, 172.16/12, 192.168/16) — ordinary private LANs.
//
//   - 100.64/10 (RFC6598, CGNAT) — the range Tailscale and many ISP routers use.
//
//   - 169.254/16 + fe80::/10 — link-local, incl. self-assigned addressing.
//
//   - fc00::/7 — IPv6 unique-local, the ULA equivalent of RFC1918.
//
//   - Multicast, which is what actually makes discovery work: mDNS/Bonjour
//     (224.0.0.251, ff02::fb) and SSDP (239.255.255.250) are how a Mac finds
//     printers, AirPlay targets and Chromecasts. Opening unicast private ranges
//     alone would leave devices "visible but undiscoverable", which reads as
//     broken.
//
//     Only the LOCALLY-SCOPED multicast ranges are here, not all of 224/4 and
//     ff00::/8. Multicast has globally-routable scopes — 232/8 (SSM), 233/8
//     (GLOP) and ff0e::/16 (global) are designed to cross the internet — and a
//     range that can leave the building has no place in a pass justified by
//     "this traffic never leaves the building". So:
//
//   - 224.0.0.0/24 — local network control block, incl. mDNS 224.0.0.251.
//
//   - 239.0.0.0/8  — administratively scoped (RFC2365), incl. SSDP
//     239.255.255.250.
//
//   - ff02::/16    — IPv6 link-local scope, incl. mDNS ff02::fb and SSDP
//     ff02::c.
//
//   - ff05::/16    — IPv6 site-local scope, incl. SSDP ff05::c.
//
// NOT here: 127/8 and ::1 (loopback is passed unconditionally by every posture,
// independent of this setting) and 0.0.0.0/8, which is a source-only range.
var LocalNetworkPrefixes = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"100.64.0.0/10",
	"169.254.0.0/16",
	"224.0.0.0/24",
	"239.0.0.0/8",
	"fc00::/7",
	"fe80::/10",
	"ff02::/16",
	"ff05::/16",
}

// LocalNetworkPrefixesFor returns the local-network prefixes of one address
// family. nft needs them split (its `ip daddr` / `ip6 daddr` matchers are
// per-family); pf infers the family per address and does not.
func LocalNetworkPrefixesFor(v6 bool) []string {
	out := make([]string, 0, len(LocalNetworkPrefixes))
	for _, p := range LocalNetworkPrefixes {
		if strings.Contains(p, ":") == v6 {
			out = append(out, p)
		}
	}
	return out
}
