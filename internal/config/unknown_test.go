package config

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
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

// The vocabulary sweep renamed vpn.autodetect to vpn.autoDetect (casing
// consistency with every other auto* key). Because the change is case-only,
// encoding/json still honors the old spelling — so the report must say THAT,
// not "has no effect". Telling someone a live setting is inert is the same
// class of failure as silently discarding one, and the more dangerous
// direction: they stop looking while the value is in force.
func TestMiscasedAutodetectIsReportedAsHavingTakenEffect(t *testing.T) {
	cfg := loadFromJSON(t, `{"vpn": {"autodetect": false}}`)

	// The value is live: vpn.autoDetect defaults to TRUE, so a silently
	// discarded `false` would relax a deliberately narrowed guard.
	if cfg.VPN.AutoDetect {
		t.Error("vpn.autodetect=false was not honored; AutoDetect came back as the true default")
	}

	var found bool
	for _, r := range cfg.Retired {
		if r.Key != "vpn.autodetect" {
			continue
		}
		found = true
		if !r.TookEffect {
			t.Error("vpn.autodetect is reported as inert, but its value took effect")
		}
		if want := "vpn.autoDetect"; !strings.Contains(r.Reason, want) {
			t.Errorf("reason %q does not name the correct spelling %q", r.Reason, want)
		}
	}
	if !found {
		t.Errorf("the old vpn.autodetect casing was not reported at all; reported: %v", retiredKeys(cfg))
	}
}

