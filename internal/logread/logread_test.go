package logread

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/logging"
)

// The quoting rule is the whole reason this parser exists rather than a
// strings.Split. slog quotes any value containing a space, and a naive split
// turns one warning into a truncated message plus two garbage attrs.
func TestAQuotedMessageWithSpacesStaysOneMessage(t *testing.T) {
	line := `time=2026-08-21T10:15:43.972+03:30 level=WARN msg="rules missing, re-applied" repairs=2 mode=guard`
	r := ParseLine(line)

	if r.Level != "WARN" {
		t.Errorf("Level = %q", r.Level)
	}
	if r.Msg != "rules missing, re-applied" {
		t.Errorf("Msg = %q, want the whole quoted string", r.Msg)
	}
	if len(r.Attrs) != 2 {
		t.Fatalf("Attrs = %v, want 2", r.Attrs)
	}
	if r.Attrs[0] != (Attr{"repairs", "2"}) || r.Attrs[1] != (Attr{"mode", "guard"}) {
		t.Errorf("Attrs = %v", r.Attrs)
	}
	if r.Time.IsZero() {
		t.Error("Time did not parse")
	}
	if r.Raw != line {
		t.Error("Raw must be the original line, verbatim")
	}
}

// An escaped quote inside a value must not end the value early.
func TestEscapesInsideAValueSurvive(t *testing.T) {
	r := ParseLine(`time=2026-08-21T10:15:43Z level=ERROR msg="pfctl said \"no such anchor\"" n=1`)
	if r.Msg != `pfctl said "no such anchor"` {
		t.Errorf("Msg = %q", r.Msg)
	}
	if len(r.Attrs) != 1 || r.Attrs[0].Key != "n" {
		t.Errorf("Attrs = %v", r.Attrs)
	}
}

// A list attr is written unquoted with brackets: tunnels=[utun4]. It has no
// spaces, so it is one token — but a future two-element list would be quoted,
// and both must survive.
func TestListAttrs(t *testing.T) {
	r := ParseLine(`time=2026-08-21T10:15:43Z level=INFO msg=up tunnels=[utun4]`)
	if len(r.Attrs) != 1 || r.Attrs[0].Value != "[utun4]" {
		t.Errorf("Attrs = %v", r.Attrs)
	}
	r = ParseLine(`time=2026-08-21T10:15:43Z level=INFO msg=up tunnels="[utun4 utun7]"`)
	if len(r.Attrs) != 1 || r.Attrs[0].Value != "[utun4 utun7]" {
		t.Errorf("Attrs = %v", r.Attrs)
	}
}

// A line this parser does not understand is still a line worth seeing. Silently
// dropping log records is exactly the failure a log reader must not have.
func TestAnUnparseableLineIsKeptNotDropped(t *testing.T) {
	r := ParseLine("panic: runtime error: invalid memory address")
	if r.Msg != "panic: runtime error: invalid memory address" {
		t.Errorf("Msg = %q", r.Msg)
	}
	if r.Raw == "" {
		t.Error("Raw is empty")
	}
}

// An unrecognised level must not be filtered out by a warn-and-above query: a
// level this build does not know is not evidence the record is unimportant.
func TestAnUnknownLevelSortsAsInfoNotDropped(t *testing.T) {
	if Severity("TRACE") != Severity("INFO") {
		t.Error("an unknown level should rank as INFO")
	}
	if Severity("ERROR") <= Severity("WARN") || Severity("WARN") <= Severity("INFO") {
		t.Error("severity ordering is wrong")
	}
}

func writeLog(t *testing.T, path string, lines ...string) {
	t.Helper()
	var body string
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadFiltersByLevelAndKeepsTheMostRecent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.log")
	writeLog(t, path,
		`time=2026-08-21T10:00:00Z level=INFO msg=one`,
		`time=2026-08-21T10:00:01Z level=WARN msg=two`,
		`time=2026-08-21T10:00:02Z level=ERROR msg=three`,
		`time=2026-08-21T10:00:03Z level=WARN msg=four`,
	)

	recs, err := Read(path, Options{MinLevel: "WARN"})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3 (INFO filtered out)", len(recs))
	}
	if recs[0].Msg != "two" || recs[2].Msg != "four" {
		t.Errorf("records are not oldest-first: %v", recs)
	}

	recs, err = Read(path, Options{MinLevel: "WARN", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].Msg != "three" || recs[1].Msg != "four" {
		t.Errorf("Limit should keep the MOST RECENT: %v", recs)
	}
}

// The interesting failure is often the one that pushed the file over its
// rotation threshold, so the archives are read too — oldest first.
func TestRotatedArchivesAreReadOldestFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.log")
	writeLog(t, path+".2", `time=2026-08-21T09:00:00Z level=ERROR msg=oldest`)
	writeLog(t, path+".1", `time=2026-08-21T09:30:00Z level=ERROR msg=middle`)
	writeLog(t, path, `time=2026-08-21T10:00:00Z level=ERROR msg=newest`)

	recs, err := Read(path, Options{MinLevel: "ERROR"})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, r := range recs {
		got = append(got, r.Msg)
	}
	if len(got) != 3 || got[0] != "oldest" || got[1] != "middle" || got[2] != "newest" {
		t.Errorf("order = %v", got)
	}
}

