package help

import (
	"fmt"
	"html"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// A deliberately small CommonMark subset: exactly the constructs these docs use
// — headings, paragraphs, fenced code, bullet and numbered lists, tables, block
// quotes, horizontal rules, and inline links/code/emphasis.
//
// Written here rather than pulled in because the deliverable is a
// dependency-light standalone binary (see CLAUDE.md's dependency rule), and a
// markdown library would be a fifth third-party module for a build-time step.
// The subset is enforced rather than assumed: Render reports anything it does
// not understand, and a test fails the build when a bundled page uses it. That
// keeps the renderer honest AND keeps the docs inside a style the renderer can
// actually show — a silently mangled page would be worse than a build failure.
//
// "Reports anything it does not understand" is the load-bearing half, and it has
// to be maintained deliberately: a construct this renderer degrades silently is
// a page that ships wrong while every test passes. Whenever a case is added
// here that cannot be represented, note() it — do not let it fall through to
// the paragraph branch and render as literal text.

// Heading is one heading in a rendered page, for search and anchor resolution.
type Heading struct {
	Level  int    `json:"level"`
	Text   string `json:"text"`
	Anchor string `json:"anchor"`
}

// Rendered is one page's HTML plus what a browser needs to index it.
type Rendered struct {
	HTML     string
	Headings []Heading
	// Keys are the per-row anchors emitted for reference tables: one entry per
	// table row whose first cell is a lone code span, which is how every field
	// table in the docs is written. They are what lets a contextual help link
	// land on `vpn.redialWindow` itself rather than on the section heading four
	// dozen keys share. Kept separate from Headings because they are not
	// headings: they never appear in the sidebar outline.
	Keys []Heading
	// Text is the page with markup stripped, for substring search.
	Text string
	// Unsupported names constructs the renderer met and could not represent.
	// Non-empty means the page would display wrongly.
	Unsupported []string
}

var (
	inlineCode = regexp.MustCompile("`([^`]+)`")
	// A table cell that is one code span and nothing else — how every field
	// table in the docs names the key a row documents. See rowKey.
	loneCode    = regexp.MustCompile("^`([^`]+)`$")
	inlineLink  = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)
	inlineImage = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
	// Non-greedy and NOT [^*]+, so a bold span may contain italics: the docs
	// write **… *not* …** and an inner-asterisk ban left the outer ** as
	// literal text. Bold is substituted first and the inner *not* survives into
	// the emphasis pass below, which is what turns it into <em>.
	inlineBold   = regexp.MustCompile(`\*\*(.+?)\*\*`)
	inlineItalic = regexp.MustCompile(`(^|[^*])\*([^*]+)\*`)
	headingRe    = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	bulletRe     = regexp.MustCompile(`^\s*[-*]\s+(.*)$`)
	numberedRe   = regexp.MustCompile(`^\s*\d+\.\s+(.*)$`)
	anchorStrip  = regexp.MustCompile(`[^a-z0-9 -]`)
	// htmlBlockRe matches a line that opens, closes, or comments raw HTML. The
	// renderer has no way to show it, and escaping it prints the tag's source
	// at the top of the page — which is exactly what shipped before this check
	// existed.
	htmlBlockRe = regexp.MustCompile(`^<(/?[a-zA-Z][a-zA-Z0-9-]*|!--)`)
	// schemeRe matches a URL scheme prefix, so a link can be checked against the
	// allowlist in rewriteLink.
	schemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)
)

// linkSchemes is what a documentation link is allowed to use. Anything else —
// javascript:, data:, file: — is refused at render time rather than relied on
// being cancelled by the app's navigation delegate: the bundle should not
// contain the link in the first place.
var linkSchemes = map[string]bool{"http": true, "https": true, "mailto": true}

// repoBase is where a document that is NOT bundled lives instead. The docs
// cross-reference the ADRs and the contributor docs constantly, and those
// deliberately do not ship (see Pages) — so their links have to become
// somewhere real. Left as written, they resolve to a file: path beside the
// bundle that does not exist, and the pane reports "points outside the app"
// with an internal path nobody can use.
//
// Pinned to main rather than the built tag on purpose: this is the only kind of
// link the pane cannot follow, so it is a URL a reader copies into a browser
// later, and a tag that has since been deleted or rewritten reads as a broken
// project. The bundled pages themselves are always version-matched, which is
// what the offline guarantee is actually about.
const repoBase = "https://github.com/behnam-rk/dezhban/blob/main/"

