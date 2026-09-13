// Package logread parses the daemon's own log file back into records, so a
// surface can show what went wrong without a person opening a root-owned
// directory and reading slog output by eye.
//
// The daemon writes `slog`'s text format to a size-rotated file (see
// internal/logging): `time=... level=WARN msg="..." key=value ...`. That format
// is defined in this repo, so parsing it belongs here rather than in the macOS
// app — a second parser in Swift would be a second thing to get wrong about
// quoting, and it could not be tested against the writer.
//
// Read-only and unprivileged by design: the log is 0644 precisely so the GUI and
// an ordinary operator can read history without root.
//
// Nothing readable is ever dropped in silence. A line the parser cannot break
// into pairs keeps its tail under UnparsedKey; a line with no level at all
// survives a warn-and-above query; a line too long to hold is skipped and leaves
// a record saying so (OversizedKey) where it was; and a file that cannot be read
// costs only itself, its error travelling back beside the records from the rest
// of the chain. A surface that shows fewer records than the log holds, without
// saying so, is the one failure this package is built not to have.
package logread

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/behnam-rk/dezhban/internal/logging"
)

// Record is one parsed log line.
type Record struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
	// Attrs are the record's key=value pairs in the order written, excluding
	// time/level/msg. Kept as pairs rather than a map so the order the daemon
	// chose survives — it reads as a sentence, and a map would shuffle it.
	Attrs []Attr `json:"attrs,omitempty"`
	// Raw is the original line, so a surface can always show exactly what was
	// written even when this parser did not understand all of it.
	Raw string `json:"raw"`
}

// Attr is one key=value pair from a record.
type Attr struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Severity ranks a level for filtering. An unrecognised level sorts as INFO
// rather than being dropped: a record whose level this does not know is still a
// record, and silently discarding log lines is exactly the failure a log reader
// must not have.
func Severity(level string) int {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "DEBUG":
		return 0
	case "WARN", "WARNING":
		return 2
	case "ERROR":
		return 3
	default:
		return 1 // INFO, and anything unrecognised
	}
}

// Known reports whether Severity recognised the level rather than defaulting it
// to INFO. Ranking an unknown level is not the same as judging it, and only the
// filter needs the difference: giving TRACE the INFO rank and then applying a
// warn-and-above threshold to it drops the record, which is precisely what
// Severity's comment promises does not happen.
func Known(level string) bool {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "DEBUG", "INFO", "WARN", "WARNING", "ERROR":
		return true
	}
	return false
}

// ParseLine parses one slog text line. It never fails: a line it cannot make
// sense of comes back with Raw set and Msg holding the whole line, because a
// malformed line in a diagnostic log is itself worth seeing.
func ParseLine(line string) Record {
	// Level stays EMPTY until a level= is actually seen. Defaulting it to the
	// literal "INFO" made Known() call it recognised, so a warn-and-above query
	// dropped it — and the lines with no level= are raw panics and stack traces,
	// exactly the records "Recent problems" exists to show. Severity("") already
	// ranks empty as INFO for ordering; what must not follow from that ranking
	// is a verdict.
	r := Record{Raw: line}
	// Whether a msg key was SEEN, not whether it is non-empty: slog writes
	// `msg=""` for a record logged with an empty message, and testing the value
	// would re-label that record with its own raw line as the message — so the
	// surface shows `time=… level=WARN msg=""` where the message should be.
	// Falling back is for a line this parser did not understand at all.
	sawMsg := false
	rest := line
	for {
		key, value, remainder, ok := nextPair(rest)
		if !ok {
			break
		}
		rest = remainder
		switch key {
		case "time":
			if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
				r.Time = t
			}
		case "level":
			r.Level = value
		case "msg":
			r.Msg, sawMsg = value, true
		default:
			r.Attrs = append(r.Attrs, Attr{Key: key, Value: value})
		}
	}
	// Whatever the pair loop could not read is KEPT, not dropped. slog quotes a
	// key that needs quoting (`"a=b"=v`), nextPair refuses such a key, and
	// breaking there discarded every REMAINING attr on the line rather than the
	// one it choked on — with msg already seen, the raw-line fallback below
	// could not fire either, so the evidence left in Raw and nowhere a surface
	// reads. "Nothing is silently dropped" has to hold mid-line too.
	if leftover := strings.TrimSpace(rest); leftover != "" && (sawMsg || len(r.Attrs) > 0) {
		r.Attrs = append(r.Attrs, Attr{Key: UnparsedKey, Value: leftover})
	}
	if !sawMsg && len(r.Attrs) == 0 {
		r.Msg = strings.TrimSpace(line)
	}
	return r
}

