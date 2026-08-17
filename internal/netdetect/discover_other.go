//go:build !darwin

package netdetect

import (
	"context"
	"net/netip"
)

// Candidate mirrors the darwin type so callers compile on every platform.
type Candidate struct {
	VPN     string
	Server  netip.Addr
	Port    int
	Process string
}

// DiscoverEndpoints is unsupported off macOS.
func DiscoverEndpoints() ([]Candidate, error) {
	return nil, ErrDiscoverUnsupported
}

// SupportedVPNs mirrors the darwin export: no discovery, no attributable
// client patterns.
func SupportedVPNs() []string { return nil }

// ConnectedVPNName mirrors the darwin export; there is no scutil to ask.
func ConnectedVPNName(context.Context) string { return "" }
