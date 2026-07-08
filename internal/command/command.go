// Package command is the daemon's minimal control channel: a root-owned command
// file that the `dezhban switch`/`vpn` CLIs write and the running daemon consumes
// on a tick. There is no long-lived IPC server — this mirrors the one-way state
// file in the other direction and keeps dezhban dependency-light (stdlib only,
// cross-platform).
//
// Security model: the file lives in the daemon's root-owned directory (0755), so
// only root can create it. The daemon additionally verifies ownership and
// freshness and consumes the file before acting, closing replay and
// pre-planted-file holes. See Consume.
package command

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Op identifies a control operation. New ops are additive.
type Op string

const (
	OpOpenSwitchWindow   Op = "open-switch-window"
	OpCancelSwitchWindow Op = "cancel-switch-window"
	OpForgetLearned      Op = "forget-learned"
)

// Command is one control message. Duration/Profile/Name are op-specific.
type Command struct {
	Op       Op        `json:"op"`
	Duration string    `json:"duration,omitempty"` // switch window length, e.g. "90s"
	Profile  string    `json:"profile,omitempty"`  // attribution for a switch window
	Name     string    `json:"name,omitempty"`     // target for forget-learned
	IssuedAt time.Time `json:"issuedAt"`
	Nonce    string    `json:"nonce,omitempty"`
}

// OwnerChecker verifies that the command file is safe to trust (owned by root,
// not world-writable). It is injectable so tests can bypass the uid check; the
// production default is RootOwned (unix) / a no-op (Windows, which relies on the
// ProgramData ACL).
type OwnerChecker func(fi os.FileInfo, path string) error

// Write atomically writes c to path (temp + rename), 0600. Used by the CLI after
// it has elevated to root.
func Write(path string, c Command) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("command: create dir %q: %w", dir, err)
	}
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("command: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".command-*.json.tmp")
	if err != nil {
		return fmt.Errorf("command: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("command: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("command: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("command: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("command: rename into place: %w", err)
	}
	return nil
}

// Consume reads, validates, and DELETES the command file at path, returning the
// command. The delete happens before the caller acts (consume-once), so a crash
// loop can't replay a command. Validation:
//   - the file must be a regular file (no symlink),
//   - it must pass owner (root-owned, not world-writable) — via check,
//   - IssuedAt must be within ±freshness of now (rejects a stale file surviving a
//     reboot, and a clock-skewed future stamp).
//
// Returns (cmd, true, nil) when a valid command was consumed; (_, false, nil)
// when there is no file; (_, false, err) when a file was present but invalid (it
// is deleted regardless, so a bad file can't wedge the tick).
func Consume(path string, now time.Time, freshness time.Duration, check OwnerChecker) (Command, bool, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Command{}, false, nil
		}
		return Command{}, false, fmt.Errorf("command: lstat: %w", err)
	}
	// Consume-once: the file is removed on every return path (valid, invalid, or
	// error), so a command can never be replayed and a bad file can't wedge the
	// tick. The removal is deferred, not immediate — the read below still sees the
	// file — but no code path leaves it in place. The caller acts only after
	// Consume returns, so it always acts on an already-deleted file.
	defer func() { _ = os.Remove(path) }()

	if !fi.Mode().IsRegular() {
		return Command{}, false, fmt.Errorf("command: %q is not a regular file", path)
	}
	if check != nil {
		if err := check(fi, path); err != nil {
			return Command{}, false, err
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Command{}, false, fmt.Errorf("command: read: %w", err)
	}
	var c Command
	if err := json.Unmarshal(data, &c); err != nil {
		return Command{}, false, fmt.Errorf("command: parse: %w", err)
	}
	if c.Op == "" {
		return Command{}, false, fmt.Errorf("command: missing op")
	}
	skew := now.Sub(c.IssuedAt)
	age := skew
	if age < 0 {
		age = -age
	}
	if age > freshness {
		if skew < 0 {
			// Future-dated command (clock skew / bad writer): report the offset
			// as a positive duration rather than a confusing negative "ago".
			return Command{}, false, fmt.Errorf("command: rejected (issued %s in the future, freshness %s)", age, freshness)
		}
		return Command{}, false, fmt.Errorf("command: stale (issued %s ago, freshness %s)", age, freshness)
	}
	return c, true, nil
}

// Discard deletes any command file at path without acting on it. Called at daemon
// startup so a file left over from a previous run (or a reboot) can't trigger an
// action on boot.
func Discard(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("command: discard: %w", err)
	}
	return nil
}
