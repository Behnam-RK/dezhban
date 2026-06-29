package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissingPathReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %s, want 30s", cfg.PollInterval)
	}
	if !cfg.FailClosed {
		t.Error("FailClosed = false, want true (security default)")
	}
}

func TestLoadOverlaysAndNormalizes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	body := `{
		"pollInterval": "5s",
		"blockedCountries": ["ru", " ir "],
		"failClosed": false,
		"hysteresis": 2,
		"logLevel": "DEBUG"
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PollInterval != 5*time.Second {
		t.Errorf("PollInterval = %s, want 5s", cfg.PollInterval)
	}
	if cfg.FailClosed {
		t.Error("FailClosed = true, want false (explicitly set)")
	}
	if got := cfg.BlockedCountries; len(got) != 2 || got[0] != "RU" || got[1] != "IR" {
		t.Errorf("BlockedCountries = %v, want [RU IR] (upper-cased, trimmed)", got)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug (lower-cased)", cfg.LogLevel)
	}
}

func TestLoadVPNBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	body := `{
		"vpn": {
			"enabled": true,
			"tunnelInterfaces": [" utun4 "],
			"endpoints": ["203.0.113.5"],
			"autodetect": true
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.VPN.Enabled {
		t.Error("VPN.Enabled = false, want true")
	}
	if got := cfg.VPN.TunnelInterfaces; len(got) != 1 || got[0] != "utun4" {
		t.Errorf("VPN.TunnelInterfaces = %v, want [utun4] (trimmed)", got)
	}
	if got := cfg.VPN.Endpoints; len(got) != 1 || got[0] != "203.0.113.5" {
		t.Errorf("VPN.Endpoints = %v, want [203.0.113.5]", got)
	}
	if !cfg.VPN.Autodetect {
		t.Error("VPN.Autodetect = false, want true")
	}
}

func TestLoadVPNHostnamesAndDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	body := `{
		"vpn": {
			"enabled": true,
			"tunnelInterfaces": ["utun4"],
			"endpoints": ["vpn.example.com", "203.0.113.5"],
			"autoDiscoverEndpoints": true,
			"endpointRefresh": "2m",
			"tunnelWatch": "500ms"
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.VPN.Endpoints; len(got) != 2 || got[0] != "vpn.example.com" {
		t.Errorf("VPN.Endpoints = %v, want [vpn.example.com 203.0.113.5]", got)
	}
	if !cfg.VPN.AutoDiscoverEndpoints {
		t.Error("VPN.AutoDiscoverEndpoints = false, want true")
	}
	if cfg.VPN.EndpointRefresh != 2*time.Minute {
		t.Errorf("VPN.EndpointRefresh = %s, want 2m", cfg.VPN.EndpointRefresh)
	}
	if cfg.VPN.TunnelWatch != 500*time.Millisecond {
		t.Errorf("VPN.TunnelWatch = %s, want 500ms", cfg.VPN.TunnelWatch)
	}
}

func TestLoadVPNCadenceDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	body := `{"vpn": {"enabled": true, "tunnelInterfaces": ["utun4"], "endpoints": ["1.2.3.4"]}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.VPN.EndpointRefresh != 5*time.Minute {
		t.Errorf("VPN.EndpointRefresh = %s, want default 5m", cfg.VPN.EndpointRefresh)
	}
	if cfg.VPN.TunnelWatch != time.Second {
		t.Errorf("VPN.TunnelWatch = %s, want default 1s", cfg.VPN.TunnelWatch)
	}
}

// Auto-discovery with no hand-typed endpoint is a valid zero-config setup.
func TestLoadVPNAutoDiscoverNoEndpoints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	body := `{"vpn": {"enabled": true, "tunnelInterfaces": ["utun4"], "autoDiscoverEndpoints": true}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Errorf("Load: %v, want success (auto-discover replaces endpoints)", err)
	}
}

func TestClassifyTarget(t *testing.T) {
	cases := map[string]targetKind{
		"203.0.113.5":             kindIP,
		"2001:db8::1":             kindIP,
		"vpn.example.com":         kindHost,
		"nordlynx":                kindHost,
		"a-b.c-d.example":         kindHost,
		"1.example.com":           kindHost, // numeric non-final label is fine
		"":                        kindInvalid,
		"bad endpoint!":           kindInvalid,
		"-leading.example":        kindInvalid,
		"trailing-.example":       kindInvalid,
		"under_score.example.com": kindInvalid,
		"203.0.113":               kindInvalid, // truncated IP, not a hostname
		"999.999.999.999":         kindInvalid, // malformed IP masquerading as host
	}
	for in, want := range cases {
		if got := classifyTarget(in); got != want {
			t.Errorf("classifyTarget(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestValidateErrors(t *testing.T) {
	cases := map[string]string{
		"bad interval":     `{"pollInterval": "0s"}`,
		"bad hyst":         `{"hysteresis": 0}`,
		"bad country":      `{"blockedCountries": ["USA"]}`,
		"no providers":     `{"providers": []}`,
		"vpn no ifaces":    `{"vpn": {"enabled": true, "endpoints": ["1.2.3.4"]}}`,
		"vpn no endpoints": `{"vpn": {"enabled": true, "tunnelInterfaces": ["utun4"]}}`,
		"vpn bad endpoint": `{"vpn": {"enabled": true, "tunnelInterfaces": ["utun4"], "endpoints": ["bad endpoint!"]}}`,
		"vpn bad refresh":  `{"vpn": {"enabled": true, "tunnelInterfaces": ["utun4"], "endpoints": ["1.2.3.4"], "endpointRefresh": "soon"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cfg.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Errorf("Load(%s) = nil error, want validation error", body)
			}
		})
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("Load(invalid json) = nil error, want parse error")
	}
}
