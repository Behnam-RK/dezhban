// Package state publishes the daemon's live posture to an on-disk JSON file so
// out-of-process observers (the macOS menubar app, `status --json`) can read
// exactly what the daemon decided without running their own poller.
//
// The daemon runs as root; the reader is typically the unprivileged logged-in
// user, so the file is written world-readable (0644). Writes are atomic
// (temp-file + rename) so a reader never sees a half-written snapshot. Publishing
// is best-effort and must never affect enforcement — callers log write failures
// at debug and carry on.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Tunnel is one VPN tunnel interface's observed state (VPN mode only).
type Tunnel struct {
	Name   string `json:"name"`
	Up     bool   `json:"up"`
	Detail string `json:"detail,omitempty"`
}

// Snapshot is the daemon's current posture at a point in time. It is the on-disk
// contract consumed by the menubar app and `status --json`; the JSON keys are
// lowerCamelCase and stable. Time marshals as RFC3339 (Go's default), which the
// Swift client decodes with an ISO-8601 strategy.
type Snapshot struct {
	Time    time.Time `json:"time"`
	Posture string    `json:"posture"` // "guard" | "full-block" | "switch-window" | "standby" | "stopped"
	// Version is the build version of the daemon process that wrote this
	// snapshot ("v0.5.0", or a dev-build string). It answers a question no
	// other surface can: WHICH version is actually running. The binary on
	// disk is not that answer — `upgrade apply` replaces /usr/local/bin/dezhban
	// while the daemon keeps running on its old inode, by design, so disk and
	// process disagree for the whole deferred-activation window. Written by
	// whoever supplies runner.Options.Publish (cmd/dezhban stamps it there,
	// the one choke point every snapshot passes through, including the
	// terminal publishStopped one). Empty from a daemon older than this field
	// — consumers must treat "" as unknown, never as a version.
	Version     string `json:"version,omitempty"`
	Blocked     bool   `json:"blocked"`      // egress currently cut
	IP          string `json:"ip,omitempty"` // last observed public IP
	CountryCode string `json:"countryCode,omitempty"`
	Provider    string `json:"provider,omitempty"` // geo provider of the last reading
	// LookupErr is a GENUINE exit-country lookup failure: a tunnel was up, so
	// there was an exit to measure, and measuring it did not work — the exit may
	// be censoring the geo providers (an Iranian exit blocking them looks exactly
	// like this) or the response was malformed. Worth surfacing.
	LookupErr string `json:"lookupErr,omitempty"`
	// ExitUnknown explains why the exit country is unknown when that is EXPECTED
	// rather than a fault — set instead of LookupErr when no tunnel is up, so a
	// switch window, a standby host, or a drop does not report an alarming
	// "lookup failed" for the entirely normal condition of having no VPN exit to
	// measure. Never set at the same time as LookupErr.
	ExitUnknown string `json:"exitUnknown,omitempty"`
	// EnforcementErr is the last firewall action failure (Block/Unblock/Apply), "" when
	// clear. Distinct from LookupErr: a set value means the daemon TRIED to enforce and
	// the backend rejected it, so posture/blocked describe the data plane truthfully but
	// the intended posture was not achieved (e.g. a failed block leaves posture "allow"
	// during an active leak). Observers should surface it prominently regardless of posture.
	// On a terminal posture:"stopped" snapshot it additionally carries WHY the daemon
	// went down when the exit was not a clean shutdown — a startup refusal or a run-loop
	// failure (see runner.publishStopped and docs/contribute/architecture.md); a clean, operator-requested
	// stop leaves it empty. Either way the contract holds: the intended posture (enforcing)
	// was not achieved.
	EnforcementErr string   `json:"enforcementErr,omitempty"`
	Tunnels        []Tunnel `json:"tunnels,omitempty"`   // VPN mode
	Endpoints      []string `json:"endpoints,omitempty"` // resolved VPN endpoints (VPN mode)
	// PollIntervalSeconds is the daemon's poll cadence, so a reader can size its own
	// staleness threshold off the actual interval instead of hardcoding one. 0 when unknown.
	PollIntervalSeconds int      `json:"pollIntervalSeconds,omitempty"`
	BlockedCountries    []string `json:"blockedCountries,omitempty"`
	PID                 int      `json:"pid,omitempty"`
	// ActiveProfile is the profile the most recent switch window verified onto
	// (VPN mode); "" until a switch window has completed. Normal guard operation
	// and switching between already-known profiles do not set it.
	ActiveProfile string `json:"activeProfile,omitempty"`
	// Switch describes an open switch window, present only while one is active.
	Switch *SwitchState `json:"switch,omitempty"`
	// Pending describes a posture change the daemon is counting toward but has
	// not committed, present only while a hysteresis streak is running.
	Pending *PendingFlip `json:"pending,omitempty"`
	// Drop records the most recent tunnel drop, from the moment it happened
	// until a tunnel is up again — including across the redial window that
	// follows, which is the only reason any surface can report the drop at all.
	// Additive field: absent from older snapshots, so nil means "no drop is
	// being carried", never "no drop has ever happened".
	Drop *DropRecord `json:"drop,omitempty"`
	// Hold reports that the next tunnel drop will stay cut instead of opening
	// an automatic redial window. Present only while armed. Additive field:
	// absent from older snapshots, so nil means "not armed".
	Hold *HoldState `json:"hold,omitempty"`
	// Redial reports that the automatic redial window was REFUSED for the drop
	// currently being carried, and when one could open instead. Present only
	// while such a refusal stands. Additive field: absent from older snapshots,
	// so nil means "nothing refused", never "no budget exists".
	Redial *RedialState `json:"redial,omitempty"`
	// Verify reports that enforcement verification found something wrong: the
	// rules dezhban believes it installed are missing, or the backend could not
	// be read at all. Present only while such a condition stands, and cleared by
	// the next clean check. Additive field: absent from older snapshots, so nil
	// means "nothing wrong is being reported", never "no verification happens".
	//
	// Distinct from EnforcementErr, which means the daemon TRIED to enforce and
	// the backend rejected it. This one means enforcement previously SUCCEEDED
	// and the rules are gone now — the silent-failure case that had no signal
	// at all before.
	Verify *VerifyState `json:"verify,omitempty"`
	// Zombie reports a hung tunnel: interface up, exit lookups through it
	// failing. Present only while such a streak stands. Additive field, like
	// Verify: absent from older snapshots, so nil means "nothing wrong is being
	// reported", never "no tunnel is being watched".
	Zombie *ZombieState `json:"zombie,omitempty"`
	// ExitIPChangedAt is when the observed exit IP last differed from the
	// previous successful reading — purely observational, like CVG's
	// equivalent check: it never flips posture and never touches the
	// hysteresis streak (CountryCode/Pending already own that). It is the
	// signal that best explains "my exit country flapped" — a failover between
	// two VPN servers in the same allowed country changes nothing CountryCode
	// reports, but changes this. omitzero: zero means no change has been
	// observed yet, not "the exit has never had an IP".
	ExitIPChangedAt time.Time `json:"exitIpChangedAt,omitzero"`
	// Display is the rendered posture sentence — see internal/render, the
	// package that composes it from this same Snapshot. Carried here for the
	// one consumer that cannot call Go directly: the macOS menubar app reads
	// it instead of composing its own prose, so the CLI, the state file, and
	// the app always say the same thing. Additive field, like Version and
	// Trigger: absent from a snapshot written before this field existed, or
	// from one written by test code that built a Snapshot by hand — observers
	// must treat a nil Display as unknown, not as "nothing to report".
	Display *Display `json:"display,omitempty"`
}

