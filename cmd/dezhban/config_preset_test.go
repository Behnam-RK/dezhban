package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/behnam-rk/dezhban/internal/config"
)

func TestConfigPresetApplyWritesValidatedValues(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	cfg := config.Default()
	cfg.VPN.Endpoints = []string{"203.0.113.9"}
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if code := cmdConfig([]string{"preset", "apply", "strict", "--config", p}); code != 0 {
			t.Fatalf("preset apply exited %d, want 0", code)
		}
	})
	if !strings.Contains(out, "applying strict") {
		t.Errorf("output missing the applying-preset line:\n%s", out)
	}

	got, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.VPN.SwitchWindow != config.Disabled || got.VPN.RedialWindow != config.Disabled || got.VPN.PauseMax != config.Disabled {
		t.Errorf("strict should disable all three windows, got switch=%v redial=%v pause=%v",
			got.VPN.SwitchWindow, got.VPN.RedialWindow, got.VPN.PauseMax)
	}
	if got.Hysteresis != 1 || got.PollInterval.String() != "10s" {
		t.Errorf("hysteresis/pollInterval not applied: hysteresis=%d pollInterval=%s", got.Hysteresis, got.PollInterval)
	}
	if got.VPN.AllowLocalNetwork || got.VPN.AllowPhysicalDNS {
		t.Errorf("strict should turn off both passes, got allowLocalNetwork=%v allowPhysicalDNS=%v",
			got.VPN.AllowLocalNetwork, got.VPN.AllowPhysicalDNS)
	}
}

func TestConfigPresetApplyPreservesIdentity(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	cfg := config.Default()
	cfg.BlockedCountries = []string{"KP"}
	cfg.VPN.TunnelInterfaces = []string{"utun9"}
	cfg.VPN.Endpoints = []string{"203.0.113.9"}
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}

	if code := cmdConfig([]string{"preset", "apply", "relaxed", "--config", p}); code != 0 {
		t.Fatalf("preset apply exited %d, want 0", code)
	}
	got, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.BlockedCountries) != 1 || got.BlockedCountries[0] != "KP" {
		t.Errorf("blockedCountries changed: %v", got.BlockedCountries)
	}
	if len(got.VPN.TunnelInterfaces) != 1 || got.VPN.TunnelInterfaces[0] != "utun9" {
		t.Errorf("tunnelInterfaces changed: %v", got.VPN.TunnelInterfaces)
	}
	if len(got.VPN.Endpoints) != 1 || got.VPN.Endpoints[0] != "203.0.113.9" {
		t.Errorf("endpoints changed: %v", got.VPN.Endpoints)
	}
}

func TestConfigPresetApplyUnknownNameFails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	cfg := config.Default()
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}
	if code := cmdConfig([]string{"preset", "apply", "paranoid", "--config", p}); code != 2 {
		t.Fatalf("preset apply with an unknown name exited %d, want 2", code)
	}
}

// apply's output is the ordinary "set k = v" lines `config set` prints, not a
// JSON-able report — --json is rejected rather than silently ignored, so a
// script can't believe it asked for machine-readable output and get prose.
func TestConfigPresetApplyRejectsJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	cfg := config.Default()
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}
	if code := cmdConfig([]string{"preset", "apply", "strict", "--json", "--config", p}); code != 2 {
		t.Fatalf("preset apply --json exited %d, want 2", code)
	}
}

func TestConfigPresetListJSONReportsMatch(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	cfg := config.Default()
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if code := cmdConfig([]string{"preset", "list", "--json", "--config", p}); code != 0 {
			t.Fatalf("preset list exited %d, want 0", code)
		}
	})
	var got []presetJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v\noutput:\n%s", err, out)
	}
	if len(got) != 3 {
		t.Fatalf("got %d presets, want 3", len(got))
	}
	var matchedCount int
	for _, p := range got {
		if p.Matched {
			matchedCount++
			if p.Name != "balanced" {
				t.Errorf("matched preset = %q, want \"balanced\" (config.Default())", p.Name)
			}
		}
	}
	if matchedCount != 1 {
		t.Errorf("matched %d presets, want exactly 1 for an un-drifted default config", matchedCount)
	}
}

func TestConfigPresetShowJSONIncludesValues(t *testing.T) {
	out := captureStdout(t, func() {
		if code := cmdConfig([]string{"preset", "show", "strict", "--json"}); code != 0 {
			t.Fatalf("preset show exited %d, want 0", code)
		}
	})
	var got presetJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v\noutput:\n%s", err, out)
	}
	if got.Name != "strict" {
		t.Errorf("name = %q, want \"strict\"", got.Name)
	}
	if got.Values["vpn.switchWindow"] != "0" {
		t.Errorf("values[vpn.switchWindow] = %q, want \"0\"", got.Values["vpn.switchWindow"])
	}
}

func TestConfigPresetDiffDefaultsToNearestWhenCustom(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	cfg := config.Default()
	config.Normalize(&cfg)
	cfg.Hysteresis = 9 // drift by exactly one key from balanced
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if code := cmdConfig([]string{"preset", "diff", "--config", p}); code != 0 {
			t.Fatalf("preset diff exited %d, want 0", code)
		}
	})
	if !strings.Contains(out, "drift from balanced:") {
		t.Errorf("expected the nearest preset (balanced) as the default diff target:\n%s", out)
	}
	if !strings.Contains(out, "hysteresis:") {
		t.Errorf("expected hysteresis in the drift output:\n%s", out)
	}
}

func TestConfigPresetDiffNamedPresetNoDriftWhenExact(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	cfg := config.Default()
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if code := cmdConfig([]string{"preset", "diff", "balanced", "--config", p}); code != 0 {
			t.Fatalf("preset diff exited %d, want 0", code)
		}
	})
	if !strings.Contains(out, "no drift from balanced") {
		t.Errorf("expected no drift for a fresh default config against balanced:\n%s", out)
	}
}

// This package's configFields setters and internal/config's presetSetters are
// two independent implementations of the same eight keys (preset.go documents
// the duplication as deliberate, to avoid a reverse dependency on cmd/dezhban).
// Nothing else pins that they agree: if they drifted, `preset apply` would
// write one set of values while `preset diff`/`preset list --json`'s `matched`
// flag (what the GUI's preset picker checkmark reads) judged against another,
// so a user could apply Strict and still see "Custom" with drift they have no
// way to clear. Round-tripping every shipped preset through the real `preset
// apply` command and asking config.MatchPreset whether it landed closes that
// gap.
func TestConfigPresetApplyMatchesItself(t *testing.T) {
	for _, p := range config.Presets() {
		t.Run(p.Name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.json")
			cfg := config.Default()
			cfg.VPN.Endpoints = []string{"203.0.113.9"}
			if err := config.Save(path, &cfg); err != nil {
				t.Fatal(err)
			}
			if code := cmdConfig([]string{"preset", "apply", p.Name, "--config", path}); code != 0 {
				t.Fatalf("preset apply %s exited %d, want 0", p.Name, code)
			}
			got, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if name, exact := config.MatchPreset(got); !exact || name != p.Name {
				t.Errorf("after applying %s, MatchPreset = (%q, %v); the CLI's setters and "+
					"internal/config's presetSetters have drifted apart", p.Name, name, exact)
			}
		})
	}
}
