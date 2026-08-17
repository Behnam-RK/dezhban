package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A preset is a write-time macro over ordinary keys, never runtime state: the
// daemon never knows a preset was applied, only the resulting values. Applying
// one writes those values through the ordinary config-write path (`dezhban
// config set`/`config preset apply`), so it gets the same validation, the same
// live-reload/restart-required reporting, and the same audit trail as any hand
// edit. Drift detection (PresetDrift/MatchPreset) compares the CURRENT config
// against a preset's values at display time — it is not persisted anywhere.
//
// Presets cover exactly the keys that answer "how strict am I": the three
// relaxation windows, poll cadence and hysteresis (how fast a bad exit is
// caught), the two firewall-pass toggles, and arm-at-boot. They never touch
// identity (blockedCountries, tunnel interfaces, endpoints, profiles) — same
// carve-out as `config reset --all` — which is what keeps vpn.profiles (VPN
// identities) cleanly distinct from presets (strictness strategies).
type Preset struct {
	// Name is the lowercase identifier ("strict", "balanced", "relaxed"), used
	// by `config preset apply/show/diff` and matched case-insensitively.
	Name string
	// Summary is a one-line description of the strategy.
	Summary string
	// Cost states what applying this preset gives up, in plain words — a
	// security tool names the trade rather than a bare "safe"/"strict" label
	// (see docs/concepts/glossary.md's "Words we do not use" table).
	Cost string
	// Values are the dotted keys and the string values `dezhban config set`
	// would accept for each — the same vocabulary a user types by hand.
	Values map[string]string
}

// presetKeys are the exact keys every Preset.Values must set — no more, no
// less. A preset that touched anything else would blur the line
// docs/concepts/glossary.md's "Preset" entry draws between presets (strictness
// strategies) and profiles (VPN identities).
var presetKeys = []string{
	"vpn.switchWindow", "vpn.redialWindow", "vpn.pauseMax",
	"pollInterval", "hysteresis",
	"vpn.allowLocalNetwork", "vpn.allowPhysicalDNS", "vpn.armAtBoot",
}

// Presets returns the four shipped presets, in Strict → Focused → Balanced →
// Relaxed order (increasing relaxation). Balanced is exactly config.Default()
// after Normalize — pinned by TestBalancedPresetMatchesDefault — so that
// preset and the shipped defaults can never silently disagree.
func Presets() []Preset {
	return []Preset{
		{
			Name:    "strict",
			Summary: "No windows at all: every window disabled, exit checks fastest.",
			Cost: "Connecting a new VPN or reconnecting after a drop needs the server's " +
				"address in vpn.endpoints ahead of time — there is no window to redial or " +
				"switch through. Pausing to use your real IP is unavailable. A VPN endpoint " +
				"given as a hostname can't re-resolve its server while the tunnel is down " +
				"(allowPhysicalDNS off). Faster polling means more geo-provider requests.",
			Values: map[string]string{
				"vpn.switchWindow":      "0",
				"vpn.redialWindow":      "0",
				"vpn.pauseMax":          "0",
				"pollInterval":          "10s",
				"hysteresis":            "1",
				"vpn.allowLocalNetwork": "false",
				"vpn.allowPhysicalDNS":  "false",
				"vpn.armAtBoot":         "true",
			},
		},
		{
			Name:    "focused",
			Summary: "Redial-only: the automatic redial window stays, manual switch and pause are disabled, exit checks fastest.",
			Cost: "Connecting a brand-new VPN needs its server in vpn.endpoints ahead of " +
				"time — only the automatic 15s redial window can relax the guard, and only " +
				"for a drop of the VPN you already use. Pausing to use your real IP is " +
				"unavailable. A VPN endpoint given as a hostname can't re-resolve its server " +
				"while the tunnel is down (allowPhysicalDNS off). Faster polling means more " +
				"geo-provider requests. Local devices stay reachable, which also lets them " +
				"reach you on an untrusted network.",
			Values: map[string]string{
				"vpn.switchWindow":      "0",
				"vpn.redialWindow":      "15s",
				"vpn.pauseMax":          "0",
				"pollInterval":          "10s",
				"hysteresis":            "1",
				"vpn.allowLocalNetwork": "true",
				"vpn.allowPhysicalDNS":  "false",
				"vpn.armAtBoot":         "true",
			},
		},
		{
			Name:    "balanced",
			Summary: "The shipped defaults: sanctioned relaxations kept short, arms at boot.",
			Cost: "A brief, bounded exposure window whenever the VPN redials or a new one " +
				"connects. Local devices (printers, NAS, your router) stay reachable, which " +
				"also lets them reach you on an untrusted network.",
			Values: map[string]string{
				"vpn.switchWindow":      "5s",
				"vpn.redialWindow":      "30s",
				"vpn.pauseMax":          "30m",
				"pollInterval":          "15s",
				"hysteresis":            "2",
				"vpn.allowLocalNetwork": "true",
				"vpn.allowPhysicalDNS":  "true",
				"vpn.armAtBoot":         "true",
			},
		},
		{
			Name:    "relaxed",
			Summary: "Longer windows, slower exit checks, doesn't arm automatically at boot.",
			Cost: "Longer exposure whenever a switch/redial/pause window is open. A forbidden " +
				"exit takes longer to catch (hysteresis 3 at a slower 30s poll — up to ~90s " +
				"worst case). A reboot before the VPN reconnects leaves the network open until " +
				"you arm it yourself (armAtBoot off).",
			Values: map[string]string{
				"vpn.switchWindow":      "30s",
				"vpn.redialWindow":      "2m",
				"vpn.pauseMax":          "2h",
				"pollInterval":          "30s",
				"hysteresis":            "3",
				"vpn.allowLocalNetwork": "true",
				"vpn.allowPhysicalDNS":  "true",
				"vpn.armAtBoot":         "false",
			},
		},
	}
}

