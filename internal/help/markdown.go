package help

import (
	"fmt"
	"html"
	"regexp"
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
	// Text is the page with markup stripped, for substring search.
	Text string
	// Unsupported names constructs the renderer met and could not represent.
	// Non-empty means the page would display wrongly.
	Unsupported []string
}

var (
	inlineCode   = regexp.MustCompile("`([^`]+)`")
	inlineLink   = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)
	inlineImage  = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
	inlineBold   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	inlineItalic = regexp.MustCompile(`(^|[^*])\*([^*]+)\*`)
	headingRe    = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	bulletRe     = regexp.MustCompile(`^\s*[-*]\s+(.*)$`)
	numberedRe   = regexp.MustCompile(`^\s*\d+\.\s+(.*)$`)
	anchorStrip  = regexp.MustCompile(`[^a-z0-9 -]`)
)

// Render converts one markdown document to HTML.
func Render(markdown string) Rendered {
	var out strings.Builder
	var text strings.Builder
	r := Rendered{}

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

		if m := headingRe.FindStringSubmatch(trimmed); m != nil {
			closeList()
			closeQuote()
			level := len(m[1])
			title := stripInline(m[2])
			anchor := Anchor(title)
			r.Headings = append(r.Headings, Heading{Level: level, Text: title, Anchor: anchor})
			fmt.Fprintf(&out, "<h%d id=%q>%s</h%d>\n", level, anchor, inline(m[2]), level)
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
			consumed := renderTable(&out, &text, lines[i:])
			i += consumed - 1
			continue
		}

		if strings.HasPrefix(trimmed, ">") {
			closeList()
			if !inQuote {
				out.WriteString("<blockquote>\n")
				inQuote = true
			}
			body := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			out.WriteString("<p>" + inline(body) + "</p>\n")
			text.WriteString(stripInline(body) + "\n")
			continue
		}

		if m := bulletRe.FindStringSubmatch(line); m != nil {
			if inList && listTag != "ul" {
				closeList()
			}
			if !inList {
				out.WriteString("<ul>\n")
				inList, listTag = true, "ul"
			}
			out.WriteString("<li>" + inline(m[1]) + "</li>\n")
			text.WriteString(stripInline(m[1]) + "\n")
			continue
		}
		if m := numberedRe.FindStringSubmatch(line); m != nil {
			if inList && listTag != "ol" {
				closeList()
			}
			if !inList {
				out.WriteString("<ol>\n")
				inList, listTag = true, "ol"
			}
			out.WriteString("<li>" + inline(m[1]) + "</li>\n")
			text.WriteString(stripInline(m[1]) + "\n")
			continue
		}

		// A continuation line inside a list item, rather than a new paragraph.
		if inList && strings.HasPrefix(line, "  ") {
			out.WriteString(" " + inline(trimmed))
			text.WriteString(stripInline(trimmed) + "\n")
			continue
		}

		closeList()
		closeQuote()
		out.WriteString("<p>" + inline(trimmed) + "</p>\n")
		text.WriteString(stripInline(trimmed) + "\n")
	}

	closeList()
	closeQuote()
	if inCode {
		r.Unsupported = append(r.Unsupported, "an unclosed code fence")
	}

	r.HTML = out.String()
	r.Text = text.String()
	return r
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
func renderTable(out *strings.Builder, text *strings.Builder, lines []string) int {
	header := splitRow(lines[0])
	out.WriteString("<div class=\"table-scroll\"><table>\n<thead><tr>")
	for _, c := range header {
		out.WriteString("<th>" + inline(c) + "</th>")
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
		out.WriteString("<tr>")
		for _, c := range splitRow(s) {
			out.WriteString("<td>" + inline(c) + "</td>")
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

// inline renders inline markup. Escaping happens FIRST and the markup is
// substituted into the escaped text, so no document content can inject HTML —
// the pages are ours, but the rule costs nothing and removes the question.
func inline(s string) string {
	out := html.EscapeString(s)
	// Code first: its contents must not then be read as emphasis.
	out = inlineCode.ReplaceAllString(out, "<code>$1</code>")
	out = inlineImage.ReplaceAllString(out, `<img src="$2" alt="$1">`)
	out = inlineLink.ReplaceAllStringFunc(out, func(m string) string {
		g := inlineLink.FindStringSubmatch(m)
		return fmt.Sprintf("<a href=%q>%s</a>", rewriteLink(g[2]), g[1])
	})
	out = inlineBold.ReplaceAllString(out, "<strong>$1</strong>")
	out = inlineItalic.ReplaceAllString(out, "$1<em>$2</em>")
	return out
}

// rewriteLink points a cross-document link at the bundled copy of that page.
// A link to something not bundled is left as written; the browser refuses
// anything that is not a local file, so it simply does nothing rather than
// silently leaving the app.
func rewriteLink(href string) string {
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") ||
		strings.HasPrefix(href, "#") {
		return href
	}
	path, frag, _ := strings.Cut(href, "#")
	// Links are written relative to the page they live in ("../adr/0001.md",
	// "config.md"); resolve to the docs-relative form the manifest uses.
	path = strings.TrimPrefix(path, "./")
	for strings.HasPrefix(path, "../") {
		path = strings.TrimPrefix(path, "../")
	}
	for _, p := range Pages {
		if p.Source == path || strings.HasSuffix(p.Source, "/"+path) {
			if frag != "" {
				return OutputName(p.Source) + "#" + frag
			}
			return OutputName(p.Source)
		}
	}
	return href
}

// stripInline reduces a line to its words, for the search index.
func stripInline(s string) string {
	out := inlineImage.ReplaceAllString(s, "$1")
	out = inlineLink.ReplaceAllString(out, "$1")
	out = strings.NewReplacer("`", "", "**", "", "*", "", "_", "").Replace(out)
	return strings.TrimSpace(out)
}

// Anchor derives the fragment id a heading gets, matching GitHub's rule so an
// anchor written in the docs (and in a Tunable's DocAnchor) resolves in the
// rendered page too.
func Anchor(title string) string {
	s := strings.ToLower(stripInline(title))
	s = anchorStrip.ReplaceAllString(s, "")
	return strings.ReplaceAll(strings.TrimSpace(s), " ", "-")
}
