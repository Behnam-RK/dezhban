//go:build !darwin

package netdetect

import (
	"errors"
	"net/netip"
)

// ErrDiscoverUnsupported is returned by DiscoverEndpoints on platforms without a
// discovery implementation. Endpoint auto-discovery currently exists only on
// macOS, where the connected VPN's WAN transport is observable via netstat/scutil.
var ErrDiscoverUnsupported = errors.New("vpn endpoint auto-discovery is only supported on macOS")

// Candidate mirrors the darwin type so callers compile on every platform.
type Candidate struct {
	VPN    string
	Server netip.Addr
	Port   int
}

// DiscoverEndpoints is unsupported off macOS.
func DiscoverEndpoints() ([]Candidate, error) {
	return nil, ErrDiscoverUnsupported
}
