// Package monitor resolves the machine's public IP and its country, on a
// polling loop, with multi-provider redundancy.
package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
)

// maxBody caps how much of a provider response we read, as a cheap guard
// against a misbehaving or hostile endpoint.
const maxBody = 64 << 10

// Reading is a single resolved public-IP/country observation.
type Reading struct {
	IP          netip.Addr
	CountryCode string // ISO-3166 alpha-2, upper-case
	Provider    string // which provider answered (for logs)
}

// GeoProvider resolves the public IP and country from one endpoint.
type GeoProvider interface {
	Name() string
	URL() string
	Lookup(ctx context.Context, client *http.Client) (Reading, error)
}

// parseFunc extracts (ip, alpha-2 country) from a provider's response body.
type parseFunc func([]byte) (netip.Addr, string, error)

// provider is the shared HTTP implementation; behavior differs only in URL and
// how the JSON body is parsed.
type provider struct {
	name  string
	url   string
	parse parseFunc
}

func (p *provider) Name() string { return p.name }
func (p *provider) URL() string  { return p.url }

func (p *provider) Lookup(ctx context.Context, client *http.Client) (Reading, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return Reading{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dezhban")

	resp, err := client.Do(req)
	if err != nil {
		return Reading{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Reading{}, fmt.Errorf("status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Reading{}, err
	}
	ip, cc, err := p.parse(body)
	if err != nil {
		return Reading{}, err
	}
	cc = strings.ToUpper(strings.TrimSpace(cc))
	if len(cc) != 2 {
		return Reading{}, fmt.Errorf("provider returned invalid country %q", cc)
	}
	if !ip.IsValid() {
		return Reading{}, fmt.Errorf("provider returned invalid ip")
	}
	return Reading{IP: ip, CountryCode: cc, Provider: p.name}, nil
}

// --- Per-endpoint parsers ---

func parseIPInfo(b []byte) (netip.Addr, string, error) {
	var v struct {
		IP      string `json:"ip"`
		Country string `json:"country"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return netip.Addr{}, "", err
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(v.IP))
	if err != nil {
		return netip.Addr{}, "", fmt.Errorf("parse ip: %w", err)
	}
	return ip, v.Country, nil
}

func parseIPAPI(b []byte) (netip.Addr, string, error) {
	var v struct {
		Status      string `json:"status"`
		Query       string `json:"query"`
		CountryCode string `json:"countryCode"`
		Message     string `json:"message"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return netip.Addr{}, "", err
	}
	// ip-api.com always includes a status field; require it to be "success" so
	// an ambiguous/malformed response (e.g. empty body) fails closed.
	if v.Status != "success" {
		return netip.Addr{}, "", fmt.Errorf("provider status %q: %s", v.Status, v.Message)
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(v.Query))
	if err != nil {
		return netip.Addr{}, "", fmt.Errorf("parse ip: %w", err)
	}
	return ip, v.CountryCode, nil
}

func parseIfconfig(b []byte) (netip.Addr, string, error) {
	var v struct {
		IP         string `json:"ip"`
		CountryISO string `json:"country_iso"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return netip.Addr{}, "", err
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(v.IP))
	if err != nil {
		return netip.Addr{}, "", fmt.Errorf("parse ip: %w", err)
	}
	return ip, v.CountryISO, nil
}

// knownProviders maps a configured URL to its parser. New endpoints are added
// here; URLs not present are skipped with a warning (see ProvidersFromURLs).
func knownProviders() map[string]parseFunc {
	return map[string]parseFunc{
		"https://ipinfo.io/json":   parseIPInfo,
		"http://ip-api.com/json":   parseIPAPI,
		"https://ifconfig.co/json": parseIfconfig,
	}
}

// nameForURL derives a short, log-friendly provider name from its URL host.
func nameForURL(url string) string {
	s := url
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return s
}

// ProvidersFromURLs builds GeoProviders from configured URLs, matching each to a
// known parser. Unknown URLs are skipped with a warning.
func ProvidersFromURLs(urls []string, log *slog.Logger) []GeoProvider {
	known := knownProviders()
	out := make([]GeoProvider, 0, len(urls))
	for _, u := range urls {
		parse, ok := known[u]
		if !ok {
			log.Warn("unknown geo provider url, skipping", "url", u)
			continue
		}
		out = append(out, &provider{name: nameForURL(u), url: u, parse: parse})
	}
	return out
}
