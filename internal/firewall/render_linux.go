//go:build linux

package firewall

// RenderRules returns the exact nftables ruleset text the linux backend would
// load for policy p, WITHOUT applying it. See render_darwin.go for the rationale:
// pure rendering is what lets `dezhban print-rules` show a block/guard before it
// is installed.
func RenderRules(p Policy) (string, error) {
	return renderNftRuleset(p), nil
}
