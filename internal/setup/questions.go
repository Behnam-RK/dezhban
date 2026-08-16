// Package setup holds the first-run wizard's decisions: what it asks, in what
// order, which answers unlock which follow-ups, and how the answers become a
// config.
//
// It is deliberately free of any presentation. The CLI renders these questions
// with huh; the macOS app renders the same list natively from
// `dezhban setup --questions --json`. Neither owns the question set, so the two
// wizards cannot ask different things or apply the same answer differently —
// the same reason internal/config owns the tunable schema rather than each
// surface holding its own copy.
//
// Nothing here touches the firewall, the daemon, or the filesystem. It reads a
// config, and reports questions and the config the answers imply.
package setup

import (
	"sort"
	"strconv"
	"strings"

	"github.com/behnam-rk/dezhban/internal/config"
	"github.com/behnam-rk/dezhban/internal/country"
)

// Question kinds. The set is small on purpose: every kind must be renderable
// both as a huh field and as a native control, so adding one is a change in
// two places and should be rare.
const (
	// KindDuration is a Go duration string, validated by ValidDuration.
	KindDuration = "duration"
	// KindText is free text.
	KindText = "text"
	// KindList is comma-separated free text that becomes a list.
	KindList = "list"
	// KindSelect is one value from Options.
	KindSelect = "select"
	// KindMultiSelect is any number of values from Options.
	KindMultiSelect = "multiselect"
	// KindBool is a yes/no.
	KindBool = "bool"
)

