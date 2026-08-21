package runner

import (
	"io"
	"log/slog"
	"time"

	"github.com/behnam-rk/dezhban/internal/applied"
	"github.com/behnam-rk/dezhban/internal/firewall"
)

// recordingBackend records what was applied, then gets out of the way.
//
// A decorator rather than a `applied.Save` beside each `Backend.Apply`: the run
// loop applies from nineteen places, and a record that is only as complete as
// the last person to remember it is worse than none — a surface would show a
// stale posture with no way to tell. Wrapping makes a new call site recorded by
// construction.
//
// It preserves the single-writer invariant exactly, because it adds no writer:
// every method is called from the run-loop goroutine, by the same code that
// called the wrapped backend before. That also means the fields below need no
// locking, and nothing here may be moved onto another goroutine. The write is
// an atomic replace of a small file — bounded work, on the goroutine that owns
// window expiry and geo ticks, which is why it must stay that shape.
//
// Every failure to record is logged and swallowed. This is a diagnostic aid;
// failing to write down what was applied must never become a reason not to
// apply it, and must never turn a successful enforcement into a returned error.
type recordingBackend struct {
	// Embedded so the wrapper stays exactly as narrow as the interface the run
	// loop uses. Widening Backend to carry a diagnostic read would put a method
	// on the enforcement seam that enforcement never calls.
	Backend
	path string
	log  *slog.Logger
	// now is injected so a test can assert the recorded timestamp instead of
	// asserting that some time passed.
	now func() time.Time
}

// newRecordingBackend wraps b when path is non-empty; otherwise it returns b
// unchanged, so a caller with no state directory (tests, Windows service
// harnesses) is unaffected.
func newRecordingBackend(b Backend, path string, log *slog.Logger) Backend {
	if path == "" || b == nil {
		return b
	}
	if log == nil {
		// Run does not default a nil Log, and every method here logs on the
		// failure path. A diagnostic aid must not be the thing that panics the
		// daemon on the one day the disk is full.
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &recordingBackend{Backend: b, path: path, log: log, now: time.Now}
}

func (r *recordingBackend) Apply(p firewall.Policy) error {
	// Record only what actually landed. A failed Apply leaves the previous
	// ruleset live, so overwriting the record first would describe rules that
	// were never installed — the one thing a surface reading this must be able
	// to rely on not happening.
	if err := r.Backend.Apply(p); err != nil {
		return err
	}
	rules, err := firewall.RenderRules(p)
	if err != nil {
		r.log.Warn("could not render the applied ruleset for the diagnostics record", "err", err)
		return nil
	}
	rec := applied.Record{
		Mode:    p.Mode.String(),
		At:      r.now(),
		Rules:   rules,
		Backend: firewall.RulesetKind,
	}
	if err := applied.Save(r.path, rec); err != nil {
		r.log.Warn("could not record the applied ruleset", "err", err, "path", r.path)
	}
	return nil
}

func (r *recordingBackend) Unblock() error {
	err := r.Backend.Unblock()
	// Clear even when Unblock failed: the rules are in an unknown state, and a
	// record that confidently names the old posture is worse than none.
	r.clear()
	return err
}

func (r *recordingBackend) Cleanup() error {
	err := r.Backend.Cleanup()
	r.clear()
	return err
}

func (r *recordingBackend) clear() {
	if err := applied.Remove(r.path); err != nil {
		r.log.Warn("could not clear the applied-ruleset record", "err", err, "path", r.path)
	}
}
