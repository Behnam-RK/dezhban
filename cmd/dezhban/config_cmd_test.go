package main

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/behnam-rk/dezhban/internal/config"
)

func TestStripConfigFlag(t *testing.T) {
	cases := []struct {
		name     string
		in       []string
		wantPath string
		wantRest []string
	}{
		{"absent", []string{"get", "logLevel"}, "", []string{"get", "logLevel"}},
		{"flag-first-space", []string{"--config", "/tmp/x.json", "show"}, "/tmp/x.json", []string{"show"}},
		{"flag-last-space", []string{"show", "--config", "/tmp/x.json"}, "/tmp/x.json", []string{"show"}},
		{"equals", []string{"--config=/tmp/x.json", "get", "logLevel"}, "/tmp/x.json", []string{"get", "logLevel"}},
		{"equals-after-positional", []string{"set", "blockedCountries", "IR", "--config=/tmp/x.json"}, "/tmp/x.json", []string{"set", "blockedCountries", "IR"}},
		{"single-dash", []string{"-config", "/tmp/x.json", "show"}, "/tmp/x.json", []string{"show"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotPath, gotRest := stripConfigFlag(c.in)
			if gotPath != c.wantPath {
				t.Errorf("path = %q, want %q", gotPath, c.wantPath)
			}
			if !reflect.DeepEqual(gotRest, c.wantRest) {
				t.Errorf("rest = %v, want %v", gotRest, c.wantRest)
			}
		})
	}
}

// TestConfigGetHonorsConfigFlag proves the config subcommands actually read the file
// named by --config (previously the flag was silently ignored and the system config
// was read instead).
func TestConfigGetHonorsConfigFlag(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.json")
	cfg := config.Default()
	cfg.BlockedCountries = []string{"IR", "CN"}
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if code := cmdConfig([]string{"get", "blockedCountries", "--config", p}); code != 0 {
			t.Fatalf("cmdConfig get exited %d, want 0", code)
		}
	})
	if got := strings.TrimSpace(out); got != "IR,CN" {
		t.Errorf("config get blockedCountries = %q, want %q", got, "IR,CN")
	}
}

// The multi-pair form exists so the GUI can apply a whole panel of fields in ONE
// privileged invocation — i.e. one password prompt instead of one per field. It must
// write every pair, in one file write.
func TestConfigSetAppliesAllPairsInOneWrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	cfg := config.Default()
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}

	// A batch validates once, at the end, so no key ordering is needed to keep the
	// file valid at every intermediate step — that this passes is the point.
	code := cmdConfig([]string{
		"set",
		"vpn.tunnelInterfaces=utun4",
		"vpn.autodetect=true",
		"vpn.autoDiscoverEndpoints=true",
		"logLevel=debug",
		"--config", p,
	})
	if code != 0 {
		t.Fatalf("config set (multi) exited %d, want 0", code)
	}

	got, err := config.Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !got.VPN.Autodetect ||
		len(got.VPN.TunnelInterfaces) != 1 || got.VPN.TunnelInterfaces[0] != "utun4" ||
		got.LogLevel != "debug" {
		t.Fatalf("not every pair was applied: %+v", got.VPN)
	}
}

// The batch is all-or-nothing. Validation happens once, after every pair is applied,
// so a bad value anywhere must leave the file completely untouched — never a
// half-applied config (which, with vpn.enabled, could mean an enforcing guard with
// no tunnel).
func TestConfigSetRejectsWholeBatchOnBadValue(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	cfg := config.Default()
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	code := cmdConfig([]string{
		"set",
		"logLevel=debug",                 // valid
		"vpn.tunnelWatch=not-a-duration", // invalid — must sink the whole batch
		"--config", p,
	})
	if code == 0 {
		t.Fatal("config set accepted an invalid value")
	}

	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("a rejected batch still wrote to the config file; the earlier pairs were persisted")
	}
}

// The two-positional form predates the batch form and is used by scripts and docs.
func TestConfigSetLegacyTwoArgFormStillWorks(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	cfg := config.Default()
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}
	if code := cmdConfig([]string{"set", "logLevel", "warn", "--config", p}); code != 0 {
		t.Fatalf("legacy `config set <key> <value>` exited %d, want 0", code)
	}
	got, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.LogLevel != "warn" {
		t.Errorf("logLevel = %q, want %q", got.LogLevel, "warn")
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	_ = w.Close()
	data, _ := io.ReadAll(r)
	return string(data)
}

// parseSetPairs carries the whole legacy-vs-batch disambiguation, and the GUI depends
// on its edges: it sends an empty value on every apply (`vpn.endpoints=` clears the
// list), and a regression that routed `config set logLevel debug` into the pair parser
// would break every script and doc example.
func TestParseSetPairs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []setPair
	}{
		{"legacy two positionals", []string{"logLevel", "debug"}, []setPair{{"logLevel", "debug"}}},
		{"legacy value with an equals sign", []string{"providers", "https://x/?a=b"},
			[]setPair{{"providers", "https://x/?a=b"}}},
		{"single pair", []string{"vpn.enabled=true"}, []setPair{{"vpn.enabled", "true"}}},
		{"several pairs", []string{"vpn.enabled=true", "logLevel=warn"},
			[]setPair{{"vpn.enabled", "true"}, {"logLevel", "warn"}}},
		{"empty value clears a list", []string{"vpn.endpoints="}, []setPair{{"vpn.endpoints", ""}}},
		{"value keeps later equals signs", []string{"providers=https://x/?a=b"},
			[]setPair{{"providers", "https://x/?a=b"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseSetPairs(c.in)
			if err != nil {
				t.Fatalf("parseSetPairs(%v) errored: %v", c.in, err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("parseSetPairs(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}

	bad := [][]string{
		{},                               // nothing to set
		{"logLevel"},                     // a lone key is not a pair
		{"logLevel", "debug", "extra"},   // 3 args must all be pairs
		{"=debug"},                       // empty key
		{"vpn.enabled=true", "logLevel"}, // one good pair, one bare word
	}
	for _, in := range bad {
		if _, err := parseSetPairs(in); err == nil {
			t.Errorf("parseSetPairs(%v) succeeded; want an error", in)
		}
	}
}

// vpn.switchWindow "0" must disable rather than reset to default — the same
// explicit-opt-out sentinel vpn.reconnectWindow already remaps. Before this
// fix, `config set vpn.switchWindow 0` was silently coerced back to the 5s
// default by Normalize, the exact bug class CLAUDE.md calls the worst this
// tool can have: a security setting accepted, discarded, and never reported.
func TestConfigSetSwitchWindowZeroDisables(t *testing.T) {
	for _, key := range []string{"vpn.switchWindow", "vpn.reconnectWindow", "vpn.pauseMax"} {
		t.Run(key, func(t *testing.T) {
			cfg := config.Default()
			field, ok := configFields[key]
			if !ok {
				t.Fatalf("no configField for %q", key)
			}
			if err := field.set(&cfg, "0"); err != nil {
				t.Fatalf("set(%q, \"0\") errored: %v", key, err)
			}
			config.Normalize(&cfg)
			if got := field.get(&cfg); got != "0s" {
				t.Errorf("get(%q) after set 0 + Normalize = %q, want \"0s\" (disabled, not reset to default)", key, got)
			}
		})
	}
}
