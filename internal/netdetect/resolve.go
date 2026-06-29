package netdetect

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"time"
)

// hostLookupTimeout bounds a single hostname resolution, mirroring the provider
// resolution in cmd/dezhban buildAllowlist.
const hostLookupTimeout = 5 * time.Second

// LookupNetIPer resolves a host to IP addresses. *net.Resolver satisfies it;
// tests inject a fake so resolution needs no real DNS.
type LookupNetIPer interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// EndpointSet is the resolved set of VPN endpoint addresses kept reachable on the
// physical interface, plus where each came from (for monitor/doctor display).
// Addrs is deduped and sorted deterministically so callers can cheaply detect
// whether the set changed between refreshes.
type EndpointSet struct {
	Addrs   []netip.Addr
	Sources map[netip.Addr]string // "literal" | "dns:<host>" | "discovered"
}

// SameAddrs reports whether two sets hold the same addresses. Both are kept
// sorted by Resolve, so this is a straight element-wise compare.
func (s EndpointSet) SameAddrs(other EndpointSet) bool {
	if len(s.Addrs) != len(other.Addrs) {
		return false
	}
	for i := range s.Addrs {
		if s.Addrs[i] != other.Addrs[i] {
			return false
		}
	}
	return true
}

// EndpointSource resolves the live VPN endpoint set from three inputs, in
// trust order: configured IP literals (always included), configured hostnames
// (re-resolved each call), and live socket discovery (macOS). Tunnels, when set,
// lets Resolve drop any address that is tunnel-internal — a hole-punch / lockout
// risk that must never enter the guard's pass-list.
type EndpointSource struct {
	Literals  []netip.Addr
	Hostnames []string
	Tunnels   []string
	Resolver  LookupNetIPer                // nil → net.DefaultResolver
	Discover  func() ([]netip.Addr, error) // nil → no live discovery
	Log       *slog.Logger
}

// Resolve computes the current endpoint set as the union of literals, resolved
// hostnames and discovered addresses. It NEVER aborts on a partial failure: a
// hostname that won't resolve or a discovery error is logged and skipped, and
// whatever resolved stands. The caller decides what an empty result means (the
// runner refuses to start, or keeps the last-known-good set).
func (s *EndpointSource) Resolve(ctx context.Context) EndpointSet {
	set := EndpointSet{Sources: map[netip.Addr]string{}}
	add := func(a netip.Addr, src string) {
		a = a.Unmap()
		if !a.IsValid() {
			return
		}
		if _, ok := set.Sources[a]; ok {
			return // first (highest-trust) source wins
		}
		set.Sources[a] = src
		set.Addrs = append(set.Addrs, a)
	}

	for _, a := range s.Literals {
		add(a, "literal")
	}

	res := s.Resolver
	if res == nil {
		res = net.DefaultResolver
	}
	for _, h := range s.Hostnames {
		hctx, cancel := context.WithTimeout(ctx, hostLookupTimeout)
		ips, err := res.LookupNetIP(hctx, "ip", h)
		cancel()
		if err != nil {
			s.logWarn("vpn endpoint hostname resolution failed; skipping", "host", h, "err", err)
			continue
		}
		for _, ip := range ips {
			add(ip, "dns:"+h)
		}
	}

	if s.Discover != nil {
		ips, err := s.Discover()
		if err != nil {
			s.logDebug("vpn endpoint discovery unavailable", "err", err)
		} else {
			for _, ip := range ips {
				add(ip, "discovered")
			}
		}
	}

	// A tunnel-internal endpoint can never serve as the physical-side handshake
	// target — opening it punches a useless hole and the tunnel can't recover
	// through itself. Drop any such address before it reaches the guard.
	if len(s.Tunnels) > 0 && len(set.Addrs) > 0 {
		if bad, err := CheckEndpointRouting(set.Addrs, s.Tunnels); err == nil && len(bad) > 0 {
			drop := make(map[netip.Addr]bool, len(bad))
			for _, b := range bad {
				drop[b.Endpoint] = true
				s.logWarn("dropping tunnel-internal vpn endpoint", "endpoint", b.Endpoint, "iface", b.Iface, "subnet", b.Subnet)
			}
			kept := set.Addrs[:0:0]
			for _, a := range set.Addrs {
				if drop[a] {
					delete(set.Sources, a)
					continue
				}
				kept = append(kept, a)
			}
			set.Addrs = kept
		}
	}

	sort.Slice(set.Addrs, func(i, j int) bool { return set.Addrs[i].Less(set.Addrs[j]) })
	return set
}

func (s *EndpointSource) logWarn(msg string, args ...any) {
	if s.Log != nil {
		s.Log.Warn(msg, args...)
	}
}

func (s *EndpointSource) logDebug(msg string, args ...any) {
	if s.Log != nil {
		s.Log.Debug(msg, args...)
	}
}

// DiscoverEndpointsAddrs projects DiscoverEndpoints() candidates to their server
// addresses, for use as an EndpointSource.Discover hook. It is portable: on
// platforms without discovery, DiscoverEndpoints returns ErrDiscoverUnsupported,
// which Resolve treats as "no discovered endpoints" and skips.
func DiscoverEndpointsAddrs() ([]netip.Addr, error) {
	cands, err := DiscoverEndpoints()
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.Server)
	}
	return out, nil
}
