//go:build windows

package main

import (
	"fmt"
	"hash/fnv"
	"syscall"
	"unsafe"
)

var (
	modkernel32 = syscall.NewLazyDLL("kernel32.dll")
	modadvapi32 = syscall.NewLazyDLL("advapi32.dll")

	procCreateMutex              = modkernel32.NewProc("CreateMutexW")
	procCreateBoundaryDescriptor = modkernel32.NewProc("CreateBoundaryDescriptorW")
	procAddSIDToBoundary         = modkernel32.NewProc("AddSIDToBoundaryDescriptor")
	procCreatePrivateNamespace   = modkernel32.NewProc("CreatePrivateNamespaceW")
	procOpenPrivateNamespace     = modkernel32.NewProc("OpenPrivateNamespaceW")
	procCreateWellKnownSid       = modadvapi32.NewProc("CreateWellKnownSid")
)

// Well-known SID types (winnt.h WELL_KNOWN_SID_TYPE) admitted into the
// boundary descriptor below. Both are needed: `run` requires an
// elevated/admin context (requireRoot) for an interactive shell, but
// kardianos/service commonly runs the installed service as LocalSystem — a
// distinct SID from BUILTIN\Administrators — and the guard is meant to hold
// across both (a service-manager session and an interactive admin shell),
// not just one.
const (
	winLocalSystemSidType           = 22
	winBuiltinAdministratorsSidType = 26
	// securityMaxSidSize is SECURITY_MAX_SID_SIZE from the Windows SDK — the
	// buffer CreateWellKnownSid requires to guarantee success.
	securityMaxSidSize = 68
)

const (
	runLockBoundaryName   = `dezhban-run-boundary`
	runLockNamespaceAlias = `dezhban-run-ns`
)

// wellKnownSid returns the binary SID for a WELL_KNOWN_SID_TYPE.
func wellKnownSid(sidType uintptr) ([]byte, error) {
	buf := make([]byte, securityMaxSidSize)
	cb := uint32(len(buf))
	ret, _, callErr := procCreateWellKnownSid.Call(
		sidType,
		0, // DomainSid: NULL — not needed for either SID used here
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&cb)),
	)
	if ret == 0 {
		return nil, fmt.Errorf("create well-known sid: %w", callErr)
	}
	return buf[:cb], nil
}

// runLockNamespacePrefix builds (or, on a race with another dezhban process,
// opens) a boundary-restricted private namespace that only a process
// carrying the LocalSystem or BUILTIN\Administrators SID can create or open
// objects in, and returns the "alias\" prefix objects must use to live
// inside it. This is the documented Windows mitigation for named-object
// squatting ("Preventing Squatting Attacks" in Microsoft's kernel object
// namespaces docs): a predictable Global\ name can be pre-created by an
// unprivileged local process, which CreateMutexW then either silently opens
// (letting that process masquerade as the lock holder) or fails to open at
// all if the squatter's DACL denies us — either way blocking the privileged
// daemon from ever starting, across service restarts, until an admin
// intervenes.
//
// Returns "" if any step fails, so the caller can fall back to the
// unhardened Global\ name — degrading to the pre-hardening behavior rather
// than blocking the daemon from starting over an environment (SID lookup
// failure, a namespace API removed by some future Windows edition) this
// code didn't anticipate.
func runLockNamespacePrefix() string {
	boundaryUTF16, err := syscall.UTF16PtrFromString(runLockBoundaryName)
	if err != nil {
		return ""
	}
	bd, _, _ := procCreateBoundaryDescriptor.Call(uintptr(unsafe.Pointer(boundaryUTF16)), 0)
	if bd == 0 {
		return ""
	}

	for _, sidType := range []uintptr{winLocalSystemSidType, winBuiltinAdministratorsSidType} {
		sid, err := wellKnownSid(sidType)
		if err != nil {
			return ""
		}
		ret, _, _ := procAddSIDToBoundary.Call(uintptr(unsafe.Pointer(&bd)), uintptr(unsafe.Pointer(&sid[0])))
		if ret == 0 {
			return ""
		}
	}

	aliasUTF16, err := syscall.UTF16PtrFromString(runLockNamespaceAlias)
	if err != nil {
		return ""
	}
	ns, _, callErr := procCreatePrivateNamespace.Call(0, bd, uintptr(unsafe.Pointer(aliasUTF16)))
	if ns == 0 {
		// Another dezhban process (or a prior run) already created this
		// namespace — open it instead of creating a second one.
		if errno, ok := callErr.(syscall.Errno); ok && errno == syscall.ERROR_ALREADY_EXISTS {
			ns, _, _ = procOpenPrivateNamespace.Call(bd, uintptr(unsafe.Pointer(aliasUTF16)))
		}
		if ns == 0 {
			return ""
		}
	}
	return runLockNamespaceAlias + `\`
}

// acquireRunLock takes a named Windows mutex for the daemon's entire lifetime
// — the Windows twin of the Unix flock in lock_unix.go; see that file's doc
// comment for why this exists and what it guards.
//
// The name is derived from dir so that two callers pointed at different
// directories don't contend with each other, matching the Unix
// implementation's per-directory scoping — useful for tests
// (lock_windows_test.go exercises this directly). In production the only
// caller always passes stateDir(), a fixed OS path unaffected by
// $DEZHBAN_CONFIG or --config (those select the config *file*, not the
// state *directory*), so all `dezhban run` invocations on a host contend on
// the same lock regardless of which config each used — correct, since they
// would otherwise fight over the same firewall backend.
//
// The object lives inside runLockNamespacePrefix's hardened private
// namespace when available, falling back to a plain Global\ name otherwise.
// Global\, not a session-local name either way: the guard is meant to hold
// across sessions (a service-manager session and an interactive admin
// shell), not just within one.
//
// The handle returned by CreateMutexW is intentionally never closed. Windows
// releases a mutex, and the OS reclaims its handle, when the owning process
// exits by any means — a crashed or killed daemon never leaves this locked.
func acquireRunLock(dir string) error {
	h := fnv.New64a()
	_, _ = h.Write([]byte(dir))
	mutexName := fmt.Sprintf("dezhban-run-%x", h.Sum64())

	prefix := runLockNamespacePrefix()
	if prefix == "" {
		prefix = `Global\`
	}

	name, err := syscall.UTF16PtrFromString(prefix + mutexName)
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
