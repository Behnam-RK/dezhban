// Package config defines dezhban's runtime configuration and loading.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"time"
)

// Allowlist names the destinations that must stay reachable even while blocking,
// so recovery detection (geo-API lookups) keeps working. Loopback is always
// allowed implicitly by the firewall backends.
type Allowlist struct {
	// DNS resolver IPs that must stay reachable for hostname re-resolution.
	DNS []string `json:"dns"`
	// Hosts is extra host IPs to always allow (provider IPs are added at runtime).
	Hosts []string `json:"hosts"`
}

// VPN configures the interface-aware kill-switch guard for hosts behind a
// full-tunnel VPN. When Enabled, enforcement uses the tunnel interface(s) and
// VPN endpoint(s) instead of the destination-IP allowlist (which is meaningless
// under a tunnel). See docs/plans VPN mode. Disabled by default — the always-on
// guard can lock a host out if misconfigured, so it is opt-in.
type VPN struct {
	// Enabled turns on VPN guard mode.
	Enabled bool
	// TunnelInterfaces are the VPN tunnel interface names (e.g. "utun4"). For now
	// these are concrete names; autodetect/pattern expansion lands with netdetect.
	TunnelInterfaces []string
	// Endpoints are the VPN server addresses reachable on the physical interface,
	// kept open so the tunnel can stay up and reconnect. Each entry may be an IP
	// literal or a hostname (resolved and re-resolved at runtime) — hostnames let
	// third-party VPNs that publish a server name rather than a fixed IP be used.
	Endpoints []string
	// Autodetect requests discovery of the tunnel interface (netdetect); explicit
	// TunnelInterfaces always win.
	Autodetect bool
	// AutoDiscoverEndpoints continuously learns the live VPN server IP from the
	// active socket (macOS only) and keeps that egress open, so a rotating-pool
	// VPN (NordVPN/ProtonVPN/…) needs no endpoint typed by hand. On other
	// platforms it is ignored and the host falls back to Endpoints.
	AutoDiscoverEndpoints bool
	// EndpointRefresh is how often hostnames are re-resolved and live discovery
	// re-run. Defaults to 5m.
	EndpointRefresh time.Duration
	// TunnelWatch is how often the tunnel interface(s) are sampled for up/down so
	// a drop cuts the network at once instead of waiting for the next geo poll.
	// Defaults to 1s.
	TunnelWatch time.Duration
}

// Config is dezhban's validated runtime configuration.
type Config struct {
	// PollInterval is how often the monitor checks the current country.
	PollInterval time.Duration
	// BlockedCountries are ISO-3166 alpha-2 codes that trigger a block.
	BlockedCountries []string
	// FailClosed blocks traffic when the country cannot be determined.
	FailClosed bool
	// Hysteresis is the consecutive agreeing readings required before toggling.
	Hysteresis int
	// Providers are geo-location endpoint URLs, tried for redundancy.
	Providers []string
	// Allowlist holds destinations kept reachable while blocking.
	Allowlist Allowlist
	// ProviderQuorum requires a majority of providers to agree on the country.
	ProviderQuorum bool
	// LogLevel is one of debug, info, warn, error.
	LogLevel string
	// VPN configures the interface-aware guard for full-tunnel VPN hosts.
	VPN VPN
}

// fileConfig is the on-disk JSON shape. Durations are strings (e.g. "30s")
// because JSON has no native duration type. Pointer fields distinguish
// "absent" (keep default) from a zero value the user set deliberately.
type fileConfig struct {
	PollInterval     string    `json:"pollInterval"`
	BlockedCountries []string  `json:"blockedCountries"`
	FailClosed       *bool     `json:"failClosed"`
	Hysteresis       *int      `json:"hysteresis"`
	Providers        []string  `json:"providers"`
	Allowlist        Allowlist `json:"allowlist"`
	ProviderQuorum   *bool     `json:"providerQuorum"`
	LogLevel         string    `json:"logLevel"`
	VPN              *fileVPN  `json:"vpn"`
}

