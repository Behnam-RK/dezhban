package help

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/behnam-rk/dezhban/internal/config"
)

const docsDir = "../../docs"

// TestEveryBundledPageExists is the load-bearing-doc rule from CLAUDE.md,
// enforced. A page listed here ships inside the app, so moving or renaming a doc
// without updating the manifest would quietly drop it from the bundle — and the
// page most likely to be missed is the one a locked-out user opens.
//
// It runs in `go test ./...` rather than only in CI, so the failure lands on the
// machine that made the change.
func TestEveryBundledPageExists(t *testing.T) {
	for _, page := range Pages {
		path := filepath.Join(docsDir, filepath.FromSlash(page.Source))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("bundled page %q does not exist: %v", page.Source, err)
		}
		if page.Title == "" || page.Summary == "" {
			t.Errorf("%s: a bundled page needs a title and a one-line summary", page.Source)
		}
	}
}

// TestBundleBuilds renders the real docs, which is the only way to know the
// subset renderer copes with what is actually written.
func TestBundleBuilds(t *testing.T) {
	index := buildInto(t)

	if len(index) != len(Pages) {
		t.Fatalf("index has %d entries, want %d", len(index), len(Pages))
	}
	for _, e := range index {
		if len(e.Headings) == 0 {
			t.Errorf("%s rendered with no headings — it probably did not render at all", e.Source)
		}
		if len(e.Text) < 200 {
			t.Errorf("%s produced %d characters of searchable text, which is implausibly little",
				e.Source, len(e.Text))
		}
	}
}

// TestEveryTunableDocAnchorResolves ties the settings schema to the bundle. A
// contextual help link is a promise that the section exists; a stale anchor
// silently lands the reader at the top of a long reference page instead.
func TestEveryTunableDocAnchorResolves(t *testing.T) {
	index := buildInto(t)

	anchors := map[string]map[string]bool{}
	for _, e := range index {
		set := map[string]bool{}
		for _, h := range e.Headings {
			set[h.Anchor] = true
		}
		anchors[e.Source] = set
	}

	for _, tun := range config.Tunables() {
		page, frag, found := strings.Cut(tun.DocAnchor, "#")
		if !found {
			t.Errorf("%s: DocAnchor %q has no fragment", tun.Key, tun.DocAnchor)
			continue
		}
		set, ok := anchors[page]
		if !ok {
			t.Errorf("%s: DocAnchor names %q, which is not a bundled page", tun.Key, page)
			continue
		}
		if !set[frag] {
			t.Errorf("%s: no heading in %s has the anchor %q", tun.Key, page, frag)
		}
	}
}

