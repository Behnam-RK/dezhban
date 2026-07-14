//go:build !windows

package control

import (
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

// listenSecure binds the control socket and publishes it at path only once it
// already carries its intended ownership and mode.
//
// The indirection is the point. net.Listen creates the socket with a umask-derived
// mode, so binding directly at path would leave a window — between the bind and
// secureSocket's chown/chmod — in which the socket is live at its published path
// under whatever mode the umask happened to allow. With a permissive umask that is
// a world-writable control socket, and since filesystem permissions are the ENTIRE
// authorization gate here, a window that small is still a hole.
//
// So: bind inside a fresh 0700 root-only directory (nothing else can even traverse
// it), secure the socket there, then rename it into place. A rename moves the
// directory entry, not the bound inode — the listener keeps serving, and the socket
// first becomes reachable at its published path already root:group 0660. The rename
// also replaces any socket left behind by a crash, so no stale-unlink dance is
// needed.
func listenSecure(path, group string) (net.Listener, error) {
	staging, err := os.MkdirTemp(filepath.Dir(path), ".control-")
	if err != nil {
		return nil, fmt.Errorf("control: create staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	tmp := filepath.Join(staging, filepath.Base(path))
	ln, err := net.Listen("unix", tmp)
	if err != nil {
		return nil, fmt.Errorf("control: listen %q: %w", path, err)
	}
	if err := secureSocket(tmp, group); err != nil {
		_ = ln.Close()
		return nil, err
	}
	// Close must not unlink: after the rename the listener's bound path no longer
	// exists, and unlinking the PUBLISHED path is Stop's job (it removes s.path).
	// Left at its default, Close would silently fail to clean anything up.
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("control: publish socket %q: %w", path, err)
	}
	return ln, nil
}

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
