//go:build windows

package command

import "os"

// RootOwned on Windows is a no-op: access is controlled by the ACL on the
// %ProgramData%\dezhban directory (writable only by administrators/SYSTEM), not
// by POSIX ownership bits. Documented as a known difference — Windows is
// service-managed and the command file inherits the directory's restricted ACL.
func RootOwned(fi os.FileInfo, path string) error { return nil }
