package setup

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/config"
)

// Apply in auto mode must produce a "connect any VPN" config: no pinned
// interfaces (autodetect implied on Normalize) and profiles carried.
func TestApplyAutoMode(t *testing.T) {
	cfg := config.Default()
	Apply(&cfg, Input{
		Hysteresis:   "3",
		ConfigureVPN: true, AutoMode: true,
		Tunnels:      []string{"utun9"}, // must be ignored in auto mode
		Endpoints:    []string{"vpn.example.com"},
		Profiles:     []config.Profile{{Name: "home", Endpoints: []string{"203.0.113.7"}}},
		AutoDiscover: boolPtr(true),
	})
	if !cfg.VPN.AutoDiscoverEndpoints {
		t.Error("an answered autoDiscover should be applied")
	}
	if len(cfg.VPN.TunnelInterfaces) != 0 {
		t.Errorf("auto mode must not pin interfaces, got %v", cfg.VPN.TunnelInterfaces)
	}
	if len(cfg.VPN.Profiles) != 1 || cfg.VPN.Profiles[0].Name != "home" {
		t.Errorf("profiles not carried: %+v", cfg.VPN.Profiles)
	}
	// Normalize (run on save) implies autodetect when no interfaces are pinned.
	config.Normalize(&cfg)
	if !cfg.VPN.AutoDetect {
		t.Error("autodetect should be implied for an auto-mode config")
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("auto-mode config should validate: %v", err)
	}
}

func boolPtr(v bool) *bool { return &v }

// A question the wizard never put on screen must not have its key rewritten.
// This used to cover only the macOS-gated autoDiscover question; since the
// wizard shrank to its essentials it also names every key the dropped
// questions wrote — pollInterval, logLevel, providerQuorum,
// vpn.allowPhysicalDNS — because "unasked means untouched" is exactly what
// makes dropping a question safe for a tuned config.
func TestAnUnaskedQuestionLeavesItsKeyAlone(t *testing.T) {
	cfg := config.Default()
	cfg.VPN.AutoDiscoverEndpoints = true
	cfg.PollInterval = 45 * time.Second
	cfg.LogLevel = "debug"
	cfg.ProviderQuorum = true
	cfg.VPN.AllowPhysicalDNS = false // an explicit tightening, the worst thing to clobber

	qs := Questions(Options{Config: &cfg, GOOS: "linux"})
	for _, q := range qs {
		switch q.ID {
		case "autoDiscover", "pollInterval", "logLevel", "providerQuorum", "allowPhysicalDNS":
			t.Fatalf("the %s question should no longer be asked", q.ID)
		}
	}

	answers := NewAnswers(qs)
	if answers.OptionalBool("autoDiscover") != nil {
		t.Fatal("an unasked question should have no binding")
	}
	in := answers.Input(strconv.Itoa(cfg.Hysteresis), nil)
	in.ConfigExisted = true
	Apply(&cfg, in)

	if !cfg.VPN.AutoDiscoverEndpoints {
		t.Error("vpn.autoDiscoverEndpoints was cleared by a wizard that never asked about it")
	}
	if cfg.PollInterval != 45*time.Second {
		t.Errorf("pollInterval changed to %v by a wizard that never asked about it", cfg.PollInterval)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("logLevel changed to %q by a wizard that never asked about it", cfg.LogLevel)
	}
	if !cfg.ProviderQuorum {
		t.Error("providerQuorum was cleared by a wizard that never asked about it")
	}
	if cfg.VPN.AllowPhysicalDNS {
		t.Error("an explicit vpn.allowPhysicalDNS=false was flipped back by a wizard that never asked about it")
	}
}

// Advanced mode pins the chosen interfaces.
func TestApplyAdvancedPin(t *testing.T) {
	cfg := config.Default()
	Apply(&cfg, Input{
		Hysteresis:   "3",
		ConfigureVPN: true, AutoMode: false,
		Tunnels:   []string{"utun4"},
		Endpoints: []string{"203.0.113.7"},
	})
	if len(cfg.VPN.TunnelInterfaces) != 1 || cfg.VPN.TunnelInterfaces[0] != "utun4" {
		t.Errorf("advanced mode should pin utun4, got %v", cfg.VPN.TunnelInterfaces)
	}
}

