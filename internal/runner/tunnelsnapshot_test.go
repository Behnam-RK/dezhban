package runner

import (
	"strings"
	"testing"

	"github.com/behnam-rk/dezhban/internal/netdetect"
)

// state.json's tunnels[].name is a field the diagnostic bundle redacts BY KEY,
// and a comma-joined value defeats that twice over: keepIface cannot see
// "utun4,nordlynx" as the kernel vocabulary it half is, so the structural half
// stops being kept, and the whole pair mints ONE token, so the bundle reports
// two interfaces as one identity.
func TestEachPublishedTunnelNamesExactlyOneInterface(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   netdetect.TunnelState
		cfg  []string
		want []string
	}{
		{
			name: "a down edge falls back to every configured tunnel",
			st:   netdetect.TunnelState{Up: false, Detail: "no configured tunnel is up"},
			cfg:  []string{"utun4", "nordlynx"},
			want: []string{"utun4", "nordlynx"},
		},
		{
			name: "an up edge publishes every interface the watcher saw",
			st: netdetect.TunnelState{
				Up: true, Name: "nordlynx", Names: []string{"nordlynx", "utun4"},
				Detail: "nordlynx,utun4 up",
			},
			cfg:  []string{"utun4", "nordlynx"},
			want: []string{"nordlynx", "utun4"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tunnelSnapshot(tc.st, tc.cfg)
			if len(got) != len(tc.want) {
				t.Fatalf("published %d entries, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, tun := range got {
				if strings.ContainsAny(tun.Name, ", ") {
					t.Errorf("entry %d names more than one interface: %q", i, tun.Name)
				}
				if tun.Name != tc.want[i] {
					t.Errorf("entry %d name = %q, want %q", i, tun.Name, tc.want[i])
				}
			}
		})
	}
}

// Detail used to repeat the interface name that Name already carries — the same
// word one key over, landing where only a literal pass over free text can reach
// it. The watcher's own Detail is untouched; this is about what gets published.
func TestAPublishedTunnelDoesNotRepeatItsOwnNameInItsDetail(t *testing.T) {
	st := netdetect.TunnelState{
		Up: true, Name: "nordlynx", Names: []string{"nordlynx"}, Detail: "nordlynx up",
	}
	for _, tun := range tunnelSnapshot(st, []string{"nordlynx"}) {
		if strings.Contains(tun.Detail, "nordlynx") {
			t.Errorf("detail %q repeats the interface name", tun.Detail)
		}
		if tun.Detail == "" {
			t.Error("the up/down answer was dropped along with the name")
		}
	}
}

// A host with nothing configured and nothing observed still gets one entry, so a
// consumer reading tunnels[0] sees the up/down answer rather than an empty list.
func TestATunnelSnapshotIsNeverEmpty(t *testing.T) {
	got := tunnelSnapshot(netdetect.TunnelState{Up: false, Detail: "no tunnel interface is up"}, nil)
	if len(got) != 1 {
		t.Fatalf("published %d entries, want 1: %+v", len(got), got)
	}
	if got[0].Name != "" || got[0].Up {
		t.Errorf("entry = %+v, want an unnamed down entry", got[0])
	}
}
