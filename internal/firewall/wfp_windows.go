//go:build windows

package firewall

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/behnam-rk/dezhban/internal/atomicfile"
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

// appliedActionPath records the DefaultOutboundAction the last successful Apply
// set every profile to — IsBlocked's cross-check that the boundary Apply
// actually installed hasn't drifted out from under the still-present allow
// rules (see IsBlocked's doc comment).
func appliedActionPath() string { return filepath.Join(stateDir(), "fw.applied") }

// writeAppliedAction records action as the DefaultOutboundAction Apply just
// applied, via temp-file-then-rename rather than a plain WriteFile — the
// rename is atomic, so a crash between the two never leaves this file
// half-written. IsBlocked's drift check reads this file's exact bytes back
// and compares them against the live profiles, so a truncated/empty file
// from a non-atomic write would read as "every profile drifted" and force a
// repair loop that only a later, fully-written Apply would clear.
func writeAppliedAction(action string) error {
	return atomicfile.Write(appliedActionPath(), []byte(action), 0o600)
}

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

// Block is the `block --force` entry point: a full block whose only
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
	// Best-effort: IsBlocked degrades to its old, weaker check if this is
	// missing (e.g. leftover state from before this file existed), so a
	// failure here must not fail the Apply that just succeeded. Written
	// atomically (temp + rename), which means a failed write leaves the
	// PREVIOUS Apply's value untouched on disk rather than truncating it —
	// so on failure we remove it outright instead of leaving it in place.
	// A stale-but-present file would make IsBlocked's drift check compare
	// the live (correctly re-applied) profiles against what an EARLIER
	// Apply set, not this one, misreporting real drift and triggering an
	// unwanted repair. An absent file is already the designed fallback —
	// "no record of what was last applied" degrades to the weaker
	// group-existence-only check, not evidence of tampering.
	if err := writeAppliedAction(expectedOutboundAction(p)); err != nil {
		_ = os.Remove(appliedActionPath())
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
	_ = os.Remove(appliedActionPath())
	return nil
}

// IsBlocked reports whether the dezhban rule group is currently installed AND
// the profile outbound default Apply set still matches what it applied.
//
// The rule group existing is not sufficient on its own: our rules are all
// Allow exceptions layered on the profile DefaultOutboundAction doing the
// actual blocking (see the Model note above renderBlockScript). Something
// else — Group Policy refresh, another security tool, an admin running
// `Set-NetFirewallProfile` by hand — can flip that default back to Allow
// without touching a single dezhban rule, leaving the group intact while
// every packet the Allow rules didn't already cover sails through unfiltered.
// A bare group-existence check would report "blocked" through that entire
// gap. Cross-checking against the action Apply actually persisted
// (appliedActionPath) catches it, while still tolerating the ONE posture
// where Allow is the deliberately-correct default: an unrestricted switch
// window (see expectedOutboundAction) — comparing against what THIS Apply
// call set, not a hardcoded "Block", is what makes that distinction safe
// instead of reporting a false "MISSING" (and triggering an unwanted repair)
// every time a window opens.
func (b *wfpBackend) IsBlocked() (bool, error) {
	blocked, got, err := queryBlockedAndDefaults()
	if err != nil {
		return false, err
	}
	if !blocked {
		return false, nil
	}

	wantRaw, err := os.ReadFile(appliedActionPath())
	if err != nil {
		// No record of what we last applied (state predates this file, or was
		// cleared) — degrade to the group-existence check above rather than
		// treat an unrelated read failure as evidence of tampering.
		return true, nil
	}
	want := strings.TrimSpace(string(wantRaw))

	for _, prof := range fwProfiles {
		if got[prof] != want {
			return false, nil
		}
	}
	return true, nil
}

// queryBlockedAndDefaults combines the group-existence check and the
// per-profile DefaultOutboundAction query into a single PowerShell
// invocation. IsBlocked is called synchronously from the run loop's verifyC
// tick, which CLAUDE.md requires stay bounded — two sequential psTimeout-
// bounded subprocess calls would nearly double that tick's worst case.
func queryBlockedAndDefaults() (bool, map[string]string, error) {
	out, err := powershell(
		"$g = Get-NetFirewallRule -Group " + groupName + " -ErrorAction SilentlyContinue\n" +
			"if ($g) {\n" +
			"  'blocked'\n" +
			"  Get-NetFirewallProfile -All | ForEach-Object { \"$($_.Name)=$($_.DefaultOutboundAction)\" }\n" +
			"} else {\n" +
			"  'clear'\n" +
			"}")
	if err != nil {
		return false, nil, err
	}
	return parseProfileQuery(out)
}

