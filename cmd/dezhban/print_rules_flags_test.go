package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/behnam-rk/dezhban/internal/applied"
	"github.com/behnam-rk/dezhban/internal/firewall"
)

// print-rules carries two kinds of flag: ones describing a ruleset to RENDER
// (--mode) and ones selecting a live ruleset to REPORT (--applied, --installed).
// Mixing them, or asking for --json where the output is firewall syntax, is a
// request that cannot be honoured — and accepting it while quietly ignoring half
// of it is the failure this project treats as the worst kind.
func TestPrintRulesRefusesFlagsItCannotHonour(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"two sources at once", []string{"--applied", "--installed"}, 2},
		{"mode with applied", []string{"--applied", "--mode", "fullblock"}, 2},
		{"mode with installed", []string{"--installed", "--mode", "guard"}, 2},
		{"json with no source", []string{"--json"}, 2},
		{"config with applied", []string{"--applied", "--config", "/tmp/x.json"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cmdPrintRules(tc.args); got != tc.want {
				t.Errorf("cmdPrintRules(%v) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}

// The refusal must be about what the user TYPED, not about a flag's default.
// --mode defaults to "guard", so testing its value instead of whether it was
// given would reject every plain --applied run.
func TestPrintRulesAppliedIsFineWithoutMode(t *testing.T) {
	withTempAppliedRecord(t)
	if got := cmdPrintRules([]string{"--applied"}); got != 0 {
		t.Errorf("cmdPrintRules(--applied) = %d, want 0", got)
	}
}

// withTempAppliedRecord points the record at a temp dir for the duration of one
// test. stateDir() is a hardcoded absolute path, so without this these tests
// read the developer's own live ruleset off the host.
func withTempAppliedRecord(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), applied.FileName)
	prev := appliedPath
	appliedPath = func() string { return path }
	t.Cleanup(func() { appliedPath = prev })
	return path
}

// The record must not outlive the rules it describes. `panic` and `unblock` tear
// the firewall down without any daemon involved, so nothing else will ever clear
// it — and a surviving record is read as the live posture, which is a pane
// saying "guard is enforcing" over a network that command just threw open.
func TestTearingDownClearsTheAppliedRecord(t *testing.T) {
	path := withTempAppliedRecord(t)

	recordAppliedBestEffort(firewall.Policy{Mode: firewall.ModeFullBlock})
	if _, ok, err := applied.Load(path); err != nil || !ok {
		t.Fatalf("record not written: ok=%v err=%v", ok, err)
	}

	clearAppliedRecordBestEffort("panic")
	_, ok, err := applied.Load(path)
	if err != nil {
		t.Fatalf("load after clear: %v", err)
	}
	if ok {
		t.Error("the record survived teardown; --applied would report a posture that is gone")
	}
	// Teardown runs on failure paths too, so clearing has to be idempotent.
	clearAppliedRecordBestEffort("panic")
}

// What `block` installs directly is recorded too, or the diagnostic is only
// truthful when the daemon happened to be the one enforcing.
func TestABlockAppliedByHandIsRecorded(t *testing.T) {
	path := withTempAppliedRecord(t)

	recordAppliedBestEffort(firewall.Policy{Mode: firewall.ModeFullBlock})
	rec, ok, err := applied.Load(path)
	if err != nil || !ok {
		t.Fatalf("record not written: ok=%v err=%v", ok, err)
	}
	if rec.Mode != firewall.ModeFullBlock.String() {
		t.Errorf("mode = %q, want %q", rec.Mode, firewall.ModeFullBlock.String())
	}
	if rec.Rules == "" {
		t.Error("no ruleset text recorded")
	}
	if rec.Backend != firewall.RulesetKind {
		t.Errorf("backend = %q, want %q", rec.Backend, firewall.RulesetKind)
	}
	_ = os.Remove(path)
}

// internal/applied's contract is that a corrupt record is discarded, never
// fatal. --applied has to agree: an unreadable file is "nothing recorded",
// which exits 0, not a command failure.
func TestACorruptRecordReadsAsNothingRecorded(t *testing.T) {
	path := withTempAppliedRecord(t)
	if err := os.WriteFile(path, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := cmdPrintRules([]string{"--applied"}); got != 0 {
		t.Errorf("cmdPrintRules(--applied) on a corrupt record = %d, want 0", got)
	}
}
