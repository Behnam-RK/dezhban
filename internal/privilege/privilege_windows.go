//go:build windows

package privilege

// IsPrivileged is a stub on Windows. A real Administrator/elevation check is
// implemented in Phase 5 alongside the WFP backend.
func IsPrivileged() bool {
	return true
}
