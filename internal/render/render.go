// Package render turns a state.Snapshot into the sentences an operator reads:
// a short headline and a supporting detail, plus a stable machine key that
// classifies the two. It is the single place that composes posture prose —
// every surface (the CLI, the state file consumed by status --json, the
// menubar app) displays what this package says rather than writing its own
// words. See docs/concepts/glossary.md for the vocabulary this package
// implements.
//
// Pure and leaf-ward: it imports only internal/state, does no I/O, and prints
// nothing. That is also what makes it testable — every prior attempt at this
// (cmd/dezhban's status, the macOS app) mixed composing the sentence with
// printing or displaying it.
package render

import (
	"fmt"
	"time"

	"github.com/behnam-rk/dezhban/internal/state"
)

// Posture name constants, mirroring the literal strings runner.go publishes
// into state.Snapshot.Posture. internal/state deliberately has no exported
// constants for them (see state.Snapshot's doc comment); this package is
// their one home, replacing the local, unexported copies that used to live in
// internal/update/gate.go and cmd/dezhban/upgrade.go.
const (
	PostureGuard        = "guard"
	PostureFullBlock    = "full-block"
	PostureSwitchWindow = "switch-window"
	PostureStandby      = "standby"
	PostureStopped      = "stopped"
)

// Display keys, the stable machine-readable classification alongside the
// prose. They are also PNG filename components in the macOS app
// (menubar-state-<key>.png, dock-state-<key>.png), so a caller that wants to
// change the icon or notification class should switch on Key, never parse
// Headline or Detail.
const (
	KeyOn      = "on"      // guarding, traffic flows through the tunnel
	KeyOff     = "off"     // standby or stopped — nothing is enforced
	KeyBlocked = "blocked" // full block, or a guard holding a downed tunnel
	KeyWarning = "warning" // a switch/redial window is open, or enforcement failed
	KeyPaused  = "paused"  // an operator-requested pause is open
)

// untilFormat matches the CLI's prior time.Kitchen formatting for window
// deadlines ("3:04PM") — short, and unambiguous about being a clock time
// rather than a duration.
const untilFormat = time.Kitchen

// Display is the rendered form of one Snapshot.
type Display struct {
	Key      string `json:"key"`
	Headline string `json:"headline"`
	Detail   string `json:"detail"`
}

// Posture renders ONLY what the posture itself says: no enforcement-error
// override, no trailing lookup/hysteresis notes. Use it where the caller has
// already decided which fact it is reporting and is going to append its own
// clause — `switch --status` printing "(profile …)" after the sentence, or
// `vpn list`'s window line. Text's extras are correct for a general readout
// and wrong there: the EnforcementErr short-circuit would put a raw backend
// error where the window sentence belongs, leaving `switch --status` to print
// something like `pfctl: /dev/pf: Device busy (profile "work")`, and the
// appended notes would land between the sentence and the caller's own clause.
//
// A caller that wants the whole story for a human — the CLI's `status`
// headline, the daemon's published Display — wants Text, not this.
func Posture(s state.Snapshot) Display {
	return postureDisplay(s)
}

// Text renders a Snapshot in full: the posture sentence, with an enforcement
// failure taking precedence over it and any lookup/hysteresis note appended.
// It always returns something displayable, even for a posture string it does
// not recognise (an older or newer daemon), so a caller never needs its own
// fallback.
func Text(s state.Snapshot) Display {
	if s.EnforcementErr != "" {
		// Wins over posture: the daemon tried to enforce and the backend
		// refused, so posture/blocked describe the data plane truthfully but
		// the intended posture was not achieved. That is more urgent than
		// whatever the intended posture was, so there is no point deriving
		// its display just to discard it.
		return Display{Key: KeyWarning, Headline: "Enforcement failed", Detail: s.EnforcementErr}
	}
	d := postureDisplay(s)
	d.Detail = joinSentences(d.Detail, lookupNote(s))
	d.Detail = joinSentences(d.Detail, pendingNote(s.Pending))
	return d
}

// StaleFloor is the minimum staleness budget, regardless of poll cadence.
// Mirrors DezhbanCore's PostureUI.staleFloor (Swift) — 90s tolerates a couple
// of missed default-cadence poll cycles without a live daemon reading as dead.
const StaleFloor = 90 * time.Second

