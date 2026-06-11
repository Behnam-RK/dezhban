// Package config defines dezhban's runtime configuration and loading.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
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
	return nil
}

// normalize canonicalizes values (upper-case country codes, lower-case level).
func normalize(cfg *Config) {
	for i, c := range cfg.BlockedCountries {
		cfg.BlockedCountries[i] = strings.ToUpper(strings.TrimSpace(c))
	}
	cfg.LogLevel = strings.ToLower(strings.TrimSpace(cfg.LogLevel))
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
	return nil
}
