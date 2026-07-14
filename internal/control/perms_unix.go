//go:build !windows

package control

import (
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// checkDirSecure refuses to publish the socket into a directory an unprivileged
// local user could tamper with.
//
// The socket's own 0660 root:admin mode is only half the boundary. Permissions on
// a unix socket gate who may CONNECT to it; permissions on its parent directory
// gate who may UNLINK it — and a local user who can unlink our socket can bind
// their own in its place and answer `block`/`unblock`/`open-switch` however they
// like. Since filesystem permissions are the entire authorization model here, the
// container's bits are part of that model, so an insecure directory fails the
// control feature closed rather than publishing into it.
//
// Two ways a directory qualifies as insecure:
//
//   - group- or world-writable WITHOUT the sticky bit. Sticky is the exception on
//     purpose: it is exactly the bit that makes /tmp-style 1777 dirs safe here, by
//     restricting unlink to the file's owner (root, for us).
//   - owned by neither root nor us. The owner can chmod it back open whenever they
//     like, so its current mode proves nothing.
func checkDirSecure(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("control: stat socket dir %q: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("control: socket dir %q is not a directory", dir)
	}
	mode := fi.Mode()
	if mode.Perm()&0o022 != 0 && mode&os.ModeSticky == 0 {
		return fmt.Errorf("control: socket dir %q is group/world-writable (%#o) and not sticky: "+
			"a local user could replace the control socket; tighten it to 0755 or set control.enabled=false",
			dir, mode.Perm())
	}
	// Ownership is a *syscall.Stat_t detail; if some platform doesn't give us one,
	// the mode check above still stands rather than failing the daemon outright.
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if uid := int(st.Uid); uid != 0 && uid != os.Getuid() {
		return fmt.Errorf("control: socket dir %q is owned by uid %d (neither root nor us): "+
			"its owner could replace the control socket; move control.socket into a root-owned directory",
			dir, uid)
	}
	return nil
}

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
