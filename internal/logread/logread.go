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
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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

// ParseLine parses one slog text line. It never fails: a line it cannot make
// sense of comes back with Raw set and Msg holding the whole line, because a
// malformed line in a diagnostic log is itself worth seeing.
func ParseLine(line string) Record {
	r := Record{Raw: line, Level: "INFO"}
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
			r.Msg = value
		default:
			r.Attrs = append(r.Attrs, Attr{Key: key, Value: value})
		}
	}
	if r.Msg == "" && len(r.Attrs) == 0 {
		r.Msg = strings.TrimSpace(line)
	}
	return r
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
func Read(path string, opt Options) ([]Record, error) {
	var all []Record
	// Oldest archive first, live file last, so the result reads forward in time.
	for i := 2; i >= 1; i-- {
		recs, err := readFile(fmt.Sprintf("%s.%d", path, i), opt)
		if err != nil {
			return nil, err
		}
		all = append(all, recs...)
	}
	recs, err := readFile(path, opt)
	if err != nil {
		return nil, err
	}
	all = append(all, recs...)

	if opt.Limit > 0 && len(all) > opt.Limit {
		all = all[len(all)-opt.Limit:]
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
		if Severity(r.Level) < min {
			continue
		}
		if !opt.Since.IsZero() && !r.Time.IsZero() && r.Time.Before(opt.Since) {
			continue
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	return out, nil
}
