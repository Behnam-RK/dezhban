package main

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/behnam-rk/dezhban/internal/config"
	"github.com/behnam-rk/dezhban/internal/privilege"
)

// sudoDisabled reports whether auto-elevation is turned off, via the --no-sudo
// flag or a truthy DEZHBAN_NO_SUDO env value.
func sudoDisabled() bool {
	if noSudo {
		return true
	}
	if v := os.Getenv("DEZHBAN_NO_SUDO"); v != "" {
		// Truthy disables; any unparseable-but-set value also counts as "disable".
		b, err := strconv.ParseBool(v)
		return err != nil || b
	}
	return false
}

// pathWritable reports whether the current process can write path without
// elevation, leaving no lasting side effects.
func pathWritable(path string) bool {
	if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		_ = f.Close()
		return true
	} else if !os.IsNotExist(err) {
		return false // exists but not writable, or some other error
	}
	// Absent file: config.Save will MkdirAll any missing parent dirs unprivileged, so
	// writability is decided by the nearest EXISTING ancestor, not path's immediate
	// dir (which may not exist yet). Probing only Dir(path) would wrongly report an
	// unwritable path for a user-owned tree like $HOME/newdir/dezhban.json and force a
	// needless sudo escalation that then creates root-owned dirs in the user's home.
	dir := filepath.Dir(path)
	for {
		if fi, err := os.Stat(dir); err == nil {
			if !fi.IsDir() {
				return false // an ancestor is a file, not a dir — Save can't MkdirAll through it
			}
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false // reached the root without finding an existing dir
		}
		dir = parent
	}
	f, err := os.CreateTemp(dir, ".dezhban-wtest-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// writeConfig persists cfg to path, elevating just the write via sudo when the
// path needs root and elevation is allowed. Unlike the whole-command re-exec
// used by privileged subcommands, this never restarts the process — so an
// interactive wizard (setup) or a one-shot `config set` keeps its result.
func writeConfig(path string, cfg *config.Config) error {
	if privilege.IsPrivileged() || pathWritable(path) {
		return config.Save(path, cfg)
	}
	if canElevate() {
		data, err := config.Marshal(cfg)
		if err != nil {
			return err
		}
		return elevatedWrite(path, data)
	}
	return config.Save(path, cfg) // will fail with permission → caller shows a hint
}
