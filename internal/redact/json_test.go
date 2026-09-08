package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

// The doctor writes learned entry names — which are the user's profile names —
// into its Details and Summary as ordinary PROSE. Bare words: no dot for a
// hostname shape to catch, no key for a field-aware pass to read. Nothing was
// looking at them, so every host with learned endpoints shipped its profile
// names inside a bundle that says it redacts them, and the on-host checklist's
// "grep for your profile names — none may appear" failed there.
func TestAProfileNameInDoctorsProseIsRedacted(t *testing.T) {
	r := New(true)
	// config.json is collected first, which is what teaches the redactor the
	// name. The order is pinned in cmd/dezhban/report.go.
	r.JSON(`{"vpn":{"profiles":[{"name":"work-nord","endpoints":["203.0.113.9"]}]}}`)

	got := r.JSON(`{"checks":[{"name":"endpointRetention","status":"warn",` +
		`"summary":"every learned address for work-nord has aged out.",` +
		`"details":["work-nord — 3 stored, 0 within the 168h retention window"]}]}`)

	if strings.Contains(got, "work-nord") {
		t.Errorf("the profile name survived in doctor's prose: %s", got)
	}
	if !strings.Contains(got, "profile-1") {
		t.Errorf("it was not replaced with the token the rest of the bundle uses: %s", got)
	}
	// dezhban's own words stay: the check name and the retention window are the
	// diagnosis, and a bundle that hides those has replaced the answer.
	for _, want := range []string{"endpointRetention", "168h", "aged out"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q was redacted out of the diagnosis: %s", want, got)
		}
	}
}

// A doctor fix is dezhban's own command text — every Fixes entry is a literal
// template with nothing interpolated. The shape passes turned `vpn.endpoints`
// and `wg0.conf` into placeholders and made the suggested command nonsense.
func TestADoctorFixStaysARunnableCommand(t *testing.T) {
	in := `{"checks":[{"name":"endpoints","fixes":[` +
		`"sudo dezhban config set vpn.endpoints=<server-ip>",` +
		`"dezhban vpn import <wg0.conf|client.ovpn>"]}]}`
	got := New(true).JSON(in)
	for _, want := range []string{"vpn.endpoints", "wg0.conf", "client.ovpn"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q was redacted out of a fix command: %s", want, got)
		}
	}
}

// An ADDRESS in a fix is still a leak. The premise that a fix never carries user
// data was wrong when it was written — buildEndpointsCheck interpolated the
// offending endpoint — so one check redacted an address in Details and shipped
// it verbatim here. The source no longer interpolates; this is the belt that
// catches the next one.
func TestAnAddressInterpolatedIntoAFixIsStillRedacted(t *testing.T) {
	in := `{"checks":[{"name":"endpoints","fixes":[` +
		`"203.0.113.9 is a tunnel-internal address; set vpn.endpoints to your public IP",` +
		`"look in /Users/someone/Downloads"]}]}`
	got := New(true).JSON(in)
	for _, leak := range []string{"203.0.113.9", "someone"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q survived inside a fix: %s", leak, got)
		}
	}
	// Still a runnable command: the config key and the path shape survive.
	for _, want := range []string{"vpn.endpoints", "/Users/", "Downloads"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q was redacted out of a fix command: %s", want, got)
		}
	}
}

// The whole document must still parse, and the escapes in it must survive.
//
// A text pattern matched the tail of an escaped `<` where it abutted a dotted
// name — `u003cfile.conf` — and replacing it destroyed the escape, so the bundle
// shipped a doctor.json that no JSON reader could open.
func TestTheRewrittenDocumentStillParses(t *testing.T) {
	for _, in := range []string{
		`{"fixes":["dezhban vpn import <file.conf|file.ovpn>"]}`,
		`{"profiles":[{"name":"foo\"bar"}]}`,
		`{"addr":"a\tb"}`,
		`{"summary":"a \\ b é \n c"}`,
	} {
		got := New(true).JSON(in)
		if !json.Valid([]byte(got)) {
			t.Errorf("JSON(%q) = %q — the bundle would carry a file nothing can open", in, got)
		}
	}
}

