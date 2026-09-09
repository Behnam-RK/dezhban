package redact

import (
	"bytes"
	"encoding/json"
	"io"
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
	// The document has to be ONE value and nothing else. A file with trailing
	// content decoded to its first value and the rest was DROPPED — so the
	// bundle showed a config that is not the file on disk, which is a
	// diagnostic lie however well the part that survived was redacted. Hand
	// those to Text, which reads the whole thing.
	if _, err := dec.Token(); err != io.EOF {
		return r.Text(s)
	}

	// Everything minted from here on is discarded if the encode fails, because
	// the fallback re-reads the ORIGINAL text: without this the legend would
	// count tokens that appear nowhere in the bundle and push every real token's
	// ordinal past them.
	mark := len(r.order)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	// Off here and in marshalPlain: the default rewrites <, > and & as `\u003c`
	// and friends, which is where doctor's `<name>` placeholders got the escapes
	// that a text pattern then matched into and destroyed.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r.walk(doc, nil)); err != nil {
		r.rollback(mark)
		return r.Text(s)
	}
	return buf.String()
}

// rollback forgets every placeholder minted since mark, for a pass whose output
// was thrown away.
func (r *Redactor) rollback(mark int) {
	for _, key := range r.order[mark:] {
		delete(r.seen, key)
	}
	r.order = r.order[:mark]
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
		// state.json's tunnels[].name is an INTERFACE name, and the generic ones
		// are the kernel's vocabulary rather than the user's.
		if within(path, "tunnels") {
			return r.ifaceName(s)
		}
		// A `name` outside those arrays is dezhban's own vocabulary — a doctor
		// CHECK name — so it gets the shape passes and NOT knownNames. A user
		// whose profile is called `config` would otherwise have the check named
		// `config` replaced with that profile's token: the exact over-redaction
		// the scoping above exists to prevent, walking back in through the
		// literal-name pass. Prose still gets knownNames, where mangling a
		// common word is noise and the leak it closes is not.
		return r.Text(s)
	case "tunnelInterfaces", "iface":
		// learned.json records the interface it observed under `iface`. The same
		// value is redacted under tunnelInterfaces and tunnels[].name, so leaving
		// this key out meant one document redacted a provider-named interface and
		// the one beside it did not.
		return r.ifaceName(s)
	case "addr", "endpoint", "endpoints", "ip", "ipv6":
		return r.endpointOrFree(s)
	case "fixes":
		// A fix is a command someone RUNS, so `vpn.endpoints` and `wg0.conf`
		// have to survive it — the hostname shape claimed both and made the
		// suggested command nonsense.
		//
		// Not "no redaction", though. The premise that a fix never carries user
		// data was WRONG when it was written: buildEndpointsCheck interpolated
		// the offending endpoint, so one check redacted an address in Details
		// and shipped it verbatim here. That is fixed at the source, and the
		// address passes stay as the belt — they cost nothing a fix command
		// needs, and they are what catches the next interpolation.
		return r.addresses(s)
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
	// A bare name carrying a port — `se-sto-01:51820` — is not a bare token, so
	// endpoint declines it, and hostRe needs a dot, so the shape passes decline
	// it too. It fell through both and shipped verbatim. Split the port off and
	// ask again, keeping the port: a port is structural, and which one the
	// tunnel uses is a real diagnostic.
	if host, port, ok := splitPort(s); ok {
		if red := r.endpoint(host); red != host {
			return red + ":" + port
		}
	}
	return r.free(s)
}

// splitPort separates a trailing :port from a value that is not itself an
// address. An IPv6 literal is full of colons and is never split.
func splitPort(s string) (host, port string, ok bool) {
	i := strings.LastIndexByte(s, ':')
	if i <= 0 || i == len(s)-1 || strings.Count(s, ":") != 1 {
		return "", "", false
	}
	host, port = s[:i], s[i+1:]
	if !isAllDigits(port) {
		return "", "", false
	}
	return host, port, true
}

// free rewrites a string that no key identifies: the shape passes, then the
// names this bundle has already replaced elsewhere.
func (r *Redactor) free(s string) string {
	// knownNames FIRST. Its values are known identities; hostRe is a guess. Run
	// the guess first and a profile called `nord.vpn` is minted as `host-N` in
	// prose and `profile-N` in the config — one identifier, two tokens, two
	// legend lines, and no way for a reader to see they are the same server.
	return r.Text(r.knownNames(s))
}

// knownNames replaces profile and hint names this Redactor has already minted,
// wherever they appear as words in free text.
//
// This is what reaches the names no shape and no key can see: the doctor writes
// learned entry names — which are profile names — into its Details and Summary
// as ordinary prose ("every learned address for work-nord has aged out"), and
// nothing was looking at them.
//
// HOSTNAMES are in here for the same reason and it is not redundant with hostRe:
// a single-label endpoint has no dot, so the shape cannot see it, and the daemon
// logs it twice — `host=mullvad err="lookup mullvad: no such host"`. The
// field-aware pass replaces the attr and the copy inside the error text is left
// standing. Only hosts this redactor actually replaced are in here; an
// allow-listed provider never reaches the map. It works because config.json and learned.json are
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
		if (kind == "profile" || kind == "hint" || kind == "host") && value != "" {
			names = append(names, named{value, r.seen[key]})
		}
	}
	// Longest first, so a name that is a prefix of another cannot claim it.
	sort.Slice(names, func(i, j int) bool { return len(names[i].value) > len(names[j].value) })
	for _, n := range names {
		if reserved[n.value] || !strings.Contains(strings.ToLower(s), n.value) {
			continue
		}
		// Not `\b`. config accepts a profile name of [A-Za-z0-9._-] (config.go's
		// profile validation), and `\b` cannot match at a non-word-to-non-word
		// edge — so a name spelled `-work-` or `.home` had no boundary at either
		// end and stayed verbatim in every prose string. The boundary is the
		// name's OWN alphabet: a match must not be flanked by another character
		// that could be part of one.
		re, err := regexp.Compile(`(?i)(^|[^A-Za-z0-9._-])` + regexp.QuoteMeta(n.value) + `($|[^A-Za-z0-9._-])`)
		if err != nil {
			continue
		}
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			sub := re.FindStringSubmatch(m)
			return sub[1] + n.token + sub[2]
		})
	}
	return s
}

// reserved are the words this pass may never claim, whatever a profile is
// called. They are dezhban's own stable identifiers — the posture and mode
// strings CLAUDE.md forbids renaming, the placeholder kinds, and the doctor's
// check names — so a user whose profile is called `guard` would otherwise have
// `"posture": "guard"` rewritten to `"posture": "profile-1"`, which is the
// over-redaction the path scoping exists to prevent arriving by another road.
// Failing to redact one profile whose name collides with dezhban's vocabulary
// is noise in a bundle; rewriting the vocabulary is a broken diagnosis.
//
// This pass cannot launder its own tokens, but that is `free`'s ordering rather
// than anything here: knownNames runs on the value's ORIGINAL text, before Text
// has minted a placeholder into it, so there is never a token present for a name
// to match inside.
var reserved = map[string]bool{
	"guard": true, "full-block": true, "switch-window": true, "standby": true,
	"stopped": true, "fullblock": true, "switch": true, "pause": true,
	"config": true, "tunnels": true, "endpoints": true, "lockout": true,
	"service": true, "liveness": true, "control": true, "armatboot": true,
	"endpointretention": true,
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
