// Package redact replaces the network identifiers in a diagnostic bundle with
// stable placeholders, so the bundle can be pasted into a public issue.
//
// What is sensitive here is specific: the VPN server addresses in the config and
// in learned.json (they name the provider, often the exact server), and the
// public exit IP in state.json (that is the user's location). A firewall
// ruleset carries both, because the whole point of the ruleset is which
// addresses may be reached.
//
// Several identifiers in a bundle are not address-shaped and are just as
// telling, and each gets placeholders of its own kind: the PROFILE NAMES the
// user chose (they are called "mullvad-de"), a profile's TUNNEL HINT, the
// TUNNEL INTERFACE NAME the VPN client created ("nordlynx" names the provider
// as plainly as a server does, while the kernel's own `utun4`/`en0` name
// nobody), the VPN's SERVICE NAME from the OS network settings, and the ACCOUNT
// NAME in every home-directory path.
//
// **Stable** placeholders, not `[redacted]`: the same address becomes the same
// placeholder everywhere it appears, so the bundle stays diagnosable. "The rules
// pass ip-1 but the endpoint is ip-2" is the finding; with every address flattened
// to one token it would be invisible.
//
// The rule this package lives by: it must never claim to have redacted something
// it did not. A redactor that misses a field is worse than no redactor at all,
// because it advertises a safety it did not deliver — so this works by finding
// address-shaped and hostname-shaped text everywhere, in every file, rather than
// by knowing which fields of which struct to blank. Anything it is unsure about
// is redacted.
//
// TWO RESIDUALS, stated rather than implied, because the rule above means the
// limits have to be written down as plainly as the coverage.
//
//  1. A real identifier SPELLED like an already-minted token, arriving after that
//     mint, keeps its spelling — see placeholder. Nothing can move a token that
//     earlier entries already carry. The bound: only `<kind>-<digits>` can
//     collide, and that spelling names no provider and no person.
//
//  2. An interface known ONLY to the live host — autodetect with no
//     vpn.tunnelInterfaces, and no daemon, so no state.json — is minted by
//     nothing before rules-preview.txt and doctor.json are walked, and the name
//     replay has nothing to replay. With the daemon running, state.json's
//     tunnels[].name closes it, which is why the bundle's collection order
//     (cmd/dezhban/report.go) is load-bearing. Closing it outright means either a
//     field-aware pass over the three rendering grammars dezhban emits
//     (`on { … }`, `oifname { … }`, `-InterfaceAlias …`) or seeding this
//     Redactor from the resolved policy — the second risks minting tokens for
//     values no entry ends up carrying, which is the legend overcount
//     redactEntry's comment warns about.
package redact

