package setup

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/behnam-rk/dezhban/internal/config"
)

// Answers holds one binding per question, seeded with that question's default.
//
// The bindings are pointers because that is what huh writes through: the CLI
// hands `TextPtr`/`BoolPtr`/`ListPtr` straight to a form field, the form fills
// them in, and Input reads the result. A native wizard sets them with Set
// instead. Both then produce the same Input, which is the point.
type Answers struct {
	text map[string]*string
	flag map[string]*bool
	list map[string]*[]string
	// asked remembers the question set, so gating can be evaluated against the
	// answers actually collected rather than re-derived by each caller.
	asked []Question
}

// NewAnswers allocates a binding per question, seeded from its default.
func NewAnswers(qs []Question) *Answers {
	a := &Answers{
		text: map[string]*string{}, flag: map[string]*bool{}, list: map[string]*[]string{},
		asked: qs,
	}
	for _, q := range qs {
		switch q.Kind {
		case KindBool:
			v := q.Default == "true"
			a.flag[q.ID] = &v
		case KindMultiSelect:
			v := append([]string(nil), q.Selected...)
			a.list[q.ID] = &v
		default:
			v := q.Default
			a.text[q.ID] = &v
		}
	}
	return a
}

// TextPtr, BoolPtr, and ListPtr are the bindings a form writes through. An
// unknown ID gets a throwaway binding rather than a nil dereference: a wizard
// asking for a question that does not exist is a bug, but not one worth
// crashing a user's setup over.
func (a *Answers) TextPtr(id string) *string {
	if p, ok := a.text[id]; ok {
		return p
	}
	var v string
	a.text[id] = &v
	return &v
}

func (a *Answers) BoolPtr(id string) *bool {
	if p, ok := a.flag[id]; ok {
		return p
	}
	var v bool
	a.flag[id] = &v
	return &v
}

func (a *Answers) ListPtr(id string) *[]string {
	if p, ok := a.list[id]; ok {
		return p
	}
	var v []string
	a.list[id] = &v
	return &v
}

// Text, Bool, and List read an answer back.
func (a *Answers) Text(id string) string {
	if p, ok := a.text[id]; ok {
		return *p
	}
	return ""
}

func (a *Answers) Bool(id string) bool {
	if p, ok := a.flag[id]; ok {
		return *p
	}
	return false
}

// OptionalBool distinguishes "answered false" from "never asked". A question the
// wizard did not put on screen — the macOS-only ones, on another platform — has
// no binding at all, and Bool would report false for it, which Apply would then
// write over whatever the user had configured.
func (a *Answers) OptionalBool(id string) *bool {
	p, ok := a.flag[id]
	if !ok {
		return nil
	}
	v := *p
	return &v
}

func (a *Answers) List(id string) []string {
	if p, ok := a.list[id]; ok {
		return append([]string(nil), *p...)
	}
	return nil
}

// Set writes an answer as a string, which is how a non-Go wizard delivers one:
// "true"/"false" for a bool, comma-separated for a list.
func (a *Answers) Set(id, value string) {
	switch {
	case a.flag[id] != nil:
		v := value == "true"
		*a.flag[id] = v
	case a.list[id] != nil:
		*a.list[id] = SplitList(value)
	default:
		v := value
		a.text[id] = &v
	}
}

// ShouldAsk reports whether a question's gate is satisfied by the answers so
// far. A question whose gate is not met is skipped, and its seeded default is
// what Input sees — which is why gating and application live together here
// rather than in each wizard.
func (a *Answers) ShouldAsk(q Question) bool {
	if !q.Gated() {
		return true
	}
	if p, ok := a.flag[q.RequiresID]; ok {
		return strconv.FormatBool(*p) == q.RequiresValue
	}
	return a.Text(q.RequiresID) == q.RequiresValue
}

// Input is the collected answers, in the shape the config wants them.
type Input struct {
	PollInterval, Hysteresis, LogLevel string
	Quorum                             bool
	Countries                          []string
	ConfigureVPN                       bool
	// AutoMode is automatic tunnel detection: no pinned interface names.
	AutoMode           bool
	Tunnels, Endpoints []string
	Profiles           []config.Profile
	// AutoDiscover is nil when the wizard never asked — the question is macOS-only,
	// so on every other platform there is no answer to apply. Nil rather than
	// false because Apply must leave an unasked key ALONE: writing false would
	// silently clear a value the user set, which is the same failure that made
	// re-running setup delete imported profiles (see mergeProfiles).
	AutoDiscover     *bool
	AllowPhysicalDNS bool
}

