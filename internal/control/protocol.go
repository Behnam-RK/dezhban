// Package control is the daemon's live control channel: a unix socket the daemon
// listens on and the CLI dials, so routine posture changes (block / unblock /
// switch window) work without re-elevating to root on every call. It complements
// — never replaces — the root-owned command file in internal/command, which stays
// the always-available root path (and the only path when the daemon is down).
//
// Security model: the socket is the daemon's, created root-owned with mode 0660
// and chowned to a trusted group (macOS: "admin", i.e. the machine's admins).
// Filesystem permissions ARE the gate — dezhban stays stdlib-only, so there are
// no SO_PEERCRED peer credentials; anyone who can open the socket is trusted to
// issue the ops below. That is a deliberate relaxation of "every privileged op
// needs root", scoped so it cannot widen egress beyond what the daemon's own
// state machine already sanctions:
//
//   - block / unblock only move between the daemon's standing fail-closed
//     postures (GUARD ↔ FULL BLOCK). They can never open egress past the guard.
//   - switch-open / switch-cancel CAN relax the guard (bounded, ≤ SwitchWindowMax).
//     They ride the socket only when config `control.allowSwitchOps` is true
//     (the default); set it to false to force switch ops back to root-only.
//   - panic is deliberately NOT an op here: the lockout escape hatch must work
//     with no daemon running.
package control

import "time"

// Op identifies a control operation. New ops are additive.
type Op string

const (
	// OpPing is a liveness check. It is answered by the accept goroutine without
	// involving the run loop, so it stays cheap and can never block on it.
	OpPing Op = "ping"
	// OpStatus reports the daemon's current posture from the run loop's own state.
	OpStatus Op = "status"
	// OpBlock escalates to FULL BLOCK (VPN mode) / Block (legacy) and HOLDS it
	// until OpUnblock: the geo state machine will not lift a manual block.
	OpBlock Op = "block"
	// OpUnblock releases a manual block, returning to the standing posture and
	// handing the geo state machine back the wheel.
	OpUnblock Op = "unblock"
	// OpOpenSwitch opens (or extends) a switch window. Gated by allowSwitchOps.
	OpOpenSwitch Op = "switch-open"
	// OpCancelSwitch closes an open switch window early. Gated by allowSwitchOps.
	OpCancelSwitch Op = "switch-cancel"
)

// Version is the wire protocol version. A request carrying a different version is
// rejected rather than guessed at.
const Version = 1

// Request is one control message: exactly one per connection.
type Request struct {
	V        int    `json:"v"`
	Op       Op     `json:"op"`
	Duration string `json:"duration,omitempty"` // switch-open: window length, e.g. "90s"
	Profile  string `json:"profile,omitempty"`  // switch-open: attribution
}

// Response is the daemon's single reply. Mode/Posture reuse the stable strings
// published in the state file, so a caller can render them the same way.
type Response struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Mode    string `json:"mode,omitempty"`
	Posture string `json:"posture,omitempty"`
	Blocked bool   `json:"blocked"`
}

// errResponse is the shorthand for a refusal.
func errResponse(msg string) Response { return Response{OK: false, Error: msg} }

// Timeouts. dialTimeout/replyTimeout bound the client; connDeadline bounds a
// server-side connection so a stalled peer can't pin a goroutine; handoffTimeout
// bounds how long a request waits for the (single) run-loop goroutine before we
// tell the caller the daemon is busy rather than hanging.
const (
	dialTimeout     = 2 * time.Second
	replyTimeout    = 10 * time.Second
	connDeadline    = 5 * time.Second
	handoffTimeout  = 2 * time.Second
	maxRequestBytes = 4096
)
