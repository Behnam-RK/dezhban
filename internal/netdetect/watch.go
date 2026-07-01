package netdetect

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"time"
)

// TunnelState is a sampled view of the configured tunnel interface(s): Up is
// true when at least one is a usable tunnel (per isTunnelIface). Name is the
// interface that satisfied the check (empty when none is up or enumeration
// failed). Detail carries a short human reason for logs.
type TunnelState struct {
	Up     bool
	Name   string
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
		emittedUp := cur.Up
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
				if st.Up {
					downStreak = 0
					if !emittedUp { // down→up: emit immediately
						emittedUp = true
						if !w.send(ctx, ch, st) {
							return
						}
					}
				} else {
					downStreak++
					if emittedUp && downStreak >= downDebounce { // up→down: debounced
						emittedUp = false
						if !w.send(ctx, ch, st) {
							return
						}
					}
				}
			}
		}
	}()
	return ch
}

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
	for _, ifc := range ifaces {
		if len(tunnels) > 0 && !want[ifc.Name] {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		if isTunnelIface(ifc.Name, ifc.Flags, addrs) {
			return TunnelState{Up: true, Name: ifc.Name, Detail: ifc.Name + " up"}
		}
	}
	return TunnelState{Up: false, Detail: "no configured tunnel is up"}
}
