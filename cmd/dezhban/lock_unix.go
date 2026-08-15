//go:build !windows

package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
)

// runLockName is the lock file's name under the state directory. Not tagged
// "dezhban" like the firewall rules (nothing else in this file needs the
// backend's surgical-teardown discipline — it is deleted with the rest of the
// state directory, never parsed, never shared).
const runLockName = "dezhban.lock"

// acquireRunLock takes an exclusive, non-blocking lock on <dir>/dezhban.lock,
// held for the daemon's entire lifetime. It is the guard `panic`, `unblock`,
// and the service-lifecycle commands deliberately do NOT take (they must stay
// usable with no daemon running at all) — only `run` calls this, once, before
// the run loop starts.
//
// Without it, `sudo dezhban run --no-daemon` started beside an already-running
// service gives two processes both calling Backend.Apply — one process each,
// so the "single run-loop goroutine owns every Apply" invariant
// (docs/contribute/architecture.md) holds inside a process but nothing enforced
// it across two.
//
// A raw file descriptor, not *os.File: os.File attaches a GC finalizer that
// closes the fd — and so releases the flock — the moment the wrapper becomes
// unreachable, which can happen before the daemon actually exits since nothing
// here reads the descriptor again. The fd below is intentionally never closed;
// the kernel releases the lock when the process ends, by any means (a clean
// stop, a crash, a SIGKILL), so a killed daemon never leaves the next start
// wedged behind a stale lock.
func acquireRunLock(dir string) error {
	_, err := tryRunLock(filepath.Join(dir, runLockName))
	return err
}

// tryRunLock does the actual open+flock and returns the raw fd on success, so
// tests can acquire and explicitly release a lock to exercise contention —
// acquireRunLock itself never exposes or closes it, by design (see its doc
// comment). Not used by acquireRunLock's own error message, which reports the
// path rather than the fd.
func tryRunLock(path string) (int, error) {
	// 0600, not 0644: flock only requires a readable descriptor, so a
	// world/group-readable lock file would let any local user hold it open
	// (e.g. `flock -x <path> -c 'sleep inf'`) and starve the guard from ever
	// starting, including at boot. O_CLOEXEC keeps the descriptor from leaking
	// into a backend child (pfctl/nft) that outlives the daemon, which would
	// otherwise hold the flock past this process's own exit.
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return -1, fmt.Errorf("open lock file %s: %w", path, err)
	}
	// O_CREAT's mode only applies to a newly-created file, so an upgrade from
	// a build that created this file 0644 would otherwise keep the looser
	// mode forever. Enforce 0600 unconditionally.
	if err := syscall.Fchmod(fd, 0600); err != nil {
		_ = syscall.Close(fd)
		return -1, fmt.Errorf("chmod lock file %s: %w", path, err)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = syscall.Close(fd)
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return -1, fmt.Errorf("%w (holds %s) — see `dezhban status`", ErrRunLockHeld, path)
		}
		return -1, fmt.Errorf("lock %s: %w", path, err)
	}
	return fd, nil
}
