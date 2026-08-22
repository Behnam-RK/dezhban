package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestKeyRowsAnchorToTheirDefinitionSection pins the document ordering that
// help.claimKey silently depends on.
//
// claimKey gives a key's row anchor to the FIRST table row naming that key on the
// page — it cannot tell a definition from a summary, and refusing a duplicate
// loudly would fail the build on every legitimately repeated key. So the guarantee
// that a contextual help link lands on the definition rather than on the Presets
// comparison is a property of how config.md is *arranged*. Nothing in the renderer
// or the resolution test notices if that changes: both stay green as long as some
// row carries the anchor.
//
// This is the test that notices. For every settable key, the first table row
// naming it must fall under one of the reference's definition headings.
func TestKeyRowsAnchorToTheirDefinitionSection(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "usage", "config.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	// The sections that define keys, as opposed to comparing or retiring them.
	definition := map[string]bool{
		"Fields":                             true,
		"`control` block":                    true,
		"`vpn` block":                        true,
		"Advanced tunables (`vpn.advanced`)": true,
	}

	firstCell := regexp.MustCompile("^\\|\\s*`([^`]+)`")
	heading := regexp.MustCompile(`^#{2,6}\s+(.*)$`)

	seen := map[string]string{} // key -> heading it was first seen under
	section := ""
	inCode := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		if m := heading.FindStringSubmatch(line); m != nil {
			section = strings.TrimSpace(m[1])
			continue
		}
		if m := firstCell.FindStringSubmatch(line); m != nil {
			if _, dup := seen[m[1]]; !dup {
				seen[m[1]] = section
			}
		}
	}

	checked := 0
	for _, tun := range Tunables() {
		if tun.DocKeyAnchor == "" {
			continue // documented in prose, so no row to order
		}
		where, ok := seen[tun.Key]
		if !ok {
			t.Errorf("%s: no table row names this key, so it has no anchor to claim", tun.Key)
			continue
		}
		if !definition[where] {
			t.Errorf("%s: its first table row is under %q, so the help link would land there "+
				"rather than on its definition", tun.Key, where)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("checked no keys — this test is no longer pinning the document order")
	}
}
