package help

import (
	"strings"
	"testing"
)

// Only a table that heads its first column "Field" documents config keys. Every
// other lone code span in a first cell is something else — a CIDR range in
// concepts/modes.md, a subcommand in usage/cli.md — and minting `key-` anchors for
// those contradicted Rendered.Keys' own description and, worse, extended the
// collision guard (which aborts the whole app build) to pages with no keys at all.
func TestOnlyFieldHeadedTablesDefineKeys(t *testing.T) {
	fields := "| Field | Default |\n|---|---|\n| `vpn.redialWindow` | `30s` |\n"
	other := "| Range | Meaning |\n|---|---|\n| `100.64/10` | CGNAT |\n"

	r := Render("probe.md", "## A\n\n"+fields+"\n## B\n\n"+other)

	if !strings.Contains(r.HTML, `<tr id="key-vpnredialwindow"`) {
		t.Error("a Field-headed row did not get its anchor")
	}
	if strings.Contains(r.HTML, `id="key-1006410"`) {
		t.Error("a non-Field table minted a key anchor")
	}
	if len(r.Keys) != 1 {
		t.Errorf("expected exactly one documented key, got %d", len(r.Keys))
	}
}

// The presets and retired tables in docs/usage/config.md head their first column
// "Key", so a summary row cannot claim a key's anchor even when it comes first —
// which is what previously made the guarantee depend on section order.
func TestASummaryTableCannotClaimAKeysAnchor(t *testing.T) {
	summary := "| Key | Strict |\n|---|---|\n| `pollInterval` | `1s` |\n"
	definition := "| Field | Default |\n|---|---|\n| `pollInterval` | `5s` |\n"

	r := Render("probe.md", "## Presets\n\n"+summary+"\n## Fields\n\n"+definition)

	if strings.Count(r.HTML, `id="key-pollinterval"`) != 1 {
		t.Fatalf("expected exactly one anchored row, got %d",
			strings.Count(r.HTML, `id="key-pollinterval"`))
	}
	// And it is the definition row: the one whose Default cell says 5s.
	i := strings.Index(r.HTML, `id="key-pollinterval"`)
	if !strings.Contains(r.HTML[i:min(i+240, len(r.HTML))], "5s") {
		t.Error("the anchor landed on the summary row rather than the definition")
	}
	if len(r.Unsupported) != 0 {
		t.Errorf("a legitimate summary repeat was reported: %v", r.Unsupported)
	}
}
