// Package vocab reads the banned-word table out of docs/concepts/glossary.md and
// matches text against it. It exists so the glossary is the thing a rename is
// checked against, rather than a page that happens to agree with a hardcoded
// list until someone edits one of the two.
//
// The glossary already declares itself the authority — "when user-facing copy
// and this page disagree, the copy is wrong" — but nothing verified that, and
// user-facing copy drifted back to "protection", "egress" and "daemon" while the
// page said not to. Parsing the page is the cheapest way to make the claim true:
// there is one list, it is the one a human reads, and the build fails when copy
// disagrees with it.
//
// Same trick internal/config/docdrift_test.go already uses to read
// docs/usage/config.md from a test.
package vocab

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// heading is the section whose table is parsed. Renaming it in the glossary is a
// deliberate act; the parser fails loudly rather than silently linting nothing,
// because a lint that quietly checks an empty list is worse than no lint.
const heading = "## Words we do not use"

// tableHeader is the first cell of the terms table's header row, and the parser
// latches onto the separator that FOLLOWS it rather than onto the first
// separator in the section.
//
// The section opens with a second table — the marker legend explaining ‡ and † —
// so "first separator wins" would start reading terms out of the legend. Today
// that is harmless only by accident: no legend row's first cell happens to
// contain a double-quoted phrase. Anchoring on the header makes it structural,
// so a quoted example added to the legend cannot inject a term that no one
// wrote down as one.
const tableHeader = "Don't say"

const (
	// contextual ("†") marks a row whose ban needs judgement rather than a
	// string match — "Blocked" is correct in FULL BLOCK and wrong for STANDBY,
	// and no matcher can tell those apart. Such a row is documentation for a
	// reviewer, not an input to the lint. Saying so in the table beats a lint
	// that either misses the real cases or drowns the honest ones in noise
	// until someone switches it off.
	contextual = "†"
	// copyOnly ("‡") marks a row that applies to user-facing copy but not to
	// the technical register. "Daemon" and "egress" are the shape of it: wrong
	// on a button, exactly right in a log line and in these docs, which say so
	// themselves. Without this distinction the lint would have to ban them
	// everywhere (making the docs unwritable) or nowhere (making the app's copy
	// unchecked), and both are worse than reading the marker.
	copyOnly = "‡"
)

// A Term is one banned phrase and what to say instead.
type Term struct {
	// Phrase is the exact wording that must not appear, lowercased.
	Phrase string
	// Instead is the "Say" cell verbatim — the message is only useful if it
	// carries the replacement, not just the complaint.
	Instead string
	// CopyOnly restricts this term to user-facing copy. Callers linting the
	// technical register (docs prose, logs) skip these.
	CopyOnly bool
	// re matches Phrase on word boundaries, so banning "secured" does not fire
	// on "unsecured" and banning "daemon" does not fire on "daemonize".
	re *regexp.Regexp
}

// Hit is one occurrence of a banned phrase.
type Hit struct {
	Term  Term
	Match string // the text as it actually appeared, for the error message
}

// Load parses the banned-word table from a glossary at path.
//
// A row contributes every double-quoted phrase in its "Don't say" cell, so one
// row can retire several spellings of the same mistake — which is how the page
// is already written, and reading it as it is written is the point.
func Load(path string) ([]Term, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var terms []Term
	inSection, sawHeader, inTable := false, false, false
	// foundTable is inTable's latch and is never reset. inTable itself is cleared
	// by the next H2 — correctly, so a later section's table is not read as
	// vocabulary — but the "did we find it at all" check below runs after the
	// loop, so reusing inTable there would report a missing table for any page
	// that simply continues past this section. That made the parse depend on the
	// glossary's section ORDER, which nothing states and nobody would preserve on
	// purpose.
	foundTable := false
	quoted := regexp.MustCompile(`"([^"]+)"`)

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " ")
		if strings.HasPrefix(line, "## ") {
			// Any other H2 ends the section, so a table added later elsewhere on
			// the page is not silently swept in.
			inSection = line == heading
			sawHeader, inTable = false, false
			continue
		}
		if !inSection || !strings.HasPrefix(line, "|") {
			continue
		}
		cells := splitRow(line)
		if len(cells) < 2 {
			continue
		}
		// Wait for the terms table's own header, then for the |---|---| separator
		// under it; the first data row follows. Anything before that — the marker
		// legend and its own separator — is skipped rather than read as terms.
		if !inTable {
			switch {
			case cells[0] == tableHeader:
				sawHeader = true
			case sawHeader && strings.HasPrefix(cells[0], "---"):
				inTable, foundTable = true, true
			}
			continue
		}
		dont, say := cells[0], cells[1]
		if strings.Contains(dont, contextual) {
			continue
		}
		for _, m := range quoted.FindAllStringSubmatch(dont, -1) {
			phrase := strings.ToLower(strings.TrimSpace(m[1]))
			if phrase == "" {
				continue
			}
			re, err := compile(phrase)
			if err != nil {
				return nil, fmt.Errorf("glossary row %q: %w", dont, err)
			}
			terms = append(terms, Term{
				Phrase:   phrase,
				Instead:  strings.TrimSpace(say),
				CopyOnly: strings.Contains(dont, copyOnly),
				re:       re,
			})
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !foundTable {
		return nil, fmt.Errorf("%s: no %q table with a %q column found — the lint reads it, "+
			"so a rename of either the heading or the column silently disables the check",
			path, heading, tableHeader)
	}
	if len(terms) == 0 {
		return nil, fmt.Errorf("%s: the %q table parsed to zero terms", path, heading)
	}
	return terms, nil
}

// compile builds the word-boundary matcher for a phrase. Internal whitespace is
// relaxed to \s+ so a phrase that got soft-wrapped across two lines in prose
// still matches — a line break is not a different sentence.
func compile(phrase string) (*regexp.Regexp, error) {
	parts := strings.Fields(phrase)
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return regexp.Compile(`(?i)\b` + strings.Join(parts, `\s+`) + `\b`)
}

// Check reports each banned term that appears in text — one Hit per term, at its
// first occurrence, not one per occurrence. The message names the term and its
// replacement, so a second hit on the same term would repeat the same advice
// about the same string. Callers decide what counts as user-facing; this only
// answers "does this string say a word we retired".
//
// userFacing says which register text is in. False restricts the check to terms
// wrong in both, so linting docs prose or a log line does not demand the
// technical vocabulary be renamed too. (Named for the register rather than as
// `copy`, which would shadow the builtin in a function that may one day want it.)
func Check(text string, terms []Term, userFacing bool) []Hit {
	var hits []Hit
	for _, t := range terms {
		if t.CopyOnly && !userFacing {
			continue
		}
		if m := t.re.FindString(text); m != "" {
			hits = append(hits, Hit{Term: t, Match: m})
		}
	}
	return hits
}

// splitRow splits a markdown table row into its cells, dropping the empty
// leading and trailing fields the outer pipes produce.
func splitRow(line string) []string {
	cells := strings.Split(strings.Trim(line, "|"), "|")
	for i, c := range cells {
		cells[i] = strings.TrimSpace(c)
	}
	return cells
}