// UnparsedKey is the attr key ParseLine uses for the tail of a line it could
// not read as key=value pairs. Named rather than anonymous so a surface can
// tell dezhban's own words from the daemon's.
const UnparsedKey = "logread.unparsed"

// OversizedKey is the attr key on the record readFile puts in place of a line
// past maxLineBytes; its value is that line's length in bytes, not counting the
// line ending. A sibling of UnparsedKey, named for the same reason — a surface
// can tell dezhban's own words from the daemon's, and a caller counting gaps has
// something exact to match on rather than the English in Msg.
const OversizedKey = "logread.oversized"

// maxLineBytes caps one log line. A stack trace, or a rendered ruleset inside a
// msg, runs far past bufio's 64 KiB default, so the cap is generous; what it
// protects is memory, since a reader that held whatever the file happened to
// contain could be made to hold the whole file.
//
// A line longer than this is SKIPPED and reported in its place — never silently
// dropped, and never held. See readFile and oversizedRecord.
//
// Named rather than written as a literal, for the reason logging.FileBackups is
// exported and internal/vpnimport names maxConfigLine: the number is stated in
// this package's prose, in its tests, and in docs/usage/cli.md, and a limit that
// lives in four places is a limit that drifts. Changing it means changing that
// doc too.
const maxLineBytes = 4 << 20 // 4 MiB

// lineBufBytes is the window the reader fills per read, NOT a limit: a longer
// line is assembled from as many windows as it takes, and one past maxLineBytes
// is drained through this window without being kept. It is the size the
// bufio.Scanner this replaced started at, so the ordinary path costs what it did.
const lineBufBytes = 64 << 10 // 64 KiB

// oversizedMsg is what a skipped line says for itself. Prose, because the macOS
// pane renders it as the row's primary text to someone who is not a developer:
// it has to name the cause and the consequence in one line.
const oversizedMsg = "log line too long to read; skipped"

// oversizedRecord stands in for a line readFile would not hold, carrying how many
// bytes went and what they were measured against.
//
// Raw is a real slog line, not a summary, because `dezhban logs` in text mode and
// the bundle's log.txt print Raw and NOTHING else — a record with an empty Raw
// prints a blank line exactly where the explanation belongs. Writing it in slog's
// own grammar keeps log.txt homogeneous and keeps it readable by the parser that
// produced it: ParseLine(Raw) reconstructs this record field for field.
//
// WARN, not ERROR: dezhban did not fail, one line was too big to read, and
// escalating a reading limit to the loudest row on a diagnostics pane misdirects.
// Not an empty level either — Known("") is false, so no filter could ever drop it
// and `--level error` would show it, which is not what a gap deserves.
//
// The zero Time is the honest one: the `time=` was inside the bytes that went. A
// guessed "now" would sort a gap against real timestamps and let --since include
// it on a fabrication. It also means the Since filter, which tests
// !r.Time.IsZero(), always lets a marker through — right, because a gap that may
// hide in-window records must not itself be hidden by the window.
func oversizedRecord(n int64) Record {
	size, limit := strconv.FormatInt(n, 10), strconv.Itoa(maxLineBytes)
	return Record{
		Level: "WARN",
		Msg:   oversizedMsg,
		Attrs: []Attr{{Key: OversizedKey, Value: size}, {Key: "limit", Value: limit}},
		Raw: fmt.Sprintf("level=WARN msg=%s %s=%s limit=%s",
			strconv.Quote(oversizedMsg), OversizedKey, size, limit),
	}
}

