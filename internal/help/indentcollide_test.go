package help

import (
	"strings"
	"testing"
)

// An indented heading is still a heading, so a key row that collides with it must
// still be refused — the pre-scan and the main pass have to agree about what
// counts. They did not: the scan matched the raw line, the renderer the trimmed
// one, and a heading with up to three leading spaces slipped through to produce
// two elements sharing one id and a green build.
func TestIndentedHeadingStillBlocksACollidingKeyRow(t *testing.T) {
	md := "  ## Key flags\n\n| Field | Default |\n|---|---|\n| `flags` | `x` |\n"
	r := Render("probe.md", md)

	if !strings.Contains(r.HTML, `<h2 id="key-flags"`) {
		t.Fatal("the indented heading did not render as a heading; this test no longer tests anything")
	}
	if strings.Contains(r.HTML, `<tr id="key-flags"`) {
		t.Error("the key row claimed an anchor the heading already owns")
	}
	if len(r.Unsupported) == 0 {
		t.Error("the collision was not reported, so it would not fail the bundle build")
	}
}