import (
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	// A run of dot-separated numbers, with an optional /prefix. Deliberately
	// loose, and deliberately GREEDY about the number of groups: matching the
	// whole run means "1.2.3.4.5" arrives here in one piece and fails
	// netip.ParseAddr, rather than having its first four octets replaced and a
	// stray ".5" left behind. netip is what decides whether a match is an
	// address at all.
	ipv4Re = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){2,}(?:/\d{1,2})?\b`)
	// IPv6, including the compressed forms. Loose for the same reason.
	//
	// The leading group is the boundary, and it is spelled out rather than
	// written `\b` because `\b` is the wrong boundary for this alphabet: there
	// is no word boundary in front of a leading `::`, so `::ffff:cb00:7107`
	// matched only its `ffff:cb00:7107` tail, which is not a parseable address —
	// and an unparseable match is returned VERBATIM, so the whole address
	// survived a bundle that said it had been redacted. A trailing `\b` failed
	// the mirror case, `2001:db8::`, for the same reason. The class excludes
	// what an address is made of, so a match still cannot start mid-address.
	ipv6Re = regexp.MustCompile(`(^|[^0-9A-Za-z:.])((?:[0-9A-Fa-f]{0,4}:){2,7}[0-9A-Fa-f]{0,4}(?:%[0-9A-Za-z._-]+)?(?:/\d{1,3})?)`)
	// A dotted name with a TLD-ish last label. Matched after addresses so a
	// dotted quad is never mistaken for one.
	//
	// Narrower than the endpoint grammar config accepts, deliberately: widening
	// the last label to admit digits would swallow the fractional seconds of
	// every timestamp in log.txt. The names this shape cannot see — a
	// single-label host, a last label with digits — are reached by the
	// field-aware endpoint pass below instead.
	hostRe = regexp.MustCompile(`\b(?:[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?\.)+[A-Za-z]{2,}\b`)
	// The identity-bearing NAMES, which are not address-shaped at all: a profile
	// is called "mullvad-de" or "work-nord", and a tunnel hint is "nordlynx" or
	// "proton". Those name the provider as plainly as its server address does,
	// and no shape-based rule can see them — they are ordinary words.
	//
	// Field-aware, which the shape-based passes above deliberately are not. That
	// is not a retreat from the rule at the top of this file: shape-matching is
	// what makes the redactor complete, and this pass only ever REMOVES more. A
	// key added to the config and not here is redacted by nothing, which is the
	// failure to watch for — keep this in step with config.Profile and
	// state.Snapshot.
	// `name` is scoped to the arrays that hold profiles — config's `profiles`
	// and learned.json's `entries` — and is NOT matched as a bare key, because
	// `"name"` is not a profile's private word: doctor.json gives every CHECK a
	// name, and matching those turned the one file a reader opens first into
	// nine `profile-N` rows, while telling the legend the host had nine VPN
	// profiles when it had none. A check name is dezhban's own vocabulary,
	// identical on every install, exactly like the geo providers it keeps.
	// Scoped by the PRESENCE of the key, not by finding the array's extent.
	// Balancing brackets with a regex was tried and fails open — a `[` inside a
	// name made the whole match vanish and every name in the document shipped
	// verbatim, a `]` truncated the body and every name after it did, and two
	// levels of nesting did the same. Failing open is the one direction this
	// package may never fail, and no amount of hardening that pattern gets
	// there: RE2 cannot count brackets. So the gate is only whether the
	// document is one that HAS profiles, and every `"name"` in such a document
	// is a profile or entry name. doctor.json carries neither key, which is the
	// whole reason the scoping exists.
	profileDocRe  = regexp.MustCompile(`"(?:profiles|entries)"\s*:\s*\[`)
	profileNameRe = regexp.MustCompile(`("name"\s*:\s*")((?:[^"\\]|\\.)+)(")`)
	// `profile` is in here because state.json writes the switch window's profile
	// under exactly that key (state.SwitchState.Profile) — and a window being
	// open is the likeliest moment for someone to collect a bundle, so the one
	// file most likely to carry a profile name was the one nothing looked at.
	profileJSONRe = regexp.MustCompile(`("(?:activeProfile|profile)"\s*:\s*")((?:[^"\\]|\\.)+)(")`)
	profileAttrRe = regexp.MustCompile(`\b(profile|activeProfile)=("(?:[^"\\]|\\.)*"|[^\s]+)`)
	// A tunnel hint gets its OWN kind, not the profile namespace. It is
	// display-only (docs/usage/config.md) and it is an interface-name PREFIX —
	// `utun`, `wg`, `nordlynx` — matched against the live interface. Counting
	// hints as profile names made the legend claim VPN profiles the host does
	// not have, the same overcount the doctor.json narrowing was made to stop,
	// and it hid whether the hint matches the `utun4` that rules-preview.txt
	// keeps in plain sight, which is the only reason the field exists.
	hintJSONRe = regexp.MustCompile(`("tunnelHint"\s*:\s*")((?:[^"\\]|\\.)+)(")`)
	hintAttrRe = regexp.MustCompile(`\btunnelHint=("(?:[^"\\]|\\.)*"|[^\s]+)`)
	// The VPN endpoints, by field. config.isPlausibleHostname accepts names the
	// shape passes above cannot see: a single label ("mullvad") has no dot at
	// all, and a last label carrying digits ("vpn.123abc") is a valid endpoint
	// but not a hostRe match. Those are the provider's own server names — the
	// exact thing this package exists to hide — so they get a field-aware pass
	// of their own, on the same terms as the profile names: it only ever
	// REMOVES more. Keep in step with config.fileVPN, config.fileProfile,
	// state.Snapshot and learned.Endpoint.
	//
	// The array body excludes braces and brackets so this matches the ARRAY OF
	// STRINGS config and state.json write, and never learned.json's array of
	// objects — whose own address field is endpointJSONRe's job.
	// The closing bracket is OPTIONAL, and that is the point rather than a
	// nicety. Text is the fallback for a document that did not parse, and the
	// commonest way a config fails to parse is being cut off mid-array —
	// `{"vpn":{"endpoints":["mullvad"`. Requiring the `]` meant that exact input
	// matched nothing here, and a single-label endpoint matches no shape either,
	// so the fallback returned the file VERBATIM. A fallback that fails open is
	// worse than no fallback.
	endpointsJSONRe = regexp.MustCompile(`("endpoints"\s*:\s*\[)([^\[\]{}]*)(\]?)`)
	endpointJSONRe  = regexp.MustCompile(`("(?:addr|endpoint)"\s*:\s*")((?:[^"\\]|\\.)+)(")`)
	endpointAttrRe  = regexp.MustCompile(`\b(endpoint|host)=("(?:[^"\\]|\\.)*"|[^\s]+)`)
	// Interfaces by field, for the text path and for a document that did not
	// parse. Closing bracket optional for the same reason the endpoints pattern
	// makes it optional: a config cut off mid-array is exactly what reaches
	// Text, and requiring the `]` there left `tunnelInterfaces:["nordlynx"`
	// untouched.
	ifacesJSONRe = regexp.MustCompile(`("tunnelInterfaces"\s*:\s*\[)([^\[\]{}]*)(\]?)`)
	ifaceJSONRe  = regexp.MustCompile(`("iface"\s*:\s*")((?:[^"\\]|\\.)+)(")`)
	// Only `iface=`. `tunnel=` was in here with no producer anywhere in the
	// tree — the Windows preview writes `-DisplayName 'dezhban-tunnel'`, not an
	// attr — and a pattern matching a key the daemon never writes is untested
	// surface that can only mint a token for a word it should not have claimed.
	// The producers that justify this one: internal/runner/runner.go's bad-route
	// warning and internal/netdetect/resolve.go's tunnel-internal drop.
	ifaceAttrRe  = regexp.MustCompile(`\biface=("(?:[^"\\]|\\.)*"|[^\s]+)`)
	jsonStringRe = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
	// A home directory names the account, which names the person. The segment
	// after /Users or /home is the only identifying part — the rest of the path
	// is structural and worth reading.
	// Both separators. dezhban ships on Windows, where the account lives in
	// `C:\Users\Alice\…` — matching only the unix spelling left the account
	// name in a bundle that says it redacts it, on a whole platform. The
	// doubled form is how a path arrives inside JSON or a Go-quoted log value.
	// Two patterns, and the order matters. A segment FOLLOWED by another
	// separator can safely take spaces and apostrophes, because the separator
	// says where it ends — `C:\Users\Alice Smith\AppData` and
	// `C:\Users\O'Brien\AppData` both leaked their second half to a class that
	// stopped at whitespace and an apostrophe. A segment with nothing after it
	// falls back to stopping at whitespace, because in prose ("look in
	// /Users/alice and try again") that is where the name really ends.
	homeDirSegRe = regexp.MustCompile(`((?:/|\\{1,2})(?:Users|home)(?:/|\\{1,2}))([^/\\"]+)((?:/|\\{1,2}))`)
	homeDirRe    = regexp.MustCompile(`((?:/|\\{1,2})(?:Users|home)(?:/|\\{1,2}))([^/\\\s"']+)`)
)

