//go:build windows

package firewall

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

// Windows enforcement via the Windows Firewall (NetSecurity cmdlets).
//
// Backend choice (mirrors pf/nft, see pf_darwin.go and nft_linux.go): we shell
// to PowerShell's `New-NetFirewallRule -Group dezhban` rather than linking
// tailscale/wf. The plan tentatively preferred WFP but sanctioned this as the
// alternative; we take it for the same reasons as Linux — dependency-light,
// clean cross-compilation, and a teardown that is surgical by construction
// (`Remove-NetFirewallRule -Group dezhban` only ever touches our rules).
//
// Model: Windows Firewall lets Block rules win over Allow rules, so a
// "block-all + allow-some" rule pair would never let the allowlist through.
// Instead we set each profile's DefaultOutboundAction to Block (the implicit
// default-deny) and add Allow rules in the dezhban group for the exceptions.
// The prior DefaultOutboundAction is snapshotted on the first block (to a state
// file under ProgramData) so Unblock restores it exactly, even across separate
// `dezhban` invocations — the Windows twin of pf's saved-state file.
const groupName = "dezhban"

// fwProfiles are the three Windows Firewall profiles whose outbound default we
// flip; we save and restore each independently.
var fwProfiles = []string{"Domain", "Private", "Public"}

func stateDir() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "dezhban")
}

func statePath() string { return filepath.Join(stateDir(), "fw.state") }

// wfpBackend is the Windows FirewallBackend. It holds no in-memory state: the
// authoritative state is the dezhban rule group plus the saved DefaultOutbound
// snapshot on disk, so it survives across separate invocations.
type wfpBackend struct{}

// New returns the Windows firewall backend.
func New() (FirewallBackend, error) {
	return &wfpBackend{}, nil
}

// savedState records the per-profile DefaultOutboundAction to restore on
// unblock. Captured only on the first block so re-blocking never clobbers the
// true pre-block state.
type savedState struct {
	// OutboundAction maps profile name -> its DefaultOutboundAction before block
	// (e.g. "Allow", "Block", "NotConfigured").
	OutboundAction map[string]string `json:"outboundAction"`
}

// Block is the legacy direct-connection entry point: a full block whose only
// exceptions are loopback and the dst-IP allowlist. It is Apply with
// ModeFullBlock and no tunnel interfaces.
func (b *wfpBackend) Block(a Allowlist) error {
	return b.Apply(Policy{Mode: ModeFullBlock, Allowlist: a})
}

// Apply installs the dezhban rule group for p and flips the profiles' outbound
// default to Block. Re-applying first removes the group, so rules never stack
// (idempotent). The prior outbound defaults are snapshotted only on the first
// block.
func (b *wfpBackend) Apply(p Policy) error {
	// Guard mode with no tunnel interface would allow only loopback under a
	// default-deny — a total lockout. Reject at the seam, mirroring pf/nft.
	if p.Mode == ModeGuard && len(p.TunnelIfaces) == 0 && len(p.TunnelGroups) == 0 {
		return fmt.Errorf("guard mode requires at least one tunnel interface")
	}

	// First block only: snapshot prior outbound defaults so Unblock can restore.
	if !fileExists(statePath()) {
		prev, err := queryOutboundDefaults()
		if err != nil {
			return fmt.Errorf("query firewall defaults: %w", err)
		}
		if err := saveState(savedState{OutboundAction: prev}); err != nil {
			return err
		}
	}

	if _, err := powershell(renderBlockScript(p)); err != nil {
		return fmt.Errorf("apply dezhban firewall rules: %w", err)
	}
	return nil
}

// Unblock restores the saved outbound defaults, then removes ONLY the dezhban
// rule group. Safe to run when nothing is blocked.
//
// Order matters: defaults are restored FIRST, while the allow rules are still in
// place, so there is never a window of "Block default + no allow rules" (a total
// outbound lockout). If the saved state is missing or corrupt we restore every
// profile to Allow rather than leaving the default at Block — failing to read
// state must never strand the host with no egress (CLAUDE.md: a stale block-all
// rule can lock the user out of their own network).
func (b *wfpBackend) Unblock() error {
	st, ok := loadState()
	var sb strings.Builder
	sb.WriteString("$ErrorActionPreference='Stop'\n")
	for _, prof := range fwProfiles {
		action := "Allow" // fail-open on restore: never leave a profile at Block
		if ok {
			if a := st.OutboundAction[prof]; a != "" {
				action = a
			}
		}
		fmt.Fprintf(&sb, "Set-NetFirewallProfile -Name %s -DefaultOutboundAction %s\n", prof, action)
	}
	if _, err := powershell(sb.String()); err != nil {
		return fmt.Errorf("restore firewall defaults: %w", err)
	}

	if _, err := powershell(removeGroupScript()); err != nil {
		return fmt.Errorf("remove dezhban firewall rules: %w", err)
	}
	if ok {
		_ = os.Remove(statePath())
	}
	return nil
}

