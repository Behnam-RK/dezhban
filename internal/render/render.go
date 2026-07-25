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

// Text renders a Snapshot. It always returns something displayable, even for
// a posture string it does not recognise (an older or newer daemon), so a
// caller never needs its own fallback.
func Text(s state.Snapshot) Display {
	d := postureDisplay(s)
	if s.EnforcementErr != "" {
		// Wins over posture: the daemon tried to enforce and the backend
		// refused, so posture/blocked describe the data plane truthfully but
		// the intended posture was not achieved. That is more urgent than
		// whatever the intended posture was.
		return Display{Key: KeyWarning, Headline: "Enforcement failed", Detail: s.EnforcementErr}
	}
	d.Detail = joinSentences(d.Detail, lookupNote(s))
	d.Detail = joinSentences(d.Detail, pendingNote(s.Pending))
	return d
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
	if guardHoldsDownedTunnel(s) {
		return Display{
			Key:      KeyBlocked,
			Headline: "VPN down — traffic cut",
			Detail:   "Guard active, but no tunnel is up — all traffic is cut until your VPN redials.",
		}
	}
	return Display{
		Key:      KeyOn,
		Headline: "Guarding",
		Detail:   "Traffic leaves only through your VPN tunnel.",
	}
}

// guardHoldsDownedTunnel mirrors PostureUI.guardHoldsDownedTunnel on the macOS
// side: no tunnels observed, or none of the observed tunnels are up, means the
// guard is holding a downed tunnel rather than actively passing traffic.
func guardHoldsDownedTunnel(s state.Snapshot) bool {
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
	switch trigger {
	case state.TriggerPause:
		return Display{
			Key:      KeyPaused,
			Headline: "Paused",
			Detail:   "Using your real IP at your request. " + rearmSentence(until),
		}
	case state.TriggerAuto:
		return Display{
			Key:      KeyWarning,
			Headline: "Redial window open",
			Detail:   "Your VPN dropped. The guard is relaxed while it redials — " + exposedSentence(until),
		}
	default:
		return Display{
			Key:      KeyWarning,
			Headline: "Switch window open",
			Detail:   "Guard relaxed so a new VPN can connect — " + exposedSentence(until),
		}
	}
}

func rearmSentence(until string) string {
	if until == "" {
		return "The guard re-arms automatically when the pause ends."
	}
	return "The guard re-arms automatically at " + until + "."
}

func exposedSentence(until string) string {
	if until == "" {
		return "your real IP may be exposed until it closes."
	}
	return "your real IP may be exposed until it closes (" + until + ")."
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
