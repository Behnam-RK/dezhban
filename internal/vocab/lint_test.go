package vocab

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// repoRoot is two levels up from internal/vocab.
const repoRoot = "../.."

func glossary() string { return filepath.Join(repoRoot, "docs/concepts/glossary.md") }

// allowed exempts a literal from the lint BY ITS EXACT TEXT, and every entry
// carries the reason. An exception with a written reason is a recorded decision;
// an exception without one is a silent dodge that the next person reads as
// evidence the rule does not really apply.
//
// Keyed by the literal's exact content so an entry cannot quietly widen: change
// the copy and the exemption stops applying, which is the correct default for a
// list of things the rule does not reach.
var allowed = map[string]string{
	// Flag names and shell tokens. These are CLI identifiers a user types, not
	// prose — renaming them would break every script and every muscle memory,
	// and the glossary's own rule is that identifiers keep the technical word.
	"-no-daemon":          "the flag's own name — a stable CLI identifier",
	"--no-daemon":         "the flag's own name — a stable CLI identifier",
	"DEZHBAN_NO_DAEMON=1": "the environment variable's own name",
	"remove rules unconditionally, bypassing the daemon (unblock is already unconditional)": "" +
		"a --force flag's usage text, and it must name the mechanism being bypassed; " +
		"'bypassing dezhban' would read as bypassing the product, which is the opposite of true",
	"skip the control socket, act on the firewall directly (or DEZHBAN_NO_DAEMON=1)": "" +
		"--no-daemon's own usage text; it has to explain the flag, whose name is the identifier above",
	"--no-daemon     Don't use the control socket; act on the firewall directly": "" +
		"the flag's line in `dezhban help`; the word here IS the flag's name, and a help " +
		"page that lists a flag under a different name than the one you type is useless",

	// A log fragment built one line before the log call that consumes it. The
	// exemption pass walks literals INSIDE a logging CallExpr, so a string
	// assigned to a local first is invisible to it — the local here feeds three
	// o.Log.Warn calls and goes nowhere else, which is the technical register
	// where "egress" is the correct word. Exempting the fragment is honest;
	// teaching isLogCall to chase locals would be a dataflow analysis, and a
	// wrong one the first time a local reached both a log and a reply.
	"egress relaxed to configured protocols/ports": "" +
		"a fragment of three o.Log.Warn lines, assigned to a local one line above them",

	// A file path. Renaming a shipped ADR to satisfy a copy rule is precisely
	// what ADRs forbid, and the path has to match the file on disk.
	"has leaked. See docs/adr/0003-biometric-token-over-existing-daemon.md.": "" +
		"a shipped ADR's filename, which is a path on disk and not prose",
}

// goScopes are the trees whose string literals reach a user. cmd/ is the CLI's
// human output, internal/render composes the sentence every surface shows, and
// internal/config holds the two tables whose prose IS the settings UI:
// schema.go's Tunable.Label/Help (printed by `dezhban config schema` and shown
// as each row's label and hint in the macOS Settings pane) and preset.go's
// Summary/Cost (printed by `config preset list`/`show`).
//
// That last scope is easy to miss precisely because nothing in it prints:
// `fmt.Printf("  %s\n", t.Help)` lives in cmd/, so a lint that follows the print
// statement sees a format verb and declares the file clean while the sentence
// itself sits in a package it never opens. Copy is where the words are, not
// where the write call is.
//
// internal/runner is here for the same reason, and it was missed for the same
// reason. Its refusal messages look like internals — `reply(false, "…")` deep in
// a select case — but reply puts them in control.Response.Error, and
// cmd/dezhban/control_client.go prints that verbatim after "dezhban refused:".
// Four of them said "daemon" or "egress" on the day this lint shipped, so
// `dezhban pause` in standby answered a user with two retired words in one
// sentence. The log calls all around them stay exempt through isLogCall, which
// is exactly the distinction go/parser buys.
var goScopes = []string{"cmd", "internal/render", "internal/config", "internal/runner"}