// IsBlocked reports whether the dezhban rule group is currently installed.
func (b *wfpBackend) IsBlocked() (bool, error) {
	out, err := powershell(
		"if (Get-NetFirewallRule -Group " + groupName +
			" -ErrorAction SilentlyContinue) { 'blocked' } else { 'clear' }")
	if err != nil {
		return false, err
	}
	return strings.Contains(out, "blocked"), nil
}

// Cleanup is best-effort teardown for shutdown/panic. It is just Unblock; any
// error is returned for the caller to log, never treated as fatal.
func (b *wfpBackend) Cleanup() error {
	return b.Unblock()
}

// renderBlockScript builds the PowerShell that installs the dezhban allow rules
// and flips the outbound default to Block. It is the Windows twin of pf's
// renderRuleset and follows the same postures:
//
//   - ModeGuard: allow egress on the tunnel interface(s) and the handshake to
//     the VPN endpoint(s); the Block default cuts everything else, so a tunnel
//     drop has no physical leak.
//   - ModeFullBlock, direct (no tunnel ifaces): allow the dst-IP DNS + geo-API
//     allowlist — the legacy model, valid only off-VPN.
//   - ModeFullBlock, VPN (tunnel ifaces present): no tunnel-iface allow, so no
//     user traffic leaks to a forbidden exit — but keep the endpoint allow so the
//     encrypted handshake reaches the server and the tunnel can reconnect.
//     Identical to ModeGuard minus the tunnel-iface allow. The dst-IP allowlist
//     is still meaningless under a tunnel.
//
// The script opens by removing any existing dezhban group, so a re-block
// replaces rather than stacks (idempotent), and sets the outbound default last.
func renderBlockScript(p Policy) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference='Stop'\n")
	b.WriteString("Remove-NetFirewallRule -Group " + groupName + " -ErrorAction SilentlyContinue\n")

	rule := func(name, args string) {
		fmt.Fprintf(&b, "New-NetFirewallRule -DisplayName 'dezhban-%s' -Group %s -Direction Outbound -Action Allow %s | Out-Null\n",
			name, groupName, args)
	}

	// Loopback always passes.
	rule("loopback", "-RemoteAddress 127.0.0.1,::1")

	// defaultAction is the profile's outbound default installed at the end. It is
	// Block for every posture EXCEPT an unrestricted switch window, which must
	// allow all outbound so a brand-new VPN's handshake can complete. (Windows
	// ignores TunnelGroups — it matches interfaces by exact alias only.)
	defaultAction := "Block"

	switch p.Mode {
	case ModeSwitchWindow:
		if len(p.WindowProtos) == 0 && len(p.WindowPorts) == 0 {
			// Unrestricted: keep only the marker (loopback) rule so the group stays
			// non-empty for surgical teardown, and flip the default to Allow. The
			// daemon reverts to guard (default Block) when the window closes.
			defaultAction = "Allow"
		} else {
			if len(p.TunnelIfaces) > 0 {
				rule("tunnel", "-InterfaceAlias "+psStringList(p.TunnelIfaces))
			}
			if ep := psAddrList(p.VPNEndpoints); ep != "" {
				rule("endpoint", "-RemoteAddress "+ep)
			}
			rule("dns-any-udp", "-Protocol UDP -RemotePort 53")
			rule("dns-any-tcp", "-Protocol TCP -RemotePort 53")
			emitLocalNetworkRules(rule, p)
			emitWindowPortRules(rule, p)
		}
	case ModeGuard:
		if len(p.TunnelIfaces) > 0 {
			rule("tunnel", "-InterfaceAlias "+psStringList(p.TunnelIfaces))
		}
		if len(p.VPNEndpoints) > 0 {
			rule("endpoint", "-RemoteAddress "+psAddrList(p.VPNEndpoints))
		}
		emitAllowPhysicalDNSRules(rule, p)
		emitLocalNetworkRules(rule, p)
	default: // ModeFullBlock
		if isVPNPolicy(p) {
			// VPN full block (including the zero-tunnel standing posture): no
			// tunnel-iface allow, so no user traffic leaks to a forbidden exit — but
			// keep the endpoint allow so the encrypted handshake reaches the server
			// and the tunnel can reconnect (a cut endpoint would livelock recovery).
			if ep := psAddrList(p.VPNEndpoints); ep != "" {
				rule("endpoint", "-RemoteAddress "+ep)
			}
			emitAllowPhysicalDNSRules(rule, p)
			emitLocalNetworkRules(rule, p)
		} else {
			// Legacy direct model: dst-IP allowlist.
			if dns := psAddrList(p.Allowlist.DNS); dns != "" {
				rule("dns-udp", "-Protocol UDP -RemotePort 53 -RemoteAddress "+dns)
				rule("dns-tcp", "-Protocol TCP -RemotePort 53 -RemoteAddress "+dns)
			}
			if hosts := psAddrList(p.Allowlist.Hosts); hosts != "" {
				rule("hosts", "-RemoteAddress "+hosts)
			}
		}
	}

	// Set the profile outbound default last, once the allow rules are in place.
	fmt.Fprintf(&b, "Set-NetFirewallProfile -Name %s -DefaultOutboundAction %s\n",
		strings.Join(fwProfiles, ","), defaultAction)
	return b.String()
}