// nextPair pulls one key=value off the front of s, honouring slog's quoting:
// a value containing a space, a quote, or an equals sign is written as a Go
// quoted string. Without that, `msg="rules missing, re-applied" n=2` would parse
// as a msg of `"rules` and two garbage attrs.
func nextPair(s string) (key, value, rest string, ok bool) {
	s = strings.TrimLeft(s, " ")
	if s == "" {
		return "", "", "", false
	}
	eq := strings.IndexByte(s, '=')
	if eq < 0 {
		return "", "", "", false
	}
	key = s[:eq]
	if strings.ContainsAny(key, " \"") {
		return "", "", "", false
	}
	s = s[eq+1:]
	if strings.HasPrefix(s, `"`) {
		// QuotedPrefix scans for the closing quote ONCE. Trying Unquote on every
		// prefix did the same work O(n) times, and the reader admits lines up to
		// maxLineBytes, so a long quoted value full of escaped quotes made `dezhban
		// logs` reparse the same megabyte over and over. Same decoder either way,
		// so escapes are still handled by the code that wrote them.
		if q, err := strconv.QuotedPrefix(s); err == nil {
			if v, uerr := strconv.Unquote(q); uerr == nil {
				return key, v, s[len(q):], true
			}
		}
		// Unterminated quote: take the remainder verbatim rather than dropping
		// the line.
		return key, s, "", true
	}
	end := strings.IndexByte(s, ' ')
	if end < 0 {
		return key, s, "", true
	}
	return key, s[:end], s[end:], true
}

// Options selects which records Read returns.
type Options struct {
	// MinLevel drops anything less severe. "" means everything.
	MinLevel string
	// Limit caps the result to the most recent N. <=0 means no cap.
	Limit int
	// Since drops anything older. Zero means no cutoff.
	Since time.Time
}

// Read returns matching records from the log file and its rotated archives,
// oldest first.
//
// The archives are read too, because the interesting failure is often the one
// that pushed the file over its rotation threshold. A missing file is an empty
// result, not an error: a daemon that has never run has no log, and that is an
// ordinary state for the surfaces that call this.
//
// A non-nil error and a non-empty slice arrive TOGETHER when part of the chain
// could not be read. Callers must report the error and still use the records —
// the whole point is that one unreadable file costs only itself.
//
// A line past the reader's size cap is NOT one of those cases: it is skipped, a
// record marking the gap takes its place, and the read is not an error. Nothing
// before or after such a line is lost.
func Read(path string, opt Options) ([]Record, error) {
	var all []Record
	var problems []string
	// Every file is read, and one that fails costs only itself. Returning early
	// meant a permission problem on the OLDEST archive threw away the live file
	// with it — the most recent and most useful records lost to the least
	// important one. The error still travels back beside the records, so a
	// caller can say what it could not read without pretending it read nothing.
	read := func(p string) {
		recs, err := readFile(p, opt)
		all = append(all, recs...)
		if err != nil {
			problems = append(problems, err.Error())
		}
	}
	// Oldest archive first, live file last, so the result reads forward in time.
	// The chain length comes from the WRITER's constant, not a literal matching
	// it by coincidence: growing the writer's chain must not leave this reader
	// silently dropping the oldest records.
	for i := logging.FileBackups; i >= 1; i-- {
		read(fmt.Sprintf("%s.%d", path, i))
	}
	read(path)

	if opt.Limit > 0 && len(all) > opt.Limit {
		all = all[len(all)-opt.Limit:]
	}
	if len(problems) > 0 {
		return all, errors.New(strings.Join(problems, "; "))
	}
	return all, nil
}

