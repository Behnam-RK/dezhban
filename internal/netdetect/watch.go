package netdetect

import (
	"context"
	"log/slog"
	"net"
	"sort"
	"strings"
	"time"
)

// TunnelState is a sampled view of the configured tunnel interface(s): Up is
// true when at least one is a usable tunnel (per isTunnelIface). Name is the
// interface that satisfied the check (empty when none is up or enumeration
// failed). Detail carries a short human reason for logs.
type TunnelState struct {
	Up bool
	// Name is the first (sorted) tunnel that satisfied the check, kept for
	// backward-compatible logging; "" when none is up.
	Name string
	// Names is every interface that satisfied isTunnelIface this sample, sorted.
	// The runner uses set changes here to grow/prune its dynamic tunnel set.
	Names  []string
	Detail string
}

// Watcher samples the tunnel interface(s) on a short interval and emits an event
// on every up/down edge, so the runner can react to a VPN drop the instant it
// happens instead of waiting for the next geo poll. Up edges fire immediately;
// down edges are debounced (a tunnel that flaps for a tick must not flip the
// network) — an asymmetry that biases toward staying protected.
type Watcher struct {
	Tunnels  []string
	Interval time.Duration // sample period; <=0 → 1s
	// Sample reports the current tunnel state. nil → liveSample over
	// net.Interfaces. Injectable so the watcher (and the tunnel-drop simulation)
	// run with no real VPN.
	Sample func(tunnels []string) TunnelState
	// DownDebounce is the count of consecutive down samples required before a
	// down edge is emitted. <=0 → 2.
	DownDebounce int
	Log          *slog.Logger
}

// Watch starts the sampling loop and returns a channel of edge events. The first
// value sent is the initial observed state (so the consumer learns the starting
// posture); every later value is an up/down transition. The channel is closed
// when ctx is cancelled.
func (w *Watcher) Watch(ctx context.Context) <-chan TunnelState {
	ch := make(chan TunnelState, 1)

	sample := w.Sample
	if sample == nil {
		sample = liveSample
	}
	interval := w.Interval
	if interval <= 0 {
		interval = time.Second
	}
	downDebounce := w.DownDebounce
	if downDebounce <= 0 {
		downDebounce = 2
	}

	go func() {
		defer close(ch)

		cur := sample(w.Tunnels)
		emitted := cur
		downStreak := 0
		if !cur.Up {
			downStreak = downDebounce
		}
		if !w.send(ctx, ch, cur) {
			return
		}

		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				st := sample(w.Tunnels)
				// Grow / down→up is emitted immediately (a new tunnel must be
				// guarded at once); a set that lost members or went down is
				// debounced (a flapping reconnect must not churn rule reloads).
				grew := setGrew(emitted.Names, st.Names)
				shrankOrDown := (!st.Up && emitted.Up) || setShrank(emitted.Names, st.Names)
				if st.Up && (grew || (!emitted.Up)) {
					downStreak = 0
					emitted = st
					if !w.send(ctx, ch, st) {
						return
					}
				} else if shrankOrDown {
					downStreak++
					if downStreak >= downDebounce {
						downStreak = 0
						emitted = st
						if !w.send(ctx, ch, st) {
							return
						}
					}
				} else {
					downStreak = 0
				}
			}
		}
	}()
	return ch
}

// setGrew reports whether next contains a name not in prev.
func setGrew(prev, next []string) bool {
	have := make(map[string]bool, len(prev))
	for _, n := range prev {
		have[n] = true
	}
	for _, n := range next {
		if !have[n] {
			return true
		}
	}
	return false
}

// setShrank reports whether prev contains a name not in next.
func setShrank(prev, next []string) bool { return setGrew(next, prev) }

// send delivers st unless ctx is cancelled. Returns false if it should stop.
func (w *Watcher) send(ctx context.Context, ch chan<- TunnelState, st TunnelState) bool {
	select {
	case ch <- st:
		return true
	case <-ctx.Done():
		return false
	}
}

// SampleTunnels reports the current tunnel state for the given interfaces using
// the live interface list — the read-only snapshot the `monitor` command and the
// default watcher share.
func SampleTunnels(tunnels []string) TunnelState { return liveSample(tunnels) }

// liveSample reports whether any configured tunnel is currently a usable tunnel.
// When Tunnels is empty it considers every interface (autodetect). A failure to
// enumerate interfaces reports Up (a read hiccup is not evidence of a drop, and
// guard mode's standing rule still blocks real leaks) — the debounce would
// absorb a spurious one-shot anyway.
func liveSample(tunnels []string) TunnelState {
	ifaces, err := net.Interfaces()
	if err != nil {
		return TunnelState{Up: true, Detail: "interface enumeration failed: " + err.Error()}
	}
	want := make(map[string]bool, len(tunnels))
	for _, t := range tunnels {
		want[strings.TrimSpace(t)] = true
	}
	var names []string
	for _, ifc := range ifaces {
		if len(tunnels) > 0 && !want[ifc.Name] {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		if isTunnelIface(ifc.Name, ifc.Flags, addrs) {
			names = append(names, ifc.Name)
		}
	}
	if len(names) == 0 {
		return TunnelState{Up: false, Detail: "no configured tunnel is up"}
	}
	sort.Strings(names)
	return TunnelState{Up: true, Name: names[0], Names: names, Detail: strings.Join(names, ",") + " up"}
}