// goExempt are files whose string literals are not copy at all. completion.go is
// one big shell-script template: every "daemon" in it is `--no-daemon`, a flag
// name a user types. Exempting the file rather than each generated line keeps
// the allowlist from filling up with fragments of bash.
var goExempt = []string{"cmd/dezhban/completion.go"}

// docScopes are the pages linted as prose. README and the intro docs are out:
// "kill switch" is the correct name for what dezhban is when introducing it, and
// the glossary says so. docs/adr is out for a stronger reason — an ADR is a
// permanent record of a decision as it was made, and editing a shipped one to
// satisfy a lint is the thing ADRs explicitly forbid. It is left off this list
// rather than exempted below, so the omission is the decision rather than a
// filter that only looks like coverage.
var docScopes = []string{"docs/usage", "docs/concepts", "docs/contribute"}

// docExempt are pages that describe the vocabulary rather than obey it. The
// glossary is the list; a page that quotes every banned word in order to ban it
// cannot also be checked against itself.
var docExempt = []string{"docs/concepts/glossary.md"}

// TestTheGlossaryStillParses is separate from the lint itself so a broken table
// fails as "the glossary changed shape", not as "zero violations found". A lint
// that silently checks an empty list is worse than no lint: it reports success.
func TestTheGlossaryStillParses(t *testing.T) {
	t.Parallel()
	terms, err := Load(glossary())
	if err != nil {
		t.Fatal(err)
	}
	// The four the audit turned up, plus a copy-only one. If a rename removes
	// any of these rows the removal should be deliberate, so name them.
	want := map[string]bool{"protection": true, "daemon": true, "egress": true, "relaxation": true}
	for _, term := range terms {
		delete(want, term.Phrase)
	}
	if len(want) > 0 {
		t.Errorf("glossary no longer bans %v — if that was intended, update this test with the reason", want)
	}
	var copyOnly int
	for _, term := range terms {
		if term.CopyOnly {
			copyOnly++
		}
	}
	if copyOnly == 0 {
		t.Error("no ‡ rows parsed; the marker is how a word stays legal in logs and docs, " +
			"and losing it would force the technical register to be renamed too")
	}
}

// TestEveryTermMatchesItself is the row-level version of Load's zero-terms
// guard, and it exists because the table-level one is not enough: a row can
// parse, be counted, and still match nothing.
//
// Three rows shipped that way — every phrase ending in a config key, e.g.
// "Enable VPN guard (vpn.enabled)" — because \b after ")" asserts a transition
// that cannot occur. They read as enforced on the page and enforced nothing, so
// the lint reported success over a rule it was not applying. A term that cannot
// find itself in its own text can never find itself in anyone else's.
func TestEveryTermMatchesItself(t *testing.T) {
	t.Parallel()
	terms, err := Load(glossary())
	if err != nil {
		t.Fatal(err)
	}
	for _, term := range terms {
		if term.re.FindString(term.Phrase) == "" {
			t.Errorf("glossary bans %q but the matcher never fires — the row lints nothing. "+
				"Check for leading/trailing punctuation, which \\b cannot anchor against.",
				term.Phrase)
		}
		// Again inside a sentence: the anchors must survive real surroundings,
		// not just an exact-string match, or a row would pass here and still
		// miss every actual use.
		if term.re.FindString("the "+term.Phrase+" thing") == "" {
			t.Errorf("glossary bans %q but the matcher does not fire inside a sentence", term.Phrase)
		}
	}
}

// TestUserFacingCopyUsesTheGlossary is the check the glossary's own claim to
// authority depends on: "when user-facing copy and this page disagree, the copy
// is wrong" was true only as an intention until something verified it.
func TestUserFacingCopyUsesTheGlossary(t *testing.T) {
	t.Parallel()
	terms, err := Load(glossary())
	if err != nil {
		t.Fatal(err)
	}

	for _, scope := range goScopes {
		for _, file := range goFiles(t, filepath.Join(repoRoot, scope)) {
			checkGoFile(t, file, terms)
		}
	}
	for _, file := range swiftFiles(t, filepath.Join(repoRoot, "gui/macos/Sources")) {
		checkSwiftFile(t, file, terms)
	}
	for _, scope := range docScopes {
		for _, file := range markdownFiles(t, filepath.Join(repoRoot, scope)) {
			checkDoc(t, file, terms)
		}
	}
}

