package main

import (
	"os"
	"path/filepath"
	"time"
)

// panicMarkerName is the file whose presence tells a RUNNING daemon that
// `dezhban panic` tore down the firewall rules deliberately, and enforcement
// verification (vpn.advanced.verifyInterval) must stand down instead of
// silently re-applying the posture the operator just removed on purpose —
// see runner.Options.PanicDisarmed's doc comment for the full rationale and
// docs/usage/troubleshooting.md for the operator-facing workflow.
//
// Not tagged "dezhban" like the firewall rules (cmd/dezhban/lock_unix.go's
// runLockName follows the same convention for the same reason) — it lives in
// the state directory, deleted with the rest of it, never parsed by the
// firewall backend.
const panicMarkerName = "panic.marker"

func panicMarkerPath(dir string) string {
	return filepath.Join(dir, panicMarkerName)
}

// setPanicMarker records that panic ran. Root-owned and 0600: a marker any
// local user could create would let them suppress verification's self-healing
// after something else — accidentally or maliciously — removed the rules,
// which is exactly the silent-failure mode verification exists to close.
// Best-effort — a failure here must never fail `panic` itself, since the
// teardown it protects is the half of the command that actually matters.
func setPanicMarker(dir string) error {
	body := []byte("panic ran at " + time.Now().UTC().Format(time.RFC3339) + "\n")
	if err := os.WriteFile(panicMarkerPath(dir), body, 0o600); err != nil {
		return err
	}
	// WriteFile's mode only applies to a NEWLY-created file; enforce 0600
	// unconditionally in case the marker somehow already existed looser.
	return os.Chmod(panicMarkerPath(dir), 0o600)
}

// clearPanicMarker removes the marker — called when an operator explicitly
// asks to resume enforcement (`unblock`) or a fresh daemon start re-arms on
// its own (a restart already re-applies the initial posture unconditionally,
// so a marker surviving past it would silently suppress verification forever
// until someone thought to run `unblock`). Clearing a marker that was never
// set is not an error.
func clearPanicMarker(dir string) error {
	err := os.Remove(panicMarkerPath(dir))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// panicMarkerPresent reports whether panic's marker is currently set.
func panicMarkerPresent(dir string) bool {
	_, err := os.Stat(panicMarkerPath(dir))
	return err == nil
}
