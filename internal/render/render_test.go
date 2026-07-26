package render

import (
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/state"
)

func TestText(t *testing.T) {
	until := time.Date(2026, 7, 25, 15, 4, 0, 0, time.UTC)

	cases := []struct {
		name         string
		snap         state.Snapshot
		wantKey      string
		wantHeadline string
		wantDetail   string
	}{
		{
			name:         "stopped",
			snap:         state.Snapshot{Posture: PostureStopped},
			wantKey:      KeyOff,
			wantHeadline: "Stopped",
			wantDetail:   "dezhban is not running. Nothing is being blocked.",
		},
		{
			name:         "standby",
			snap:         state.Snapshot{Posture: PostureStandby},
			wantKey:      KeyOff,
			wantHeadline: "Standby — nothing is being blocked.",
			wantDetail:   "Connect your VPN and the guard arms itself.",
		},
		{
			name:         "guard healthy",
			snap:         state.Snapshot{Posture: PostureGuard, Tunnels: []state.Tunnel{{Name: "utun4", Up: true}}},
			wantKey:      KeyOn,
			wantHeadline: "Guarding",
			wantDetail:   "Traffic leaves only through your VPN tunnel.",
		},
		{
			name:         "guard holds downed tunnel, no tunnels observed",
			snap:         state.Snapshot{Posture: PostureGuard},
			wantKey:      KeyBlocked,
			wantHeadline: "VPN down — traffic cut",
			wantDetail:   "Guard active, but no tunnel is up — all traffic is cut until your VPN redials.",
		},
		{
			name: "guard holds downed tunnel, all tunnels down",
			snap: state.Snapshot{Posture: PostureGuard, Tunnels: []state.Tunnel{
				{Name: "utun4", Up: false}, {Name: "utun5", Up: false},
			}},
			wantKey:      KeyBlocked,
			wantHeadline: "VPN down — traffic cut",
			wantDetail:   "Guard active, but no tunnel is up — all traffic is cut until your VPN redials.",
		},
		{
			name:         "full block with country",
			snap:         state.Snapshot{Posture: PostureFullBlock, CountryCode: "IR"},
			wantKey:      KeyBlocked,
			wantHeadline: "Full block (IR)",
			wantDetail:   "Your VPN is exiting through a country you've blocked (IR). Everything is cut until it moves.",
		},
		{
			name:         "full block without country",
			snap:         state.Snapshot{Posture: PostureFullBlock},
			wantKey:      KeyBlocked,
			wantHeadline: "Full block",
			wantDetail:   "Your VPN is exiting through a blocked country. Everything is cut until it moves.",
		},
		{
			name: "manual switch window",
			snap: state.Snapshot{Posture: PostureSwitchWindow, Switch: &state.SwitchState{
				Open: true, Until: until, Trigger: state.TriggerManual,
			}},
			wantKey:      KeyWarning,
			wantHeadline: "Switch window open",
			wantDetail:   "Guard relaxed so a new VPN can connect — your real IP may be exposed until it closes (3:04PM).",
		},
		{
			name: "automatic redial window",
			snap: state.Snapshot{Posture: PostureSwitchWindow, Switch: &state.SwitchState{
				Open: true, Until: until, Trigger: state.TriggerAuto,
			}},
			wantKey:      KeyWarning,
			wantHeadline: "Redial window open",
			wantDetail:   "Your VPN dropped. The guard is relaxed while it redials — your real IP may be exposed until it closes (3:04PM).",
		},
		{
			name: "pause window",
			snap: state.Snapshot{Posture: PostureSwitchWindow, Switch: &state.SwitchState{
				Open: true, Until: until, Trigger: state.TriggerPause,
			}},
			wantKey:      KeyPaused,
			wantHeadline: "Paused",
			wantDetail:   "Using your real IP at your request. The guard re-arms automatically at 3:04PM.",
		},
		{
			name:         "switch window with no Switch struct falls back to manual wording",
			snap:         state.Snapshot{Posture: PostureSwitchWindow},
			wantKey:      KeyWarning,
			wantHeadline: "Switch window open",
			wantDetail:   "Guard relaxed so a new VPN can connect — your real IP may be exposed until it closes.",
		},
		{
			name:         "unknown posture",
			snap:         state.Snapshot{Posture: "something-new"},
			wantKey:      KeyWarning,
			wantHeadline: "Unknown posture",
			wantDetail:   `dezhban reported an unrecognised posture ("something-new").`,
		},
		{
			name: "enforcement error wins over posture",
			snap: state.Snapshot{
				Posture:        PostureGuard,
				Tunnels:        []state.Tunnel{{Name: "utun4", Up: true}},
				EnforcementErr: "backend apply failed: exit status 1",
			},
			wantKey:      KeyWarning,
			wantHeadline: "Enforcement failed",
			wantDetail:   "backend apply failed: exit status 1",
		},
		{
			name: "lookup error appended to guard detail",
			snap: state.Snapshot{
				Posture:   PostureGuard,
				Tunnels:   []state.Tunnel{{Name: "utun4", Up: true}},
				LookupErr: "malformed response",
			},
			wantKey:      KeyOn,
			wantHeadline: "Guarding",
			wantDetail:   "Traffic leaves only through your VPN tunnel. Last exit-country check failed: malformed response.",
		},
		{
			name: "exit-unknown never surfaced",
			snap: state.Snapshot{
				Posture:     PostureGuard,
				ExitUnknown: "no tunnel is up, so there is no VPN exit to check",
			},
			wantKey:      KeyBlocked,
			wantHeadline: "VPN down — traffic cut",
			wantDetail:   "Guard active, but no tunnel is up — all traffic is cut until your VPN redials.",
		},
		{
			name: "pending restore appended",
			snap: state.Snapshot{
				Posture: PostureFullBlock,
				Pending: &state.PendingFlip{To: PostureGuard, Have: 1, Need: 2},
			},
			wantKey:      KeyBlocked,
			wantHeadline: "Full block",
			wantDetail:   "Your VPN is exiting through a blocked country. Everything is cut until it moves. Restoring the guard — 1 of 2 confirming checks.",
		},
		{
			name: "pending escalation appended",
			snap: state.Snapshot{
				Posture: PostureGuard,
				Tunnels: []state.Tunnel{{Name: "utun4", Up: true}},
				Pending: &state.PendingFlip{To: PostureFullBlock, Have: 2, Need: 2},
			},
			wantKey:      KeyOn,
			wantHeadline: "Guarding",
			wantDetail:   "Traffic leaves only through your VPN tunnel. Escalating to full block — 2 of 2 confirming checks.",
		},
		{
			name: "pending with have zero is not shown",
			snap: state.Snapshot{
				Posture: PostureStandby,
				Pending: &state.PendingFlip{To: PostureGuard, Have: 0, Need: 2},
			},
			wantKey:      KeyOff,
			wantHeadline: "Standby — nothing is being blocked.",
			wantDetail:   "Connect your VPN and the guard arms itself.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Text(tc.snap)
			if got.Key != tc.wantKey {
				t.Errorf("Key = %q, want %q", got.Key, tc.wantKey)
			}
			if got.Headline != tc.wantHeadline {
				t.Errorf("Headline = %q, want %q", got.Headline, tc.wantHeadline)
			}
			if got.Detail != tc.wantDetail {
				t.Errorf("Detail = %q, want %q", got.Detail, tc.wantDetail)
			}
		})
	}
}