// StaleThreshold is the age past which a Snapshot should be treated as no
// longer trustworthy: 3x the daemon's own poll cadence (so a deliberately
// long pollInterval doesn't make an enforcing daemon read as stopped between
// polls), floored at StaleFloor, or StaleFloor alone when PollIntervalSeconds
// is absent (an older daemon, or one that hasn't published yet). Mirrors
// PostureUI.staleThreshold — kept as one implementation here rather than a
// second copy in cmd/dezhban, since both need the identical rule to agree
// with the GUI on what "the daemon looks alive" means.
func StaleThreshold(s state.Snapshot) time.Duration {
	if s.PollIntervalSeconds <= 0 {
		return StaleFloor
	}
	if d := time.Duration(s.PollIntervalSeconds) * time.Second * 3; d > StaleFloor {
		return d
	}
	return StaleFloor
}

// IsStale reports whether a Snapshot is older than its StaleThreshold as of
// now — a crashed or SIGKILLed daemon leaves its last posture on disk
// indefinitely, so a caller that only checks state.Read's error (as `status`
// used to) will keep reporting "Guarding" long after enforcement stopped.
func IsStale(s state.Snapshot, now time.Time) bool {
	return now.Sub(s.Time) > StaleThreshold(s)
}

func postureDisplay(s state.Snapshot) Display {
	switch s.Posture {
	case PostureStopped:
		return Display{
			Key:      KeyOff,
			Headline: "Stopped",
			Detail:   "dezhban is not running. Nothing is being blocked.",
		}
	case PostureStandby:
		return Display{
			Key:      KeyOff,
			Headline: "Standby — nothing is being blocked.",
			Detail:   "Connect your VPN and the guard arms itself.",
		}
	case PostureFullBlock:
		return fullBlockDisplay(s)
	case PostureSwitchWindow:
		return windowDisplay(s)
	case PostureGuard:
		return guardDisplay(s)
	default:
		return Display{
			Key:      KeyWarning,
			Headline: "Unknown posture",
			Detail:   fmt.Sprintf("dezhban reported an unrecognised posture (%q).", s.Posture),
		}
	}
}

func guardDisplay(s state.Snapshot) Display {
	if GuardHoldsDownedTunnel(s) {
		detail := "Guard active, but no tunnel is up — all traffic is cut until your VPN redials."
		if at := dropTime(s); at != "" {
			detail = "Your VPN dropped at " + at + ". " + detail
		}
		return Display{
			Key:      KeyBlocked,
			Headline: "VPN down — traffic cut",
			Detail:   detail,
		}
	}
	return Display{
		Key:      KeyOn,
		Headline: "Guarding",
		Detail:   "Traffic leaves only through your VPN tunnel.",
	}
}

// GuardHoldsDownedTunnel mirrors PostureUI.guardHoldsDownedTunnel on the macOS
// side: no tunnels observed, or none of the observed tunnels are up, means the
// guard is holding a downed tunnel rather than actively passing traffic.
//
// Exported because it is the difference between the two things the posture
// string "guard" covers, and that difference is not only cosmetic. A guard with
// a tunnel up is passing traffic through it; a guard with none up is the ONLY
// thing keeping traffic off the physical link, so anything that tears the rules
// down (update.CanActivate's restart) must refuse in the second case and not
// the first. Callers must not re-derive this from Tunnels themselves — a second
// copy of the rule is how the CLI, the GUI, and the update gate would come to
// disagree about what a healthy guard is.
func GuardHoldsDownedTunnel(s state.Snapshot) bool {
	if len(s.Tunnels) == 0 {
		return true
	}
	for _, t := range s.Tunnels {
		if t.Up {
			return false
		}
	}
	return true
}

func fullBlockDisplay(s state.Snapshot) Display {
	if s.CountryCode != "" {
		return Display{
			Key:      KeyBlocked,
			Headline: fmt.Sprintf("Full block (%s)", s.CountryCode),
			Detail: fmt.Sprintf(
				"Your VPN is exiting through a country you've blocked (%s). Everything is cut until it moves.",
				s.CountryCode,
			),
		}
	}
	return Display{
		Key:      KeyBlocked,
		Headline: "Full block",
		Detail:   "Your VPN is exiting through a blocked country. Everything is cut until it moves.",
	}
}