// Render converts one markdown document to HTML.
//
// source is the page's docs-relative path ("usage/cli.md"), which is what
// relative links in it are resolved against — "../adr/0001.md" means something
// different depending on the page it is written in. Empty source is for
// rendering a fragment with no place in the manifest (tests): relative links
// are then reported as unresolvable rather than guessed at.
func Render(source, markdown string) Rendered {
	var out strings.Builder
	var text strings.Builder
	r := Rendered{}

	// note collects what could not be represented. A set, because one page
	// repeating the same unsupported construct twenty times is still one thing
	// to fix, and the message should say what it is rather than how often.
	unsupported := map[string]bool{}
	note := func(what string) { unsupported[what] = true }

	// renderInline is the only way inline markup reaches the output, so every
	// construct it cannot represent is reported from one place.
	renderInline := func(s string) string {
		rendered := inline(source, s, note)
		if strings.Contains(rendered, "**") {
			note("unpaired ** (bold that never closed)")
		}
		return rendered
	}

	// claimKey hands out the anchor for a key row, the FIRST time that key is seen
	// on this page. A key documented in more than one table — several are, e.g.
	// `pollInterval` in both the field reference and the presets comparison — can
	// only have one id, since duplicates are invalid HTML and a browser jumps to
	// whichever came first; the choice is made here rather than left to chance.
	//
	// "First" is the whole rule, and it is not the same claim as "its definition".
	// It lands on the definition because docs/usage/config.md is ordered that way,
	// which is an assumption about the document, not something this function can
	// check — a duplicate is refused silently, because refusing loudly would fail
	// the build on every legitimately repeated key. Reorder the reference so the
	// Presets comparison precedes the field tables and every `?` for those keys
	// would quietly deep-link to the presets row instead, with the resolution test
	// still green because *some* row carries the anchor. TestKeyRowsAnchorToTheir
	// DefinitionSection pins the ordering this relies on.
	// anchor -> the key that claimed it, not merely "claimed". The distinction is
	// the whole point: refusing a *repeat* of the same key is benign and therefore
	// silent (config.md documents several keys in more than one table, and failing
	// the build on that would be absurd), but two *different* keys landing on one
	// anchor is a silently wrong deep link. `Anchor` lowercases and strips `.`, `[`
	// and `]`, so `vpn.pauseMax` and `vpn.pause-max` — or any rename that changes
	// only punctuation or case — slug identically. The "Renamed keys" table precedes
	// the definition tables and its first cell is the OLD name, so such a rename
	// would hand the anchor to the old-name row and send every `?` for the live key
	// there, with a green build: the resolution test only asks whether *some* row
	// carries the fragment. Reported the same way a heading collision is.
	claimedKeys := map[string]string{}
	// Heading anchors are collected up front, because a heading can appear *after*
	// the row that would collide with it while r.Headings is filled in the same
	// single pass below.
	//
	// The `key-` prefix was assumed to make collision impossible. It does not: a
	// heading of "Key flags" — docs/usage/cli.md has one — slugs to exactly
	// `key-flags`, which is also what a row for a key named `flags` would claim.
	// Two elements with one id is invalid HTML and a browser jumps to whichever came
	// first, so the reader lands on the heading and the contextual link is silently
	// wrong. The heading wins, being the anchor markdown itself generates and the one
	// written links depend on, and the row is *noted* rather than quietly skipped:
	// Unsupported fails the bundle build (bundle.go), so this becomes a build error
	// naming the page instead of a link that misses.
	// Through headingRe and outside code fences, so the pre-scan sees exactly what
	// the main pass will call a heading. Accepting "any trimmed line starting with
	// #" was looser: `#nospace`, seven hashes, and — worst — a shell comment inside a
	// fenced block all registered as phantom headings, and one that happened to slug
	// to a real row's anchor would refuse that row and fail the whole app build
	// naming a heading nobody can find.
	headingAnchors := map[string]bool{}
	scanInCode := false
	for _, line := range strings.Split(markdown, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			scanInCode = !scanInCode
			continue
		}
		if scanInCode {
			continue
		}
		// The TRIMMED line, exactly as the main pass matches it. Markdown allows up
		// to three leading spaces on an ATX heading, and this renderer honours that
		// — so matching the raw line here let "  ## Key flags" render as a heading
		// that the pre-scan never saw, and a colliding row was then claimed anyway.
		// Two elements with one id, the browser jumping to the heading, and a green
		// build: precisely the failure the guard was added to turn into an error.
		if m := headingRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			if title := strings.TrimSpace(m[2]); title != "" {
				headingAnchors[Anchor(title)] = true
			}
		}
	}
	claimKey := func(key string) (string, bool) {
		anchor := KeyAnchor(key)
		if owner, taken := claimedKeys[anchor]; taken {
			if owner != key {
				note("two different keys claim the anchor " + anchor +
					" (" + owner + " and " + key + ")")
			}
			return "", false
		}
		if headingAnchors[anchor] {
			note("a key row and a heading both claim the anchor " + anchor)
			return "", false
		}
		claimedKeys[anchor] = key
		r.Keys = append(r.Keys, Heading{Text: key, Anchor: anchor})
		return anchor, true
	}

	lines := strings.Split(markdown, "\n")
	inCode, inList, inQuote := false, false, false
	listTag := ""

	closeList := func() {
		if inList {
			out.WriteString("</" + listTag + ">\n")
			inList = false
		}
	}
	closeQuote := func() {
		if inQuote {
			out.WriteString("</blockquote>\n")
			inQuote = false
		}
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Fenced code: taken verbatim, so nothing inside is interpreted.
		if strings.HasPrefix(trimmed, "```") {
			if inCode {
				out.WriteString("</code></pre>\n")
				inCode = false
				continue
			}
			closeList()
			closeQuote()
			lang := strings.TrimPrefix(trimmed, "```")
			out.WriteString(`<pre><code class="language-` + html.EscapeString(lang) + `">`)
			inCode = true
			continue
		}
		if inCode {
			out.WriteString(html.EscapeString(line) + "\n")
			continue
		}

		if trimmed == "" {
			closeList()
			closeQuote()
			continue
		}

		// Raw HTML. Checked before everything else that could swallow the line,
		// and reported rather than rendered: there is no honest way to show it,
		// and the paragraph branch would print the tag source as visible text.
		if htmlBlockRe.MatchString(trimmed) {
			note("raw HTML (" + firstTag(trimmed) + ") — the renderer has no way to show it")
			continue
		}

		if m := headingRe.FindStringSubmatch(trimmed); m != nil {
			closeList()
			closeQuote()
			level := len(m[1])
			title := stripInline(m[2])
			anchor := Anchor(title)
			r.Headings = append(r.Headings, Heading{Level: level, Text: title, Anchor: anchor})
			fmt.Fprintf(&out, "<h%d id=%q>%s</h%d>\n", level, anchor, renderInline(m[2]), level)
			text.WriteString(title + "\n")
			continue
		}

		if trimmed == "---" || trimmed == "***" || trimmed == "___" {
			closeList()
			closeQuote()
			out.WriteString("<hr>\n")
			continue
		}

		// Tables: a header row followed by a separator row. Detected by looking
		// ahead, since a lone pipe-containing line is just a paragraph.
		if strings.HasPrefix(trimmed, "|") && i+1 < len(lines) && isTableSeparator(lines[i+1]) {
			closeList()
			closeQuote()
			consumed := renderTable(&out, &text, lines[i:], renderInline, claimKey)
			i += consumed - 1
			continue
		}

		if strings.HasPrefix(trimmed, ">") {
			closeList()
			if !inQuote {
				out.WriteString("<blockquote>\n")
				inQuote = true
			}
			body := quoteBody(trimmed)
			for i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), ">") {
				i++
				body += " " + quoteBody(strings.TrimSpace(lines[i]))
			}
			out.WriteString("<p>" + renderInline(body) + "</p>\n")
			text.WriteString(stripInline(body) + "\n")
			continue
		}

		if m := bulletRe.FindStringSubmatch(line); m != nil {
			noteIfNested(line, note)
			if inList && listTag != "ul" {
				closeList()
			}
			if !inList {
				out.WriteString("<ul>\n")
				inList, listTag = true, "ul"
			}
			body, last := gatherItem(lines, i, m[1])
			i = last
			out.WriteString("<li>" + renderInline(body) + "</li>\n")
			text.WriteString(stripInline(body) + "\n")
			continue
		}
		if m := numberedRe.FindStringSubmatch(line); m != nil {
			noteIfNested(line, note)
			if inList && listTag != "ol" {
				closeList()
			}
			if !inList {
				out.WriteString("<ol>\n")
				inList, listTag = true, "ol"
			}
			body, last := gatherItem(lines, i, m[1])
			i = last
			out.WriteString("<li>" + renderInline(body) + "</li>\n")
			text.WriteString(stripInline(body) + "\n")
			continue
		}

		closeList()
		closeQuote()
		body := trimmed
		for i+1 < len(lines) && !startsNewBlock(lines, i+1) {
			i++
			body += " " + strings.TrimSpace(lines[i])
		}
		out.WriteString("<p>" + renderInline(body) + "</p>\n")
		text.WriteString(stripInline(body) + "\n")
	}

	closeList()
	closeQuote()
	if inCode {
		note("an unclosed code fence")
	}

	// Sorted, so a build failure names the same things in the same order twice
	// running and a diff of two failures is readable.
	r.Unsupported = make([]string, 0, len(unsupported))
	for what := range unsupported {
		r.Unsupported = append(r.Unsupported, what)
	}
	sort.Strings(r.Unsupported)

	r.HTML = out.String()
	r.Text = text.String()
	return r
}