// A key's meaning comes from the path, so a name containing a bracket cannot
// make the scoping vanish the way a bracket-balancing pattern let it.
func TestNameScopingCannotFailOpenOnAWalk(t *testing.T) {
	for _, tc := range []struct {
		in    string
		leaks []string
		keep  string
	}{
		// The bracketed name itself must go too — it is the shape the whole
		// redesign is named after, and asserting only its neighbour left it
		// untested.
		{`{"profiles":[{"name":"work[de"},{"name":"mullvad-se"}]}`, []string{"work[de", "mullvad-se"}, ""},
		{`{"profiles":[{"name":"a]b"},{"name":"mullvad-se"}]}`, []string{"a]b", "mullvad-se"}, ""},
		{`{"entries":[{"name":"nord-ch","endpoints":[{"addr":"203.0.113.4"}]}]}`, []string{"nord-ch"}, ""},
		// A `name` that is NOT under profiles/entries is dezhban's vocabulary.
		{`{"checks":[{"name":"lockout"}]}`, nil, "lockout"},
	} {
		got := New(true).JSON(tc.in)
		for _, leak := range tc.leaks {
			if strings.Contains(got, leak) {
				t.Errorf("JSON(%q) = %q — %q survived", tc.in, got, leak)
			}
		}
		if tc.keep != "" && !strings.Contains(got, tc.keep) {
			t.Errorf("JSON(%q) = %q — %q was redacted", tc.in, got, tc.keep)
		}
	}
}

// Key order is preserved. A config.json whose keys came back alphabetised is a
// bundle that does not look like the file the reader has open.
func TestKeyOrderSurvivesTheWalk(t *testing.T) {
	in := `{"zeta":1,"alpha":2,"mid":{"yy":3,"xx":4}}`
	got := New(true).JSON(in)
	if i, j := strings.Index(got, "zeta"), strings.Index(got, "alpha"); i > j {
		t.Errorf("keys were reordered: %s", got)
	}
	if i, j := strings.Index(got, "yy"), strings.Index(got, "xx"); i > j {
		t.Errorf("nested keys were reordered: %s", got)
	}
}

// A document that does not parse still gets redacted, through Text. `report`
// copies config.json off disk verbatim and never validates it, so a broken
// config is exactly the host a bundle is collected from.
func TestAnUnparseableDocumentFallsBackToText(t *testing.T) {
	in := `{"vpn": {"endpoints": ["203.0.113.9"` // truncated on purpose
	got := New(true).JSON(in)
	if strings.Contains(got, "203.0.113.9") {
		t.Errorf("JSON(%q) = %q — a broken document was left unredacted", in, got)
	}
}

// Disabled stays a true pass-through here too — the same rule Text lives by.
func TestDisabledJSONIsAPassThrough(t *testing.T) {
	in := `{"vpn":{"endpoints":["203.0.113.9"],"profiles":[{"name":"work-nord"}]}}`
	if got := New(false).JSON(in); got != in {
		t.Errorf("got %q, want the input unchanged", got)
	}
}

// state.json's tunnel names are interface names — structural, like lo0 — and
// must not be taken for profile names just because the key is `name`.
func TestAnInterfaceNameIsNotAProfileName(t *testing.T) {
	in := `{"tunnels":[{"name":"utun4","up":true}],"activeProfile":"work-nord"}`
	got := New(true).JSON(in)
	if !strings.Contains(got, "utun4") {
		t.Errorf("the interface name was redacted: %s", got)
	}
	if strings.Contains(got, "work-nord") {
		t.Errorf("the active profile survived: %s", got)
	}
}

// The bundle's JSON reads the way the terminal does. json.Marshal always
// rewrites <, > and & into their escaped form, and the encoder's
// SetEscapeHTML(false) does not reach bytes a custom MarshalJSON returned — so
// doctor's `<name>` placeholders came back escaped even with it off. Valid
// either way; this is about the file being readable.
func TestTheRewrittenDocumentIsNotHTMLEscaped(t *testing.T) {
	escapedLT := `\u` + "003c"
	in := `{"fixes":["dezhban vpn add <name> --endpoint <host-or-ip>"]}`
	got := New(true).JSON(in)
	if strings.Contains(got, escapedLT) {
		t.Errorf("JSON(%q) = %q — the placeholders came back escaped", in, got)
	}
	if !strings.Contains(got, "<name>") {
		t.Errorf("JSON(%q) = %q — the placeholder is not readable", in, got)
	}
}

// A document with trailing content must not come back TRUNCATED.
//
// The walk decoded the first value and stopped, so everything after it was
// dropped — the bundle then showed a config that is not the file on disk, which
// is a diagnostic lie however well the surviving half was redacted. Those go to
// Text, which reads the whole thing.
func TestTrailingContentIsNotDropped(t *testing.T) {
	in := `{"vpn":{"endpoints":["203.0.113.9"]}} stray 198.51.100.7`
	got := New(true).JSON(in)
	if !strings.Contains(got, "stray") {
		t.Errorf("JSON(%q) = %q — content after the first value was dropped", in, got)
	}
	for _, leak := range []string{"203.0.113.9", "198.51.100.7"} {
		if strings.Contains(got, leak) {
			t.Errorf("JSON(%q) = %q — %q survived", in, got, leak)
		}
	}
}

