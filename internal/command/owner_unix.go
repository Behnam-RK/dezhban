//go:build !windows

package command

import (
	"fmt"
	"os"
	"syscall"
)

// RootOwned is the production OwnerChecker on unix: the command file must be
// owned by uid 0 and must not be world-writable. Combined with the root-owned
// parent directory, this ensures only root could have created a command.
func RootOwned(fi os.FileInfo, path string) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("command: cannot stat owner of %q", path)
	}
	if st.Uid != 0 {
		return fmt.Errorf("command: %q not owned by root (uid %d)", path, st.Uid)
	}
	if fi.Mode().Perm()&0o002 != 0 {
		return fmt.Errorf("command: %q is world-writable", path)
	}
	return nil
}
