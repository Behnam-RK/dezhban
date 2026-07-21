package update

import (
	"fmt"
	"time"

	"github.com/behnam-rk/dezhban/internal/state"
)

// These mirror the literal posture strings runner.go publishes into
// state.Snapshot.Posture (internal/state has no exported constants for them,
// and this package doesn't add any either — that would be a second source of
// truth for values internal/runner already owns; it just names the two this
// package treats specially).
const (
	posturePassGuard   = "guard"
	posturePassStandby = "standby"
)

// staleFallback is the staleness budget used when a snapshot has no
// PollIntervalSeconds (an older daemon, or one that hasn't published yet) to
// size it from.
const staleFallback = 5 * time.Minute

// GateResult explains an activation gate decision.
type GateResult struct {
	OK      bool
	Reason  string // human-readable, always set
	Posture string // the snapshot's posture, "" if no snapshot was read at all
}

// CanActivate reports whether restarting into a newly-applied version is safe
// right now. Restarting removes every firewall rule for the duration of the
// swap — runner.Run's Cleanup is deferred unconditionally, the same invariant
// that keeps a normal `stop` from ever leaving a stale block-all rule also
// means a restart briefly has none at all. Only two postures make that gap
// harmless:
//
//   - "guard": the tunnel is up and the exit is allowed, so routing still
//     carries traffic through the tunnel during the gap — nothing is trying
//     to take the physical path in the first place.
//   - "standby": no rules are installed at all. Nothing to lose.
//
// Everything else refuses, especially "full-block": tearing that down would
// unblock a host sitting on a forbidden-country exit — the one scenario this
// whole tool exists to prevent, caused by the updater. An open switch window
// also refuses: a restart would cancel it mid-use rather than let it expire
// or close on its own terms. A missing, unreadable, or STALE snapshot refuses
// too — an unknown state is not assumed safe, the same rule
// decision.Evaluate already applies to an undeterminable country reading
// (hold, never escalate on a guess).
//
// Callers must re-run this at the instant of activation, not once at download
// time: a payload staged before FULL BLOCK engaged must not activate into it
// just because the check ran earlier.
func CanActivate(statePath string) GateResult {
	snap, err := state.Read(statePath)
	if err != nil {
		return GateResult{OK: false, Reason: fmt.Sprintf("could not read daemon state (%v) — refusing to guess it's safe", err)}
	}

	maxAge := staleFallback
	if snap.PollIntervalSeconds > 0 {
		// A few missed poll cycles' worth of slack, not a hair trigger — the
		// daemon publishes roughly once per poll, so one or two delayed
		// writes should not itself read as "stale".
		maxAge = time.Duration(snap.PollIntervalSeconds) * time.Second * 3
	}
	if age := time.Since(snap.Time); age > maxAge {
		return GateResult{
			OK:      false,
			Reason:  fmt.Sprintf("state snapshot is %s old (stale) — refusing to guess it's still accurate", age.Round(time.Second)),
			Posture: snap.Posture,
		}
	}

	switch snap.Posture {
	case posturePassGuard:
		return GateResult{OK: true, Reason: "guard is healthy — the tunnel carries traffic during the restart", Posture: snap.Posture}
	case posturePassStandby:
		return GateResult{OK: true, Reason: "standby — no rules are installed, nothing to interrupt", Posture: snap.Posture}
	case "full-block":
		return GateResult{OK: false, Reason: "FULL BLOCK is active — restarting would lift the block on a forbidden-country exit", Posture: snap.Posture}
	case "switch-window":
		return GateResult{OK: false, Reason: "a switch window is open — restarting would cancel it mid-use", Posture: snap.Posture}
	default:
		return GateResult{OK: false, Reason: fmt.Sprintf("posture %q is not safe to restart through", snap.Posture), Posture: snap.Posture}
	}
}
