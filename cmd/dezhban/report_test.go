package main

import (
	"strings"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/redact"
)

// A note names why a file was left out, and the reason can quote the value that
// broke — a malformed endpoint in the config is exactly that shape. Notes ship
// inside the bundle's README, so they must go through the redactor like every
// other body: a redactor that misses a field advertises a safety it did not
// deliver.
func TestBundleNotesAreRedacted(t *testing.T) {
	r := redact.New(true)
	notes := []string{
		`config.json: not included — parse: invalid endpoint "vpn.acme-provider.net:1194"`,
		"state.json: not included — bad exit ip 203.0.113.9",
	}
	out := reportReadme(time.Now(), r, notes)

	for _, leaked := range []string{"vpn.acme-provider.net", "203.0.113.9"} {
		if strings.Contains(out, leaked) {
			t.Errorf("README leaked %q:\n%s", leaked, out)
		}
	}
	if !strings.Contains(out, "host-1") || !strings.Contains(out, "ip-1") {
		t.Errorf("notes were not replaced with placeholders:\n%s", out)
	}
	// The legend counts what the whole bundle replaced, so a placeholder minted
	// by a note has to be in it — otherwise the README understates itself.
	if !strings.Contains(out, "1 distinct IP address") || !strings.Contains(out, "1 distinct hostname") {
		t.Errorf("legend does not count the placeholders the notes minted:\n%s", out)
	}
}

// The unredacted bundle is the explicit opt-out and must stay byte-identical
// through the same code path — there is no second route for it to drift down.
func TestBundleNotesAreVerbatimWithoutRedaction(t *testing.T) {
	r := redact.New(false)
	out := reportReadme(time.Now(), r, []string{"state.json: not included — bad exit ip 203.0.113.9"})
	if !strings.Contains(out, "203.0.113.9") {
		t.Errorf("--include-network must keep the note verbatim:\n%s", out)
	}
}
