package logread

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
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

// writeRaw writes body verbatim — no trailing newline added. Its whole purpose is
// the file that ends mid-line, which writeLog cannot produce and which is where a
// hand-rolled read loop is easiest to get wrong.
func writeRaw(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
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

// A line past the cap costs THAT LINE, and nothing else in the file.
//
// Two things used to go wrong here, and this pins both. A bufio.Scanner stops at
// ErrTooLong and cannot be restarted, so the records AFTER a long line were lost
// along with it — the `after` assertion is issue #64. And the failed read was
// reported as an error, which said the log could not be read when in fact it had
// been read to the end; that is why `err == nil` below is the reverse of what the
// earlier version of this test asserted.
func TestALineOverTheCapCostsThatLineAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.log")
	writeLog(t, path,
		`time=2026-09-08T10:00:00Z level=ERROR msg=before`,
		`time=2026-09-08T10:00:01Z level=ERROR msg=`+strings.Repeat("x", maxLineBytes+1),
		`time=2026-09-08T10:00:02Z level=ERROR msg=after`,
	)

	recs, err := Read(path, Options{})
	if err != nil {
		t.Errorf("err = %v; the file was read to its end, so this is not a partial read", err)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records, want before + the marker + after: %+v", len(recs), recs)
	}
	if recs[0].Msg != "before" || recs[2].Msg != "after" {
		t.Errorf("got %q and %q, want the records either side of the long line", recs[0].Msg, recs[2].Msg)
	}
	if recs[1].Msg != oversizedMsg {
		t.Errorf("recs[1] = %+v, want the marker in the long line's place", recs[1])
	}
}

// The marker is a record like any other, and every surface has to be able to
// render it: `dezhban logs` and the bundle's log.txt print Raw and NOTHING else,
// the macOS pane reads Msg and Attrs, and log.txt is re-parseable by the code that
// wrote it.
func TestASkippedLineLeavesAMarkerRecordInItsPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.log")
	huge := strings.Repeat("x", maxLineBytes+7)
	writeLog(t, path, huge)

	recs, err := Read(path, Options{})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want just the marker: %+v", len(recs), recs)
	}
	r := recs[0]
	if r.Level != "WARN" {
		t.Errorf("level = %q, want WARN so --level warn shows it and --level error does not", r.Level)
	}
	if !r.Time.IsZero() {
		t.Errorf("time = %v, want the zero time — the timestamp was inside the bytes that went", r.Time)
	}
	if strings.TrimSpace(r.Raw) == "" {
		t.Error("Raw is blank; `dezhban logs` and log.txt print Raw and nothing else, so this prints an empty line")
	}
	var size string
	for _, a := range r.Attrs {
		if a.Key == OversizedKey {
			size = a.Value
		}
	}
	if want := strconv.Itoa(len(huge)); size != want {
		t.Errorf("%s = %q, want %q — the length of the line that was skipped", OversizedKey, size, want)
	}
	// log.txt is read back by the same parser that produced it.
	if got := ParseLine(r.Raw); !reflect.DeepEqual(got, r) {
		t.Errorf("ParseLine(Raw) did not round-trip:\n got %+v\nwant %+v", got, r)
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

// A file that ends mid-long-line still ends cleanly. ReadLine reports isPrefix
// false for the last piece of a long line whether a newline follows it or not, so
// the marker has to be emitted before the io.EOF ends the loop.
func TestALongLastLineWithNoTrailingNewlineIsStillSkippedCleanly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.log")
	writeRaw(t, path, "time=2026-09-08T10:00:00Z level=ERROR msg=before\n"+
		strings.Repeat("x", maxLineBytes+1))

	recs, err := Read(path, Options{})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if len(recs) != 2 || recs[0].Msg != "before" || recs[1].Msg != oversizedMsg {
		t.Fatalf("got %+v, want the record then the marker", recs)
	}
}

// A final line the writer left without a newline is still a record.
//
// Unlike its siblings this one PASSES against the code it guards — bufio.Scanner
// handled it for free. It is here because the hand-rolled reader that replaced the
// Scanner is exactly where that free behaviour is easiest to lose: emit the record
// after the io.EOF check rather than before it, and this is the only thing that
// notices.
func TestALastLineWithNoTrailingNewlineIsStillRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.log")
	writeRaw(t, path, "time=2026-09-08T10:00:00Z level=ERROR msg=first\n"+
		"time=2026-09-08T10:00:01Z level=ERROR msg=unterminated")

	recs, err := Read(path, Options{})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if len(recs) != 2 || recs[1].Msg != "unterminated" {
		t.Fatalf("got %+v, want the unterminated final line kept", recs)
	}
}