func windowDisplay(s state.Snapshot) Display {
	trigger := state.TriggerManual
	until := ""
	if s.Switch != nil {
		if s.Switch.Trigger != "" {
			trigger = s.Switch.Trigger
		}
		if !s.Switch.Until.IsZero() {
			until = s.Switch.Until.Format(untilFormat)
		}
	}
	// The exposure and when it ends LEAD every one of these sentences. A relaxed
	// guard is the one state where this tool is not protecting the user, so the
	// copy must not open with machinery ("Guard relaxed so…") and leave the
	// consequence trailing behind a dash.
	switch trigger {
	case state.TriggerPause:
		return Display{
			Key:      KeyPaused,
			Headline: "Paused",
			Detail:   joinSentences(pausedSentence(until), rearmSentence(until)),
		}
	case state.TriggerAuto:
		return Display{
			Key:      KeyWarning,
			Headline: "Redial window open",
			Detail:   joinSentences(exposedSentence(until), redialCause(s)),
		}
	default:
		return Display{
			Key:      KeyWarning,
			Headline: "Switch window open",
			Detail: joinSentences(exposedSentence(until),
				"The guard is relaxed so a new VPN can connect."),
		}
	}
}

func rearmSentence(until string) string {
	if until == "" {
		return "The guard re-arms automatically when the pause ends."
	}
	return "The guard re-arms automatically at " + until + "."
}

// pausedSentence leads with the fact that matters — the real IP is in use — and
// says so as a present condition, not a risk: during a pause the exposure is the
// point, not a side effect.
func pausedSentence(until string) string {
	if until == "" {
		return "You are using your real IP at your request."
	}
	return "You are using your real IP at your request, until " + until + "."
}

func exposedSentence(until string) string {
	if until == "" {
		return "Your real IP may be exposed until this window closes."
	}
	return "Your real IP may be exposed until " + until + "."
}

// redialCause explains why the guard relaxed, after the exposure has already
// been stated. It names the drop time when the snapshot carries one — a drop is
// the one thing a user can check against their own experience of the network
// dying, and it is what turns "a window is open" into an account of what
// happened.
func redialCause(s state.Snapshot) string {
	if at := dropTime(s); at != "" {
		return "Your VPN dropped at " + at + " and the guard relaxed so it can redial."
	}
	return "Your VPN dropped and the guard relaxed so it can redial."
}

// droppedFormat qualifies a drop with the day it happened. Used once the drop is
// no longer on the snapshot's own day.
const droppedFormat = time.Kitchen + " on Jan 2"

// dropTime renders the carried drop's time, or "" when no drop is being carried.
//
// The record is carried until a tunnel returns, which through an overnight
// outage or a long FULL BLOCK is not the same day. A bare "3:04PM" then reads as
// "a few minutes ago" and quietly misstates how long the host has been cut, so
// the day is named as soon as it is not the snapshot's own. Compared against
// s.Time rather than time.Now() so the sentence a snapshot renders to depends
// only on the snapshot.
func dropTime(s state.Snapshot) string {
	if s.Drop == nil || s.Drop.At.IsZero() {
		return ""
	}
	if !s.Time.IsZero() && !sameDay(s.Drop.At, s.Time) {
		return s.Drop.At.Format(droppedFormat)
	}
	return s.Drop.At.Format(untilFormat)
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// lookupNote surfaces a genuine exit-country lookup failure. ExitUnknown is
// deliberately never surfaced here: it names the ordinary condition of having
// no VPN exit to measure (no tunnel up), and prose about it would be exactly
// the over-alarming noise state.Snapshot.ExitUnknown's doc comment describes
// avoiding.
func lookupNote(s state.Snapshot) string {
	if s.LookupErr == "" {
		return ""
	}
	return fmt.Sprintf("Last exit-country check failed: %s.", s.LookupErr)
}

// pendingNote reports a hysteresis streak in progress, in the one spelling
// ("confirming checks") that replaces the CLI's "agreeing readings" and the
// macOS app's "confirming checks"/decision.Pending's own doc-comment "good
// readings" — three spellings of the same fact.
func pendingNote(p *state.PendingFlip) string {
	if p == nil || p.Have == 0 {
		return ""
	}
	return fmt.Sprintf("%s — %d of %d confirming checks.", pendingAction(p.To), p.Have, p.Need)
}

func pendingAction(to string) string {
	switch to {
	case PostureFullBlock:
		return "Escalating to full block"
	case PostureGuard:
		return "Restoring the guard"
	default:
		return "Changing posture to " + to
	}
}

func joinSentences(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + " " + b
	}
}
