//go:build darwin

package firewall

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/behnam-rk/dezhban/internal/state"
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

// Block is the legacy direct-connection entry point: a full block whose only
// exceptions are loopback and the dst-IP allowlist. It is Apply with
// ModeFullBlock and no tunnel interfaces.
func (b *pfBackend) Block(a Allowlist) error {
	return b.Apply(Policy{Mode: ModeFullBlock, Allowlist: a})
}

// Apply installs the ruleset for p into the dezhban anchor. The mechanism is
// identical regardless of mode (validate → snapshot → anchor ref → load → enable);
// only the rendered ruleset differs, so guard and full-block share one code path
// and the same idempotent, surgical teardown.
func (b *pfBackend) Apply(p Policy) error {
	// Guard mode with no tunnel interface would render only loopback + a default
	// deny — a total lockout with no guard at all. cmdBlock checks this, but a
	// programmatic caller (the Phase 3 daemon) must not be able to self-inflict
	// it, so reject it at the backend seam.
	if p.Mode == ModeGuard && len(p.TunnelIfaces) == 0 && len(p.TunnelGroups) == 0 {
		return fmt.Errorf("guard mode requires at least one tunnel interface or group")
	}
	ruleset := renderRuleset(p)
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

// renderRuleset builds the pf ruleset loaded into the dezhban anchor. Every
// posture is loopback-always + a default-deny `block drop out all` last; the
// quick passes in between depend on the mode. Every pass is stateless (see the
// `no state` rationale below); inbound is never blocked (we only cut egress), so
// return traffic for allowed flows is unaffected.
//
//   - ModeGuard: pass egress on the tunnel interface(s) and the handshake to the
//     VPN endpoint(s). Everything else is dropped, so if the tunnel disappears,
//     traffic falling back to the physical interface is cut with no leak.
//   - ModeFullBlock, direct (no tunnel ifaces): pass the dst-IP DNS + geo-API
//     allowlist — the legacy model, valid only off-VPN.
//   - ModeFullBlock, VPN (tunnel ifaces present): no tunnel-iface pass, so no
//     user traffic leaks to a forbidden exit — but keep the endpoint pass so the
//     encrypted handshake reaches the server and the tunnel can reconnect.
//     Identical to ModeGuard minus the tunnel-iface pass. The dst-IP allowlist
//     is still meaningless under a tunnel and stays omitted; the daemon opens a
//     brief guard window to probe for recovery (see Phase 4).
func renderRuleset(p Policy) string {
	var b strings.Builder
	b.WriteString("# dezhban ruleset — default-deny outbound.\n")
	// Every pass is `no state`. We filter OUTBOUND only, so return traffic is
	// never touched and per-flow state buys nothing. Critically, pf's default
	// (`keep state flags S/SA`) admits only TCP flows that START after the rules
	// load — it drops mid-stream packets of connections already open at block
	// time, which would tear down the live VPN tunnel transport (the encrypted
	// connection to the endpoint, established before the guard). Stateless passes
	// admit that existing connection.
	b.WriteString("pass quick on lo0 all no state\n")
	switch p.Mode {
	case ModeSwitchWindow:
		if len(p.WindowProtos) == 0 && len(p.WindowPorts) == 0 {
			// Unrestricted: pass all outbound for the (daemon-bounded) window so
			// any VPN's handshake to any server can complete.
			b.WriteString("pass out quick all no state\n")
		} else {
			// Restricted: keep the standing passes plus the given proto/port set.
			if len(p.TunnelIfaces) > 0 {
				fmt.Fprintf(&b, "pass out quick on { %s } all no state\n", strings.Join(p.TunnelIfaces, " "))
			}
			if groups := ifaceGroups(p.TunnelGroups); groups != "" {
				fmt.Fprintf(&b, "pass out quick on { %s } all no state\n", groups)
			}
			if len(p.VPNEndpoints) > 0 {
				fmt.Fprintf(&b, "pass out quick to { %s } no state\n", joinAddrs(p.VPNEndpoints))
			}
			b.WriteString(allowPhysicalDNSRule) // resolution during a restricted window
			if p.AllowLocalNetwork {
				b.WriteString(localNetworkRule())
			}
			if ports := joinPorts(p.WindowPorts); ports != "" {
				fmt.Fprintf(&b, "pass out quick proto { %s } to any port { %s } no state\n", windowProtoSet(p.WindowProtos), ports)
			}
		}
	case ModeGuard:
		if len(p.TunnelIfaces) > 0 {
			fmt.Fprintf(&b, "pass out quick on { %s } all no state\n", strings.Join(p.TunnelIfaces, " "))
		}
		if groups := ifaceGroups(p.TunnelGroups); groups != "" {
			// Interface-group pass: matches every current and future interface of
			// this class (e.g. every utunN), so a new tunnel is covered with no rule
			// reload. Safe because tunnels re-encapsulate onto the still-blocked
			// physical interface.
			fmt.Fprintf(&b, "pass out quick on { %s } all no state\n", groups)
		}
		if len(p.VPNEndpoints) > 0 {
			fmt.Fprintf(&b, "pass out quick to { %s } no state\n", joinAddrs(p.VPNEndpoints))
		}
		if p.AllowPhysicalDNS {
			b.WriteString(allowPhysicalDNSRule)
		}
		if p.AllowLocalNetwork {
			b.WriteString(localNetworkRule())
		}
	default: // ModeFullBlock
		if isVPNPolicy(p) {
			// VPN full block (including the zero-tunnel standing posture): no
			// tunnel-iface pass, so no user traffic leaks to a forbidden exit — but
			// keep the endpoint pass so the encrypted handshake reaches the server
			// and the tunnel can reconnect (a cut endpoint would livelock recovery).
			if len(p.VPNEndpoints) > 0 {
				fmt.Fprintf(&b, "pass out quick to { %s } no state\n", joinAddrs(p.VPNEndpoints))
			}
			// Tunnel-scoped geo-provider pass: lets the exit-country lookup run
			// through the tunnel WITHOUT lifting the guard, which is what the
			// recovery probe used to do for ~8s on every tick.
			b.WriteString(tunnelProviderRules(p))
			if p.AllowPhysicalDNS {
				b.WriteString(allowPhysicalDNSRule)
			}
			if p.AllowLocalNetwork {
				b.WriteString(localNetworkRule())
			}
		} else {
			// Legacy direct model: dst-IP allowlist.
			if len(p.Allowlist.DNS) > 0 {
				fmt.Fprintf(&b, "pass out quick proto { udp tcp } to { %s } port 53 no state\n", joinAddrs(p.Allowlist.DNS))
			}
			if len(p.Allowlist.Hosts) > 0 {
				fmt.Fprintf(&b, "pass out quick to { %s } no state\n", joinAddrs(p.Allowlist.Hosts))
			}
		}
	}
	b.WriteString("block drop out all\n")
	return b.String()
}

// ifaceGroups renders a space-separated pf interface-group list, or "" if empty.
func ifaceGroups(groups []string) string {
	return strings.Join(groups, " ")
}

// windowProtoSet renders the pf proto set for a restricted switch window,
// defaulting to udp+tcp when unspecified.
func windowProtoSet(protos []string) string {
	if len(protos) == 0 {
		return "udp tcp"
	}
	return strings.Join(protos, " ")
}

// joinPorts renders ports as a space-separated pf list, or "" if empty.
func joinPorts(ports []int) string {
	var b strings.Builder
	for i, p := range ports {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%d", p)
	}
	return b.String()
}

// allowPhysicalDNSRule is the opt-in plain-DNS pass (vpn.allowPhysicalDNS): a
// VPN client that re-resolves its server hostname on reconnect can do so while
// the tunnel is down. `to any` deliberately — resolution must work regardless
// of which resolver the system uses on reconnect.
const allowPhysicalDNSRule = "pass out quick proto { udp tcp } to any port 53 no state\n"

// localNetworkRule renders the destination-scoped LAN pass (vpn.allowLocalNetwork).
// pf infers each address's family from the address itself, so v4 and v6 prefixes
// can share one list — verified with `pfctl -nvf`, a mixed list expands to one
// inet rule and one inet6 rule.
// tunnelProviderRules renders the tunnel-scoped geo-provider passes used in FULL
// BLOCK, so the exit-country lookup can traverse the tunnel while all other user
// traffic stays cut. Empty when there is no tunnel or no resolved provider IP —
// with either missing the rule cannot be built, and the daemon falls back to the
// old lift-and-probe rather than losing the ability to recover.
//
// ONE rule, scoped to both the tunnel interface and the provider addresses.
//
// Deliberately NO accompanying DNS pass. An earlier draft added
// `on <tunnel> proto { udp tcp } to any port 53` so provider hostnames could be
// re-resolved — but `to any` is destination-unscoped, so it passed EVERY
// application's DNS through the tunnel to the forbidden exit's resolver, for as
// long as FULL BLOCK lasted. That hands the exit whose country we are refusing a
// continuous log of every hostname this host looks up: precisely the exposure
// FULL BLOCK exists to prevent, and far broader than the daemon's own need.
//
// Losing it is safe because the provider set is refreshed on the endpoint
// cadence while the guard is HEALTHY, where tunnel DNS is already unrestricted,
// so FULL BLOCK begins with a fresh set. If the providers do rotate mid-block
// the lookup fails, the posture holds, and recovery falls back to lift-and-probe
// — which lifts the guard, letting the next refresh succeed and the scoped rule
// heal itself. A bounded, self-clearing leak beats a continuous metadata one.
func tunnelProviderRules(p Policy) string {
	ifaces := append(append([]string{}, p.TunnelIfaces...), p.TunnelGroups...)
	if len(ifaces) == 0 || len(p.ProviderAddrs) == 0 {
		return ""
	}
	return fmt.Sprintf("pass out quick on { %s } to { %s } no state\n",
		strings.Join(ifaces, " "), joinAddrs(p.ProviderAddrs))
}

func localNetworkRule() string {
	return fmt.Sprintf("pass out quick to { %s } no state\n", strings.Join(LocalNetworkPrefixes, " "))
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
	// state.DirMode (0755), not 0700: this is the shared daemon state directory,
	// and whichever component creates it first fixes its mode for everyone. Creating
	// it 0700 here locked the unprivileged menubar app out of state.json and every
	// admin user out of control.sock — see state.EnsureDir. The file itself stays
	// 0600, which is where this state's confidentiality actually lives.
	if err := os.MkdirAll(stateDir, state.DirMode); err != nil {
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
