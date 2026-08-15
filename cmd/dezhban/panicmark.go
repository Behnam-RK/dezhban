package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/behnam-rk/dezhban/internal/atomicfile"
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
//
// Atomic (temp + fsync + rename), same convention as internal/armed's Save
// and learned.json for this class of daemon-owned state-dir file: a crash
// mid-write must never leave the marker at a looser mode than 0600, or
// missing content a future caller starts relying on.
func setPanicMarker(dir string) error {
	body := []byte("panic ran at " + time.Now().UTC().Format(time.RFC3339) + "\n")
	if err := atomicfile.Write(panicMarkerPath(dir), body, 0o600); err != nil {
		return fmt.Errorf("panic marker: write: %w", err)
	}
	return nil
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

// clearPanicMarkerBestEffort clears the marker and reports a failure through
// warn, never through a returned error — every caller treats this clear as
// best-effort (see clearPanicMarker's doc comment) but each logs the failure
// through its own surface (structured daemon log vs. CLI stderr), so the
// logging call is left to warn rather than fixed here.
//
// internal/runner/runner.go has its own clearPanicDisarmBestEffort with the
// same shape — not merged with this one. That side never sees dir or
// clearPanicMarker directly (it only holds the injected
// runner.Options.ClearPanicDisarm), and this is a main package, which
// nothing else can import — see that function's doc comment for the full
// reasoning.
func clearPanicMarkerBestEffort(dir string, warn func(err error)) {
	if err := clearPanicMarker(dir); err != nil {
		warn(err)
	}
}

// panicMarkerPresent reports whether panic's marker is currently set. Only a
// definite os.IsNotExist counts as absent — any other stat error (a
// transient I/O or permission blip) is indistinguishable from "still there"
// and must not be read as absent, or a running daemon's enforcement
// verification would silently re-apply the very posture panic just tore
// down.
func panicMarkerPresent(dir string) bool {
	_, err := os.Stat(panicMarkerPath(dir))
	if err == nil {
		return true
	}
	return !os.IsNotExist(err)
}