// Answering "no" to "configure your VPN now?" must leave a VPN somebody already
// set up completely alone — the wizard is also how people change their
// blocked-country list.
func TestDecliningTheVPNBranchTouchesNoVPNKey(t *testing.T) {
	cfg := config.Default()
	cfg.VPN.TunnelInterfaces = []string{"utun4"}
	cfg.VPN.Endpoints = []string{"203.0.113.7"}
	cfg.VPN.AllowPhysicalDNS = true

	Apply(&cfg, Input{Countries: []string{"IR", "SY"}, ConfigureVPN: false})

	if !reflect.DeepEqual(cfg.VPN.TunnelInterfaces, []string{"utun4"}) {
		t.Errorf("tunnels changed: %v", cfg.VPN.TunnelInterfaces)
	}
	if !reflect.DeepEqual(cfg.VPN.Endpoints, []string{"203.0.113.7"}) {
		t.Errorf("endpoints changed: %v", cfg.VPN.Endpoints)
	}
	if !cfg.VPN.AllowPhysicalDNS {
		t.Error("allowPhysicalDNS was cleared")
	}
	if !reflect.DeepEqual(cfg.BlockedCountries, []string{"IR", "SY"}) {
		t.Errorf("the answers that WERE given should still apply, got blockedCountries %v", cfg.BlockedCountries)
	}
}

// Profiles are imported from files named in the wizard, and nowhere else. If
// applying them ASSIGNED rather than merged, re-running setup without naming a
// file again would silently delete every saved profile.
func TestImportedProfilesAddToTheSavedOnes(t *testing.T) {
	cfg := config.Default()
	cfg.VPN.Profiles = []config.Profile{
		{Name: "home", Endpoints: []string{"203.0.113.7"}},
		{Name: "work", Endpoints: []string{"198.51.100.9"}},
	}

	// A run that imported nothing keeps both.
	Apply(&cfg, Input{ConfigureVPN: true, AutoMode: true})
	if len(cfg.VPN.Profiles) != 2 {
		t.Fatalf("a run importing nothing must keep saved profiles, got %+v", cfg.VPN.Profiles)
	}

	// A run that re-imports one replaces that one and keeps the other.
	Apply(&cfg, Input{ConfigureVPN: true, AutoMode: true,
		Profiles: []config.Profile{{Name: "work", Endpoints: []string{"192.0.2.5"}}}})
	if len(cfg.VPN.Profiles) != 2 {
		t.Fatalf("re-importing a profile must not drop the others, got %+v", cfg.VPN.Profiles)
	}
	byName := map[string][]string{}
	for _, p := range cfg.VPN.Profiles {
		byName[p.Name] = p.Endpoints
	}
	if !reflect.DeepEqual(byName["work"], []string{"192.0.2.5"}) {
		t.Errorf("re-imported profile should win, got %v", byName["work"])
	}
	if !reflect.DeepEqual(byName["home"], []string{"203.0.113.7"}) {
		t.Errorf("untouched profile should survive, got %v", byName["home"])
	}
}

// --- questions ---

// The wizard edits rather than clobbers, so every question must start at what
// the config already says.
func TestQuestionsSeedFromTheConfig(t *testing.T) {
	cfg := config.Default()
	cfg.BlockedCountries = []string{"IR", "AQ", "CN"}
	cfg.VPN.Endpoints = []string{"203.0.113.7", "vpn.example.com"}
	cfg.LogLevel = "debug"

	qs := byID(Questions(Options{Config: &cfg, GOOS: "darwin"}))

	if got := qs["blockedCountries"].Selected; !reflect.DeepEqual(got, []string{"IR", "CN"}) {
		t.Errorf("checkbox seed = %v, want the codes that ARE on the common list", got)
	}
	// A configured code the wizard does not offer must survive a re-run, which
	// means it has to land in the free-text question.
	if got := qs["otherCountries"].Default; got != "AQ" {
		t.Errorf("other-countries seed = %q, want the codes not on the list", got)
	}
	if got := qs["endpoints"].Default; got != "203.0.113.7,vpn.example.com" {
		t.Errorf("endpoints seed = %q", got)
	}
}

// Endpoint discovery defaults on only for a brand-new macOS config: the
// defaulting used to live on the (since-dropped) autoDiscover question and now
// lives in Apply, and re-running setup must still never silently flip an
// explicit false back on.
func TestAutoDiscoverDefaultsOnlyForANewMacConfig(t *testing.T) {
	fresh := config.Default()
	fresh.VPN.AutoDiscoverEndpoints = false // Default() has it on; force the observable flip
	Apply(&fresh, Input{ConfigureVPN: true, AutoMode: true, MacOS: true, ConfigExisted: false})
	if !fresh.VPN.AutoDiscoverEndpoints {
		t.Error("a brand-new macOS config should get discovery on")
	}

	existing := config.Default()
	existing.VPN.AutoDiscoverEndpoints = false
	Apply(&existing, Input{ConfigureVPN: true, AutoMode: true, MacOS: true, ConfigExisted: true})
	if existing.VPN.AutoDiscoverEndpoints {
		t.Error("an existing config's explicit false must be preserved")
	}

	linux := config.Default()
	linux.VPN.AutoDiscoverEndpoints = false
	Apply(&linux, Input{ConfigureVPN: true, AutoMode: true, MacOS: false, ConfigExisted: false})
	if linux.VPN.AutoDiscoverEndpoints {
		t.Error("discovery is macOS-only; a new Linux config must not have it defaulted on")
	}

	// An explicit answer (a surface that still collects one) beats the default.
	answered := config.Default()
	answered.VPN.AutoDiscoverEndpoints = true
	off := false
	Apply(&answered, Input{ConfigureVPN: true, AutoMode: true, MacOS: true, ConfigExisted: false, AutoDiscover: &off})
	if answered.VPN.AutoDiscoverEndpoints {
		t.Error("an explicit false answer must win over the new-config default")
	}
}

