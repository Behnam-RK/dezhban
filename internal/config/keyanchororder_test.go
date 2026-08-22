package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The sections of the reference that *define* keys, as opposed to comparing,
// renaming or retiring them.
var definitionSections = map[string]bool{
	"Fields":                             true,
	"`control` block":                    true,
	"`vpn` block":                        true,
	"Advanced tunables (`vpn.advanced`)": true,
}

// A *lone* code span in the first cell, exactly as help.rowKey requires — nothing
// else in the cell.
//
// Looser than that, this test recorded rows the renderer never claims: a row
// written "| `vpn.redialWindow` (renamed) | … |", the natural way to annotate the
// Renamed table, would have been reported as that key's first row and failed the
// build even though claimKey had correctly anchored the definition. A spurious
// failure on the one test that exists to catch a silent one.
var firstCellKey = regexp.MustCompile("^\\|\\s*`([^`]+)`\\s*\\|")

var sectionHeading = regexp.MustCompile(`^#{2,6}\s+(.*)$`)

// TestKeyRowsAnchorToTheirDefinitionSection pins the document ordering that
// help.claimKey silently depends on.
//
// claimKey gives a key's row anchor to the FIRST table row naming that key on the
// page — it cannot tell a definition from a summary, and refusing a duplicate
// loudly would fail the build on every legitimately repeated key. So the guarantee
// that a contextual help link lands on the definition rather than on the Presets
// comparison is a property of how the reference is *arranged*. Nothing in the
// renderer or the resolution test notices if that changes: both stay green as long
// as some row carries the anchor.
//
// This is the test that notices. For every settable key, the first table row naming
// it must fall under one of the reference's definition headings.
func TestKeyRowsAnchorToTheirDefinitionSection(t *testing.T) {
	// The pages come from the anchors themselves rather than being hardcoded, so a
	// tunable documented on some other bundled page is checked against that page
	// instead of being reported as having no row at all.
	pages := map[string]bool{}
	for _, tun := range Tunables() {
		if tun.DocKeyAnchor == "" {
			continue
		}
		page, _, _ := strings.Cut(tun.DocKeyAnchor, "#")
		pages[page] = true
	}
	if len(pages) == 0 {
		t.Fatal("no tunable carries a row anchor — this test is no longer pinning anything")
	}

	// key -> the heading its first row appeared under, per page.
	seen := map[string]map[string]string{}
	for page := range pages {
		path := filepath.Join("..", "..", "docs", filepath.FromSlash(page))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		seen[page] = firstRowSections(string(data))
	}

	checked := 0
	for _, tun := range Tunables() {
		if tun.DocKeyAnchor == "" {
			continue // documented in prose, so there is no row to order
		}
		page, _, _ := strings.Cut(tun.DocKeyAnchor, "#")
		where, ok := seen[page][tun.Key]
		if !ok {
			t.Errorf("%s: no table row on %s names this key, so it has no anchor to claim",
				tun.Key, page)
			continue
		}
		if !definitionSections[where] {
			t.Errorf("%s: its first table row on %s is under %q, so the help link would land "+
				"there rather than on its definition", tun.Key, page, where)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("checked no keys — this test is no longer pinning the document order")
	}
}

// firstRowSections maps each key to the heading under which its first table row
// appears, skipping fenced code the way the renderer does.
func firstRowSections(markdown string) map[string]string {
	out := map[string]string{}
	section := ""
	inCode := false
	for _, raw := range strings.Split(markdown, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		if m := sectionHeading.FindStringSubmatch(line); m != nil {
			section = strings.TrimSpace(m[1])
			continue
		}
		if m := firstCellKey.FindStringSubmatch(line); m != nil {
			if _, dup := out[m[1]]; !dup {
				out[m[1]] = section
			}
		}
	}
	return out
}