// Display is the rendered form of a Snapshot: a short machine-readable key
// (also a PNG filename component in the macOS app: menubar-state-<key>.png,
// dock-state-<key>.png) alongside the headline and detail sentences.
// internal/state cannot import internal/render (render already imports
// state, to take a Snapshot as input), so this is a plain mirror of
// render.Display that the daemon's Publish closure populates by converting
// one into the other — see cmd/dezhban's publish func.
type Display struct {
	Key      string `json:"key"`
	Headline string `json:"headline"`
	Detail   string `json:"detail"`
}

// PendingFlip is a posture change under way: hysteresis requires Need
// consecutive agreeing exit-country readings and Have of them have arrived.
//
// It exists because the posture alone cannot distinguish "recovering, two more
// readings to go" from "stuck, nothing is happening" — and after a VPN redials
// out of FULL BLOCK those look identical for up to poll-interval × hysteresis.
// Reporting the count is what makes the wait legible instead of alarming.
//
// Informational only. Publishing progress must never influence it: the streak
// belongs to decision.Decider, and only a successful reading may move it.
type PendingFlip struct {
	// To is the posture being counted toward, using the same stable strings as
	// Snapshot.Posture ("guard" or "full-block").
	To   string `json:"to"`
	Have int    `json:"have"`
	Need int    `json:"need"`
}

