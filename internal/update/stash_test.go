package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStashRestoreFile(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "dezhban")
	if err := os.WriteFile(src, []byte("v1 binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	stashDir := filepath.Join(root, StashDirName)
	if err := StashFile(stashDir, src); err != nil {
		t.Fatalf("StashFile: %v", err)
	}
	if !HasStash(stashDir) {
		t.Fatal("HasStash false right after stashing")
	}

	// Simulate the binary being replaced by a new version.
	if err := os.WriteFile(src, []byte("v2 binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := RestoreFile(stashDir, "dezhban", src); err != nil {
		t.Fatalf("RestoreFile: %v", err)
	}
	got, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v1 binary" {
		t.Errorf("restored content = %q, want %q", got, "v1 binary")
	}
	info, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("restored mode = %v, want 0755", info.Mode().Perm())
	}

	if err := ClearStash(stashDir); err != nil {
		t.Fatalf("ClearStash: %v", err)
	}
	if HasStash(stashDir) {
		t.Error("HasStash true after ClearStash")
	}
}

func TestStashRestoreDir(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "Dezhban.app")
	mustMkdirAll(t, filepath.Join(app, "Contents", "MacOS"))
	mustWriteFile(t, filepath.Join(app, "Contents", "MacOS", "DezhbanMenu"), "v1 app binary")
	mustWriteFile(t, filepath.Join(app, "Contents", "Info.plist"), "v1 plist")

	stashDir := filepath.Join(root, StashDirName)
	if err := StashDir(stashDir, app); err != nil {
		t.Fatalf("StashDir: %v", err)
	}

	// Simulate the app bundle being replaced by a new version (different
	// structure entirely — restore must not leave any v2 leftovers behind).
	if err := os.RemoveAll(app); err != nil {
		t.Fatal(err)
	}
	mustMkdirAll(t, filepath.Join(app, "Contents", "MacOS"))
	mustWriteFile(t, filepath.Join(app, "Contents", "MacOS", "DezhbanMenu"), "v2 app binary")
	mustMkdirAll(t, filepath.Join(app, "Contents", "Resources"))
	mustWriteFile(t, filepath.Join(app, "Contents", "Resources", "new-in-v2.png"), "v2 only")

	if err := RestoreDir(stashDir, "Dezhban.app", app); err != nil {
		t.Fatalf("RestoreDir: %v", err)
	}

	got := mustReadFile(t, filepath.Join(app, "Contents", "MacOS", "DezhbanMenu"))
	if got != "v1 app binary" {
		t.Errorf("restored MacOS binary = %q, want v1", got)
	}
	if _, err := os.Stat(filepath.Join(app, "Contents", "Resources", "new-in-v2.png")); !os.IsNotExist(err) {
		t.Error("v2-only file survived the restore — RestoreDir must fully replace, not merge")
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestHasStashEmpty(t *testing.T) {
	if HasStash(filepath.Join(t.TempDir(), "nope")) {
		t.Error("HasStash true for a directory that does not exist")
	}
}

func TestClassifyStash(t *testing.T) {
	cases := []struct {
		name             string
		stashed, running string
		want             StashVerdict
	}{
		{"stashed older, plain vX.Y.Z", "v0.4.0", "v0.5.0", StashObsolete},
		{"stashed older, no leading v either side", "0.4.0", "0.5.0", StashObsolete},
		{"stashed equals running — still pending activation", "v0.5.0", "v0.5.0", StashPending},
		{"stashed newer than running — should never happen, refuse", "v0.6.0", "v0.5.0", StashUnknown},
		{"stashed is a dev build", "v0.4.0-3-gabc123-dirty", "v0.5.0", StashUnknown},
		{"running is a dev build", "v0.4.0", "v0.5.0-3-gabc123-dirty", StashUnknown},
		{"both empty (no snapshot and unreadable stash)", "", "", StashUnknown},
		{"stashed empty only", "", "v0.5.0", StashUnknown},
		// A daemon predating state.Snapshot.Version publishes no version at
		// all. That must refuse, not sail through on a zero value.
		{"running empty — daemon stopped or too old to report a version", "v0.4.0", "", StashUnknown},
		{"an rc compares by its base core, same as normalizeVersion", "v0.4.0-rc.1", "v0.5.0", StashObsolete},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyStash(c.stashed, c.running); got != c.want {
				t.Errorf("ClassifyStash(%q, %q) = %v, want %v", c.stashed, c.running, got, c.want)
			}
		})
	}
}

// TestClassifyStashDeferredActivationIsPending is the regression guard for the
// bug this comparison originally shipped with: classifying against the version
// on DISK instead of the version RUNNING.
//
// Replay the deferred-activation sequence. `upgrade apply` stashes the running
// v0.4.0, the installer writes v0.5.0 to /usr/local/bin/dezhban, and then
// activation is deferred (--no-activate, or the gate refusing during FULL
// BLOCK). At that instant disk says v0.5.0 while the daemon is still executing
// v0.4.0 on its old inode — that divergence is the whole point of the two-phase
// design, not an edge case.
//
// Compared against disk, the stash looks strictly older and gets classified
// StashObsolete — so the next `upgrade apply` would DELETE the only copy of
// v0.4.0, the last version known to have run, and then stash the never-yet-run
// v0.5.0 as its "rollback" target. Compared against the running version, it is
// correctly StashPending: refuse, and tell the operator to finish activating.
func TestClassifyStashDeferredActivationIsPending(t *testing.T) {
	const (
		stashed   = "v0.4.0" // what the daemon is still executing
		onDisk    = "v0.5.0" // what the installer just wrote — NOT running yet
		running   = stashed
		activated = onDisk // what runs after `sudo dezhban restart`
	)

	if got := ClassifyStash(stashed, running); got != StashPending {
		t.Errorf("deferred activation: ClassifyStash(%q, running=%q) = %v, want StashPending — "+
			"the stash is the only copy of the running version and must not be cleared", stashed, running, got)
	}
	if got := ClassifyStash(stashed, onDisk); got == StashPending {
		t.Fatalf("test is not exercising the bug: comparing against the on-disk version %q "+
			"should differ from comparing against the running one", onDisk)
	}

	// Once the operator actually activates, the same stash IS obsolete: the
	// daemon now reports the newer version, which is only possible if the
	// activation landed. That is the wedge this whole classification exists
	// to clear automatically.
	if got := ClassifyStash(stashed, activated); got != StashObsolete {
		t.Errorf("after activation: ClassifyStash(%q, running=%q) = %v, want StashObsolete", stashed, activated, got)
	}
}
