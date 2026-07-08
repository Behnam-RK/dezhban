package netdetect

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestWatchEdges drives a scripted sample sequence and asserts the watcher emits
// the initial state, debounces a down edge (needs DownDebounce consecutive down
// samples), and emits an up edge immediately.
func TestWatchEdges(t *testing.T) {
	up := TunnelState{Up: true}
	down := TunnelState{Up: false}
	// initial=up, up, down, down(=down edge), down, up(=up edge)
	script := []TunnelState{up, up, down, down, down, up}

	var mu sync.Mutex
	i := 0
	sample := func([]string) TunnelState {
		mu.Lock()
		defer mu.Unlock()
		st := script[i]
		if i < len(script)-1 {
			i++
		}
		return st
	}

	w := &Watcher{Interval: time.Millisecond, DownDebounce: 2, Sample: sample}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := w.Watch(ctx)

	want := []bool{true, false, true} // initial up, debounced down, immediate up
	for n, wantUp := range want {
		select {
		case st, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed early after %d events", n)
			}
			if st.Up != wantUp {
				t.Fatalf("event %d: Up = %v, want %v", n, st.Up, wantUp)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for event %d (want Up=%v)", n, wantUp)
		}
	}
}

// A tunnel that is down at startup is reported as the initial state.
func TestWatchInitialDown(t *testing.T) {
	w := &Watcher{Interval: time.Millisecond, Sample: func([]string) TunnelState {
		return TunnelState{Up: false, Detail: "absent"}
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := w.Watch(ctx)
	select {
	case st := <-ch:
		if st.Up {
			t.Errorf("initial state Up = true, want false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial state")
	}
}

// A new tunnel appearing while another is already up is a set change with no
// up/down edge — it must be emitted immediately so the runner can guard it.
func TestWatchSetGrowthEmitsImmediately(t *testing.T) {
	one := TunnelState{Up: true, Name: "utun4", Names: []string{"utun4"}}
	two := TunnelState{Up: true, Name: "utun4", Names: []string{"utun4", "utun6"}}
	script := []TunnelState{one, one, two, two}

	var mu sync.Mutex
	i := 0
	sample := func([]string) TunnelState {
		mu.Lock()
		defer mu.Unlock()
		st := script[i]
		if i < len(script)-1 {
			i++
		}
		return st
	}
	w := &Watcher{Interval: time.Millisecond, DownDebounce: 2, Sample: sample}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := w.Watch(ctx)

	// initial {utun4}, then immediately {utun4,utun6} on growth.
	wantNames := [][]string{{"utun4"}, {"utun4", "utun6"}}
	for n, want := range wantNames {
		select {
		case st := <-ch:
			if strings.Join(st.Names, ",") != strings.Join(want, ",") {
				t.Fatalf("event %d: Names = %v, want %v", n, st.Names, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for event %d (want %v)", n, want)
		}
	}
}

// An Unknown sample (enumeration failed) must not be read as a set shrink: it
// carries empty Names, and treating that as a drop would churn the runner's
// dynamic tunnel set. A genuine shrink afterwards must still be emitted.
func TestWatchUnknownSampleIgnored(t *testing.T) {
	two := TunnelState{Up: true, Name: "utun4", Names: []string{"utun4", "utun6"}}
	unknown := TunnelState{Up: true, Unknown: true, Detail: "enumeration failed"}
	one := TunnelState{Up: true, Name: "utun4", Names: []string{"utun4"}}
	// After the initial {utun4,utun6}, three unknowns (which must be ignored, not
	// debounced into a shrink), then a real drop to {utun4}.
	script := []TunnelState{two, unknown, unknown, unknown, one, one, one, one}

	var mu sync.Mutex
	i := 0
	sample := func([]string) TunnelState {
		mu.Lock()
		defer mu.Unlock()
		st := script[i]
		if i < len(script)-1 {
			i++
		}
		return st
	}
	w := &Watcher{Interval: time.Millisecond, DownDebounce: 2, Sample: sample}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := w.Watch(ctx)

	// Exactly two events: the initial set, then the real shrink — never an empty
	// Names event from an unknown sample.
	wantNames := [][]string{{"utun4", "utun6"}, {"utun4"}}
	for n, want := range wantNames {
		select {
		case st := <-ch:
			if st.Unknown {
				t.Fatalf("event %d: watcher emitted an Unknown sample", n)
			}
			if len(st.Names) == 0 {
				t.Fatalf("event %d: watcher emitted empty Names (spurious shrink)", n)
			}
			if strings.Join(st.Names, ",") != strings.Join(want, ",") {
				t.Fatalf("event %d: Names = %v, want %v", n, st.Names, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for event %d (want %v)", n, want)
		}
	}
}