// Each long line is counted on its own. A drain counter that did not reset would
// report the second gap as the sum of both.
func TestTwoLongLinesInOneFileEachLeaveTheirOwnMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.log")
	first, second := strings.Repeat("x", maxLineBytes+1), strings.Repeat("y", maxLineBytes+9)
	writeLog(t, path,
		`time=2026-09-08T10:00:00Z level=ERROR msg=before`,
		first,
		`time=2026-09-08T10:00:01Z level=ERROR msg=middle`,
		second,
		`time=2026-09-08T10:00:02Z level=ERROR msg=after`,
	)

	recs, err := Read(path, Options{})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if len(recs) != 5 {
		t.Fatalf("got %d records, want three real ones and two markers: %+v", len(recs), recs)
	}
	for i, want := range []string{"before", oversizedMsg, "middle", oversizedMsg, "after"} {
		if recs[i].Msg != want {
			t.Errorf("recs[%d].Msg = %q, want %q", i, recs[i].Msg, want)
		}
	}
	if a, b := sizeAttr(t, recs[1]), sizeAttr(t, recs[3]); a != len(first) || b != len(second) {
		t.Errorf("marker sizes = %d and %d, want %d and %d — each marker counts its OWN line",
			a, b, len(first), len(second))
	}
}

// sizeAttr is the byte count a marker record carries.
func sizeAttr(t *testing.T, r Record) int {
	t.Helper()
	for _, a := range r.Attrs {
		if a.Key == OversizedKey {
			n, err := strconv.Atoi(a.Value)
			if err != nil {
				t.Fatalf("%s = %q: %v", OversizedKey, a.Value, err)
			}
			return n
		}
	}
	t.Fatalf("no %s attr on %+v", OversizedKey, r)
	return 0
}

// The rotation analogue of TestAnUnreadableArchiveDoesNotCostTheLiveFile: a long
// line in an archive costs neither that archive's own tail nor the live file.
func TestALongLineInAnArchiveDoesNotCostTheLiveFileOrItsOwnTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.log")
	writeLog(t, path+".1",
		`time=2026-09-08T09:00:00Z level=ERROR msg=archived-before`,
		strings.Repeat("x", maxLineBytes+1),
		`time=2026-09-08T09:00:01Z level=ERROR msg=archived-after`,
	)
	writeLog(t, path, `time=2026-09-08T10:00:00Z level=ERROR msg=live`)

	recs, err := Read(path, Options{})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	var got []string
	for _, r := range recs {
		got = append(got, r.Msg)
	}
	want := []string{"archived-before", oversizedMsg, "archived-after", "live"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v — oldest first, with the gap in its place", got, want)
	}
}

// The cap is INCLUSIVE. bufio.Scanner errored when its buffer was full at max, so
// the old true maximum was one byte below the number everything else stated; the
// reader admits exactly maxLineBytes. Pinned so the shift is deliberate.
func TestALineExactlyAtTheCapIsStillARecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.log")
	const prefix = `time=2026-09-08T10:00:00Z level=ERROR msg=`
	writeLog(t, path, prefix+strings.Repeat("x", maxLineBytes-len(prefix)))

	recs, err := Read(path, Options{})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if len(recs) != 1 || recs[0].Msg == oversizedMsg {
		t.Fatalf("got %+v, want a line exactly at the cap read as a record", recs)
	}
}

// The marker goes THROUGH the level filter, not around it — so a warn-and-above
// query shows the gap and an errors-only query does not.
//
// The cost is stated rather than hidden: the skipped line might itself have been
// an ERROR. Its bytes are gone, so claiming so would rank a guess against real
// records; docs/usage/cli.md says to ask for warn when you want to see gaps.
func TestTheSkippedLineMarkerAnswersAWarnQueryButNotAnErrorQuery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.log")
	writeLog(t, path,
		`time=2026-09-08T10:00:00Z level=ERROR msg=before`,
		strings.Repeat("x", maxLineBytes+1),
		`time=2026-09-08T10:00:01Z level=ERROR msg=after`,
	)

	warn, err := Read(path, Options{MinLevel: "warn"})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if len(warn) != 3 || warn[1].Msg != oversizedMsg {
		t.Errorf("warn query = %+v, want the gap shown", warn)
	}

	errs, err := Read(path, Options{MinLevel: "error"})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if len(errs) != 2 {
		t.Fatalf("error query = %+v, want only the two ERROR records", errs)
	}
	for _, r := range errs {
		if r.Msg == oversizedMsg {
			t.Error("the marker is a WARN and must not answer an errors-only query")
		}
	}
}

