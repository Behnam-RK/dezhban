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

// The failure this guards against for array-valued keys: vpn.profiles is a
// []object, and walkUnknown used to only recurse into map[string]any, so a
// typo or a renamed key inside a profile was silently dropped — the very
// failure this file exists to prevent, just one JSON container type away from
// where it was already caught for vpn/vpn.advanced/control.
func TestUnknownKeysInsideProfilesAreReported(t *testing.T) {
	cfg := loadFromJSON(t, `{
	  "vpn": {
	    "profiles": [
	      {"name": "a", "endpoints": ["1.1.1.1"], "ifaceHint": "wg"},
	      {"name": "b", "endpoints": ["2.2.2.2"], "bogusKey": true}
	    ]
	  }
	}`)

	got := retiredKeys(cfg)
	if !slices.Contains(got, "vpn.profiles[1].bogusKey") {
		t.Errorf("unknown key inside a profile element was not reported (with its index); reported: %v", got)
	}
	if slices.Contains(got, "vpn.profiles[0].ifaceHint") {
		t.Errorf("a known profile key was reported as unknown: %v", got)
	}
}

// describeUnknown must normalise an array index away before consulting
// renamedKeys, so a rename inside a profile is reported for every element with
// one map entry rather than needing one per index. Uses a synthetic entry
// (saved/restored) rather than depending on any real rename existing.
func TestRenamedKeyInsideAnArrayElementIsNormalised(t *testing.T) {
	const oldKey, newKey = "vpn.profiles[].syntheticOld", "vpn.profiles[].syntheticNew"
	renamedKeys[oldKey] = newKey
	t.Cleanup(func() { delete(renamedKeys, oldKey) })

	got := describeUnknown("vpn.profiles[3].syntheticOld")
	if want := "renamed to " + newKey; !strings.Contains(got, want) {
		t.Errorf("describeUnknown(%q) = %q, want it to mention %q", "vpn.profiles[3].syntheticOld", got, want)
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
