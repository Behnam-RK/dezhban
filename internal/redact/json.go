package redact

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// JSON rewrites a JSON document by WALKING it, rather than by matching patterns
// against its text.
//
// Text's patterns are right for a log line or a rendered ruleset, and wrong for
// a document with structure. Asked to do this job they had to reason about
// escaping, about which array a key sat in, and about where a value ended — and
// each of those was got wrong in turn: a match that started inside a
// `\u003c` escape left the document unparseable; brackets balanced by hand FAILED OPEN
// when a profile name contained one, so every name in the file shipped verbatim;
// an unescaped body written back into an escaped position produced a literal tab
// inside a JSON string. None of those are reachable here. The document is parsed
// once, values are rewritten as values, keys decide what a value means, and
// encoding/json re-escapes on the way out.
//
// A document that does not parse falls back to Text. `report` copies config.json
// off disk verbatim and never validates it, so a hand-edited config that no
// longer parses is exactly the host a bundle gets collected from — the worst
// possible moment to give up and redact nothing.
func (r *Redactor) JSON(s string) string {
	if !r.Enabled {
		return s
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return r.Text(s)
	}
	doc, err := decodeValue(dec, tok)
	if err != nil {
		return r.Text(s)
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	// Off here and in marshalPlain: the default rewrites <, > and & as `\u003c`
	// and friends, which is where doctor's `<name>` placeholders got the escapes
	// that a text pattern then matched into and destroyed.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r.walk(doc, nil)); err != nil {
		return r.Text(s)
	}
	return buf.String()
}

// object preserves an object's key ORDER, which encoding/json's map does not.
// A config.json whose keys came back alphabetised is a bundle that does not look
// like the file the reader has open, and "why does this not match mine?" is
// exactly the question a diagnostic bundle exists to prevent.
type object struct {
	keys []string
	vals map[string]any
}

