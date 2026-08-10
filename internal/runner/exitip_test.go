package runner

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/decision"
	"github.com/behnam-rk/dezhban/internal/monitor"
	"github.com/behnam-rk/dezhban/internal/state"
)

// Purely observational, like CVG's equivalent check: a change in the observed
// exit IP is published, but never flips posture and never touches the
// hysteresis streak (CountryCode/Pending already own that job). It exists
// because a failover between two servers in the same allowed country changes
// nothing CountryCode reports.
func TestExitIPChangeIsObservedAndPublished(t *testing.T) {
	be := &fakeBackend{}
	ip1 := netip.MustParseAddr("203.0.113.10")
	ip2 := netip.MustParseAddr("203.0.113.20")
	ctx, cancel := context.WithCancel(context.Background())
	mon := &fakeMonitor{cancel: cancel, results: []monitor.Result{
		{Reading: monitor.Reading{IP: ip1, CountryCode: "US"}}, // first reading: nothing to compare against
		{Reading: monitor.Reading{IP: ip1, CountryCode: "US"}}, // same IP: no change
		{Reading: monitor.Reading{IP: ip2, CountryCode: "US"}}, // different IP: a change
	}}
	var snaps []state.Snapshot
	o := Options{
		Monitor:   mon,
		Decider:   decision.New([]string{"IR"}, 1),
		Backend:   be,
		Log:       discardLog(),
		Interval:  time.Millisecond,
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{netip.MustParseAddr("198.51.100.7")},
		Publish:   func(s state.Snapshot) { snaps = append(snaps, s) },
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	if len(snaps) == 0 {
		t.Fatal("no snapshots published")
	}
	if !snaps[0].ExitIPChangedAt.IsZero() {
		t.Error("the very first reading was reported as a change; there was nothing yet to compare it against")
	}
	// Not snaps[len(snaps)-1]: the run's final publish is the terminal "stopped"
	// snapshot (publishStopped), a fresh minimal Snapshot that carries none of
	// the run loop's diagnostic state — so the change has to be found among the
	// snapshots the geo ticks themselves published, not assumed to be the last.
	var sawChange bool
	for _, s := range snaps {
		if !s.ExitIPChangedAt.IsZero() {
			sawChange = true
		}
	}
	if !sawChange {
		t.Error("ExitIPChangedAt was never set after the exit IP genuinely changed")
	}
}

// A failover between two servers in the same FORBIDDEN country still changes
// the exit IP — and the readings that observe it are exactly the ones FULL
// BLOCK is watching over, so excluding them here would leave "my exit
// flapped" unexplained for the one posture where an operator most wants to
// know. observeExitIP must run on every successful reading, not just an
// ALLOWED one.
func TestExitIPChangeIsObservedWhileBlocked(t *testing.T) {
	be := &fakeBackend{}
	ip1 := netip.MustParseAddr("203.0.113.10")
	ip2 := netip.MustParseAddr("203.0.113.20")
	ctx, cancel := context.WithCancel(context.Background())
	mon := &fakeMonitor{cancel: cancel, results: []monitor.Result{
		{Reading: monitor.Reading{IP: ip1, CountryCode: "IR"}}, // blocked country: nothing to compare against yet
		{Reading: monitor.Reading{IP: ip1, CountryCode: "IR"}}, // same IP, still blocked: no change
		{Reading: monitor.Reading{IP: ip2, CountryCode: "IR"}}, // different IP, still blocked: a change
	}}
	var snaps []state.Snapshot
	o := Options{
		Monitor:   mon,
		Decider:   decision.New([]string{"IR"}, 1),
		Backend:   be,
		Log:       discardLog(),
		Interval:  time.Millisecond,
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{netip.MustParseAddr("198.51.100.7")},
		Publish:   func(s state.Snapshot) { snaps = append(snaps, s) },
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}

	var sawBlocked, sawChange bool
	for _, s := range snaps {
		if s.Blocked {
			sawBlocked = true
		}
		if !s.ExitIPChangedAt.IsZero() {
			sawChange = true
		}
	}
	if !sawBlocked {
		t.Fatal("posture never escalated to FULL BLOCK; this fixture tests nothing")
	}
	if !sawChange {
		t.Error("ExitIPChangedAt was never set for an exit-IP change observed while blocked")
	}
}

// A steady exit IP across every reading must never be reported as a change.
func TestSteadyExitIPNeverReportsAChange(t *testing.T) {
	be := &fakeBackend{}
	ip := netip.MustParseAddr("203.0.113.10")
	ctx, cancel := context.WithCancel(context.Background())
	mon := &fakeMonitor{cancel: cancel, results: []monitor.Result{
		{Reading: monitor.Reading{IP: ip, CountryCode: "US"}},
		{Reading: monitor.Reading{IP: ip, CountryCode: "US"}},
		{Reading: monitor.Reading{IP: ip, CountryCode: "US"}},
	}}
	var snaps []state.Snapshot
	o := Options{
		Monitor:   mon,
		Decider:   decision.New([]string{"IR"}, 1),
		Backend:   be,
		Log:       discardLog(),
		Interval:  time.Millisecond,
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{netip.MustParseAddr("198.51.100.7")},
		Publish:   func(s state.Snapshot) { snaps = append(snaps, s) },
	}
	if err := Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	for _, s := range snaps {
		if !s.ExitIPChangedAt.IsZero() {
			t.Fatalf("a steady exit IP was reported as changed: %+v", s)
		}
	}
}
