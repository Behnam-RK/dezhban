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
//   - pause / resume CAN also relax the guard (bounded, ≤ vpn.pauseMax) — a
//     third, independently-gated relaxation (`control.allowPauseOps`, default
//     true) alongside the switch window, sharing its bounded-timer machinery
//     but serving a different purpose: a deliberate, timed drop to the real
//     ISP IP rather than connecting a new VPN. See docs/adr/0008-arm-at-boot.md.
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
	// OpPause opens a bounded pause: egress is fully opened for Duration (capped
	// by vpn.pauseMax), then the guard re-arms itself with no operator action.
	// Gated by allowPauseOps. Unlike switch-open/switch-cancel it is available
	// in every posture, including standby (nothing to pause) and FULL BLOCK
	// (an operator dropping to the real ISP IP on purpose, e.g. to reach a
	// sanctioned-country-only service, is exactly what this op is for).
	OpPause Op = "pause"
	// OpResume ends an open pause early, re-arming immediately instead of
	// waiting out the deadline. Gated by allowPauseOps.
	OpResume Op = "resume"
	// OpReload makes the daemon re-read its own config file and adopt whatever
	// it can without restarting. Ungated, and deliberately so: the config file
	// is root-owned, so this op grants no authority its caller did not already
	// have — it only asks the daemon to notice a change root already made. The
	// reply names what was adopted and what still needs a restart, so no caller
	// can report a setting as applied when it is not.
	OpReload Op = "reload"
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
	Posture string `json:"posture,omitempty"`
	Blocked bool   `json:"blocked"`
	// Transient marks a not-OK reply that is NOT the daemon's decision: the
	// server couldn't get the request to the run loop in time ("daemon busy",
	// a reply timeout, shutdown mid-request). Callers must treat it like an
	// unreachable daemon — retry or fall back to the root command-file path —
	// never like a refusal, which is deliberate and must not be routed around.
	// Optional on the wire (omitempty), so older peers interop unchanged.
	Transient bool `json:"transient,omitempty"`
	// Applied and NeedsRestart answer an OpReload: which changed keys the running
	// daemon adopted, and which ones it could not. Both are reported because a
	// reload that silently ignored half a config edit would leave the user
	// believing a security setting took effect when it had not.
	Applied      []string `json:"applied,omitempty"`
	NeedsRestart []string `json:"needsRestart,omitempty"`
}

// errResponse is the shorthand for a refusal.
func errResponse(msg string) Response { return Response{OK: false, Error: msg} }

// busyResponse is the shorthand for a transient server-side failure — the run
// loop never saw (or never answered) the request. See Response.Transient.
func busyResponse(msg string) Response { return Response{OK: false, Error: msg, Transient: true} }

// Timeouts. These form a budget, and the ordering between them is load-bearing:
// the server's worst case (handoffTimeout waiting for the run loop, then
// replyTimeout waiting for it to finish) must fit inside what the client is
// willing to wait, or the client would hang up exactly as the answer arrives.
//
//	server worst case: handoffTimeout + replyTimeout            (12s)
//	client patience:   handoffTimeout + replyTimeout + writeDeadline + dialTimeout
//
// connDeadline bounds only the server-side READ, so a peer that connects and never
// sends can't pin a goroutine; the reply carries its own writeDeadline because it
// may be issued long after the read deadline was armed.
const (
	dialTimeout     = 2 * time.Second
	replyTimeout    = 10 * time.Second
	connDeadline    = 5 * time.Second
	writeDeadline   = 5 * time.Second
	handoffTimeout  = 2 * time.Second
	maxRequestBytes = 4096
)

// clientDeadline is how long the client waits, end to end, once connected. It
// deliberately exceeds the server's worst case so a slow-but-honest daemon gets to
// deliver its answer — including a refusal — instead of the client timing out and
// reporting an unreachable daemon, which the CLI would wrongly retry as root.
const clientDeadline = handoffTimeout + replyTimeout + writeDeadline

// Accept-failure backoff. Bounds the retry rate for a persistently failing
// Accept (fd exhaustion, say) so it degrades into a slow poll instead of a
// spin, while still recovering promptly once the condition clears.
const (
	acceptBackoffMin = 5 * time.Millisecond
	acceptBackoffMax = time.Second
)
