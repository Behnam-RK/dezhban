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
package logread

import (
	"bufio"
	"errors"
	"fmt"
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
		// Let strconv find the closing quote so escapes inside the value are
		// handled by the same code that wrote them.
		for i := 1; i <= len(s); i++ {
			if v, err := strconv.Unquote(s[:i]); err == nil {
				return key, v, s[i:], true
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
	sc := bufio.NewScanner(f)
	// A stack trace or a long attr can exceed bufio's 64KiB default, and a
	// scanner that stops mid-file would silently truncate the history.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		r := ParseLine(line)
		// Only a level this build RECOGNISES may be filtered out. A level it
		// does not know is not evidence the record is unimportant — a newer
		// daemon, a custom slog level, a hand-edited line — and ranking it as
		// INFO for ordering must not become a warn-and-above query silently
		// swallowing it.
		if Known(r.Level) && Severity(r.Level) < min {
			continue
		}
		if !opt.Since.IsZero() && !r.Time.IsZero() && r.Time.Before(opt.Since) {
			continue
		}
		out = append(out, r)
	}
	// `out`, not nil: a single line past the 4 MiB cap used to cost every record
	// already parsed from this file, so one pathological line took the whole
	// history with it. The error still travels — it is the caller's to report —
	// but it no longer erases what was readable.
	//
	// What survives is the records BEFORE the oversized line, and only those: a
	// bufio.Scanner cannot resume past ErrTooLong, so the rest of that file is
	// still lost. Recovering it needs a reader loop that consumes through the
	// long line's newline, which is a bigger change than the one this comment
	// used to claim to have made.
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	return out, nil
}
