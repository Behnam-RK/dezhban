package config

import (
	"sort"
	"strings"
)

// This file exists because a default stated in more than one place is a default
// that will drift — and this one already had. Before it, every tunable's default
// was written down four times: the constants below, the macOS app's placeholder
// hints, the tables in docs/usage/config.md, and the example configs. The app was
// advertising a 30s endpoint refresh and a 5s tunnel watch while the shipped
// defaults were 1m and 1s, so anyone reading the settings pane was being told the
// wrong thing.
//
// The fix is to make defaults data. Every surface — the GUI's hints and slider
// bounds, `dezhban config schema`, the CLI's help — reads the table below instead
// of restating it, and a test fails the build when the docs or the example configs
// disagree with it.
//
// Crucially, a Tunable does NOT carry a hand-written default value. Defaults are
// derived from a normalized Default() config (see Defaults), so the only place a
// default number is written down stays the const block in config.go. A metadata
// table that restated them would just be a fifth copy.

// Kind classifies a tunable's value shape, so a surface can pick a control for it
// without knowing the key. It describes the *shape*, not the meaning: two
// durations may mean very different things but both get a duration control.
type Kind string

const (
	KindDuration Kind = "duration"
	KindBool     Kind = "bool"
	KindInt      Kind = "int"
	KindList     Kind = "list"   // comma-separated, order-significant
	KindString   Kind = "string" // free text with no list semantics
)

// A Tunable describes one settable config key well enough that a surface can
// render a control for it, bound it, explain it, and link to its documentation
// without hardcoding anything of its own.
type Tunable struct {
	// Key is the dotted name `dezhban config set` accepts. It is the identity:
	// every other table in the codebase is keyed by the same string.
	Key string `json:"key"`

	// Label is the human name for the concept, in the vocabulary of
	// docs/concepts/glossary.md. It never contains the config key — "Serialised
	// forms are not a UI", and neither are JSON keys in a label.
	Label string `json:"label"`

	Kind Kind `json:"kind"`

	// Default is the value this key takes when the config does not set it,
	// rendered exactly as KeyValues renders it. Filled by Tunables from a
	// normalized Default() — never written by hand.
	Default string `json:"default"`

	// CapKey names the tunable that bounds this one, or "" when nothing does.
	// It is a key rather than a number because the ceiling is itself settable:
	// a slider's top must be read from the live config, not from a constant.
	//
	// Deliberately NOT transitive and deliberately not shared — the three windows
	// have their own caps precisely so that widening one cannot widen another
	// (see CLAUDE.md's window-trigger invariant).
	CapKey string `json:"capKey,omitempty"`

	// Unit names what an int counts, for surfaces that want to write "16
	// endpoints" rather than "16". Empty for every other kind: a duration's unit
	// is carried by its own Go duration syntax, and bools and lists count nothing.
	Unit string `json:"unit,omitempty"`

	// Disablable reports whether "0" is an explicit, persisted opt-out for this
	// key rather than "reset me to the default". Only the keys whose setters
	// install the negative Disabled sentinel may set this — for every other
	// duration, Normalize coerces a plain 0 back to the default, so offering an
	// "Off" choice would silently discard the user's decision.
	Disablable bool `json:"disablable"`

	// Advanced marks the power-user tier: every `vpn.advanced.*` knob, plus the
	// handful of top-level keys the setup wizard stopped asking about
	// (hysteresis, providerQuorum, logLevel). Surfaces put these behind a
	// disclosure with the "touch only if you know why" warning — the app's
	// Developer section is generated from exactly this flag, so a key is
	// reachable there by being marked here, never by a hand-kept Swift list.
	Advanced bool `json:"advanced"`

	// Help is one line explaining what the key does and what it costs.
	Help string `json:"help"`

	// DocAnchor points at the *section* of the documentation that covers this
	// key, as "<page>#<anchor>". A heading anchor, so it resolves everywhere
	// markdown does — on GitHub, in an editor's preview, and in the app's bundled
	// help. This is what a CLI surface prints, because it is the only one a reader
	// outside the app can follow.
	DocAnchor string `json:"docAnchor"`

	// DocKeyAnchor points at the key's own table row, as "<page>#<anchor>", or is
	// empty for a key documented in prose rather than a row.
	//
	// Additional to DocAnchor, never a replacement for it. Row ids exist only in
	// the HTML tools/helpgen renders; markdown viewers generate heading anchors
	// only. Overwriting DocAnchor with this therefore handed every CLI user a
	// fragment that resolves nowhere, to fix a granularity problem only the app
	// had — and it removed the app's own middle step on version skew, where a CLI
	// newer than the bundled help finds no row id and would otherwise fall all the
	// way back to the top of a forty-key reference instead of to the section.
	DocKeyAnchor string `json:"docKeyAnchor,omitempty"`

	// RestartReason is why a running daemon cannot adopt this key in place, or ""
	// when it can. Derived from restartReasonFor — never restated here, so a key
	// cannot claim to be live in one table and restart-required in another.
	RestartReason string `json:"restartReason,omitempty"`
}

