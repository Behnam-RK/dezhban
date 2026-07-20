package firewall

import "net/netip"

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
	// WindowProtos / WindowPorts optionally restrict the switch window instead of
	// passing all outbound.
	WindowProtos []string
	WindowPorts  []int
	// Allowlist is the legacy direct model's dst-IP allowlist. A VPN posture opens
	// endpoints rather than a physical allowlist, so the run loop leaves this
	// empty; only print-rules populates it, and only for non-VPN configs.
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

// FullBlock is the cut posture: no tunnel-interface pass, so no user traffic can
// reach a forbidden exit — but the endpoint pass stays open so the encrypted
// handshake still reaches the server and the tunnel can reconnect. Cutting the
// endpoint too would livelock recovery.
//
// The dst-IP allowlist is meaningless against a tunnel's encrypted outer packets,
// which is why the run loop never sets it here.
func (in PolicyInput) FullBlock() Policy {
	return Policy{
		Mode:             ModeFullBlock,
		Allowlist:        canonAllowlist(in.Allowlist),
		TunnelIfaces:     in.Tunnels,
		VPNEndpoints:     canonAddrs(in.Endpoints),
		AllowPhysicalDNS: in.AllowPhysicalDNS,
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
		Mode:             ModeGuard,
		Allowlist:        canonAllowlist(in.Allowlist),
		TunnelIfaces:     in.Tunnels,
		TunnelGroups:     in.TunnelGroups,
		VPNEndpoints:     canonAddrs(in.Endpoints),
		AllowPhysicalDNS: in.AllowPhysicalDNS,
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
	}
}
