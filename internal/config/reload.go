package config

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// This file answers one question for a running daemon: given the config it
// started with and the config now on disk, what actually changed, and which of
// those changes can it adopt without being restarted?
//
// Being honest about the second half is the whole point. Reporting a change as
// applied when the daemon is still enforcing the old value is the same class of
// bug as silently coercing a disabled window back to its default — the user
// believes a security setting took effect when it did not.

// dur renders a duration for comparison and display. The negative Disabled
// sentinel is an explicit opt-out, not a duration, so it reads as "off" rather
// than as the meaningless "-1ns".
func dur(d time.Duration) string {
	if d == Disabled {
		return "off"
	}
	return d.String()
}

// KeyValues renders every user-settable key to a comparable, displayable string,
// under the same dotted names `dezhban config set` accepts. Two configs are
// equal for reload purposes exactly when their KeyValues are equal.
//
// The key set is asserted against the CLI's settable-key table by a test, so a
// key cannot become settable without also becoming reloadable-or-restart-flagged.
func KeyValues(c *Config) map[string]string {
	v := c.VPN
	adv := v.Advanced
	return map[string]string{
		"pollInterval":     dur(c.PollInterval),
		"blockedCountries": strings.Join(c.BlockedCountries, ","),
		"hysteresis":       strconv.Itoa(c.Hysteresis),
		"providers":        strings.Join(c.Providers, ","),
		"providerQuorum":   strconv.FormatBool(c.ProviderQuorum),
		"logLevel":         c.LogLevel,

		"vpn.tunnelInterfaces":      strings.Join(v.TunnelInterfaces, ","),
		"vpn.endpoints":             strings.Join(v.Endpoints, ","),
		"vpn.autoDetect":            strconv.FormatBool(v.AutoDetect),
		"vpn.autoDiscoverEndpoints": strconv.FormatBool(v.AutoDiscoverEndpoints),
		"vpn.allowPhysicalDNS":      strconv.FormatBool(v.AllowPhysicalDNS),
		"vpn.allowGeoProviders":     strconv.FormatBool(v.AllowGeoProviders),
		"vpn.allowLocalNetwork":     strconv.FormatBool(v.AllowLocalNetwork),
		"vpn.autoArm":               strconv.FormatBool(v.AutoArm),
		"vpn.armAtBoot":             strconv.FormatBool(v.ArmAtBoot),
		"vpn.switchWindow":          dur(v.SwitchWindow),
		"vpn.redialWindow":          dur(v.RedialWindow),
		"vpn.pauseMax":              dur(v.PauseMax),
		"vpn.endpointRefresh":       dur(v.EndpointRefresh),
		"vpn.endpointGrace":         dur(v.EndpointGrace),
		"vpn.tunnelWatch":           dur(v.TunnelWatch),

		"control.enabled":        strconv.FormatBool(c.Control.Enabled),
		"control.socket":         c.Control.Socket,
		"control.group":          c.Control.Group,
		"control.allowSwitchOps": strconv.FormatBool(c.Control.AllowSwitchOps),
		"control.allowPauseOps":  strconv.FormatBool(c.Control.AllowPauseOps),
		"control.allowConfigOps": strconv.FormatBool(c.Control.AllowConfigOps),

		"vpn.advanced.switchWindowMax":         dur(adv.SwitchWindowMax),
		"vpn.advanced.redialWindowMax":         dur(adv.RedialWindowMax),
		"vpn.advanced.redialMinUptime":         dur(adv.RedialMinUptime),
		"vpn.advanced.redialBudget":            dur(adv.RedialBudget),
		"vpn.advanced.redialBudgetWindow":      dur(adv.RedialBudgetWindow),
		"vpn.advanced.commandFreshness":        dur(adv.CommandFreshness),
		"vpn.advanced.windowDiscoveryInterval": dur(adv.WindowDiscoveryInterval),
		"vpn.advanced.tunnelPruneAfter":        dur(adv.TunnelPruneAfter),
		"vpn.advanced.learnedEndpointTTL":      dur(adv.LearnedEndpointTTL),
		"vpn.advanced.learnedMaxPerProfile":    strconv.Itoa(adv.LearnedMaxPerProfile),
		"vpn.advanced.promoteAfterRefreshes":   strconv.Itoa(adv.PromoteAfterRefreshes),
		"vpn.advanced.endpointWarnThreshold":   strconv.Itoa(adv.EndpointWarnThreshold),
		"vpn.advanced.verifyInterval":          dur(adv.VerifyInterval),
		"vpn.advanced.livenessRedial":          strconv.FormatBool(adv.LivenessRedial),
		"vpn.advanced.windowProtocols":         strings.Join(adv.WindowProtocols, ","),
		"vpn.advanced.windowPorts":             joinInts(adv.WindowPorts),
	}
}

