package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Defaults are data (see schema.go), but documentation is prose, and prose is
// where the drift lived: the config reference and the example configs restate
// values that nothing forces to agree with the code. These tests are that force.
//
// They run inside `go test ./...`, so both CI legs catch a stale table — this is
// cheaper and earlier than a CI-only check, and it means editing a default
// without editing its documentation fails on the contributor's own machine.

const (
	docConfigRef = "../../docs/usage/config.md"
	exampleDir   = "../../configs"
)

// docDefaultIsProse names keys whose Default column deliberately says something
// other than the literal value, with the reason. Anything NOT listed here must
// state its default verbatim, so a new prose default fails the build until
// somebody justifies it rather than quietly opting out of the check.
var docDefaultIsProse = map[string]string{
	"providers": "eight URLs would not fit a table cell; the Notes column names them in order instead",
	"control.socket": "the code default is empty, meaning 'resolve it against the daemon's state dir'; " +
		"the table documents the resolved path, which is what a reader needs",
	"control.group": "genuinely platform-dependent (admin on macOS, empty elsewhere), so there is no " +
		"single literal to state",
}

// TestDocumentedDefaultsMatchTheCode is the drift check. A default stated in the
// config reference that disagrees with the shipped default is worse than no
// documentation: a reader acts on it.
func TestDocumentedDefaultsMatchTheCode(t *testing.T) {
	t.Parallel()
	documented := parseDocDefaults(t, docConfigRef)

	for _, tun := range Tunables() {
		cell, ok := documented[tun.Key]
		if !ok {
			t.Errorf("%s is settable but has no row in %s — every key a user can set must be documented",
				tun.Key, docConfigRef)
			continue
		}

		got, parsed := normalizeDocDefault(cell)
		if reason, prose := docDefaultIsProse[tun.Key]; prose {
			// A stale exemption is its own kind of rot: if the cell now states
			// the value exactly, the exemption is no longer buying anything.
			if parsed && got == tun.Default {
				t.Errorf("%s: documented default now matches the code exactly, so its "+
					"docDefaultIsProse exemption (%q) should be removed", tun.Key, reason)
			}
			continue
		}

		if !parsed {
			t.Errorf("%s: Default column %q is not a literal value. State the default, "+
				"or add the key to docDefaultIsProse with a reason.", tun.Key, cell)
			continue
		}
		if !sameDefault(tun, got) {
			t.Errorf("%s: %s says the default is %q, the code says %q",
				tun.Key, docConfigRef, got, tun.Default)
		}
	}

	for key := range docDefaultIsProse {
		if _, ok := TunableByKey(key); !ok {
			t.Errorf("docDefaultIsProse exempts %q, which is not a settable key", key)
		}
	}
}

// TestExampleConfigsUseOnlyKnownKeys keeps the shipped examples honest. They are
// the first config most people ever see, and a copied-out example that sets a
// renamed or retired key silently does nothing — the exact failure the
// reconnect→redial rename was designed to make loud everywhere else.
//
// The check is "every key is real", not "every value is the default": an example
// exists precisely to show non-default values, so equality would be the wrong
// assertion. What must never happen is an example naming a key that no longer
// takes effect.
func TestExampleConfigsUseOnlyKnownKeys(t *testing.T) {
	t.Parallel()
	paths, err := filepath.Glob(filepath.Join(exampleDir, "*.json"))
	if err != nil {
		t.Fatalf("glob %s: %v", exampleDir, err)
	}
	if len(paths) == 0 {
		t.Fatalf("no example configs found under %s", exampleDir)
	}

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			for _, u := range unknownKeys(data) {
				reason, tookEffect := describeUnknown(u)
				if tookEffect {
					// A renamed key that still works: worth saying, not failing.
					t.Logf("%s: %s", u.Key, reason)
					continue
				}
				t.Errorf("sets %q, which does nothing: %s", u.Key, reason)
			}

			// It must also still load and validate — an example that cannot be
			// used is not an example.
			if _, err := Load(path); err != nil {
				t.Errorf("does not load: %v", err)
			}
		})
	}
}

// sameDefault compares a documented default to the declared one. Durations are
// compared as durations, not as strings: the tables read "30m" while Go's
// Duration.String renders "30m0s", and those are the same value. Forcing the
// prose to say "30m0s" would make the documentation worse to serve the test,
// which is the wrong way round.
func sameDefault(tun Tunable, documented string) bool {
	if tun.Kind != KindDuration {
		return documented == tun.Default
	}
	want, errWant := parseDuration(tun.Default)
	got, errGot := parseDuration(documented)
	if errWant != nil || errGot != nil {
		return documented == tun.Default
	}
	return want == got
}