// LiveAppliable reports whether a running daemon adopts this key in place.
func (t Tunable) LiveAppliable() bool { return t.RestartReason == "" }

// Doc anchors — the *section* of docs/usage/config.md that covers a group of keys,
// and every key's DocAnchor unconditionally. These are heading anchors, so they are
// what `dezhban config schema` prints for a reader who will open the file on GitHub,
// and the app's second choice after the key's own row (docKeyAnchorFor derives that
// separately, and it is additional rather than a replacement).
//
// Not a "fallback for keys documented in prose", which is what this said: that was
// true only of an intermediate design where the row anchor overwrote this one.
// keysDocumentedInProse is empty today, and even if it were not, these would still
// be every key's section.
const (
	anchorFields   = "usage/config.md#fields"
	anchorControl  = "usage/config.md#control-block"
	anchorVPN      = "usage/config.md#vpn-block"
	anchorAdvanced = "usage/config.md#advanced-tunables-vpnadvanced"
)

// tunables is the declared table, in the order surfaces should present it:
// what the guard blocks, then which tunnel it trusts, then how it behaves when
// that tunnel comes and goes, then the control socket, then the advanced knobs.
//
// Default is left empty here on purpose — Tunables fills it. See the file comment.
var tunables = []Tunable{
	{
		Key:       "blockedCountries",
		Label:     "Blocked countries",
		Kind:      KindList,
		Help:      "Exit countries the guard refuses. If your VPN surfaces in one of these, everything is cut until it moves.",
		DocAnchor: anchorFields,
	},
	{
		Key:       "pollInterval",
		Label:     "Exit country check interval",
		Kind:      KindDuration,
		Help:      "How often the current VPN exit's country is checked.",
		DocAnchor: anchorFields,
	},
	{
		Key:       "hysteresis",
		Label:     "Agreeing readings before a flip",
		Kind:      KindInt,
		Unit:      "readings",
		Advanced:  true,
		Help:      "How many consecutive agreeing readings it takes to change posture. Higher is slower to react but harder to fool with one bad lookup.",
		DocAnchor: anchorFields,
	},
	{
		Key:       "providers",
		Label:     "Exit country lookup services",
		Kind:      KindList,
		Help:      "Tried in order, so the first reachable one absorbs nearly all the traffic.",
		DocAnchor: anchorFields,
	},
	{
		Key:       "providerQuorum",
		Label:     "Require two services to agree",
		Kind:      KindBool,
		Advanced:  true,
		Help:      "Confirms each reading against a second service before it counts. Safer against one wrong answer; twice the lookups.",
		DocAnchor: anchorFields,
	},
	{
		Key:       "logLevel",
		Label:     "Log level",
		Kind:      KindString,
		Advanced:  true,
		Help:      "How much dezhban writes to its log: debug, info, warn, or error.",
		DocAnchor: anchorFields,
	},

	{
		Key:       "vpn.tunnelInterfaces",
		Label:     "Your VPN tunnel",
		Kind:      KindList,
		Help:      "Tunnels named here are pinned: the guard trusts them and never prunes them automatically. Leave empty to rely on autodetection.",
		DocAnchor: anchorVPN,
	},
	{
		Key:       "vpn.endpoints",
		Label:     "VPN server addresses",
		Kind:      KindList,
		Help:      "The addresses your VPN client dials. The guard keeps these reachable on the physical link so a dropped tunnel can redial without opening a window.",
		DocAnchor: anchorVPN,
	},
	{
		Key:       "vpn.autoDetect",
		Label:     "Find my VPN tunnel automatically",
		Kind:      KindBool,
		Help:      "Watches for tunnel interfaces appearing and disappearing instead of relying only on the pinned list.",
		DocAnchor: anchorVPN,
	},
	{
		Key:       "vpn.autoDiscoverEndpoints",
		Label:     "Find the VPN server address automatically",
		Kind:      KindBool,
		Help:      "Learns the address your VPN client is actually talking to, so a redial works without you typing it in.",
		DocAnchor: anchorVPN,
	},
	{
		Key:       "vpn.autoArm",
		Label:     "Arm the guard when a VPN connects",
		Kind:      KindBool,
		Help:      "With no VPN connected dezhban idles in standby and arms the moment a tunnel appears. It never disarms on a drop — that is the kill switch.",
		DocAnchor: anchorVPN,
	},
	{
		Key:       "vpn.armAtBoot",
		Label:     "Arm the guard at boot",
		Kind:      KindBool,
		Help:      "On a host where a tunnel has been up at least once, a reboot stays cut until the VPN redials rather than opening for however long that takes.",
		DocAnchor: anchorVPN,
	},
	{
		Key:       "vpn.allowLocalNetwork",
		Label:     "Keep local devices reachable",
		Kind:      KindBool,
		Help:      "Printers, NAS, your router's admin page, AirPlay and local dev servers keep working. Local destinations only, so nothing on the internet is opened — but on untrusted Wi-Fi it also lets that network reach you.",
		DocAnchor: anchorVPN,
	},
	{
		Key:       "vpn.allowPhysicalDNS",
		Label:     "Keep DNS working while the tunnel is down",
		Kind:      KindBool,
		Help:      "Lets name lookups use your ISP's resolver while the tunnel is down, including during a full block. Convenient, but your ISP sees what you look up.",
		DocAnchor: anchorVPN,
	},
	{
		Key:       "vpn.allowGeoProviders",
		Label:     "Keep exit checks running when blocked",
		Kind:      KindBool,
		Help:      "During a full block, exit-country checks stay reachable through the tunnel only, so the block lifts itself when your exit is allowed again. Off does not stop those checks — recovery instead briefly lifts the guard to look, which exposes more, not less.",
		DocAnchor: anchorVPN,
	},
	{
		Key:        "vpn.switchWindow",
		Label:      "Switch window",
		Kind:       KindDuration,
		CapKey:     "vpn.advanced.switchWindowMax",
		Disablable: true,
		Help:       "How long the guard relaxes when you ask to switch VPN by hand. Off means nothing you type can relax it.",
		DocAnchor:  anchorVPN,
	},
	{
		Key:        "vpn.redialWindow",
		Label:      "Redial window",
		Kind:       KindDuration,
		CapKey:     "vpn.advanced.redialWindowMax",
		Disablable: true,
		Help:       "How long the guard relaxes by itself when a healthy tunnel drops, so your VPN can redial. Your real IP may be exposed for that long. Off is the strict, zero-leak choice.",
		DocAnchor:  anchorVPN,
	},
	{
		Key:        "vpn.pauseMax",
		Label:      "Longest pause",
		Kind:       KindDuration,
		Disablable: true,
		Help:       "The most you can pause the guard for in one go, when you deliberately want your real IP. Off removes pausing entirely.",
		DocAnchor:  anchorVPN,
	},
	{
		Key:       "vpn.endpointRefresh",
		Label:     "VPN server address refresh",
		Kind:      KindDuration,
		Help:      "How often a VPN server hostname is re-resolved, so a rotated address is noticed.",
		DocAnchor: anchorVPN,
	},
	{
		Key:       "vpn.endpointGrace",
		Label:     "VPN server address grace",
		Kind:      KindDuration,
		Help:      "How long a discovered VPN server stays reachable after its connection disappears, so a dropped VPN can redial the same server.",
		DocAnchor: anchorVPN,
	},
	{
		Key:       "vpn.tunnelWatch",
		Label:     "Tunnel check interval",
		Kind:      KindDuration,
		Help:      "How often the tunnel is checked for coming up or going down. This is how fast a drop is noticed.",
		DocAnchor: anchorVPN,
	},

	{
		Key:       "control.enabled",
		Label:     "Control socket",
		Kind:      KindBool,
		Help:      "Lets an authorised local client ask the running dezhban to act, instead of every command needing root.",
		DocAnchor: anchorControl,
	},
	{
		Key:       "control.socket",
		Label:     "Control socket path",
		Kind:      KindString,
		Help:      "Where the control socket is bound. Empty means dezhban picks the path under its own state directory.",
		DocAnchor: anchorControl,
	},
	{
		Key:       "control.group",
		Label:     "Control socket group",
		Kind:      KindString,
		Help:      "The group allowed to talk to the socket. Membership in it is the authorisation.",
		DocAnchor: anchorControl,
	},
	{
		Key:       "control.allowSwitchOps",
		Label:     "Allow switch windows over the socket",
		Kind:      KindBool,
		Help:      "Lets opening and cancelling a switch window go through the control socket. Off makes those root-only again.",
		DocAnchor: anchorControl,
	},
	{
		Key:       "control.allowPauseOps",
		Label:     "Allow pause over the socket",
		Kind:      KindBool,
		Help:      "Lets pause and resume go through the control socket. Independent of switch windows: turning one off leaves the other alone.",
		DocAnchor: anchorControl,
	},
	{
		Key:       "control.allowConfigOps",
		Label:     "Allow settings changes over the socket",
		Kind:      KindBool,
		Help:      "Lets an enrolled, token-holding client change settings without root. The token gate still applies.",
		DocAnchor: anchorControl,
	},

	{
		Key:       "vpn.advanced.switchWindowMax",
		Label:     "Switch window cap",
		Kind:      KindDuration,
		Advanced:  true,
		Help:      "The ceiling on a manual switch window. Its own cap on purpose — raising it must never widen the redial window or a pause.",
		DocAnchor: anchorAdvanced,
	},
	{
		Key:       "vpn.advanced.redialWindowMax",
		Label:     "Redial window cap",
		Kind:      KindDuration,
		Advanced:  true,
		Help:      "The ceiling on an automatic redial window, and so the most exposure one dropped tunnel can cause.",
		DocAnchor: anchorAdvanced,
	},
	{
		Key:        "vpn.advanced.redialMinUptime",
		Label:      "Redial backoff threshold",
		Kind:       KindDuration,
		Advanced:   true,
		Disablable: true,
		Help:       "A tunnel that was up for less than this still gets a window, but a shorter one for each consecutive fast drop, with a growing wait between them. Off gives every drop a full window until the budget runs out.",
		DocAnchor:  anchorAdvanced,
	},
	{
		Key:        "vpn.advanced.verifyInterval",
		Label:      "Enforcement verification interval",
		Kind:       KindDuration,
		Advanced:   true,
		Disablable: true,
		Help:       "How often dezhban confirms its firewall rules are still installed, re-applying them if something removed them from outside. Off trusts the rules to stay put once applied.",
		DocAnchor:  anchorAdvanced,
	},
	{
		Key:       "vpn.advanced.livenessRedial",
		Label:     "Redial on a hung tunnel",
		Kind:      KindBool,
		Advanced:  true,
		Help:      "Lets a tunnel that reports up but has stopped passing traffic open an automatic redial window, the same as an ordinary drop. Off by default: an exit that censors the geo lookup looks identical to a hung tunnel, and this would let it trigger a window on a tunnel that was never actually down.",
		DocAnchor: anchorAdvanced,
	},
	// Not Disablable, unlike almost every other duration here. These two are
	// limits, so an Off switch would have to mean "no limit" — the opposite of
	// what Off means on every other row, and the wrong direction to offer on a
	// security surface. Raise the budget to relax the bound; use
	// `vpn.redialWindow: "0"` to turn the automatic window off outright.
	{
		Key:       "vpn.advanced.redialBudget",
		Label:     "Redial budget",
		Kind:      KindDuration,
		Advanced:  true,
		Help:      "Total time automatic redial windows may leave the guard relaxed per budget period. A window that closes early only spends what it used, so successful redials cost almost nothing. When it runs out the guard simply holds.",
		DocAnchor: anchorAdvanced,
	},
	{
		Key:       "vpn.advanced.redialBudgetWindow",
		Label:     "Redial budget period",
		Kind:      KindDuration,
		Advanced:  true,
		Help:      "The rolling period the redial budget is measured over. Each window's cost is returned once it falls out of this period.",
		DocAnchor: anchorAdvanced,
	},
	{
		Key:       "vpn.advanced.commandFreshness",
		Label:     "Command freshness",
		Kind:      KindDuration,
		Advanced:  true,
		Help:      "How recent a control command must be to be acted on — the replay guard.",
		DocAnchor: anchorAdvanced,
	},
	{
		Key:       "vpn.advanced.windowDiscoveryInterval",
		Label:     "Window discovery interval",
		Kind:      KindDuration,
		Advanced:  true,
		Help:      "How often a new VPN server is looked for while a window is open.",
		DocAnchor: anchorAdvanced,
	},
	{
		Key:       "vpn.advanced.tunnelPruneAfter",
		Label:     "Tunnel prune delay",
		Kind:      KindDuration,
		Advanced:  true,
		Help:      "How long a detected tunnel must be gone before it is dropped from the trusted set. Pinned tunnels are never pruned.",
		DocAnchor: anchorAdvanced,
	},
	{
		Key:       "vpn.advanced.learnedEndpointTTL",
		Label:     "Learned address lifetime",
		Kind:      KindDuration,
		Advanced:  true,
		Help:      "How long an unused learned VPN server address is kept.",
		DocAnchor: anchorAdvanced,
	},
	{
		Key:       "vpn.advanced.learnedMaxPerProfile",
		Label:     "Learned addresses per profile",
		Kind:      KindInt,
		Unit:      "addresses",
		Advanced:  true,
		Help:      "How many learned VPN server addresses are kept for each profile before the oldest is forgotten.",
		DocAnchor: anchorAdvanced,
	},
	{
		Key:       "vpn.advanced.promoteAfterRefreshes",
		Label:     "Sightings before an address is learned",
		Kind:      KindInt,
		Unit:      "sightings",
		Advanced:  true,
		Help:      "Consecutive sightings before a discovered VPN server address is learned under a normal guard.",
		DocAnchor: anchorAdvanced,
	},
	{
		Key:       "vpn.advanced.endpointWarnThreshold",
		Label:     "Address-bloat warning threshold",
		Kind:      KindInt,
		Unit:      "addresses",
		Advanced:  true,
		Help:      "How many resolved VPN server addresses it takes before dezhban warns that the set has grown unreasonably.",
		DocAnchor: anchorAdvanced,
	},
	{
		Key:       "vpn.advanced.windowProtocols",
		Label:     "Window protocols",
		Kind:      KindList,
		Advanced:  true,
		Help:      "Restricts an open window to these protocols instead of allowing all outbound traffic.",
		DocAnchor: anchorAdvanced,
	},
	{
		Key:       "vpn.advanced.windowPorts",
		Label:     "Window ports",
		Kind:      KindList,
		Advanced:  true,
		Help:      "Restricts an open window to these ports instead of allowing all outbound traffic.",
		DocAnchor: anchorAdvanced,
	},
}

