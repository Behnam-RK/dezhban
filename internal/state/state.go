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
	Time             time.Time `json:"time"`
	Mode             string    `json:"mode"`         // "vpn" | "legacy"
	Posture          string    `json:"posture"`      // "allow" | "block" | "guard" | "full-block" | "stopped"
	Blocked          bool      `json:"blocked"`      // egress currently cut
	IP               string    `json:"ip,omitempty"` // last observed public IP
	CountryCode      string    `json:"countryCode,omitempty"`
	Provider         string    `json:"provider,omitempty"`  // geo provider of the last reading
	LookupErr        string    `json:"lookupErr,omitempty"` // last lookup error, "" if none
	Tunnels          []Tunnel  `json:"tunnels,omitempty"`   // VPN mode
	Endpoints        []string  `json:"endpoints,omitempty"` // resolved VPN endpoints (VPN mode)
	BlockedCountries []string  `json:"blockedCountries,omitempty"`
	PID              int       `json:"pid,omitempty"`
}

// Write atomically persists s to path as JSON. It creates the parent directory
// (0755) if needed, writes a temp file in the same directory, chmods it 0644
// (world-readable), then renames it over path — rename is atomic on the same
// filesystem, so a concurrent reader sees either the old file or the new one,
// never a partial write.
func Write(path string, s Snapshot) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
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
