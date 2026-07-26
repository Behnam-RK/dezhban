package update

import (
	"fmt"
	"time"

	"github.com/behnam-rk/dezhban/internal/render"
	"github.com/behnam-rk/dezhban/internal/state"
)

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
//   - "guard" WITH A TUNNEL UP: the exit is allowed and routing still carries
//     traffic through the tunnel during the gap — nothing is trying to take the
//     physical path in the first place.
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
// "guard" with NO tunnel up refuses as well, and the distinction is the whole
// reason render.GuardHoldsDownedTunnel is exported. That state shares the
// "guard" posture string but not its safety argument: with the tunnel down the
// standing guard rule is the only thing keeping traffic off the physical link
// (render renders it "VPN down — traffic cut"), so removing every rule for the
// length of a restart is a real leak, and under vpn.armAtBoot: false the host
// stays open afterwards rather than re-arming. CLAUDE.md's rule has always read
// "healthy guard"; this is what makes the code agree with it.
//
// Callers must re-run this at the instant of activation, not once at download
// time: a payload staged before FULL BLOCK engaged must not activate into it
// just because the check ran earlier.
func CanActivate(statePath string) GateResult {
	snap, err := state.Read(statePath)
	if err != nil {
		return GateResult{OK: false, Reason: fmt.Sprintf("could not read daemon state (%v) — refusing to guess it's safe", err)}
	}

	// render.StaleThreshold, not a local copy of the same 3x-poll arithmetic:
	// this gate must not call a snapshot fresh that `status` and the menubar app
	// already render as "Stopped", nor stale one they still show as guarding —
	// an activation refused for a reason the operator's own tools contradict is
	// an unexplainable refusal.
	//
	// It moves in BOTH directions, deliberately. With no PollIntervalSeconds the
	// budget tightens (5m → 90s), which is the safe direction. With one it can
	// LOOSEN: at the default 15s poll it goes 45s → 90s, because the shared rule
	// floors at 90s so that a daemon between polls never reads as dead. Agreeing
	// with the other two surfaces is worth those extra 45 seconds — the gate's
	// real work is the posture check below, and a snapshot that stale would have
	// to have gone stale in the window between `upgrade apply` reading it and
	// restarting.
	if age := time.Since(snap.Time); age > render.StaleThreshold(snap) {
		return GateResult{
			OK:      false,
			Reason:  fmt.Sprintf("state snapshot is %s old (stale) — refusing to guess it's still accurate", age.Round(time.Second)),
			Posture: snap.Posture,
		}
	}

	switch snap.Posture {
	case render.PostureGuard:
		if render.GuardHoldsDownedTunnel(snap) {
			return GateResult{
				OK:      false,
				Reason:  "the guard is holding a downed tunnel — a restart would remove the only rules cutting egress",
				Posture: snap.Posture,
			}
		}
		return GateResult{OK: true, Reason: "guard is healthy — the tunnel carries traffic during the restart", Posture: snap.Posture}
	case render.PostureStandby:
		return GateResult{OK: true, Reason: "standby — no rules are installed, nothing to interrupt", Posture: snap.Posture}
	case render.PostureFullBlock:
		return GateResult{OK: false, Reason: "FULL BLOCK is active — restarting would lift the block on a forbidden-country exit", Posture: snap.Posture}
	case render.PostureSwitchWindow:
		return GateResult{OK: false, Reason: "a switch window is open — restarting would cancel it mid-use", Posture: snap.Posture}
	default:
		return GateResult{OK: false, Reason: fmt.Sprintf("posture %q is not safe to restart through", snap.Posture), Posture: snap.Posture}
	}
}
