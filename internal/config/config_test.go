package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadMissingPathReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PollInterval != 15*time.Second {
		t.Errorf("PollInterval = %s, want 15s", cfg.PollInterval)
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
	// failClosed is retired: accepted without error, has no effect on runtime
	// Config, and is reported so an operator isn't left thinking it did something.
	if len(cfg.Retired) != 1 || cfg.Retired[0].Key != "failClosed" {
		t.Errorf("Retired = %v, want exactly one entry for failClosed", cfg.Retired)
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
	if cfg.VPN.EndpointRefresh != time.Minute {
		t.Errorf("VPN.EndpointRefresh = %s, want default 1m", cfg.VPN.EndpointRefresh)
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
		"bad interval":         `{"pollInterval": "0s"}`,
		"bad hyst":             `{"hysteresis": 0}`,
		"bad country":          `{"blockedCountries": ["USA"]}`,
		"no providers":         `{"providers": []}`,
		"vpn bad endpoint":     `{"vpn": {"enabled": true, "tunnelInterfaces": ["utun4"], "endpoints": ["bad endpoint!"]}}`,
		"vpn bad refresh":      `{"vpn": {"enabled": true, "tunnelInterfaces": ["utun4"], "endpoints": ["1.2.3.4"], "endpointRefresh": "soon"}}`,
		"profile dup name":     `{"vpn": {"enabled": true, "profiles": [{"name": "a", "endpoints": ["1.2.3.4"]}, {"name": "A", "endpoints": ["5.6.7.8"]}]}}`,
		"profile no endpoints": `{"vpn": {"enabled": true, "profiles": [{"name": "a", "endpoints": []}]}}`,
		"profile bad name":     `{"vpn": {"enabled": true, "profiles": [{"name": "a b", "endpoints": ["1.2.3.4"]}]}}`,
		"profile bad endpoint": `{"vpn": {"enabled": true, "profiles": [{"name": "a", "endpoints": ["bad ep!"]}]}}`,
		// No floor any more (2026-07-22 defaults review: "1s" now validates) —
		// negative is still rejected, just via the explicit apply()-time check
		// rather than a range floor.
		"switch window negative": `{"vpn": {"enabled": true, "endpoints": ["1.2.3.4"], "switchWindow": "-1s"}}`,
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
			// Retired describes the FILE that was read, not the configuration
			// itself: cfg1 comes from a fixture that may still carry a retired key,
			// while Save never writes one. Comparing it would assert that a
			// migration note survives a round trip, which is exactly what must not
			// happen.
			cfg1.Retired, cfg2.Retired = nil, nil
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
	if cfg.VPN.SwitchWindow != 5*time.Second {
		t.Errorf("default switchWindow = %s, want 5s", cfg.VPN.SwitchWindow)
	}
	a := cfg.VPN.Advanced
	if a.SwitchWindowMax != 3*time.Minute || a.RedialWindowMax != 10*time.Minute ||
		a.CommandFreshness != 30*time.Second ||
		a.LearnedMaxPerProfile != 16 || a.PromoteAfterRefreshes != 3 ||
		a.EndpointWarnThreshold != 256 || a.LearnedEndpointTTL != 720*time.Hour ||
		a.TunnelPruneAfter != 60*time.Second || a.WindowDiscoveryInterval != 1*time.Second {
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
	if cfg2.VPN.Advanced.SwitchWindowMax != 3*time.Minute {
		t.Errorf("switchWindowMax = %s, want default 3m", cfg2.VPN.Advanced.SwitchWindowMax)
	}
	if cfg2.VPN.Advanced.RedialWindowMax != 10*time.Minute {
		t.Errorf("redialWindowMax = %s, want default 10m", cfg2.VPN.Advanced.RedialWindowMax)
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

// An invalid switchWindowMax must surface as its own direct error, not a
// confusing derived switchWindow range: the advanced block is validated before
// switchWindow is bounded against it.
func TestValidateAdvancedBeforeSwitchWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	body := `{"vpn":{"enabled":true,"endpoints":["1.2.3.4"],"switchWindow":"2m","advanced":{"switchWindowMax":"5s"}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load = nil error, want validation error")
	}
	if !strings.Contains(err.Error(), "switchWindowMax") {
		t.Errorf("error = %q, want it to name switchWindowMax directly", err)
	}
}

// Normalize must canonicalize windowProtocols (trim + lowercase) so the pf/nft/WFP
// renderers, which emit the strings verbatim, never receive " UDP" or "Tcp".
func TestNormalizeWindowProtocols(t *testing.T) {
	cfg := Default()
	cfg.VPN.Advanced.WindowProtocols = []string{"  UDP", "Tcp"}
	Normalize(&cfg)

	want := []string{"udp", "tcp"}
	if got := cfg.VPN.Advanced.WindowProtocols; !reflect.DeepEqual(got, want) {
		t.Fatalf("WindowProtocols = %v, want %v", got, want)
	}
	if err := validateAdvanced(cfg.VPN.Advanced); err != nil {
		t.Errorf("validateAdvanced after normalize: %v, want success", err)
	}
}

// --- automatic redial window config ---

func TestRedialWindowDefaultsAndDisable(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Absent → default 30s.
	cfg, err := Load(write("default.json", `{"vpn": {"enabled": true, "tunnelInterfaces": ["utun4"], "endpoints": ["1.2.3.4"]}}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.VPN.RedialWindow != defaultRedialWindow {
		t.Errorf("RedialWindow = %s, want default %s", cfg.VPN.RedialWindow, defaultRedialWindow)
	}
	if cfg.VPN.Advanced.RedialMinUptime != defaultRedialMinUptime {
		t.Errorf("RedialMinUptime = %s, want default %s", cfg.VPN.Advanced.RedialMinUptime, defaultRedialMinUptime)
	}

	// Explicit "0" → disabled (negative sentinel), and it must survive Normalize.
	cfg, err = Load(write("off.json", `{"vpn": {"enabled": true, "tunnelInterfaces": ["utun4"], "endpoints": ["1.2.3.4"], "redialWindow": "0s"}}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.VPN.RedialWindow >= 0 {
		t.Errorf("RedialWindow = %s after explicit \"0s\", want the Disabled sentinel (<0)", cfg.VPN.RedialWindow)
	}

	// The disabled state must round-trip through Marshal/Load.
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"redialWindow": "0s"`) {
		t.Errorf("Marshal did not emit redialWindow \"0s\":\n%s", data)
	}
	cfg2, err := Load(write("off-roundtrip.json", string(data)))
	if err != nil {
		t.Fatalf("Load(round-trip): %v", err)
	}
	if cfg2.VPN.RedialWindow >= 0 {
		t.Errorf("disabled redialWindow did not survive a save/load round-trip: %s", cfg2.VPN.RedialWindow)
	}

	// No floor any more: "3s" now validates. Above redialWindowMax (10m
	// default, separate from switchWindowMax) must still fail.
	for _, bad := range []string{`"11m"`} {
		_, err := Load(write("bad.json", `{"vpn": {"enabled": true, "tunnelInterfaces": ["utun4"], "endpoints": ["1.2.3.4"], "redialWindow": `+bad+`}}`))
		if err == nil || !strings.Contains(err.Error(), "redialWindow") {
			t.Errorf("redialWindow %s: err = %v, want out-of-range error", bad, err)
		}
	}
}

// endpointGrace and autoArm must survive a save/load round-trip — a saved
// config silently dropping them is how the GUI "reset to zero" bug happened.
func TestSavePreservesEndpointGraceAndAutoArm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	body := `{"vpn": {"enabled": true, "tunnelInterfaces": ["utun4"], "endpoints": ["1.2.3.4"], "endpointGrace": "42m", "autoArm": true}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	out := filepath.Join(t.TempDir(), "out.json")
	if err := Save(out, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cfg2, err := Load(out)
	if err != nil {
		t.Fatalf("Load(saved): %v", err)
	}
	if cfg2.VPN.EndpointGrace != 42*time.Minute {
		t.Errorf("EndpointGrace = %s after round-trip, want 42m", cfg2.VPN.EndpointGrace)
	}
	if !cfg2.VPN.AutoArm {
		t.Error("AutoArm = false after round-trip, want true")
	}
}

// vpn.pauseMax defaults to 30m when absent, and "0" must survive as the
// Disabled sentinel through Normalize and a save/load round-trip — the same
// class of bug CLAUDE.md calls out for switchWindow/redialWindow: a "0"
// silently coerced back to a default is a security setting accepted, discarded,
// and never reported.
func TestPauseMaxDefaultAndDisableSentinel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	body := `{"vpn": {"tunnelInterfaces": ["utun4"], "endpoints": ["1.2.3.4"]}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.VPN.PauseMax != 30*time.Minute {
		t.Errorf("PauseMax = %s with the key absent, want the 30m default", cfg.VPN.PauseMax)
	}

	path2 := filepath.Join(t.TempDir(), "cfg2.json")
	body2 := `{"vpn": {"tunnelInterfaces": ["utun4"], "endpoints": ["1.2.3.4"], "pauseMax": "0"}}`
	if err := os.WriteFile(path2, []byte(body2), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(path2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg2.VPN.PauseMax >= 0 {
		t.Errorf("PauseMax = %s after \"0\", want the negative Disabled sentinel", cfg2.VPN.PauseMax)
	}
	out := filepath.Join(t.TempDir(), "out.json")
	if err := Save(out, cfg2); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cfg3, err := Load(out)
	if err != nil {
		t.Fatalf("Load(saved): %v", err)
	}
	if cfg3.VPN.PauseMax >= 0 {
		t.Errorf("PauseMax = %s after round-trip, want the disabled sentinel to survive", cfg3.VPN.PauseMax)
	}
}

// armAtBoot defaults to true when absent (Default() and an absent vpn.armAtBoot
// key must agree — see docs/adr/0008-arm-at-boot.md), and an explicit false
// must survive a save/load round-trip rather than being silently reset.
func TestArmAtBootDefaultTrueAndRoundTrips(t *testing.T) {
	if !Default().VPN.ArmAtBoot {
		t.Error("Default().VPN.ArmAtBoot = false, want true")
	}

	path := filepath.Join(t.TempDir(), "cfg.json")
	body := `{"vpn": {"tunnelInterfaces": ["utun4"], "endpoints": ["1.2.3.4"]}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.VPN.ArmAtBoot {
		t.Error("ArmAtBoot = false with the key absent, want true (default)")
	}

	path2 := filepath.Join(t.TempDir(), "cfg2.json")
	body2 := `{"vpn": {"tunnelInterfaces": ["utun4"], "endpoints": ["1.2.3.4"], "armAtBoot": false}}`
	if err := os.WriteFile(path2, []byte(body2), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(path2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg2.VPN.ArmAtBoot {
		t.Error("ArmAtBoot = true with an explicit false in the file, want false")
	}
	out := filepath.Join(t.TempDir(), "out.json")
	if err := Save(out, cfg2); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cfg3, err := Load(out)
	if err != nil {
		t.Fatalf("Load(saved): %v", err)
	}
	if cfg3.VPN.ArmAtBoot {
		t.Error("ArmAtBoot = true after round-trip, want the explicit false to survive")
	}
}

// An absent endpointGrace now normalizes to the effective 15m default so
// observers (GUI, config show) see the real value instead of 0.
func TestEndpointGraceDefaultVisible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	body := `{"vpn": {"enabled": true, "tunnelInterfaces": ["utun4"], "endpoints": ["1.2.3.4"]}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.VPN.EndpointGrace != defaultEndpointGrace {
		t.Errorf("EndpointGrace = %s, want normalized default %s", cfg.VPN.EndpointGrace, defaultEndpointGrace)
	}
}

// Both windows must be independently disableable with "0". switchWindow could
// not be disabled at all before the single-mode merge: Normalize coerced any
// value <= 0 back to the default, so an operator asking for a strictly
// zero-leak posture was silently overridden. redialWindow already had the
// sentinel; this asserts the pair now behaves the same and that disabling one
// never disables the other.
func TestWindowDisableMatrix(t *testing.T) {
	cases := []struct {
		name                      string
		body                      string
		wantSwitchOff, wantRecOff bool
	}{
		{"both default", `{"vpn":{"endpoints":["1.2.3.4"]}}`, false, false},
		{"switch off only", `{"vpn":{"endpoints":["1.2.3.4"],"switchWindow":"0"}}`, true, false},
		{"redial off only", `{"vpn":{"endpoints":["1.2.3.4"],"redialWindow":"0"}}`, false, true},
		{"both off", `{"vpn":{"endpoints":["1.2.3.4"],"switchWindow":"0","redialWindow":"0"}}`, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cfg.json")
			if err := os.WriteFile(path, []byte(c.body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			// The runner gates both features on `> 0`, so "disabled" must survive
			// Normalize as a non-positive value rather than being defaulted back.
			if off := cfg.VPN.SwitchWindow <= 0; off != c.wantSwitchOff {
				t.Errorf("switchWindow disabled = %v, want %v (got %s)", off, c.wantSwitchOff, cfg.VPN.SwitchWindow)
			}
			if off := cfg.VPN.RedialWindow <= 0; off != c.wantRecOff {
				t.Errorf("redialWindow disabled = %v, want %v (got %s)", off, c.wantRecOff, cfg.VPN.RedialWindow)
			}
		})
	}
}

// No floor any more (2026-07-22 defaults review): switchWindow/redialWindow
// values well under the old 10s/5s floors must validate, right up to their
// (now independent) caps.
func TestWindowNoFloor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	body := `{"vpn":{"endpoints":["1.2.3.4"],"switchWindow":"3s","redialWindow":"1s"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v, want a sub-10s switchWindow and sub-5s redialWindow to validate", err)
	}
	if cfg.VPN.SwitchWindow != 3*time.Second {
		t.Errorf("SwitchWindow = %s, want 3s", cfg.VPN.SwitchWindow)
	}
	if cfg.VPN.RedialWindow != 1*time.Second {
		t.Errorf("RedialWindow = %s, want 1s", cfg.VPN.RedialWindow)
	}
}

// Absent blockedCountries gets the recommended default; an explicit empty list
// is a deliberate "block nothing" and must never be overridden. Both must
// survive a save/load round-trip.
func TestBlockedCountriesDefaultVsExplicitEmpty(t *testing.T) {
	dir := t.TempDir()

	absent := filepath.Join(dir, "absent.json")
	if err := os.WriteFile(absent, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(absent)
	if err != nil {
		t.Fatalf("Load(absent): %v", err)
	}
	if want := []string{"IR", "RU", "KP"}; !reflect.DeepEqual(cfg.BlockedCountries, want) {
		t.Errorf("BlockedCountries (absent key) = %v, want %v", cfg.BlockedCountries, want)
	}

	explicit := filepath.Join(dir, "explicit-empty.json")
	if err := os.WriteFile(explicit, []byte(`{"blockedCountries": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(explicit)
	if err != nil {
		t.Fatalf("Load(explicit empty): %v", err)
	}
	if len(cfg2.BlockedCountries) != 0 {
		t.Errorf("BlockedCountries (explicit []) = %v, want empty — an explicit choice must never be overridden", cfg2.BlockedCountries)
	}

	// The explicit-empty choice must survive a save/load round-trip too.
	out := filepath.Join(dir, "explicit-empty-out.json")
	if err := Save(out, cfg2); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cfg3, err := Load(out)
	if err != nil {
		t.Fatalf("Load(round-trip): %v", err)
	}
	if len(cfg3.BlockedCountries) != 0 {
		t.Errorf("BlockedCountries after round-trip = %v, want still empty", cfg3.BlockedCountries)
	}
}

// A disabled switchWindow must round-trip as "0", not as the default it would
// have been coerced to.
func TestDisabledSwitchWindowRoundTrips(t *testing.T) {
	src := filepath.Join(t.TempDir(), "in.json")
	if err := os.WriteFile(src, []byte(`{"vpn":{"endpoints":["1.2.3.4"],"switchWindow":"0"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(src)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	out := filepath.Join(t.TempDir(), "out.json")
	if err := Save(out, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	again, err := Load(out)
	if err != nil {
		t.Fatalf("Load saved: %v", err)
	}
	if again.VPN.SwitchWindow > 0 {
		t.Errorf("switchWindow came back enabled (%s) after a round trip", again.VPN.SwitchWindow)
	}
}

// A pre-merge config must load, enforce identically, and say exactly which of
// its keys stopped meaning anything — without the loader rewriting the user's
// file behind their back. Silently accepting a discarded security setting is
// the failure mode this whole mechanism exists to prevent.
func TestLegacyConfigMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.json")
	body := `{
		"pollInterval": "30s",
		"blockedCountries": ["IR"],
		"failClosed": true,
		"hysteresis": 3,
		"allowlist": {"dns": ["1.1.1.1"], "hosts": ["9.9.9.9"]},
		"vpn": {
			"enabled": true,
			"tunnelInterfaces": ["utun4"],
			"endpoints": ["203.0.113.9"]
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Every retired key is reported, exactly once, with a reason.
	want := map[string]bool{"failClosed": true, "allowlist": true, "vpn.enabled": true}
	got := map[string]bool{}
	for _, r := range cfg.Retired {
		if got[r.Key] {
			t.Errorf("Retired reports %q more than once", r.Key)
		}
		got[r.Key] = true
		if r.Reason == "" {
			t.Errorf("Retired key %q has no reason — the operator learns nothing", r.Key)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Retired keys = %v, want %v", got, want)
	}

	// The surviving settings are untouched: migration drops, it does not reinterpret.
	if cfg.PollInterval != 30*time.Second || cfg.Hysteresis != 3 {
		t.Errorf("carried-over settings changed: poll=%s hysteresis=%d", cfg.PollInterval, cfg.Hysteresis)
	}
	if g := cfg.BlockedCountries; len(g) != 1 || g[0] != "IR" {
		t.Errorf("BlockedCountries = %v, want [IR]", g)
	}
	if g := cfg.VPN.TunnelInterfaces; len(g) != 1 || g[0] != "utun4" {
		t.Errorf("VPN.TunnelInterfaces = %v, want [utun4]", g)
	}
	if g := cfg.VPN.Endpoints; len(g) != 1 || g[0] != "203.0.113.9" {
		t.Errorf("VPN.Endpoints = %v, want [203.0.113.9]", g)
	}

	// Load must NOT rewrite the user's file — migration is reported, not applied
	// in place. `dezhban config migrate` is the explicit, opt-in writer.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != body {
		t.Error("Load rewrote the config file; migration must be reported, not applied silently")
	}

	// Saving explicitly drops the retired keys rather than echoing them back.
	out := filepath.Join(t.TempDir(), "out.json")
	if err := Save(out, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	saved, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, dead := range []string{"failClosed", "allowlist"} {
		if strings.Contains(string(saved), dead) {
			t.Errorf("Save wrote retired key %s back to disk:\n%s", dead, saved)
		}
	}
	// vpn.enabled is checked via reload rather than by substring: "enabled" is
	// also a live key under the control block, so a raw text match would be a
	// false positive.
	//
	// And the saved file is clean on reload: nothing left to report.
	again, err := Load(out)
	if err != nil {
		t.Fatalf("Load saved: %v", err)
	}
	if len(again.Retired) != 0 {
		t.Errorf("saved config still reports retired keys: %v", again.Retired)
	}
}

// allowLocalNetwork defaults ON, and an explicit false must survive — the same
// tri-state the other posture booleans use. Getting this wrong in the "absent"
// direction silently breaks every LAN device the moment the guard arms; getting
// it wrong in the "explicit false" direction silently re-opens the LAN for
// someone who deliberately closed it on an untrusted network.
func TestAllowLocalNetworkTriState(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"absent vpn block", `{}`, true},
		{"absent key", `{"vpn":{"endpoints":["1.2.3.4"]}}`, true},
		{"explicit true", `{"vpn":{"endpoints":["1.2.3.4"],"allowLocalNetwork":true}}`, true},
		{"explicit false", `{"vpn":{"endpoints":["1.2.3.4"],"allowLocalNetwork":false}}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cfg.json")
			if err := os.WriteFile(path, []byte(c.body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.VPN.AllowLocalNetwork != c.want {
				t.Errorf("AllowLocalNetwork = %v, want %v", cfg.VPN.AllowLocalNetwork, c.want)
			}
		})
	}
}

// An explicit false must also survive a save/reload round trip, or `config set`
// on any unrelated key would quietly re-open the LAN.
func TestAllowLocalNetworkFalseRoundTrips(t *testing.T) {
	src := filepath.Join(t.TempDir(), "in.json")
	if err := os.WriteFile(src, []byte(`{"vpn":{"endpoints":["1.2.3.4"],"allowLocalNetwork":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(src)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	out := filepath.Join(t.TempDir(), "out.json")
	if err := Save(out, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	again, err := Load(out)
	if err != nil {
		t.Fatalf("Load saved: %v", err)
	}
	if again.VPN.AllowLocalNetwork {
		t.Error("allowLocalNetwork came back enabled after a round trip")
	}
}
