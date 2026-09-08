// Package redact replaces the network identifiers in a diagnostic bundle with
// stable placeholders, so the bundle can be pasted into a public issue.
//
// What is sensitive here is specific: the VPN server addresses in the config and
// in learned.json (they name the provider, often the exact server), and the
// public exit IP in state.json (that is the user's location). A firewall
// ruleset carries both, because the whole point of the ruleset is which
// addresses may be reached.
//
// Two identifiers in a bundle are not address-shaped and are just as telling:
// the PROFILE NAMES the user chose (they are called "mullvad-de", and a tunnel
// hint is "nordlynx"), and the ACCOUNT NAME in every home-directory path. Both
// get placeholders of their own kind.
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
package redact

import (
	"fmt"
	"net/netip"
	"regexp"
	"sort"
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
	ipv6Re = regexp.MustCompile(`\b(?:[0-9A-Fa-f]{0,4}:){2,7}[0-9A-Fa-f]{0,4}(?:%[0-9A-Za-z._-]+)?(?:/\d{1,3})?\b`)
	// A dotted name with a TLD-ish last label. Matched after addresses so a
	// dotted quad is never mistaken for one.
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
	profileJSONRe = regexp.MustCompile(`("(?:name|activeProfile|tunnelHint)"\s*:\s*")([^"]+)(")`)
	profileAttrRe = regexp.MustCompile(`\b(profile|activeProfile|tunnelHint)=("[^"]*"|[^\s]+)`)
	// A home directory names the account, which names the person. The segment
	// after /Users or /home is the only identifying part — the rest of the path
	// is structural and worth reading.
	homeDirRe = regexp.MustCompile(`(/(?:Users|home)/)([^/\s"']+)`)
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
}

// New returns a Redactor. enabled false is the explicit opt-out: everything
// passes through untouched.
func New(enabled bool) *Redactor {
	return &Redactor{Enabled: enabled, seen: map[string]string{}}
}

// Text rewrites every address and hostname in s.
func (r *Redactor) Text(s string) string {
	if !r.Enabled {
		return s
	}
	// Names first, while the field they sit in is still visible: once a value has
	// been rewritten by a later pass there is no key left to recognise it by.
	// Each yields a placeholder with no dots, so the passes below leave it alone.
	s = profileJSONRe.ReplaceAllStringFunc(s, func(m string) string {
		g := profileJSONRe.FindStringSubmatch(m)
		return g[1] + r.placeholder(strings.ToLower(g[2]), "profile") + g[3]
	})
	s = profileAttrRe.ReplaceAllStringFunc(s, func(m string) string {
		g := profileAttrRe.FindStringSubmatch(m)
		return g[1] + "=" + r.placeholder(strings.ToLower(strings.Trim(g[2], `"`)), "profile")
	})
	s = homeDirRe.ReplaceAllStringFunc(s, func(m string) string {
		g := homeDirRe.FindStringSubmatch(m)
		return g[1] + r.placeholder(strings.ToLower(g[2]), "user")
	})
	// Addresses next: an IPv4 literal also matches nothing in hostRe, but an
	// IPv6 zone or a bracketed form could confuse the host pattern, and doing
	// the precise patterns first keeps the loose one from claiming them.
	s = ipv4Re.ReplaceAllStringFunc(s, func(m string) string { return r.address(m) })
	s = ipv6Re.ReplaceAllStringFunc(s, func(m string) string { return r.address(m) })
	s = hostRe.ReplaceAllStringFunc(s, func(m string) string { return r.host(m) })
	return s
}

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
	if keepAddr(addr) {
		return m
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

// placeholder returns the stable token for one value, minting it on first sight.
func (r *Redactor) placeholder(value, kind string) string {
	key := kind + ":" + value
	if p, ok := r.seen[key]; ok {
		return p
	}
	n := 0
	for _, k := range r.order {
		if strings.HasPrefix(k, kind+":") {
			n++
		}
	}
	p := fmt.Sprintf("%s-%d", kind, n+1)
	r.seen[key] = p
	r.order = append(r.order, key)
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
	counts := map[string]int{}
	for _, key := range r.order {
		kind, _, _ := strings.Cut(key, ":")
		counts[kind]++
	}
	kinds := make([]string, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)

	out := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		n := counts[kind]
		out = append(out, fmt.Sprintf("%d distinct %s → %s-1 … %s-%d",
			n, kindNoun(kind, n), kind, kind, n))
	}
	return out
}

func kindNoun(kind string, n int) string {
	singular, plural := "IP address", "IP addresses"
	switch kind {
	case "host":
		singular, plural = "hostname", "hostnames"
	case "profile":
		singular, plural = "profile name", "profile names"
	case "user":
		singular, plural = "account name", "account names"
	}
	if n == 1 {
		return singular
	}
	return plural
}