// emitWindowPortRules renders the proto/port allows for a restricted switch
// window (WFP). Protocols default to udp+tcp when unspecified.
func emitWindowPortRules(rule func(name, args string), p Policy) {
	protos := p.WindowProtos
	if len(protos) == 0 {
		protos = []string{"udp", "tcp"}
	}
	for _, proto := range protos {
		for _, port := range p.WindowPorts {
			up := strings.ToUpper(proto)
			rule(fmt.Sprintf("win-%s-%d", strings.ToLower(proto), port),
				fmt.Sprintf("-Protocol %s -RemotePort %d", up, port))
		}
	}
}

// emitLocalNetworkRules renders the destination-scoped LAN pass
// (vpn.allowLocalNetwork). New-NetFirewallRule's -RemoteAddress accepts mixed
// v4/v6 CIDRs in one comma-separated list, so unlike nft this needs no split.
func emitLocalNetworkRules(rule func(name, args string), p Policy) {
	if !p.AllowLocalNetwork {
		return
	}
	rule("local-network", "-RemoteAddress "+strings.Join(LocalNetworkPrefixes, ","))
}

// emitAllowPhysicalDNSRules renders the opt-in plain-DNS pass
// (vpn.allowPhysicalDNS) so a VPN client can re-resolve its server hostname
// while the tunnel is down. Deliberately unscoped by address — resolution must
// work regardless of which resolver the system uses on reconnect.
func emitAllowPhysicalDNSRules(rule func(name, args string), p Policy) {
	if !p.AllowPhysicalDNS {
		return
	}
	rule("dns-any-udp", "-Protocol UDP -RemotePort 53")
	rule("dns-any-tcp", "-Protocol TCP -RemotePort 53")
}

// removeGroupScript removes the dezhban rule group. Idempotent: missing rules
// are silently ignored.
func removeGroupScript() string {
	return "Remove-NetFirewallRule -Group " + groupName + " -ErrorAction SilentlyContinue"
}

// queryOutboundDefaults reads each profile's current DefaultOutboundAction so it
// can be restored verbatim on unblock. Output lines are "Name=Action".
func queryOutboundDefaults() (map[string]string, error) {
	out, err := powershell(
		"Get-NetFirewallProfile -All | ForEach-Object { \"$($_.Name)=$($_.DefaultOutboundAction)\" }")
	if err != nil {
		return nil, err
	}
	res := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if name, action, ok := strings.Cut(line, "="); ok {
			res[strings.TrimSpace(name)] = strings.TrimSpace(action)
		}
	}
	return res, nil
}

// psAddrList renders addresses as a PowerShell comma-separated list:
// "1.1.1.1,8.8.8.8". Returns "" for an empty slice so callers can skip the rule.
func psAddrList(addrs []netip.Addr) string {
	if len(addrs) == 0 {
		return ""
	}
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = a.Unmap().String()
	}
	return strings.Join(parts, ",")
}

// psStringList renders names as a quoted PowerShell list: 'utun4','wg0'.
func psStringList(names []string) string {
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = "'" + strings.ReplaceAll(n, "'", "''") + "'"
	}
	return strings.Join(parts, ",")
}

func saveState(s savedState) error {
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.WriteFile(statePath(), data, 0o600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

func loadState() (savedState, bool) {
	data, err := os.ReadFile(statePath())
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