// A block's soft-wrapped lines are joined before any inline markup is rendered,
// because markdown emphasis spans a line break and this renderer's input is
// hard-wrapped prose. Rendering line by line meant a bold span the author wrapped
// across two lines never paired, and the `**` showed up as literal text in the
// shipped bundle — in eight of the nine pages. Joining is also what puts a list
// item's continuation INSIDE its <li> rather than after it.

// startsNewBlock reports whether the line at i begins something that cannot be
// a continuation of the paragraph or list item being gathered.
func startsNewBlock(lines []string, i int) bool {
	line := lines[i]
	t := strings.TrimSpace(line)
	switch {
	case t == "":
	case strings.HasPrefix(t, "```"):
	case htmlBlockRe.MatchString(t):
	case headingRe.MatchString(t):
	case t == "---" || t == "***" || t == "___":
	case strings.HasPrefix(t, ">"):
	case bulletRe.MatchString(line) || numberedRe.MatchString(line):
	case strings.HasPrefix(t, "|") && i+1 < len(lines) && isTableSeparator(lines[i+1]):
	default:
		return false
	}
	return true
}

// gatherItem joins a list item with its indented continuation lines, reporting
// the index of the last line it consumed. Continuation must be indented: an
// unindented line after an item ends the list, the same boundary the renderer
// drew before blocks were gathered.
func gatherItem(lines []string, i int, first string) (string, int) {
	body := first
	for i+1 < len(lines) {
		next := lines[i+1]
		if startsNewBlock(lines, i+1) || strings.TrimLeft(next, " \t") == next {
			break
		}
		i++
		body += " " + strings.TrimSpace(next)
	}
	return body, i
}

