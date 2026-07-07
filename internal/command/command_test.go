package command

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// allowAny is a permissive OwnerChecker for tests (the real uid-0 check can't be
// satisfied by a non-root test process).
func allowAny(os.FileInfo, string) error { return nil }

func TestWriteConsumeRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "command.json")
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	in := Command{Op: OpOpenSwitchWindow, Duration: "90s", Profile: "wg", IssuedAt: now, Nonce: "abc"}
	if err := Write(p, in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, ok, err := Consume(p, now.Add(2*time.Second), 30*time.Second, allowAny)
	if err != nil || !ok {
		t.Fatalf("Consume = (%+v, %v, %v), want ok", got, ok, err)
	}
	if got.Op != OpOpenSwitchWindow || got.Duration != "90s" || got.Profile != "wg" {
		t.Errorf("consumed = %+v, want the written command", got)
	}
	// Consume-once: the file is gone.
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("command file must be deleted after Consume")
	}
}

func TestConsumeNoFile(t *testing.T) {
	_, ok, err := Consume(filepath.Join(t.TempDir(), "none.json"), time.Now(), time.Minute, allowAny)
	if ok || err != nil {
		t.Errorf("Consume(missing) = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

func TestConsumeRejectsStaleButDeletes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "command.json")
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	// Issued 10 minutes ago, freshness 30s → stale.
	if err := Write(p, Command{Op: OpOpenSwitchWindow, IssuedAt: now.Add(-10 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	_, ok, err := Consume(p, now, 30*time.Second, allowAny)
	if ok || err == nil {
		t.Errorf("Consume(stale) = (ok=%v, err=%v), want (false, error)", ok, err)
	}
	// A rejected file must still be deleted so it can't wedge the tick.
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("stale command file must be deleted")
	}
}

func TestConsumeRejectsFutureStamp(t *testing.T) {
	p := filepath.Join(t.TempDir(), "command.json")
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	if err := Write(p, Command{Op: OpOpenSwitchWindow, IssuedAt: now.Add(10 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := Consume(p, now, 30*time.Second, allowAny); ok || err == nil {
		t.Errorf("Consume(future) = (ok=%v, err=%v), want rejected", ok, err)
	}
}

func TestConsumeOwnerCheckRejects(t *testing.T) {
	p := filepath.Join(t.TempDir(), "command.json")
	now := time.Now()
	if err := Write(p, Command{Op: OpOpenSwitchWindow, IssuedAt: now}); err != nil {
		t.Fatal(err)
	}
	deny := func(os.FileInfo, string) error { return errDenied }
	if _, ok, err := Consume(p, now, time.Minute, deny); ok || err == nil {
		t.Errorf("Consume with denying owner check = (ok=%v, err=%v), want rejected", ok, err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("owner-rejected file must still be deleted")
	}
}

func TestConsumeRejectsMissingOp(t *testing.T) {
	p := filepath.Join(t.TempDir(), "command.json")
	now := time.Now()
	if err := Write(p, Command{IssuedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := Consume(p, now, time.Minute, allowAny); ok || err == nil {
		t.Errorf("Consume(no op) = (ok=%v, err=%v), want rejected", ok, err)
	}
}

func TestDiscardRemovesFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "command.json")
	if err := Write(p, Command{Op: OpCancelSwitchWindow, IssuedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := Discard(p); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("Discard must remove the file")
	}
	// Discard on a missing file is a no-op.
	if err := Discard(p); err != nil {
		t.Errorf("Discard(missing) = %v, want nil", err)
	}
}

var errDenied = &deniedError{}

type deniedError struct{}

func (*deniedError) Error() string { return "denied" }
