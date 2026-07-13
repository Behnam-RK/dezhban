//go:build !windows

package control

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
)

// secureSocket gives the socket its trust boundary: owned by root, mode 0660,
// group-owned by `group` (macOS: "admin"). Filesystem permissions are the ONLY
// gate — with no peer credentials available in stdlib, membership of that group
// IS the authorization. An empty group means root-only (0600): the socket exists
// but no unprivileged caller can open it.
//
// Every failure path removes nothing here (the caller closes and unlinks) but
// returns an error, so the daemon never ends up serving a socket whose ownership
// it could not establish.
func secureSocket(path, group string) error {
	if group == "" {
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("control: chmod socket root-only: %w", err)
		}
		return nil
	}
	g, err := user.LookupGroup(group)
	if err != nil {
		return fmt.Errorf("control: look up group %q (set control.group to a group that exists, or \"\" for root-only): %w", group, err)
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return fmt.Errorf("control: group %q has non-numeric gid %q: %w", group, g.Gid, err)
	}
	if err := os.Chown(path, 0, gid); err != nil {
		return fmt.Errorf("control: chown socket to root:%s: %w", group, err)
	}
	// 0660 after the chown, never before: a window at 0666 would be a real hole.
	if err := os.Chmod(path, 0o660); err != nil {
		return fmt.Errorf("control: chmod socket 0660: %w", err)
	}
	return nil
}
