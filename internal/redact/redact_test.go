package redact

import (
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
	// being visible in the bundle.
	for _, want := range []string{"profile-1", "profile-2", "profile-3"} {
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