// SwitchState describes an open switch window for observers (status, menubar).
type SwitchState struct {
	Open bool `json:"open"`
	// Until is the window's deadline. omitzero for the reason given on
	// RedialState.NextEligible: a zero time.Time marshals to year 1, which the
	// macOS app's ISO8601 decoder refuses, and one unreadable date fails the
	// WHOLE Snapshot decode. render.windowDisplay already treats a zero Until as
	// "no deadline to show" rather than as an error, so the zero case is one this
	// codebase considers reachable — it must not be the one that blanks the
	// menubar.
	Until   time.Time `json:"until,omitzero"`
	Profile string    `json:"profile,omitempty"`
	// Trigger says what opened the window: TriggerManual (operator command) or
	// TriggerAuto (automatic redial window on a tunnel drop). Additive field —
	// absent in snapshots from older daemons, so observers must treat "" as
	// TriggerManual.
	Trigger string `json:"trigger,omitempty"`
}

// DropRecord is the moment a healthy tunnel went down, carried forward until a
// tunnel is up again.
//
// It exists because the cut is otherwise unobservable on the common path. The
// run loop opens the automatic redial window on the same tunnel-down edge, so a
// snapshot showing the guard holding a downed tunnel is superseded microseconds
// later, and observers read the state file about once a second — they would
// never see it. Without this, both surfaces could only say "a window is open"
// and never "your VPN dropped at 3:04PM, and here is what happened next".
//
// Carrying it does NOT make the window look like a cut. An open window is a
// relaxation: traffic is flowing and the real IP may be exposed, so it stays
// amber. The record says what happened, not what is happening.
// It carries the moment and nothing else. A companion "was traffic cut at that
// instant" flag was tried and removed: no surface could truthfully render it,
// because a window may have opened and expired in between, so neither "cut
// since" nor "not cut" describes what followed. What a reader needs from a drop
// is WHEN — the posture fields already say what is happening now.
type DropRecord struct {
	// At is when the tunnel was observed down. omitzero for the reason given on
	// RedialState.NextEligible — render.dropTime already treats a zero as "no
	// drop time to show", so publishing it as a year-1 timestamp would turn a
	// clause this codebase knows how to omit into a total decode failure in the
	// app.
	At time.Time `json:"at,omitzero"`
}

// VerifyState is what enforcement verification found the last time it did not
// like the answer. It exists because every other Apply the daemon makes is
// triggered by something the daemon itself did, so a ruleset removed from
// OUTSIDE — another firewall tool, `pfctl -F all`, `nft flush ruleset`, an OS
// ruleset reload — used to go entirely unnoticed. The daemon kept reporting its
// posture, `status` kept reporting blocked, and the host was open.
//
// Present only while something is wrong; a clean check clears it. Publishing it
// only on failure is deliberate: a field that says "verified OK" on every
// snapshot is noise, and its absence must not be readable as "never checked" —
// that is what the configured interval is for.
type VerifyState struct {
	// At is when the failing check ran.
	At time.Time `json:"at,omitzero"`
	// Missing is true when the backend answered and said the rules are gone.
	// This is the actionable case: the daemon re-applies immediately.
	Missing bool `json:"missing,omitempty"`
	// Err is set when the backend could not be READ at all. Not the same as
	// Missing: an unreadable backend is not evidence of absence, so the daemon
	// changes nothing and only reports — the same discipline as an
	// undeterminable exit country holding the current posture.
	Err string `json:"err,omitempty"`
	// Repairs counts how many times verification has re-applied the posture
	// since the daemon started. A number that keeps climbing means something on
	// this host is repeatedly removing dezhban's rules, which is worth seeing.
	Repairs int `json:"repairs,omitempty"`
}