// Defaults renders every key's default exactly as KeyValues renders a live value,
// so the two are directly comparable. It is derived rather than declared: a
// normalized Default() IS the definition of "what you get if you set nothing",
// and asking it beats maintaining a second copy that can disagree with it.
func Defaults() map[string]string {
	c := Default()
	Normalize(&c)
	return KeyValues(&c)
}

// Tunables returns the declared table with Default and RestartReason filled in
// from the code that actually decides them. Callers get a fresh slice, so a
// surface cannot mutate the table for everyone else.
func Tunables() []Tunable {
	defaults := Defaults()
	out := make([]Tunable, len(tunables))
	for i, t := range tunables {
		t.Default = defaults[t.Key]
		t.RestartReason = restartReasonFor(t.Key)
		t.DocKeyAnchor = docKeyAnchorFor(t.Key, t.DocAnchor)
		out[i] = t
	}
	return out
}

// docKeyAnchorFor derives the anchor of a key's own row in the reference, to sit
// alongside its declared section anchor rather than in place of it.
//
// The reference documents each key as a table row opening with the key in a code
// span, and the help renderer gives every such row an id (help.KeyAnchor). So a
// contextual help link can land on the key the reader clicked rather than on a
// section heading that four dozen keys share, which is what the four constants
// above delivered on their own.
//
// Alongside, because a row id is not a markdown anchor: it exists only in the HTML
// tools/helpgen renders. Replacing the section anchor with it therefore broke every
// reader outside the app — `config schema` prints the anchor for someone who will
// open the file on GitHub — and cost the app its middle step when a CLI is newer
// than the bundled help.
//
// Derived rather than hand-written, for the same reason defaults are: forty-odd
// anchors restated by hand is forty-odd chances to drift. keysDocumentedInProse
// names the exceptions, and TestEveryTunableDocAnchorResolves fails the build
// naming any key whose derived anchor does not exist — so a key that loses its
// row cannot silently fall back to landing somewhere plausible and wrong.
func docKeyAnchorFor(key, declared string) string {
	if keysDocumentedInProse[key] {
		return ""
	}
	page, _, ok := strings.Cut(declared, "#")
	if !ok || page == "" {
		return ""
	}
	return page + "#key-" + anchorSlug(key)
}

// keysDocumentedInProse are the keys docs/usage/config.md covers outside a table
// row, which therefore have no row anchor to land on. They get an empty
// DocKeyAnchor and are reached through their section anchor alone. Adding a row
// for one of these is an improvement — delete it from here when you do.
var keysDocumentedInProse = map[string]bool{}

// anchorSlug mirrors help.Anchor's rule (GitHub's), applied to a config key.
// Duplicated deliberately rather than imported: internal/help renders the docs
// and would import this package to check its work, so depending on it here would
// close a cycle. TestKeyAnchorSlugMatchesTheRenderer pins the two together.
func anchorSlug(key string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(key) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

// TunableByKey looks one key up. The bool is false for a key that is not
// settable, which callers should treat as "unknown key", not "no metadata".
func TunableByKey(key string) (Tunable, bool) {
	for _, t := range Tunables() {
		if t.Key == key {
			return t, true
		}
	}
	return Tunable{}, false
}

// TunableKeys lists every settable key, sorted. Handy for tests and for CLI help
// that wants a stable order rather than the presentation order.
func TunableKeys() []string {
	out := make([]string, 0, len(tunables))
	for _, t := range tunables {
		out = append(out, t.Key)
	}
	sort.Strings(out)
	return out
}
