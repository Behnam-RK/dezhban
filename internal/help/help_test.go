package help

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
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
//
// Both anchors a Tunable carries are checked, because they are load-bearing for
// different readers. DocAnchor is a *heading* anchor and must resolve everywhere
// markdown does, since a CLI prints it for someone reading the file on GitHub.
// DocKeyAnchor names the key's own row (config.docKeyAnchorFor) and exists only in
// the rendered HTML; it is what gives the app's contextual help its per-key grain,
// and a key that loses its documentation row fails here by name rather than
// degrading into a link to the top of a long reference.
func TestEveryTunableDocAnchorResolves(t *testing.T) {
	index := buildInto(t)

	anchors := map[string]map[string]bool{}
	for _, e := range index {
		set := map[string]bool{}
		for _, h := range e.Headings {
			set[h.Anchor] = true
		}
		for _, k := range e.Keys {
			set[k.Anchor] = true
		}
		anchors[e.Source] = set
	}

	headings := map[string]map[string]bool{}
	for _, e := range index {
		set := map[string]bool{}
		for _, h := range e.Headings {
			set[h.Anchor] = true
		}
		headings[e.Source] = set
	}

	check := func(t *testing.T, key, field, value string, in map[string]map[string]bool) {
		t.Helper()
		page, frag, found := strings.Cut(value, "#")
		if !found {
			t.Errorf("%s: %s %q has no fragment", key, field, value)
			return
		}
		set, ok := in[page]
		if !ok {
			t.Errorf("%s: %s names %q, which is not a bundled page", key, field, page)
			return
		}
		if !set[frag] {
			t.Errorf("%s: nothing in %s has the anchor %q (%s)", key, page, frag, field)
		}
	}

	for _, tun := range config.Tunables() {
		// A heading, specifically: this is the one a CLI prints for a reader who
		// will open the file on GitHub, where row ids do not exist.
		check(t, tun.Key, "DocAnchor", tun.DocAnchor, headings)
		if tun.DocKeyAnchor != "" {
			check(t, tun.Key, "DocKeyAnchor", tun.DocKeyAnchor, anchors)
		}
	}
}

// TestKeyAnchorSlugMatchesTheRenderer pins config.anchorSlug (which cannot
// import this package — internal/help imports internal/config to check its own
// work) to help.KeyAnchor. The two derive the same fragment id from opposite
// ends of the same link, and a divergence would break every contextual help
// link at once while both packages' own tests still passed.
func TestKeyAnchorSlugMatchesTheRenderer(t *testing.T) {
	index := buildInto(t)

	rendered := map[string]bool{}
	for _, e := range index {
		for _, k := range e.Keys {
			rendered[e.Source+"#"+k.Anchor] = true
		}
	}
	for _, tun := range config.Tunables() {
		if !rendered[tun.DocAnchor] {
			continue // documented in prose; covered by the resolution test above
		}
		page, frag, _ := strings.Cut(tun.DocAnchor, "#")
		if want := KeyAnchor(tun.Key); frag != want {
			t.Errorf("%s: schema derived %q, renderer derives %q (page %s)",
				tun.Key, frag, want, page)
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
	r := Render("usage/config.md", md)

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
	r := Render("", "```sh\n| a | b |\n|---|---|\n**not bold**\n```\n")
	if strings.Contains(r.HTML, "<table>") {
		t.Error("a table inside a code fence was rendered as a table")
	}
	if strings.Contains(r.HTML, "<strong>") {
		t.Error("emphasis inside a code fence was interpreted")
	}
}

// TestEveryRealPageRendersCleanly is the test the synthetic subset sample above
// cannot be: it runs the renderer over the pages that actually ship.
//
// Both checks failed before they existed. usage/getting-started.md — the first
// page of the guided track — opened with a raw HTML banner that rendered as
// three paragraphs of visible tag source, and usage/cli.md left literal ** in
// the middle of a sentence. Render reported neither, so the build passed and the
// bundle shipped wrong. Asserting on the rendered HTML, not just on Unsupported,
// is what makes this independent of the reporting it is meant to police.
func TestEveryRealPageRendersCleanly(t *testing.T) {
	for _, page := range Pages {
		data, err := os.ReadFile(filepath.Join(docsDir, filepath.FromSlash(page.Source)))
		if err != nil {
			t.Fatalf("%s: %v", page.Source, err)
		}
		r := Render(page.Source, string(data))
		if len(r.Unsupported) != 0 {
			t.Errorf("%s: renderer reported %v", page.Source, r.Unsupported)
		}
		// A paragraph or list item opening with an escaped tag is markup that
		// leaked through as text. Inline code legitimately contains &lt;, hence
		// anchoring on the element boundary rather than searching for &lt;.
		for _, leak := range []string{"<p>&lt;", "<li>&lt;"} {
			if strings.Contains(r.HTML, leak) {
				t.Errorf("%s: raw HTML rendered as visible text (%q)", page.Source, leak)
			}
		}
		// Fenced code is verbatim and may legitimately contain **, so only the
		// prose outside it is checked.
		if strings.Contains(stripFences(r.HTML), "**") {
			t.Errorf("%s: literal ** survived into the rendered page", page.Source)
		}
	}
}

// stripFences removes <pre> blocks, whose contents are verbatim by design.
func stripFences(html string) string {
	var b strings.Builder
	for rest := html; ; {
		start := strings.Index(rest, "<pre>")
		if start < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:start])
		end := strings.Index(rest[start:], "</pre>")
		if end < 0 {
			return b.String()
		}
		rest = rest[start+end+len("</pre>"):]
	}
}

