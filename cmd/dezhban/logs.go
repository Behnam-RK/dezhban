package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/behnam-rk/dezhban/internal/logread"
)

// cmdLogs prints recent records from dezhban's own log file.
//
// The daemon's log is the one place a problem that has already passed is still
// visible, and until now reading it meant knowing where it lived
// (<state dir>/logs/dezhban.log) and reading slog output by eye. The file is
// deliberately 0644 — the same call state.json makes — so this needs no root.
//
// Read-only: no firewall effects, no config writes, nothing started or stopped.
func cmdLogs(args []string) int {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	level := fs.String("level", "", "minimum level: debug, info, warn, error (default: all)")
	limit := fs.Int("limit", 200, "keep at most this many of the most recent records (0: no limit)")
	since := fs.Duration("since", 0, "only records newer than this (e.g. 1h)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	_ = fs.Parse(args)

	// Reject a level this command does not know, rather than letting it fall
	// through logread.Severity's "anything unrecognised sorts as INFO" default.
	// That default is right where it lives — an unknown level on a RECORD must
	// not be a reason to drop the record — but applied to the QUERY it silently
	// answers a different question than the one asked, and then prints
	// "no oopz-or-worse records" as though the filter had been honoured.
	if v := strings.ToLower(strings.TrimSpace(*level)); v != "" {
		switch v {
		case "debug", "info", "warn", "warning", "error":
		default:
			fmt.Fprintf(os.Stderr, "unknown level %q: use debug, info, warn, or error\n", *level)
			return 2
		}
	}

	opt := logread.Options{MinLevel: *level, Limit: *limit}
	if *since > 0 {
		opt.Since = time.Now().Add(-*since)
	}

	path := defaultLogPath()
	// Read hands back an error and records TOGETHER when part of the chain was
	// unreadable. Say what could not be read, then print what could: a
	// permission problem on the oldest archive must not cost the live file.
	recs, err := logread.Read(path, opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: part of the log could not be read:", err)
		if len(recs) == 0 {
			return 1
		}
	}

	if *asJSON {
		// An empty result encodes as [], never null: a consumer must not have to
		// tell those apart to answer "were there any problems?".
		if recs == nil {
			recs = []logread.Record{}
		}
		out, err := json.MarshalIndent(recs, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "encode failed:", err)
			return 1
		}
		fmt.Println(string(out))
		return 0
	}

	if len(recs) == 0 {
		// Exit 0. "Nothing matched" is an answer — often the good one, when the
		// query was for errors — and must not look like a failure to read.
		if strings.TrimSpace(*level) != "" {
			fmt.Fprintf(os.Stderr, "no %s-or-worse records in %s.\n", strings.ToLower(*level), path)
		} else {
			fmt.Fprintf(os.Stderr, "no log records in %s.\n", path)
		}
		return 0
	}
	for _, r := range recs {
		fmt.Println(r.Raw)
	}
	return 0
}
