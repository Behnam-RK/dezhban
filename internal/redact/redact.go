// Package redact replaces the network identifiers in a diagnostic bundle with
// stable placeholders, so the bundle can be pasted into a public issue.
//
// What is sensitive here is specific: the VPN server addresses in the config and
// in learned.json (they name the provider, often the exact server), and the
// public exit IP in state.json (that is the user's location). A firewall
// ruleset carries both, because the whole point of the ruleset is which
// addresses may be reached.
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
	// Addresses first: an IPv4 literal also matches nothing in hostRe, but an
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

// keptSuffixes are the endings that mean "this is a file or an identifier, not
// a host we reached".
var keptSuffixes = []string{
	".json", ".log", ".conf", ".ovpn", ".plist", ".sh", ".go", ".swift", ".md",
	".dezhban", ".local", ".arpa", ".invalid", ".test",
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
	if kind == "host" {
		singular, plural = "hostname", "hostnames"
	}
	if n == 1 {
		return singular
	}
	return plural
}
