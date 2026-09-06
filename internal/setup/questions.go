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
	// only steers the flow (AutoMode) or is folded into another key's value
	// (OtherCountries).
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

	endpointDesc := "Server IP(s)/hostname(s), comma-separated. Optional on macOS (auto-discovered); needed elsewhere."
	if !macOS {
		endpointDesc = "Server IP(s)/hostname(s), comma-separated. Required on this platform (no live discovery)."
	}

	// Automatic detection is the recommendation, but never at the cost of
	// silently unpinning interfaces someone chose on purpose: a config with
	// pinned vpn.tunnelInterfaces seeds this to false, so clicking straight
	// through a re-run preserves them. This is load-bearing now that there is
	// no "configure your VPN?" question to skip the whole branch —
	// TestAutoModeSeedsFalseWhenInterfacesArePinned pins it.
	autoModeDefault := "true"
	if len(cfg.VPN.TunnelInterfaces) > 0 {
		autoModeDefault = "false"
	}

	// Endpoints are gated behind "not automatic" on macOS, where live discovery
	// learns the server address. Everywhere else there is no discovery, so the
	// endpoint is required whichever detection mode is chosen and the question
	// is ungated — a Linux host that picked automatic detection and was never
	// asked for a server would end up with a config that cannot enforce.
	endpointsRequires, endpointsRequiresValue := "autoMode", "false"
	if !macOS {
		endpointsRequires, endpointsRequiresValue = "", ""
	}

	// The wizard asks only what has no safe default: what to block, and how to
	// find the VPN. Everything it used to also ask (poll interval, log level,
	// provider quorum, physical DNS, auto-discovery) ships with a sane default,
	// lives in Settings/`config set`, and — critically — is left UNTOUCHED by a
	// wizard run, so re-running setup never clobbers a tuned value
	// (TestAnUnaskedQuestionLeavesItsKeyAlone pins this).
	//
	// Two groups, which is two steps: what to block, then how to find the VPN.
	// Everything in group 2 hangs off the one automatic-detection question, so
	// unticking it reveals the manual fields in place rather than paging to
	// another screen.
	return []Question{
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
			ID: "autoMode", Kind: KindBool, Group: 2,
			Title: "Use automatic VPN detection? (recommended)",
			Description: "dezhban finds your tunnel and, on macOS, learns the server address " +
				"itself — works with any VPN and survives redials. Untick it to name your " +
				"tunnel and server yourself.",
			Default: autoModeDefault,
		},
		tunnelQuestion(opts.DetectedTunnels, cfg.VPN.TunnelInterfaces),
		{
			ID: "profileFiles", Kind: KindList, Group: 2,
			Title: "Self-hosted VPN config files",
			Description: "Comma-separated paths to WireGuard/.conf, OpenVPN/.ovpn, or V2Ray " +
				"JSON to import as profiles (optional).",
			RequiresID: "autoMode", RequiresValue: "false",
		},
		{
			ID: "endpoints", Key: "vpn.endpoints", Kind: KindList, Group: 2,
			Title:         "VPN endpoint(s)",
			Description:   endpointDesc,
			Default:       strings.Join(cfg.VPN.Endpoints, ","),
			RequiresID:    endpointsRequires,
			RequiresValue: endpointsRequiresValue,
		},
	}
}

// tunnelQuestion is a pick list when tunnels were detected and free text when
// none were — the same split the CLI's tunnelSelector used to make on its own.
func tunnelQuestion(detected, configured []string) Question {
	q := Question{
		ID: "tunnels", Key: "vpn.tunnelInterfaces", Group: 2,
		Title:      "Tunnel interface(s)",
		RequiresID: "autoMode", RequiresValue: "false",
	}
	if len(detected) == 0 {
		q.Kind = KindList
		q.Description = "None detected. Enter comma-separated names (e.g. utun4)."
		q.Default = strings.Join(configured, ",")
		return q
	}
	q.Kind = KindMultiSelect

	// A pinned interface is an option even when it is not detected right now.
	// Detection only sees tunnels that are UP, so a re-run while the VPN is
	// down would otherwise offer a list its own configured interface is not in
	// — and pressing Enter through that list answers "none of them", which
	// Apply writes as an empty vpn.tunnelInterfaces. That silently unpins an
	// interface someone chose deliberately, which is the very thing seeding
	// autoMode to false from those pins exists to prevent; offering the pins
	// but leaving them unselectable would close only half of it.
	// TestAPinnedTunnelSurvivesADetectionMiss pins this.
	//
	// Those extras are labelled, because a list headed "detected" containing a
	// ticked interface that is plainly not up reads as a bug in the detector.
	// The label carries it; the Value stays the bare interface name, which is
	// what gets written.
	detectedSet := map[string]bool{}
	for _, t := range detected {
		detectedSet[t] = true
	}
	seen := map[string]bool{}
	offDuty := false
	for _, t := range append(append([]string(nil), detected...), configured...) {
		if seen[t] {
			continue
		}
		seen[t] = true
		label := t
		if !detectedSet[t] {
			label = t + " (configured, not up right now)"
			offDuty = true
		}
		q.Options = append(q.Options, Option{Label: label, Value: t})
	}
	q.Description = "Detected tunnels — pick the VPN's."
	if offDuty {
		q.Description = "Pick the VPN's. Interfaces you already configured are listed " +
			"and kept ticked even while they are down."
	}
	cfgSet := map[string]bool{}
	for _, t := range configured {
		cfgSet[t] = true
	}
	for _, o := range q.Options {
		if cfgSet[o.Value] {
			q.Selected = append(q.Selected, o.Value)
		}
	}
	return q
}
