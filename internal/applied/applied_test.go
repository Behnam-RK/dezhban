package applied

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	path := Path(t.TempDir())
	want := Record{
		Mode:    "guard",
		At:      time.Date(2026, 8, 21, 14, 2, 11, 0, time.UTC),
		Rules:   "pass out quick on utun4 all\nblock drop out all\n",
		Backend: "pf",
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := Load(path)
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	if got.Mode != want.Mode || got.Rules != want.Rules || got.Backend != want.Backend {
		t.Errorf("round trip lost data: %+v", got)
	}
	if !got.At.Equal(want.At) {
		t.Errorf("At = %v, want %v", got.At, want.At)
	}
	if got.Version != version {
		t.Errorf("Version = %d, want %d", got.Version, version)
	}
}

// The GUI runs unprivileged and has to be able to read this, exactly like
// state.json. 0600 would make the pane useless to the surface it exists for.
func TestRecordIsWorldReadable(t *testing.T) {
	path := Path(t.TempDir())
	if err := Save(path, Record{Mode: "guard"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", fi.Mode().Perm())
	}
}

// "Nothing recorded yet" is an ordinary state — a daemon in standby has applied
// nothing — and must not read as a failure to the surfaces that show it.
func TestMissingFileIsNotAnError(t *testing.T) {
	_, ok, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if ok || err != nil {
		t.Errorf("ok=%v err=%v, want false/nil", ok, err)
	}
}

// A stale ruleset read as current after teardown would say the guard is
// enforcing when nothing is.
func TestRemoveClearsTheRecordAndIsIdempotent(t *testing.T) {
	path := Path(t.TempDir())
	if err := Save(path, Record{Mode: "guard"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := Remove(path); err != nil {
			t.Fatalf("Remove #%d: %v", i, err)
		}
	}
	if _, ok, _ := Load(path); ok {
		t.Error("record survived Remove")
	}
}

// Corrupt is discarded, never fatal: it describes the past, and enforcement
// does not depend on it. Same call as learned.json and armed.json.
func TestCorruptRecordIsDiscardedNotFatal(t *testing.T) {
	path := Path(t.TempDir())
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, ok, err := Load(path)
	if ok {
		t.Error("a corrupt record was reported as usable")
	}
	if err == nil {
		t.Error("a corrupt record should still be reported to the caller to log")
	}
}
