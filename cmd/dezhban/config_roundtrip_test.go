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
	"vpn.autodetect":            {set: "false", want: "false"},
	"vpn.autoDiscoverEndpoints": {set: "false", want: "false"},
	"vpn.allowPhysicalDNS":      {set: "false", want: "false"},
	"vpn.allowLocalNetwork":     {set: "false", want: "false"},
	"vpn.autoArm":               {set: "false", want: "false"},
	"vpn.armAtBoot":             {set: "false", want: "false"},
	"vpn.switchWindow":          {set: "7s", want: "7s"},
	"vpn.reconnectWindow":       {set: "45s", want: "45s"},
	"vpn.pauseMax":              {set: "12m", want: "12m0s"},
	"vpn.endpointRefresh":       {set: "2m", want: "2m0s"},
	"vpn.endpointGrace":         {set: "9m", want: "9m0s"},
	"vpn.tunnelWatch":           {set: "3s", want: "3s"},

	"control.enabled":        {set: "false", want: "false"},
	"control.allowSwitchOps": {set: "false", want: "false"},
	"control.allowPauseOps":  {set: "false", want: "false"},
	"control.group":          {set: "wheel", want: "wheel"},
	"control.socket":         {set: "/var/run/dezhban-test.sock", want: "/var/run/dezhban-test.sock"},
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
