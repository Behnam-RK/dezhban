package monitor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// The public-IPv6 lookup is deliberately much smaller than the country lookup
// it rides beside: a fixed endpoint list (not configurable — no key, no reload
// surface), sequential fallback, no quorum, and no country parsing. The result
// is observational only (state.Snapshot.IPv6); it never feeds a posture
// decision, so a failure here is nothing more than an empty field.
const ipv6LookupTimeout = 2 * time.Second

// ipv6Endpoints answer with the caller's address as plain text. Kept off the
// country-provider list on purpose: those are tried over whatever family the
// OS picks, and their answers feed decisions; these exist only to observe the
// v6 path.
var defaultIPv6Endpoints = []string{
	"https://api6.ipify.org",
	"https://v6.ident.me",
}

// newIPv6Client forces "tcp6" at the dialer. This is correctness, not
// preference: every endpoint above also resolves over A records, and a
// dual-stack host that happens to dial v4 would get its v4 address back —
// reported as v6 — with nothing failing loudly.
func newIPv6Client() *http.Client {
	d := &net.Dialer{Timeout: ipv6LookupTimeout}
	return &http.Client{
		Timeout: ipv6LookupTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return d.DialContext(ctx, "tcp6", addr)
			},
		},
	}
}

// OnceIPv6 resolves the machine's public IPv6 address, trying the fixed
// endpoints in order and returning the first valid answer. It returns an error
// when every endpoint fails — routine on v4-only hosts, and callers treat it as
// "no v6 to show", never as a fault worth surfacing.
func (m *Monitor) OnceIPv6(ctx context.Context) (netip.Addr, error) {
	// Read-only on the receiver, like every other Monitor method: New builds the
	// client (a zero-value Monitor from a test that skipped New falls back here
	// without mutating anything shared).
	client := m.v6client
	if client == nil {
		client = newIPv6Client()
	}
	endpoints := m.v6endpoints
	if len(endpoints) == 0 {
		endpoints = defaultIPv6Endpoints
	}
	var errs []error
	for _, url := range endpoints {
		pctx, cancel := context.WithTimeout(ctx, ipv6LookupTimeout)
		ip, err := fetchIPv6(pctx, client, url)
		cancel()
		if err != nil {
			m.log.Debug("ipv6 lookup failed", "endpoint", url, "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", url, err))
			continue
		}
		return ip, nil
	}
	return netip.Addr{}, fmt.Errorf("all ipv6 lookups failed: %w", errors.Join(errs...))
}

func fetchIPv6(ctx context.Context, client *http.Client, url string) (netip.Addr, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return netip.Addr{}, err
	}
	req.Header.Set("User-Agent", "dezhban")
	resp, err := client.Do(req)
	if err != nil {
		return netip.Addr{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return netip.Addr{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return netip.Addr{}, err
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(string(body)))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("parse ip: %w", err)
	}
	// The tcp6 dialer already guarantees the *path* was v6; this guards the
	// payload — an endpoint echoing something else must not land in the field.
	if !ip.Is6() || ip.Is4In6() {
		return netip.Addr{}, fmt.Errorf("endpoint returned a non-IPv6 address %s", ip)
	}
	// Is6 is true for ::1, fe80::/10 and fd00::/8 alike, so the family check
	// alone would let a captive portal or an intercepting proxy put a loopback,
	// link-local or ULA address into a field the app labels "Public IPv6".
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return netip.Addr{}, fmt.Errorf("endpoint returned a non-public IPv6 address %s", ip)
	}
	return ip, nil
}
