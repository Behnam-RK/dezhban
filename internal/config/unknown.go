package config

import (
	"encoding/json"
	"reflect"
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

// renamedKeys maps keys that used to mean something to what replaced them, so a
// stale config gets a fix rather than just a complaint. Entries stay until the
// old name is long gone.
var renamedKeys = map[string]string{
	"vpn.reconnectWindow":              "vpn.redialWindow",
	"vpn.advanced.reconnectWindowMax":  "vpn.advanced.redialWindowMax",
	"vpn.advanced.reconnectMinUptime":  "vpn.advanced.redialMinUptime",
	"vpn.advanced.reconnectWindowsMax": "vpn.advanced.redialWindowMax",
}

// unknownKeys reports dotted keys present in the raw config that the file schema
// does not define, sorted for a stable order. The schema comes from the DTO's
// own struct tags by reflection, so it cannot drift from what the loader
// actually reads.
func unknownKeys(data []byte) []string {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		// Malformed JSON is the caller's error to report; there is nothing
		// meaningful to say about individual keys.
		return nil
	}
	var out []string
	walkUnknown(raw, reflect.TypeFor[fileConfig](), "", &out)
	sort.Strings(out)
	return out
}

func walkUnknown(raw map[string]any, t reflect.Type, prefix string, out *[]string) {
	known := jsonFields(t)
	for name, val := range raw {
		ft, ok := known[name]
		if !ok {
			*out = append(*out, prefix+name)
			continue
		}
		// Recurse into nested objects (vpn, vpn.advanced, control) so a typo
		// inside a block is caught with its full dotted path, not just its leaf.
		nested, isObj := val.(map[string]any)
		if !isObj {
			continue
		}
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			walkUnknown(nested, ft, prefix+name+".", out)
		}
	}
}

// jsonFields maps a struct's JSON names to their field types, honouring the
// `json:"name,omitempty"` form and skipping fields explicitly excluded.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	out := make(map[string]reflect.Type, t.NumField())
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
		out[name] = f.Type
	}
	return out
}

// describeUnknown turns a dotted key into the line a user sees, naming the
// replacement when the key was renamed rather than merely mistyped.
func describeUnknown(key string) string {
	if to, ok := renamedKeys[key]; ok {
		return "renamed to " + to + "; the old name has no effect"
	}
	return "not a recognised config key; it has no effect"
}
