package runner

import (
	"time"

	"github.com/behnam-rk/dezhban/internal/decision"
)

// LiveSettings is the subset of Options a running guard can adopt in place,
// without rebuilding anything it started with. It deliberately carries no
// resources — no backend, no control socket, no watcher, no monitor — because
// those are exactly the things that cannot be swapped underneath a live loop,
// and a struct that could carry them would invite trying.
//
// The daemon derives one of these from a freshly-read config file and sends it
// on Options.ReloadC. Whatever is not in this struct needs a restart, and the
// user is told so by name rather than left to discover it: see
// config.Changes, which classifies every key against exactly this boundary.
type LiveSettings struct {
	// Interval is the geo-poll period; adopting it resets the poll ticker, so a
	// shortened interval takes effect from the next tick rather than after the
	// old one finally elapses.
	Interval time.Duration

	// Decider carries the blocked-country list and hysteresis together, already
	// built. Replacing it resets the hysteresis streak, which is the correct
	// reading of a changed country list: readings counted toward a verdict under
	// the old list say nothing about the new one. Posture itself is unaffected —
	// only a successful reading may move it, exactly as during normal operation.
	Decider          *decision.Decider
	BlockedCountries []string

	Autodetect        bool
	AllowPhysicalDNS  bool
	AllowLocalNetwork bool
	AutoArm           bool

	// Window durations and their caps. Adopting these affects the NEXT window
	// only: an episode already open keeps the deadline and cap it was opened
	// with. Letting a reload extend a live window would hand any config write
	// the power to lengthen an active relaxation of the guard, which is exactly
	// what the per-trigger caps exist to prevent.
	SwitchWindow            time.Duration
	SwitchWindowMax         time.Duration
	ReconnectWindow         time.Duration
	ReconnectWindowMax      time.Duration
	ReconnectMinUptime      time.Duration
	PauseMax                time.Duration
	WindowDiscoveryInterval time.Duration

	EndpointRefresh time.Duration
	EndpointGrace   time.Duration

	AllowSwitchOps bool
	AllowPauseOps  bool
}

// ReloadReport names what a reload actually did, so the answer travelling back
// to the user distinguishes the two outcomes that matter: keys the running
// daemon adopted, and keys that changed on disk but are still being enforced at
// their old values until a restart.
//
// Both halves are reported even when one is empty. "Nothing needed a restart"
// and "we didn't check" have to look different to whoever is reading.
type ReloadReport struct {
	Applied      []string
	NeedsRestart []string
}

// Live captures the current live-appliable settings. It exists so a reload can
// be expressed as a diff against what the loop is actually running, and so tests
// can assert that adopting a LiveSettings really did change the loop's behaviour
// rather than only its bookkeeping.
func (o Options) Live() LiveSettings {
	return LiveSettings{
		Interval:                o.Interval,
		Decider:                 o.Decider,
		BlockedCountries:        o.BlockedCountries,
		Autodetect:              o.Autodetect,
		AllowPhysicalDNS:        o.AllowPhysicalDNS,
		AllowLocalNetwork:       o.AllowLocalNetwork,
		AutoArm:                 o.AutoArm,
		SwitchWindow:            o.SwitchWindow,
		SwitchWindowMax:         o.SwitchWindowMax,
		ReconnectWindow:         o.ReconnectWindow,
		ReconnectWindowMax:      o.ReconnectWindowMax,
		ReconnectMinUptime:      o.ReconnectMinUptime,
		PauseMax:                o.PauseMax,
		WindowDiscoveryInterval: o.WindowDiscoveryInterval,
		EndpointRefresh:         o.EndpointRefresh,
		EndpointGrace:           o.EndpointGrace,
		AllowSwitchOps:          o.AllowSwitchOps,
		AllowPauseOps:           o.AllowPauseOps,
	}
}
