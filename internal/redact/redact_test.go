package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

// The property the whole bundle rests on: a real endpoint must not survive.
func TestAPublicAddressIsReplaced(t *testing.T) {
	r := New(true)
	got := r.Text(`vpn.endpoints = ["203.0.113.7"], exit 198.51.100.9`)
	if strings.Contains(got, "203.0.113.7") || strings.Contains(got, "198.51.100.9") {
		t.Fatalf("a public address survived: %q", got)
	}
	if !strings.Contains(got, "ip-1") || !strings.Contains(got, "ip-2") {
		t.Errorf("got %q, want stable ip-N placeholders", got)
	}
}

// Stable, not flattened. "The rules pass one address but the endpoint is
// another" is the finding; one shared [redacted] token would hide it.
func TestTheSameAddressAlwaysGetsTheSamePlaceholder(t *testing.T) {
	r := New(true)
	first := r.Text("endpoint 203.0.113.7")
	second := r.Text("pass out to 203.0.113.7")
	third := r.Text("other 203.0.113.8")

	if !strings.Contains(first, "ip-1") || !strings.Contains(second, "ip-1") {
		t.Errorf("the same address got different placeholders: %q / %q", first, second)
	}
	if !strings.Contains(third, "ip-2") {
		t.Errorf("a different address reused a placeholder: %q", third)
	}
}

// Redacting these protects nobody — every install has them — and destroys the
// reader's ability to see what a rule does.
func TestStructuralAddressesAreKept(t *testing.T) {
	r := New(true)
	for _, addr := range []string{
		"127.0.0.1", "0.0.0.0", "::1", "10.0.0.1", "192.168.1.1", "172.16.0.1",
		"169.254.0.1", "224.0.0.1", "fe80::1",
	} {
		if got := r.Text("pass to " + addr); !strings.Contains(got, addr) {
			t.Errorf("%s was redacted; it identifies nobody and hides what the rule does: %q", addr, got)
		}
	}
}

// A subnet's prefix length says "this is a subnet rule". That is structure, not
// identity, and losing it makes a ruleset unreadable.
func TestAPrefixLengthSurvives(t *testing.T) {
	got := New(true).Text("block to 203.0.113.0/24")
	if !strings.HasSuffix(strings.TrimSpace(got), "/24") {
		t.Errorf("got %q, want the /24 kept", got)
	}
	if strings.Contains(got, "203.0.113") {
		t.Errorf("the network address survived: %q", got)
	}
}

// The direction that makes this safe: an unknown hostname is redacted. A
// deny-list would leak every provider nobody thought of — which is exactly the
// VPN provider this exists to hide.
func TestAnUnknownHostnameIsRedacted(t *testing.T) {
	got := New(true).Text("resolving nl-free-01.protonvpn.net")
	if strings.Contains(got, "protonvpn") {
		t.Fatalf("a VPN provider's hostname survived: %q", got)
	}
	if !strings.Contains(got, "host-1") {
		t.Errorf("got %q, want a host-N placeholder", got)
	}
}

// The shipped geo providers are in every install's default config, and which
// one answered is a real diagnostic. Redacting them costs information and
// protects nothing.
func TestShippedGeoProvidersAreKept(t *testing.T) {
	r := New(true)
	for _, host := range []string{"get.geojs.io", "api.country.is", "ip-api.com", "ipinfo.io"} {
		if got := r.Text("provider " + host + " answered"); !strings.Contains(got, host) {
			t.Errorf("%s was redacted: %q", host, got)
		}
	}
}

// dezhban's OWN filenames are noise, not identifiers, and a bundle that hid them
// would be hard to read for no gain.
func TestDezhbanFilenamesAreNotHostnames(t *testing.T) {
	r := New(true)
	for _, name := range []string{"learned.json", "dezhban.log", "README.txt"} {
		if got := r.Text("wrote " + name); !strings.Contains(got, name) {
			t.Errorf("%s was treated as a hostname: %q", name, got)
		}
	}
}

// The user's OWN filenames are the opposite case, and this is the direction the
// suffix list used to have backwards.
//
// A `.conf`/`.ovpn` file is named after the VPN it configures, so
// `mullvad-frankfurt.conf` states the provider and the city outright — while
// being no kind of hostname, which is exactly why a "these endings mean it is a
// file" rule waved it through. `.sh` and `.md` are worse still: both are
// delegated TLDs, so a provider can simply live on one.
func TestUserFilenamesAndRealTLDsAreRedacted(t *testing.T) {
	r := New(true)
	for _, name := range []string{
		"mullvad-frankfurt.conf", "work-vpn.ovpn", "provider.sh", "provider.md",
	} {
		if got := r.Text("imported " + name); strings.Contains(got, name) {
			t.Errorf("%s survived redaction: %q", name, got)
		}
	}
}

