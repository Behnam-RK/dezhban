// Package decision maps monitor readings to an enforcement verdict. It is
// platform-independent. The raw country/error → verdict mapping is pure; on top
// of it sits a small hysteresis state machine so a single bad reading or a
// transient lookup blip does not flap the firewall.
//
// Phase 4 semantics:
//   - lookup error          → Block if FailClosed (default), else Allow
//   - country in blocklist  → Block
//   - otherwise             → Allow
//
// A raw verdict must repeat for Hysteresis consecutive readings before it is
// committed; until then Evaluate keeps returning the last committed verdict.
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

// Decider turns monitor results into committed verdicts against a country
// blocklist, applying fail-mode and hysteresis. It is stateful: Evaluate must be
// called once per reading, in order, on the same instance.
type Decider struct {
	// blocked is the set of upper-cased ISO-3166 alpha-2 codes that trigger Block.
	blocked map[string]bool
	// failClosed makes an undeterminable country (lookup error) raw-Block.
	failClosed bool
	// need is the hysteresis threshold: consecutive agreeing readings to commit.
	need int

	// current is the last committed verdict (what callers act on).
	current Verdict
	// candidate is the raw verdict the streak is counting toward.
	candidate Verdict
	// streak is how many consecutive readings have agreed on candidate.
	streak int
}

// New builds a Decider. Codes are upper-cased and trimmed so matching is
// case-insensitive. failClosed selects the lookup-error posture; hysteresis is
// the number of consecutive agreeing readings required to flip state (clamped to
// at least 1). The initial committed verdict is Allow — it matches both the
// legacy unblocked start and the VPN guard (the "allow" posture).
func New(blockedCountries []string, failClosed bool, hysteresis int) *Decider {
	set := make(map[string]bool, len(blockedCountries))
	for _, c := range blockedCountries {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c != "" {
			set[c] = true
		}
	}
	if hysteresis < 1 {
		hysteresis = 1
	}
	return &Decider{
		blocked:    set,
		failClosed: failClosed,
		need:       hysteresis,
		current:    Allow,
		candidate:  Allow,
	}
}

// raw maps a single reading to a verdict, ignoring history. An undeterminable
// country (lookup error) is Block when fail-closed, else Allow. A lookup error
// feeds the streak like any other reading, so N consecutive errors are needed to
// trip a fail-closed block — transient blips do not toggle.
func (d *Decider) raw(r monitor.Result) Verdict {
	if r.Err != nil {
		if d.failClosed {
			return Block
		}
		return Allow
	}
	if d.blocked[strings.ToUpper(strings.TrimSpace(r.Reading.CountryCode))] {
		return Block
	}
	return Allow
}

// Evaluate folds one reading into the hysteresis state machine and returns the
// committed verdict. A raw verdict that disagrees with the committed state must
// recur for `need` consecutive readings before it is committed; any reading that
// agrees with the committed state resets the pending streak.
func (d *Decider) Evaluate(r monitor.Result) Verdict {
	v := d.raw(r)
	if v == d.current {
		// Back in agreement with the committed state: abandon any pending flip.
		d.candidate = d.current
		d.streak = 0
		return d.current
	}
	if v == d.candidate {
		d.streak++
	} else {
		d.candidate = v
		d.streak = 1
	}
	if d.streak >= d.need {
		d.current = v
		d.streak = 0
	}
	return d.current
}
