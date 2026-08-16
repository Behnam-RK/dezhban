package main

import (
	"path/filepath"
	"testing"

	"github.com/behnam-rk/dezhban/internal/config"
)

// roundTripCase is one settable key, the value `config set` is given, and the
// value `config get` must report after the file has been written and read back.
// The two differ wherever Normalize canonicalises (country codes upper-case, log
// levels lower-case), and that difference is the point: the test pins what the
// user actually ends up with, not what they typed.
type roundTripCase struct {
	set  string
	want string
}

// roundTripCases must cover every key in configFields — TestConfigKeyRoundTrip
// fails when it doesn't. That is deliberate: a new settable key is not finished
// until someone has proven it survives a write/read cycle, which is exactly the
// bug class this guards ("I set it, nothing happened").
var roundTripCases = map[string]roundTripCase{
	"pollInterval":     {set: "23s", want: "23s"},
	"blockedCountries": {set: "ir,ru", want: "IR,RU"},
	"hysteresis":       {set: "4", want: "4"},
	"providers":        {set: "https://ifconfig.co/json,https://ipinfo.io/json", want: "https://ifconfig.co/json,https://ipinfo.io/json"},
	"providerQuorum":   {set: "true", want: "true"},
	"logLevel":         {set: "DEBUG", want: "debug"},

	"vpn.tunnelInterfaces":      {set: "utun7", want: "utun7"},
	"vpn.endpoints":             {set: "203.0.113.9", want: "203.0.113.9"},
	"vpn.autoDetect":            {set: "false", want: "false"},
	"vpn.autoDiscoverEndpoints": {set: "false", want: "false"},
	"vpn.allowPhysicalDNS":      {set: "false", want: "false"},
	"vpn.allowGeoProviders":     {set: "false", want: "false"},
	"vpn.allowLocalNetwork":     {set: "false", want: "false"},
	"vpn.autoArm":               {set: "false", want: "false"},
	"vpn.armAtBoot":             {set: "false", want: "false"},
	"vpn.switchWindow":          {set: "7s", want: "7s"},
	"vpn.redialWindow":          {set: "45s", want: "45s"},
	"vpn.pauseMax":              {set: "12m", want: "12m0s"},
	"vpn.endpointRefresh":       {set: "2m", want: "2m0s"},
	"vpn.endpointGrace":         {set: "9m", want: "9m0s"},
	"vpn.tunnelWatch":           {set: "3s", want: "3s"},

	"control.enabled":        {set: "false", want: "false"},
	"control.allowSwitchOps": {set: "false", want: "false"},
	"control.allowPauseOps":  {set: "false", want: "false"},
	"control.allowConfigOps": {set: "false", want: "false"},
	"control.group":          {set: "wheel", want: "wheel"},
	"control.socket":         {set: "/var/run/dezhban-test.sock", want: "/var/run/dezhban-test.sock"},

	"vpn.advanced.switchWindowMax":         {set: "4m", want: "4m0s"},
	"vpn.advanced.redialWindowMax":         {set: "11m", want: "11m0s"},
	"vpn.advanced.redialMinUptime":         {set: "20s", want: "20s"},
	"vpn.advanced.verifyInterval":          {set: "90s", want: "1m30s"},
	"vpn.advanced.livenessRedial":          {set: "true", want: "true"},
	"vpn.advanced.redialBudget":            {set: "3m", want: "3m0s"},
	"vpn.advanced.redialBudgetWindow":      {set: "20m", want: "20m0s"},
	"vpn.advanced.commandFreshness":        {set: "45s", want: "45s"},
	"vpn.advanced.windowDiscoveryInterval": {set: "2s", want: "2s"},
	"vpn.advanced.tunnelPruneAfter":        {set: "90s", want: "1m30s"},
	"vpn.advanced.learnedEndpointTTL":      {set: "48h", want: "48h0m0s"},
	"vpn.advanced.learnedMaxPerProfile":    {set: "32", want: "32"},
	"vpn.advanced.promoteAfterRefreshes":   {set: "5", want: "5"},
	"vpn.advanced.endpointWarnThreshold":   {set: "512", want: "512"},
	"vpn.advanced.windowProtocols":         {set: "udp,tcp", want: "udp,tcp"},
	"vpn.advanced.windowPorts":             {set: "51820,443", want: "51820,443"},
}