// Redactor rewrites text, remembering what it has already replaced so the same
// input always yields the same placeholder within one bundle.
type Redactor struct {
	// Enabled false makes every method a pass-through, so a caller building a
	// full-fidelity bundle uses exactly the same code path — there is no second,
	// less-tested route for the unredacted case to drift down.
	Enabled bool

	seen  map[string]string
	order []string

	// next is the highest ordinal ever OFFERED for a kind, whether or not it was
	// used. Monotonic, because an ordinal derived by counting `order` reissues
	// one that a skip vacated — see placeholder.
	next map[string]int
	// values is every value keyed so far, ACROSS ALL KINDS. A token must not be
	// the spelling of a name this bundle carries, and knownNames replays values
	// without regard to kind, so the check cannot be per-kind either.
	values map[string]bool
	// minted is every token this Redactor has produced. A pass that runs after
	// another one can be handed one — see placeholder.
	minted map[string]bool
}

// New returns a Redactor. enabled false is the explicit opt-out: everything
// passes through untouched.
func New(enabled bool) *Redactor {
	return &Redactor{
		Enabled: enabled,
		seen:    map[string]string{},
		next:    map[string]int{},
		values:  map[string]bool{},
		minted:  map[string]bool{},
	}
}

// Text redacts one TEXTUAL entry of a bundle: the names this bundle has already
// replaced elsewhere, then the shape passes.
//
// knownNames FIRST, deliberately. Its values are known identities; hostRe is a
// guess. Run the guess first and a profile called `nord.vpn` is minted as
// `host-N` in prose and `profile-N` in the config — one identifier, two tokens,
// two legend lines, and no way for a reader to see they are the same server.
//
// Every external caller wants this order, which is why it is what `Text` means
// rather than a second exported method beside it. `rules-preview.txt`, `log.txt`
// and the README's notes can each quote a name another entry taught the redactor,
// and a two-door API whose wrong door under-redacts silently is the one shape
// this package may not have. Inside the package, a caller that wants the shape
// passes ALONE says so by calling shapes.
//
// The residual: a name first learned in the same entry that quotes it — the
// daemon's `host=x err="lookup x: no such host"` when `x` appears in no earlier
// file — is not replayed within that entry, because the replay runs ahead of the
// mint by design. Entry order (see cmd/dezhban/report.go) is what keeps that rare.
func (r *Redactor) Text(s string) string {
	if !r.Enabled {
		return s
	}
	return r.shapes(r.knownNames(s))
}