// TestTextEnforcementErrWinsOverUnknownPosture pins that a snapshot carrying
// EnforcementErr short-circuits before postureDisplay ever runs, even for a
// posture string this package does not recognise — the error is more urgent
// than any posture prose, recognised or not.
func TestTextEnforcementErrWinsOverUnknownPosture(t *testing.T) {
	got := Text(state.Snapshot{Posture: "some-future-posture", EnforcementErr: "backend refused"})
	if got.Key != KeyWarning || got.Headline != "Enforcement failed" || got.Detail != "backend refused" {
		t.Errorf("got %+v, want the enforcement-failed display regardless of posture", got)
	}
}

// Posture is the variant for callers that append their own clause, so it must
// report the POSTURE and nothing else: no EnforcementErr short-circuit (which
// would put a raw backend error where `switch --status` prints the window
// sentence, right before "(profile …)"), and no appended lookup/hysteresis
// note (which would land between that sentence and the caller's clause).
func TestPostureIgnoresEnforcementErrAndNotes(t *testing.T) {
	until := time.Date(2026, 7, 25, 15, 4, 0, 0, time.UTC)
	snap := state.Snapshot{
		Posture:        PostureSwitchWindow,
		Switch:         &state.SwitchState{Open: true, Until: until, Trigger: state.TriggerManual},
		EnforcementErr: "pfctl: /dev/pf: Device busy",
		LookupErr:      "dial tcp: i/o timeout",
		Pending:        &state.PendingFlip{To: PostureFullBlock, Have: 1, Need: 2},
	}

	got := Posture(snap)
	want := "Guard relaxed so a new VPN can connect — your real IP may be exposed until it closes (3:04PM)."
	if got.Key != KeyWarning || got.Headline != "Switch window open" || got.Detail != want {
		t.Errorf("Posture() = %+v, want the window sentence alone (detail %q)", got, want)
	}

	// Text on the same snapshot is deliberately different — that is the whole
	// reason both exist, so pin the divergence rather than just the new one.
	if full := Text(snap); full.Headline != "Enforcement failed" {
		t.Errorf("Text() = %+v, want the enforcement failure to still win there", full)
	}
}

func TestStaleThreshold(t *testing.T) {
	cases := []struct {
		name string
		poll int
		want time.Duration
	}{
		{"absent poll interval falls back to the floor", 0, StaleFloor},
		{"short poll interval still floors at StaleFloor", 5, StaleFloor},
		{"long poll interval scales 3x", 60, 180 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StaleThreshold(state.Snapshot{PollIntervalSeconds: tc.poll})
			if got != tc.want {
				t.Errorf("StaleThreshold(poll=%d) = %v, want %v", tc.poll, got, tc.want)
			}
		})
	}
}

func TestIsStale(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

	fresh := state.Snapshot{Time: now.Add(-30 * time.Second), PollIntervalSeconds: 15}
	if IsStale(fresh, now) {
		t.Error("a snapshot well within its threshold should not read as stale")
	}

	dead := state.Snapshot{Time: now.Add(-time.Hour), PollIntervalSeconds: 15}
	if !IsStale(dead, now) {
		t.Error("a snapshot an hour old (crashed/SIGKILLed daemon) should read as stale")
	}
}
