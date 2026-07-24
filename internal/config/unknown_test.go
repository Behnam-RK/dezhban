package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func loadFromJSON(t *testing.T, body string) *Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

func retiredKeys(cfg *Config) []string {
	out := make([]string, 0, len(cfg.Retired))
	for _, r := range cfg.Retired {
		out = append(out, r.Key)
	}
	return out
}

// The failure this guards against: a key the schema does not know is dropped by
// the JSON decoder without a word, so the setting silently reverts to its
// default. For a window that someone deliberately disabled, that quietly
// re-enables a relaxation of the guard.
func TestUnknownKeysAreReported(t *testing.T) {
	cfg := loadFromJSON(t, `{
	  "pollInterval": "20s",
	  "notAKey": true,
	  "vpn": { "alsoNotAKey": 1, "advanced": { "nopeNotThisEither": "x" } }
	}`)

	got := retiredKeys(cfg)
	for _, want := range []string{"notAKey", "vpn.alsoNotAKey", "vpn.advanced.nopeNotThisEither"} {
		if !slices.Contains(got, want) {
			t.Errorf("unknown key %q was not reported; reported: %v", want, got)
		}
	}
}

// A renamed key has to say what replaced it. "not recognised" sends someone
// hunting through docs for a key that simply moved.
func TestRenamedKeysPointAtTheirReplacement(t *testing.T) {
	cfg := loadFromJSON(t, `{"vpn": {"reconnectWindow": "0"}}`)

	var found bool
	for _, r := range cfg.Retired {
		if r.Key != "vpn.reconnectWindow" {
			continue
		}
		found = true
		if want := "vpn.redialWindow"; !strings.Contains(r.Reason, want) {
			t.Errorf("reason %q does not name the replacement %q", r.Reason, want)
		}
	}
	if !found {
		t.Errorf("the old vpn.reconnectWindow was not reported; reported: %v", retiredKeys(cfg))
	}
}

// A valid config must stay quiet, or the report becomes noise nobody reads.
func TestKnownKeysAreNotReportedAsUnknown(t *testing.T) {
	cfg := loadFromJSON(t, `{
	  "pollInterval": "20s",
	  "hysteresis": 2,
	  "vpn": {
	    "tunnelInterfaces": ["utun4"],
	    "redialWindow": "45s",
	    "advanced": { "redialMinUptime": "20s" }
	  },
	  "control": { "enabled": true }
	}`)

	if len(cfg.Retired) != 0 {
		t.Errorf("a valid config reported issues: %v", cfg.Retired)
	}
}