// With both spellings present, which one wins is DOCUMENT ORDER, not
// exactness — the decoder assigns each key as it reads it, so the later one
// overwrites the earlier regardless of whose tag matches exactly. That is
// genuinely ambiguous and cannot be fixed from here, which is exactly why the
// miscased key has to be reported: the report is the only thing that tells an
// operator their config has two keys fighting over one setting.
func TestBothAutoDetectSpellingsPresentIsReported(t *testing.T) {
	for _, body := range []string{
		`{"vpn": {"autodetect": true, "autoDetect": false}}`,
		`{"vpn": {"autoDetect": false, "autodetect": true}}`,
	} {
		cfg := loadFromJSON(t, body)
		if !slices.Contains(retiredKeys(cfg), "vpn.autodetect") {
			t.Errorf("%s: the miscased vpn.autodetect was not reported; reported: %v", body, retiredKeys(cfg))
		}
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
	      {"name": "a", "endpoints": ["1.1.1.1"], "tunnelHint": "wg"},
	      {"name": "b", "endpoints": ["2.2.2.2"], "bogusKey": true}
	    ]
	  }
	}`)

	got := retiredKeys(cfg)
	if !slices.Contains(got, "vpn.profiles[1].bogusKey") {
		t.Errorf("unknown key inside a profile element was not reported (with its index); reported: %v", got)
	}
	if slices.Contains(got, "vpn.profiles[0].tunnelHint") {
		t.Errorf("a known profile key was reported as unknown: %v", got)
	}
}

// The real-world case the array-index normalisation (see
// TestRenamedKeyInsideAnArrayElementIsNormalised) exists for: the vocabulary
// sweep renamed vpn.profiles[].ifaceHint to tunnelHint, and an old config with
// ifaceHint set on ANY profile must be told so, with the index that pinpoints
// which entry.
func TestOldIfaceHintInsideAProfileIsReportedAsRenamed(t *testing.T) {
	cfg := loadFromJSON(t, `{
	  "vpn": {
	    "profiles": [
	      {"name": "a", "endpoints": ["1.1.1.1"], "tunnelHint": "wg"},
	      {"name": "b", "endpoints": ["2.2.2.2"], "ifaceHint": "tun"}
	    ]
	  }
	}`)

	var found bool
	for _, r := range cfg.Retired {
		if r.Key != "vpn.profiles[1].ifaceHint" {
			continue
		}
		found = true
		if want := "vpn.profiles[].tunnelHint"; !strings.Contains(r.Reason, want) {
			t.Errorf("reason %q does not name the replacement %q", r.Reason, want)
		}
	}
	if !found {
		t.Errorf("the old vpn.profiles[1].ifaceHint was not reported; reported: %v", retiredKeys(cfg))
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

	got, tookEffect := describeUnknown(unknownKey{Key: "vpn.profiles[3].syntheticOld"})
	if want := "renamed to " + newKey; !strings.Contains(got, want) {
		t.Errorf("describeUnknown(%q) = %q, want it to mention %q", "vpn.profiles[3].syntheticOld", got, want)
	}
	if tookEffect {
		t.Error("a renamed key was reported as having taken effect; the old name is inert")
	}
}

// A key misspelled only in letter case normally IS live, because the decoder
// folds it onto the schema field — but not when the field it folds onto is
// RETIRED, which the DTO keeps only so apply() can report it. Reporting
// "this value IS in effect" there tells an operator a discarded security
// setting is running, which is the lie this whole file exists to prevent,
// reached from the one direction the case-folding branch didn't cover.
func TestMiscasedRetiredKeyIsNotReportedAsLive(t *testing.T) {
	cfg := loadFromJSON(t, `{
	  "FailClosed": true,
	  "vpn": { "tunnelInterfaces": ["utun4"], "endpoints": ["1.2.3.4"], "Enabled": false }
	}`)

	for _, miscased := range []string{"FailClosed", "vpn.Enabled"} {
		var found bool
		for _, r := range cfg.Retired {
			if r.Key != miscased {
				continue
			}
			found = true
			if r.TookEffect {
				t.Errorf("%q reported as TookEffect; it folds onto a retired key, which nothing reads", miscased)
			}
			if !strings.Contains(r.Reason, "retired") {
				t.Errorf("%q reason = %q, want it to name the retirement", miscased, r.Reason)
			}
		}
		if !found {
			t.Errorf("%q was not reported at all; reported: %v", miscased, retiredKeys(cfg))
		}
	}
}

// The failure this guards against, which is the mirror image of the one at the
// top of this file: encoding/json matches field tags case-insensitively, so a
// key typed in the wrong case is NOT dropped — it is honored — while a
// case-sensitive schema walk called it unrecognised and the report said "it has
// no effect". An operator reading that about a relaxing value stops looking
// while the value is in force. Both halves are asserted together on purpose:
// the value landing, and the report admitting it.
func TestMiscasedKeysTakeEffectAndAreReportedAsSuch(t *testing.T) {
	cfg := loadFromJSON(t, `{
	  "pollinterval": "1h",
	  "vpn": { "PAUSEMAX": "2h", "advanced": { "RedialMinUptime": "0s" } }
	}`)

	if want := time.Hour; cfg.PollInterval != want {
		t.Errorf("pollinterval (miscased) = %v, want %v — the premise of this test is that it DOES take effect", cfg.PollInterval, want)
	}
	if want := 2 * time.Hour; cfg.VPN.PauseMax != want {
		t.Errorf("vpn.PAUSEMAX (miscased) = %v, want %v", cfg.VPN.PauseMax, want)
	}

	byKey := map[string]Retired{}
	for _, r := range cfg.Retired {
		byKey[r.Key] = r
	}
	for _, key := range []string{"pollinterval", "vpn.PAUSEMAX", "vpn.advanced.RedialMinUptime"} {
		r, ok := byKey[key]
		if !ok {
			t.Errorf("miscased key %q was not reported at all; reported: %v", key, retiredKeys(cfg))
			continue
		}
		if !r.TookEffect {
			t.Errorf("%q is reported as inert, but a miscased key takes effect", key)
		}
		if strings.Contains(r.Reason, "no effect") {
			t.Errorf("%q reason says it has no effect, which is false: %q", key, r.Reason)
		}
	}
}

// A miscased BLOCK is still walked into: `"VPN": {"nonsense": 1}` took effect as
// vpn, so a typo nested under it is just as inert as one under the correctly
// spelled block, and just as much worth reporting.
func TestTyposUnderAMiscasedBlockAreStillReported(t *testing.T) {
	cfg := loadFromJSON(t, `{"VPN": {"tunnelInterfaces": ["utun4"], "nonsenseKey": 1}}`)

	got := retiredKeys(cfg)
	if !slices.Contains(got, "VPN.nonsenseKey") {
		t.Errorf("a typo nested under a miscased block was not reported; reported: %v", got)
	}
	if !slices.Contains(cfg.VPN.TunnelInterfaces, "utun4") {
		t.Errorf("the miscased block itself did not take effect; tunnels: %v", cfg.VPN.TunnelInterfaces)
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

// Reported keys are sorted so a human can scan them, and for array elements
// that ordering has to be NUMERIC: a plain string sort puts "vpn.profiles[10]"
// before "vpn.profiles[2]", so on a config with more than ten profiles the
// report's line order stops matching the file's, and "the third one" no longer
// means the third line. See sortKey.
func TestArrayIndexedKeysSortNumerically(t *testing.T) {
	var profiles []string
	for i := range 12 {
		profiles = append(profiles, `{"name":"p`+strconv.Itoa(i)+`","endpoints":["198.51.100.1"],"ifaceHint":"wg"}`)
	}
	cfg := loadFromJSON(t, `{"vpn":{"profiles":[`+strings.Join(profiles, ",")+`]}}`)

	var got []int
	for _, r := range cfg.Retired {
		if !strings.HasSuffix(r.Key, ".ifaceHint") {
			continue
		}
		idx := r.Key[strings.Index(r.Key, "[")+1 : strings.Index(r.Key, "]")]
		n, err := strconv.Atoi(idx)
		if err != nil {
			t.Fatalf("unparsable index in %q: %v", r.Key, err)
		}
		got = append(got, n)
	}
	if len(got) != 12 {
		t.Fatalf("reported %d ifaceHint keys, want 12: %v", len(got), retiredKeys(cfg))
	}
	if !slices.IsSorted(got) {
		t.Errorf("indices reported in order %v, want ascending — array keys are sorting lexicographically", got)
	}
}

// The reported key keeps the FILE's spelling so the user can find the line, so a
// miscased PARENT block ("VPN" rather than "vpn") reaches the renamedKeys lookup
// with a path the map does not hold verbatim. The rename hint is the only part
// of that line a reader can act on, and losing it over a parent's casing leaves
// a truthful but useless "not a recognised config key". The value is dead in
// both spellings — a renamed key has no struct field for the decoder's
// case-insensitive fallback to land on — so the hint is right either way.
func TestRenameHintSurvivesAMiscasedParentBlock(t *testing.T) {
	cases := []string{
		"vpn.profiles[1].ifaceHint", // exact
		"VPN.profiles[1].ifaceHint", // parent miscased
		"vpn.Profiles[1].IfaceHint", // parent and leaf miscased
	}
	for _, key := range cases {
		got, tookEffect := describeUnknown(unknownKey{Key: key})
		if want := "renamed to vpn.profiles[].tunnelHint"; !strings.Contains(got, want) {
			t.Errorf("describeUnknown(%q) = %q, want it to mention %q", key, got, want)
		}
		if tookEffect {
			t.Errorf("describeUnknown(%q) reported a renamed key as live", key)
		}
	}
}

// End to end through the loader: the note a user actually reads for a miscased
// parent block must carry the replacement name.
func TestMiscasedParentBlockStillReportsTheRename(t *testing.T) {
	cfg := loadFromJSON(t, `{"VPN": {"profiles": [{"name":"a","endpoints":["1.2.3.4"],"ifaceHint":"wg"}]}}`)
	var found bool
	for _, r := range cfg.Retired {
		if !strings.Contains(r.Key, "ifaceHint") {
			continue
		}
		found = true
		if want := "vpn.profiles[].tunnelHint"; !strings.Contains(r.Reason, want) {
			t.Errorf("reason %q does not name the replacement %q", r.Reason, want)
		}
	}
	if !found {
		t.Errorf("the miscased-parent ifaceHint was not reported at all; reported: %v", retiredKeys(cfg))
	}
}