func readFile(path string, opt Options) ([]Record, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	defer f.Close()

	min := Severity(opt.MinLevel)
	if strings.TrimSpace(opt.MinLevel) == "" {
		min = -1
	}

	var out []Record
	// One gate for every record, synthesised or parsed. A marker that skipped the
	// filters would answer a different question than the one asked, and a marker
	// the filters could not see would be a record this package had decided was
	// exempt from the caller's query.
	keep := func(r Record) {
		// Only a level this build RECOGNISES may be filtered out. A level it
		// does not know is not evidence the record is unimportant — a newer
		// daemon, a custom slog level, a hand-edited line — and ranking it as
		// INFO for ordering must not become a warn-and-above query silently
		// swallowing it.
		if Known(r.Level) && Severity(r.Level) < min {
			return
		}
		if !opt.Since.IsZero() && !r.Time.IsZero() && r.Time.Before(opt.Since) {
			return
		}
		out = append(out, r)
	}

	// A bufio.Reader, NOT a Scanner. A Scanner cannot resume past ErrTooLong, so
	// one line longer than the cap ended the scan and took every record after it
	// in that file with it. ReadLine hands a long line back in PIECES, which is
	// what makes it possible to walk past one without ever holding it.
	br := bufio.NewReaderSize(f, lineBufBytes)
	var (
		line    []byte // the line being assembled
		dropped int64  // length of an over-cap line being drained; 0 when not draining
		readErr error
	)
	for {
		frag, isPrefix, err := br.ReadLine()
		// frag points INTO br's buffer and dies at the next read, so every byte
		// kept is copied here and now.
		switch {
		case dropped > 0:
			// Already past the cap: count the rest of the line, keep none of it.
			dropped += int64(len(frag))
		case len(line)+len(frag) > maxLineBytes:
			// This piece crosses the cap. Release what was held — the line is not
			// coming back, and holding it in order to describe it is the
			// allocation the cap exists to refuse.
			dropped, line = int64(len(line)+len(frag)), nil
		default:
			line = append(line, frag...)
		}
		if isPrefix {
			continue
		}
		// A whole line. ReadLine reports isPrefix false for the LAST piece of a
		// long line and for a final line the file left without a newline, so both
		// arrive here — and this emit has to happen BEFORE the break below, or a
		// file ending mid-line loses its last record to the io.EOF.
		if dropped > 0 {
			keep(oversizedRecord(dropped))
		} else if s := strings.TrimSuffix(string(line), "\r"); strings.TrimSpace(s) != "" {
			// ScanLines dropped a trailing \r from every line INCLUDING a final
			// one with no newline; ReadLine drops it only before a newline. Same
			// line either way, so Raw stays what it has always been.
			keep(ParseLine(s))
		}
		line, dropped = line[:0], 0
		if err != nil {
			// io.EOF is the end, not a problem — the io.Reader contract makes it
			// that exact value and bufio does not wrap it. Anything else is a
			// genuine read failure and travels back BESIDE the records already
			// parsed, exactly as a file that cannot be opened does.
			if err != io.EOF {
				readErr = err
			}
			break
		}
	}
	// `out`, not nil — and no error for a line this reader chose to skip. Reading
	// SUCCEEDED: the file was walked to its end, one line was not a record, and
	// the record standing in its place says so where it happened.
	//
	// That is the whole of issue #64. A single line past the cap used to cost
	// every record after it in the same file, because a Scanner stops at
	// ErrTooLong and cannot be restarted. The error path that remains is the one
	// it was built for: a file that could not be opened, or could not be read at
	// all — see TestAnUnreadableArchiveDoesNotCostTheLiveFile, which is now the
	// only test holding it open.
	if readErr != nil {
		return out, fmt.Errorf("read %s: %w", filepath.Base(path), readErr)
	}
	return out, nil
}
