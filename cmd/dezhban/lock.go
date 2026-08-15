package main

import "errors"

// ErrRunLockHeld distinguishes genuine single-instance contention — another
// `dezhban run` already holds the lock — from every other reason
// acquireRunLock can fail (an unwritable state directory, a missing parent,
// a permission error). Only the former should refuse to start: the lock is a
// safety NET around Backend.Apply, and its own failure to establish must
// never become a reason the kill switch does not enforce — the same
// principle state.EnsureDir's own tolerated failure already follows for the
// directory underneath it. See acquireRunLock's doc comment in
// lock_unix.go/lock_windows.go for what the lock protects.
var ErrRunLockHeld = errors.New("another dezhban is already running")