// An mDNS name is the machine, and a Mac's is built from its owner's name. It
// was the single largest identifier the suffix list let through.
func TestLocalNamesAreRedacted(t *testing.T) {
	r := New(true)
	if got := r.Text("host=firstname-macbook.local"); strings.Contains(got, "firstname-macbook") {
		t.Errorf("an mDNS name survived redaction: %q", got)
	}
}

// Profile names and tunnel hints are not address-shaped at all — they are
// ordinary words — and they name the provider as plainly as a server address
// does. No shape-based rule can see them, so they get a field-aware pass and a
// placeholder kind of their own.
func TestProfileNamesAreRedacted(t *testing.T) {
	r := New(true)
	body := `{"activeProfile":"mullvad-de","profiles":[{"name":"nordvpn-ch","tunnelHint":"nordlynx"}]}`
	got := r.Text(body)
	for _, leaked := range []string{"mullvad-de", "nordvpn-ch", "nordlynx"} {
		if strings.Contains(got, leaked) {
			t.Errorf("%s survived redaction: %q", leaked, got)
		}
	}
	// Stable and DISTINCT: three different names must not collapse onto one
	// token, or "the active profile is not the one that was imported" stops
	// being visible in the bundle. The hint carries its OWN kind — it is an
	// interface-name prefix, display-only, and counting it as a profile name
	// made the legend claim a VPN profile the host does not have.
	for _, want := range []string{"profile-1", "profile-2", "hint-1"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %q", want, got)
		}
	}
	// The same name twice is the same token.
	twice := r.Text(`profile=mullvad-de and profile=mullvad-de`)
	if strings.Count(twice, "profile-1") != 2 {
		t.Errorf("a repeated profile name did not get a stable token: %q", twice)
	}
}

// A home directory names the account, which names the person. The rest of the
// path is structural and stays readable.
func TestHomeDirectoryAccountNamesAreRedacted(t *testing.T) {
	r := New(true)
	got := r.Text("imported /Users/firstname/Downloads/wg0.conf and /home/firstname/vpn")
	if strings.Contains(got, "firstname") {
		t.Errorf("an account name survived redaction: %q", got)
	}
	if !strings.Contains(got, "/Users/user-1/") || !strings.Contains(got, "/home/user-1/") {
		t.Errorf("the same account should be one stable token on both paths: %q", got)
	}
	if !strings.Contains(got, "/Downloads/") {
		t.Errorf("the structural part of the path should survive: %q", got)
	}
}

// The legend counts the new kinds by name, or a reader cannot tell what class of
// identifier the bundle held.
func TestLegendNamesTheNewKinds(t *testing.T) {
	r := New(true)
	r.Text(`{"activeProfile":"mullvad-de"} /Users/firstname/x`)
	legend := strings.Join(r.Legend(), "\n")
	for _, want := range []string{"profile name", "account name"} {
		if !strings.Contains(legend, want) {
			t.Errorf("legend does not mention %q: %q", want, legend)
		}
	}
	// And still never the originals.
	for _, leaked := range []string{"mullvad-de", "firstname"} {
		if strings.Contains(legend, leaked) {
			t.Errorf("legend leaked %q: %q", leaked, legend)
		}
	}
}

// Version strings and times are address-shaped to a loose regexp. Redacting
// them makes the bundle harder to read and protects nothing.
func TestNonAddressesAreLeftAlone(t *testing.T) {
	r := New(true)
	for _, s := range []string{"v0.10.1", "1.2.3.4.5", "took 1.25s"} {
		if got := r.Text(s); got != s {
			t.Errorf("%q was rewritten to %q", s, got)
		}
	}
}

// Disabled is the explicit opt-out and must be a true pass-through — the same
// code path, so the full-fidelity case cannot drift down a less-tested route.
func TestDisabledIsAPassThrough(t *testing.T) {
	in := "endpoint 203.0.113.7 at nl-free-01.protonvpn.net"
	if got := New(false).Text(in); got != in {
		t.Errorf("got %q, want the input unchanged", got)
	}
	if legend := New(false).Legend(); legend != nil {
		t.Errorf("a disabled redactor produced a legend: %v", legend)
	}
}

