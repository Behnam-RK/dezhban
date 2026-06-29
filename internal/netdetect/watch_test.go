package netdetect

import (
	"context"
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