// TestBundleIsSelfContained — the pane opens when the kill switch has cut every
// byte of egress, so a page that reaches for a CDN would render broken at
// exactly the moment it is needed.
func TestBundleIsSelfContained(t *testing.T) {
	dir := t.TempDir()
	if _, err := Build(docsDir, dir); err != nil {
		t.Fatalf("Build: %v", err)
	}
	entries, err := filepath.Glob(filepath.Join(dir, "*.html"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no rendered pages: %v", err)
	}
	for _, path := range entries {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		html := string(data)
		for _, forbidden := range []string{"<script", "src=\"http", "href=\"http://cdn", "@import"} {
			if strings.Contains(html, forbidden) {
				t.Errorf("%s contains %q — the bundle must load nothing from the network",
					filepath.Base(path), forbidden)
			}
		}
	}
}

// TestBuildFailsOnAMissingPage — a bundle quietly missing a page is worse than a
// build that stops, so the failure has to be an error rather than a skip.
func TestBuildFailsOnAMissingPage(t *testing.T) {
	empty := t.TempDir()
	if _, err := Build(empty, t.TempDir()); err == nil {
		t.Fatal("Build succeeded against a docs directory with no pages in it")
	}
}

func buildInto(t *testing.T) []IndexEntry {
	t.Helper()
	index, err := Build(docsDir, t.TempDir())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return index
}

// --- renderer ---

func TestRenderCoversTheSubset(t *testing.T) {
	md := "# Title\n\n" +
		"A paragraph with `code`, **bold**, and a [link](config.md#fields).\n\n" +
		"- one\n- two\n\n" +
		"1. first\n2. second\n\n" +
		"> a quote\n\n" +
		"| Field | Default |\n|---|---|\n| `a` | `1s` |\n\n" +
		"```sh\necho hi\n```\n\n" +
		"---\n"
	r := Render(md)

	if len(r.Unsupported) != 0 {
		t.Fatalf("the subset renderer reported %v on its own subset", r.Unsupported)
	}
	for _, want := range []string{
		"<h1 id=\"title\">Title</h1>", "<code>code</code>", "<strong>bold</strong>",
		"<ul>", "<ol>", "<blockquote>", "<table>", "<th>Field</th>", "<td><code>1s</code></td>",
		"<pre><code class=\"language-sh\">", "<hr>",
	} {
		if !strings.Contains(r.HTML, want) {
			t.Errorf("rendered HTML is missing %q", want)
		}
	}
	// A link to a bundled page is rewritten to the bundled copy; the fragment
	// survives, since that is what a contextual deep link depends on.
	if !strings.Contains(r.HTML, `href="usage-config.html#fields"`) {
		t.Errorf("cross-document link was not rewritten to the bundled page:\n%s", r.HTML)
	}
}

// Content inside a fence is verbatim: a shell example full of pipes and dashes
// must not be read as a table, and markup in it must not be interpreted.
func TestFencedCodeIsVerbatim(t *testing.T) {
	r := Render("```sh\n| a | b |\n|---|---|\n**not bold**\n```\n")
	if strings.Contains(r.HTML, "<table>") {
		t.Error("a table inside a code fence was rendered as a table")
	}
	if strings.Contains(r.HTML, "<strong>") {
		t.Error("emphasis inside a code fence was interpreted")
	}
}

// The anchors have to match the ones written in the docs (and in DocAnchor), so
// this pins the derivation rather than trusting it.
func TestAnchorMatchesTheDocsConvention(t *testing.T) {
	cases := map[string]string{
		"Fields":          "fields",
		"`control` block": "control-block",
		"`vpn` block":     "vpn-block",
		// Punctuation is dropped and the surviving words are joined by hyphens,
		// so the two words either side of the parenthesis stay separated.
		"Advanced tunables (`vpn.advanced`)": "advanced-tunables-vpnadvanced",
		"Words we do not use":                "words-we-do-not-use",
	}
	for title, want := range cases {
		if got := Anchor(title); got != want {
			t.Errorf("Anchor(%q) = %q, want %q", title, got, want)
		}
	}
}

// A page name is flattened into one directory, so the bundle stays flat and a
// link can be a bare filename.
func TestOutputName(t *testing.T) {
	cases := map[string]string{
		"usage/config.md":          "usage-config.html",
		"concepts/glossary.md":     "concepts-glossary.html",
		"usage/getting-started.md": "usage-getting-started.html",
	}
	for src, want := range cases {
		if got := OutputName(src); got != want {
			t.Errorf("OutputName(%q) = %q, want %q", src, got, want)
		}
	}
}

// The index is what the app decodes, so it must be valid JSON with the fields
// the sidebar and search need.
func TestIndexDecodes(t *testing.T) {
	dir := t.TempDir()
	if _, err := Build(docsDir, dir); err != nil {
		t.Fatalf("Build: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "help-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []IndexEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("help-index.json does not decode: %v", err)
	}
	tutorial := 0
	for _, e := range entries {
		if e.File == "" || e.Title == "" {
			t.Errorf("%s: incomplete index entry", e.Source)
		}
		if e.Tutorial > 0 {
			tutorial++
		}
	}
	if tutorial == 0 {
		t.Error("no page is in the tutorial track, so a first-time reader has no starting point")
	}
}