// shapes rewrites every address and hostname in s. No literal-name replay: a
// caller reaching a value whose KEY already identified it must not have the
// bundle's other names spliced into it.
func (r *Redactor) shapes(s string) string {
	if !r.Enabled {
		return s
	}
	// Names first, while the field they sit in is still visible: once a value has
	// been rewritten by a later pass there is no key left to recognise it by.
	// Each yields a placeholder with no dots, so the passes below leave it alone.
	s = profileJSONRe.ReplaceAllStringFunc(s, func(m string) string {
		g := profileJSONRe.FindStringSubmatch(m)
		return g[1] + r.name(jsonBody(g[2]), "profile") + g[3]
	})
	s = hintJSONRe.ReplaceAllStringFunc(s, func(m string) string {
		g := hintJSONRe.FindStringSubmatch(m)
		return g[1] + r.name(jsonBody(g[2]), "hint") + g[3]
	})
	// The profile names inside config's `profiles` and learned.json's `entries`.
	// Scoped to those arrays rather than matching `"name"` as a bare key, which
	// swallowed doctor.json's check names.
	//
	if profileDocRe.MatchString(s) {
		s = r.replaceProfileNames(s)
	}
	s = profileAttrRe.ReplaceAllStringFunc(s, func(m string) string {
		g := profileAttrRe.FindStringSubmatch(m)
		return g[1] + "=" + r.name(jsonBody(strings.Trim(g[2], `"`)), "profile")
	})
	s = hintAttrRe.ReplaceAllStringFunc(s, func(m string) string {
		g := hintAttrRe.FindStringSubmatch(m)
		return "tunnelHint=" + r.name(jsonBody(strings.Trim(g[1], `"`)), "hint")
	})
	s = r.homeDirs(s)
	// Endpoints by field, still ahead of the shape passes and for the same
	// reason: the key is what makes a bare name recognisable as a server.
	//
	// Every one of these writes the ORIGINAL match back when the value is not
	// replaced. Writing the unescaped body back unconditionally turned
	// `{"addr":"a\tb"}` into a literal tab inside a JSON string — the bundle
	// would carry a file that no longer parses, over a value nothing needed to
	// hide in the first place.
	s = endpointsJSONRe.ReplaceAllStringFunc(s, func(m string) string {
		g := endpointsJSONRe.FindStringSubmatch(m)
		body := jsonStringRe.ReplaceAllStringFunc(g[2], func(q string) string {
			v := jsonBody(strings.Trim(q, `"`))
			red := r.endpoint(v)
			if red == v {
				return q
			}
			return `"` + red + `"`
		})
		return g[1] + body + g[3]
	})
	s = endpointJSONRe.ReplaceAllStringFunc(s, func(m string) string {
		g := endpointJSONRe.FindStringSubmatch(m)
		v := jsonBody(g[2])
		red := r.endpoint(v)
		if red == v {
			return m
		}
		return g[1] + red + g[3]
	})
	s = ifacesJSONRe.ReplaceAllStringFunc(s, func(m string) string {
		g := ifacesJSONRe.FindStringSubmatch(m)
		body := jsonStringRe.ReplaceAllStringFunc(g[2], func(q string) string {
			v := jsonBody(strings.Trim(q, `"`))
			red := r.ifaceName(v)
			if red == v {
				return q
			}
			return `"` + red + `"`
		})
		return g[1] + body + g[3]
	})
	s = ifaceJSONRe.ReplaceAllStringFunc(s, func(m string) string {
		g := ifaceJSONRe.FindStringSubmatch(m)
		v := jsonBody(g[2])
		red := r.ifaceName(v)
		if red == v {
			return m
		}
		return g[1] + red + g[3]
	})
	s = ifaceAttrRe.ReplaceAllStringFunc(s, func(m string) string {
		g := ifaceAttrRe.FindStringSubmatch(m)
		v := jsonBody(strings.Trim(g[1], `"`))
		red := r.ifaceName(v)
		if red == v {
			return m
		}
		if strings.HasPrefix(g[1], `"`) {
			red = `"` + red + `"`
		}
		return "iface=" + red
	})
	s = endpointAttrRe.ReplaceAllStringFunc(s, func(m string) string {
		g := endpointAttrRe.FindStringSubmatch(m)
		v := jsonBody(strings.Trim(g[2], `"`))
		red := r.endpoint(v)
		if red == v {
			return m
		}
		if strings.HasPrefix(g[2], `"`) {
			red = `"` + red + `"`
		}
		return g[1] + "=" + red
	})
	// Addresses next: an IPv4 literal also matches nothing in hostRe, but an
	// IPv6 zone or a bracketed form could confuse the host pattern, and doing
	// the precise patterns first keeps the loose one from claiming them.
	s = ipv4Re.ReplaceAllStringFunc(s, func(m string) string { return r.address(m) })
	s = ipv6Re.ReplaceAllStringFunc(s, func(m string) string {
		g := ipv6Re.FindStringSubmatch(m)
		return g[1] + r.address(g[2])
	})
	s = hostRe.ReplaceAllStringFunc(s, func(m string) string { return r.host(m) })
	return s
}

