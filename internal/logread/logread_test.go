package logread

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