// The legend ships INSIDE the bundle. Putting the originals in it would undo
// the entire exercise.
func TestTheLegendNeverContainsTheOriginals(t *testing.T) {
	r := New(true)
	r.Text("endpoint 203.0.113.7 at nl-free-01.protonvpn.net and 198.51.100.9")
	legend := strings.Join(r.Legend(), "\n")
	if legend == "" {
		t.Fatal("no legend was produced")
	}
	for _, secret := range []string{"203.0.113.7", "198.51.100.9", "protonvpn"} {
		if strings.Contains(legend, secret) {
			t.Errorf("the legend leaked %q: %s", secret, legend)
		}
	}
	// Counts, not one line per placeholder: a token with no original beside it
	// carries no information, and a real bundle mints dozens of them.
	if len(r.Legend()) != 2 {
		t.Errorf("legend = %v, want one line per kind", r.Legend())
	}
	if !strings.Contains(legend, "2 distinct IP addresses") ||
		!strings.Contains(legend, "1 distinct hostname") {
		t.Errorf("legend does not report the counts: %s", legend)
	}
}

// A pf ruleset is the densest concentration of identifiers in the bundle, and
// the one most likely to be pasted into an issue.
func TestARealRulesetLosesEveryIdentifier(t *testing.T) {
	ruleset := `
set skip on lo0
pass out quick on utun4 all
pass out quick proto udp to 203.0.113.7 port 51820
pass out quick to 198.51.100.9
pass out quick to 192.168.1.0/24
block drop out all
`
	got := New(true).Text(ruleset)
	for _, secret := range []string{"203.0.113.7", "198.51.100.9"} {
		if strings.Contains(got, secret) {
			t.Errorf("%s survived:\n%s", secret, got)
		}
	}
	// Structure has to survive or the ruleset is unreadable.
	for _, kept := range []string{"utun4", "lo0", "port 51820", "192.168.1.0/24", "block drop out all"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q was lost:\n%s", kept, got)
		}
	}
}

// An IPv6 address whose compression sits at either END must not survive.
//
// The matcher used `\b` for its boundaries, and `\b` is the wrong boundary for
// this alphabet: there is no word boundary in front of a leading `::`, so
// `::ffff:cb00:7107` matched only its `ffff:cb00:7107` tail — which is not a
// parseable address, and an unparseable match is returned verbatim. The whole
// address therefore left the machine inside a bundle that said it had been
// redacted, which is the one failure this package must not have. `2001:db8::`
// is the mirror case, failing the trailing boundary.
func TestACompressedIPv6AtEitherEndIsRedacted(t *testing.T) {
	for _, in := range []string{
		"::ffff:cb00:7107",
		"::abcd:1234:5678",
		"2001:db8::",
		`"endpoints": ["2001:db8::"]`,
		"block drop out quick from any to ::abcd:1234:5678",
	} {
		got := New(true).Text(in)
		for _, leak := range []string{"cb00", "abcd:1234", "2001:db8"} {
			if strings.Contains(in, leak) && strings.Contains(got, leak) {
				t.Errorf("Text(%q) = %q — %q survived", in, got, leak)
			}
		}
	}
}

// Every endpoint config ACCEPTS must be redacted, not only the ones the
// hostname shape happens to match.
//
// config.isPlausibleHostname takes single-label names and last labels carrying
// digits; hostRe requires a dot and an all-letter last label. The gap between
// them is a provider's own server name copied verbatim into a bundle — exactly
// what the allow-list direction exists to prevent — so the endpoint FIELDS are
// read by name, the way the profile names already are.
func TestAnEndpointFieldIsRedactedEvenWhenItIsNotHostShaped(t *testing.T) {
	for _, tc := range []struct{ in, leak string }{
		{`"endpoints": ["mullvad"]`, "mullvad"},
		{`"endpoints": ["vpn.123abc"]`, "vpn.123abc"},
		{`"endpoints": ["se-sto-01", "203.0.113.9"]`, "se-sto-01"},
		{`"addr": "nordlynx"`, "nordlynx"},
		{`level=WARN msg="resolution failed" host=vpn.123abc`, "vpn.123abc"},
		{`level=WARN msg=dropping endpoint=mullvad`, "endpoint=mullvad"},
	} {
		if got := New(true).Text(tc.in); strings.Contains(got, tc.leak) {
			t.Errorf("Text(%q) = %q — %q survived", tc.in, got, tc.leak)
		}
	}
}