// checkGoFile flags string literals that reach a person, using go/parser to tell
// them from the ones that reach a log. grep cannot make that distinction: it
// would either miss real violations or ban the logs, where "daemon" and "egress"
// are the right words.
func checkGoFile(t *testing.T, path string, terms []Term) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}

	// Two passes. The first collects every literal inside a logging call —
	// including nested ones, since a log argument is often built by a helper —
	// and the second flags what is left.
	exempt := map[ast.Node]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isLogCall(call.Fun) {
			return true
		}
		ast.Inspect(call, func(inner ast.Node) bool {
			exempt[inner] = true
			return true
		})
		return true
	})

	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || exempt[n] {
			return true
		}
		text, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		// Line by line, because a `usage` block is one literal holding a whole
		// help page: reporting "this 40-line string says daemon" would name the
		// page rather than the sentence, and an allowlist keyed on the whole
		// page would exempt every future edit to it too.
		start := fset.Position(lit.Pos()).Line
		for off, line := range strings.Split(text, "\n") {
			if _, ok := allowed[strings.TrimSpace(line)]; ok {
				continue
			}
			for _, hit := range Check(line, terms, true) {
				t.Errorf("%s:%d: user-facing copy says %q — say %s instead (docs/concepts/glossary.md).\n    in: %q",
					rel(path), start+off, hit.Match, hit.Term.Instead, trim(line))
			}
		}
		return true
	})
}

// isLogCall reports whether a call is a logging one, whose arguments are the
// technical register and therefore exempt. Matched by name rather than by type:
// the loggers here arrive as *slog.Logger fields (o.Log, s.log) and a
// types-checked answer would cost a full package load for no extra certainty.
func isLogCall(fun ast.Expr) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "Debug", "Info", "Warn", "Error", "Log", "Printf", "Print", "Println", "Fatalf":
	default:
		return false
	}
	// The receiver has to look like a logger — otherwise this would exempt every
	// fmt.Println in the CLI, which is exactly the output being linted.
	var recv string
	switch x := sel.X.(type) {
	case *ast.Ident:
		recv = x.Name
	case *ast.SelectorExpr:
		recv = x.Sel.Name
	case *ast.CallExpr:
		if inner, ok := x.Fun.(*ast.SelectorExpr); ok {
			recv = inner.Sel.Name
		}
	}
	switch strings.ToLower(recv) {
	case "log", "logger", "slog":
		return true
	}
	return false
}

