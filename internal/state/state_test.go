package state

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestWriteReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "state.json") // sub dir must be created
	want := Snapshot{
		Time:             time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
		Mode:             "vpn",
		Posture:          "guard",
		Blocked:          false,
		IP:               "203.0.113.45",
		CountryCode:      "US",
		Provider:         "ipinfo.io",
		Tunnels:          []Tunnel{{Name: "utun4", Up: true, Detail: "UP"}},
		Endpoints:        []string{"198.51.100.7"},
		BlockedCountries: []string{"IR"},
		PID:              4242,
	}

	if err := Write(path, want); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !got.Time.Equal(want.Time) {
		t.Errorf("Time: got %v want %v", got.Time, want.Time)
	}
	if got.Mode != want.Mode || got.Posture != want.Posture || got.Blocked != want.Blocked {
		t.Errorf("posture fields mismatch: got %+v", got)
	}
	if got.IP != want.IP || got.CountryCode != want.CountryCode || got.Provider != want.Provider {
		t.Errorf("reading fields mismatch: got %+v", got)
	}
	if len(got.Tunnels) != 1 || got.Tunnels[0] != want.Tunnels[0] {
		t.Errorf("Tunnels mismatch: got %+v", got.Tunnels)
	}
	if len(got.Endpoints) != 1 || got.Endpoints[0] != want.Endpoints[0] {
		t.Errorf("Endpoints mismatch: got %+v", got.Endpoints)
	}
	if len(got.BlockedCountries) != 1 || got.BlockedCountries[0] != "IR" {
		t.Errorf("BlockedCountries mismatch: got %+v", got.BlockedCountries)
	}
	if got.PID != want.PID {
		t.Errorf("PID: got %d want %d", got.PID, want.PID)
	}
}

func TestWriteIsWorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows has no POSIX mode bits: os.Chmod only honors the write bit and
		// os.Stat reports 0666/0444, so the 0644 contract is meaningless here.
		// File access on Windows is governed by ACLs, not the Unix mode. The
		// world-readable requirement only matters for the root-daemon/unprivileged-
		// reader split on Unix.
		t.Skip("permission bits are not POSIX on Windows")
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := Write(path, Snapshot{Posture: "allow"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o644 {
		t.Errorf("perm: got %o want 0644 (reader is the unprivileged user)", perm)
	}
}

// TestWriteAtomicNoTempLeak checks the temp file is renamed away, not left behind,
// so the directory holds only the final state file after a successful write.
func TestWriteAtomicNoTempLeak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := Write(path, Snapshot{Posture: "block"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("expected only state.json, got %v (temp leak?)", names)
	}
}

func TestReadMissingFile(t *testing.T) {
	_, err := Read(filepath.Join(t.TempDir(), "nope.json"))
	if !os.IsNotExist(err) {
		t.Errorf("expected not-exist error, got %v", err)
	}
}

// The state directory is the daemon's whole out-of-process contract: the menubar app
// reads state.json out of it and every routine op reaches control.sock through it,
// both as the unprivileged logged-in user. A 0700 directory silently severs both —
// the GUI reports "stopped" while the daemon enforces normally, and block/unblock
// fall back to a password prompt. This regressed exactly that way once (the pf
// backend created the shared dir 0700 and MkdirAll then no-ops on it forever), which
// is why EnsureDir must REPAIR the mode, not just create it.
func TestEnsureDirRepairsTooRestrictiveMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dezhban")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != DirMode {
		t.Fatalf("dir mode = %#o, want %#o — an unprivileged reader still can't traverse it", got, DirMode)
	}
}

// The other direction matters more, and for a different reason. A world-writable
// state dir lets any local user unlink control.sock and bind their own socket at that
// path — the daemon's authorization gate IS the filesystem, so the mode of the
// directory holding the socket is part of the boundary, not decoration around it.
// "At least traversable" is therefore not the check; the exact mode is.
func TestEnsureDirTightensTooPermissiveMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dezhban")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	// MkdirAll honours the umask, so force the mode we actually want to test.
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != DirMode {
		t.Fatalf("dir mode = %#o, want %#o — a local user could replace control.sock", got, DirMode)
	}
}

func TestEnsureDirCreatesTraversableDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != DirMode {
		t.Errorf("dir mode = %#o, want %#o", got, DirMode)
	}
}
