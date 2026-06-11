//go:build darwin

package firewall

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

// macOS enforcement via pfctl.
//
// Anchor-evaluation choice (the open research item in the Phase 2 plan):
// macOS only evaluates a sub-anchor if the main ruleset references it. We use
// approach (A): append a one-time `anchor "dezhban"` line to /etc/pf.conf
// (backed up first) and load our rules INTO that kernel anchor with
// `pfctl -a dezhban -f -`.
//
// Why (A) over the plan's tentatively-preferred (B) full-ruleset swap:
//   - `block` and `unblock` run as SEPARATE processes, so any pf state held in
//     memory is gone between them. (A) keeps the rules in the kernel anchor, so
//     teardown works even if the blocking process was killed (acceptance #5).
//   - Teardown is `pfctl -a dezhban -F all` — surgical by construction, it can
//     only ever touch our anchor, never unrelated rules.
//   - (B) would need fragile cross-process disk state plus a `pfctl -sr/-sn`
//     round-trip that does not faithfully reproduce options/nat/scrub anchors.
//
// The only persistent system mutation is the single anchor line in
// /etc/pf.conf; Unblock restores the saved backup, which removes it.
const (
	anchorName = "dezhban"
	pfConfPath = "/etc/pf.conf"
	backupPath = "/etc/pf.conf.dezhban.bak"
	stateDir   = "/var/db/dezhban"
)

// anchorRef is the line we append to /etc/pf.conf so pf evaluates our anchor.
var anchorRef = fmt.Sprintf("anchor \"%s\"", anchorName)

var statePath = filepath.Join(stateDir, "pf.state")

// pfBackend is the macOS FirewallBackend. It holds no in-memory state: the
// authoritative state lives in the kernel anchor and the on-disk state file, so
// it survives across separate `dezhban` invocations.
type pfBackend struct{}

// New returns the macOS pfctl backend.
func New() (FirewallBackend, error) {
	return &pfBackend{}, nil
}

// savedState records what to restore on unblock. Captured only on the first
// block so re-blocking never clobbers the true pre-block state.
type savedState struct {
	// PrevEnabled is whether pf was already enabled before we blocked.
	PrevEnabled bool `json:"prevEnabled"`
}

func (b *pfBackend) Block(a Allowlist) error {
	ruleset := renderRuleset(a)
	// Validate the generated ruleset (-n parses without loading) BEFORE touching
	// any system state, so a malformed allowlist can't half-apply and leave a
	// modified pf.conf with an empty kernel anchor.
	if _, err := pfctl(ruleset, "-n", "-f", "-"); err != nil {
		return fmt.Errorf("invalid block ruleset: %w", err)
	}

	info, err := pfctl("", "-s", "info")
	if err != nil {
		return fmt.Errorf("query pf status: %w", err)
	}
	enabled := strings.Contains(info, "Status: Enabled")

	// First block only: snapshot prior state so Unblock can restore it.
	if !fileExists(statePath) {
		if err := backupPfConf(); err != nil {
			return err
		}
		if err := saveState(savedState{PrevEnabled: enabled}); err != nil {
			return err
		}
	}

	if err := ensureAnchorRef(); err != nil {
		return err
	}
	// Reload the main ruleset so the (possibly newly added) anchor line is live.
	// This does not flush rules already loaded into the sub-anchor.
	if _, err := pfctl("", "-f", pfConfPath); err != nil {
		return fmt.Errorf("reload main ruleset: %w", err)
	}
	// (Re)load our anchor rules. Loading replaces the anchor's contents, so a
	// repeat block does not stack duplicates — this is what makes Block idempotent.
	if _, err := pfctl(ruleset, "-a", anchorName, "-f", "-"); err != nil {
		return fmt.Errorf("load dezhban anchor: %w", err)
	}
	// Turn enforcement on last, once the rules are in place.
	if !enabled {
		if _, err := pfctl("", "-e"); err != nil && !strings.Contains(err.Error(), "already enabled") {
			return fmt.Errorf("enable pf: %w", err)
		}
	}
	return nil
}

