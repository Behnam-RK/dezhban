package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/state"
)

// TestPrintSwitchStatus pins printSwitchStatus's convergence onto
// internal/render: the shared sentence now comes from the renderer (so its
// wording and clock format can never drift from `status`'s), while the
// command-specific extras (the resume hint, the profile name) are appended
// on top rather than lost.
func TestPrintSwitchStatus(t *testing.T) {
	until := time.Date(2026, 7, 25, 15, 4, 0, 0, time.UTC)

	cases := []struct {
		name string
		snap *state.Snapshot // nil means no state file at all
		want string
	}{
		{
			name: "no state file",
			snap: nil,
			want: "switch window: unknown (no state file; is the daemon running?)\n",
		},
		{
			name: "closed",
			snap: &state.Snapshot{Posture: "guard"},
			want: "switch window: closed\n",
		},
		{
			name: "manual window with a profile",
			snap: &state.Snapshot{
				Posture: "switch-window",
				Switch: &state.SwitchState{
					Open: true, Until: until, Trigger: state.TriggerManual, Profile: "home-wg",
				},
			},
			want: "switch window: OPEN — Your real IP may be exposed until 3:04PM. The guard is relaxed so a new VPN can connect. (profile \"home-wg\")\n" +
				"until: 2026-07-25T15:04:00Z\n",
		},
		{
			name: "automatic redial window",
			snap: &state.Snapshot{
				Posture: "switch-window",
				Switch:  &state.SwitchState{Open: true, Until: until, Trigger: state.TriggerAuto},
			},
			want: "switch window: OPEN — Your real IP may be exposed until 3:04PM. Your VPN dropped and the guard relaxed so it can redial.\n" +
				"until: 2026-07-25T15:04:00Z\n",
		},
		{
			name: "pause window",
			snap: &state.Snapshot{
				Posture: "switch-window",
				Switch:  &state.SwitchState{Open: true, Until: until, Trigger: state.TriggerPause},
			},
			want: "pause: OPEN — You are using your real IP at your request. The guard re-arms automatically at 3:04PM. (end early with `dezhban resume`)\n" +
				"until: 2026-07-25T15:04:00Z\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if tc.snap != nil {
				if err := state.Write(path, *tc.snap); err != nil {
					t.Fatalf("state.Write: %v", err)
				}
			}
			out := captureStdout(t, func() { printSwitchStatus(path) })
			if out != tc.want {
				t.Errorf("output = %q, want %q", out, tc.want)
			}
		})
	}
}

// TestPrintSwitchStatusUnreadableStateFile pins the fallback message when the
// path exists but isn't valid JSON (as opposed to simply missing).
func TestPrintSwitchStatusUnreadableStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	out := captureStdout(t, func() { printSwitchStatus(path) })
	if !strings.Contains(out, "unknown") {
		t.Errorf("output = %q, want it to mention the state file is unreadable", out)
	}
}
