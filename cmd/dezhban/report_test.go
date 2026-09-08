package main

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"runtime"
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

// A host with no config file is running on built-in defaults, which is a fact
// about the host, not a failure to read.
//
// resolveConfigPath returns "" there, and passing that straight to os.ReadFile
// put `config.json: not included — open : no such file or directory` in the
// bundle — an empty path in an error message, on exactly the host most likely
// to be collecting a bundle in the first place.
func TestTheConfigNoteSaysDefaultsRatherThanAnEmptyPath(t *testing.T) {
	_, err := reportConfig("")
	if err == nil {
		t.Fatal("a missing config must still be noted")
	}
	if strings.Contains(err.Error(), "open :") {
		t.Errorf("the note still quotes an empty path: %v", err)
	}
	if !strings.Contains(err.Error(), "built-in defaults") {
		t.Errorf("the note does not say what the host is actually running: %v", err)
	}
}

// The bundle itself, end to end. Nothing exercised `cmdReport` — only the two
// helpers — so the zip's entry set, its mode, and the one property the whole
// feature exists for (a public address in the config does not appear anywhere
// inside) were all unverified.
// Not hermetic, and deliberately so: cmdReport reads this HOST's state, learned
// and log paths, which have no test seam, and doctor resolves the fixture's
// endpoint. Those inputs only ever ADD entries or notes, so the assertions here
// — the entry set, the mode, and that no fixture identifier appears anywhere —
// hold on a bare host and a fully configured one alike. Adding a state-dir seam
// to production code purely for this test would be the larger change.
func TestTheBundleIsRedactedAndPrivateEndToEnd(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	const endpoint = "203.0.113.77"
	const profile = "acme-frankfurt"
	// A single-label endpoint: valid by config's grammar, invisible to a
	// hostname SHAPE, and so the case only the field-aware pass reaches.
	const bareHost = "acmevpn"
	cfg := `{
  "pollInterval": "5s",
  "blockedCountries": [],
  "hysteresis": 1,
  "providers": ["https://ipinfo.io/json"],
  "vpn": {
    "tunnelInterfaces": ["utun9"],
    "endpoints": ["` + endpoint + `", "` + bareHost + `"],
    "autoDetect": false,
    "profiles": [{"name": "` + profile + `", "endpoints": ["` + endpoint + `"]}]
  },
  "providerQuorum": false,
  "logLevel": "error"
}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if code := cmdReport([]string{"--config", cfgPath, "--out", out}); code != 0 {
		t.Fatalf("cmdReport exited %d", code)
	}

	zips, err := filepath.Glob(filepath.Join(out, "dezhban-report-*.zip"))
	if err != nil || len(zips) != 1 {
		t.Fatalf("glob = %v (%v), want exactly one bundle", zips, err)
	}
	// 0600: with --include-network this file holds the real server addresses,
	// and it lands in a directory the user picked — often a shared one.
	info, err := os.Stat(zips[0])
	if err != nil {
		t.Fatal(err)
	}
	// Windows reports 0666 for a file created 0600; internal/learned's
	// equivalent assertion carries the same guard.
	if perm := info.Mode().Perm(); perm != 0o600 && runtime.GOOS != "windows" {
		t.Errorf("bundle mode = %v, want 0600", perm)
	}

	zr, err := zip.OpenReader(zips[0])
	if err != nil {
		t.Fatalf("the bundle does not open as a zip: %v", err)
	}
	defer zr.Close()

	seen := map[string]bool{}
	for _, f := range zr.File {
		seen[f.Name] = true
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		for _, leak := range []string{endpoint, profile, bareHost} {
			if strings.Contains(string(body), leak) {
				t.Errorf("%s leaked %q", f.Name, leak)
			}
		}
	}
	// README last, so it can name what was missing; config and the two rendered
	// entries always collect, because they need no daemon to have run.
	for _, want := range []string{"README.txt", "config.json", "doctor.json", "rules-preview.txt"} {
		if !seen[want] {
			t.Errorf("the bundle has no %s (entries: %v)", want, seen)
		}
	}
}
