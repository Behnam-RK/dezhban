//go:build darwin

package firewall

// RenderRules returns the exact pf ruleset text the darwin backend would load
// for policy p, WITHOUT applying it. It is pure (no pfctl, no root, no live
// firewall state), which is what makes `dezhban print-rules` safe: an operator
// can inspect precisely what a block/guard would install before risking a
// lockout. The host build compiles only its own backend, so this resolves to the
// platform whose firewall actually runs.
func RenderRules(p Policy) (string, error) {
	return renderRuleset(p), nil
}

// RulesetKind names the mechanism RenderRules writes for, so a surface showing
// the text does not have to infer a syntax from the platform it happens to be
// running on. Here: the pf ruleset `pfctl -a dezhban -f -` loads.
const RulesetKind = "pf"