// ZombieState reports a tunnel interface that reports up while a run of exit
// lookups through it has failed — the interface object still looks fine, but
// nothing is getting through it. dezhban's posture never escalates on a lookup
// failure alone (an unknown country holds, never flips — see decision.Evaluate),
// so without this a hung tunnel stayed correctly cut but explained itself to
// no one and recovered only if a person noticed and intervened.
//
// This is diagnosis, not a leak: the guard is holding exactly as designed.
// Present only while a streak stands; cleared the moment a lookup succeeds, the
// tunnel reports down, or anything else ends the streak's eligibility (standby,
// a switch window, a manual block). Additive field, like Verify: absent from
// older snapshots, so nil means "nothing wrong is being reported".
type ZombieState struct {
	// Since is when the failing streak started.
	Since time.Time `json:"since,omitzero"`
	// Checks is how many consecutive geo lookups have failed through this
	// otherwise-up tunnel.
	Checks int `json:"checks"`
}

// HoldState reports that "hold the line" is armed: the next tunnel drop will
// NOT open an automatic redial window, so a deliberate disconnect stays cut.
//
// dezhban cannot tell an intentional disconnect from an accidental drop, and
// that ambiguity is the whole reason this exists — disconnecting by hand
// otherwise opens a redial window the user never wanted. Arming says which kind
// of drop the next one will be.
//
// It only ever SUPPRESSES. It removes an automatic relaxation for one drop and
// grants nothing, so the three sanctioned relaxation triggers are unchanged and
// there is no fourth. Being strictly more restrictive is also why it needs no
// config gate of its own: there is no setting to protect.
type HoldState struct {
	// Armed is true from the moment it is armed until the drop it covers, an
	// explicit cancel, or a tunnel coming back up.
	Armed bool `json:"armed"`
	// At is when it was armed. omitzero for the reason given on
	// RedialState.NextEligible: no reader needs the instant (the app shows only
	// that hold is armed), so a zero must cost a missing key rather than the
	// whole snapshot.
	At time.Time `json:"at,omitzero"`
}

// RedialState is why the automatic redial window did not open for the drop being
// carried, and when one could. It exists because a guard that silently declines
// to help is the failure mode this project treats as worst: without it the only
// difference between "your VPN has not redialed yet" and "dezhban will not let
// it try again for eleven minutes" is a log line nobody is reading.
//
// A refusal only, never a grant. An open window is already reported by Switch,
// and duplicating it here would give two fields one truth to disagree about.
//
// See docs/adr/0009-redial-budget.md for what does the refusing.
type RedialState struct {
	// Reason is the redial.Reason that refused, as a stable identifier
	// ("cooldown", "exhausted"). Surfaces match on it; the sentence a user reads
	// is composed in internal/render, never here.
	Reason string `json:"reason"`
	// NextEligible is the earliest instant a window could open — a bound, not a
	// promise. The run loop re-takes the decision when this instant arrives, so
	// it is a time the guard acts on rather than one it merely reports; but the
	// re-decision consults the budget afresh and re-checks every precondition,
	// so it may refuse again. Read it as "nothing before this time", never as
	// "a window at this time". Publishing it at all is the point — "the guard is
	// holding" alone leaves the user unable to tell a wait from a wall.
	//
	// omitzero, not omitempty: omitempty does not omit a zero time.Time (a
	// non-empty struct), so a writer without an instant would publish
	// "0001-01-01T00:00:00Z". Every reader then has to special-case year 1, and
	// the macOS app's ISO8601 decoder simply refuses it — failing the WHOLE
	// Snapshot decode, which reads as "stopped" while dezhban is enforcing. An
	// absent key is the one shape every consumer already handles.
	NextEligible time.Time `json:"nextEligible,omitzero"`
	// RemainingSeconds is what is left of the rolling budget AS OF THIS
	// SNAPSHOT, not as of the refusal: episodes keep rolling out of the interval
	// while the cut lasts, so this grows back on its own and a reader can watch
	// it. Seconds rather than a Go duration string so a non-Go reader (the macOS
	// app, jq) gets a number it can compare rather than "1m30s" it has to parse.
	//
	// Reason and NextEligible are the opposite: they are the decision that was
	// made on the drop edge and do not move until the next one.
	RemainingSeconds float64 `json:"remainingSeconds"`
	// FastDrops is how many consecutive fast drops are behind the current
	// backoff. Zero when the budget, not the backoff, is what refused.
	FastDrops int `json:"fastDrops,omitempty"`
}

