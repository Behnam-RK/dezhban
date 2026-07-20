//go:build windows

package main

import "errors"

// Windows has no sudo; elevation there is UAC, a separate mechanism not wired up.
// Keep the clear "must run as root" error path instead.
func canElevate() bool { return false }

func reexecElevated() error {
	return errors.New("auto-elevation is not supported on Windows; run from an elevated (Administrator) prompt")
}

// elevatedWrite is unreachable on Windows (writeConfig only calls it when
// canElevate is true), but must exist for the build.
func elevatedWrite(path string, data []byte) error {
	return errors.New("auto-elevation is not supported on Windows")
}

// sudoTouchIDConfigured: Touch ID is a macOS concept; doctor's hint is gated on
// darwin, but the symbol must exist for the build.
func sudoTouchIDConfigured() bool { return false }
