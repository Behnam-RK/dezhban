package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

// Go's JSON decoder ignores fields it does not recognise, which is the wrong
// default for a security tool's configuration: a typo, or a key renamed by an
// upgrade, silently reverts that setting to its default. Someone who wrote
// `"redialWindow": "0"` to forbid every automatic relaxation would get the 30s
// default back without a word — a security setting accepted and discarded, which
// is the worst failure this codebase has.
//
// So the schema is walked explicitly and anything unrecognised is reported. It
// is a report rather than a hard error on purpose: refusing to start would leave
// the machine with no kill switch at all, which is a worse outcome than running
// with one setting at its default and saying so loudly.
//
// The same walk carries the mirror-image duty, which is easy to miss: the
// decoder is CASE-INSENSITIVE, so `"pollinterval"` is not dropped at all — it
// takes effect. A schema check that compared spellings exactly would call that
// key unrecognised and report "it has no effect" about a live value. Both
// directions are the one failure this file exists to prevent — the file and the
// enforcement disagreeing, silently — so the report distinguishes them rather
// than lumping every non-schema spelling together. See unknownKey.Canonical.

// renamedKeys maps keys that used to mean something to what replaced them, so a
// stale config gets a fix rather than just a complaint. Entries stay until the
// old name is long gone.
//
// A rename that changes only letter case does NOT belong here: the decoder
// honors it (see unknownKey.Canonical), so it is reported as a misspelling that
// took effect rather than as a name with no effect. `vpn.autodetect` →
// `vpn.autoDetect` is exactly that case and is deliberately absent.
var renamedKeys = map[string]string{
	"vpn.reconnectWindow":             "vpn.redialWindow",
	"vpn.advanced.reconnectWindowMax": "vpn.advanced.redialWindowMax",
	"vpn.advanced.reconnectMinUptime": "vpn.advanced.redialMinUptime",
	// Vocabulary sweep (docs/concepts/glossary.md): dropping the word
	// *interface* from vpn.profiles[].ifaceHint per the glossary's "keep tunnel,
	// drop interface" rule. describeUnknown normalises the "[N]" index away
	// before this lookup, so this one entry covers every profile.
	"vpn.profiles[].ifaceHint": "vpn.profiles[].tunnelHint",
}

// unknownKey is one key from the file that is not spelled the way the schema
// spells it, and the schema key (if any) the decoder folded it onto.
type unknownKey struct {
	Key string
	// Canonical is the schema spelling this key case-folded onto — non-empty
	// only for a key that differs from a real one by letter case alone.
	//
	// encoding/json accepts that: it prefers an exact tag match but falls back
	// to a case-insensitive one, so `"pollinterval": "1h"` sets PollInterval for
	// real. Such a key must therefore NOT be reported the way an ordinary typo
	// is. Telling an operator that a live setting "has no effect" is the same
	// class of lie as silently discarding one — the failure this file exists to
	// prevent, pointed the other way — and it is the more dangerous direction
	// for a relaxing value: someone who reads "vpn.pausemax has no effect"
	// stops looking, while a 2h pause window is in force.
	Canonical string
}

