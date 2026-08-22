package runner

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/applied"
	"github.com/behnam-rk/dezhban/internal/firewall"
)

func recordingAt(t *testing.T, at time.Time) (Backend, *fakeBackend, string) {
	t.Helper()
	inner := &fakeBackend{}
	path := applied.Path(t.TempDir())
	b := newRecordingBackend(inner, path, discardLog())
	b.(*recordingBackend).now = func() time.Time { return at }
	return b, inner, path
}

func guardPolicy() firewall.Policy {
	return firewall.Policy{
		Mode:         firewall.ModeGuard,
		TunnelIfaces: []string{"utun4"},
		VPNEndpoints: []netip.Addr{netip.MustParseAddr("203.0.113.7")},
	}
}

func TestRecordingBackendRecordsWhatItApplied(t *testing.T) {
	at := time.Date(2026, 8, 21, 14, 2, 11, 0, time.UTC)
	b, inner, path := recordingAt(t, at)

	if err := b.Apply(guardPolicy()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(inner.policies) != 1 {
		t.Fatalf("the wrapped backend saw %d applies, want 1", len(inner.policies))
	}

	rec, ok, err := applied.Load(path)
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	if rec.Mode != "guard" {
		t.Errorf("Mode = %q, want \"guard\"", rec.Mode)
	}
	if !rec.At.Equal(at) {
		t.Errorf("At = %v, want %v", rec.At, at)
	}
	if rec.Backend != firewall.RulesetKind {
		t.Errorf("Backend = %q, want %q", rec.Backend, firewall.RulesetKind)
	}
	// The recorded text must be what this policy renders, not a re-render of
	// some later state: the resolved endpoint has to be in it.
	want, err := firewall.RenderRules(guardPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if rec.Rules != want {
		t.Errorf("recorded rules differ from RenderRules for the same policy")
	}
}

// A failed Apply leaves the PREVIOUS ruleset live. Recording the attempt would
// describe rules that were never installed — the one thing a reader of this
// file has to be able to rely on not happening.
func TestAFailedApplyRecordsNothing(t *testing.T) {
	b, inner, path := recordingAt(t, time.Unix(0, 0))
	if err := b.Apply(guardPolicy()); err != nil {
		t.Fatal(err)
	}
	first, _, _ := applied.Load(path)

	inner.applyErr = errors.New("pfctl exploded")
	fullBlock := firewall.Policy{Mode: firewall.ModeFullBlock}
	if err := b.Apply(fullBlock); err == nil {
		t.Fatal("Apply returned nil for a failing backend")
	}

	after, ok, _ := applied.Load(path)
	if !ok {
		t.Fatal("the previous record was destroyed by a failed apply")
	}
	if after.Mode != first.Mode || after.Rules != first.Rules {
		t.Errorf("a failed apply overwrote the record: %q", after.Mode)
	}
}

// After teardown there are no rules. A record left behind would be read as the
// live posture — a surface saying "guard is enforcing" over an open network.
func TestUnblockAndCleanupClearTheRecord(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(Backend) error
	}{
		{"unblock", func(b Backend) error { return b.Unblock() }},
		{"cleanup", func(b Backend) error { return b.Cleanup() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _, path := recordingAt(t, time.Unix(0, 0))
			if err := b.Apply(guardPolicy()); err != nil {
				t.Fatal(err)
			}
			if err := tc.call(b); err != nil {
				t.Fatal(err)
			}
			if _, ok, _ := applied.Load(path); ok {
				t.Error("the record survived teardown")
			}
		})
	}
}

// An empty path is "recording off" and must hand back the backend untouched, so
// a caller with no state directory pays nothing and behaves identically.
func TestNoPathMeansNoWrapper(t *testing.T) {
	inner := &fakeBackend{}
	if got := newRecordingBackend(inner, "", discardLog()); got != Backend(inner) {
		t.Error("an empty path still wrapped the backend")
	}
}

// Run does not default a nil Log, and every failure path here logs. A
// diagnostic aid must not be what panics the daemon.
func TestANilLoggerDoesNotPanic(t *testing.T) {
	b := newRecordingBackend(&fakeBackend{}, applied.Path(t.TempDir()), nil)
	if err := b.Apply(guardPolicy()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}