func (b *pfBackend) Unblock() error {
	// Surgical: only ever clears the dezhban anchor (rules, tables, states).
	if _, err := pfctl("", "-a", anchorName, "-F", "all"); err != nil {
		return fmt.Errorf("flush dezhban anchor: %w", err)
	}

	// Restore the original /etc/pf.conf (removes our appended anchor line).
	restored := false
	if fileExists(backupPath) {
		data, err := os.ReadFile(backupPath)
		if err != nil {
			return fmt.Errorf("read pf.conf backup: %w", err)
		}
		if err := atomicWrite(pfConfPath, data, 0o644); err != nil {
			return fmt.Errorf("restore pf.conf: %w", err)
		}
		_ = os.Remove(backupPath)
		restored = true
	}

	st, haveState := loadState()
	switch {
	case haveState && !st.PrevEnabled:
		// pf was off before we blocked — turn it back off to match prior state.
		if _, err := pfctl("", "-d"); err != nil && !strings.Contains(err.Error(), "not enabled") {
			return fmt.Errorf("disable pf: %w", err)
		}
	case restored:
		// pf stays enabled; reload the restored ruleset so the anchor ref is gone.
		if _, err := pfctl("", "-f", pfConfPath); err != nil {
			return fmt.Errorf("reload restored ruleset: %w", err)
		}
	}

	_ = os.Remove(statePath)
	return nil
}

func (b *pfBackend) IsBlocked() (bool, error) {
	out, err := pfctl("", "-a", anchorName, "-s", "rules")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(out) == "" {
		return false, nil
	}
	// Anchor rules are loaded but only enforced while pf itself is enabled, so a
	// stale anchor under a disabled pf must not report as blocked.
	info, err := pfctl("", "-s", "info")
	if err != nil {
		return false, err
	}
	return strings.Contains(info, "Status: Enabled"), nil
}

// Cleanup is best-effort teardown for shutdown/panic. It is just Unblock; any
// error is returned for the caller to log, never treated as fatal.
func (b *pfBackend) Cleanup() error {
	return b.Unblock()
}

// renderRuleset builds the pf ruleset loaded into the dezhban anchor: a
// default-deny on outbound traffic, with quick passes for loopback, DNS, and
// the geo-API allowlist. `pass` rules keep state by default, so return traffic
// for the allowed flows is permitted; inbound is not blocked (we only cut egress).
func renderRuleset(a Allowlist) string {
	var b strings.Builder
	b.WriteString("# dezhban block ruleset (Phase 2) — default-deny outbound.\n")
	b.WriteString("pass quick on lo0 all\n")
	if len(a.DNS) > 0 {
		fmt.Fprintf(&b, "pass out quick proto { udp tcp } to { %s } port 53\n", joinAddrs(a.DNS))
	}
	if len(a.Hosts) > 0 {
		fmt.Fprintf(&b, "pass out quick to { %s }\n", joinAddrs(a.Hosts))
	}
	b.WriteString("block drop out all\n")
	return b.String()
}

func joinAddrs(addrs []netip.Addr) string {
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = a.String()
	}
	return strings.Join(parts, " ")
}

// ensureAnchorRef appends the `anchor "dezhban"` line to /etc/pf.conf if absent.
// Idempotent: a second block finds the line already there and does nothing.
func ensureAnchorRef() error {
	data, err := os.ReadFile(pfConfPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", pfConfPath, err)
	}
	if strings.Contains(string(data), anchorRef) {
		return nil
	}
	body := string(data)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += "# dezhban anchor (Phase 2) — removed on unblock by restoring the backup\n" + anchorRef + "\n"
	if err := atomicWrite(pfConfPath, []byte(body), 0o644); err != nil {
		return fmt.Errorf("append anchor to %s: %w", pfConfPath, err)
	}
	return nil
}

// backupPfConf copies /etc/pf.conf to the backup path if no backup exists yet.
// Preserving an existing backup avoids clobbering a good copy with an
// already-modified file left by a prior crashed run.
func backupPfConf() error {
	if fileExists(backupPath) {
		return nil
	}
	data, err := os.ReadFile(pfConfPath)
	if err != nil {
		return fmt.Errorf("read %s for backup: %w", pfConfPath, err)
	}
	if err := atomicWrite(backupPath, data, 0o600); err != nil {
		return fmt.Errorf("write pf.conf backup: %w", err)
	}
	return nil
}

func saveState(s savedState) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := atomicWrite(statePath, data, 0o600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

// atomicWrite writes data to a temp file in the same directory, fsyncs it, then
// renames it over path — so a crash mid-write can never leave a partially
// written /etc/pf.conf or state file that a later restore would trust.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dezhban-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func loadState() (savedState, bool) {
	data, err := os.ReadFile(statePath)
	if err != nil {
		return savedState{}, false
	}
	var s savedState
	if err := json.Unmarshal(data, &s); err != nil {
		return savedState{}, false
	}
	return s, true
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
