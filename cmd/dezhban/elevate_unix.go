//go:build !windows

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// canElevate reports whether we can and should auto-elevate via sudo: not
// disabled, sudo is present, and stdin is an interactive terminal to prompt on.
func canElevate() bool {
	if sudoDisabled() {
		return false
	}
	if _, err := exec.LookPath("sudo"); err != nil {
		return false
	}
	return isTerminal(os.Stdin)
}

// reexecElevated replaces the current process with `sudo <self> <same args>`.
// It returns only on failure. $DEZHBAN_CONFIG is preserved so the root process
// reads the same config the user selected.
func reexecElevated() error {
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	argv := []string{"sudo"}
	if cfg := strings.TrimSpace(os.Getenv("DEZHBAN_CONFIG")); cfg != "" {
		argv = append(argv, "--preserve-env=DEZHBAN_CONFIG")
	}
	argv = append(argv, self)
	argv = append(argv, os.Args[1:]...)
	return syscall.Exec(sudo, argv, os.Environ())
}

// elevatedWrite writes data to path as root via sudo, creating the parent dir.
func elevatedWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	script := fmt.Sprintf("set -e; mkdir -p %s; cat > %s; chmod 0644 %s",
		shQuote(dir), shQuote(path), shQuote(path))
	fmt.Fprintf(os.Stderr, "writing %s needs root — using sudo…\n", path)
	cmd := exec.Command("sudo", "sh", "-c", script)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// shQuote single-quotes s for safe interpolation into a /bin/sh command.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
