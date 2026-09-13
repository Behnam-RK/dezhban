package redact

import (
	"encoding/json"
	"slices"
	"strconv"
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

// learned.json records the interface it observed under `iface`. The same value
// is redacted under tunnelInterfaces and tunnels[].name, so leaving this key out
// meant one document redacted a provider-named interface and the one beside it
// did not.
func TestTheLearnedInterfaceKeyIsRedacted(t *testing.T) {
	got := New(true).JSON(`{"entries":[{"name":"p","iface":"nordlynx"},{"name":"q","iface":"utun4"}]}`)
	if strings.Contains(got, "nordlynx") {
		t.Errorf("got %q — the learned interface survived", got)
	}
	if !strings.Contains(got, `"utun4"`) {
		t.Errorf("got %q — a generic interface was redacted", got)
	}
}

// Interface names need the same fail-open protection endpoints got. A config cut
// off mid-array reaches Text, and Text had no field-aware interface handling at
// all, so `tunnelInterfaces:["nordlynx"` came back untouched.
func TestATruncatedInterfaceArrayIsStillRedacted(t *testing.T) {
	if got := New(true).JSON(`{"vpn":{"tunnelInterfaces":["nordlynx"`); strings.Contains(got, "nordlynx") {
		t.Errorf("got %q — the fallback left the interface name verbatim", got)
	}
	// And in a log attr, where there is no JSON to walk at all.
	if got := New(true).Text(`level=WARN msg=drop iface=nordlynx tunnel=utun4`); strings.Contains(got, "nordlynx") {
		t.Errorf("got %q — the attr form leaked", got)
	}
}

// The replay sorts longest-value-first, so a value SPELLED like a token gets the
// pair (`profile-1` → `profile-2`) applied to text the replay itself just wrote:
// the long name became `profile-1` and was then rewritten to `profile-2`. Two
// identities onto one token, and the long name's real token nowhere in the file.
func TestTheNameReplayNeverLaundersATokenIntoAnother(t *testing.T) {
	r := New(true)
	r.JSON(`{"vpn":{"profiles":[{"name":"a-very-long-profile-name"},{"name":"profile-1"}]}}`)
	got := r.JSON(`{"checks":[{"summary":"every learned address for a-very-long-profile-name has aged out."}]}`)
	if strings.Contains(got, "a-very-long-profile-name") {
		t.Fatalf("the name survived: %q", got)
	}
	if !strings.Contains(got, "profile-1") || strings.Contains(got, "profile-2") {
		t.Errorf("got %q — the prose carries a token minted for a different identity", got)
	}
}

// An interface can be CALLED `vpn` or `wireguard`: keepIface only keeps a generic
// stem followed by digits. Once iface joined the replay, such a name would have
// rewritten dezhban's own prose everywhere it says those words.
func TestTheReplayNeverClaimsAWordDezhbanWrites(t *testing.T) {
	r := New(true)
	r.JSON(`{"vpn":{"tunnelInterfaces":["vpn"]}}`)
	const prose = "The vpn guard is on. WireGuard clients send from an unconnected socket."
	got := r.JSON(`{"checks":[{"summary":` + strconv.Quote(prose) + `}]}`)
	if !strings.Contains(got, "vpn guard is on") || !strings.Contains(got, "WireGuard clients") {
		t.Errorf("got %q — the replay claimed dezhban's own vocabulary", got)
	}
}

// state.json pairs tunnels[].name, which the walk redacts by key, with a detail
// that repeats the same word one key over — where only the replay can reach it.
func TestAnInterfaceNameTheBundleAlreadyKnowsIsReplacedInProse(t *testing.T) {
	got := New(true).JSON(`{"tunnels":[{"name":"nordlynx","detail":"nordlynx up"}]}`)
	if strings.Contains(got, "nordlynx") {
		t.Fatalf("the interface name survived: %q", got)
	}
	if strings.Count(got, "iface-1") != 2 {
		t.Errorf("got %q — the two copies are not the same token", got)
	}
}

// rules-preview.txt is a rendered ruleset, and a pf or nft rule carries an
// interface name as a bare word — no key, no dot, no attr. Only the replay can
// see it, and Text did not run the replay.
func TestARenderedRulesetLosesTheProviderInterface(t *testing.T) {
	r := New(true)
	r.JSON(`{"vpn":{"tunnelInterfaces":["nordlynx","utun4"]}}`)
	got := r.Text("pass out quick on { nordlynx utun4 } all no state\n" +
		"oifname { \"nordlynx\" } accept\n" +
		"pass out quick on lo0 all\n")
	if strings.Contains(got, "nordlynx") {
		t.Fatalf("the provider interface survived the ruleset: %q", got)
	}
	for _, keep := range []string{"utun4", "lo0", "pass out quick", "accept"} {
		if !strings.Contains(got, keep) {
			t.Errorf("got %q — %q is structural and must survive", got, keep)
		}
	}
}

// The daemon logs a single-label endpoint twice: once as an attr, once inside
// the resolver's error text. A name with no dot is invisible to hostRe, so the
// copy in the error stood while the attr went.
func TestASingleLabelHostSurvivesNowhereInALogLine(t *testing.T) {
	r := New(true)
	r.JSON(`{"vpn":{"endpoints":["mullvad"]}}`)
	got := r.Text(`level=ERROR msg="resolve failed" host=mullvad err="lookup mullvad: no such host"`)
	if strings.Contains(got, "mullvad") {
		t.Fatalf("the host survived: %q", got)
	}
	if strings.Count(got, "host-1") != 2 {
		t.Errorf("got %q — the attr and the error text are not the same token", got)
	}
}

// doctor.json carries its identifiers as fields so the walk sees them by key.
// The keys are the ones this switch already knows; a new spelling would fall to
// the default branch and get only the treatment the prose already had.
func TestADoctorDetailsIdentifierIsRedactedByItsKey(t *testing.T) {
	got := New(true).JSON(`{"checks":[{"name":"tunnels","details":[` +
		`{"iface":"nordlynx","text":"— 10.0.0.0/24"},` +
		`{"iface":"utun4","text":"— 10.1.0.0/24"},` +
		`{"endpoint":"198.51.100.7","text":":51820"}` +
		`]}]}`)
	if strings.Contains(got, "nordlynx") {
		t.Errorf("the interface name survived: %q", got)
	}
	if !strings.Contains(got, "utun4") {
		t.Errorf("got %q — a generic interface is structural and must survive", got)
	}
	if strings.Contains(got, "198.51.100.7") {
		t.Errorf("the endpoint survived: %q", got)
	}
	if !strings.Contains(got, `"text": "— 10.0.0.0/24"`) {
		t.Errorf("got %q — the private subnet is deliberately kept", got)
	}
}

// A doctor check's profiles field must share the config's token, or the bundle
// shows two identities where the host has one. And it must not fire on
// config.json's own `profiles`, which holds objects rather than names.
func TestADoctorChecksProfileFieldSharesTheConfigsToken(t *testing.T) {
	r := New(true)
	cfg := r.JSON(`{"vpn":{"profiles":[{"name":"work-nord"}]}}`)
	if !strings.Contains(cfg, "profile-1") || strings.Contains(cfg, "work-nord") {
		t.Fatalf("config = %q", cfg)
	}
	got := r.JSON(`{"checks":[{"name":"endpointRetention","profiles":["work-nord"]}]}`)
	if strings.Contains(got, "work-nord") {
		t.Fatalf("the entry name survived: %q", got)
	}
	if !strings.Contains(got, "profile-1") {
		t.Errorf("got %q — the same profile got a second token", got)
	}
}

// The connected VPN's service name is chosen by the user and usually names the
// provider. It is its own kind: a tunnelHint is an interface-name PREFIX whose
// allow-list would keep `wg`, and whose legend noun would mislabel this.
func TestAConnectedVPNNameIsRedacted(t *testing.T) {
	r := New(true)
	got := r.JSON(`{"checks":[{"name":"discover","connectedVPN":"Mullvad VPN"}]}`)
	if strings.Contains(got, "Mullvad") {
		t.Fatalf("the service name survived: %q", got)
	}
	if !strings.Contains(got, "vpn-1") {
		t.Errorf("got %q, want a vpn-N token", got)
	}
	legend := r.Legend()
	if len(legend) != 1 || !strings.Contains(legend[0], "VPN service name") {
		t.Errorf("legend = %v — the kind has no noun of its own", legend)
	}
}

// The mint guard is CROSS-KIND, and a review proposed scoping it to the kind as
// a tightening. It is the opposite, and this is the case that shows it.
//
// `work-nord` is both a profile name and an endpoint, so it holds two tokens.
// Text replays one of them into the log line, and endpointAttrRe then offers
// that token back under the `host` kind. Cross-kind, the guard recognises it and
// the line keeps the token it was given. Kind-scoped, `host` has not minted that
// spelling, so a SECOND token is minted for the first one — the bundle carries a
// token standing for a token, and the legend counts a hostname that is nowhere
// in it.
func TestAReplayedTokenIsNotReMintedUnderAnotherKind(t *testing.T) {
	r := New(true)
	r.JSON(`{"vpn":{"profiles":[{"name":"work-nord"}],"endpoints":["work-nord"]}}`)
	before := append([]string(nil), r.Legend()...)

	got := r.Text(`level=WARN host=work-nord msg="resolve failed"`)
	if strings.Contains(got, "work-nord") {
		t.Fatalf("the name survived: %q", got)
	}
	if strings.Contains(got, "host-2") {
		t.Errorf("got %q — a token was minted for a token", got)
	}
	if after := r.Legend(); !slices.Equal(before, after) {
		t.Errorf("the legend grew replaying a name it already knew:\n  before: %v\n  after:  %v", before, after)
	}
}

// The one case the redaction does NOT cover, pinned so it stays true of the docs
// that now state it (docs/usage/cli.md, and residual 1 in this package's doc
// comment). A name spelled like a token ALREADY MINTED keeps its spelling: the
// token is in entries that are already written, and nothing can move it.
//
// Pinned rather than merely documented, because this is the shape of thing that
// gets "fixed" by someone who has not read why — and the fix that suggests
// itself, making the mint guard kind-scoped, is the regression
// TestAReplayedTokenIsNotReMintedUnderAnotherKind rejects.
func TestANameSpelledLikeAnExistingTokenKeepsItsSpelling(t *testing.T) {
	r := New(true)
	r.JSON(`{"vpn":{"profiles":[{"name":"alpha"}]}}`) // mints profile-1

	got := r.JSON(`{"vpn":{"tunnelInterfaces":["profile-1"]}}`)
	if !strings.Contains(got, `"profile-1"`) {
		t.Errorf("got %q — the documented residual no longer holds; docs/usage/cli.md says it does", got)
	}
	// And the bundle does not grow a second identity for it: the legend still
	// names exactly the one profile.
	legend := r.Legend()
	if len(legend) != 1 || !strings.Contains(legend[0], "1 distinct profile name") {
		t.Errorf("legend = %v, want the single profile and no interface kind", legend)
	}
}