// PresetByName looks up a preset case-insensitively.
func PresetByName(name string) (Preset, bool) {
	for _, p := range Presets() {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return Preset{}, false
}

// presetSetters applies one preset value onto a Config, using the identical
// semantics `dezhban config set` would for that key — including the "0
// disables" sentinel the three windows share. Kept local to this file rather
// than shared with cmd/dezhban's configFields, so internal/config carries no
// reverse dependency on the CLI package; it is a small, bounded duplication of
// the same three-line idiom already duplicated between config.go's
// applyAdvanced and cmd/dezhban's configFields.
var presetSetters = map[string]func(*Config, string) error{
	"vpn.switchWindow": func(c *Config, v string) error { return setPresetWindow(&c.VPN.SwitchWindow, v) },
	"vpn.redialWindow": func(c *Config, v string) error { return setPresetWindow(&c.VPN.RedialWindow, v) },
	"vpn.pauseMax":     func(c *Config, v string) error { return setPresetWindow(&c.VPN.PauseMax, v) },
	"pollInterval": func(c *Config, v string) error {
		d, err := time.ParseDuration(v)
		if err != nil {
			return err
		}
		c.PollInterval = d
		return nil
	},
	"hysteresis": func(c *Config, v string) error {
		n, err := strconv.Atoi(v)
		if err != nil {
			return err
		}
		c.Hysteresis = n
		return nil
	},
	"vpn.allowLocalNetwork": func(c *Config, v string) error { return setPresetBool(&c.VPN.AllowLocalNetwork, v) },
	"vpn.allowPhysicalDNS":  func(c *Config, v string) error { return setPresetBool(&c.VPN.AllowPhysicalDNS, v) },
	"vpn.armAtBoot":         func(c *Config, v string) error { return setPresetBool(&c.VPN.ArmAtBoot, v) },
}

func setPresetWindow(dst *time.Duration, v string) error {
	d, err := time.ParseDuration(v)
	if err != nil {
		return err
	}
	if d == 0 {
		d = Disabled
	}
	*dst = d
	return nil
}

func setPresetBool(dst *bool, v string) error {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return err
	}
	*dst = b
	return nil
}

// apply writes every presetKeys value from p onto a copy of c, using the same
// semantics `config set` would. Never mutates the caller's Config.
func (p Preset) apply(c Config) (Config, error) {
	for _, key := range presetKeys {
		v, ok := p.Values[key]
		if !ok {
			return c, fmt.Errorf("preset %s: missing value for %s", p.Name, key)
		}
		if err := presetSetters[key](&c, v); err != nil {
			return c, fmt.Errorf("preset %s: %s: %w", p.Name, key, err)
		}
	}
	return c, nil
}

// PresetDrift reports which of the preset's keys differ from c's current
// values, expressed as ordinary config.Change values — the same vocabulary
// `config set`'s reload report already uses, so a diff reads identically to
// any other config change. Empty means c already matches the preset exactly.
func PresetDrift(c *Config, p Preset) []Change {
	target, err := p.apply(*c)
	if err != nil {
		// Every shipped preset's Values are covered by TestPresetsAreWellFormed;
		// reaching this means a preset was hand-edited into an inconsistent
		// state, which is a bug, not a runtime condition to report gracefully.
		panic(err)
	}
	return Changes(c, &target)
}