// TestEveryLinkGoesSomewhere — no bundled page may contain a link that resolves
// to nothing.
//
// Thirty did. The docs cross-reference the ADRs and the contributor docs
// constantly, and rewriteLink used to return an unmatched relative link
// unchanged, so "../adr/0008-arm-at-boot.md" shipped verbatim and resolved
// beside the bundle, where no such file exists. Clicking one produced "That link
// points outside the app: file:///…/Contents/Resources/adr/0008-arm-at-boot.md"
// — an internal path, on a Copy button that copied it. Every page had them, and
// the only existing link assertion covered the case that DID resolve.
//
// The rule is checked against the built bundle rather than the renderer, because
// "resolves to nothing" is a fact about the directory, not about the markdown.
func TestEveryLinkGoesSomewhere(t *testing.T) {
	dir := t.TempDir()
	if _, err := Build(docsDir, dir); err != nil {
		t.Fatalf("Build: %v", err)
	}
	href := regexp.MustCompile(`href="([^"]*)"`)
	for _, page := range Pages {
		name := OutputName(page.Source)
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range href.FindAllStringSubmatch(string(data), -1) {
			link := m[1]
			switch {
			case strings.HasPrefix(link, "#"), link == "help.css":
				continue
			case strings.HasPrefix(link, "https://"), strings.HasPrefix(link, "http://"),
				strings.HasPrefix(link, "mailto:"):
				// Off-bundle by design: the pane reports it and offers the URL.
				continue
			}
			file, _, _ := strings.Cut(link, "#")
			if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
				t.Errorf("%s links to %q, which is not in the bundle — it would resolve "+
					"to a file that does not exist", page.Source, link)
			}
		}
	}
}

