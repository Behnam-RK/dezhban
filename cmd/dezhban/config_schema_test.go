package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/behnam-rk/dezhban/internal/config"
)

// TestConfigFieldsMatchTheDeclaredSchema is the reason `config schema` can be
// trusted. configFields is what `config set` will actually accept; Tunables is
// what every surface tells the user exists. A key in one and not the other is
// either a setting nobody can discover or a setting the app offers and the CLI
// rejects.
func TestConfigFieldsMatchTheDeclaredSchema(t *testing.T) {
	declared := map[string]bool{}
	for _, k := range config.TunableKeys() {
		declared[k] = true
	}

	for key := range configFields {
		if !declared[key] {
			t.Errorf("config set accepts %q but it has no Tunable — add one to internal/config/schema.go", key)
		}
	}
	for key := range declared {
		if _, ok := configFields[key]; !ok {
			t.Errorf("%q is declared in the schema but config set cannot write it", key)
		}
	}
}

// TestUsageListsEveryKey pins the generated help. The key list used to be typed
// out by hand and is now built from the schema; this catches a regression back
// to a hand-maintained copy.
func TestUsageListsEveryKey(t *testing.T) {
	for _, key := range config.TunableKeys() {
		if !strings.Contains(configUsage, " "+key) {
			t.Errorf("config usage text omits %q", key)
		}
	}
}

// TestSchemaDefaultsAgreeWithConfigGet cross-checks the two renderings of a
// value: KeyValues (which the schema's defaults come from) and configFields.get
// (which `config get` prints). On a default config nothing is disabled, so the
// two must agree exactly — a divergence would mean the app shows one default as
// a hint and `config get` reports another.
func TestSchemaDefaultsAgreeWithConfigGet(t *testing.T) {
	cfg := config.Default()
	config.Normalize(&cfg)

	for _, tun := range config.Tunables() {
		field, ok := configFields[tun.Key]
		if !ok {
			continue // reported by TestConfigFieldsMatchTheDeclaredSchema
		}
		if got := field.get(&cfg); got != tun.Default {
			t.Errorf("%s: config get renders %q but the schema's default is %q",
				tun.Key, got, tun.Default)
		}
	}
}

// TestConfigSchemaJSONCoversEveryKey exercises the command end to end, since the
// macOS app decodes exactly these bytes.
func TestConfigSchemaJSONCoversEveryKey(t *testing.T) {
	var code int
	out := captureStdout(t, func() { code = configSchema([]string{"--json"}) })
	if code != 0 {
		t.Fatalf("config schema --json exited %d", code)
	}

	var entries []struct {
		Key           string `json:"key"`
		Label         string `json:"label"`
		Kind          string `json:"kind"`
		Default       string `json:"default"`
		CapKey        string `json:"capKey"`
		Disablable    bool   `json:"disablable"`
		Advanced      bool   `json:"advanced"`
		Preset        bool   `json:"preset"`
		Help          string `json:"help"`
		DocAnchor     string `json:"docAnchor"`
		RestartReason string `json:"restartReason"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("config schema --json emitted undecodable JSON: %v", err)
	}

	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Key] = true
		if e.Label == "" || e.Kind == "" || e.Help == "" || e.DocAnchor == "" {
			t.Errorf("%s: incomplete entry in JSON output", e.Key)
		}
	}
	for _, key := range config.TunableKeys() {
		if !seen[key] {
			t.Errorf("config schema --json omits %q", key)
		}
	}

	// The embedded Tunable must flatten, not nest — the app decodes these fields
	// at the top level of each entry.
	if strings.Contains(out, `"Tunable"`) {
		t.Error("config schema --json nested the embedded Tunable instead of flattening it")
	}
}

// TestConfigSchemaMarksPresetKeys — a surface warns that editing one of these by
// hand drifts the config off its preset, so the flag has to be right.
func TestConfigSchemaMarksPresetKeys(t *testing.T) {
	written := presetWritten()
	if len(written) == 0 {
		t.Fatal("no preset writes any key")
	}
	for key := range written {
		if _, ok := configFields[key]; !ok {
			t.Errorf("preset writes %q, which config set cannot write", key)
		}
	}
	// Spot-check a key presets deliberately leave alone: presets are strictness
	// strategies, never identity.
	if written["vpn.endpoints"] {
		t.Error("a preset writes vpn.endpoints — presets must not touch identity data")
	}
}

// TestConfigSchemaRejectsArguments — it takes none, and a typo'd subcommand must
// not be silently ignored.
func TestConfigSchemaRejectsArguments(t *testing.T) {
	if code := configSchema([]string{"pollInterval"}); code != 2 {
		t.Errorf("config schema with an argument exited %d, want 2", code)
	}
}

// TestConfigSchemaNeedsNoConfigFile is the property a first-run wizard depends
// on: the schema describes what the keys ARE, so it must answer on a host that
// has no config yet.
func TestConfigSchemaNeedsNoConfigFile(t *testing.T) {
	t.Setenv("DEZHBAN_CONFIG", "/nonexistent/dezhban/config.json")
	var code int
	out := captureStdout(t, func() { code = configSchema([]string{"--json"}) })
	if code != 0 {
		t.Fatalf("config schema --json exited %d with no config file", code)
	}
	if !strings.Contains(out, `"key": "pollInterval"`) {
		t.Error("config schema --json produced nothing useful without a config file")
	}
}