// PresetConflicts reports why p cannot be applied to c as things stand: one
// entry per preset window that exceeds a cap the user has lowered in
// vpn.advanced, naming both sides and what to do about it. Empty means p is
// appliable.
//
// A preset never resolves this by widening the cap, which is why the two caps
// are deliberately absent from presetKeys. vpn.advanced.switchWindowMax and
// redialWindowMax are the operator's own hard ceiling on two of the three
// sanctioned relaxations; a strictness macro that silently raised one would
// relax the guard past a limit its owner set by hand — exactly what the caps
// exist to prevent. So a conflicting preset is refused by name, up front,
// instead of failing at Validate with an error that names only the rule.
//
// Only the two capped windows can conflict: vpn.pauseMax IS the pause cap (it
// has none above it), and the remaining five preset keys have no ceiling.
func PresetConflicts(c *Config, p Preset) []string {
	target, err := p.apply(*c)
	if err != nil {
		// Malformed preset — same reasoning as PresetDrift's panic.
		panic(err)
	}
	var out []string
	check := func(key string, want, max time.Duration, capKey string, fallback time.Duration) {
		if max <= 0 {
			max = fallback
		}
		if want > 0 && want > max {
			out = append(out, fmt.Sprintf(
				"%s sets %s %s, above your %s %s — raise the cap (dezhban config set %s=%s) or pick another preset",
				p.Name, key, want, capKey, max, capKey, want))
		}
	}
	adv := c.VPN.Advanced
	check("vpn.switchWindow", target.VPN.SwitchWindow, adv.SwitchWindowMax,
		"vpn.advanced.switchWindowMax", defaultSwitchWindowMax)
	check("vpn.redialWindow", target.VPN.RedialWindow, adv.RedialWindowMax,
		"vpn.advanced.redialWindowMax", defaultRedialWindowMax)
	return out
}

// MatchPreset reports which preset c currently matches exactly (checked in
// Presets() order — Strict, Focused, Balanced, Relaxed), or ("", false) for a
// config that has drifted from all four ("Custom").
func MatchPreset(c *Config) (name string, exact bool) {
	for _, p := range Presets() {
		if len(PresetDrift(c, p)) == 0 {
			return p.Name, true
		}
	}
	return "", false
}

// Pause durations, defined once so the CLI and the app offer the same choices.
//
// A pause is the one relaxation a user asks for on purpose, so the question is
// never "how many seconds" but "how long do I need my real IP for". These are
// the realistic answers to that; anything else is still typeable
// (`dezhban pause 7m`), this is just what gets offered.
var pauseOptions = []PauseOption{
	{Label: "5 minutes", Duration: 5 * time.Minute, Why: "long enough to load one stubborn site"},
	{Label: "15 minutes", Duration: 15 * time.Minute, Why: "a bank transfer or a delivery tracker"},
	{Label: "30 minutes", Duration: 30 * time.Minute, Why: "a form you have to fill in properly"},
	{Label: "1 hour", Duration: time.Hour, Why: "a long call with a domestic-only service"},
	{Label: "2 hours", Duration: 2 * time.Hour, Why: "an evening of something the VPN's exit can't reach"},
}

// A PauseOption is one offered pause length.
type PauseOption struct {
	Label    string        `json:"label"`
	Duration time.Duration `json:"-"`
	// Value is the Duration as `dezhban pause` accepts it, so a surface can
	// pass it straight through without formatting a duration itself.
	Value string `json:"value"`
	// Why says what this length is for, so the choice is made on the need
	// rather than on the number.
	Why string `json:"why"`
	// Unavailable is non-empty when vpn.pauseMax forbids this length, and says
	// so in words. The option is still LISTED: silently hiding or shortening a
	// choice teaches a user their cap is something other than it is. See the
	// "clamp nothing silently" decision in issue #33's second addendum.
	Unavailable string `json:"unavailable,omitempty"`
}

// PauseOptions returns the offered pause lengths for a config, each marked
// available or not against that config's vpn.pauseMax.
//
// Options above the cap are returned, not filtered. A picker must show them as
// unavailable with the reason, because the alternative — quietly dropping them,
// or worse, accepting one and shortening it — leaves the user with a wrong idea
// of what their own setting does.
func PauseOptions(c *Config) []PauseOption {
	max := c.VPN.PauseMax
	out := make([]PauseOption, 0, len(pauseOptions))
	for _, o := range pauseOptions {
		o.Value = durString(o.Duration)
		switch {
		case max <= 0:
			o.Unavailable = "pausing is off (vpn.pauseMax is \"0\")"
		case o.Duration > max:
			o.Unavailable = fmt.Sprintf("longer than your %s cap (vpn.pauseMax)", durString(max))
		}
		out = append(out, o)
	}
	return out
}

// PauseRefusal reports why a requested pause duration cannot be granted, or ""
// when it can.
//
// This is where "clamp nothing silently" is enforced. The runner used to shorten
// an over-cap request to the cap, which is the same class of bug as accepting a
// disabled window and restoring the default: the user asked for one thing, got
// another, and was not told.
//
// runner.pauseDuration says the same things to a client that got past this one
// (a raw socket call, or a stale command file). Kept as two sentences rather
// than one helper because internal/runner takes an Options and never a
// *Config — change one, change the other.
func PauseRefusal(c *Config, requested time.Duration) string {
	switch {
	case c.VPN.PauseMax <= 0:
		return "pausing is disabled (vpn.pauseMax is \"0\"). Set it to a duration to enable it."
	case requested <= 0:
		return ""
	case requested > c.VPN.PauseMax:
		return fmt.Sprintf("%s is longer than your %s cap (vpn.pauseMax). "+
			"Ask for %s or less, or raise the cap with `dezhban config set vpn.pauseMax=%s`.",
			durString(requested), durString(c.VPN.PauseMax), durString(c.VPN.PauseMax), durString(requested))
	}
	return ""
}