// Every settable key must survive the full path a user's edit actually takes:
// `config set` → validate → marshal → file → Load → Normalize → read back. A key
// that is parsed but dropped anywhere along that chain is the "I changed the
// setting and nothing happened" bug, and it is invisible to a test that only
// exercises the in-memory struct.
func TestConfigKeyRoundTrip(t *testing.T) {
	for key, tc := range roundTripCases {
		t.Run(key, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "c.json")
			base := config.Default()
			if err := config.Save(p, &base); err != nil {
				t.Fatal(err)
			}
			if code := cmdConfig([]string{"set", key + "=" + tc.set, "--config", p}); code != 0 {
				t.Fatalf("config set %s=%s exited %d, want 0", key, tc.set, code)
			}
			got, err := config.Load(p)
			if err != nil {
				t.Fatalf("load after setting %s: %v", key, err)
			}
			if v := configFields[key].get(got); v != tc.want {
				t.Errorf("after set %s=%s, get returned %q, want %q", key, tc.set, v, tc.want)
			}
		})
	}
}

// The table above is only a guarantee if it stays exhaustive, so adding a
// settable key without a round-trip case is a test failure rather than a silent
// coverage gap.
func TestRoundTripCasesCoverEverySettableKey(t *testing.T) {
	for key := range configFields {
		if _, ok := roundTripCases[key]; !ok {
			t.Errorf("settable key %q has no round-trip case; add one to roundTripCases", key)
		}
	}
	for key := range roundTripCases {
		if _, ok := configFields[key]; !ok {
			t.Errorf("round-trip case %q is not a settable key; it is dead weight", key)
		}
	}
}

// The CLI's settable keys and the daemon's reloadable keys are two views of one
// vocabulary, maintained in different packages. If they drift, a user can set a
// key the daemon never diffs — so it would silently never be reported as changed
// on reload, which is the very failure this epic exists to remove. Every real
// config key must now be settable — the vpn.advanced.* block that used to be an
// explicit, tracked exception (notYetSettable) no longer is one.
func TestSettableKeysAndReloadKeysAgree(t *testing.T) {
	base := config.Default()
	known := config.KeyValues(&base)

	for key := range configFields {
		if _, ok := known[key]; !ok {
			t.Errorf("%q is settable but unknown to config.KeyValues, so a reload would never notice it changing", key)
		}
	}
	for key := range known {
		if _, ok := configFields[key]; !ok {
			t.Errorf("%q is a real config key with no way to set it; add it to configFields", key)
		}
	}
}

// TestSetRedialMinUptimeZeroDisables pins the CLI path for the one advanced
// duration with disable semantics: "0" must persist as the negative Disabled
// sentinel (surviving Normalize), not silently reset to the 15s default —
// exactly the bug class the three window keys already guard against.
func TestSetRedialMinUptimeZeroDisables(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	base := config.Default()
	if err := config.Save(p, &base); err != nil {
		t.Fatal(err)
	}
	if code := cmdConfig([]string{"set", "vpn.advanced.redialMinUptime=0", "--config", p}); code != 0 {
		t.Fatalf("config set exited %d, want 0", code)
	}
	got, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.VPN.Advanced.RedialMinUptime != config.Disabled {
		t.Errorf("RedialMinUptime = %v, want config.Disabled", got.VPN.Advanced.RedialMinUptime)
	}
	if v := configFields["vpn.advanced.redialMinUptime"].get(got); v != "0s" {
		t.Errorf("get = %q, want \"0s\"", v)
	}
}

// The mirror of the test above, and the reason it needs one of its own: the two
// budget keys are the only durations here that REFUSE a "0" rather than treating
// it as an opt-out or normalising it away. They are limits, so "off" would mean
// "no limit" — the opposite of what "0" means on every other key — and a config
// that accepted it would leave the user believing the bound was lifted when it
// had been reset to 2m. Failing loudly is the whole point.
func TestSetRedialBudgetZeroIsRefused(t *testing.T) {
	for _, key := range []string{"vpn.advanced.redialBudget", "vpn.advanced.redialBudgetWindow"} {
		t.Run(key, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "c.json")
			base := config.Default()
			base.VPN.TunnelInterfaces = []string{"utun3"}
			if err := config.Save(p, &base); err != nil {
				t.Fatal(err)
			}
			if code := cmdConfig([]string{"set", key + "=0", "--config", p}); code == 0 {
				t.Fatalf("config set %s=0 exited 0, want a non-zero exit — a limit has no off", key)
			}
			// And the refusal must not have written anything: a rejected value that
			// still lands on disk is worse than one silently normalised.
			got, err := config.Load(p)
			if err != nil {
				t.Fatal(err)
			}
			if v := configFields[key].get(got); v != configFields[key].get(&base) {
				t.Errorf("%s = %q after a refused write, want the original %q",
					key, v, configFields[key].get(&base))
			}
		})
	}
}