// joinInts renders an int slice for display/comparison — comma-separated, no
// spaces. Mirrors the identically-named helper cmd/dezhban uses for `config
// set`'s get side, kept here too since KeyValues has no dependency on cmd/dezhban.
func joinInts(ns []int) string {
	if len(ns) == 0 {
		return ""
	}
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}

// restartReasons names the keys a running daemon cannot adopt, and says why.
// The reason is shown to the user, so each one names the thing that was already
// built from the old value and cannot be rebuilt underneath a live run loop.
//
// Every key must appear in exactly one of restartReasons and liveKeys, which a
// test enforces. A key in neither is treated as needing a restart: an unclassified
// key is one nobody has reasoned about, and the safe answer there is to under-claim
// rather than to tell the user a setting took effect when it may not have.
var restartReasons = map[string]string{
	"logLevel":       "the logger is wired up before the run loop starts",
	"providers":      "the geo monitor is built from the provider list at startup",
	"providerQuorum": "the geo monitor is built from the quorum setting at startup",

	"control.enabled": "the control socket is bound at startup",
	"control.socket":  "the control socket is bound to its path at startup",
	"control.group":   "the control socket's group ownership is set when it is bound",

	"vpn.tunnelWatch":           "the tunnel watcher runs on its own interval, fixed when it starts",
	"vpn.autoDetect":            "the tunnel watcher's interface allowlist is fixed when it is built",
	"vpn.armAtBoot":             "arm-at-boot is a startup decision; it has already been taken",
	"vpn.endpoints":             "endpoint resolution is wired up at startup",
	"vpn.autoDiscoverEndpoints": "endpoint resolution is wired up at startup",
	"vpn.tunnelInterfaces":      "the pinned tunnel set is resolved at startup",
	"vpn.allowGeoProviders":     "the geo-provider resolver is wired into the run loop at startup",

	"vpn.advanced.commandFreshness":      "the command-file poller is wired up at startup",
	"vpn.advanced.tunnelPruneAfter":      "tunnel pruning is wired up at startup",
	"vpn.advanced.learnedEndpointTTL":    "the learned-endpoint store is wired up at startup",
	"vpn.advanced.learnedMaxPerProfile":  "the learned-endpoint store is wired up at startup",
	"vpn.advanced.promoteAfterRefreshes": "endpoint promotion is wired up at startup",
	"vpn.advanced.endpointWarnThreshold": "endpoint resolution is wired up at startup",

	// Read only when a switch window opens (Options.policyInput), not on any
	// standing interval the run loop rebuilds — the run loop's live-reload path
	// (LiveSettings/applyLive) deliberately doesn't carry these two, so a change
	// takes effect on the next restart rather than the next window.
	"vpn.advanced.windowProtocols": "the switch-window ruleset is only rebuilt when a window opens; live reload doesn't carry this yet",
	"vpn.advanced.windowPorts":     "the switch-window ruleset is only rebuilt when a window opens; live reload doesn't carry this yet",
}

// liveKeys names the keys a running daemon adopts in place. Each is either read
// fresh by the run loop on every pass, or belongs to something the loop rebuilds
// cheaply when it reloads (the country decider, the poll ticker, the standing
// rule set).
var liveKeys = map[string]bool{
	"pollInterval":     true,
	"blockedCountries": true,
	"hysteresis":       true,

	"vpn.allowPhysicalDNS":  true,
	"vpn.allowLocalNetwork": true,
	"vpn.autoArm":           true,
	"vpn.switchWindow":      true,
	"vpn.redialWindow":      true,
	"vpn.pauseMax":          true,
	"vpn.endpointRefresh":   true,
	"vpn.endpointGrace":     true,

	"control.allowSwitchOps": true,
	"control.allowPauseOps":  true,
	"control.allowConfigOps": true,

	"vpn.advanced.switchWindowMax":         true,
	"vpn.advanced.redialWindowMax":         true,
	"vpn.advanced.redialMinUptime":         true,
	"vpn.advanced.redialBudget":            true,
	"vpn.advanced.redialBudgetWindow":      true,
	"vpn.advanced.windowDiscoveryInterval": true,
	"vpn.advanced.verifyInterval":          true,
	"vpn.advanced.livenessRedial":          true,
}

