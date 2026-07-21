// Rollback support for `dezhban upgrade --apply`. The design (see
// docs/upgrade.md): stash the current binary (and, on macOS, the app bundle)
// before applying a new .pkg, restart into the new version, and wait for a
// live snapshot. Healthy — delete the stash; it only ever exists during the
// activation risk window, so it is a transaction log, not residue. Not
// healthy — restore it and restart back into the version that was known
// good. Nothing here is OS-specific; darwin_darwin.go-style build tags aren't
// needed because copying files and directories is the same operation
// everywhere. Callers decide what to stash: cmd/dezhban's macOS path stashes
// both the binary and Dezhban.app, since that is the only self-updating
// platform (Linux/Windows are notify-only — see CanActivate's callers).
package update

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// StashDirName lives under the daemon's state directory (/var/db/dezhban) —
// machine-derived, safe-to-discard operational data, the same classification
// learned.json already has, not user config.
const StashDirName = "upgrade-stash"

// StashFile copies src (a regular file, e.g. the CLI binary) into dir,
// preserving its executable bit. dir is created if absent.
func StashFile(dir, src string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("stash: %w", err)
	}
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stash: %w", err)
	}
	return copyFile(src, filepath.Join(dir, filepath.Base(src)), info.Mode())
}

// StashDir recursively copies src (a directory, e.g. Dezhban.app) into
// dir/<base of src>. dir is created if absent.
func StashDir(dir, src string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("stash: %w", err)
	}
	dst := filepath.Join(dir, filepath.Base(src))
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("stash: %w", err)
	}
	return copyDir(src, dst)
}

// RestoreFile copies the previously-stashed file (named base) from dir back
// to dst, preserving the stashed mode.
func RestoreFile(dir, base, dst string) error {
	src := filepath.Join(dir, base)
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	return copyFile(src, dst, info.Mode())
}

// RestoreDir replaces dst with the previously-stashed directory (named base)
// from dir.
func RestoreDir(dir, base, dst string) error {
	src := filepath.Join(dir, base)
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	return copyDir(src, dst)
}

// ClearStash deletes dir entirely. Called once the newly-applied version has
// proven healthy — the stash's whole reason to exist is the activation risk
// window, and it must not outlive that.
func ClearStash(dir string) error {
	return os.RemoveAll(dir)
}

// HasStash reports whether dir exists and is non-empty — used to detect an
// interrupted previous upgrade (stashed but never cleared or restored) before
// starting a new one.
func HasStash(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

// StashVerdict classifies a leftover rollback stash against the version
// currently on disk, so `upgrade apply`'s HasStash guard (cmd/dezhban) can
// tell "a previous upgrade already activated fine and this is just unswept
// disk" from "activation is still deferred and this copy is still needed" —
// see docs/upgrade.md's "If the restart doesn't come back healthy" section.
//
// Pure and filesystem-free on purpose, matching this file's existing split
// with cmd/dezhban: the decision belongs here, where it is unit-testable
// without root or a real service; only the exec calls that obtain
// stashedVersion/onDiskVersion in the first place live in cmd/.
type StashVerdict int

const (
	// StashUnknown means the two versions could not be compared with
	// confidence: a dev build on either side, an unparseable version string,
	// or the stash reporting a NEWER version than what's on disk (which
	// should never happen in practice and is refused rather than guessed
	// at). Callers must refuse — the same "an undeterminable reading holds,
	// never escalates" rule CLAUDE.md documents for decision.Evaluate.
	StashUnknown StashVerdict = iota
	// StashPending means the stashed version equals what's on disk: the
	// payload landed but activation hasn't happened (or been confirmed
	// healthy) yet. The stash is still the only rollback copy — refuse.
	StashPending
	// StashObsolete means the stash is OLDER than what's on disk: some
	// activation already happened and came back healthy since this stash
	// was made. It has outlived its purpose and is safe to clear.
	StashObsolete
)

// ClassifyStash compares a stashed binary's version against the version
// currently installed on disk. Both arguments are bare version strings as
// normalizeVersion expects (a leading "v" is fine; callers are responsible
// for stripping any other prefix — e.g. the "dezhban " that `dezhban
// version`'s output carries — before calling this). See StashVerdict for
// what each outcome means and what callers must do with it.
func ClassifyStash(stashedVersion, onDiskVersion string) StashVerdict {
	stashed := normalizeVersion(stashedVersion)
	onDisk := normalizeVersion(onDiskVersion)
	if stashed == "" || onDisk == "" {
		return StashUnknown
	}
	switch {
	case semverLess(stashed, onDisk):
		return StashObsolete
	case stashed == onDisk:
		return StashPending
	default: // stashed > onDisk — should never happen; refuse rather than guess
		return StashUnknown
	}
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	// Rename, not a direct write to dst: dst may be the currently-running
	// binary. Renaming over it is the same "old inode stays alive" semantics
	// install.sh's binary swap relies on — never truncate a file that might be
	// executing.
	return os.Rename(tmp, dst)
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if d.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode())
	})
}
