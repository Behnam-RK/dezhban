package main

import (
	"testing"

	"github.com/behnam-rk/dezhban/internal/render"
	"github.com/behnam-rk/dezhban/internal/state"
)

// TestStampAndRender pins the daemon's publish choke point: every snapshot it
// writes carries a Version and a Display rendered from that exact same
// snapshot, and the Display's Key is always one of the five brand states the
// macOS app maps to icons/PNGs.
func TestStampAndRender(t *testing.T) {
	brandKeys := map[string]bool{
		render.KeyOn: true, render.KeyOff: true, render.KeyBlocked: true,
		render.KeyWarning: true, render.KeyPaused: true,
	}

	cases := []struct {
		name string
		in   state.Snapshot
	}{
		{"guard", state.Snapshot{Posture: render.PostureGuard, Tunnels: []state.Tunnel{{Name: "utun4", Up: true}}}},
		{"standby", state.Snapshot{Posture: render.PostureStandby}},
		{"full-block", state.Snapshot{Posture: render.PostureFullBlock, CountryCode: "IR"}},
		{"stopped", state.Snapshot{Posture: render.PostureStopped}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stampAndRender(tc.in, "v0.8.0-test")
			if got.Version != "v0.8.0-test" {
				t.Errorf("Version = %q, want %q", got.Version, "v0.8.0-test")
			}
			if got.Display == nil {
				t.Fatal("Display is nil, want it populated")
			}
			if got.Display.Headline == "" || got.Display.Detail == "" {
				t.Errorf("Display = %+v, want non-empty Headline and Detail", got.Display)
			}
			if !brandKeys[got.Display.Key] {
				t.Errorf("Display.Key = %q, want one of the five brand states", got.Display.Key)
			}
		})
	}
}
