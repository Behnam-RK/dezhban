//go:build windows

package privilege

import (
	"syscall"
	"unsafe"
)

// Token information class for elevation state (TokenElevation). The returned
// TOKEN_ELEVATION struct is a single DWORD: non-zero means the process token is
// elevated (running with full Administrator rights).
const tokenElevation = 20

var (
	advapi32                = syscall.NewLazyDLL("advapi32.dll")
	procGetTokenInformation = advapi32.NewProc("GetTokenInformation")
)

// IsPrivileged reports whether the current process is running elevated (as
// Administrator). It opens the process token and queries its elevation flag —
// the correct check on Windows, where merely being in the Administrators group
// is not enough; the token must be elevated. Implemented with stdlib syscall
// (lazy DLL) so no external dependency is needed.
func IsPrivileged() bool {
	proc, err := syscall.GetCurrentProcess()
	if err != nil {
		return false
	}
	var token syscall.Token
	if err := syscall.OpenProcessToken(proc, syscall.TOKEN_QUERY, &token); err != nil {
		return false
	}
	defer token.Close()

	var elevated uint32
	var retLen uint32
	ret, _, _ := procGetTokenInformation.Call(
		uintptr(token),
		uintptr(tokenElevation),
		uintptr(unsafe.Pointer(&elevated)),
		unsafe.Sizeof(elevated),
		uintptr(unsafe.Pointer(&retLen)),
	)
	if ret == 0 {
		return false
	}
	return elevated != 0
}