// endpoint replaces one endpoint-shaped FIELD VALUE — an address or a bare
// name. It acts only on a bare token: anything carrying a scheme, a path, a
// port or whitespace is left for the shape passes, which read a provider URL
// correctly and keep the allow-listed provider host inside it, since which
// provider answered is a real diagnostic. An all-digit value is a count or a
// port, never a name — `endpoint=443` is a port, not a server.
func (r *Redactor) endpoint(v string) string {
	if v == "" || strings.ContainsAny(v, "/ \t") || isAllDigits(v) {
		return v
	}
	if addr, err := netip.ParseAddr(v); err == nil {
		if keepAddr(addr) {
			return v
		}
		return r.placeholder(addr.String(), "ip")
	}
	// Not an address, so a colon here means host:port or a URL remnant, not a
	// compressed IPv6 — either way not a bare name.
	if strings.ContainsAny(v, ":@?#") {
		return v
	}
	if keepHost(v) {
		return v
	}
	return r.placeholder(strings.ToLower(v), "host")
}

// jsonBody turns the matched body of a JSON string back into the value it
// stands for. The patterns above admit `\"` so a name carrying a quote is
// matched WHOLE — with `[^"]+` the match stopped at the backslash, minted a
// placeholder for the first half, and left the second half in the bundle beside
// a stray quote that also broke the JSON. Keying on the escaped spelling would
// then give one name two placeholders depending on how it was written, so the
// key is the unescaped value; text that will not unquote falls back to itself
// rather than being dropped.
func jsonBody(escaped string) string {
	if v, err := strconv.Unquote(`"` + escaped + `"`); err == nil {
		return v
	}
	return escaped
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// addresses runs only the passes that find an ADDRESS or a home directory —
// no hostname shape, no name pass. For text that is dezhban's own words but
// could still have an identifier interpolated into it: an address there is
// always a leak, while a dotted token is far more likely to be a config key or
// a filename the reader needs.
func (r *Redactor) addresses(s string) string {
	if !r.Enabled {
		return s
	}
	s = r.homeDirs(s)
	s = ipv4Re.ReplaceAllStringFunc(s, func(m string) string { return r.address(m) })
	return ipv6Re.ReplaceAllStringFunc(s, func(m string) string {
		g := ipv6Re.FindStringSubmatch(m)
		return g[1] + r.address(g[2])
	})
}

// homeDirs replaces the account segment of every home-directory path.
func (r *Redactor) homeDirs(s string) string {
	s = homeDirSegRe.ReplaceAllStringFunc(s, func(m string) string {
		g := homeDirSegRe.FindStringSubmatch(m)
		return g[1] + r.placeholder(strings.ToLower(g[2]), "user") + g[3]
	})
	return homeDirRe.ReplaceAllStringFunc(s, func(m string) string {
		g := homeDirRe.FindStringSubmatch(m)
		// The segment pass has already run, so this one can be looking at its
		// output: `/Users/user-1/x` would otherwise mint a placeholder FOR a
		// placeholder and hand the same account two tokens.
		if placeholderRe.MatchString(g[2]) {
			return m
		}
		return g[1] + r.placeholder(strings.ToLower(g[2]), "user")
	})
}

// placeholderKinds is every kind this package mints. It is a list rather than a
// literal alternation because the alternation drifted: the `iface` kind was added
// with its own mint path and placeholderRe was left as it was, so the pattern
// stopped matching every token the package produces — which is the one thing it
// promises. kindNoun keeps its own switch (nouns are prose, not identity) and a
// test pins the two together.
var placeholderKinds = []string{"ip", "host", "profile", "hint", "user", "iface", "vpn"}

// placeholderRe matches a token this package has already minted. Any pass that
// can run after another one needs it: replacing a placeholder produces a token
// in no legend and splits one identifier across two. knownNames relies on it to
// refuse a value that is spelled like a token — see json.go.
var placeholderRe = regexp.MustCompile(`^(?:` + strings.Join(placeholderKinds, "|") + `)-\d+$`)

// address replaces one address-shaped match, keeping any /prefix — the prefix
// length is structural (it says "this is a subnet rule"), not identifying.
func (r *Redactor) address(m string) string {
	body, suffix := m, ""
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		body, suffix = m[:i], m[i:]
	}
	zone := ""
	if i := strings.IndexByte(body, '%'); i >= 0 {
		body, zone = body[:i], body[i:]
	}
	addr, err := netip.ParseAddr(body)
	if err != nil {
		// Not actually an address — a version string, a time, a MAC-ish token.
		// Leave it alone: redacting text that is not an identifier makes the
		// bundle harder to read for no gain.
		return m
	}
	// The zone is an INTERFACE name — `fe80::1%nordlynx` — and a provider-created
	// interface names the provider as plainly as a server address does. It used
	// to be copied through verbatim on both branches, and the keepAddr branch is
	// the one that mattered: a link-local address is structural, so the whole
	// original was returned and the zone rode out with it.
	if zone != "" {
		zone = "%" + r.ifaceName(zone[1:])
	}
	if keepAddr(addr) {
		return body + zone + suffix
	}
	return r.placeholder(addr.String(), "ip") + zone + suffix
}