// quoteBody strips one leading ">" from a block-quote line.
func quoteBody(trimmed string) string {
	return strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
}

// noteIfNested reports an indented list item. The renderer emits one flat list,
// so a nested item would silently become a sibling of its own parent — the
// hierarchy the author wrote would be gone, and the surrounding text would end
// up directly inside the <ul>.
func noteIfNested(line string, note func(string)) {
	if len(line)-len(strings.TrimLeft(line, " \t")) >= 2 {
		note("a nested list item — this renderer emits one flat list")
	}
}

// firstTag names the tag in a raw-HTML line, so the build failure says which
// one to remove rather than only that some HTML exists.
func firstTag(line string) string {
	if strings.HasPrefix(line, "<!--") {
		return "<!-- -->"
	}
	end := strings.IndexAny(line, " \t>")
	if end < 0 {
		end = len(line)
	}
	return line[:end] + ">"
}

// isTableSeparator matches the |---|---| row that turns the line above it into
// a table header.
func isTableSeparator(line string) bool {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "|") {
		return false
	}
	for _, cell := range splitRow(s) {
		if cell == "" || strings.Trim(cell, "-: ") != "" {
			return false
		}
	}
	return true
}

// renderTable emits one table and reports how many lines it consumed.
//
// A row whose first cell is a lone code span — `vpn.redialWindow`, which is how
// every field table in the docs opens a row — gets an `id` on its <tr> so a
// contextual help link can land on that key. Every other row is emitted exactly
// as before. claimKey decides the anchor and reports whether this row is the
// one that gets it — see Render, which resolves keys documented in two tables.
func renderTable(out *strings.Builder, text *strings.Builder, lines []string,
	renderInline func(string) string, claimKey func(string) (string, bool)) int {
	header := splitRow(lines[0])
	out.WriteString("<div class=\"table-scroll\"><table>\n<thead><tr>")
	for _, c := range header {
		out.WriteString("<th>" + renderInline(c) + "</th>")
		text.WriteString(stripInline(c) + " ")
	}
	out.WriteString("</tr></thead>\n<tbody>\n")
	text.WriteString("\n")

	used := 2 // header + separator
	for _, line := range lines[2:] {
		s := strings.TrimSpace(line)
		if !strings.HasPrefix(s, "|") {
			break
		}
		cells := splitRow(s)
		anchor := ""
		if key, ok := rowKey(cells); ok {
			anchor, _ = claimKey(key)
		}
		if anchor != "" {
			fmt.Fprintf(out, "<tr id=%q>", anchor)
		} else {
			out.WriteString("<tr>")
		}
		for _, c := range cells {
			out.WriteString("<td>" + renderInline(c) + "</td>")
			text.WriteString(stripInline(c) + " ")
		}
		out.WriteString("</tr>\n")
		text.WriteString("\n")
		used++
	}
	out.WriteString("</tbody></table></div>\n")
	return used
}

