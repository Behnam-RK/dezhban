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
	Time        time.Time `json:"time"`
	Posture     string    `json:"posture"`      // "guard" | "full-block" | "switch-window" | "standby" | "stopped"
	Blocked     bool      `json:"blocked"`      // egress currently cut
	IP          string    `json:"ip,omitempty"` // last observed public IP
	CountryCode string    `json:"countryCode,omitempty"`
	Provider    string    `json:"provider,omitempty"`  // geo provider of the last reading
	LookupErr   string    `json:"lookupErr,omitempty"` // last geo-lookup error (expected; handled by fail-closed)
	// EnforcementErr is the last firewall action failure (Block/Unblock/Apply), "" when
	// clear. Distinct from LookupErr: a set value means the daemon TRIED to enforce and
	// the backend rejected it, so posture/blocked describe the data plane truthfully but
	// the intended posture was not achieved (e.g. a failed block leaves posture "allow"
	// during an active leak). Observers should surface it prominently regardless of posture.
	// On a terminal posture:"stopped" snapshot it additionally carries WHY the daemon
	// went down when the exit was not a clean shutdown — a startup refusal or a run-loop
	// failure (see runner.publishStopped and docs/state.md); a clean, operator-requested
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
}

// SwitchState describes an open switch window for observers (status, menubar).
type SwitchState struct {
	Open    bool      `json:"open"`
	Until   time.Time `json:"until"`
	Profile string    `json:"profile,omitempty"`
	// Trigger says what opened the window: TriggerManual (operator command) or
	// TriggerAuto (automatic reconnect window on a tunnel drop). Additive field —
	// absent in snapshots from older daemons, so observers must treat "" as
	// TriggerManual.
	Trigger string `json:"trigger,omitempty"`
}

// Trigger values for SwitchState.Trigger. Stable identifiers — status --json
// consumers match on them.
const (
	TriggerManual = "manual"
	TriggerAuto   = "auto"
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
