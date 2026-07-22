package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/state"
	"github.com/behnam-rk/dezhban/internal/update"
)

// TestWaitForHealthySnapshotStalePreRestartSnapshot pins the rule
// waitForHealthySnapshot's doc comment states: a snapshot published BEFORE the
// restart must never read as proof the NEW process is healthy, even if its
// posture and EnforcementErr look perfectly fine. Without the after-timestamp
// check, a slow daemon that hadn't published anything new yet would let this
// return "healthy" on stale, pre-upgrade data — exactly the false confidence
// that would clear the rollback stash for a version that never actually came
// up.
func TestWaitForHealthySnapshotStalePreRestartSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	restartedAt := time.Now()

	// Written BEFORE restartedAt on purpose.
	stale := state.Snapshot{
		Time:    restartedAt.Add(-1 * time.Hour),
		Posture: "guard",
	}
	if err := state.Write(path, stale); err != nil {
		t.Fatalf("state.Write: %v", err)
	}

	snap, healthy := waitForHealthySnapshot(path, restartedAt, 200*time.Millisecond)
	if healthy {
		t.Fatalf("waitForHealthySnapshot reported healthy from a pre-restart snapshot (posture %q, time %s)", snap.Posture, snap.Time)
	}
}

// TestWaitForHealthySnapshotFreshHealthy is the positive case: a snapshot
// published AFTER the restart, in a non-terminal posture, with no
// EnforcementErr, is exactly what a successfully activated new version looks
// like — waitForHealthySnapshot must return it immediately rather than
// polling out the full budget.
func TestWaitForHealthySnapshotFreshHealthy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	restartedAt := time.Now()

	fresh := state.Snapshot{
		Time:    restartedAt.Add(1 * time.Second),
		Posture: "guard",
	}
	if err := state.Write(path, fresh); err != nil {
		t.Fatalf("state.Write: %v", err)
	}

	start := time.Now()
	snap, healthy := waitForHealthySnapshot(path, restartedAt, 5*time.Second)
	if !healthy {
		t.Fatal("waitForHealthySnapshot reported unhealthy for a fresh, non-terminal, error-free snapshot")
	}
	if snap.Posture != "guard" {
		t.Errorf("snap.Posture = %q, want %q", snap.Posture, "guard")
	}
	if elapsed := time.Since(start); elapsed > 1*time.Second {
		t.Errorf("took %s to notice an already-healthy snapshot — should return on the first poll", elapsed)
	}
}

// TestWaitForHealthySnapshotStoppedPosture pins postureStopped: a fresh
// snapshot in the terminal "stopped" posture (the daemon published one final
// snapshot on its way down — see internal/runner.Run) must not read as
// healthy even though it postdates the restart and carries no
// EnforcementErr. Regression guard for the postureStopped rename — a typo'd
// literal here would silently defeat this check.
func TestWaitForHealthySnapshotStoppedPosture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	restartedAt := time.Now()

	stopped := state.Snapshot{
		Time:    restartedAt.Add(1 * time.Second),
		Posture: postureStopped,
	}
	if err := state.Write(path, stopped); err != nil {
		t.Fatalf("state.Write: %v", err)
	}

	_, healthy := waitForHealthySnapshot(path, restartedAt, 200*time.Millisecond)
	if healthy {
		t.Fatal("waitForHealthySnapshot reported healthy for a terminal \"stopped\" posture")
	}
}

// TestWaitForHealthySnapshotEnforcementErr pins the other half of "healthy":
// a fresh, non-terminal-posture snapshot that nonetheless carries a set
// EnforcementErr means the daemon TRIED to enforce and the backend rejected
// it — state.Snapshot's own doc comment says the intended posture was not
// actually achieved. Must not read as healthy.
func TestWaitForHealthySnapshotEnforcementErr(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	restartedAt := time.Now()

	failing := state.Snapshot{
		Time:           restartedAt.Add(1 * time.Second),
		Posture:        "guard",
		EnforcementErr: "pfctl: rule load failed",
	}
	if err := state.Write(path, failing); err != nil {
		t.Fatalf("state.Write: %v", err)
	}

	_, healthy := waitForHealthySnapshot(path, restartedAt, 200*time.Millisecond)
	if healthy {
		t.Fatal("waitForHealthySnapshot reported healthy despite a set EnforcementErr")
	}
}

// TestRunningVersionFromSnapshot pins where `upgrade apply` learns what is
// actually running: the snapshot the daemon publishes, not the binary on
// disk. See update.ClassifyStash's doc comment for why the distinction is
// load-bearing.
func TestRunningVersionFromSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := state.Write(path, state.Snapshot{
		Time:    time.Now(),
		Posture: "guard",
		Version: "v0.4.0",
	}); err != nil {
		t.Fatalf("state.Write: %v", err)
	}

	snap, err := state.Read(path)
	if err != nil {
		t.Fatalf("state.Read: %v", err)
	}
	if snap.Version != "v0.4.0" {
		t.Errorf("snap.Version = %q, want %q — Version must survive the JSON round trip", snap.Version, "v0.4.0")
	}
}

// TestRunningVersionUnknownRefuses covers the two ways the running version is
// undeterminable — no state file at all, and a snapshot from a daemon
// predating state.Snapshot.Version. Both must produce "", which
// update.ClassifyStash maps to StashUnknown, which makes `upgrade apply`
// refuse rather than guess. Silently reading "" as a version would classify
// every stash as unknown-but-comparable and is exactly the failure mode the
// "an undeterminable reading holds" rule exists to prevent.
func TestRunningVersionUnknownRefuses(t *testing.T) {
	dir := t.TempDir()

	// A daemon too old to publish a version.
	oldDaemon := filepath.Join(dir, "old.json")
	if err := state.Write(oldDaemon, state.Snapshot{Time: time.Now(), Posture: "guard"}); err != nil {
		t.Fatalf("state.Write: %v", err)
	}
	snap, err := state.Read(oldDaemon)
	if err != nil {
		t.Fatalf("state.Read: %v", err)
	}
	if snap.Version != "" {
		t.Errorf("snap.Version = %q, want empty for a versionless snapshot", snap.Version)
	}
	if got := update.ClassifyStash("v0.4.0", snap.Version); got != update.StashUnknown {
		t.Errorf("ClassifyStash with a versionless running snapshot = %v, want StashUnknown", got)
	}

	// No state file at all.
	if _, err := state.Read(filepath.Join(dir, "absent.json")); err == nil {
		t.Error("state.Read on a missing file returned nil error — runningVersion relies on this failing")
	}
}