// A daemon that has never run has no log. That is an ordinary state for every
// surface that calls this, not an error to report.
func TestAMissingLogIsEmptyNotAnError(t *testing.T) {
	recs, err := Read(filepath.Join(t.TempDir(), "nope.log"), Options{})
	if err != nil || len(recs) != 0 {
		t.Errorf("recs=%v err=%v", recs, err)
	}
}

func TestSinceDropsOlderRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.log")
	writeLog(t, path,
		`time=2026-08-21T10:00:00Z level=ERROR msg=old`,
		`time=2026-08-21T12:00:00Z level=ERROR msg=new`,
	)
	cutoff := time.Date(2026, 8, 21, 11, 0, 0, 0, time.UTC)
	recs, err := Read(path, Options{Since: cutoff})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Msg != "new" {
		t.Errorf("recs = %v", recs)
	}
}

// Read walks exactly the archive chain the writer keeps.
//
// The count used to be a literal 2 here and a separate literal in
// internal/logging. Nothing tied them together, so growing the writer's chain
// would have left this reader silently dropping the oldest records — the one
// failure this package is built not to have. Reading it from logging.FileBackups
// is what makes that impossible; this pins it in both directions.
func TestReadCoversExactlyTheWritersArchiveChain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.log")

	write := func(name, msg string) {
		t.Helper()
		line := fmt.Sprintf(`time=2026-09-08T10:00:00.000000Z level=INFO msg=%s`+"\n", msg)
		if err := os.WriteFile(name, []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(path, "live")
	for i := 1; i <= logging.FileBackups; i++ {
		write(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("archive%d", i))
	}
	// One beyond the chain: the writer deletes this, so a reader must not invent it.
	write(fmt.Sprintf("%s.%d", path, logging.FileBackups+1), "beyond")

	recs, err := Read(path, Options{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(recs) != logging.FileBackups+1 {
		t.Fatalf("read %d records, want %d (%d archives + the live file)",
			len(recs), logging.FileBackups+1, logging.FileBackups)
	}
	for _, r := range recs {
		if r.Msg == "beyond" {
			t.Error("read an archive past the writer's chain")
		}
	}
	// Oldest archive first, live file last.
	if recs[0].Msg != fmt.Sprintf("archive%d", logging.FileBackups) {
		t.Errorf("first record = %q, want the oldest archive", recs[0].Msg)
	}
	if recs[len(recs)-1].Msg != "live" {
		t.Errorf("last record = %q, want the live file", recs[len(recs)-1].Msg)
	}
}

// A very long quoted value stays one value, with the attrs after it intact.
//
// slog quotes anything containing a space, and the reader admits lines up to
// 4 MiB, so "long" here is ordinary rather than pathological — a rendered
// ruleset in a msg reaches this size. The failure to guard against is a value
// that ends early and turns the rest of the line into garbage attrs.
func TestAVeryLongQuotedValueStaysOneValue(t *testing.T) {
	body := strings.Repeat("a b ", 64*1024) // spaces, so slog would quote it
	r := ParseLine(`time=2026-09-08T10:00:00.000000Z level=WARN msg="` + body + `" n=2`)

	if r.Msg != body {
		t.Errorf("message truncated: %d bytes, want %d", len(r.Msg), len(body))
	}
	if len(r.Attrs) != 1 || r.Attrs[0].Key != "n" || r.Attrs[0].Value != "2" {
		t.Errorf("attrs after a long value = %+v, want one n=2", r.Attrs)
	}
}

// slog writes `msg=""` for a record logged with an empty message. That is a
// record it understood, not a line it failed on, so the raw-line fallback must
// not fire — otherwise the surface shows `time=… level=WARN msg=""` where the
// message should be.
func TestAnEmptyMessageIsNotRelabelledWithTheRawLine(t *testing.T) {
	line := `time=2026-09-08T10:00:00.000000Z level=WARN msg=""`
	r := ParseLine(line)
	if r.Msg != "" {
		t.Errorf("Msg = %q, want the empty message slog actually wrote", r.Msg)
	}
	if r.Level != "WARN" {
		t.Errorf("Level = %q, want WARN", r.Level)
	}
	// A line this parser genuinely could not read still falls back.
	if got := ParseLine("panic: runtime error").Msg; got != "panic: runtime error" {
		t.Errorf("the fallback stopped working: %q", got)
	}
}

// A level this build does not recognise must survive a warn-and-above query.
//
// Severity ranks an unknown level as INFO so it can be ordered at all, and the
// filter used to read that rank as a verdict — so `--level warn` silently
// dropped every record a newer daemon, a custom slog level or a hand-edited
// line wrote. A level this build does not know is not evidence the record is
// unimportant, which is the whole reason it is not dropped at parse time.
func TestAnUnknownLevelSurvivesAWarnAndAboveQuery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.log")
	writeLog(t, path,
		`time=2026-09-08T10:00:00Z level=INFO msg=ordinary`,
		`time=2026-09-08T10:00:01Z level=TRACE msg=unfamiliar`,
		`time=2026-09-08T10:00:02Z level=ERROR msg=broken`,
	)

	recs, err := Read(path, Options{MinLevel: "warn"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var msgs []string
	for _, r := range recs {
		msgs = append(msgs, r.Msg)
	}
	if len(recs) != 2 || msgs[0] != "unfamiliar" || msgs[1] != "broken" {
		t.Fatalf("got %v, want the unknown level kept and INFO filtered out", msgs)
	}
}

// A key this parser cannot read must not take the rest of the line with it.
//
// slog quotes a key that needs quoting, and nextPair refuses such a key.
// Breaking the loop there discarded every REMAINING attr on the line, and with
// `msg` already seen the raw-line fallback could not fire either — so the
// evidence survived only in Raw, which no surface reads for a record that
// parsed. "Nothing is silently dropped" has to hold mid-line too.
func TestAnUnreadableKeyKeepsTheRestOfTheLine(t *testing.T) {
	r := ParseLine(`time=2026-09-08T10:00:00Z level=WARN msg=cut "a=b"=v mode=guard`)
	if r.Msg != "cut" {
		t.Fatalf("Msg = %q, want the message", r.Msg)
	}
	var kept string
	for _, a := range r.Attrs {
		if a.Key == UnparsedKey {
			kept = a.Value
		}
	}
	if !strings.Contains(kept, "mode=guard") {
		t.Errorf("attrs = %+v, want the unreadable tail kept including mode=guard", r.Attrs)
	}
}

// One unreadable file in the rotation chain costs only itself.
//
// Read used to return on the first error, so a permission problem on the
// OLDEST archive threw away the live file with it — the most recent and most
// useful records lost to the least important one.
func TestAnUnreadableArchiveDoesNotCostTheLiveFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.log")
	writeLog(t, path, `time=2026-09-08T10:00:00Z level=ERROR msg=live`)
	blocked := fmt.Sprintf("%s.%d", path, logging.FileBackups)
	writeLog(t, blocked, `time=2026-09-08T09:00:00Z level=ERROR msg=oldest`)
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o644) })
	if f, err := os.Open(blocked); err == nil { // running as root: the test cannot bite
		f.Close()
		t.Skip("cannot make a file unreadable as this user")
	}

	recs, err := Read(path, Options{})
	if err == nil {
		t.Error("an unreadable archive must still be reported")
	}
	if len(recs) != 1 || recs[0].Msg != "live" {
		t.Fatalf("got %+v, want the live file's record kept alongside the error", recs)
	}
}