// keepAddr reports addresses that identify nobody and whose meaning is entirely
// structural. Redacting these would destroy the reader's ability to see what a
// ruleset does — "pass on lo0 to ip-4" instead of "to 127.0.0.1" hides that the
// rule is loopback — while protecting nothing: every dezhban install has them.
func keepAddr(a netip.Addr) bool {
	return a.IsLoopback() || a.IsUnspecified() || a.IsMulticast() ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsPrivate()
}

// host replaces one hostname-shaped match.
func (r *Redactor) host(m string) string {
	if keepHost(m) {
		return m
	}
	return r.placeholder(strings.ToLower(m), "host")
}

// keepHost is an ALLOW-list, not a deny-list, and that direction is the whole
// safety property: an unknown name is redacted. A deny-list would leak every
// hostname nobody thought of, which is precisely the VPN provider this exists
// to hide.
//
// What is kept is dezhban's own vocabulary — the geo providers it ships (they
// are in the shipped default config, identical on every install, and which
// provider answered is a real diagnostic), plus filenames and identifiers that
// merely look like hostnames.
func keepHost(m string) bool {
	lower := strings.ToLower(m)
	if allowedHosts[lower] {
		return true
	}
	for _, suffix := range keptSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// allowedHosts are the shipped geo-provider endpoints and this project's own
// domains. Keep this list in step with config.DefaultProviders — a provider
// added there and not here is redacted, which is merely noisy, never unsafe.
var allowedHosts = map[string]bool{
	"get.geojs.io":              true,
	"api.country.is":            true,
	"ip-api.com":                true,
	"ipwho.is":                  true,
	"freeipapi.com":             true,
	"ifconfig.co":               true,
	"ipinfo.io":                 true,
	"ipapi.co":                  true,
	"github.com":                true,
	"raw.githubusercontent.com": true,
	"vpn.example.com":           true,
	"example.com":               true,
}

// keptSuffixes are the endings that mean "this is one of dezhban's own files, or
// a name that cannot be a host we reached".
//
// Two rules decide membership, and both have to hold:
//
//  1. It cannot be a real public TLD. `.sh` (Saint Helena) and `.md` (Moldova)
//     are delegated, so a provider can live on either and a suffix rule would
//     wave it straight through.
//  2. The name in FRONT of it is dezhban's, not the user's. `.conf` and `.ovpn`
//     pass rule 1 and fail this one: those files are the user's, and they are
//     named after the provider — `mullvad-frankfurt.conf` leaks exactly what
//     this package exists to hide, while being no kind of hostname at all.
//
// `.local` fails both readings: an mDNS name is the machine, and a Mac's is
// built from its owner's name. It was the single largest identifier this list
// let through.
//
// This is a deny-list embedded in an allow-listed matcher, so it is kept as
// short as those two rules allow. Redacting a filename is noise; keeping one is
// a leak.
var keptSuffixes = []string{
	".json", ".log", ".txt", ".plist", ".dezhban",
	// Reserved by RFC 2606 / RFC 6761: never delegated, so never a real host.
	".arpa", ".invalid", ".test",
}

// replaceProfileNames rewrites every `"name": "..."` in body.
func (r *Redactor) replaceProfileNames(body string) string {
	return profileNameRe.ReplaceAllStringFunc(body, func(n string) string {
		h := profileNameRe.FindStringSubmatch(n)
		return h[1] + r.name(jsonBody(h[2]), "profile") + h[3]
	})
}

// name replaces one identity-bearing NAME, keeping the words that are dezhban's
// own rather than the user's.
func (r *Redactor) name(v, kind string) string {
	lower := strings.ToLower(v)
	if lower == unattributed {
		return v
	}
	if kind == "hint" && keptHints[lower] {
		return v
	}
	return r.placeholder(lower, kind)
}

// ifaceName replaces an interface name that is not one of the kernel's own.
//
// A generic name — `utun4`, `lo0`, `en0`, `wg0` — is structural: every host has
// them, they identify nobody, and the rulesets keep them in plain sight, so
// replacing them would make a bundle unreadable for no gain. But
// `vpn.tunnelInterfaces` takes whatever the user's VPN client created, and
// netdetect recognises `nordlynx`, `proton` and `gpd` by name precisely because
// those are what commercial clients install. Those name the provider as plainly
// as a server address does, and no shape can see them.
func (r *Redactor) ifaceName(v string) string {
	if v == "" || keepIface(v) {
		return v
	}
	return r.placeholder(strings.ToLower(v), "iface")
}

// keepIface reports the interface names that are the kernel's vocabulary rather
// than a vendor's: a generic prefix followed by nothing but digits.
func keepIface(name string) bool {
	l := strings.ToLower(name)
	digits := len(l)
	for digits > 0 && l[digits-1] >= '0' && l[digits-1] <= '9' {
		digits--
	}
	return genericIfaces[l[:digits]]
}

// genericIfaces is the kernel/driver vocabulary. Deliberately NOT the whole of
// netdetect.tunnelPrefixes: that list also carries `nordlynx`, `proton` and
// `gpd`, which are there because they name a commercial VPN — the exact thing
// this package exists to hide.
var genericIfaces = map[string]bool{
	"utun": true, "tun": true, "tap": true, "wg": true, "ipsec": true,
	"lo": true, "en": true, "eth": true, "ppp": true, "bridge": true,
	"awdl": true, "llw": true, "gif": true, "stf": true, "anpi": true,
	"ap": true, "vmenet": true, "veth": true, "docker": true,
}

// keptHints are the interface-name PREFIXES that are dezhban's own vocabulary
// rather than the user's. A hint is matched against the live interface name, so
// on most hosts it is literally `utun` or `wg` — replacing those names nobody
// and hides whether the hint matches the `utun4` the rulesets keep in plain
// sight, which is the only thing the field is for.
//
// The generic subset of netdetect.tunnelPrefixes, deliberately: `nordlynx`,
// `proton` and `gpd` are on that list too and they name the provider as plainly
// as a server address does, so those still go. Keep in step with it.
var keptHints = map[string]bool{
	"utun": true, "tun": true, "tap": true, "wg": true, "ipsec": true,
}

// unattributed is learned.json's name for endpoints that belong to no profile.
// dezhban's own constant, identical on every install — minting it as `profile-N`
// hid a word that names nobody and told the legend the host had one more VPN
// profile than it has, which is the doctor.json failure again in a second file.
const unattributed = "_unattributed"

// placeholder returns the stable token for one value, minting it on first sight.
//
// A candidate ordinal is REFUSED for two reasons, and the second is the one that
// is easy to get wrong.
//
//  1. It would produce the value itself. Config accepts `[A-Za-z0-9._-]`, so a
//     profile can legitimately be CALLED `profile-1` — and minting `profile-1`
//     for it left the name verbatim in the bundle while the legend said it had
//     been replaced, which is the one failure this package must never have:
//     advertising a safety it did not deliver.
//
//  2. It is a value this bundle already carries, under ANY kind. A token that is
//     also a real name is indistinguishable from that name to a reader, and
//     knownNames — which replays values without regard to kind — would rewrite
//     the token as if it were the name.
//
// The counter is r.next, not a count of `order` entries, and that distinction is
// the whole bug this replaced: a refusal advances the token but appends ONE
// order entry, so a count-derived ordinal handed the vacated number to the next
// value. `profile-1` refused its way to `profile-2`, then `alpha` counted one
// entry and was also given `profile-2` — two identities, one token, and a legend
// reading `2 distinct profile names → profile-2 … profile-2`.
//
// The residual a streaming redactor cannot close: a real identifier spelled like
// a token ALREADY minted, arriving after that mint. Its own mint is safe, since
// tokens are never reissued, but the entries carrying that token are already
// written and nothing can move it retroactively. The bound that makes this
// acceptable is that the only colliding spellings are `<kind>-<digits>`, which
// name no provider and no person.
func (r *Redactor) placeholder(value, kind string) string {
	// A token this Redactor already produced is not a name. Text replays known
	// names BEFORE the shape passes run, so `profile=mullvad-de` becomes
	// `profile=profile-1` and profileAttrRe then offers `profile-1` here as if it
	// were a value — minting a second token for the same identity, in no legend,
	// splitting one server across two. This is the guard placeholderRe was
	// written for; it belongs at the mint, the one place every pass funnels
	// through, rather than at each pass.
	//
	// Membership, not shape: a real profile CALLED `profile-1` that this bundle
	// has not already used as a token must still be replaced, which is what the
	// refusal loop below is for.
	if r.minted[value] {
		return value
	}
	key := kind + ":" + value
	if p, ok := r.seen[key]; ok {
		return p
	}
	n := r.next[kind]
	var p string
	for {
		n++
		p = fmt.Sprintf("%s-%d", kind, n)
		if p != value && !r.values[p] {
			break
		}
	}
	r.next[kind] = n
	r.seen[key] = p
	r.order = append(r.order, key)
	r.values[value] = true
	r.minted[p] = true
	return p
}

// Legend says HOW MUCH was replaced, never WHAT. It ships inside the bundle, so
// listing the originals would undo the whole exercise — and listing the
// placeholders one per line says nothing a count does not, since a placeholder
// with no original beside it carries no information at all. The counts are what
// a reader actually wants: "sixty-one distinct hostnames" tells them the
// provider list is in here; "host-37" tells them nothing.
func (r *Redactor) Legend() []string {
	if !r.Enabled || len(r.order) == 0 {
		return nil
	}
	// The tokens ACTUALLY minted, in mint order — not 1..n. An ordinal is
	// skipped whenever it would have produced the value it replaces (a profile
	// really can be called `profile-1`), and a legend that assumed 1..n then
	// advertised a token appearing nowhere in the bundle.
	tokens := map[string][]string{}
	var kinds []string
	for _, key := range r.order {
		kind, _, _ := strings.Cut(key, ":")
		if _, seen := tokens[kind]; !seen {
			kinds = append(kinds, kind)
		}
		tokens[kind] = append(tokens[kind], r.seen[key])
	}
	sort.Strings(kinds)

	out := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		t := tokens[kind]
		n := len(t)
		// A range of one is not a range: "ip-1 … ip-1" reads as a rendering
		// bug in the one file that has to look trustworthy.
		if n == 1 {
			out = append(out, fmt.Sprintf("1 distinct %s → %s", kindNoun(kind, n), t[0]))
			continue
		}
		// A RANGE promises everything between its ends. placeholder refuses an
		// ordinal whenever it would collide, so the tokens of one kind need not
		// be consecutive — and `profile-1 … profile-3` for two profiles
		// advertises a `profile-2` that is nowhere in the bundle, which is the
		// same lie the 1..n rendering told. When there is a hole, list them.
		//
		// Listing, rather than dropping to a bare count: the token SPELLING is
		// what a reader greps the bundle with, and teaching that spelling is
		// what the range was for. The list is not unbounded in practice — a hole
		// needs an identifier literally spelled `<kind>-<digits>`, impossible for
		// `ip` (values are parsed addresses) and vanishingly rare for `host`, so
		// this branch belongs to the small-count kinds. It discloses one thing,
		// and only one: that some identifier here is spelled like a placeholder,
		// a spelling that names nobody.
		if consecutive(t) {
			out = append(out, fmt.Sprintf("%d distinct %s → %s … %s",
				n, kindNoun(kind, n), t[0], t[n-1]))
			continue
		}
		out = append(out, fmt.Sprintf("%d distinct %s → %s",
			n, kindNoun(kind, n), strings.Join(t, ", ")))
	}
	return out
}

