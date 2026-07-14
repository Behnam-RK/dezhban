//go:build !windows

package control

import (
	"os"
	"path/filepath"
	"testing"
)

// The socket's 0660 root:admin mode gates who may CONNECT. Its parent directory
// gates who may UNLINK — and a local user who can unlink the socket can bind their
// own in its place and answer block/unblock/open-switch however they like. These
// tests pin the parent directory as part of the authorization boundary.

// mkdirMode makes a directory with an exact mode, defeating the umask (MkdirAll
// masks the requested bits, so a 0777 dir under the usual 022 umask arrives as
// 0755 and the test would pass for the wrong reason).
func mkdirMode(t *testing.T, parent, name string, mode os.FileMode) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return dir
}

func TestWorldWritableDirFailsClosed(t *testing.T) {
	base, err := os.MkdirTemp("", "dzb")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	for _, mode := range []os.FileMode{0o777, 0o757, 0o775} {
		dir := mkdirMode(t, base, "d"+mode.String(), mode)
		path := filepath.Join(dir, "c.sock")
		if _, err := New(path, "", testLogger()); err == nil {
			t.Fatalf("New succeeded in a %#o dir; a local user could replace the socket, so it must fail closed", mode)
		}
		// Failing closed means failing closed: no socket left behind for anyone to talk to.
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("socket published into an insecure %#o dir: %v", mode, err)
		}
	}
}

// Sticky is the deliberate exception: it is exactly the bit that restricts unlink
// to the file's owner, which is what makes a /tmp-style 1777 dir safe for us.
func TestStickyWorldWritableDirIsAccepted(t *testing.T) {
	base, err := os.MkdirTemp("", "dzb")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	dir := mkdirMode(t, base, "sticky", 0o777|os.ModeSticky)
	srv, err := New(filepath.Join(dir, "c.sock"), "", testLogger())
	if err != nil {
		t.Fatalf("New failed in a sticky 1777 dir; sticky restricts unlink to the owner, so it is safe: %v", err)
	}
	srv.Stop()
}

func TestPrivateDirIsAccepted(t *testing.T) {
	base, err := os.MkdirTemp("", "dzb")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	dir := mkdirMode(t, base, "ok", 0o755)
	srv, err := New(filepath.Join(dir, "c.sock"), "", testLogger())
	if err != nil {
		t.Fatalf("New failed in a 0755 dir, which is the normal state-dir mode: %v", err)
	}
	srv.Stop()
}
