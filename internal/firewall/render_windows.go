//go:build windows

package firewall

// RenderRules returns the exact PowerShell/WFP script the windows backend would
// run for policy p, WITHOUT executing it. See render_darwin.go for the rationale:
// pure rendering is what lets `dezhban print-rules` show a block/guard before it
// is installed.
func RenderRules(p Policy) (string, error) {
	return renderBlockScript(p), nil
}

// RulesetKind names the mechanism RenderRules writes for, so a surface showing
// the text does not have to infer a syntax from the platform it happens to be
// running on. Here: the PowerShell that installs the WFP rules.
const RulesetKind = "wfp"
