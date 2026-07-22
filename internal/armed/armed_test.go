package armed

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestLoadMissingFileIsNeverArmed(t *testing.T) {
	r, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if r.TunnelEverUp || r.Version != version {
		t.Errorf("missing file record = %+v, want zero-value v%d", r, version)
	}
}

func TestLoadCorruptIsNeverArmedNotFatal(t *testing.T) {
	p := filepath.Join(t.TempDir(), "armed.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(p)
	if err == nil {
		t.Error("Load(corrupt) = nil error, want a (non-fatal) parse error")
	}
	if r == nil || r.TunnelEverUp {
		t.Errorf("corrupt load must yield an unarmed record, got %+v", r)
	}
}

func TestMarkUpRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "armed.json")
	t1 := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	if err := MarkUp(p, t1); err != nil {
		t.Fatalf("MarkUp: %v", err)
	}

	// 0644 so the unprivileged CLI/GUI can read it. Windows doesn't honor Unix
	// perm bits, so the exact-mode check is POSIX-only.
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o644 {
		t.Errorf("mode = %o, want 0644", perm)
	}

	r, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !r.TunnelEverUp {
		t.Fatal("TunnelEverUp = false after MarkUp")
	}
	if !r.FirstUp.Equal(t1) {
		t.Errorf("FirstUp = %v, want %v", r.FirstUp, t1)
	}
	if !r.LastUp.Equal(t1) {
		t.Errorf("LastUp = %v, want %v", r.LastUp, t1)
	}

	// A second MarkUp must refresh LastUp but never move FirstUp — that is the
	// "observed at least once" fact, and it must not drift forward on every boot.
	t2 := t1.Add(24 * time.Hour)
	if err := MarkUp(p, t2); err != nil {
		t.Fatalf("MarkUp #2: %v", err)
	}
	r2, err := Load(p)
	if err != nil {
		t.Fatalf("Load #2: %v", err)
	}
	if !r2.FirstUp.Equal(t1) {
		t.Errorf("FirstUp moved on second MarkUp: got %v, want %v", r2.FirstUp, t1)
	}
	if !r2.LastUp.Equal(t2) {
		t.Errorf("LastUp = %v, want %v", r2.LastUp, t2)
	}
}

func TestMarkUpOverCorruptFileRecovers(t *testing.T) {
	p := filepath.Join(t.TempDir(), "armed.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	if err := MarkUp(p, now); err != nil {
		t.Fatalf("MarkUp over corrupt file: %v", err)
	}
	r, err := Load(p)
	if err != nil {
		t.Fatalf("Load after recovery: %v", err)
	}
	if !r.TunnelEverUp {
		t.Error("TunnelEverUp = false after MarkUp recovered from a corrupt file")
	}
}