// fileVPN is the on-disk shape of the VPN block. A pointer in fileConfig lets an
// absent block keep defaults.
type fileVPN struct {
	Enabled               bool     `json:"enabled"`
	TunnelInterfaces      []string `json:"tunnelInterfaces"`
	Endpoints             []string `json:"endpoints"`
	Autodetect            bool     `json:"autodetect"`
	AutoDiscoverEndpoints bool     `json:"autoDiscoverEndpoints"`
	EndpointRefresh       string   `json:"endpointRefresh"`
	TunnelWatch           string   `json:"tunnelWatch"`
}

// Default returns a Config with safe, security-first defaults.
func Default() Config {
	return Config{
		PollInterval:     30 * time.Second,
		BlockedCountries: nil,
		FailClosed:       true,
		Hysteresis:       3,
		Providers: []string{
			"https://ipinfo.io/json",
			"http://ip-api.com/json",
			"https://ifconfig.co/json",
		},
		Allowlist:      Allowlist{},
		ProviderQuorum: false,
		LogLevel:       "info",
	}
}

// Load reads config from path, layering it over defaults. A missing path is not
// an error: defaults are returned. The result is always validated.
func Load(path string) (*Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			// No file: defaults only.
		case err != nil:
			return nil, fmt.Errorf("read config %q: %w", path, err)
		default:
			var fc fileConfig
			if err := json.Unmarshal(data, &fc); err != nil {
				return nil, fmt.Errorf("parse config %q: %w", path, err)
			}
			if err := apply(&cfg, fc); err != nil {
				return nil, fmt.Errorf("config %q: %w", path, err)
			}
		}
	}

	normalize(&cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// apply overlays non-empty fields from fc onto cfg.
func apply(cfg *Config, fc fileConfig) error {
	if fc.PollInterval != "" {
		d, err := time.ParseDuration(fc.PollInterval)
		if err != nil {
			return fmt.Errorf("pollInterval: %w", err)
		}
		cfg.PollInterval = d
	}
	if fc.BlockedCountries != nil {
		cfg.BlockedCountries = fc.BlockedCountries
	}
	if fc.FailClosed != nil {
		cfg.FailClosed = *fc.FailClosed
	}
	if fc.Hysteresis != nil {
		cfg.Hysteresis = *fc.Hysteresis
	}
	if fc.Providers != nil {
		cfg.Providers = fc.Providers
	}
	if fc.Allowlist.DNS != nil || fc.Allowlist.Hosts != nil {
		cfg.Allowlist = fc.Allowlist
	}
	if fc.ProviderQuorum != nil {
		cfg.ProviderQuorum = *fc.ProviderQuorum
	}
	if fc.LogLevel != "" {
		cfg.LogLevel = fc.LogLevel
	}
	if fc.VPN != nil {
		v := VPN{
			Enabled:               fc.VPN.Enabled,
			TunnelInterfaces:      fc.VPN.TunnelInterfaces,
			Endpoints:             fc.VPN.Endpoints,
			Autodetect:            fc.VPN.Autodetect,
			AutoDiscoverEndpoints: fc.VPN.AutoDiscoverEndpoints,
		}
		if fc.VPN.EndpointRefresh != "" {
			d, err := time.ParseDuration(fc.VPN.EndpointRefresh)
			if err != nil {
				return fmt.Errorf("vpn.endpointRefresh: %w", err)
			}
			v.EndpointRefresh = d
		}
		if fc.VPN.TunnelWatch != "" {
			d, err := time.ParseDuration(fc.VPN.TunnelWatch)
			if err != nil {
				return fmt.Errorf("vpn.tunnelWatch: %w", err)
			}
			v.TunnelWatch = d
		}
		cfg.VPN = v
	}
	return nil
}

// normalize canonicalizes values (upper-case country codes, lower-case level).
func normalize(cfg *Config) {
	for i, c := range cfg.BlockedCountries {
		cfg.BlockedCountries[i] = strings.ToUpper(strings.TrimSpace(c))
	}
	cfg.LogLevel = strings.ToLower(strings.TrimSpace(cfg.LogLevel))
	for i, iface := range cfg.VPN.TunnelInterfaces {
		cfg.VPN.TunnelInterfaces[i] = strings.TrimSpace(iface)
	}
	for i, ep := range cfg.VPN.Endpoints {
		cfg.VPN.Endpoints[i] = strings.TrimSpace(ep)
	}
	// VPN guard cadence defaults. Set unconditionally — these are only read in
	// VPN mode, but defaulting here keeps Validate and the runner simple.
	if cfg.VPN.EndpointRefresh <= 0 {
		cfg.VPN.EndpointRefresh = 5 * time.Minute
	}
	if cfg.VPN.TunnelWatch <= 0 {
		cfg.VPN.TunnelWatch = 1 * time.Second
	}
}

// targetKind classifies an allowlist/endpoint entry.
type targetKind int

const (
	kindInvalid targetKind = iota
	kindIP
	kindHost
)

// classifyTarget reports whether s is an IP literal, a plausible DNS hostname,
// or neither. It performs NO DNS resolution — classification stays offline so
// validation is root-free and fast; real resolution happens later in the run
// path (internal/netdetect.EndpointSource).
func classifyTarget(s string) targetKind {
	s = strings.TrimSpace(s)
	if s == "" {
		return kindInvalid
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return kindIP
	}
	if isPlausibleHostname(s) {
		return kindHost
	}
	return kindInvalid
}

// isPlausibleHostname is a light, offline RFC-1123-ish syntax check: dotted
// labels of ASCII letters, digits and hyphens, each label 1..63 chars and not
// hyphen-bounded, total length <= 253. The final (top-level) label must not be
// all-numeric, which rejects truncated or malformed IPs (e.g. "203.0.113") that
// would otherwise masquerade as hostnames and pass validation but never resolve.
func isPlausibleHostname(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	labels := strings.Split(s, ".")
	for _, lab := range labels {
		if l := len(lab); l == 0 || l > 63 {
			return false
		}
		if lab[0] == '-' || lab[len(lab)-1] == '-' {
			return false
		}
		for i := 0; i < len(lab); i++ {
			c := lab[i]
			ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '-'
			if !ok {
				return false
			}
		}
	}
	// A real hostname's top-level label is never all digits; an all-numeric final
	// label means this is a malformed IP, not a host to resolve.
	if last := labels[len(labels)-1]; isAllDigits(last) {
		return false
	}
	return true
}

// isAllDigits reports whether s is non-empty and contains only ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Validate checks invariants the rest of the program relies on.
func (c *Config) Validate() error {
	if c.PollInterval <= 0 {
		return fmt.Errorf("pollInterval must be > 0, got %s", c.PollInterval)
	}
	if c.Hysteresis < 1 {
		return fmt.Errorf("hysteresis must be >= 1, got %d", c.Hysteresis)
	}
	if len(c.Providers) == 0 {
		return errors.New("at least one provider is required")
	}
	for _, code := range c.BlockedCountries {
		if len(code) != 2 {
			return fmt.Errorf("blocked country %q must be a 2-letter ISO-3166 code", code)
		}
	}
	if c.VPN.Enabled {
		// Guard mode needs tunnel interface(s): either explicit, or discovered at
		// runtime by netdetect when Autodetect is set. A wrong/empty set would lock
		// the host out, so fail loudly here rather than at block time. Endpoints are
		// always explicit — autodetecting them is unsafe (a wrong endpoint leaks),
		// so netdetect never supplies them.
		if len(c.VPN.TunnelInterfaces) == 0 && !c.VPN.Autodetect {
			return errors.New("vpn.enabled requires vpn.tunnelInterfaces or vpn.autodetect")
		}
		// At least one endpoint OR auto-discovery: a rotating-pool VPN may carry no
		// hand-typed endpoint and rely entirely on live discovery, but a guard with
		// no way to learn the server address can never let the tunnel reconnect.
		if len(c.VPN.Endpoints) == 0 && !c.VPN.AutoDiscoverEndpoints {
			return errors.New("vpn.enabled requires at least one vpn.endpoints entry or vpn.autoDiscoverEndpoints")
		}
		// Endpoints may be IP literals or hostnames; hostnames are resolved at
		// runtime. Reject only entries that are neither.
		for _, ep := range c.VPN.Endpoints {
			if classifyTarget(ep) == kindInvalid {
				return fmt.Errorf("vpn.endpoints: %q is neither an IP address nor a valid hostname", ep)
			}
		}
	}
	return nil
}
