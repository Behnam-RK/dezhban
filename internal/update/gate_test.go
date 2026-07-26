package update

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/render"
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

// up is a tunnel set with one live tunnel — what makes a "guard" snapshot a
// HEALTHY guard rather than one holding a downed tunnel. Spelled out in every
// guard case below because the difference is the whole safety argument for
// allowing a restart: with a tunnel up, routing carries traffic through it
// during the gap; with none up, the rules being torn down are the only thing
// stopping egress.
func up() []state.Tunnel { return []state.Tunnel{{Name: "utun4", Up: true}} }

func TestCanActivate(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name   string
		snap   state.Snapshot
		wantOK bool
	}{
		{"healthy guard", state.Snapshot{Time: time.Now(), Posture: "guard", Tunnels: up(), PollIntervalSeconds: 30}, true},
		{"standby", state.Snapshot{Time: time.Now(), Posture: "standby", PollIntervalSeconds: 30}, true},
		{"full block refuses", state.Snapshot{Time: time.Now(), Posture: "full-block", PollIntervalSeconds: 30}, false},
		{"switch window refuses", state.Snapshot{Time: time.Now(), Posture: "switch-window", PollIntervalSeconds: 30}, false},
		{"stopped refuses", state.Snapshot{Time: time.Now(), Posture: "stopped", PollIntervalSeconds: 30}, false},
		{"stale guard refuses", state.Snapshot{Time: time.Now().Add(-time.Hour), Posture: "guard", Tunnels: up(), PollIntervalSeconds: 30}, false},
		{"unknown posture refuses", state.Snapshot{Time: time.Now(), Posture: "something-new", PollIntervalSeconds: 30}, false},

		// The posture string "guard" covers two states with opposite safety
		// arguments. Restarting through the second removes every rule while
		// nothing carries traffic through a tunnel — a real leak, caused by the
		// updater, and under vpn.armAtBoot: false the host stays open after.
		{"guard with no tunnel up refuses", state.Snapshot{
			Time: time.Now(), Posture: "guard", PollIntervalSeconds: 30,
			Tunnels: []state.Tunnel{{Name: "utun4", Up: false}},
		}, false},
		{"guard with an empty tunnel set refuses", state.Snapshot{
			Time: time.Now(), Posture: "guard", PollIntervalSeconds: 30,
		}, false},
		{"guard with one of several tunnels up is allowed", state.Snapshot{
			Time: time.Now(), Posture: "guard", PollIntervalSeconds: 30,
			Tunnels: []state.Tunnel{{Name: "utun4", Up: false}, {Name: "utun6", Up: true}},
		}, true},
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
	// PollIntervalSeconds absent (0) falls back to render.StaleFloor rather than
	// a zero budget that would reject every snapshot outright.
	dir := t.TempDir()
	path := writeSnap(t, dir, state.Snapshot{Time: time.Now(), Posture: "guard", Tunnels: up()})
	if res := CanActivate(path); !res.OK {
		t.Errorf("expected OK with no PollIntervalSeconds and a fresh timestamp, got %+v", res)
	}
}

// The staleness budget is render.StaleThreshold, not a local copy: a snapshot
// this gate calls fresh must be one `status` and the menubar app also call
// fresh. This used to allow 5m where they allowed 90s — a safety gate trusting
// state the display had already given up on.
func TestCanActivateStalenessMatchesTheRenderedThreshold(t *testing.T) {
	dir := t.TempDir()
	// No PollIntervalSeconds, so the floor applies: just past it must refuse.
	snap := state.Snapshot{Time: time.Now().Add(-render.StaleFloor - time.Minute), Posture: "guard", Tunnels: up()}
	path := writeSnap(t, dir, snap)
	if res := CanActivate(path); res.OK {
		t.Errorf("a snapshot the renderer calls stale was accepted: %+v", res)
	}
	if !render.IsStale(snap, time.Now()) {
		t.Error("premise broken: render.IsStale disagrees that this snapshot is stale")
	}
}
