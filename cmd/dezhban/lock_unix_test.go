//go:build !windows

package main

import (
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
