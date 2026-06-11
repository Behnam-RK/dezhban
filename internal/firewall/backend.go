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

// FirewallBackend is the per-OS firewall driver. Implementations must be
// idempotent and surgical: they touch only rules tagged "dezhban" and never
// disturb unrelated firewall state.
type FirewallBackend interface {
	// Block installs a default-deny-outbound ruleset, passing only the
	// allowlist (plus loopback and state for allowed return traffic).
	// Re-blocking must not stack duplicate rules.
	Block(a Allowlist) error
	// Unblock removes ONLY dezhban's rules and restores prior firewall state.
	Unblock() error
	// IsBlocked reports whether dezhban's block is currently installed.
	IsBlocked() (bool, error)
	// Cleanup is an always-safe, best-effort teardown for shutdown/panic. It
	// never returns fatally; failures are the caller's to log.
	Cleanup() error
}
