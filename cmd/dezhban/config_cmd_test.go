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