func (o *object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := marshalPlain(k)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		val, err := marshalPlain(o.vals[k])
		if err != nil {
			return nil, err
		}
		b.Write(val)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// marshalPlain encodes without HTML escaping.
//
// json.Marshal ALWAYS rewrites <, > and & as `\u003c` and friends, and the
// encoder's SetEscapeHTML(false) does not reach bytes a custom MarshalJSON
// returned — so doctor's `<name>` placeholders came back escaped even with it
// off. Valid JSON either way, and it decodes to the same string; this is only
// so the raw file in the bundle reads the way the terminal does.
func marshalPlain(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// decodeValue reads one value, given the token that opened it.
func decodeValue(dec *json.Decoder, tok json.Token) (any, error) {
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch delim {
	case '{':
		o := &object{vals: map[string]any{}}
		for {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			if d, ok := kt.(json.Delim); ok && d == '}' {
				return o, nil
			}
			key, ok := kt.(string)
			if !ok {
				return nil, errNotAKey
			}
			vt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := decodeValue(dec, vt)
			if err != nil {
				return nil, err
			}
			// A duplicate key keeps its first position, as the last value.
			if _, seen := o.vals[key]; !seen {
				o.keys = append(o.keys, key)
			}
			o.vals[key] = v
		}
	case '[':
		list := []any{}
		for {
			vt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			if d, ok := vt.(json.Delim); ok && d == ']' {
				return list, nil
			}
			v, err := decodeValue(dec, vt)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
	}
	return nil, errNotAKey
}

var errNotAKey = jsonError("malformed JSON")

type jsonError string

func (e jsonError) Error() string { return string(e) }

// walk rewrites every string in the document. path is the chain of keys down to
// v, so a value's KEY decides what it is — which is the whole reason this exists
// rather than a pattern that has to guess from the text around it. An array's
// elements share the array's own path, because `"endpoints": ["a"]` says what
// "a" is just as plainly as `"addr": "a"` does.
func (r *Redactor) walk(v any, path []string) any {
	switch t := v.(type) {
	case *object:
		for _, k := range t.keys {
			// A fresh slice per child: `append(path, k)` would hand every
			// sibling the same backing array, and a path is read after the
			// call that built it.
			child := make([]string, len(path), len(path)+1)
			copy(child, path)
			t.vals[k] = r.walk(t.vals[k], append(child, k))
		}
		return t
	case []any:
		for i := range t {
			t[i] = r.walk(t[i], path)
		}
		return t
	case string:
		return r.value(t, path)
	default:
		return v
	}
}

// value rewrites one string, by the key it sits under.
func (r *Redactor) value(s string, path []string) string {
	if s == "" {
		return s
	}
	key := ""
	if len(path) > 0 {
		key = path[len(path)-1]
	}
	switch key {
	case "activeProfile", "profile":
		return r.name(s, "profile")
	case "tunnelHint":
		return r.name(s, "hint")
	case "name":
		// Only inside the arrays that hold profiles. doctor.json gives every
		// CHECK a name, and those are dezhban's own vocabulary — redacting them
		// replaces the diagnosis rather than the identity. Here that is a fact
		// about the path rather than a regex balancing brackets, so a profile
		// name containing one cannot make the scoping vanish.
		if within(path, "profiles") || within(path, "entries") {
			return r.name(s, "profile")
		}
		return r.free(s)
	case "addr", "endpoint", "endpoints", "ip", "ipv6":
		return r.endpointOrFree(s)
	case "fixes":
		// dezhban's own command text, never user data — every Fixes entry in
		// the doctor is a literal template with nothing interpolated into it.
		// Running the shape passes over it turned `vpn.endpoints` into a
		// placeholder and made the suggested command nonsense. It still gets
		// the known-name pass, so a name that reached it anyway still goes.
		return r.knownNames(s)
	default:
		return r.free(s)
	}
}

// endpointOrFree redacts a value whose key says it names a server. A value that
// is not a bare address or name — a URL, a host:port — falls through to the
// shape passes, which read those correctly and keep the allow-listed provider
// host inside them.
func (r *Redactor) endpointOrFree(s string) string {
	if red := r.endpoint(s); red != s {
		return red
	}
	return r.free(s)
}

// free rewrites a string that no key identifies: the shape passes, then the
// names this bundle has already replaced elsewhere.
func (r *Redactor) free(s string) string {
	return r.knownNames(r.Text(s))
}

// knownNames replaces profile and hint names this Redactor has already minted,
// wherever they appear as words in free text.
//
// This is what reaches the names no shape and no key can see: the doctor writes
// learned entry names — which are profile names — into its Details and Summary
// as ordinary prose ("every learned address for work-nord has aged out"), and
// nothing was looking at them. It works because config.json and learned.json are
// collected BEFORE doctor.json, so by the time the prose is walked the names are
// known. Keep that order in cmd/dezhban/report.go.
//
// Longest first, so a name that is a prefix of another cannot claim it.
func (r *Redactor) knownNames(s string) string {
	if len(r.order) == 0 {
		return s
	}
	type named struct{ value, token string }
	var names []named
	for _, key := range r.order {
		kind, value, _ := strings.Cut(key, ":")
		if (kind == "profile" || kind == "hint") && value != "" {
			names = append(names, named{value, r.seen[key]})
		}
	}
	// Longest first, so a name that is a prefix of another cannot claim it.
	sort.Slice(names, func(i, j int) bool { return len(names[i].value) > len(names[j].value) })
	for _, n := range names {
		if !strings.Contains(strings.ToLower(s), n.value) {
			continue
		}
		re, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(n.value) + `\b`)
		if err != nil {
			continue
		}
		s = re.ReplaceAllLiteralString(s, n.token)
	}
	return s
}

// within reports whether key appears anywhere in the path.
func within(path []string, key string) bool {
	for _, p := range path {
		if p == key {
			return true
		}
	}
	return false
}
