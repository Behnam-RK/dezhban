package setup

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/behnam-rk/dezhban/internal/config"
)

// Apply in auto mode must produce a "connect any VPN" config: no pinned
// interfaces (autodetect implied on Normalize), profiles carried, and
// allowPhysicalDNS honored.
func TestApplyAutoMode(t *testing.T) {
	cfg := config.Default()
	Apply(&cfg, Input{
		PollInterval: "30s", Hysteresis: "3", LogLevel: "info",
		ConfigureVPN: true, AutoMode: true,
		Tunnels:          []string{"utun9"}, // must be ignored in auto mode
		Endpoints:        []string{"vpn.example.com"},
		Profiles:         []config.Profile{{Name: "home", Endpoints: []string{"203.0.113.7"}}},
		AutoDiscover:     true,
		AllowPhysicalDNS: true,
	})
	if len(cfg.VPN.TunnelInterfaces) != 0 {
		t.Errorf("auto mode must not pin interfaces, got %v", cfg.VPN.TunnelInterfaces)
	}
	if !cfg.VPN.AllowPhysicalDNS {
		t.Error("allowPhysicalDNS should be set")
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

// Advanced mode pins the chosen interfaces.
func TestApplyAdvancedPin(t *testing.T) {
	cfg := config.Default()
	Apply(&cfg, Input{
		PollInterval: "30s", Hysteresis: "3", LogLevel: "info",
		ConfigureVPN: true, AutoMode: false,
		Tunnels:   []string{"utun4"},
		Endpoints: []string{"203.0.113.7"},
	})
	if len(cfg.VPN.TunnelInterfaces) != 1 || cfg.VPN.TunnelInterfaces[0] != "utun4" {
		t.Errorf("advanced mode should pin utun4, got %v", cfg.VPN.TunnelInterfaces)
	}
}

// Answering "no" to "configure your VPN now?" must leave a VPN somebody already
// set up completely alone — the wizard is also how people change their log
// level.
func TestDecliningTheVPNBranchTouchesNoVPNKey(t *testing.T) {
	cfg := config.Default()
	cfg.VPN.TunnelInterfaces = []string{"utun4"}
	cfg.VPN.Endpoints = []string{"203.0.113.7"}
	cfg.VPN.AllowPhysicalDNS = true

	Apply(&cfg, Input{PollInterval: "45s", LogLevel: "warn", ConfigureVPN: false})

	if !reflect.DeepEqual(cfg.VPN.TunnelInterfaces, []string{"utun4"}) {
		t.Errorf("tunnels changed: %v", cfg.VPN.TunnelInterfaces)
	}
	if !reflect.DeepEqual(cfg.VPN.Endpoints, []string{"203.0.113.7"}) {
		t.Errorf("endpoints changed: %v", cfg.VPN.Endpoints)
	}
	if !cfg.VPN.AllowPhysicalDNS {
		t.Error("allowPhysicalDNS was cleared")
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("the answers that WERE given should still apply, got logLevel %q", cfg.LogLevel)
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

	qs := byID(Questions(Options{Config: &cfg, ConfigExisted: true, GOOS: "darwin"}))

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
	if got := qs["logLevel"].Default; got != "debug" {
		t.Errorf("logLevel seed = %q", got)
	}
}

// Endpoint discovery is macOS-only, and defaults on only for a brand-new config
// there: re-running setup must never silently flip an explicit false back on.
func TestAutoDiscoverDefaultsOnlyForANewMacConfig(t *testing.T) {
	off := config.Default()
	off.VPN.AutoDiscoverEndpoints = false

	fresh := byID(Questions(Options{Config: &off, ConfigExisted: false, GOOS: "darwin"}))
	if fresh["autoDiscover"].Default != "true" {
		t.Error("a brand-new macOS config should be offered discovery on")
	}

	existing := byID(Questions(Options{Config: &off, ConfigExisted: true, GOOS: "darwin"}))
	if existing["autoDiscover"].Default != "false" {
		t.Error("an existing config's explicit false must be preserved")
	}

	linux := byID(Questions(Options{Config: &off, ConfigExisted: false, GOOS: "linux"}))
	if _, asked := linux["autoDiscover"]; asked {
		t.Error("discovery is macOS-only, so elsewhere it must not be asked at all")
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

	qs := Questions(Options{Config: &cfg, ConfigExisted: true, GOOS: "darwin", DetectedTunnels: []string{"utun4"}})
	a := NewAnswers(qs)
	// The one answer with no config to seed it: the VPN branch is offered, and
	// its own sub-answers are seeded, so accepting them must be a no-op too.
	a.Set("configureVPN", "true")
	a.Set("autoMode", "false")

	after := cfg
	// Hysteresis has no question; the wizard carries the current value through,
	// which is exactly what the caller passes here.
	Apply(&after, a.Input(strconv.Itoa(cfg.Hysteresis), nil))
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