// TestRenderReportsWhatItCannotShow pins the reporting itself. Each of these
// rendered silently — and wrongly — before, which is the failure mode the whole
// bundled-docs design depends on not having.
func TestRenderReportsWhatItCannotShow(t *testing.T) {
	cases := map[string]string{
		"raw HTML block":      "<p align=\"center\">\n  <img src=\"x.png\">\n</p>\n",
		"nested list item":    "- parent\n  - child\n",
		"unpaired bold":       "A sentence with **one marker only.\n",
		"javascript: link":    "A [trap](javascript:alert(1)) link.\n",
		"unclosed code fence": "```sh\necho hi\n",
	}
	for name, md := range cases {
		if r := Render("usage/config.md", md); len(r.Unsupported) == 0 {
			t.Errorf("%s was rendered without being reported:\n%s", name, r.HTML)
		}
	}
	// A refused scheme must also not reach the href, so the bundle cannot carry
	// the link even if someone ships past the build failure.
	if r := Render("usage/config.md", "[trap](javascript:alert(1))\n"); strings.Contains(r.HTML, "javascript:") {
		t.Errorf("a javascript: href survived into the rendered page:\n%s", r.HTML)
	}
}

// TestRelativeLinkCannotClimbAboveTheRepoRoot pins rewriteLink's bound on ".."
// segments: enough of them turn "docs/<dir>/<target>" into a path that climbs
// past the repo root, which repoBase would otherwise turn into a URL pointing
// outside the project (or nowhere). Refused the same way a disallowed scheme
// is, rather than shipped.
func TestRelativeLinkCannotClimbAboveTheRepoRoot(t *testing.T) {
	md := "[trap](../../../../../../../etc/passwd)\n"
	r := Render("usage/config.md", md)
	if len(r.Unsupported) == 0 {
		t.Fatalf("a link climbing above the repo root was rendered without being reported:\n%s", r.HTML)
	}
	if strings.Contains(r.HTML, "etc/passwd") {
		t.Errorf("the climbing path survived into the rendered href:\n%s", r.HTML)
	}
}

// TestInlineMarkupSpansSoftLineBreaks — these docs are hard-wrapped prose, so
// emphasis routinely straddles a line break. Rendering line by line left the **
// as literal text in eight of the nine bundled pages.
func TestInlineMarkupSpansSoftLineBreaks(t *testing.T) {
	r := Render("", "The guard **fail-closes\nand stays closed** afterwards.\n")
	if !strings.Contains(r.HTML, "<strong>fail-closes and stays closed</strong>") {
		t.Errorf("bold did not survive a soft line break:\n%s", r.HTML)
	}
	if len(r.Unsupported) != 0 {
		t.Errorf("a soft-wrapped bold span was reported as unsupported: %v", r.Unsupported)
	}
}

// A list item's continuation belongs INSIDE its <li>. Emitting it afterwards put
// loose text directly inside the <ul>, which is invalid.
func TestListItemContinuationStaysInsideTheItem(t *testing.T) {
	r := Render("", "- first line\n  second line\n- next item\n")
	if !strings.Contains(r.HTML, "<li>first line second line</li>") {
		t.Errorf("continuation leaked out of its list item:\n%s", r.HTML)
	}
}

// Emphasis must not reach inside a code span. `vpn.advanced.*` used to pair its
// glob with a later asterisk and open an <em> that closed outside the </code>.
func TestCodeSpansAreNotTouchedByEmphasis(t *testing.T) {
	r := Render("", "Every `vpn.advanced.*` key, **\"Use Touch ID\"** and more.\n")
	if !strings.Contains(r.HTML, "<code>vpn.advanced.*</code>") {
		t.Errorf("a code span was rewritten by the emphasis passes:\n%s", r.HTML)
	}
	if strings.Contains(r.HTML, "<em>") {
		t.Errorf("an asterisk inside a code span opened emphasis:\n%s", r.HTML)
	}
}

// Bold containing italics is written throughout the docs.
func TestItalicsInsideBold(t *testing.T) {
	r := Render("", "**Endpoints are deliberately *not* required.**\n")
	if !strings.Contains(r.HTML, "<strong>") || !strings.Contains(r.HTML, "<em>not</em>") {
		t.Errorf("nested emphasis was not rendered:\n%s", r.HTML)
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
