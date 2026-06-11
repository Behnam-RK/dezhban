//go:build !windows

// Package privilege reports whether dezhban has the OS privileges needed to
// modify the firewall (root on unix, Administrator on Windows).
package privilege

import "os"

// IsPrivileged reports whether the process is running as root.
func IsPrivileged() bool {
	return os.Geteuid() == 0
}
