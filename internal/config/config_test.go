package config

import (
	"os"
	"path/filepath"
	"reflect"
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
		"bad interval":           `{"pollInterval": "0s"}`,
		"bad hyst":               `{"hysteresis": 0}`,
		"bad country":            `{"blockedCountries": ["USA"]}`,
		"no providers":           `{"providers": []}`,
		"vpn no endpoints":       `{"vpn": {"enabled": true, "tunnelInterfaces": ["utun4"]}}`,
		"vpn bad endpoint":       `{"vpn": {"enabled": true, "tunnelInterfaces": ["utun4"], "endpoints": ["bad endpoint!"]}}`,
		"vpn bad refresh":        `{"vpn": {"enabled": true, "tunnelInterfaces": ["utun4"], "endpoints": ["1.2.3.4"], "endpointRefresh": "soon"}}`,
		"profile dup name":       `{"vpn": {"enabled": true, "profiles": [{"name": "a", "endpoints": ["1.2.3.4"]}, {"name": "A", "endpoints": ["5.6.7.8"]}]}}`,
		"profile no endpoints":   `{"vpn": {"enabled": true, "profiles": [{"name": "a", "endpoints": []}]}}`,
		"profile bad name":       `{"vpn": {"enabled": true, "profiles": [{"name": "a b", "endpoints": ["1.2.3.4"]}]}}`,
		"profile bad endpoint":   `{"vpn": {"enabled": true, "profiles": [{"name": "a", "endpoints": ["bad ep!"]}]}}`,
		"switch window too low":  `{"vpn": {"enabled": true, "endpoints": ["1.2.3.4"], "switchWindow": "1s"}}`,
		"switch window too high": `{"vpn": {"enabled": true, "endpoints": ["1.2.3.4"], "switchWindow": "10m"}}`,
		"advanced bad proto":     `{"vpn": {"enabled": true, "endpoints": ["1.2.3.4"], "advanced": {"windowProtocols": ["icmp"]}}}`,
		"advanced bad port":      `{"vpn": {"enabled": true, "endpoints": ["1.2.3.4"], "advanced": {"windowPorts": [99999]}}}`,
		"advanced bad duration":  `{"vpn": {"enabled": true, "endpoints": ["1.2.3.4"], "advanced": {"commandFreshness": "soon"}}}`,
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

// Save then Load must reproduce the same validated Config. Both configs pass
// through the Load pipeline so nil/empty-slice representations match.
func TestSaveLoadRoundTrip(t *testing.T) {
	cases := map[string]string{
		"legacy": `{
			"pollInterval": "5s",
			"blockedCountries": ["RU", "IR"],
			"failClosed": false,
			"hysteresis": 2,
			"allowlist": {"dns": ["1.1.1.1"], "hosts": ["9.9.9.9"]},
			"providerQuorum": true,
			"logLevel": "debug"
		}`,
		"vpn": `{
			"vpn": {
				"enabled": true,
				"tunnelInterfaces": ["utun4"],
				"endpoints": ["vpn.example.com", "203.0.113.5"],
				"autoDiscoverEndpoints": true,
				"endpointRefresh": "2m",
				"tunnelWatch": "500ms"
			}
		}`,
		"profiles": `{
			"vpn": {
				"enabled": true,
				"autodetect": true,
				"autoDiscoverEndpoints": true,
				"switchWindow": "90s",
				"profiles": [
					{"name": "proton", "endpoints": ["nl.proton.me"]},
					{"name": "home-wg", "endpoints": ["203.0.113.7"], "ifaceHint": "wg"}
				]
			}
		}`,
		"advanced": `{
			"vpn": {
				"enabled": true,
				"endpoints": ["1.2.3.4"],
				"advanced": {
					"switchWindowMax": "3m",
					"commandFreshness": "15s",
					"learnedMaxPerProfile": 8,
					"windowProtocols": ["udp"],
					"windowPorts": [51820]
				}
			}
		}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			src := filepath.Join(t.TempDir(), "src.json")
			if err := os.WriteFile(src, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg1, err := Load(src)
			if err != nil {
				t.Fatalf("Load src: %v", err)
			}

			out := filepath.Join(t.TempDir(), "nested", "out.json")
			if err := Save(out, cfg1); err != nil {
				t.Fatalf("Save: %v", err)
			}
			cfg2, err := Load(out)
			if err != nil {
				t.Fatalf("Load saved: %v", err)
			}
			if !reflect.DeepEqual(cfg1, cfg2) {
				t.Errorf("round-trip mismatch:\n before = %+v\n after  = %+v", cfg1, cfg2)
			}
		})
	}
}

// A config saved with the guard disabled must keep its tunnel/endpoint fields —
// otherwise re-enabling the guard later would fail validation or lock the host
// out. Regression test for toFileConfig dropping the vpn block when !Enabled.
func TestSavePreservesVPNFieldsWhenDisabled(t *testing.T) {
	cfg := Default()
	cfg.VPN = VPN{
		Enabled:          false,
		TunnelInterfaces: []string{"utun4"},
		Endpoints:        []string{"203.0.113.5"},
		Autodetect:       true,
	}
	path := filepath.Join(t.TempDir(), "cfg.json")
	if err := Save(path, &cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.VPN.Enabled {
		t.Error("VPN.Enabled = true, want false")
	}
	if g := got.VPN.TunnelInterfaces; len(g) != 1 || g[0] != "utun4" {
		t.Errorf("VPN.TunnelInterfaces = %v, want [utun4]", g)
	}
	if g := got.VPN.Endpoints; len(g) != 1 || g[0] != "203.0.113.5" {
		t.Errorf("VPN.Endpoints = %v, want [203.0.113.5]", g)
	}
	if !got.VPN.Autodetect {
		t.Error("VPN.Autodetect = false, want true")
	}
}

// Save must canonicalize (upper-case, trim, de-dupe) blocked countries — that
// normalization lives in config.Normalize and runs on every write path, so
// callers (config set, the setup wizard) don't each re-implement it.
func TestSaveNormalizesCountries(t *testing.T) {
	cfg := Default()
	cfg.BlockedCountries = []string{" ir ", "IR", "ru"}
	path := filepath.Join(t.TempDir(), "cfg.json")
	if err := Save(path, &cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := []string{"IR", "RU"}; !reflect.DeepEqual(got.BlockedCountries, want) {
		t.Errorf("BlockedCountries = %v, want %v", got.BlockedCountries, want)
	}
}

// Save must reject an invalid Config rather than persist it.
func TestSaveValidates(t *testing.T) {
	bad := Default()
	bad.PollInterval = 0
	if err := Save(filepath.Join(t.TempDir(), "x.json"), &bad); err == nil {
		t.Error("Save(invalid) = nil error, want validation error")
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

func TestLoadVPNProfilesAndSwitchWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	body := `{
		"vpn": {
			"enabled": true,
			"autoDiscoverEndpoints": true,
			"switchWindow": "90s",
			"profiles": [
				{"name": "proton", "endpoints": [" nl.proton.me "]},
				{"name": "home-wg", "endpoints": ["203.0.113.7"], "ifaceHint": "wg"}
			]
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.VPN.Profiles) != 2 {
		t.Fatalf("profiles = %d, want 2", len(cfg.VPN.Profiles))
	}
	if got := cfg.VPN.Profiles[0].Endpoints[0]; got != "nl.proton.me" {
		t.Errorf("profile endpoint = %q, want trimmed nl.proton.me", got)
	}
	if cfg.VPN.Profiles[1].IfaceHint != "wg" {
		t.Errorf("ifaceHint = %q, want wg", cfg.VPN.Profiles[1].IfaceHint)
	}
	if cfg.VPN.SwitchWindow != 90*time.Second {
		t.Errorf("switchWindow = %s, want 90s", cfg.VPN.SwitchWindow)
	}
	// No explicit tunnelInterfaces → autodetect implied by Normalize.
	if !cfg.VPN.Autodetect {
		t.Error("autodetect should be implied when enabled with no tunnelInterfaces")
	}
}

// A config with no vpn.advanced block gets every knob defaulted; an explicit
// block overrides only the fields it sets.
func TestLoadVPNAdvancedDefaultsAndOverride(t *testing.T) {
	dir := t.TempDir()
	defPath := filepath.Join(dir, "def.json")
	if err := os.WriteFile(defPath, []byte(`{"vpn":{"enabled":true,"endpoints":["1.2.3.4"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(defPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.VPN.SwitchWindow != 2*time.Minute {
		t.Errorf("default switchWindow = %s, want 2m", cfg.VPN.SwitchWindow)
	}
	a := cfg.VPN.Advanced
	if a.SwitchWindowMax != 5*time.Minute || a.CommandFreshness != 30*time.Second ||
		a.LearnedMaxPerProfile != 16 || a.PromoteAfterRefreshes != 3 ||
		a.EndpointWarnThreshold != 256 || a.LearnedEndpointTTL != 720*time.Hour ||
		a.TunnelPruneAfter != 60*time.Second || a.WindowDiscoveryInterval != 2*time.Second {
		t.Errorf("advanced defaults not applied: %+v", a)
	}

	ovPath := filepath.Join(dir, "ov.json")
	body := `{"vpn":{"enabled":true,"endpoints":["1.2.3.4"],"advanced":{"commandFreshness":"15s","learnedMaxPerProfile":8}}}`
	if err := os.WriteFile(ovPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(ovPath)
	if err != nil {
		t.Fatalf("Load override: %v", err)
	}
	if cfg2.VPN.Advanced.CommandFreshness != 15*time.Second {
		t.Errorf("commandFreshness = %s, want 15s", cfg2.VPN.Advanced.CommandFreshness)
	}
	if cfg2.VPN.Advanced.LearnedMaxPerProfile != 8 {
		t.Errorf("learnedMaxPerProfile = %d, want 8", cfg2.VPN.Advanced.LearnedMaxPerProfile)
	}
	// Untouched knobs keep their defaults.
	if cfg2.VPN.Advanced.SwitchWindowMax != 5*time.Minute {
		t.Errorf("switchWindowMax = %s, want default 5m", cfg2.VPN.Advanced.SwitchWindowMax)
	}
}

func TestEffectiveEndpoints(t *testing.T) {
	cfg := Default()
	cfg.VPN.Endpoints = []string{"1.1.1.1", "dup.example.com"}
	cfg.VPN.Profiles = []Profile{
		{Name: "a", Endpoints: []string{"2.2.2.2", "dup.example.com"}},
		{Name: "b", Endpoints: []string{"3.3.3.3"}},
	}
	got := EffectiveEndpoints(&cfg, []string{"4.4.4.4", "1.1.1.1"})
	want := []string{"1.1.1.1", "dup.example.com", "2.2.2.2", "3.3.3.3", "4.4.4.4"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EffectiveEndpoints = %v, want %v", got, want)
	}
}

// A profiles-only config (no flat endpoints, no autoDiscover) is valid because
// the union has endpoints.
func TestValidateProfilesOnlyValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	body := `{"vpn":{"enabled":true,"profiles":[{"name":"a","endpoints":["1.2.3.4"]}]}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Errorf("Load profiles-only: %v, want success", err)
	}
}