// A line past the scanner's cap costs that line, not the file.
//
// sc.Err() used to discard every record already parsed, so one pathological
// line took the whole history with it and `dezhban logs` printed nothing.
func TestALineOverTheCapKeepsTheRecordsBeforeIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.log")
	huge := strings.Repeat("x", 5*1024*1024) // over the 4 MiB cap
	writeLog(t, path,
		`time=2026-09-08T10:00:00Z level=ERROR msg=before`,
		`time=2026-09-08T10:00:01Z level=ERROR msg=`+huge,
	)

	recs, err := Read(path, Options{})
	if err == nil {
		t.Error("a line past the cap must still be reported")
	}
	if len(recs) != 1 || recs[0].Msg != "before" {
		t.Fatalf("got %d records, want the one parsed before the long line", len(recs))
	}
}

// A line with no `level=` at all must survive a warn-and-above query.
//
// ParseLine defaulted such a record to the literal "INFO", which Known() then
// calls recognised, so the filter dropped it — and the lines with no level are
// raw panics and stack traces, exactly the records "Recent problems" exists to
// show. Ranking empty as INFO for ORDERING must not become a verdict.
func TestALineWithNoLevelSurvivesAWarnAndAboveQuery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.log")
	writeLog(t, path,
		`time=2026-09-08T10:00:00Z level=INFO msg=ordinary`,
		`panic: runtime error: invalid memory address`,
		`time=2026-09-08T10:00:02Z level=ERROR msg=broken`,
	)

	recs, err := Read(path, Options{MinLevel: "warn"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var msgs []string
	for _, r := range recs {
		msgs = append(msgs, r.Msg)
	}
	if len(recs) != 2 || !strings.HasPrefix(msgs[0], "panic:") || msgs[1] != "broken" {
		t.Fatalf("got %v, want the panic line kept and the INFO record filtered out", msgs)
	}
}
