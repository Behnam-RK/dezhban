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
		AutoMode:     true,
		Tunnels:      []string{"utun9"}, // must be ignored in auto mode
		Endpoints:    eps("vpn.example.com"),
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
		Hysteresis: "3",
		AutoMode:   false,
		Tunnels:    []string{"utun4"},
		Endpoints:  eps("203.0.113.7"),
	})
	if len(cfg.VPN.TunnelInterfaces) != 1 || cfg.VPN.TunnelInterfaces[0] != "utun4" {
		t.Errorf("advanced mode should pin utun4, got %v", cfg.VPN.TunnelInterfaces)
	}
}

// An UNASKED endpoint question must leave a configured server alone.
//
// This replaced the "configure your VPN now?" question as the thing standing
// between a re-run and someone's working config. On macOS the endpoint question
// is gated behind "not automatic", so a user who re-runs setup to change their
// blocked-country list — and leaves automatic detection on, as recommended —
// reaches Apply with no endpoint answer at all. Writing that as an empty list
// would delete their server.
func TestAnUnaskedEndpointListTouchesNoEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.VPN.TunnelInterfaces = []string{"utun4"}
	cfg.VPN.Endpoints = []string{"203.0.113.7"}
	cfg.VPN.AllowPhysicalDNS = true

	// Endpoints nil is what Input produces when the question was never shown.
	Apply(&cfg, Input{Countries: []string{"IR", "SY"}, AutoMode: false,
		Tunnels: []string{"utun4"}})

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
	Apply(&cfg, Input{AutoMode: true})
	if len(cfg.VPN.Profiles) != 2 {
		t.Fatalf("a run importing nothing must keep saved profiles, got %+v", cfg.VPN.Profiles)
	}

	// A run that re-imports one replaces that one and keeps the other.
	Apply(&cfg, Input{AutoMode: true,
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
	Apply(&fresh, Input{AutoMode: true, MacOS: true, ConfigExisted: false})
	if !fresh.VPN.AutoDiscoverEndpoints {
		t.Error("a brand-new macOS config should get discovery on")
	}

	existing := config.Default()
	existing.VPN.AutoDiscoverEndpoints = false
	Apply(&existing, Input{AutoMode: true, MacOS: true, ConfigExisted: true})
	if existing.VPN.AutoDiscoverEndpoints {
		t.Error("an existing config's explicit false must be preserved")
	}

	linux := config.Default()
	linux.VPN.AutoDiscoverEndpoints = false
	Apply(&linux, Input{AutoMode: true, MacOS: false, ConfigExisted: false})
	if linux.VPN.AutoDiscoverEndpoints {
		t.Error("discovery is macOS-only; a new Linux config must not have it defaulted on")
	}

	// An explicit answer (a surface that still collects one) beats the default.
	answered := config.Default()
	answered.VPN.AutoDiscoverEndpoints = true
	off := false
	Apply(&answered, Input{AutoMode: true, MacOS: true, ConfigExisted: false, AutoDiscover: &off})
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

// Detection only sees tunnels that are UP. A re-run while the VPN is down must
// still offer — and preselect — the interface the config pins, or pressing
// Enter through the pick list answers "none of them" and unpins it.
func TestAPinnedTunnelSurvivesADetectionMiss(t *testing.T) {
	cfg := config.Default()
	cfg.BlockedCountries = []string{"IR"}
	cfg.VPN.Endpoints = []string{"203.0.113.7"}
	cfg.VPN.TunnelInterfaces = []string{"utun9"}
	config.Normalize(&cfg)
	before := config.KeyValues(&cfg)

	// utun9 is absent from the detected set: that tunnel is not up right now.
	qs := Questions(Options{Config: &cfg, GOOS: "darwin", DetectedTunnels: []string{"utun0", "utun4"}})

	q := byID(qs)["tunnels"]
	var offered []string
	for _, o := range q.Options {
		offered = append(offered, o.Value)
	}
	if !reflect.DeepEqual(offered, []string{"utun0", "utun4", "utun9"}) {
		t.Errorf("options = %v, want the detected tunnels plus the pinned one", offered)
	}
	if !reflect.DeepEqual(q.Selected, []string{"utun9"}) {
		t.Errorf("selected = %v, want the pinned tunnel preselected", q.Selected)
	}

	// And the whole click-through must be a no-op, as it is when the pin is up.
	after := cfg
	in := NewAnswers(qs).Input(strconv.Itoa(cfg.Hysteresis), nil)
	in.MacOS = true
	in.ConfigExisted = true
	Apply(&after, in)
	config.Normalize(&after)
	for key, want := range before {
		if got := config.KeyValues(&after)[key]; got != want {
			t.Errorf("%s changed by answering nothing: %q -> %q", key, want, got)
		}
	}
}

// --- gating ---

// Automatic detection is the one gate left, and everything manual hangs off it.
func TestAutomaticDetectionGatesEveryManualField(t *testing.T) {
	qs := Questions(Options{GOOS: "darwin"})
	a := NewAnswers(qs)

	a.Set("autoMode", "true")
	for _, q := range qs {
		if q.RequiresID == "autoMode" && a.ShouldAsk(q) {
			t.Errorf("%s should not be asked under automatic detection", q.ID)
		}
	}
	for _, id := range []string{"tunnels", "endpoints", "profileFiles"} {
		if !gatedOnAutoMode(qs, id) {
			t.Errorf("%s is not gated on autoMode; on macOS it must be", id)
		}
	}

	a.Set("autoMode", "false")
	for _, id := range []string{"tunnels", "endpoints", "profileFiles"} {
		if !asked(qs, a, id) {
			t.Errorf("declining automatic detection must ask %s", id)
		}
	}
}

// Off macOS there is no live discovery, so the endpoint is required whichever
// detection mode is chosen. Gating it would let a Linux host finish the wizard
// with a config that cannot enforce.
func TestEndpointsAreUngatedWhereThereIsNoDiscovery(t *testing.T) {
	qs := Questions(Options{GOOS: "linux"})
	a := NewAnswers(qs)
	a.Set("autoMode", "true")
	if !asked(qs, a, "endpoints") {
		t.Error("endpoints must be asked under automatic detection off macOS")
	}
	if gatedOnAutoMode(qs, "endpoints") {
		t.Error("endpoints is gated on autoMode off macOS")
	}
}

// Two steps, which is the whole shape of the wizard: what to block, then how to
// find the VPN. A third group would mean a third screen in the app.
func TestTheWizardIsTwoGroups(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		groups := map[int]bool{}
		for _, q := range Questions(Options{GOOS: goos}) {
			groups[q.Group] = true
		}
		if len(groups) != 2 || !groups[1] || !groups[2] {
			t.Errorf("%s: groups = %v, want exactly {1, 2}", goos, groups)
		}
	}
}

// The guard that replaced "configure your VPN now?". Without it, a re-run on a
// config with pinned interfaces would default to automatic detection, and
// clicking straight through would silently unpin them — Apply clears
// TunnelInterfaces under AutoMode on purpose.
func TestAutoModeSeedsFalseWhenInterfacesArePinned(t *testing.T) {
	pinned := config.Default()
	pinned.VPN.TunnelInterfaces = []string{"utun4"}
	if got := defaultOf(Questions(Options{Config: &pinned, GOOS: "darwin"}), "autoMode"); got != "false" {
		t.Errorf("autoMode default with pinned interfaces = %q, want \"false\"", got)
	}

	fresh := config.Default()
	fresh.VPN.TunnelInterfaces = nil
	if got := defaultOf(Questions(Options{Config: &fresh, GOOS: "darwin"}), "autoMode"); got != "true" {
		t.Errorf("autoMode default with no pinned interfaces = %q, want \"true\"", got)
	}
}

func gatedOnAutoMode(qs []Question, id string) bool {
	for _, q := range qs {
		if q.ID == id {
			return q.RequiresID == "autoMode" && q.RequiresValue == "false"
		}
	}
	return false
}

func asked(qs []Question, a *Answers, id string) bool {
	for _, q := range qs {
		if q.ID == id {
			return a.ShouldAsk(q)
		}
	}
	return false
}

func defaultOf(qs []Question, id string) string {
	for _, q := range qs {
		if q.ID == id {
			return q.Default
		}
	}
	return ""
}

func eps(v ...string) *[]string { return &v }

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
	// Nothing is Set here on purpose. autoMode seeds itself to false from the
	// pinned interfaces above, which is exactly the guard being tested: pressing
	// Enter through the whole wizard must not unpin them.

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
