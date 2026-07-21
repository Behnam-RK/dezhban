package update

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/state"
)

func writeSnap(t *testing.T, dir string, snap state.Snapshot) string {
	t.Helper()
	path := filepath.Join(dir, "state.json")
	if err := state.Write(path, snap); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCanActivate(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name   string
		snap   state.Snapshot
		wantOK bool
	}{
		{"healthy guard", state.Snapshot{Time: time.Now(), Posture: "guard", PollIntervalSeconds: 30}, true},
		{"standby", state.Snapshot{Time: time.Now(), Posture: "standby", PollIntervalSeconds: 30}, true},
		{"full block refuses", state.Snapshot{Time: time.Now(), Posture: "full-block", PollIntervalSeconds: 30}, false},
		{"switch window refuses", state.Snapshot{Time: time.Now(), Posture: "switch-window", PollIntervalSeconds: 30}, false},
		{"stopped refuses", state.Snapshot{Time: time.Now(), Posture: "stopped", PollIntervalSeconds: 30}, false},
		{"stale guard refuses", state.Snapshot{Time: time.Now().Add(-time.Hour), Posture: "guard", PollIntervalSeconds: 30}, false},
		{"unknown posture refuses", state.Snapshot{Time: time.Now(), Posture: "something-new", PollIntervalSeconds: 30}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeSnap(t, dir, c.snap)
			res := CanActivate(path)
			if res.OK != c.wantOK {
				t.Errorf("CanActivate() = %+v, want OK=%v", res, c.wantOK)
			}
			if res.Reason == "" {
				t.Error("Reason must always be set")
			}
		})
	}
}

func TestCanActivateMissingSnapshot(t *testing.T) {
	res := CanActivate(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if res.OK {
		t.Error("a missing snapshot must never be treated as safe")
	}
}

func TestCanActivateNoPollInterval(t *testing.T) {
	// PollIntervalSeconds absent (0) falls back to staleFallback rather than a
	// zero budget that would reject every snapshot outright.
	dir := t.TempDir()
	path := writeSnap(t, dir, state.Snapshot{Time: time.Now(), Posture: "guard"})
	if res := CanActivate(path); !res.OK {
		t.Errorf("expected OK with no PollIntervalSeconds and a fresh timestamp, got %+v", res)
	}
}
