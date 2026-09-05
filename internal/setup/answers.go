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
	Hysteresis string
	Countries  []string
	// AutoMode is automatic tunnel detection: no pinned interface names.
	AutoMode bool
	Tunnels  []string
	// Endpoints is nil when the wizard never asked — on macOS the question is
	// gated behind "not automatic", because live discovery learns the server
	// address there. Nil rather than empty for the same reason AutoDiscover is
	// a pointer: Apply must leave an unasked key ALONE, and an empty slice is
	// indistinguishable from "asked, and cleared on purpose". Writing it
	// unconditionally would blank the endpoints of anyone who re-ran setup and
	// left automatic detection on.
	Endpoints *[]string
	Profiles  []config.Profile
	// AutoDiscover is nil when nothing answered it — the wizard no longer asks;
	// a surface that still collects an explicit answer can set it. Nil rather
	// than false because Apply must leave an unanswered key ALONE: writing
	// false would silently clear a value the user set, which is the same
	// failure that made re-running setup delete imported profiles (see
	// mergeProfiles).
	AutoDiscover *bool
	// MacOS and ConfigExisted carry the one defaulting decision that used to
	// live on the dropped autoDiscover question: a BRAND-NEW macOS config turns
	// live endpoint discovery on. An existing config is never touched — setup
	// must not flip an explicit false back.
	MacOS         bool
	ConfigExisted bool
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
	var endpoints *[]string
	if a.wasAsked("endpoints") {
		eps := SplitList(a.Text("endpoints"))
		endpoints = &eps
	}
	return Input{
		Hysteresis: hysteresis,
		Countries:  countries,
		AutoMode:   a.Bool("autoMode"),
		Tunnels:    tunnels,
		Endpoints:  endpoints,
		Profiles:   profiles,
		// Nil unless a surface asked the (no-longer-offered) question anyway;
		// nil leaves the configured value untouched in Apply.
		AutoDiscover: a.OptionalBool("autoDiscover"),
	}
}

// wasAsked reports whether the question with this id exists and its gate is
// satisfied by the FINAL answers collected. The question set is retained by
// NewAnswers precisely so this does not have to be re-derived by every caller.
//
// That stands in for "the user saw it", and the two agree only because a gate
// never points forward: every gate names an ungated question in the same or an
// earlier group, so by the time a gated question is put to the user its gate is
// already answered and cannot move afterwards. A gate pointing at a LATER
// group's answer would be read here against that question's seeded default,
// and this would report a question asked that nobody was shown — writing its
// key from a seed. TestGatesAreShallowAndPointBackwards pins the shape.
func (a *Answers) wasAsked(id string) bool {
	for _, q := range a.asked {
		if q.ID == id {
			return a.ShouldAsk(q)
		}
	}
	return false
}

// Apply writes collected answers onto cfg. Validation happens after, by the
// caller: this only assembles.
//
// A question the user never reached leaves its part of the config alone. That
// is why Endpoints is a pointer: on macOS the question is gated behind "not
// automatic", and an unasked endpoint list must not blank out a server someone
// configured earlier.
// Keys the wizard no longer asks about (pollInterval, logLevel,
// providerQuorum, vpn.allowPhysicalDNS) are deliberately not assigned at all:
// unasked means untouched, so re-running setup can never clobber a value tuned
// in Settings or with `config set`.
func Apply(cfg *config.Config, in Input) {
	if n, err := strconv.Atoi(strings.TrimSpace(in.Hysteresis)); err == nil {
		cfg.Hysteresis = n
	}
	cfg.BlockedCountries = in.Countries // config.Normalize upper-cases and de-dupes on save

	if in.AutoMode {
		// Automatic detection: no pinned interface names (Normalize implies
		// autodetect), plus live discovery where supported.
		cfg.VPN.TunnelInterfaces = nil
	} else {
		cfg.VPN.TunnelInterfaces = in.Tunnels
	}
	if in.Endpoints != nil {
		cfg.VPN.Endpoints = *in.Endpoints
	}
	cfg.VPN.Profiles = mergeProfiles(cfg.VPN.Profiles, in.Profiles)
	switch {
	case in.AutoDiscover != nil:
		// An explicit answer, from a surface that still collects one.
		cfg.VPN.AutoDiscoverEndpoints = *in.AutoDiscover
	case in.MacOS && !in.ConfigExisted:
		// The one defaulting decision the dropped autoDiscover question used to
		// carry: a brand-new macOS config gets live discovery on. Never applied
		// to an existing config — setup must not flip an explicit false back.
		cfg.VPN.AutoDiscoverEndpoints = true
	}
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
