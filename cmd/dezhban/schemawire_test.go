package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/behnam-rk/dezhban/internal/config"
)

// TestSchemaWireNamesTheAppDecodes pins the JSON field names `config schema --json`
// emits, because the macOS app decodes them by name and fails silently if one moves.
//
// `ConfigTunable`'s CodingKeys name `docAnchor` and `docKeyAnchor`, and
// `docKeyAnchor` is decoded with `decodeIfPresent` so an older CLI degrades to the
// section anchor. That tolerance is what would make a rename invisible: rename the Go
// tag and every key silently loses its row anchor, the `?` goes back to landing on
// section headings, and nothing on either side fails — the Swift test builds its
// fixture by hand, so it would keep passing too.
//
// Asserted against `schemaEntry`, not `config.Tunable`: the wire shape is the CLI's
// wrapper, which embeds the Tunable and adds `preset`. Marshalling the Tunable alone
// omits that field, so a test written against it both misses a name the app decodes
// and fails for a reason that is not a defect. This is where the tag would be
// changed, so this is where the assertion belongs.
func TestSchemaWireNamesTheAppDecodes(t *testing.T) {
	tunables := config.Tunables()
	if len(tunables) == 0 {
		t.Fatal("no tunables — this test is pinning nothing")
	}

	entries := make([]schemaEntry, len(tunables))
	written := presetWritten()
	for i, tun := range tunables {
		entries[i] = schemaEntry{Tunable: tun, Preset: written[tun.Key]}
	}

	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Every name in ConfigSchema.swift's CodingKeys, so a Go-side rename of any of
	// them fails here rather than degrading the app in silence.
	required := []string{
		"key", "label", "kind", "default", "disablable", "advanced", "preset",
		"help", "docAnchor",
	}
	for i, obj := range decoded {
		for _, name := range required {
			if _, ok := obj[name]; !ok {
				t.Errorf("%s: no %q field on the wire", tunables[i].Key, name)
			}
		}
	}

	// docKeyAnchor is omitempty, so it is absent exactly for the keys documented in
	// prose — and present, naming a row on the reference page, for the rest.
	withRow := 0
	for i, obj := range decoded {
		key := tunables[i].Key
		frag, present := obj["docKeyAnchor"]
		if tunables[i].DocKeyAnchor == "" {
			if present {
				t.Errorf("%s: docKeyAnchor should be omitted when empty, got %s", key, frag)
			}
			continue
		}
		if !present {
			t.Errorf("%s: docKeyAnchor missing from the wire", key)
			continue
		}
		var s string
		if err := json.Unmarshal(frag, &s); err != nil {
			t.Errorf("%s: docKeyAnchor is not a string: %v", key, err)
			continue
		}
		if !strings.Contains(s, "#key-") {
			t.Errorf("%s: docKeyAnchor is %q, which does not name a key row", key, s)
		}
		withRow++
	}
	if withRow == 0 {
		t.Fatal("no tunable carried a row anchor — the app would have nothing to deep-link to")
	}
}
