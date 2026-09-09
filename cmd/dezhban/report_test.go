package main

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
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
	bodies := map[string]string{}
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
		bodies[f.Name] = string(body)
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

	// Every JSON entry still OPENS. A redactor that rewrites text it does not
	// understand can break the escaping of the file it is rewriting, and a
	// doctor.json no reader can open is a diagnosis nobody gets — which looks
	// exactly like a working bundle until someone tries to use it. This shipped
	// once; docs/contribute/testing.md asks a human for it, and a human is not
	// a regression test.
	for name, body := range bodies {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		if !json.Valid([]byte(body)) {
			t.Errorf("%s is not valid JSON:\n%s", name, body)
		}
	}

	// dezhban's OWN vocabulary survives. Over-redaction fails as badly as
	// under-redaction: a bundle that hides the diagnosis has thrown away the
	// answer and hidden no identity.
	var report struct {
		Checks []struct {
			Name  string   `json:"name"`
			Fixes []string `json:"fixes"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(bodies["doctor.json"]), &report); err != nil {
		t.Fatalf("doctor.json: %v", err)
	}
	for _, c := range report.Checks {
		if strings.HasPrefix(c.Name, "profile-") || strings.HasPrefix(c.Name, "host-") {
			t.Errorf("a doctor check name was redacted: %q", c.Name)
		}
	}
	if !strings.Contains(bodies["config.json"], "utun9") {
		t.Error("the tunnel interface name was redacted out of config.json")
	}
	if !strings.Contains(bodies["rules-preview.txt"], "utun9") {
		t.Error("the tunnel interface name was redacted out of rules-preview.txt")
	}

	// The legend counts what is actually in the bundle. A pass whose output is
	// discarded still mints, and the README then advertises tokens that appear
	// nowhere — which is the README overstating what it hid.
	// The legend prints a RANGE ("4 distinct hostnames -> host-1 ... host-4"),
	// so every ordinal has to come from the count, not from what the README
	// happens to spell out — checking only the tokens named there tests host-1
	// and nothing else.
	all := strings.Join(collect(bodies), "\n")
	// Every token the legend NAMES has to be in the bundle. It renders the
	// ordinals actually minted, and an ordinal is skipped when it would have
	// produced the value it replaces — so the first one is not always `-1`, and
	// an assertion that assumed it was tested only the first token of each kind.
	legend := regexp.MustCompile(`(\d+) distinct [a-z ]+ → ([a-z]+-\d+)(?: … ([a-z]+-\d+))?`)
	rows := legend.FindAllStringSubmatch(bodies["README.txt"], -1)
	if len(rows) == 0 && strings.Contains(bodies["README.txt"], "What was replaced") {
		t.Error("the legend rendered rows this test cannot parse")
	}
	for _, m := range rows {
		count, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("legend count %q: %v", m[1], err)
		}
		for _, token := range []string{m[2], m[3]} {
			if token != "" && !strings.Contains(all, token) {
				t.Errorf("the legend names %s, which appears nowhere in the bundle", token)
			}
		}
		if count == 1 && m[3] != "" {
			t.Errorf("a count of one rendered as a range: %q", m[0])
		}
	}
}

// collect returns every entry body except the README, which is where the legend
// itself lives.
func collect(bodies map[string]string) []string {
	var out []string
	for name, body := range bodies {
		if name != "README.txt" {
			out = append(out, body)
		}
	}
	return out
}

// The collection order is load-bearing and nothing pinned it.
//
// config.json and learned.json carry the profile names; doctor.json and
// state.json write those names into PROSE, where the redactor reaches them only
// by remembering what it has already replaced. Collect a prose file first and
// its names ship verbatim — with every test still green, because the leak is in
// a file the fixture does not have to contain.
func TestTheBundleCollectsNameSourcesFirst(t *testing.T) {
	order := bundleEntryOrder()
	pos := map[string]int{}
	for i, name := range order {
		pos[name] = i
	}
	for _, src := range []string{"config.json", "learned.json"} {
		for _, prose := range []string{"state.json", "doctor.json", "rules-preview.txt", "log.txt"} {
			if pos[src] > pos[prose] {
				t.Errorf("%s is collected after %s; a name it alone knows would ship verbatim in %s (order: %v)",
					src, prose, prose, order)
			}
		}
	}
}

// bundleEntryOrder is the entry names in the order cmdReport collects them.
func bundleEntryOrder() []string {
	empty := ""
	var names []string
	for _, e := range bundleEntries(&empty) {
		names = append(names, e.entry)
	}
	return names
}

// One pass per entry. This call site was an assignment followed by a
// conditional overwrite, and the overwritten pass still MINTED — Text reads
// `vpn.endpoints` in a doctor fix as a hostname, which the walk deliberately
// keeps, so the README's legend counted a token appearing nowhere in the
// bundle. A Redactor remembers; a pass whose output you throw away is not free.
func TestRedactEntryUsesExactlyOnePass(t *testing.T) {
	body := `{"checks":[{"name":"endpoints","fixes":["sudo dezhban config set vpn.endpoints=x"]}]}`

	got := redact.New(true)
	redactEntry(got, body, true)

	want := redact.New(true)
	want.JSON(body)

	if !slices.Equal(got.Legend(), want.Legend()) {
		t.Errorf("redactEntry minted more than the walk alone:\n  got:  %v\n  want: %v",
			got.Legend(), want.Legend())
	}
}

// The bundle is created EXCLUSIVELY, never opened over something already there.
//
// The mode argument applies at creation, so opening an existing path kept
// whatever permissions it had; and the old O_TRUNC followed a symlink, so a link
// planted in the output directory — one the user picks, often shared or synced —
// sent an --include-network bundle wherever it pointed.
func TestTheBundleIsNeverWrittenOverSomethingThatExists(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere.zip")
	link := filepath.Join(dir, "bundle.zip")
	if err := os.WriteFile(target, []byte("victim"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	f, got, err := createBundle(link)
	if err != nil {
		t.Fatalf("createBundle: %v", err)
	}
	defer f.Close()
	if got == link {
		t.Errorf("createBundle wrote through the symlink at %s", link)
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != "victim" {
		t.Errorf("the symlink target was overwritten: %q, %v", body, err)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 && runtime.GOOS != "windows" {
		t.Errorf("bundle mode = %v, want 0600", perm)
	}
}
