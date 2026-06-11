// Package decision maps a monitor reading to an enforcement verdict. It is
// platform-independent and pure: the same input always yields the same Verdict,
// so it is exhaustively table-testable. Phase 4 layers fail-mode and hysteresis
// on top without changing this call site.
package decision

import (
	"strings"

	"github.com/behnam-rk/dezhban/internal/monitor"
)

// Verdict is what the decider concludes the firewall should do.
type Verdict int

const (
	// Allow means egress should flow (legacy) or the guard should hold (VPN).
	Allow Verdict = iota
	// Block means egress should be cut (legacy) or fully blocked (VPN).
	Block
)

func (v Verdict) String() string {
	if v == Block {
		return "Block"
	}
	return "Allow"
}

// Decider turns monitor results into verdicts against a country blocklist.
type Decider struct {
	// blocked is the set of upper-cased ISO-3166 alpha-2 codes that trigger Block.
	blocked map[string]bool
}

// New builds a Decider from the configured blocked country codes. Codes are
// upper-cased and trimmed so matching is case-insensitive regardless of how the
// provider or config spelled them.
func New(blockedCountries []string) *Decider {
	set := make(map[string]bool, len(blockedCountries))
	for _, c := range blockedCountries {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c != "" {
			set[c] = true
		}
	}
	return &Decider{blocked: set}
}

// Evaluate maps a monitor result to a verdict.
//
// Phase 3 semantics:
//   - lookup error  → Allow (Phase 4 flips this to fail-closed)
//   - country in blocklist → Block
//   - otherwise → Allow
func (d *Decider) Evaluate(r monitor.Result) Verdict {
	if r.Err != nil {
		return Allow
	}
	if d.blocked[strings.ToUpper(strings.TrimSpace(r.Reading.CountryCode))] {
		return Block
	}
	return Allow
}