// An Option is one choice in a select or multi-select question.
type Option struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// A Question is one decision the wizard asks the user to make.
type Question struct {
	// ID is the stable identifier answers are keyed by. Both wizards and the
	// JSON use it, so it is a compatibility surface: do not rename one.
	ID string `json:"id"`
	// Key is the dotted config key this answer writes, empty when the question
	// only steers the flow (ConfigureVPN, AutoMode) or is folded into another
	// key's value (OtherCountries).
	Key         string `json:"key,omitempty"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Options is present for select and multiselect.
	Options []Option `json:"options,omitempty"`
	// Default is the seeded answer for every kind but multiselect, rendered the
	// way the config renders it.
	Default string `json:"default,omitempty"`
	// Selected is the seeded answer for multiselect.
	Selected []string `json:"selected,omitempty"`
	// Group is the screenful this question belongs to. Questions with the same
	// Group are asked together; the numbers are ordering only.
	Group int `json:"group"`
	// RequiresID and RequiresValue gate this question on an earlier answer —
	// "tunnels" is only asked when "autoMode" answered "false". Empty means
	// always asked.
	RequiresID    string `json:"requiresId,omitempty"`
	RequiresValue string `json:"requiresValue,omitempty"`
}

// Gated reports whether this question depends on an earlier answer.
func (q Question) Gated() bool { return q.RequiresID != "" }

// commonBlockedCodes are the countries offered as checkboxes, in the order they
// are shown. Any other ISO code can be typed into the free-text question that
// follows — so this list is a convenience, never a restriction, and adding a
// code here changes nothing about what the guard can block.
var commonBlockedCodes = []string{"IR", "RU", "CN", "KP", "SY", "CU", "BY"}

// CommonBlocked are those codes with their display labels. The labels come from
// internal/country rather than being written out here, so the wizard, the CLI
// and the window all name a country identically — this list used to be the
// repo's only code→name mapping, and its "Iran (IR)" form is the one every
// other surface now follows.
var CommonBlocked = func() []Option {
	out := make([]Option, 0, len(commonBlockedCodes))
	for _, code := range commonBlockedCodes {
		out = append(out, Option{Label: country.Label(code), Value: code})
	}
	return out
}()

// Options is what the question set is seeded from.
type Options struct {
	// Config is what the answers start at, so re-running the wizard edits
	// rather than clobbers. Nil means the shipped defaults.
	Config *config.Config
	// ConfigExisted distinguishes "the user has a config" from "we fell back to
	// defaults". Only a brand-new macOS config defaults endpoint discovery on:
	// re-running setup must never silently flip an explicit `false` back.
	ConfigExisted bool
	// GOOS is the platform the config is FOR, not necessarily the one asking —
	// live endpoint discovery is macOS-only.
	GOOS string
	// DetectedTunnels turns the tunnel question from free text into a pick
	// list. Supplied by the caller (netdetect in the CLI, `detect-vpn --json`
	// in the app) so this package stays free of platform probes.
	DetectedTunnels []string
}

// Questions returns the wizard's questions in the order they are asked, seeded
// from the given config.
func Questions(opts Options) []Question {
	cfg := opts.Config
	if cfg == nil {
		d := config.Default()
		cfg = &d
	}
	macOS := opts.GOOS == "darwin"

	blocked := map[string]bool{}
	for _, c := range cfg.BlockedCountries {
		blocked[strings.ToUpper(strings.TrimSpace(c))] = true
	}
	var selected []string
	for _, opt := range CommonBlocked {
		if blocked[opt.Value] {
			selected = append(selected, opt.Value)
			delete(blocked, opt.Value)
		}
	}
	// Whatever is configured but not on the common list seeds the free-text
	// question, so a code the wizard does not offer survives a re-run.
	var extra []string
	for code := range blocked {
		extra = append(extra, code)
	}
	// Stable order, so a re-run does not reshuffle a field nobody touched.
	sort.Strings(extra)

	// Endpoint discovery is macOS-only and defaults on ONLY for a brand-new
	// config there, for the reason in Options.ConfigExisted.
	autoDiscover := cfg.VPN.AutoDiscoverEndpoints
	if macOS && !opts.ConfigExisted {
		autoDiscover = true
	}

	endpointDesc := "Server IP(s)/hostname(s), comma-separated. Optional on macOS (auto-discovered); needed elsewhere."
	if !macOS {
		endpointDesc = "Server IP(s)/hostname(s), comma-separated. Required on this platform (no live discovery)."
	}

	qs := []Question{
		{
			ID: "pollInterval", Key: "pollInterval", Kind: KindDuration, Group: 1,
			Title:       "Poll interval",
			Description: "How often the exit country is checked, e.g. 30s.",
			Default:     cfg.PollInterval.String(),
		},
		{
			ID: "blockedCountries", Key: "blockedCountries", Kind: KindMultiSelect, Group: 1,
			Title:       "Blocked countries",
			Description: "Traffic is cut when the VPN's exit lands in one of these.",
			Options:     CommonBlocked,
			Selected:    selected,
		},
		{
			ID: "otherCountries", Kind: KindList, Group: 1,
			Title:       "Other country codes",
			Description: "Comma-separated ISO codes not listed above (optional).",
			Default:     strings.Join(extra, ","),
		},
		{
			ID: "logLevel", Key: "logLevel", Kind: KindSelect, Group: 1,
			Title:   "Log level",
			Options: []Option{{"Debug", "debug"}, {"Info", "info"}, {"Warn", "warn"}, {"Error", "error"}},
			Default: cfg.LogLevel,
		},
		{
			ID: "providerQuorum", Key: "providerQuorum", Kind: KindBool, Group: 1,
			Title:       "Require provider quorum?",
			Description: "Only act when a majority of the geo providers agree.",
			Default:     strconv.FormatBool(cfg.ProviderQuorum),
		},
		{
			ID: "configureVPN", Kind: KindBool, Group: 1,
			Title: "Configure your VPN now?",
			Description: "dezhban only enforces once it knows your VPN's tunnel and server. " +
				"Say no and it starts in standby — fully open, nothing blocked — until you " +
				"run 'dezhban setup' again or edit the config.",
			Default: "true",
		},
		{
			ID: "autoMode", Kind: KindBool, Group: 2,
			Title: "Use automatic VPN detection? (recommended)",
			Description: "dezhban finds your tunnel and, on macOS, learns the server address " +
				"itself — works with any VPN and survives redials.",
			Default:       "true",
			RequiresID:    "configureVPN",
			RequiresValue: "true",
		},
		tunnelQuestion(opts.DetectedTunnels, cfg.VPN.TunnelInterfaces),
		{
			ID: "profileFiles", Kind: KindList, Group: 4,
			Title: "Self-hosted VPN config files",
			Description: "Comma-separated paths to WireGuard/.conf, OpenVPN/.ovpn, or V2Ray " +
				"JSON to import as profiles (optional).",
			RequiresID: "configureVPN", RequiresValue: "true",
		},
		{
			ID: "endpoints", Key: "vpn.endpoints", Kind: KindList, Group: 4,
			Title:       "VPN endpoint(s)",
			Description: endpointDesc,
			Default:     strings.Join(cfg.VPN.Endpoints, ","),
			RequiresID:  "configureVPN", RequiresValue: "true",
		},
		{
			ID: "allowPhysicalDNS", Key: "vpn.allowPhysicalDNS", Kind: KindBool, Group: 4,
			Title: "Allow DNS on the physical link while the tunnel is down?",
			Description: "Lets a VPN client re-resolve its server hostname to redial. Leaks " +
				"only DNS-query metadata; your traffic stays blocked. Recommended if any " +
				"endpoint is a hostname.",
			Default:    strconv.FormatBool(cfg.VPN.AllowPhysicalDNS),
			RequiresID: "configureVPN", RequiresValue: "true",
		},
	}

	// Live endpoint discovery is macOS-only, so elsewhere the question is not
	// asked at all rather than asked and ignored.
	if macOS {
		qs = append(qs, Question{
			ID: "autoDiscover", Key: "vpn.autoDiscoverEndpoints", Kind: KindBool, Group: 5,
			Title: "Auto-discover the VPN server address? (recommended)",
			Description: "dezhban watches the live tunnel socket to learn the server IP, so " +
				"you don't pin one that changes. macOS only.",
			Default:    strconv.FormatBool(autoDiscover),
			RequiresID: "configureVPN", RequiresValue: "true",
		})
	}
	return qs
}

// tunnelQuestion is a pick list when tunnels were detected and free text when
// none were — the same split the CLI's tunnelSelector used to make on its own.
func tunnelQuestion(detected, configured []string) Question {
	q := Question{
		ID: "tunnels", Key: "vpn.tunnelInterfaces", Group: 3,
		Title:      "Tunnel interface(s)",
		RequiresID: "autoMode", RequiresValue: "false",
	}
	if len(detected) == 0 {
		q.Kind = KindList
		q.Description = "None detected. Enter comma-separated names (e.g. utun4)."
		q.Default = strings.Join(configured, ",")
		return q
	}
	cfgSet := map[string]bool{}
	for _, t := range configured {
		cfgSet[t] = true
	}
	q.Kind = KindMultiSelect
	q.Description = "Detected tunnels — pick the VPN's."
	for _, t := range detected {
		q.Options = append(q.Options, Option{Label: t, Value: t})
		if cfgSet[t] {
			q.Selected = append(q.Selected, t)
		}
	}
	return q
}
