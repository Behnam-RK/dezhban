//go:build linux

package firewall

// RenderRules returns the exact nftables ruleset text the linux backend would
// load for policy p, WITHOUT applying it. See render_darwin.go for the rationale:
// pure rendering is what lets `dezhban print-rules` show a block/guard before it
// is installed.
func RenderRules(p Policy) (string, error) {
	return renderNftRuleset(p), nil
}

// RulesetKind names the mechanism RenderRules writes for, so a surface showing
// the text does not have to infer a syntax from the platform it happens to be
// running on. Here: the nftables ruleset `nft -f -` loads.
const RulesetKind = "nft"