// A marker survives a time window it has no timestamp for. The gap may hide
// in-window records, so hiding the gap itself because its time is unknown would
// answer the query with a silence it cannot justify.
func TestTheSkippedLineMarkerSurvivesASinceQuery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.log")
	writeLog(t, path,
		`time=2020-01-01T00:00:00Z level=ERROR msg=ancient`,
		strings.Repeat("x", maxLineBytes+1),
		`time=2026-09-08T10:00:01Z level=ERROR msg=recent`,
	)

	recs, err := Read(path, Options{Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if len(recs) != 2 || recs[0].Msg != oversizedMsg || recs[1].Msg != "recent" {
		t.Fatalf("got %+v, want the marker and the in-window record", recs)
	}
}

// Draining a long line costs the cap, not the line.
//
// The claim is not a byte count, it is that cost does not TRACK line length: a
// reader that buffered the line would scale with it, and this one does not. So the
// test compares two reads whose lines differ by 16x and asserts the allocation
// barely moves — which a buffering design cannot satisfy at any threshold.
//
// TotalAlloc, not peak RSS: cumulative is what is stable enough to assert in CI.
// Measured, it is ~20 MiB either way — five times the cap, because append's growth
// factor for large slices is about 1.25 and the intermediate copies add up. Flat is
// the property worth having; the constant factor is transient garbage on a
// pathological line, and buying it down would mean hand-rolling slice growth.
//
// The record assertions are what make this a fix-pin. The allocation comparison
// alone would also pass against the bufio.Scanner this replaced, which allocated to
// the cap and then gave up.
func TestDrainingALongLineCostsTheCapNotTheLine(t *testing.T) {
	read := func(lineLen int) (uint64, []Record) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "dezhban.log")
		writeRaw(t, path, "time=2026-09-08T10:00:00Z level=ERROR msg=before\n"+
			strings.Repeat("x", lineLen)+"\ntime=2026-09-08T10:00:01Z level=ERROR msg=after\n")
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		recs, err := Read(path, Options{})
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatalf("err = %v, want none", err)
		}
		return after.TotalAlloc - before.TotalAlloc, recs
	}

	small, recs := read(maxLineBytes + 1)
	if len(recs) != 3 || recs[0].Msg != "before" || recs[2].Msg != "after" {
		t.Fatalf("got %+v, want the records either side of the long line", recs)
	}
	large, recs := read(16 * maxLineBytes)
	if len(recs) != 3 || recs[0].Msg != "before" || recs[2].Msg != "after" {
		t.Fatalf("got %+v, want the records either side of the long line", recs)
	}

	// 16x the line for no more than 2x the allocation. A design that held the
	// line would need 16x.
	if large > 2*small {
		t.Errorf("a 16x longer line cost %d bytes against %d — allocation is tracking line length",
			large, small)
	}
}

// maxLineBytes' doc comment says the number is stated in docs/usage/cli.md, and
// that a change to it means changing the doc too. This makes the claim checkable
// instead of hopeful.
//
// It exists because the claim was FALSE when it was written: the doc edit was
// dropped on the way in and nothing noticed, because prose is not compiled and the
// gate has nothing to say about it. A comment that names a file is a promise about
// that file, and the cheapest way to keep a promise is to fail without it.
func TestTheDocumentedLineCapMatchesTheCode(t *testing.T) {
	const doc = "../../docs/usage/cli.md"
	body, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	// The doc states the cap the way a reader says it, not the way Go writes it.
	want := strconv.Itoa(maxLineBytes>>20) + " MiB"
	if !strings.Contains(string(body), want) {
		t.Errorf("%s does not state the per-line cap as %q; maxLineBytes is %d and its comment says this doc names it",
			doc, want, maxLineBytes)
	}
}