// checkSwiftFile scans string literals line by line. There is no Swift parser
// here, so this is the pragmatic form: doc comments are skipped (they are notes
// to developers) and everything in quotes is treated as copy.
//
// Multi-line (`"""`) literals get their own mode, because the line-by-line form
// cannot see them at all: the fence lines carry no content and the content lines
// carry no quotes, so an entire alert body registers as zero literals. That is
// not a theoretical gap — ConfigApply's restart alert sat inside one and told
// users dezhban would "keep the daemon on the old values" while this lint
// reported the file clean. A modal alert is the most user-facing copy the app
// has, so the one shape the scanner could not see was the worst one to miss.
//
// Each line inside the fence is checked on its own, matching how checkGoFile
// splits a `usage` block: a violation names the sentence, not the alert. Swift's
// trailing `\` line-continuation means a banned phrase can straddle two lines,
// which this will miss — the same soft-wrap limit checkGoFile has, and the
// reason Term.re relaxes internal whitespace to \s+ rather than a literal space.
func checkSwiftFile(t *testing.T, path string, terms []Term) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	inBlock := false
	for i, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		// A fence toggles the mode and never carries copy itself: the opening
		// line is `... = """` and the closing one is `"""` alone.
		if strings.Contains(line, `"""`) {
			inBlock = !inBlock
			continue
		}
		if inBlock {
			// No quotes to find in here — the whole line IS the literal. Strip
			// Swift's trailing line-continuation backslash so it cannot end up
			// inside a matched phrase.
			lit := strings.TrimSuffix(trimmed, `\`)
			if _, ok := allowed[strings.TrimSpace(lit)]; ok {
				continue
			}
			for _, hit := range Check(lit, terms, true) {
				t.Errorf("%s:%d: user-facing copy says %q — say %s instead (docs/concepts/glossary.md).\n    in: %q",
					rel(path), i+1, hit.Match, hit.Term.Instead, trim(lit))
			}
			continue
		}
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		for _, lit := range swiftLiterals(line) {
			if _, ok := allowed[lit]; ok {
				continue
			}
			for _, hit := range Check(lit, terms, true) {
				t.Errorf("%s:%d: user-facing copy says %q — say %s instead (docs/concepts/glossary.md).\n    in: %q",
					rel(path), i+1, hit.Match, hit.Term.Instead, trim(lit))
			}
		}
	}
}

// swiftLiterals pulls the double-quoted runs out of one line, stopping at a
// trailing `//` comment. Naive by design: it does not understand escapes or
// multi-line literals, and a false positive here costs a rewording while a
// parser costs a dependency.
//
// The `//` check runs only BETWEEN literals, never inside one, which is what
// keeps a URL in a string ("https://…") from truncating the line it appears on.
// checkSwiftFile drops whole-line comments before calling this; a comment
// hanging off the end of a code line is the same register and needs the same
// treatment — it is a note to a developer, not copy.
func swiftLiterals(line string) []string {
	var out []string
	for i := 0; i < len(line); i++ {
		if line[i] == '/' && i+1 < len(line) && line[i+1] == '/' {
			return out
		}
		if line[i] != '"' {
			continue
		}
		end := strings.IndexByte(line[i+1:], '"')
		if end < 0 {
			return out
		}
		out = append(out, line[i+1:i+1+end])
		i += end + 1
	}
	return out
}

// checkDoc lints prose. Code fences, tables and inline code are skipped: they
// quote config keys, JSON and commands, which are identifiers.
func checkDoc(t *testing.T, path string, terms []Term) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	inFence := false
	for i, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence || strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		// copy=false: docs are the technical register, so a ‡ row does not apply.
		// What is left are the words wrong in both, which is what "protection"
		// drifting back into a runbook actually is.
		for _, hit := range Check(stripInlineCode(line), terms, false) {
			t.Errorf("%s:%d: docs prose says %q — say %s instead (docs/concepts/glossary.md).\n    in: %q",
				rel(path), i+1, hit.Match, hit.Term.Instead, trim(line))
		}
	}
}

// stripInlineCode blanks `backticked` spans so a config key or a shell flag does
// not read as prose.
func stripInlineCode(line string) string {
	var b strings.Builder
	inCode := false
	for _, r := range line {
		if r == '`' {
			inCode = !inCode
			b.WriteByte(' ')
			continue
		}
		if inCode {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func goFiles(t *testing.T, root string) []string {
	return filesUnder(t, root, func(p string) bool {
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return false
		}
		for _, ex := range goExempt {
			if strings.HasSuffix(filepath.ToSlash(p), ex) {
				return false
			}
		}
		return true
	})
}

func swiftFiles(t *testing.T, root string) []string {
	return filesUnder(t, root, func(p string) bool { return strings.HasSuffix(p, ".swift") })
}

func markdownFiles(t *testing.T, root string) []string {
	return filesUnder(t, root, func(p string) bool {
		if !strings.HasSuffix(p, ".md") {
			return false
		}
		for _, ex := range docExempt {
			if strings.Contains(filepath.ToSlash(p), ex) {
				return false
			}
		}
		return true
	})
}

func filesUnder(t *testing.T, root string, keep func(string) bool) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && keep(p) {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(out) == 0 {
		t.Fatalf("no files found under %s — the lint would pass by looking at nothing", root)
	}
	return out
}

func rel(p string) string {
	r, err := filepath.Rel(repoRoot, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}

func trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 90 {
		return s[:90] + "…"
	}
	return s
}
