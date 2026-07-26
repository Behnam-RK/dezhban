package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// A user who lowers vpn.advanced.switchWindowMax by hand puts Relaxed out of
// reach. The write already failed safely — Validate rejects it, nothing is
// persisted — but it failed naming the validation rule rather than the conflict,
// leaving the user to work out that a preset the CLI offered can never apply.
// Refuse up front, name both sides, and leave the file untouched.
func TestConfigPresetApplyRefusesAgainstALoweredCap(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	cfg := config.Default()
	cfg.VPN.Advanced.SwitchWindowMax = 10 * time.Second
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	var out string
	msg := captureStderr(t, func() {
		out = captureStdout(t, func() {
			if code := cmdConfig([]string{"preset", "apply", "relaxed", "--config", p}); code != 1 {
				t.Fatalf("preset apply exited %d, want 1", code)
			}
		})
	})
	for _, want := range []string{"cannot apply", "vpn.switchWindow", "vpn.advanced.switchWindowMax", "10s"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not mention %q", msg, want)
		}
	}
	// Nothing was applied, so nothing may announce that it was. The banner used
	// to print ahead of this check, so a refused preset led with "applying
	// relaxed: …" and a full paragraph describing a cost the user never paid.
	for _, forbidden := range []string{"applying", "cost:"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("a refused preset printed %q on stdout:\n%s", forbidden, out)
		}
	}

	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("a refused preset apply rewrote the config file")
	}
	// And the preset must still be appliable once the cap allows it — the
	// refusal is about this config, not about the preset.
	cfg.VPN.Advanced.SwitchWindowMax = 3 * time.Minute
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() {
		if code := cmdConfig([]string{"preset", "apply", "relaxed", "--config", p}); code != 0 {
			t.Fatalf("preset apply exited %d under the default cap, want 0", code)
		}
	})
}

// `preset list` offers all three, so it must say which of them this config
// cannot accept — discovering it at apply time is discovering it too late.
func TestConfigPresetListFlagsAnInappliablePreset(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	cfg := config.Default()
	cfg.VPN.Advanced.RedialWindowMax = time.Minute
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if code := cmdConfig([]string{"preset", "list", "--config", p}); code != 0 {
			t.Fatalf("preset list exited %d, want 0", code)
		}
	})
	if !strings.Contains(out, "cannot apply") || !strings.Contains(out, "vpn.advanced.redialWindowMax") {
		t.Errorf("preset list did not flag the inappliable preset:\n%s", out)
	}
}

// Nine of the twelve advanced keys coerce a non-positive value back to their
// shipped default (Normalize runs inside the write). The stored value was
// already echoed truthfully, but silently: someone who typed `=0` meaning "off"
// had to notice for themselves that a duration came back. Say it.
func TestConfigSetReportsACoercedValue(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	cfg := config.Default()
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if code := cmdConfig([]string{"set", "vpn.advanced.tunnelPruneAfter=0", "--config", p}); code != 0 {
			t.Fatal("config set exited non-zero")
		}
	})
	if !strings.Contains(out, "note: vpn.advanced.tunnelPruneAfter was normalised on write") {
		t.Errorf("no coercion note for a value Normalize replaced:\n%s", out)
	}

	// The sentinel keys are NOT coerced, so they must not draw a note —
	// reporting a change that did not happen is the same failure pointed the
	// other way.
	out = captureStdout(t, func() {
		if code := cmdConfig([]string{"set", "vpn.advanced.redialMinUptime=0", "--config", p}); code != 0 {
			t.Fatal("config set exited non-zero")
		}
	})
	if strings.Contains(out, "was normalised on write") {
		t.Errorf("redialMinUptime's disable sentinel was reported as coerced:\n%s", out)
	}

	// And an ordinary value stored exactly as typed stays quiet, whatever the
	// formatting difference between "1h" and "1h0m0s".
	out = captureStdout(t, func() {
		if code := cmdConfig([]string{"set", "vpn.advanced.tunnelPruneAfter=1h", "--config", p}); code != 0 {
			t.Fatal("config set exited non-zero")
		}
	})
	if strings.Contains(out, "was normalised on write") {
		t.Errorf("a value stored as typed was reported as coerced:\n%s", out)
	}
}

// The coercion note is owed on the token/socket path too, and used to be
// missing there: the DAEMON performs that write, so the CLI never held the
// normalised config and reported only `Saved and applied: <key>` — a true
// statement about a value the operator did not type, on the path the macOS app
// and every script prefer.
//
// The socket round trip is what this cannot stand up, so it exercises
// everything either side of it: the "before" reading the CLI now takes itself
// (typedValues), the write the daemon actually performs (writeConfigKeys — the
// same function its config-write op calls), and the "after" reading off disk.
func TestTokenPathAlsoNotesACoercedValue(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	cfg := config.Default()
	cfg.VPN.Advanced.TunnelPruneAfter = 5 * time.Minute
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}
	pairs := map[string]string{"vpn.advanced.tunnelPruneAfter": "0"}

	before, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	typed := typedValues(before, pairs)
	if got := typed["vpn.advanced.tunnelPruneAfter"]; got != "0s" {
		t.Fatalf("typedValues rendered %q, want the value as typed (%q)", got, "0s")
	}

	if err := writeConfigKeys(p, pairs); err != nil {
		t.Fatal(err)
	}
	saved, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { noteCoercions(saved, typed) })
	const prefix = "note: vpn.advanced.tunnelPruneAfter was normalised on write: 0s → "
	if !strings.Contains(out, prefix) || strings.Contains(out, prefix+"0s") {
		t.Errorf("token path produced no coercion note naming the stored value:\n%s", out)
	}

	// And a key whose "0" is a real opt-out still draws nothing, so the note
	// stays a report of a substitution rather than of every write.
	quiet := map[string]string{"vpn.advanced.redialMinUptime": "0"}
	before, err = config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	typed = typedValues(before, quiet)
	if err := writeConfigKeys(p, quiet); err != nil {
		t.Fatal(err)
	}
	saved, err = config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if out := captureStdout(t, func() { noteCoercions(saved, typed) }); out != "" {
		t.Errorf("the disable sentinel was reported as coerced on the token path:\n%s", out)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	fn()
	_ = w.Close()
	data, _ := io.ReadAll(r)
	return string(data)
}