// The endpoint pass must not eat what the shape passes read correctly: a
// provider URL keeps its allow-listed host (which provider answered is a real
// diagnostic), a count is a count, and a structural address stays readable.
func TestTheEndpointPassLeavesStructureAlone(t *testing.T) {
	for _, in := range []string{
		`level=DEBUG msg="ipv6 lookup failed" endpoint=https://get.geojs.io/v1/ip.json`,
		`level=INFO msg="vpn guard active" tunnels=[utun4] endpoint=443`,
		`"endpoints": ["192.168.1.1"]`,
		`time=2026-09-08T10:15:43.972Z level=WARN msg=late`,
	} {
		if got := New(true).Text(in); got != in {
			t.Errorf("Text(%q) = %q, want it unchanged", in, got)
		}
	}
}

// A doctor CHECK name is not a profile name, and must survive.
//
// `"name"` was matched as a bare key, so every check in doctor.json — the one
// file a reader opens first — came back as `profile-N`, and the legend told
// them the host had nine VPN profiles when it had none. A check name is
// dezhban's own vocabulary, identical on every install, like the geo providers
// this package already keeps. The profile names it was aimed at live inside
// config's `profiles` and learned.json's `entries`, and still go.
func TestADoctorCheckNameIsNotAProfileName(t *testing.T) {
	r := New(true)
	got := r.Text(`{"checks":[{"name":"vpn endpoints","status":"warn"},{"name":"tunnel interfaces","status":"ok"}]}`)
	for _, want := range []string{"vpn endpoints", "tunnel interfaces"} {
		if !strings.Contains(got, want) {
			t.Errorf("check name %q was redacted: %s", want, got)
		}
	}
	if legend := r.Legend(); len(legend) != 0 {
		t.Errorf("a doctor report minted placeholders: %v", legend)
	}
}

// The profile names the pass above narrowed around must still go, in both
// documents that carry them.
func TestAProfileNameInsideItsArrayIsStillRedacted(t *testing.T) {
	for _, tc := range []struct{ in, leak string }{
		{`{"profiles":[{"name":"mullvad-de","endpoints":["203.0.113.9"]}]}`, "mullvad-de"},
		{`{"entries":[{"name":"work-nord","endpoints":[{"addr":"203.0.113.9"}]}]}`, "work-nord"},
		{`{"activeProfile":"mullvad-de"}`, "mullvad-de"},
	} {
		if got := New(true).Text(tc.in); strings.Contains(got, tc.leak) {
			t.Errorf("Text(%q) = %q — %q survived", tc.in, got, tc.leak)
		}
	}
}

// A range of one is not a range. "ip-1 … ip-1" reads as a rendering bug, in
// the one file in the bundle that has to look trustworthy.
func TestTheLegendDoesNotRenderARangeOfOne(t *testing.T) {
	r := New(true)
	r.Text("endpoint 203.0.113.7")
	legend := r.Legend()
	if len(legend) != 1 || legend[0] != "1 distinct IP address → ip-1" {
		t.Errorf("legend = %v, want a single unranged entry", legend)
	}
}

// A name carrying a quote must be matched WHOLE.
//
// The JSON-string patterns used `[^"]+`, which stops at a backslash-escaped
// quote: `{"name":"foo\"bar"}` minted a placeholder for the first half, left
// `bar` in the bundle, and produced invalid JSON on the way out. `report`
// copies config.json off disk verbatim and never validates it, so a
// hand-edited config — exactly the kind a bundle gets collected for — reaches
// this.
func TestAnEscapedQuoteDoesNotSplitTheValue(t *testing.T) {
	for _, tc := range []struct{ in, leak string }{
		{`{"profiles":[{"name":"foo\"bar"}]}`, "bar"},
		{`{"activeProfile":"work\"nord"}`, "nord"},
		{`{"endpoints":["se\"sto"]}`, "sto"},
		{`{"addr":"vpn\"one"}`, "one"},
	} {
		got := New(true).Text(tc.in)
		if strings.Contains(got, tc.leak) {
			t.Errorf("Text(%q) = %q — %q survived", tc.in, got, tc.leak)
		}
		// The tail used to be left beside a stray quote, which also broke the
		// JSON the bundle ships.
		if !json.Valid([]byte(got)) {
			t.Errorf("Text(%q) = %q — the bundle would carry invalid JSON", tc.in, got)
		}
	}
}

