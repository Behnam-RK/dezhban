//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The single-instance guard has one job: a second `dezhban run` against the
// same state directory must refuse, and a released lock must let the next one
// through. Both are exercised directly against the fd, not through
// acquireRunLock — which deliberately never exposes or closes what it holds
// (see its doc comment) — via the tryRunLock test seam.

func TestRunLockRefusesASecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), runLockName)

	fd1, err := tryRunLock(path)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer syscall.Close(fd1)

	if _, err := tryRunLock(path); err == nil {
		t.Fatal("second lock on the same path succeeded; want refusal")
	}
}

func TestRunLockAvailableAfterRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), runLockName)

	fd1, err := tryRunLock(path)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if err := syscall.Close(fd1); err != nil {
		t.Fatalf("release: %v", err)
	}

	fd2, err := tryRunLock(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	defer syscall.Close(fd2)
}

// acquireRunLock is the production entry point: same guarantee, exercised end
// to end (directory → path → open → flock) rather than against a raw path.
func TestAcquireRunLockRefusesASecondHolder(t *testing.T) {
	dir := t.TempDir()

	if err := acquireRunLock(dir); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if err := acquireRunLock(dir); err == nil {
		t.Fatal("second acquire on the same directory succeeded; want refusal")
	}
}

// A second holder's error must be identifiable as genuine contention via
// errors.Is(err, ErrRunLockHeld) — cmdRun uses exactly that check to decide
// between refusing to start and logging a warning and continuing anyway.
func TestAcquireRunLockSecondHolderIsErrRunLockHeld(t *testing.T) {
	dir := t.TempDir()

	if err := acquireRunLock(dir); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	err := acquireRunLock(dir)
	if err == nil {
		t.Fatal("second acquire on the same directory succeeded; want refusal")
	}
	if !errors.Is(err, ErrRunLockHeld) {
		t.Fatalf("second acquire error = %v, want errors.Is(err, ErrRunLockHeld)", err)
	}
}

// Any local user must not be able to flock the lock file open and starve the
// guard from starting: 0644 (readable by everyone) allowed exactly that.
func TestRunLockFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), runLockName)

	fd, err := tryRunLock(path)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer syscall.Close(fd)

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Fatalf("lock file mode = %o, want 0600", got)
	}
}

// An upgrade from a build that created this file 0644 must not leave it that
// way forever — O_CREAT's mode argument only applies to a brand-new file.
func TestRunLockFileModeFixedOnPreExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), runLockName)
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatalf("seed pre-existing 0644 file: %v", err)
	}

	fd, err := tryRunLock(path)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer syscall.Close(fd)

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Fatalf("lock file mode = %o, want 0600 (fixed from pre-existing 0644)", got)
	}
}