// parseDuration accepts the two forms a default can take: a Go duration, or the
// literal "off" KeyValues renders for the Disabled sentinel.
func parseDuration(v string) (time.Duration, error) {
	if v == "off" {
		return Disabled, nil
	}
	return time.ParseDuration(v)
}

// parseDocDefaults reads every markdown table in the reference and returns the
// Default cell for each documented field, keyed by the dotted config key.
//
// Columns are located by header name rather than by position, because the tables
// genuinely differ: the field tables are Field/Type/Default/Notes while the
// advanced table is Field/Default/What it controls.
func parseDocDefaults(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	out := map[string]string{}
	fieldCol, defaultCol := -1, -1
	prefix := ""

	for rawLine := range strings.SplitSeq(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "|") {
			// A heading sets the key prefix for the tables under it: the
			// advanced table lists bare field names (`switchWindowMax`) because
			// its heading already says which block they live in.
			if strings.HasPrefix(line, "#") {
				prefix = ""
				if strings.Contains(line, "vpn.advanced") {
					prefix = "vpn.advanced."
				}
			}
			// Any non-table line ends the current table, so a Default column
			// index can never leak from one table into the next.
			fieldCol, defaultCol = -1, -1
			continue
		}
		cells := splitRow(line)

		// A separator row (|---|---|) means the row above was the header.
		if isSeparator(cells) {
			continue
		}
		if fieldCol == -1 {
			fieldCol, defaultCol = findColumns(cells)
			continue
		}
		if defaultCol == -1 || fieldCol >= len(cells) || defaultCol >= len(cells) {
			continue
		}

		key := strings.Trim(strings.TrimSpace(cells[fieldCol]), "`")
		if key == "" {
			continue
		}
		// A bare name under a prefixed heading is that block's field; a name
		// that is already dotted is used as written.
		if _, known := TunableByKey(key); !known && prefix != "" {
			key = prefix + key
		}
		// First table wins: a key documented twice keeps its primary row, and
		// the duplicate is reported rather than silently shadowing it.
		if prev, dup := out[key]; dup && prev != cells[defaultCol] {
			t.Errorf("%s: %s is documented twice with different defaults (%q and %q)",
				path, key, prev, cells[defaultCol])
			continue
		}
		out[key] = cells[defaultCol]
	}
	return out
}

// splitRow splits a markdown table row into its cells, dropping the empty
// leading and trailing fields the surrounding pipes produce.
func splitRow(line string) []string {
	parts := strings.Split(strings.Trim(line, "|"), "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func isSeparator(cells []string) bool {
	for _, c := range cells {
		if c == "" || strings.Trim(c, "-: ") != "" {
			return false
		}
	}
	return len(cells) > 0
}

// findColumns locates the Field and Default columns in a header row, returning
// -1 for a table that has neither (so it is skipped entirely).
func findColumns(header []string) (fieldCol, defaultCol int) {
	fieldCol, defaultCol = -1, -1
	for i, h := range header {
		switch strings.ToLower(strings.TrimSpace(h)) {
		case "field", "key":
			fieldCol = i
		case "default":
			defaultCol = i
		}
	}
	if defaultCol == -1 {
		return -1, -1
	}
	return fieldCol, defaultCol
}

// normalizeDocDefault turns a documented Default cell into the same rendering
// KeyValues produces, so the two are directly comparable. The bool is false when
// the cell is not a literal value at all (an em dash, prose, or empty).
func normalizeDocDefault(cell string) (string, bool) {
	v := strings.TrimSpace(cell)
	v = strings.TrimSpace(strings.Trim(v, "`"))
	if v == "" || v == "—" || v == "-" {
		return "", false
	}

	// Lists render comma-joined with no spaces, matching KeyValues/joinInts.
	if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
		var strs []string
		if err := json.Unmarshal([]byte(v), &strs); err == nil {
			return strings.Join(strs, ","), true
		}
		var nums []int
		if err := json.Unmarshal([]byte(v), &nums); err == nil {
			parts := make([]string, len(nums))
			for i, n := range nums {
				parts[i] = strconv.Itoa(n)
			}
			return strings.Join(parts, ","), true
		}
		return "", false
	}

	// Durations and strings are quoted in the tables ("15s", "info"); ints and
	// bools are bare.
	if unquoted, err := strconv.Unquote(v); err == nil {
		return unquoted, true
	}
	if strings.ContainsAny(v, " ") {
		return v, false // prose, e.g. "8 geo-IP URLs"
	}
	return v, true
}
