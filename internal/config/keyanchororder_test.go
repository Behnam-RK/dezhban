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

// A table's separator row, and the first cell of a header row.
var (
	tableSeparator  = regexp.MustCompile(`^\|[\s:|-]+\|$`)
	firstHeaderCell = regexp.MustCompile(`^\|([^|]*)\|`)
)

// headsAKeyTable mirrors help.renderTable's gate: inline markup stripped, then a
// case-insensitive compare against "Field".
//
// A literal regex on the raw cell did not mirror it. `| `Field` |`, `| **Field** |`
// and `| FIELD |` all make the *renderer* mint row anchors from that table while a
// pattern matching bare "Field" stops treating it as a key table — so the guarantee
// this file exists to pin would silently stop covering it, and the `checked == 0`
// fatal does not help because it only fires when *every* key falls out, not a
// subset. Copied rather than called: internal/help's own tests import this package,
// so importing help from here would close an import cycle.
func headsAKeyTable(headerLine string) bool {
	m := firstHeaderCell.FindStringSubmatch(headerLine)
	if m == nil {
		return false
	}
	// Links and images reduce to their text FIRST, then emphasis characters go.
	// Both steps, because stripInline does both: with only the second, a header
	// written `| [Field](../x.md) |` makes the renderer mint row anchors for that
	// table while this file stops treating it as a key table — the coverage
	// narrowing silently, and the `checked == 0` fatal no help because it fires
	// only when EVERY key falls out, never a subset. The same near-miss the
	// comment above warns about, one construct over.
	cell := inlineImageText.ReplaceAllString(m[1], "$1")
	cell = inlineLinkText.ReplaceAllString(cell, "$1")
	cell = strings.Map(func(r rune) rune {
		switch r {
		case '`', '*', '_':
			return -1
		}
		return r
	}, cell)
	return strings.EqualFold(strings.TrimSpace(cell), "Field")
}

// Mirroring help's own inlineImage/inlineLink, image first so `![a](b)` does not
// reduce to a stray `!`.
var (
	inlineImageText = regexp.MustCompile(`!\[([^\]]*)\]\([^)]+\)`)
	inlineLinkText  = regexp.MustCompile(`\[([^\]]*)\]\([^)]+\)`)
)

// TestKeyRowsAnchorToTheirDefinitionSection pins the document arrangement that
// help.claimKey depends on.
//
// The renderer settles most of this itself now: only a table heading its first
// column "Field" defines keys, so config.md's presets and retired tables — headed
// "Key" — cannot claim an anchor however early they appear. What it still cannot
// settle is *two* Field-headed tables naming the same key, where the first one
// wins; refusing a duplicate loudly is not an option, since several keys are
// legitimately repeated.
//
// So this is the remaining guard, and it is a second line rather than the only one.
// For every settable key, the first Field-table row naming it must fall under one of
// the reference's definition headings. Nothing else notices if that changes: the
// resolution test stays green as long as some row carries the anchor.
func TestKeyRowsAnchorToTheirDefinitionSection(t *testing.T) {
	// Read out of the anchors rather than restated here. Today that always yields
	// exactly one page — docKeyAnchorFor builds every anchor from keyReferencePage —
	// so this is not extra coverage; it is the test declining to hold a second copy
	// of a constant it does not own. If the reference is ever split across pages,
	// this reads the split instead of failing on it.
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
	// Only rows inside a real table, and only under a "Field" header — the same two
	// conditions renderTable applies. Without the separator check a pipe-prefixed
	// prose line counted as a row; without the header check a presets or retired row
	// did, and neither is something claimKey would ever anchor. Both would have
	// failed the build over a row the renderer ignores, which is the false-failure
	// this test has already been fixed for once.
	lines := strings.Split(markdown, "\n")
	inFieldTable := false
	for i, raw := range lines {
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
			inFieldTable = false
			continue
		}
		if !strings.HasPrefix(line, "|") {
			inFieldTable = false
			continue
		}
		// A header row is one followed by a separator; that is what starts a table.
		if i+1 < len(lines) && tableSeparator.MatchString(strings.TrimSpace(lines[i+1])) {
			inFieldTable = headsAKeyTable(line)
			continue
		}
		if tableSeparator.MatchString(line) || !inFieldTable {
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