// Input folds the answers into the shape Apply writes.
//
// hysteresis and profiles are not questions: hysteresis is carried through from
// the existing config untouched, and profiles are the result of importing the
// files named by the "profileFiles" answer — which the caller does, because
// reading a file is not this package's job.
func (a *Answers) Input(hysteresis string, profiles []config.Profile) Input {
	countries := append(a.List("blockedCountries"), SplitList(a.Text("otherCountries"))...)
	tunnels := a.List("tunnels")
	if len(tunnels) == 0 {
		// The no-tunnels-detected form of the question is free text.
		tunnels = SplitList(a.Text("tunnels"))
	}
	return Input{
		PollInterval: a.Text("pollInterval"),
		Hysteresis:   hysteresis,
		LogLevel:     a.Text("logLevel"),
		Quorum:       a.Bool("providerQuorum"),
		Countries:    countries,
		ConfigureVPN: a.Bool("configureVPN"),
		AutoMode:     a.Bool("autoMode"),
		Tunnels:      tunnels,
		Endpoints:    SplitList(a.Text("endpoints")),
		Profiles:     profiles,
		// Absent on every platform but macOS, where the question is not asked at
		// all. Nil there, so Apply leaves the configured value untouched.
		AutoDiscover:     a.OptionalBool("autoDiscover"),
		AllowPhysicalDNS: a.Bool("allowPhysicalDNS"),
	}
}

// Apply writes collected answers onto cfg. Validation happens after, by the
// caller: this only assembles.
//
// A question the user never reached leaves its part of the config alone. That
// is why the VPN keys are written only when ConfigureVPN is true — answering
// "no" must not blank out a tunnel someone configured earlier.
func Apply(cfg *config.Config, in Input) {
	if d, err := time.ParseDuration(strings.TrimSpace(in.PollInterval)); err == nil {
		cfg.PollInterval = d
	}
	if n, err := strconv.Atoi(strings.TrimSpace(in.Hysteresis)); err == nil {
		cfg.Hysteresis = n
	}
	cfg.LogLevel = in.LogLevel
	cfg.ProviderQuorum = in.Quorum
	cfg.BlockedCountries = in.Countries // config.Normalize upper-cases and de-dupes on save

	if !in.ConfigureVPN {
		return
	}
	if in.AutoMode {
		// Automatic detection: no pinned interface names (Normalize implies
		// autodetect), plus live discovery where supported.
		cfg.VPN.TunnelInterfaces = nil
	} else {
		cfg.VPN.TunnelInterfaces = in.Tunnels
	}
	cfg.VPN.Endpoints = in.Endpoints
	cfg.VPN.Profiles = mergeProfiles(cfg.VPN.Profiles, in.Profiles)
	// Only when the wizard asked. An unasked question leaves its key alone —
	// the same rule ConfigureVPN applies to the whole VPN branch.
	if in.AutoDiscover != nil {
		cfg.VPN.AutoDiscoverEndpoints = *in.AutoDiscover
	}
	cfg.VPN.AllowPhysicalDNS = in.AllowPhysicalDNS
}

// mergeProfiles adds newly imported profiles to the ones already configured,
// replacing by name.
//
// It merges rather than assigns because imported profiles are the only ones the
// wizard collects: assigning would mean that re-running setup and not naming a
// file again silently deleted every saved profile — losing the endpoints a user
// imported months ago, in a wizard they ran to change their log level.
func mergeProfiles(existing, imported []config.Profile) []config.Profile {
	if len(imported) == 0 {
		return existing
	}
	out := append([]config.Profile(nil), existing...)
	for _, p := range imported {
		replaced := false
		for i := range out {
			if out[i].Name == p.Name {
				out[i] = p
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, p)
		}
	}
	return out
}

// ValidDuration validates an answer holding a positive Go duration.
func ValidDuration(s string) error {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return errors.New("enter a duration like 30s or 5m")
	}
	if d <= 0 {
		return errors.New("must be greater than zero")
	}
	return nil
}

// SplitList parses comma-separated input, dropping blanks.
func SplitList(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