// unknownKeys reports dotted keys present in the raw config that the file schema
// does not define under that exact spelling, sorted for a stable order. The
// schema comes from the DTO's own struct tags by reflection, so it cannot drift
// from what the loader actually reads.
func unknownKeys(data []byte) []unknownKey {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		// Malformed JSON is the caller's error to report; there is nothing
		// meaningful to say about individual keys.
		return nil
	}
	var out []unknownKey
	walkUnknown(raw, reflect.TypeFor[fileConfig](), "", &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func walkUnknown(raw map[string]any, t reflect.Type, prefix string, out *[]unknownKey) {
	known := jsonFields(t)
	for name, val := range raw {
		canonical, ft, ok := known.lookup(name)
		if !ok {
			*out = append(*out, unknownKey{Key: prefix + name})
			continue
		}
		if canonical != name {
			// Only the spelling is wrong; the value is live. Reported, then
			// still walked into below — a typo nested under a miscased block
			// took effect just as much as one under a correctly-spelled block.
			*out = append(*out, unknownKey{Key: prefix + name, Canonical: prefix + canonical})
		}
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		switch v := val.(type) {
		case map[string]any:
			// Recurse into nested objects (vpn, vpn.advanced, control) so a typo
			// inside a block is caught with its full dotted path, not just its leaf.
			if ft.Kind() == reflect.Struct {
				walkUnknown(v, ft, prefix+name+".", out)
			}
		case []any:
			// Recurse into arrays of objects (vpn.profiles) the same way. Without
			// this case a typo or a renamed key inside a profile was silently
			// dropped — the exact failure this file exists to prevent, just one
			// JSON container type away from where it was already caught.
			if ft.Kind() != reflect.Slice {
				continue
			}
			elemType := ft.Elem()
			for elemType.Kind() == reflect.Ptr {
				elemType = elemType.Elem()
			}
			if elemType.Kind() != reflect.Struct {
				continue
			}
			for i, item := range v {
				if obj, ok := item.(map[string]any); ok {
					walkUnknown(obj, elemType, fmt.Sprintf("%s%s[%d].", prefix, name, i), out)
				}
			}
		}
	}
}

// schema is one struct's JSON field set, in the two forms encoding/json itself
// consults: the exact names it prefers, and the case-folded index it falls back
// to. Mirroring both is what keeps this report from contradicting the decoder.
type schema struct {
	exact map[string]reflect.Type
	// folded maps a lowercased name to its canonical spelling. Lowercasing is
	// not byte-for-byte encoding/json's fold (which also equates the Kelvin sign
	// and long s), but config keys are ASCII identifiers, where the two agree.
	folded map[string]string
}

// lookup resolves a raw JSON name to the schema field the decoder would put it
// in, returning that field's canonical spelling — equal to name for an exact
// match, different for a case-only variant.
func (s schema) lookup(name string) (canonical string, ft reflect.Type, ok bool) {
	if exact, found := s.exact[name]; found {
		return name, exact, true
	}
	folded, found := s.folded[strings.ToLower(name)]
	if !found {
		return "", nil, false
	}
	return folded, s.exact[folded], true
}

// jsonFields maps a struct's JSON names to their field types, honouring the
// `json:"name,omitempty"` form and skipping fields explicitly excluded.
func jsonFields(t reflect.Type) schema {
	s := schema{
		exact:  make(map[string]reflect.Type, t.NumField()),
		folded: make(map[string]string, t.NumField()),
	}
	var ambiguous []string
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		s.exact[name] = f.Type
		lower := strings.ToLower(name)
		if _, dup := s.folded[lower]; dup {
			// Two fields whose names differ only by case: the decoder has its
			// own tie-break, and no DTO here has such a pair. Rather than
			// mirror that rule, drop the folded entry so both keys still match
			// exactly and neither is claimed to be a misspelling of the other.
			ambiguous = append(ambiguous, lower)
			continue
		}
		s.folded[lower] = name
	}
	for _, lower := range ambiguous {
		delete(s.folded, lower)
	}
	return s
}

// arrayIndexPattern matches a []any element index in a dotted key, e.g. the
// "[2]" in "vpn.profiles[2].ifaceHint".
var arrayIndexPattern = regexp.MustCompile(`\[\d+\]`)

// describeUnknown turns one reported key into the line a user sees. Three
// distinct things can be wrong, and they need three different sentences —
// collapsing them would mean telling someone a live setting does nothing:
//
//   - wrong letter case: the value IS in effect (see unknownKey.Canonical);
//     say so, and name the spelling to change it to.
//   - renamed: name the replacement. "not recognised" alone sends someone
//     hunting through docs for a key that simply moved.
//   - anything else: not a key at all, and it has no effect.
//
// A key reported from inside an array element (e.g.
// "vpn.profiles[2].ifaceHint") keeps its index for the user — it says exactly
// which entry to fix — but is normalised to "vpn.profiles[].ifaceHint" before
// consulting renamedKeys, so one map entry covers a rename inside every element
// rather than needing one per index.
func describeUnknown(u unknownKey) string {
	if u.Canonical != "" {
		return fmt.Sprintf("the schema spells it %q; JSON key matching ignores case, so this value IS "+
			"in effect — rename it to match so the file says what it does", u.Canonical)
	}
	lookupKey := arrayIndexPattern.ReplaceAllString(u.Key, "[]")
	if to, ok := renamedKeys[lookupKey]; ok {
		return "renamed to " + to + "; the old name has no effect"
	}
	return "not a recognised config key; it has no effect"
}