// A profile named after one of dezhban's own words must not take that word with
// it. The literal-name pass is what reaches profile names in doctor's prose, and
// it would otherwise replace the CHECK named `config` with the token minted for
// a profile called `config` — the over-redaction the path scoping exists to
// prevent, walking back in through a different door.
func TestAProfileNamedLikeACheckDoesNotClaimTheCheck(t *testing.T) {
	r := New(true)
	r.JSON(`{"profiles":[{"name":"config"}]}`)
	got := r.JSON(`{"checks":[{"name":"config","status":"ok"}]}`)
	if !strings.Contains(got, `"config"`) {
		t.Errorf("the check name was claimed by a profile of the same name: %s", got)
	}
}

// A bare-name endpoint carrying a PORT fell through every pass: `endpoint`
// declines a value that is not a bare token, and hostRe needs a dot. It shipped
// verbatim out of a file the package copies off disk without validating.
func TestABareEndpointWithAPortIsRedacted(t *testing.T) {
	got := New(true).JSON(`{"endpoints":["se-sto-01:51820"]}`)
	if strings.Contains(got, "se-sto-01") {
		t.Errorf("got %q — the server name survived", got)
	}
	// The port is structural: which one the tunnel uses is a real diagnostic.
	if !strings.Contains(got, "51820") {
		t.Errorf("got %q — the port was redacted with the name", got)
	}
}

// A tunnel hint is an interface-name PREFIX, so on most hosts it is literally
// `utun` or `wg` — dezhban's own vocabulary, naming nobody. Replacing it hides
// whether the hint matches the interface the rulesets keep in plain sight.
// A hint that names a provider still goes.
func TestAGenericTunnelHintIsKeptAndAProviderHintIsNot(t *testing.T) {
	got := New(true).JSON(`{"profiles":[` +
		`{"name":"a","tunnelHint":"utun"},{"name":"b","tunnelHint":"wg"},` +
		`{"name":"c","tunnelHint":"nordlynx"}]}`)
	for _, want := range []string{`"utun"`, `"wg"`} {
		if !strings.Contains(got, want) {
			t.Errorf("%s was redacted: %s", want, got)
		}
	}
	if strings.Contains(got, "nordlynx") {
		t.Errorf("a provider-naming hint survived: %s", got)
	}
}

// The literal-name pass must never claim dezhban's own stable identifiers. The
// posture and mode strings are the ones CLAUDE.md forbids renaming, so a user
// whose profile is called `guard` must not turn `"posture": "guard"` into a
// placeholder — that is a broken diagnosis, where the miss is only noise.
func TestKnownNamesNeverClaimsAReservedWord(t *testing.T) {
	r := New(true)
	r.JSON(`{"profiles":[{"name":"guard"}]}`)
	got := r.JSON(`{"posture":"guard","summary":"the guard is healthy","mode":"full-block"}`)
	for _, want := range []string{`"guard"`, `"full-block"`} {
		if !strings.Contains(got, want) {
			t.Errorf("%s was claimed by a profile of that name: %s", want, got)
		}
	}
}

// One identifier, one token. Running the shape guess before the literal pass
// minted a profile called `nord.vpn` as `host-N` in prose and `profile-N` in the
// config — two legend lines for one server, and no way to see they are the same.
func TestAHostShapedProfileNameGetsOneTokenNotTwo(t *testing.T) {
	r := New(true)
	r.JSON(`{"profiles":[{"name":"nord.vpn"}]}`)
	got := r.JSON(`{"summary":"the profile nord.vpn stopped resolving"}`)
	if strings.Contains(got, "host-") {
		t.Errorf("got %q — the profile name was minted a second time as a hostname", got)
	}
	if !strings.Contains(got, "profile-1") {
		t.Errorf("got %q, want the profile token it already has", got)
	}
}

