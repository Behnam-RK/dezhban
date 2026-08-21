package help

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
)

// IndexEntry is one page in help-index.json, which the app's sidebar and search
// are built from. Keys are lowerCamelCase, the same convention every other
// JSON the app decodes uses.
type IndexEntry struct {
	File     string    `json:"file"`
	Source   string    `json:"source"`
	Title    string    `json:"title"`
	Summary  string    `json:"summary"`
	Tutorial int       `json:"tutorial,omitempty"`
	Headings []Heading `json:"headings"`
	// Keys are the per-row anchors of the page's reference tables, so a
	// contextual help link can resolve to one config key rather than to the
	// section heading it shares with dozens of others. Omitted for the pages
	// that document no keys.
	Keys []Heading `json:"keys,omitempty"`
	// Text is the page stripped to words, so search can run entirely in the app
	// with no second pass over the HTML.
	Text string `json:"text"`
}

// Build renders every manifest page from docsDir into outDir, writing the HTML,
// help.css, and help-index.json. It reports the index it wrote.
//
// A missing or renamed source is an error, never a skipped page: a help bundle
// that is quietly missing the troubleshooting page is worse than a build that
// stops.
func Build(docsDir, outDir string) ([]IndexEntry, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", outDir, err)
	}

	index := make([]IndexEntry, 0, len(Pages))
	for _, page := range Pages {
		src := filepath.Join(docsDir, filepath.FromSlash(page.Source))
		data, err := os.ReadFile(src)
		if err != nil {
			return nil, fmt.Errorf("bundled page %s: %w (it is referenced by internal/help; "+
				"moving or renaming a doc means updating the manifest)", page.Source, err)
		}

		rendered := Render(page.Source, string(data))
		if len(rendered.Unsupported) > 0 {
			return nil, fmt.Errorf("%s uses markdown the help renderer cannot show: %s",
				page.Source, strings.Join(rendered.Unsupported, ", "))
		}

		name := OutputName(page.Source)
		if err := os.WriteFile(filepath.Join(outDir, name), []byte(document(page, rendered)), 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", name, err)
		}

		index = append(index, IndexEntry{
			File: name, Source: page.Source, Title: page.Title, Summary: page.Summary,
			Tutorial: page.Tutorial, Headings: rendered.Headings, Keys: rendered.Keys,
			Text: rendered.Text,
		})
	}

	if err := os.WriteFile(filepath.Join(outDir, "help.css"), []byte(stylesheet), 0o644); err != nil {
		return nil, fmt.Errorf("write help.css: %w", err)
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode help-index.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "help-index.json"), data, 0o644); err != nil {
		return nil, fmt.Errorf("write help-index.json: %w", err)
	}
	return index, nil
}

// document wraps rendered body HTML in a page. The stylesheet is a local file
// and there is nothing else: no script, no font, no image from anywhere but the
// bundle. This has to work with every byte of egress cut, which is exactly when
// it will be opened.
func document(page Page, r Rendered) string {
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n")
	b.WriteString("<meta charset=\"utf-8\">\n")
	// The pane's navigation delegate only sees NAVIGATIONS, so it is not what
	// would stop an <img> or an @import from reaching the network. This is: the
	// page itself declares that nothing but its own stylesheet may load. Three
	// layers now say the same thing — the renderer refuses a remote src, the
	// bundle test greps for one, and the page cannot execute it either — because
	// the guarantee ("it never touches the network") is the reason this feature
	// exists at all, and the pane is opened when there is no network to check.
	//
	// `file:` is named alongside 'self' deliberately. A page loaded from a file:
	// URL has an opaque origin, so whether 'self' matches its own sibling
	// stylesheet is a WebKit implementation detail — and a CSP that quietly
	// blocked help.css would ship an unstyled help pane, which is a worse
	// regression than the one this prevents. Naming the scheme keeps the part
	// that matters (no http, no https, nothing off-disk) independent of that.
	b.WriteString("<meta http-equiv=\"Content-Security-Policy\" " +
		"content=\"default-src 'none'; style-src 'self' file:; img-src 'self' file:\">\n")
	b.WriteString("<title>" + html.EscapeString(page.Title) + "</title>\n")
	b.WriteString("<link rel=\"stylesheet\" href=\"help.css\">\n")
	b.WriteString("</head>\n<body>\n")
	b.WriteString(r.HTML)
	b.WriteString("</body>\n</html>\n")
	return b.String()
}

// stylesheet follows the system appearance in both directions, since the pane
// sits inside a native window and a light page in a dark app reads as a bug.
const stylesheet = `
:root {
  color-scheme: light dark;
  --fg: #1d1d1f;
  --muted: #6e6e73;
  --bg: #ffffff;
  --rule: #d2d2d7;
  --code-bg: #f5f5f7;
  --link: #0066cc;
}
@media (prefers-color-scheme: dark) {
  :root {
    --fg: #f5f5f7; --muted: #a1a1a6; --bg: #1e1e1e;
    --rule: #3a3a3c; --code-bg: #2a2a2c; --link: #4da3ff;
  }
}
* { box-sizing: border-box; }
body {
  margin: 0; padding: 24px 28px 64px;
  font: 14px/1.6 -apple-system, BlinkMacSystemFont, "SF Pro Text", sans-serif;
  color: var(--fg); background: var(--bg);
  overflow-wrap: break-word;
}
h1, h2, h3, h4 { line-height: 1.25; margin: 1.6em 0 0.5em; }
h1 { font-size: 1.9em; margin-top: 0; }
h2 { font-size: 1.4em; border-bottom: 1px solid var(--rule); padding-bottom: 0.25em; }
h3 { font-size: 1.15em; }
h4 { font-size: 1em; color: var(--muted); }
p, li { margin: 0.6em 0; }
a { color: var(--link); text-decoration: none; }
a:hover { text-decoration: underline; }
code {
  font: 12.5px/1.5 ui-monospace, "SF Mono", Menlo, monospace;
  background: var(--code-bg); padding: 0.15em 0.35em; border-radius: 4px;
}
pre {
  background: var(--code-bg); padding: 12px 14px; border-radius: 8px;
  overflow-x: auto;
}
pre code { background: none; padding: 0; }
blockquote {
  margin: 1em 0; padding: 0.2em 1em;
  border-left: 3px solid var(--rule); color: var(--muted);
}
hr { border: none; border-top: 1px solid var(--rule); margin: 2em 0; }
/* Wide tables scroll inside their own box so the page body never does. */
.table-scroll { overflow-x: auto; margin: 1em 0; }
table { border-collapse: collapse; width: 100%; font-size: 13px; }
th, td { border: 1px solid var(--rule); padding: 6px 10px; text-align: left; vertical-align: top; }
th { background: var(--code-bg); font-weight: 600; }
img { max-width: 100%; }
:target { scroll-margin-top: 12px; background: rgba(255, 214, 10, 0.25); }
`
