// Package applied records the firewall ruleset the daemon last installed, so a
// diagnostic surface can show what is actually being enforced rather than
// asking the reader to re-derive it.
//
// `dezhban print-rules --mode guard|fullblock|switch` already renders what each
// posture WOULD apply — pure, root-free, and available at any time. What was
// missing is the other half: which of those is live right now, rendered from the
// policy that was actually handed to the backend, including the tunnel
// interfaces and endpoint addresses resolved at that moment. Those change while
// the daemon runs, so re-rendering after the fact can quietly disagree with what
// the kernel holds.
//
// This is dezhban's own account of what it did, not a reading of the kernel. It
// is the cheap half of the picture and works identically on every platform; the
// GUI pairs it with an on-demand privileged readback, and a disagreement between
// the two is itself the finding. Deliberately NOT a substitute for the run
// loop's verify tick, which is what notices and repairs rules going missing.
//
// The record lives beside the state file (see cmd/dezhban.defaultStatePath),
// same convention as internal/learned and internal/armed: daemon-owned,
// machine-derived, never the user's config, and safe to discard — a missing or
// corrupt file just means "nothing recorded yet". Every write is a whole-file
// atomic replace, so a reader never sees a torn file. Mode 0644 like state.json:
// the unprivileged menubar app has to be able to read it, and it holds nothing
// `print-rules` would not print for free.
package applied

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/behnam-rk/dezhban/internal/atomicfile"
	"github.com/behnam-rk/dezhban/internal/state"
)

// version is the on-disk schema version. Bump on an incompatible change.
const version = 1

// FileName is the record's name within the state directory.
const FileName = "applied-rules.json"

// Record is the whole applied-rules.json document.
type Record struct {
	Version int `json:"version"`
	// Mode is the posture string the ruleset installs — the same stable
	// identifier print-rules --mode takes ("guard", "fullblock", "switch").
	Mode string `json:"mode"`
	// At is when the apply succeeded. A reader shows it verbatim: "what dezhban
	// applied at 14:02:11" is an honest label in a way "the current rules" is
	// not, because nothing here observes the kernel.
	At time.Time `json:"at"`
	// Rules is the exact text handed to the backend.
	Rules string `json:"rules"`
	// Backend names the mechanism the text is written for ("pf", "nft", "wfp"),
	// so a reader does not have to infer a syntax from the platform it happens
	// to be running on.
	Backend string `json:"backend"`
}

// Path returns the record's path within the given state directory.
func Path(stateDir string) string { return filepath.Join(stateDir, FileName) }

// Save writes the record atomically. Errors are the caller's to log and
// swallow: this is a diagnostic aid, and failing to record what was applied
// must never be a reason not to apply it.
func Save(path string, r Record) error {
	r.Version = version
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", FileName, err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, state.DirMode); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return atomicfile.Write(path, append(data, '\n'), 0o644)
}

// Load reads the record. A missing file is (Record{}, false, nil) — "nothing
// recorded yet" is an ordinary state, not an error, and the surfaces that read
// this must say so rather than reporting a failure.
func Load(path string) (Record, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		// Same call as learned.json and armed.json: a corrupt record is
		// discarded, never fatal. It describes the past, and the daemon's
		// enforcement does not depend on it.
		return Record{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	return r, true, nil
}

// Remove deletes the record. Called when rules are torn down, so a stale
// ruleset cannot be read as current after an Unblock or Cleanup. A missing file
// is success.
func Remove(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