// Trigger values for SwitchState.Trigger. Stable identifiers — status --json
// consumers match on them.
const (
	TriggerManual = "manual"
	TriggerAuto   = "auto"
	// TriggerPause is an operator-requested bounded pause (`dezhban pause` /
	// the GUI's "Pause protection"): a deliberate, timed drop to the real ISP
	// IP (e.g. to reach a sanctioned-country-only service), sharing the
	// switch-window machinery and rule shape but with its own cap
	// (vpn.pauseMax) and its own control-socket gate (control.allowPauseOps).
	// See docs/adr/0008-arm-at-boot.md.
	TriggerPause = "pause"
)

// DirMode is the mode of the daemon's state directory. It MUST stay traversable
// (0755): the daemon runs as root, but the things inside it are read and reached
// by the unprivileged logged-in user — state.json (0644) by the menubar app and
// `status --json`, and control.sock (0660 root:admin) by every routine op. A
// too-tight directory silently breaks both: the GUI reads no snapshot and reports
// "stopped" while the daemon is enforcing fine, and the control socket becomes
// unreachable so every block/unblock falls back to a password prompt.
//
// Confidentiality here is per-file, not per-directory: pf.state and command.json
// are 0600, so the open directory exposes nothing. A restrictive dir buys no
// secrecy and costs the whole out-of-process contract.
const DirMode = 0o755

// EnsureDir creates the daemon's state directory and repairs the mode of one that
// already exists, in BOTH directions. The repair is the point: MkdirAll is a no-op
// on an existing directory, so a dir created 0700 by an earlier version (or by
// whichever component happened to touch it first) would stay 0700 forever and keep
// the GUI and the control socket locked out.
//
// The mode is enforced exactly, not just "at least DirMode". A too-permissive dir
// (0777, say, from a stray chmod) is as much a defect as a too-restrictive one, and
// a more dangerous one: this directory gates control.sock, and a group- or
// world-writable dir lets a local user unlink the socket and bind their own in its
// place — the authorization boundary is the filesystem here, so the container's bits
// are part of it. Called once at daemon startup, by the root process that owns it.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return fmt.Errorf("state: create dir %q: %w", dir, err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("state: stat dir %q: %w", dir, err)
	}
	if mode := fi.Mode().Perm(); mode != DirMode {
		if err := os.Chmod(dir, DirMode); err != nil {
			return fmt.Errorf("state: chmod dir %q (%#o → %#o): %w", dir, mode, DirMode, err)
		}
	}
	return nil
}

// Write atomically persists s to path as JSON. It creates the parent directory
// (0755) if needed, writes a temp file in the same directory, chmods it 0644
// (world-readable), then renames it over path — rename is atomic on the same
// filesystem, so a concurrent reader sees either the old file or the new one,
// never a partial write.
func Write(path string, s Snapshot) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return fmt.Errorf("state: create dir %q: %w", dir, err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshal: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".state-*.json.tmp")
	if err != nil {
		return fmt.Errorf("state: create temp: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("state: write temp: %w", err)
	}
	// fsync before rename so a crash/power-loss right after the rename can't leave a
	// truncated snapshot behind (same guarantee internal/firewall/pf_darwin.go's
	// atomicWrite provides for its state files; kept as a separate impl because that
	// helper is darwin build-tagged).
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("state: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("state: close temp: %w", err)
	}
	// CreateTemp makes the file 0600; the reader is the unprivileged user.
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("state: chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("state: rename into place: %w", err)
	}
	return nil
}

// Read loads and decodes the snapshot at path.
func Read(path string) (Snapshot, error) {
	var s Snapshot
	data, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("state: parse %q: %w", path, err)
	}
	return s, nil
}