// The tunnel question is a pick list when tunnels were detected and free text
// when none were — the same split the CLI used to make on its own.
func TestTunnelQuestionFollowsDetection(t *testing.T) {
	cfg := config.Default()
	cfg.VPN.TunnelInterfaces = []string{"utun4"}

	detected := byID(Questions(Options{Config: &cfg, GOOS: "darwin", DetectedTunnels: []string{"utun4", "utun7"}}))["tunnels"]
	if detected.Kind != KindMultiSelect {
		t.Errorf("kind = %q, want a pick list when tunnels were detected", detected.Kind)
	}
	if !reflect.DeepEqual(detected.Selected, []string{"utun4"}) {
		t.Errorf("configured tunnel should be preselected, got %v", detected.Selected)
	}

	none := byID(Questions(Options{Config: &cfg, GOOS: "darwin"}))["tunnels"]
	if none.Kind != KindList {
		t.Errorf("kind = %q, want free text when nothing was detected", none.Kind)
	}
	if none.Default != "utun4" {
		t.Errorf("free-text seed = %q", none.Default)
	}
}

// --- gating ---

func TestGatingHidesTheWholeVPNBranch(t *testing.T) {
	qs := Questions(Options{GOOS: "darwin"})
	a := NewAnswers(qs)
	a.Set("configureVPN", "false")

	for _, q := range qs {
		if q.RequiresID == "configureVPN" && a.ShouldAsk(q) {
			t.Errorf("%s should not be asked when the VPN branch was declined", q.ID)
		}
	}

	a.Set("configureVPN", "true")
	a.Set("autoMode", "true")
	for _, q := range qs {
		if q.ID == "tunnels" && a.ShouldAsk(q) {
			t.Error("automatic detection must not ask which interface to pin")
		}
	}
	a.Set("autoMode", "false")
	for _, q := range qs {
		if q.ID == "tunnels" && !a.ShouldAsk(q) {
			t.Error("declining automatic detection must ask which interface to pin")
		}
	}
}

// Walking the wizard and pressing Enter on every question must land on the
// config you started with. Anything else means a default is stated in one place
// and applied differently in another — the drift Phase M exists to prevent,
// arriving through the wizard instead.
func TestAnsweringNothingChangesNothing(t *testing.T) {
	cfg := config.Default()
	cfg.BlockedCountries = []string{"IR", "AQ"}
	cfg.VPN.Endpoints = []string{"203.0.113.7"}
	cfg.VPN.TunnelInterfaces = []string{"utun4"}
	cfg.VPN.AllowPhysicalDNS = true
	config.Normalize(&cfg)
	before := config.KeyValues(&cfg)

	qs := Questions(Options{Config: &cfg, GOOS: "darwin", DetectedTunnels: []string{"utun4"}})
	a := NewAnswers(qs)
	// The one answer with no config to seed it: the VPN branch is offered, and
	// its own sub-answers are seeded, so accepting them must be a no-op too.
	a.Set("configureVPN", "true")
	a.Set("autoMode", "false")

	after := cfg
	// Hysteresis has no question; the wizard carries the current value through,
	// which is exactly what the caller passes here. MacOS/ConfigExisted mirror
	// the real caller: an existing config's discovery setting must not move.
	in := a.Input(strconv.Itoa(cfg.Hysteresis), nil)
	in.MacOS = true
	in.ConfigExisted = true
	Apply(&after, in)
	config.Normalize(&after)

	for key, want := range before {
		if got := config.KeyValues(&after)[key]; got != want {
			t.Errorf("%s changed by answering nothing: %q → %q", key, want, got)
		}
	}
}

func TestValidDuration(t *testing.T) {
	for _, ok := range []string{"30s", "5m", " 1h "} {
		if err := ValidDuration(ok); err != nil {
			t.Errorf("ValidDuration(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "soon", "0s", "-5m"} {
		if err := ValidDuration(bad); err == nil {
			t.Errorf("ValidDuration(%q) accepted", bad)
		}
	}
}

func byID(qs []Question) map[string]Question {
	out := make(map[string]Question, len(qs))
	for _, q := range qs {
		out[q.ID] = q
	}
	return out
}