// A profile name spelled with the punctuation config accepts must still go.
//
// The boundary was `\b`, which cannot match at a non-word-to-non-word edge, so
// a name like `-work-` or `.home` had no boundary at either end and stayed
// verbatim in every prose string the literal pass covers.
func TestAPunctuatedProfileNameIsStillFoundInProse(t *testing.T) {
	for _, name := range []string{"-work-", ".home", "a.b-c"} {
		r := New(true)
		r.JSON(`{"profiles":[{"name":"` + name + `"}]}`)
		got := r.JSON(`{"summary":"every learned address for ` + name + ` has aged out."}`)
		if strings.Contains(got, name) {
			t.Errorf("profile %q survived in prose: %s", name, got)
		}
	}
	// A name must still not be claimed as part of a longer word.
	r := New(true)
	r.JSON(`{"profiles":[{"name":"work"}]}`)
	if got := r.JSON(`{"summary":"network workload"}`); strings.Contains(got, "profile-") {
		t.Errorf("the name was claimed inside a longer word: %s", got)
	}
}

// A single-label endpoint is logged twice — once as an attr, once inside the
// resolver's error — and only the attr has a key to recognise it by. The shape
// passes cannot see it either, because it has no dot. It has to come off the
// list of names the bundle already knows.
func TestARememberedHostnameIsReplacedInErrorText(t *testing.T) {
	r := New(true)
	r.JSON(`{"vpn":{"endpoints":["mullvad"]}}`)
	got := r.JSON(`{"details":["host=mullvad err=\"lookup mullvad: no such host\""]}`)
	if strings.Contains(got, "mullvad") {
		t.Errorf("the endpoint survived in the error text: %s", got)
	}
	if !strings.Contains(got, "host-1") {
		t.Errorf("got %q, want the token the config already uses", got)
	}
}

// A redacted bare endpoint must not eat a provider hostname that contains it.
//
// The literal pass matches on the name's own alphabet — `[A-Za-z0-9._-]` — so
// an endpoint called `geojs` is not claimed inside `get.geojs.io`, where dots
// flank it on both sides. That is what keeps "which provider answered", a real
// diagnostic and an allow-listed name, readable next to a redacted endpoint
// that happens to share a word with it.
func TestARedactedEndpointDoesNotClaimAProviderHostname(t *testing.T) {
	r := New(true)
	r.JSON(`{"vpn":{"endpoints":["geojs"]}}`)
	got := r.JSON(`{"details":["reached https://get.geojs.io/v1/ip.json"]}`)
	if !strings.Contains(got, "get.geojs.io") {
		t.Errorf("the provider hostname was claimed by a colliding endpoint: %s", got)
	}
}

// The Text fallback must not FAIL OPEN on the very input it exists for.
//
// A config cut off mid-array is the commonest way a hand-edited file stops
// parsing, and it is exactly the case the walk hands to Text. The endpoints
// pattern required a closing `]`, so it matched nothing; a single-label endpoint
// matches no shape either; and the file came back verbatim out of a bundle that
// says it redacts it.
func TestATruncatedEndpointsArrayIsStillRedacted(t *testing.T) {
	for _, tc := range []struct{ in, leak string }{
		{`{"vpn":{"endpoints":["mullvad"`, "mullvad"},
		{`{"vpn":{"endpoints":["203.0.113.9","nl-01.protonvpn.net"`, "protonvpn"},
	} {
		if got := New(true).JSON(tc.in); strings.Contains(got, tc.leak) {
			t.Errorf("JSON(%q) = %q — %q survived the fallback", tc.in, got, tc.leak)
		}
	}
}

// An interface name the user's VPN client created names the provider.
// netdetect recognises `nordlynx`, `proton` and `gpd` by name for exactly that
// reason, and no shape can see them. The kernel's own names stay: every host has
// `utun4` and `lo0`, and the rulesets keep them in plain sight.
func TestAProviderInterfaceNameIsRedactedAndAGenericOneIsNot(t *testing.T) {
	got := New(true).JSON(`{"vpn":{"tunnelInterfaces":["utun4","nordlynx","proton","lo0","en0"]}}`)
	for _, keep := range []string{`"utun4"`, `"lo0"`, `"en0"`} {
		if !strings.Contains(got, keep) {
			t.Errorf("%s was redacted: %s", keep, got)
		}
	}
	for _, leak := range []string{"nordlynx", "proton"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q survived: %s", leak, got)
		}
	}
	// state.json's tunnels[].name is the same kind of thing.
	tun := New(true).JSON(`{"tunnels":[{"name":"utun4"},{"name":"nordlynx"}]}`)
	if !strings.Contains(tun, `"utun4"`) || strings.Contains(tun, "nordlynx") {
		t.Errorf("tunnels[].name handled wrongly: %s", tun)
	}
}