// parseProfileQuery parses queryBlockedAndDefaults' captured PowerShell output.
// Split out so it can be exercised in tests against captured tool output
// without shelling out to PowerShell — same rationale as pf_darwin's
// mainRulesetReferencesAnchor.
func parseProfileQuery(out string) (bool, map[string]string, error) {
	lines := strings.Split(out, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "blocked" {
		return false, nil, nil
	}
	res := make(map[string]string)
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if name, action, ok := strings.Cut(line, "="); ok {
			res[strings.TrimSpace(name)] = strings.TrimSpace(action)
		}
	}
	// Every profile in fwProfiles must have a line, or a caller comparing
	// got[prof] against a wanted action would silently read a missing entry as
	// "" — the Go zero value — which never matches, so a transient
	// PowerShell/WMI hiccup that drops one profile's line (while the script
	// still exits 0) would report real drift and trigger an unwanted repair.
	// That is not evidence of tampering, same discipline as the unreadable
	// appliedActionPath case in IsBlocked: an incomplete read is an error, not
	// a "missing" verdict.
	var missing []string
	for _, prof := range fwProfiles {
		if _, ok := res[prof]; !ok {
			missing = append(missing, prof)
		}
	}
	if len(missing) > 0 {
		return false, nil, fmt.Errorf("firewall profile query missing output for: %s", strings.Join(missing, ", "))
	}
	return true, res, nil
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
//   - ModeFullBlock, no VPN context (no tunnel ifaces): allow the dst-IP DNS +
//     geo-API allowlist — what `block --force` renders.
//   - ModeFullBlock, VPN (tunnel ifaces present): no tunnel-iface allow, so no
//     user traffic leaks to a forbidden exit — but keep the endpoint allow so the
//     encrypted handshake reaches the server and the tunnel can redial.
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

	defaultAction := expectedOutboundAction(p)

	switch p.Mode {
	case ModeSwitchWindow:
		if len(p.WindowProtos) == 0 && len(p.WindowPorts) == 0 {
			// Unrestricted: keep only the marker (loopback) rule so the group stays
			// non-empty for surgical teardown. defaultAction is already Allow (see
			// expectedOutboundAction). The daemon reverts to guard (default Block)
			// when the window closes.
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
			// and the tunnel can redial (a cut endpoint would livelock recovery).
			if ep := psAddrList(p.VPNEndpoints); ep != "" {
				rule("endpoint", "-RemoteAddress "+ep)
			}
			emitTunnelProviderRules(rule, p)
			emitAllowPhysicalDNSRules(rule, p)
		} else {
			// `block --force` (no VPN context): dst-IP allowlist.
			if dns := psAddrList(p.Allowlist.DNS); dns != "" {
				rule("dns-udp", "-Protocol UDP -RemotePort 53 -RemoteAddress "+dns)
				rule("dns-tcp", "-Protocol TCP -RemotePort 53 -RemoteAddress "+dns)
			}
			if hosts := psAddrList(p.Allowlist.Hosts); hosts != "" {
				rule("hosts", "-RemoteAddress "+hosts)
			}
		}
		// Outside the isVPNPolicy split on purpose — see the same hoist in
		// pf_darwin.go. AllowLocalNetwork belongs to the posture, not to which
		// FULL BLOCK shape rendered it.
		emitLocalNetworkRules(rule, p)
	}

	// Set the profile outbound default last, once the allow rules are in place.
	fmt.Fprintf(&b, "Set-NetFirewallProfile -Name %s -DefaultOutboundAction %s\n",
		strings.Join(fwProfiles, ","), defaultAction)
	return b.String()
}

// expectedOutboundAction is the profile DefaultOutboundAction renderBlockScript
// installs for p, and what IsBlocked cross-checks the live profiles against
// (see IsBlocked's doc comment). Block for every posture EXCEPT an unrestricted
// switch window, which must allow all outbound so a brand-new VPN's handshake
// can complete. Factored out of renderBlockScript so Apply can persist the
// value it actually applied without the two ever drifting apart.
func expectedOutboundAction(p Policy) string {
	if p.Mode == ModeSwitchWindow && len(p.WindowProtos) == 0 && len(p.WindowPorts) == 0 {
		return "Allow"
	}
	return "Block"
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

// emitTunnelProviderRules renders the tunnel-scoped geo-provider pass used in
// FULL BLOCK. WFP matches interface by exact alias only, so tunnel GROUPS cannot
// be expressed here — with only a group configured this emits nothing and the
// daemon falls back to lift-and-probe.
//
// Deliberately NO accompanying DNS pass; see tunnelProviderRules in pf_darwin.go
// for why an unscoped port-53 rule here would leak every hostname this host
// resolves to the very exit FULL BLOCK is refusing.
func emitTunnelProviderRules(rule func(name, args string), p Policy) {
	if len(p.ProviderAddrs) == 0 || len(p.TunnelIfaces) == 0 {
		return
	}
	rule("providers-via-tunnel",
		"-InterfaceAlias "+psStringList(p.TunnelIfaces)+" -RemoteAddress "+psAddrList(p.ProviderAddrs))
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
// work regardless of which resolver the system uses on redial.
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
	for line := range strings.SplitSeq(out, "\n") {
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
