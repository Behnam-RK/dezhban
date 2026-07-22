// Package armed persists the one fact that lets the daemon arm at boot instead
// of live-probing for a tunnel: "a configured VPN has been observed up at least
// once on this host." That is exactly the arming rail ADR-0002 specified —
// "the daemon arms only when a tunnel is both configured and has been observed
// up at least once" — and the persistence its own Consequences section flagged
// as required and never built: "the 'observed once' bit must persist across
// daemon restarts to avoid re-entering standby on every reboot." See
// docs/adr/0002-standby-no-tunnel-posture.md and docs/adr/0008-arm-at-boot.md.
//
// The record lives beside the state file (see cmd/dezhban.defaultStatePath). At
// runtime the daemon is the sole writer. Like internal/learned, it is
// machine-observed fact, deliberately separate from the user's config: a
// corrupt or missing armed.json is safe to discard — it just means "treat this
// host as never having seen a tunnel," the same as a fresh install, never a
// crash. Every write is a whole-file atomic replace (temp + fsync + rename), so
// a reader (the GUI, `status`) never sees a torn file.
package armed

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// version is the on-disk schema version. Bump on an incompatible change.
const version = 1

// Record is the whole armed.json document.
type Record struct {
	Version int `json:"version"`
	// TunnelEverUp is the arming rail: once true, it stays true forever on this
	// host (there is no code path that clears it — a VPN uninstall does not
	// retroactively make it never have worked). vpn.armAtBoot consults exactly
	// this bit.
	TunnelEverUp bool      `json:"tunnelEverUp"`
	FirstUp      time.Time `json:"firstUp"`
	LastUp       time.Time `json:"lastUp"`
}

// Load reads the record at path. A missing file yields a zero-value Record
// (TunnelEverUp: false) and a nil error — that is the correct reading for a
// fresh install or a host whose VPN has never come up, not an error case. A
// corrupt file also yields a zero-value Record, but with a non-nil error so the
// caller can log it: never treat a parse failure as license to skip the
// no-tunnel-yet safety rail by assuming it silently means "never armed" without
// saying so.
func Load(path string) (*Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Record{Version: version}, nil
		}
		return &Record{Version: version}, fmt.Errorf("armed: read %q: %w", path, err)
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return &Record{Version: version}, fmt.Errorf("armed: parse %q (discarding): %w", path, err)
	}
	if r.Version == 0 {
		r.Version = version
	}
	return &r, nil
}

// MarkUp records that a tunnel was observed up at `now`, and saves to path.
// Idempotent and cheap to call repeatedly: FirstUp is set only once (the first
// time TunnelEverUp flips true); LastUp is refreshed every call. Callers should
// still debounce to roughly one write per up-edge, not one per sample — see the
// call sites in internal/runner for the actual cadence.
func MarkUp(path string, now time.Time) error {
	r, err := Load(path)
	if err != nil {
		// A corrupt file was already logged by Load; proceed with the zero-value
		// record it returned rather than losing the observation we're here to record.
		r = &Record{Version: version}
	}
	if !r.TunnelEverUp {
		r.TunnelEverUp = true
		r.FirstUp = now
	}
	r.LastUp = now
	return r.Save(path)
}

// Save atomically writes the record to path (temp + fsync + rename), 0644 so
// the unprivileged CLI/GUI can read it. The parent directory is created if
// needed.
func (r *Record) Save(path string) error {
	r.Version = version
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("armed: create dir %q: %w", dir, err)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("armed: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".armed-*.json.tmp")
	if err != nil {
		return fmt.Errorf("armed: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("armed: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("armed: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("armed: close temp: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("armed: chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("armed: rename into place: %w", err)
	}
	return nil
}
