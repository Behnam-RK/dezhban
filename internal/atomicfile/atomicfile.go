// Package atomicfile writes a file's full contents atomically, so a crash or a
// concurrent reader can never observe a partially written result — only the
// old contents or the new ones, never a truncated mix of both.
//
// It replaces what used to be the same ~15 lines (CreateTemp in the target's
// own directory → Write → Sync → Close → Chmod → Rename) copied independently
// into internal/armed, internal/learned, internal/token, internal/config,
// internal/state, internal/command, internal/firewall (pf_darwin's pf.conf/
// state writes and wfp_windows' applied-action marker), and
// cmd/dezhban/panicmark.go — each free to drift from the others on the next
// edit. One implementation means a future correction (fsync ordering, a
// platform-specific quirk) is made once, not rediscovered per package.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write atomically replaces path's contents with data. It creates a temp file
// in the SAME directory as path — required for the final os.Rename to be a
// same-filesystem, atomic replace rather than a cross-filesystem copy — writes
// data, fsyncs it before closing (so a rename can never publish a name whose
// contents are still sitting in a buffer the kernel hasn't flushed), sets mode,
// then renames it over path. The temp file is removed on any failure path
// before the rename commits; once the rename succeeds the removal is a no-op.
func Write(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".atomicfile-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