func splitRow(line string) []string {
	parts := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// codeMark delimits a lifted code span. NUL cannot appear in the escaped text —
// html.EscapeString does not produce it and these documents do not contain it —
// so a placeholder can never collide with document content, and it matches none
// of the emphasis or link patterns.
const codeMark = "\x00"

func codePlaceholder(i int) string { return codeMark + strconv.Itoa(i) + codeMark }

// inline renders inline markup. Escaping happens FIRST and the markup is
// substituted into the escaped text, so no document content can inject HTML —
// the pages are ours, but the rule costs nothing and removes the question.
func inline(source, s string, note func(string)) string {
	out := html.EscapeString(s)

	// Code spans are LIFTED OUT before anything else runs, then put back last.
	// Wrapping them in <code> in place is not enough: the emphasis passes scan
	// the whole string afterwards, so an asterisk inside a code span (the glob
	// in `vpn.advanced.*`) would pair with an unrelated asterisk later in the
	// line and open an <em> that closes outside the </code>. That produced
	// invalid markup in the shipped bundle. Lifting also keeps link and image
	// syntax inside a code span from being turned into a real link.
	var spans []string
	out = inlineCode.ReplaceAllStringFunc(out, func(m string) string {
		spans = append(spans, inlineCode.FindStringSubmatch(m)[1])
		return codePlaceholder(len(spans) - 1)
	})

	// An image is checked, not rewritten. It has no banner to fall back on the
	// way a link does — the navigation delegate never sees a subresource load —
	// so a remote src would be a silent network request from the one pane whose
	// entire purpose is working with egress cut. Refused here rather than left
	// to the bundle's self-contained test, so Render is safe on its own terms.
	out = inlineImage.ReplaceAllStringFunc(out, func(m string) string {
		g := inlineImage.FindStringSubmatch(m)
		if schemeRe.MatchString(g[2]) || strings.HasPrefix(g[2], "//") {
			note("a remote image (" + g[2] + ") — the pane must load nothing from the network")
			return g[1]
		}
		return fmt.Sprintf("<img src=%q alt=%q>", g[2], g[1])
	})
	out = inlineLink.ReplaceAllStringFunc(out, func(m string) string {
		g := inlineLink.FindStringSubmatch(m)
		return fmt.Sprintf("<a href=%q>%s</a>", rewriteLink(source, g[2], note), g[1])
	})
	out = inlineBold.ReplaceAllString(out, "<strong>$1</strong>")
	out = inlineItalic.ReplaceAllString(out, "$1<em>$2</em>")

	for i, span := range spans {
		out = strings.ReplaceAll(out, codePlaceholder(i), "<code>"+span+"</code>")
	}
	return out
}

// rewriteLink points a cross-document link at the bundled copy of that page.
// A link to something not bundled is left as written; the browser refuses
// anything that is not a local file, so it simply does nothing rather than
// silently leaving the app.
//
// A scheme outside linkSchemes is refused HERE, at build time, rather than left
// for the app's navigation delegate to cancel. Defence in depth is the reason
// the delegate exists, but a javascript: or data: href has no business being in
// the bundle at all, and a build failure is how it gets removed.
func rewriteLink(source, href string, note func(string)) string {
	if scheme := schemeRe.FindString(href); scheme != "" {
		name := strings.ToLower(strings.TrimSuffix(scheme, ":"))
		if !linkSchemes[name] {
			note("a " + name + ": link — only http, https, and mailto are allowed")
			return "#"
		}
		return href
	}
	if strings.HasPrefix(href, "#") {
		return href
	}
	target, frag, _ := strings.Cut(href, "#")
	if target == "" {
		return href
	}
	if source == "" {
		note("a relative link (" + href + ") in a page with no place in the manifest")
		return href
	}
	// Links are written relative to the page they live in, so resolve against
	// that page's own directory: "../adr/0001.md" from usage/config.md is a
	// different file than the same text in concepts/modes.md. path.Join cleans
	// the "../" segments, and the repo root is the frame of reference because
	// the docs also link outside docs/ ("../../configs/dezhban.example.json").
	repoPath := path.Join("docs", path.Dir(source), target)

	// A link with enough "../" to climb past the repo root itself is refused
	// rather than turned into a repoBase URL that climbs past it too — that
	// would resolve to something outside the project, or nothing at all,
	// which is the same class of bug a bad scheme is refused for above.
	if strings.HasPrefix(repoPath, "../") {
		note("a relative link (" + href + ") that climbs above the repository root")
		return "#"
	}

	if inDocs, ok := strings.CutPrefix(repoPath, "docs/"); ok {
		if _, bundled := PageBySource(inDocs); bundled {
			if frag != "" {
				return OutputName(inDocs) + "#" + frag
			}
			return OutputName(inDocs)
		}
	}
	// Not bundled. Point at the repository rather than leaving a path that
	// resolves to nothing beside the bundle — see repoBase. The pane still
	// cannot follow it, but it reports a URL the reader can actually use.
	if frag != "" {
		return repoBase + repoPath + "#" + frag
	}
	return repoBase + repoPath
}

// stripInline reduces a line to its words, for the search index.
func stripInline(s string) string {
	out := inlineImage.ReplaceAllString(s, "$1")
	out = inlineLink.ReplaceAllString(out, "$1")
	out = strings.NewReplacer("`", "", "**", "", "*", "", "_", "").Replace(out)
	return strings.TrimSpace(out)
}

// rowKey reports the config key a table row documents, when its first cell is a
// lone code span and nothing else. The "nothing else" is deliberate: a row whose
// first cell is prose that happens to contain code (`"see `vpn.endpoints`"`) is
// not a definition of that key, and anchoring to it would send a help link to
// the wrong row — silently, which is the one failure mode this package refuses.
func rowKey(cells []string) (string, bool) {
	if len(cells) == 0 {
		return "", false
	}
	m := loneCode.FindStringSubmatch(strings.TrimSpace(cells[0]))
	if m == nil {
		return "", false
	}
	key := strings.TrimSpace(m[1])
	if key == "" {
		return "", false
	}
	return key, true
}

// KeyAnchor derives the fragment id a documented config key gets.
//
// Prefixed to keep key rows out of the way of most headings — but the prefix is
// not a guarantee, and it was documented as one. A heading of "Key flags", which
// docs/usage/cli.md has, slugs to `key-flags`, exactly what a row for a key named
// `flags` would claim: the two namespaces share one document and can meet. Render
// resolves that in the heading's favour and reports it, so the collision fails the
// bundle build rather than sending a help link somewhere plausible and wrong.
func KeyAnchor(key string) string {
	return "key-" + Anchor(key)
}

// Anchor derives the fragment id a heading gets, matching GitHub's rule so an
// anchor written in the docs (and in a Tunable's DocAnchor) resolves in the
// rendered page too.
func Anchor(title string) string {
	s := strings.ToLower(stripInline(title))
	s = anchorStrip.ReplaceAllString(s, "")
	return strings.ReplaceAll(strings.TrimSpace(s), " ", "-")
}
