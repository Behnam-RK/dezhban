package vocab

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The lint in lint_test.go exercises this package only against the real repo, so
// every promise made in the doc comments — word boundaries, the \s+ relaxation,
// the ‡ register split, one row yielding several phrases — is verified by the
// absence of failures somewhere else. That is the wrong direction for a matcher:
// break compile() and the lint reports ZERO VIOLATIONS, which reads as success.
// These tests fail loudly instead.

// write puts a glossary fragment on disk with the shape Load expects: the
// heading, the marker legend that really precedes the table, then the table.
func write(t *testing.T, rows ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("# Glossary\n\n## Something else\n\n| a | b |\n|---|---|\n| x | y |\n\n")
	b.WriteString(heading)
	b.WriteString("\n\n")
	b.WriteString("| Marker | Where it is enforced |\n|---|---|\n")
	b.WriteString("| *(none)* | Everywhere. |\n| ‡ | Copy only. |\n| † | Not linted. |\n\n")
	b.WriteString("| ")
	b.WriteString(tableHeader)
	b.WriteString(" | Say | Why |\n|---|---|---|\n")
	for _, r := range rows {
		b.WriteString(r)
		b.WriteString("\n")
	}
	p := filepath.Join(t.TempDir(), "glossary.md")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func loadOne(t *testing.T, rows ...string) []Term {
	t.Helper()
	terms, err := Load(write(t, rows...))
	if err != nil {
		t.Fatal(err)
	}
	return terms
}

// The boundary claim from compile's doc comment, in both directions. It is the
// difference between a usable lint and one that fires on every "unsecured" until
// somebody switches it off.
func TestWordBoundaries(t *testing.T) {
	terms := loadOne(t, `| "secured", "daemon" | "the guard" | because |`)

	for _, tc := range []struct {
		text string
		want bool
	}{
		{"the link is secured", true},
		{"an unsecured link", false}, // suffix match must not fire
		{"securedly", false},         // prefix match must not fire
		{"restart the daemon", true}, //
		{"daemonize the process", false},
		{"SECURED", true},    // case-insensitive
		{"re-secured", true}, // a hyphen IS a boundary, and the word is there
		{"the guard is up", false},
	} {
		got := len(Check(tc.text, terms, true)) > 0
		if got != tc.want {
			t.Errorf("Check(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

// A phrase that got soft-wrapped across two lines is the same sentence. Prose in
// this repo is hard-wrapped at ~80 columns, so without this a multi-word ban
// would miss roughly every other occurrence — silently, since a miss is a pass.
func TestPhrasesMatchAcrossWhitespace(t *testing.T) {
	terms := loadOne(t, `| "guard is disarmed" | "standby" | because |`)

	for _, text := range []string{
		"the guard is disarmed",
		"the guard is\ndisarmed",
		"the guard   is\t disarmed",
	} {
		if len(Check(text, terms, true)) == 0 {
			t.Errorf("Check(%q) found nothing; internal whitespace must be relaxed", text)
		}
	}
	// It relaxes whitespace, not word order or content.
	if len(Check("the guard is not disarmed", terms, true)) != 0 {
		t.Error("an interposed word matched; \\s+ must not span other words")
	}
}

// The register split is the whole reason the table carries markers: without it
// the lint has to ban "daemon" in the logs too (making them unwritable) or
// nowhere (leaving the app unchecked).
func TestCopyOnlyAppliesToCopyOnly(t *testing.T) {
	terms := loadOne(t,
		`| ‡ "Daemon" | "dezhban" | wrong on a button, right in a log |`,
		`| "Protection" | "the guard" | wrong in both registers |`,
	)

	const text = "the daemon offers no protection"
	if n := len(Check(text, terms, true)); n != 2 {
		t.Errorf("user-facing hits = %d, want both terms", n)
	}
	hits := Check(text, terms, false)
	if len(hits) != 1 {
		t.Fatalf("technical-register hits = %d, want only the unmarked term", len(hits))
	}
	if hits[0].Term.Phrase != "protection" {
		t.Errorf("technical register flagged %q; a ‡ row must not apply here", hits[0].Term.Phrase)
	}
}

// One row, several spellings of the same mistake — which is how the page is
// already written, and reading it as written is the point of parsing it at all.
func TestARowYieldsEveryQuotedPhrase(t *testing.T) {
	terms := loadOne(t, `| "Protection" / "protected" / "protecting" | "the guard" | one concept |`)

	got := map[string]bool{}
	for _, term := range terms {
		got[term.Phrase] = true
		if term.Instead != `"the guard"` {
			t.Errorf("%q carries Instead %q; the replacement is the useful half of the message",
				term.Phrase, term.Instead)
		}
	}
	for _, want := range []string{"protection", "protected", "protecting"} {
		if !got[want] {
			t.Errorf("row did not yield %q (got %v)", want, got)
		}
	}
}

// † rows are documentation for a reviewer, not input to the matcher. Linting
// them would flag "blocked" in FULL BLOCK, where it is the correct word.
func TestContextualRowsAreNotLinted(t *testing.T) {
	terms := loadOne(t,
		`| † "Blocked" for STANDBY | "Standby" | needs judgement |`,
		`| "Egress" | "traffic" | technical |`,
	)

	if len(terms) != 1 || terms[0].Phrase != "egress" {
		t.Fatalf("terms = %+v, want only the non-† row", terms)
	}
	if len(Check("traffic is blocked", terms, true)) != 0 {
		t.Error("a † row was linted")
	}
}

// The legend table now precedes the terms table inside the same section. Parsing
// must anchor on the terms header, not on the first separator it meets —
// otherwise a quoted example in the legend becomes a banned word nobody wrote.
func TestTheMarkerLegendIsNotReadAsTerms(t *testing.T) {
	p := filepath.Join(t.TempDir(), "g.md")
	body := heading + "\n\n" +
		"| Marker | Where it is enforced |\n|---|---|\n" +
		`| ‡ | Applies to "user-facing copy" only. |` + "\n\n" +
		"| " + tableHeader + " | Say | Why |\n|---|---|---|\n" +
		`| "Egress" | "traffic" | technical |` + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	terms, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(terms) != 1 || terms[0].Phrase != "egress" {
		t.Fatalf("terms = %+v, want only the real table's row — the legend's own quoted "+
			"phrase must not become a term", terms)
	}
}

// Every way the page can stop being parseable has to be an ERROR, never an empty
// term list. A lint that checks nothing reports success, which is worse than no
// lint: it converts an unguarded rename into a green build.
func TestAnUnparseablePageIsAnError(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"heading renamed":  "## Words we avoid\n\n| " + tableHeader + " | Say |\n|---|---|\n| \"Egress\" | x |\n",
		"column renamed":   heading + "\n\n| Avoid | Say |\n|---|---|\n| \"Egress\" | x |\n",
		"table removed":    heading + "\n\nJust prose now.\n",
		"rows all removed": heading + "\n\n| " + tableHeader + " | Say |\n|---|---|\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".md")
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p); err == nil {
				t.Error("Load succeeded; a page the lint cannot read must fail loudly")
			}
		})
	}
}

// A later H2 ends the section, so a table added elsewhere on the page is not
// swept in as vocabulary.
func TestALaterSectionIsNotSweptIn(t *testing.T) {
	p := filepath.Join(t.TempDir(), "g.md")
	body := heading + "\n\n| " + tableHeader + " | Say |\n|---|---|\n| \"Egress\" | \"traffic\" |\n\n" +
		"## Exit codes\n\n| Code | Meaning |\n|---|---|\n| \"daemon refused\" | 4 |\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	terms, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(terms) != 1 {
		t.Errorf("terms = %+v, want only the vocabulary section's row", terms)
	}
}

// Hit.Match carries the text AS IT APPEARED, not the lowercased phrase — the
// error message has to quote the sentence back or the author cannot find it.
func TestHitReportsTheTextAsWritten(t *testing.T) {
	terms := loadOne(t, `| "Egress" | "traffic" | technical |`)

	hits := Check("All Egress is cut", terms, true)
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	if hits[0].Match != "Egress" {
		t.Errorf("Match = %q, want the casing as written", hits[0].Match)
	}
}
