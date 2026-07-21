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
