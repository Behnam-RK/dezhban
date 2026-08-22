package help

import (
	"strings"
	"testing"
)

// Two different keys whose slugs collide is a silently wrong deep link, and has to
// be told apart from the same key appearing in two tables — which is normal and is
// refused quietly on purpose.
//
// Anchor lowercases and drops `.`, so the colliding pairs are a case-only rename
// (`vpn.armAtBoot` / `vpn.armatboot`, both `key-vpnarmatboot`) and one that removes
// a dot (`vpn.pauseMax` / `vpnpauseMax`). Hyphens survive, so `vpn.pause-max` is
// *not* one of them — checked, rather than assumed, when this test was written.
//
// Both have to be inside a Field-headed table to arise at all: renderTable's header
// gate means config.md's presets ("Key"), retired ("Key") and rename ("Old name")
// tables cannot claim an anchor however early they appear. So the live hazard is a
// rename or addition *within* the field reference itself.
func TestTwoKeysCollidingOnOneAnchorIsReported(t *testing.T) {
	md := "## Fields\n\n| Field | Default |\n|---|---|\n" +
		"| `vpn.armatboot` | old |\n| `vpn.armAtBoot` | new |\n"
	r := Render("probe.md", md)

	if len(r.Unsupported) == 0 {
		t.Fatal("a slug collision between two different keys was not reported")
	}
	if !strings.Contains(strings.Join(r.Unsupported, " "), "two different keys") {
		t.Errorf("unexpected report: %v", r.Unsupported)
	}
}

// The same key in two tables stays silent: config.md does this legitimately, and
// failing the build on it would be absurd.
func TestTheSameKeyTwiceIsNotReported(t *testing.T) {
	md := "## Fields\n\n| Field | Default |\n|---|---|\n| `pollInterval` | `5s` |\n\n" +
		"## Presets\n\n| Field | strict |\n|---|---|\n| `pollInterval` | `1s` |\n"
	r := Render("probe.md", md)

	if len(r.Unsupported) != 0 {
		t.Errorf("a legitimately repeated key was reported: %v", r.Unsupported)
	}
	if strings.Count(r.HTML, `id="key-pollinterval"`) != 1 {
		t.Errorf("expected exactly one anchored row, got %d",
			strings.Count(r.HTML, `id="key-pollinterval"`))
	}
}