// The same name written two ways is one name. Keying on the escaped spelling
// would hand it two placeholders and hide that they are the same server.
func TestAnEscapedValueKeysOnItsUnescapedForm(t *testing.T) {
	r := New(true)
	// `\u002D` IS `-`. Two spellings of one name, so a redactor keying on the
	// raw text mints two tokens and the bundle stops showing that the active
	// profile and the logged one are the same. The first version of this test
	// wrote the same literal twice, so jsonBody never ran and gutting it left
	// the test green.
	got := r.Text(`{"activeProfile":"nord\u002Dse"}` + "\n" + `profile=nord-se`)
	if strings.Count(got, "profile-1") != 2 {
		t.Errorf("got %q, want one stable token for both spellings", got)
	}
}

// The switch window's profile name is in state.json under `"profile"`, and a
// window being open is the likeliest moment to collect a bundle — so the file
// most likely to name a VPN was the one no pass was looking at.
func TestTheSwitchWindowsProfileNameIsRedacted(t *testing.T) {
	in := `{"switch":{"open":true,"profile":"mullvad-de","until":"2026-09-08T10:00:00Z"}}`
	if got := New(true).Text(in); strings.Contains(got, "mullvad-de") {
		t.Errorf("Text(%q) = %q — the switch window's profile survived", in, got)
	}
}

// The name scoping must never FAIL OPEN.
//
// profileArrayRe balances brackets by hand: a `[` inside a name made it match
// nothing at all — so every name in the document shipped verbatim — and a `]`
// truncated the body, so every name after it did. That is worse than the
// over-redaction the scoping was introduced to fix, because it is a leak.
func TestNameScopingDoesNotFailOpenOnABracket(t *testing.T) {
	for _, in := range []string{
		`{"profiles":[{"name":"work[de"},{"name":"mullvad-se"}]}`,
		`{"profiles":[{"name":"a]b"},{"name":"mullvad-se"}]}`,
		`{"entries":[{"name":"nord-ch","endpoints":[{"addr":"203.0.113.4"}],"tags":[["x"]]}]}`,
	} {
		got := New(true).Text(in)
		for _, leak := range []string{"mullvad-se", "work[de", "a]b", "nord-ch"} {
			if strings.Contains(in, leak) && strings.Contains(got, leak) {
				t.Errorf("Text(%q) = %q — %q survived", in, got, leak)
			}
		}
	}
}

// `_unattributed` is dezhban's own constant, identical on every install. Minting
// it as `profile-N` hides a word that names nobody and tells the legend the host
// has one more VPN profile than it has.
func TestDezhbansOwnEntryNameIsKept(t *testing.T) {
	r := New(true)
	in := `{"entries":[{"name":"_unattributed","endpoints":[{"addr":"203.0.113.4"}]}]}`
	if got := r.Text(in); !strings.Contains(got, "_unattributed") {
		t.Errorf("Text(%q) = %q — dezhban's own entry name was redacted", in, got)
	}
	for _, line := range r.Legend() {
		if strings.Contains(line, "profile") {
			t.Errorf("the legend counted a profile that does not exist: %q", line)
		}
	}
}

// A value that is NOT replaced must go back exactly as it was written. Writing
// the unescaped body back unconditionally put a literal tab inside a JSON
// string, so the bundle carried a file that no longer parses — over a value
// nothing needed to hide.
func TestAnUnreplacedValueKeepsItsEscaping(t *testing.T) {
	for _, in := range []string{
		`{"addr":"a\tb"}`,
		`{"endpoints":["x\ty"]}`,
		`{"addr":"192.168.1.1"}`,
	} {
		got := New(true).Text(in)
		if !json.Valid([]byte(got)) {
			t.Errorf("Text(%q) = %q — the bundle would carry invalid JSON", in, got)
		}
	}
}

// dezhban ships on Windows, where the account name lives in `C:\Users\Alice\…`.
// Matching only the unix spelling left it in a bundle that says it redacts it,
// on a whole platform.
func TestAWindowsHomeDirectoryAccountNameIsRedacted(t *testing.T) {
	for _, in := range []string{
		`C:\Users\alice\AppData\Roaming\dezhban`,
		`"C:\\Users\\alice\\AppData"`, // as it arrives inside JSON or a quoted log value
	} {
		got := New(true).Text(in)
		if strings.Contains(got, "alice") {
			t.Errorf("Text(%q) = %q — the account name survived", in, got)
		}
		// The rest of the path is structural and stays readable.
		if !strings.Contains(got, "AppData") {
			t.Errorf("Text(%q) = %q — the path structure was lost", in, got)
		}
	}
}
