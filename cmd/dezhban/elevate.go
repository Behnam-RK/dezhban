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
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
		return true // any other non-empty value counts as "set"
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
	// Absent file: can we create one in its directory?
	f, err := os.CreateTemp(filepath.Dir(path), ".dezhban-wtest-*")
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