// restartReasonFor returns why a key cannot be applied live, or "" when it can.
// Unclassified keys are reported as needing a restart — see restartReasons.
func restartReasonFor(key string) string {
	if reason, ok := restartReasons[key]; ok {
		return reason
	}
	if liveKeys[key] {
		return ""
	}
	return "this setting is not classified as live-appliable"
}

// A Change is one key whose value differs between two configs. RestartReason is
// empty when a running daemon can adopt the new value in place.
type Change struct {
	Key           string
	From          string
	To            string
	RestartReason string
}

// NeedsRestart reports whether this change requires restarting the daemon.
func (c Change) NeedsRestart() bool { return c.RestartReason != "" }

// Changes lists every key that differs between two configs, sorted by key so
// callers and tests get a stable order. Either config may be nil, which reads as
// "nothing configured" — a nil-to-nil comparison is empty rather than an error.
func Changes(old, cur *Config) []Change {
	before, after := map[string]string{}, map[string]string{}
	if old != nil {
		before = KeyValues(old)
	}
	if cur != nil {
		after = KeyValues(cur)
	}
	var out []Change
	for key, to := range after {
		from, had := before[key]
		if had && from == to {
			continue
		}
		out = append(out, Change{Key: key, From: from, To: to, RestartReason: restartReasonFor(key)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// MergeLive returns the configuration that will actually be in force after a
// running daemon adopts `cur`: every live-appliable value taken from `cur`, and
// everything else left as it is in `base`.
//
// It copies live fields onto a copy of `base` rather than starting from `cur`
// and putting restart-required fields back. The difference matters: anything not
// explicitly listed here — a field nobody has classified yet — stays at its
// running value, which is the same conservative default restartReasonFor takes.
// The alternative would adopt unclassified settings silently, which is exactly
// the behaviour this whole file exists to prevent.
func MergeLive(base, cur *Config) *Config {
	out := *base

	out.PollInterval = cur.PollInterval
	out.BlockedCountries = cur.BlockedCountries
	out.Hysteresis = cur.Hysteresis

	out.VPN.AllowPhysicalDNS = cur.VPN.AllowPhysicalDNS
	out.VPN.AllowLocalNetwork = cur.VPN.AllowLocalNetwork
	out.VPN.AutoArm = cur.VPN.AutoArm
	out.VPN.SwitchWindow = cur.VPN.SwitchWindow
	out.VPN.RedialWindow = cur.VPN.RedialWindow
	out.VPN.PauseMax = cur.VPN.PauseMax
	out.VPN.EndpointRefresh = cur.VPN.EndpointRefresh
	out.VPN.EndpointGrace = cur.VPN.EndpointGrace

	out.Control.AllowSwitchOps = cur.Control.AllowSwitchOps
	out.Control.AllowPauseOps = cur.Control.AllowPauseOps
	out.Control.AllowConfigOps = cur.Control.AllowConfigOps

	out.VPN.Advanced.SwitchWindowMax = cur.VPN.Advanced.SwitchWindowMax
	out.VPN.Advanced.RedialWindowMax = cur.VPN.Advanced.RedialWindowMax
	out.VPN.Advanced.RedialMinUptime = cur.VPN.Advanced.RedialMinUptime
	out.VPN.Advanced.RedialBudget = cur.VPN.Advanced.RedialBudget
	out.VPN.Advanced.RedialBudgetWindow = cur.VPN.Advanced.RedialBudgetWindow
	out.VPN.Advanced.WindowDiscoveryInterval = cur.VPN.Advanced.WindowDiscoveryInterval
	out.VPN.Advanced.VerifyInterval = cur.VPN.Advanced.VerifyInterval
	out.VPN.Advanced.LivenessRedial = cur.VPN.Advanced.LivenessRedial

	return &out
}

// SplitByRestart partitions changes into those a running daemon can adopt and
// those that need a restart. Callers report both, so the user is never told a
// change took effect when it did not.
func SplitByRestart(changes []Change) (live, needRestart []Change) {
	for _, ch := range changes {
		if ch.NeedsRestart() {
			needRestart = append(needRestart, ch)
			continue
		}
		live = append(live, ch)
	}
	return live, needRestart
}