// consecutive reports whether tokens run without a gap. They arrive in mint
// order, which placeholder guarantees is increasing ordinal order for one kind,
// so only the ends have to be read. An unparseable token answers false, which
// sends the caller to the enumeration — the honest direction, since a range
// nobody can verify is exactly what this guards against.
func consecutive(tokens []string) bool {
	first, ok := ordinal(tokens[0])
	if !ok {
		return false
	}
	last, ok := ordinal(tokens[len(tokens)-1])
	if !ok {
		return false
	}
	return last-first+1 == len(tokens)
}

// ordinal is the number a minted token ends in.
func ordinal(token string) (int, bool) {
	i := strings.LastIndexByte(token, '-')
	if i < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(token[i+1:])
	if err != nil {
		return 0, false
	}
	return n, true
}

func kindNoun(kind string, n int) string {
	singular, plural := "IP address", "IP addresses"
	switch kind {
	case "host":
		singular, plural = "hostname", "hostnames"
	case "profile":
		singular, plural = "profile name", "profile names"
	case "hint":
		singular, plural = "tunnel hint", "tunnel hints"
	case "iface":
		singular, plural = "interface name", "interface names"
	case "user":
		singular, plural = "account name", "account names"
	case "vpn":
		singular, plural = "VPN service name", "VPN service names"
	}
	if n == 1 {
		return singular
	}
	return plural
}
