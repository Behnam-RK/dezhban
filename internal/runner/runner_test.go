package runner

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"testing"

	"github.com/behnam-rk/dezhban/internal/decision"
	"github.com/behnam-rk/dezhban/internal/firewall"
	"github.com/behnam-rk/dezhban/internal/monitor"
)

// fakePoller emits a fixed sequence of results then closes the channel, ending
// the loop deterministically without HTTP or a real ticker.
type fakePoller struct {
	results []monitor.Result
}

func (f *fakePoller) Poll(ctx context.Context) <-chan monitor.Result {
	ch := make(chan monitor.Result)
	go func() {
		defer close(ch)
		for _, r := range f.results {
			select {
			case ch <- r:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}

// fakeBackend records the sequence of calls made against it.
type fakeBackend struct {
	calls    []string
	policies []firewall.Policy
}

func (b *fakeBackend) Apply(p firewall.Policy) error {
	b.policies = append(b.policies, p)
	if p.Mode == firewall.ModeGuard {
		b.calls = append(b.calls, "apply-guard")
	} else {
		b.calls = append(b.calls, "apply-fullblock")
	}
	return nil
}
func (b *fakeBackend) Block(a firewall.Allowlist) error {
	b.calls = append(b.calls, "block")
	return nil
}
func (b *fakeBackend) Unblock() error {
	b.calls = append(b.calls, "unblock")
	return nil
}
func (b *fakeBackend) Cleanup() error {
	b.calls = append(b.calls, "cleanup")
	return nil
}

func reading(cc string) monitor.Result {
	return monitor.Result{Reading: monitor.Reading{CountryCode: cc}}
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLegacyBlockThenUnblockIdempotent(t *testing.T) {
	be := &fakeBackend{}
	o := Options{
		Monitor: &fakePoller{results: []monitor.Result{
			reading("US"), // allow, not blocked → no-op
			reading("IR"), // block
			reading("IR"), // already blocked → no-op
			reading("US"), // unblock
			reading("US"), // already allowed → no-op
		}},
		Decider:   decision.New([]string{"IR"}),
		Backend:   be,
		Log:       discardLog(),
		Allowlist: func() firewall.Allowlist { return firewall.Allowlist{} },
	}
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	want := []string{"block", "unblock", "cleanup"}
	if !equal(be.calls, want) {
		t.Fatalf("calls = %v, want %v", be.calls, want)
	}
}

func TestLegacyErrorAllowsPhase3(t *testing.T) {
	be := &fakeBackend{}
	o := Options{
		Monitor: &fakePoller{results: []monitor.Result{
			reading("IR"), // block
			{Reading: monitor.Reading{CountryCode: "IR"}, Err: errors.New("all providers failed")}, // error → Allow → unblock
		}},
		Decider:   decision.New([]string{"IR"}),
		Backend:   be,
		Log:       discardLog(),
		Allowlist: func() firewall.Allowlist { return firewall.Allowlist{} },
	}
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	// Phase 3: a lookup error resolves to Allow, so a held block is released.
	want := []string{"block", "unblock", "cleanup"}
	if !equal(be.calls, want) {
		t.Fatalf("calls = %v, want %v", be.calls, want)
	}
}

func TestVPNGuardAtStartupThenToggle(t *testing.T) {
	be := &fakeBackend{}
	o := Options{
		Monitor: &fakePoller{results: []monitor.Result{
			reading("US"), // allow, already guard → no-op
			reading("IR"), // full block
			reading("IR"), // already full block → no-op
			reading("US"), // back to guard
		}},
		Decider:   decision.New([]string{"IR"}),
		Backend:   be,
		Log:       discardLog(),
		VPN:       true,
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{netip.MustParseAddr("203.0.113.7")},
	}
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	// startup guard, then fullblock, then guard, then cleanup.
	want := []string{"apply-guard", "apply-fullblock", "apply-guard", "cleanup"}
	if !equal(be.calls, want) {
		t.Fatalf("calls = %v, want %v", be.calls, want)
	}
	// Full-block policy under VPN must carry the tunnel ifaces (so the renderer
	// cuts the tunnel) and must NOT pass any dst-IP allowlist.
	var fb firewall.Policy
	found := false
	for _, p := range be.policies {
		if p.Mode == firewall.ModeFullBlock {
			fb, found = p, true
		}
	}
	if !found {
		t.Fatal("no full-block policy applied")
	}
	if len(fb.TunnelIfaces) == 0 {
		t.Error("VPN full block must carry tunnel ifaces")
	}
	if len(fb.Allowlist.DNS) != 0 || len(fb.Allowlist.Hosts) != 0 {
		t.Error("VPN full block must not carry a dst-IP allowlist")
	}
}

func TestVPNStartupGuardFailureAborts(t *testing.T) {
	be := &failingGuardBackend{}
	o := Options{
		Monitor:   &fakePoller{},
		Decider:   decision.New([]string{"IR"}),
		Backend:   be,
		Log:       discardLog(),
		VPN:       true,
		Tunnels:   []string{"utun4"},
		Endpoints: []netip.Addr{netip.MustParseAddr("203.0.113.7")},
	}
	err := Run(context.Background(), o)
	if err == nil {
		t.Fatal("expected startup guard failure to return an error")
	}
	// Cleanup must still run on the way out (deferred), never leaving stale rules.
	if be.cleanups != 1 {
		t.Fatalf("cleanup ran %d times, want 1", be.cleanups)
	}
}

type failingGuardBackend struct {
	cleanups int
}

func (b *failingGuardBackend) Apply(p firewall.Policy) error    { return errors.New("guard apply failed") }
func (b *failingGuardBackend) Block(a firewall.Allowlist) error { return nil }
func (b *failingGuardBackend) Unblock() error                   { return nil }
func (b *failingGuardBackend) Cleanup() error                   { b.cleanups++; return nil }
