package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/behnam-rk/dezhban/internal/config"
)

// seedConfig writes a valid default config and returns its path.
func seedConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dezhban.json")
	def := config.Default()
	if err := config.Save(path, &def); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return path
}

// writeConfigKeys is what the daemon serves config-write with, so what it accepts
// is what an authorised socket client can change. It must land on disk in a form
// a fresh Load reads back identically — anything less would reintroduce the
// "saved but not in force" bug from the other direction.
func TestWriteConfigKeysRoundTripsThroughTheFile(t *testing.T) {
	path := seedConfig(t)

	// Values are given in the canonical form Normalize produces, so the
	// comparison below tests persistence rather than re-testing normalisation.
	pairs := map[string]string{
		"pollInterval":           "17s",
		"blockedCountries":       "IR,CN",
		"vpn.switchWindow":       "45s",
		"vpn.pauseMax":           "1m0s",
		"control.allowSwitchOps": "false",
		"control.allowConfigOps": "true",
		"vpn.allowLocalNetwork":  "false",
	}

	if err := writeConfigKeys(path, pairs); err != nil {
		t.Fatalf("writeConfigKeys: %v", err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for key, want := range pairs {
		if have := configFields[key].get(got); have != want {
			t.Errorf("%s = %q after write+reload, want %q", key, have, want)
		}
	}
}

// An unknown key is refused by name, never silently dropped. A GUI that misspells
// a key must hear about it rather than believe the setting took.
func TestWriteConfigKeysRefusesAnUnknownKey(t *testing.T) {
	path := seedConfig(t)
	before, _ := os.ReadFile(path)

	err := writeConfigKeys(path, map[string]string{"pollInterval": "9s", "vpn.notAKey": "x"})
	if err == nil {
		t.Fatal("an unknown key was accepted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("a refused batch still modified the config file")
	}
}

// One bad value rejects the whole batch. Otherwise a multi-key GUI save could
// persist half a change set and leave the config in a state the user never asked
// for — the reason `config set` validates once over the finished config.
func TestWriteConfigKeysRejectsTheWholeBatchOnABadValue(t *testing.T) {
	path := seedConfig(t)
	before, _ := os.ReadFile(path)

	err := writeConfigKeys(path, map[string]string{
		"blockedCountries": "ru",
		"pollInterval":     "not-a-duration",
	})
	if err == nil {
		t.Fatal("an invalid duration was accepted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("a rejected batch persisted its valid keys; the write must be all-or-nothing")
	}
}

// The config file is what arms the guard at boot, so a save interrupted partway
// must never leave a truncated file behind. Staging in the same directory and
// renaming is what guarantees that; this pins the two observable consequences —
// the published mode, and no debris left in the directory.
func TestConfigSaveIsAtomicAndWorldReadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.json")
	def := config.Default()
	if err := config.Save(path, &def); err != nil {
		t.Fatalf("save: %v", err)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o644 {
		t.Errorf("config mode = %o, want 0644 (unprivileged tools read this file)", perm)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only the config itself (a staging file was left behind)", names)
	}
}
