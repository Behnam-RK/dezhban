//go:build windows

package main

import (
	"fmt"
	"hash/fnv"
	"syscall"
	"unsafe"
)

var (
	modkernel32     = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutex = modkernel32.NewProc("CreateMutexW")
)

// acquireRunLock takes a named Windows mutex for the daemon's entire lifetime
// — the Windows twin of the Unix flock in lock_unix.go; see that file's doc
// comment for why this exists and what it guards.
//
// The name is derived from dir (the state directory), not fixed, so two
// dezhban instances pointed at two different state directories — via
// $DEZHBAN_CONFIG or --config — don't contend with each other, matching the
// Unix implementation's per-directory scoping. Global\, not a session-local
// name: `run` already requires an elevated/admin context (requireRoot), which
// can create Global objects without SeCreateGlobalPrivilege, and the guard is
// meant to hold across sessions (a service-manager session and an interactive
// admin shell), not just within one.
//
// The handle returned by CreateMutexW is intentionally never closed. Windows
// releases a mutex, and the OS reclaims its handle, when the owning process
// exits by any means — a crashed or killed daemon never leaves this locked.
func acquireRunLock(dir string) error {
	h := fnv.New64a()
	_, _ = h.Write([]byte(dir))
	name, err := syscall.UTF16PtrFromString(fmt.Sprintf(`Global\dezhban-run-%x`, h.Sum64()))
	if err != nil {
		return fmt.Errorf("lock name: %w", err)
	}
	ret, _, callErr := procCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if ret == 0 {
		return fmt.Errorf("create run-lock mutex: %w", callErr)
	}
	// CreateMutexW always sets last-error even on success (ERROR_SUCCESS);
	// ERROR_ALREADY_EXISTS specifically means another process already owns
	// this name, which for a still-live process means it is still running.
	if errno, ok := callErr.(syscall.Errno); ok && errno == syscall.ERROR_ALREADY_EXISTS {
		return fmt.Errorf("%w against this state directory — see `dezhban status`", ErrRunLockHeld)
	}
	return nil
}
