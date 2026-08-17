package main

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/behnam-rk/dezhban/internal/config"
)

// TestStatusJSONFieldSet pins `status --json`'s top-level key set. The output
// is a stable contract for tooling (the macOS app reads it), so a field must
// only ever appear or disappear deliberately — this test is the tripwire that
// makes an accidental rename or drop fail loudly.
//
// Keys marked omitempty and empty in this environment (commit, buildDate,
// state, stateAge) are asserted as ALLOWED rather than required, since their
// presence depends on the build and on a daemon having run.
func TestStatusJSONFieldSet(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	cfg := config.Default()
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if code := cmdStatus([]string{"--json", "--config", p}); code != 0 {
			t.Fatalf("status --json exited %d, want 0", code)
		}
	})

	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v\noutput:\n%s", err, out)
	}

	required := []string{
		"version", "privileged", "service", "controlReachable", "statePath",
		"stateStale", "pollInterval", "blockedCountries", "pauseEnabled",
		"preset", "presetExact",
	}
	optional := []string{"commit", "buildDate", "state", "stateAge"}

	for _, k := range required {
		if _, ok := got[k]; !ok {
			t.Errorf("status --json is missing required key %q", k)
		}
	}
	allowed := map[string]bool{}
	for _, k := range append(required, optional...) {
		allowed[k] = true
	}
	var extra []string
	for k := range got {
		if !allowed[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("status --json grew unpinned key(s) %s — add them here deliberately", strings.Join(extra, ", "))
	}
}

// A default config matches the balanced preset exactly, and status must say so.
func TestStatusJSONReportsMatchedPreset(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	cfg := config.Default()
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if code := cmdStatus([]string{"--json", "--config", p}); code != 0 {
			t.Fatalf("status --json exited %d, want 0", code)
		}
	})
	var got struct {
		Preset      string `json:"preset"`
		PresetExact bool   `json:"presetExact"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Preset != "balanced" || !got.PresetExact {
		t.Errorf("preset = %q exact=%v, want \"balanced\" exact=true for a default config", got.Preset, got.PresetExact)
	}
}

// A drifted config still reports its nearest preset, marked inexact — the same
// anchor `config preset diff` defaults to, so the two surfaces agree.
func TestStatusJSONReportsNearestPresetWhenCustom(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	cfg := config.Default()
	cfg.Hysteresis = 7 // drift one preset key
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if code := cmdStatus([]string{"--json", "--config", p}); code != 0 {
			t.Fatalf("status --json exited %d, want 0", code)
		}
	})
	var got struct {
		Preset      string `json:"preset"`
		PresetExact bool   `json:"presetExact"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.PresetExact {
		t.Error("presetExact = true for a drifted config, want false")
	}
	if got.Preset != "balanced" {
		t.Errorf("preset = %q, want \"balanced\" (nearest to a one-key drift from default)", got.Preset)
	}
}
