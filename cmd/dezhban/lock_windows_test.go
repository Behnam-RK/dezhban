//go:build windows

package main

import (
	"errors"
	"testing"
)

// Unlike lock_unix_test.go, there is no exposed seam here comparable to
// tryRunLock: acquireRunLock's doc comment explains why the handle returned by
// CreateMutexW is deliberately never closed (the OS reclaims it on process
// exit), so there is nothing to release and re-acquire within a single test
// process. Only the production entry point's refusal is testable in-process —
// a second CreateMutexW call against the same name, even from the same
// process, sets ERROR_ALREADY_EXISTS, which is exactly the ownership question
// this guard cares about.
func TestAcquireRunLockRefusesASecondHolder(t *testing.T) {
	dir := t.TempDir()

	if err := acquireRunLock(dir); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	err := acquireRunLock(dir)
	if err == nil {
		t.Fatal("second acquire on the same directory succeeded; want refusal")
	}
	// cmdRun uses errors.Is(err, ErrRunLockHeld) to distinguish genuine
	// contention (refuse to start) from every other lock failure (warn and
	// continue) — see lock.go's doc comment.
	if !errors.Is(err, ErrRunLockHeld) {
		t.Fatalf("second acquire error = %v, want errors.Is(err, ErrRunLockHeld)", err)
	}
}

// Two different state directories must never contend with each other — the
// mutex name is derived from the directory, matching the Unix flock's
// per-path scoping (see acquireRunLock's doc comment).
func TestAcquireRunLockDoesNotContendAcrossDirectories(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()

	if err := acquireRunLock(dir1); err != nil {
		t.Fatalf("acquire dir1: %v", err)
	}
	if err := acquireRunLock(dir2); err != nil {
		t.Fatalf("acquire dir2 should not contend with dir1's lock: %v", err)
	}
}
