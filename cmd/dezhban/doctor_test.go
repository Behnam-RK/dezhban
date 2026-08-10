package main

import (
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/config"
	"github.com/behnam-rk/dezhban/internal/control"
	"github.com/behnam-rk/dezhban/internal/netdetect"
	"github.com/behnam-rk/dezhban/internal/state"
)

// The pure formatters (buildTunnelsCheck/buildEndpointsCheck/buildLockoutCheck)
// take already-resolved data, so they are testable with synthetic subnets and
// routes — a real "endpoint inside the tunnel's subnet" failure needs a live OS
// tunnel interface, which a portable test cannot depend on (see the on-host
// checklist in docs/contribute/testing.md's "VPN interface guard" section).

func TestBuildTunnelsCheck(t *testing.T) {
	t.Run("no tunnels", func(t *testing.T) {
		c := buildTunnelsCheck(nil, nil)
		if c.Status != checkWarn {
			t.Errorf("status = %q, want %q", c.Status, checkWarn)
		}
		if len(c.Details) != 1 || c.Details[0] != "(none — set vpn.tunnelInterfaces or vpn.autoDetect)" {
			t.Errorf("details = %v", c.Details)
		}
	})

	t.Run("tunnel with a subnet", func(t *testing.T) {
		nets := []netdetect.TunnelNet{{Iface: "utun4", Subnet: netip.MustParsePrefix("10.0.0.0/24")}}
		c := buildTunnelsCheck([]string{"utun4"}, nets)
		if c.Status != checkOK {
			t.Errorf("status = %q, want %q", c.Status, checkOK)
		}
		want := "utun4 — 10.0.0.0/24"
		if len(c.Details) != 1 || c.Details[0] != want {
			t.Errorf("details = %v, want [%q]", c.Details, want)
		}
	})

	t.Run("tunnel with no subnet (down or absent)", func(t *testing.T) {
		c := buildTunnelsCheck([]string{"utun9"}, nil)
		if c.Status != checkOK {
			t.Errorf("status = %q, want %q (informational only)", c.Status, checkOK)
		}
		want := "utun9 — no subnet (interface down or absent?)"
		if len(c.Details) != 1 || c.Details[0] != want {
			t.Errorf("details = %v, want [%q]", c.Details, want)
		}
	})

	t.Run("multiple subnets on one tunnel are joined", func(t *testing.T) {
		nets := []netdetect.TunnelNet{
			{Iface: "utun4", Subnet: netip.MustParsePrefix("10.0.0.0/24")},
			{Iface: "utun4", Subnet: netip.MustParsePrefix("fe80::/64")},
		}
		c := buildTunnelsCheck([]string{"utun4"}, nets)
		want := "utun4 — 10.0.0.0/24, fe80::/64"
		if len(c.Details) != 1 || c.Details[0] != want {
			t.Errorf("details = %v, want [%q]", c.Details, want)
		}
	})
}

func TestBuildLivenessCheck(t *testing.T) {
	t.Run("daemon not running", func(t *testing.T) {
		c := buildLivenessCheck(state.Snapshot{}, false)
		if c.Status != checkOK {
			t.Errorf("status = %q, want %q", c.Status, checkOK)
		}
		if c.Summary != "not checked — dezhban isn't running." {
			t.Errorf("summary = %q", c.Summary)
		}
	})

	t.Run("clean snapshot reports OK with no details", func(t *testing.T) {
		c := buildLivenessCheck(state.Snapshot{}, true)
		if c.Status != checkOK {
			t.Errorf("status = %q, want %q", c.Status, checkOK)
		}
		if len(c.Details) != 0 {
			t.Errorf("details = %v, want none", c.Details)
		}
	})

	t.Run("verify missing escalates to warn", func(t *testing.T) {
		snap := state.Snapshot{Verify: &state.VerifyState{Missing: true, Repairs: 2}}
		c := buildLivenessCheck(snap, true)
		if c.Status != checkWarn {
			t.Errorf("status = %q, want %q", c.Status, checkWarn)
		}
		if len(c.Details) != 1 || !strings.Contains(c.Details[0], "re-applied 2 time(s)") {
			t.Errorf("details = %v", c.Details)
		}
	})

	// ExitIPChangedAt is purely observational (CLAUDE.md: "it never flips
	// posture and never touches the hysteresis streak") and must not escalate
	// Status on its own — a failover between VPN servers in the same allowed
	// country is not a lockout risk, just a fact worth surfacing.
	t.Run("exit IP change alone stays OK but is reported", func(t *testing.T) {
		changedAt := time.Date(2026, 8, 9, 15, 4, 0, 0, time.UTC)
		snap := state.Snapshot{ExitIPChangedAt: changedAt}
		c := buildLivenessCheck(snap, true)
		if c.Status != checkOK {
			t.Errorf("status = %q, want %q", c.Status, checkOK)
		}
		if len(c.Details) != 1 || !strings.Contains(c.Details[0], "Exit IP last changed at") {
			t.Errorf("details = %v", c.Details)
		}
	})

	t.Run("verify and exit IP change combine under warn", func(t *testing.T) {
		snap := state.Snapshot{
			Verify:          &state.VerifyState{Err: "permission denied"},
			ExitIPChangedAt: time.Date(2026, 8, 9, 15, 4, 0, 0, time.UTC),
		}
		c := buildLivenessCheck(snap, true)
		if c.Status != checkWarn {
			t.Errorf("status = %q, want %q", c.Status, checkWarn)
		}
		if len(c.Details) != 2 {
			t.Errorf("details = %v, want 2 lines", c.Details)
		}
	})
}

func TestBuildControlCheck(t *testing.T) {
	base := func() config.Config {
		cfg := config.Default()
		cfg.Control.Enabled = true
		cfg.Control.Group = "admin"
		return cfg
	}

	t.Run("disabled", func(t *testing.T) {
		cfg := base()
		cfg.Control.Enabled = false
		c := buildControlCheck(&cfg, control.Response{}, nil)
		if c.Status != checkWarn {
			t.Errorf("status = %q, want %q", c.Status, checkWarn)
		}
		if !strings.Contains(c.Summary, "disabled") {
			t.Errorf("summary = %q, want it to say disabled", c.Summary)
		}
		// `config set` is in the privileged set — it needs an enrolled control
		// token, not just group membership. A fix modelled without sudo tells
		// the user to run something that fails, on the one check whose whole
		// subject is which commands need a password.
		if len(c.Fixes) != 1 || !strings.HasPrefix(c.Fixes[0], "sudo dezhban config set control.enabled") {
			t.Errorf("fixes = %v, want a single `sudo dezhban config set control.enabled true`", c.Fixes)
		}
	})

	t.Run("forbidden — reachable but not in the group", func(t *testing.T) {
		cfg := base()
		c := buildControlCheck(&cfg, control.Response{}, control.ErrForbidden)
		if c.Status != checkWarn {
			t.Errorf("status = %q, want %q", c.Status, checkWarn)
		}
		if !strings.Contains(c.Summary, "not in the") || !strings.Contains(c.Summary, "admin") {
			t.Errorf("summary = %q, want it to name the group", c.Summary)
		}
		// Adding an account to a unix group is usermod on Linux and dseditgroup
		// on macOS — no portable command to offer, so this branch must explain
		// rather than invent one.
		if len(c.Fixes) != 0 {
			t.Errorf("fixes = %v, want none — there is no portable add-to-group command to badge", c.Fixes)
		}
		if !strings.Contains(strings.Join(c.Details, "\n"), "passwordless.md") {
			t.Errorf("details = %v, want the doc pointer", c.Details)
		}
	})

	// doctorCheck.Fixes is documented as "commands or actions ... never prose
	// about them — the GUI badges each one". A sentence in that slot renders as
	// a command the user should type, so every branch is checked, not just the
	// ones a case above happens to exercise.
	t.Run("no branch puts prose in Fixes", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			mutate   func(*config.Config)
			resp     control.Response
			probeErr error
		}{
			{"disabled", func(c *config.Config) { c.Control.Enabled = false }, control.Response{}, nil},
			{"forbidden", nil, control.Response{}, control.ErrForbidden},
			{"unreachable", nil, control.Response{}, errors.New("dial: no such file")},
			{"unreachable, no group", func(c *config.Config) { c.Control.Group = "" }, control.Response{}, errors.New("dial: no such file")},
			{"reachable, no group", func(c *config.Config) { c.Control.Group = "" }, control.Response{OK: true}, nil},
			{"reachable and a member", nil, control.Response{OK: true}, nil},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cfg := base()
				if tc.mutate != nil {
					tc.mutate(&cfg)
				}
				for _, f := range buildControlCheck(&cfg, tc.resp, tc.probeErr).Fixes {
					if !strings.HasPrefix(f, "dezhban ") && !strings.HasPrefix(f, "sudo dezhban ") {
						t.Errorf("fix %q is not a runnable command — prose belongs in Details", f)
					}
				}
			})
		}
	})

	t.Run("unreachable — no daemon running", func(t *testing.T) {
		cfg := base()
		c := buildControlCheck(&cfg, control.Response{}, errors.New("dial: no such file"))
		if c.Status != checkWarn {
			t.Errorf("status = %q, want %q", c.Status, checkWarn)
		}
		if !strings.Contains(c.Summary, "unreachable") {
			t.Errorf("summary = %q, want it to say unreachable", c.Summary)
		}
	})

	t.Run("unreachable and no group configured names both problems", func(t *testing.T) {
		cfg := base()
		cfg.Control.Group = ""
		c := buildControlCheck(&cfg, control.Response{}, errors.New("dial: no such file"))
		if len(c.Details) == 0 || !strings.Contains(c.Details[0], "no group") {
			t.Errorf("details = %v, want a note about the missing group", c.Details)
		}
	})

	t.Run("reachable, no group configured", func(t *testing.T) {
		cfg := base()
		cfg.Control.Group = ""
		c := buildControlCheck(&cfg, control.Response{OK: true}, nil)
		if c.Status != checkWarn {
			t.Errorf("status = %q, want %q", c.Status, checkWarn)
		}
		if !strings.Contains(c.Summary, "no group") {
			t.Errorf("summary = %q, want it to say no group is configured", c.Summary)
		}
	})

	t.Run("reachable and a member — the happy path", func(t *testing.T) {
		cfg := base()
		c := buildControlCheck(&cfg, control.Response{OK: true}, nil)
		if c.Status != checkOK {
			t.Errorf("status = %q, want %q", c.Status, checkOK)
		}
		if !strings.Contains(c.Summary, "need no password") {
			t.Errorf("summary = %q, want it to confirm no password is needed", c.Summary)
		}
	})

	t.Run("gated ops are named regardless of reachability", func(t *testing.T) {
		cfg := base()
		cfg.Control.AllowSwitchOps = false
		cfg.Control.AllowConfigOps = false
		c := buildControlCheck(&cfg, control.Response{OK: true}, nil)
		joined := strings.Join(c.Details, "\n")
		if !strings.Contains(joined, "allowSwitchOps=false") {
			t.Errorf("details = %v, want the switch gate named", c.Details)
		}
		if !strings.Contains(joined, "allowConfigOps=false") {
			t.Errorf("details = %v, want the config-write gate named", c.Details)
		}
		if strings.Contains(joined, "allowPauseOps=false") {
			t.Errorf("details = %v, want the pause gate NOT named (it wasn't disabled)", c.Details)
		}
	})
}

func TestBuildEndpointsCheck(t *testing.T) {
	t.Run("no endpoints resolved", func(t *testing.T) {
		c := buildEndpointsCheck(nil, nil)
		if c.Status != checkWarn {
			t.Errorf("status = %q, want %q", c.Status, checkWarn)
		}
		if len(c.Details) != 1 || c.Details[0] != "(none resolved)" {
			t.Errorf("details = %v", c.Details)
		}
	})

	t.Run("all endpoints ok", func(t *testing.T) {
		eps := []netip.Addr{netip.MustParseAddr("203.0.113.9")}
		c := buildEndpointsCheck(eps, nil)
		if c.Status != checkOK {
			t.Errorf("status = %q, want %q", c.Status, checkOK)
		}
		want := "203.0.113.9 — ok (assumed reachable on the physical interface)"
		if len(c.Details) != 1 || c.Details[0] != want {
			t.Errorf("details = %v, want [%q]", c.Details, want)
		}
		if len(c.Fixes) != 0 {
			t.Errorf("fixes = %v, want none", c.Fixes)
		}
	})

	t.Run("a misconfigured (tunnel-internal) endpoint fails with a fix", func(t *testing.T) {
		bad := netip.MustParseAddr("10.0.0.1")
		ok := netip.MustParseAddr("203.0.113.9")
		route := netdetect.EndpointRoute{Endpoint: bad, Iface: "utun4", Subnet: netip.MustParsePrefix("10.0.0.0/24")}
		c := buildEndpointsCheck([]netip.Addr{bad, ok}, []netdetect.EndpointRoute{route})
		if c.Status != checkFail {
			t.Errorf("status = %q, want %q", c.Status, checkFail)
		}
		wantBad := "10.0.0.1 — MISCONFIGURED: inside utun4's subnet 10.0.0.0/24"
		wantOK := "203.0.113.9 — ok (assumed reachable on the physical interface)"
		if len(c.Details) != 2 || c.Details[0] != wantBad || c.Details[1] != wantOK {
			t.Errorf("details = %v, want [%q %q]", c.Details, wantBad, wantOK)
		}
		wantFix := "10.0.0.1 is a tunnel-internal address (inside utun4 10.0.0.0/24); set vpn.endpoints to\n" +
			"    your VPN server's PUBLIC IP from your VPN client config."
		if len(c.Fixes) != 1 || c.Fixes[0] != wantFix {
			t.Errorf("fixes = %v, want [%q]", c.Fixes, wantFix)
		}
	})
}

func TestBuildLockoutCheck(t *testing.T) {
	c := buildLockoutCheck([]string{"utun4"})
	if c.Status != checkFail {
		t.Errorf("status = %q, want %q", c.Status, checkFail)
	}
	if c.Summary != "dezhban will refuse to start" {
		t.Errorf("summary = %q", c.Summary)
	}
	if len(c.Details) == 0 || c.Details[0] != "The VPN guard is on and utun4 is up, but no server address is known." {
		t.Errorf("details[0] = %v", c.Details)
	}
	if len(c.Fixes) != 3 {
		t.Errorf("fixes = %v, want 3 lines", c.Fixes)
	}
}

// TestRunDoctor exercises the whole report end to end for the two cases that
// don't need a live OS tunnel interface: no tunnels configured at all, and a
// tunnel present with no endpoint known (the lockout risk).
func TestRunDoctor(t *testing.T) {
	log := newLogger(&config.Config{LogLevel: "error"})

	t.Run("no tunnels, no lockout", func(t *testing.T) {
		cfg := config.Default()
		cfg.VPN.AutoDetect = false
		cfg.VPN.Endpoints = []string{"203.0.113.9"}
		r := runDoctor(&cfg, log, false)
		if !r.OK {
			t.Errorf("OK = false, want true")
		}
		byName := map[string]doctorCheck{}
		for _, c := range r.Checks {
			byName[c.Name] = c
		}
		if byName["tunnels"].Status != checkWarn {
			t.Errorf("tunnels status = %q, want %q", byName["tunnels"].Status, checkWarn)
		}
		if byName["endpoints"].Status != checkOK {
			t.Errorf("endpoints status = %q, want %q", byName["endpoints"].Status, checkOK)
		}
		if _, hasLockout := byName["lockout"]; hasLockout {
			t.Error("lockout check present, want none (no tunnels means nothing to lock out)")
		}
	})

	t.Run("tunnel present, no endpoint known: lockout", func(t *testing.T) {
		cfg := config.Default()
		cfg.VPN.AutoDetect = false
		cfg.VPN.TunnelInterfaces = []string{"faketun0"} // explicit: passes through regardless of real OS state
		cfg.VPN.AutoDiscoverEndpoints = false
		cfg.VPN.Endpoints = nil
		r := runDoctor(&cfg, log, false)
		if r.OK {
			t.Error("OK = true, want false (lockout risk)")
		}
		byName := map[string]doctorCheck{}
		for _, c := range r.Checks {
			byName[c.Name] = c
		}
		lockout, ok := byName["lockout"]
		if !ok {
			t.Fatal("lockout check missing")
		}
		if lockout.Status != checkFail {
			t.Errorf("lockout status = %q, want %q", lockout.Status, checkFail)
		}
	})
}

// TestPrintDoctorMatchesKnownLayout pins printDoctor's exact text layout for a
// hand-built report — this is what the earlier text-based cmdDoctor produced
// before the refactor into runDoctor/printDoctor, verified by diffing built
// binaries across configs/*.json during development. Golden-string coverage
// here catches a regression a future edit to printDoctor might introduce.
func TestPrintDoctorMatchesKnownLayout(t *testing.T) {
	r := doctorReport{
		OK: false,
		Checks: []doctorCheck{
			{Name: "config", Status: checkOK, Summary: "OK (loaded and validated)"},
			{Name: "tunnels", Status: checkOK, Details: []string{"utun4 — 10.0.0.0/24"}},
			{
				Name: "endpoints", Status: checkFail,
				Details: []string{"10.0.0.1 — MISCONFIGURED: inside utun4's subnet 10.0.0.0/24"},
				Fixes: []string{
					"10.0.0.1 is a tunnel-internal address (inside utun4 10.0.0.0/24); set vpn.endpoints to\n" +
						"    your VPN server's PUBLIC IP from your VPN client config.",
				},
			},
		},
	}
	want := "dezhban doctor\n" +
		"\n" +
		"config:  OK (loaded and validated)\n" +
		"\n" +
		"tunnels:\n" +
		"  utun4 — 10.0.0.0/24\n" +
		"\n" +
		"endpoints (resolved: literals + hostnames + discovery):\n" +
		"  10.0.0.1 — MISCONFIGURED: inside utun4's subnet 10.0.0.0/24\n" +
		"\n" +
		"fixes:\n" +
		"  - 10.0.0.1 is a tunnel-internal address (inside utun4 10.0.0.0/24); set vpn.endpoints to\n" +
		"    your VPN server's PUBLIC IP from your VPN client config.\n" +
		"\n"
	got := captureStdout(t, func() { printDoctor(r) })
	if got != want {
		t.Errorf("printDoctor output mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// The three checks the layout test above does not reach, and which the
// build-a-binary-and-diff verification could not reach either: `lockout` needs
// a config with a tunnel and no endpoint, `touchID` only renders on a Mac
// WITHOUT pam_tid configured, and `discover` needs --discover. All three carry
// layout the plain checks don't — the paragraph break inside Details, and the
// four-space Fixes indent — so they are pinned by hand here.
func TestPrintDoctorLayoutForLockoutTouchIDAndDiscover(t *testing.T) {
	r := doctorReport{
		OK: false,
		Checks: []doctorCheck{
			{Name: "config", Status: checkOK, Summary: "OK (loaded and validated)"},
			{Name: "tunnels", Status: checkOK, Details: []string{"utun4 — 10.0.0.0/24"}},
			{Name: "endpoints", Status: checkWarn, Details: []string{"(none resolved)"}},
			buildLockoutCheck([]string{"utun4"}),
			{
				Name: "touchID", Status: checkWarn,
				Summary: "not configured for sudo — privileged ops will ask for a password.",
				Details: []string{"To authenticate with a fingerprint instead (survives OS updates):"},
				Fixes:   []string{"echo 'auth       sufficient     pam_tid.so' | sudo tee /etc/pam.d/sudo_local"},
			},
			{
				Name: "discover", Status: checkOK,
				Details: []string{"198.51.100.7:51820 [wg0]  <- not in vpn.endpoints"},
				Fixes:   []string{"add any missing server IP to vpn.endpoints and drop stale entries."},
			},
		},
	}
	want := "dezhban doctor\n" +
		"\n" +
		"config:  OK (loaded and validated)\n" +
		"\n" +
		"tunnels:\n" +
		"  utun4 — 10.0.0.0/24\n" +
		"\n" +
		"endpoints (resolved: literals + hostnames + discovery):\n" +
		"  (none resolved)\n" +
		"\n" +
		"LOCKOUT RISK — dezhban will refuse to start:\n" +
		"  The VPN guard is on and utun4 is up, but no server address is known.\n" +
		"  The guard would block the tunnel's own transport and cut ALL traffic.\n" +
		"\n" +
		"  Auto-discovery reads CONNECTED sockets. WireGuard (and other\n" +
		"  NetworkExtension clients) send from an UNCONNECTED UDP socket, so they\n" +
		"  never appear as a connected flow — discovery cannot find them. Name the\n" +
		"  server explicitly:\n" +
		"\n" +
		"    dezhban vpn import <wg0.conf|client.ovpn>   # reads the endpoint from it\n" +
		"    dezhban vpn add <name> --endpoint <host-or-ip>\n" +
		"    sudo dezhban config set vpn.endpoints=<server-ip>\n" +
		"\n" +
		"touch id: not configured for sudo — privileged ops will ask for a password.\n" +
		"  To authenticate with a fingerprint instead (survives OS updates):\n" +
		"\n" +
		"    echo 'auth       sufficient     pam_tid.so' | sudo tee /etc/pam.d/sudo_local\n" +
		"\n" +
		"discover (best-effort, macOS):\n" +
		"  198.51.100.7:51820 [wg0]  <- not in vpn.endpoints\n" +
		"  add any missing server IP to vpn.endpoints and drop stale entries.\n"
	got := captureStdout(t, func() { printDoctor(r) })
	if got != want {
		t.Errorf("printDoctor output mismatch\ngot:\n%q\nwant:\n%q", got, want)
	}
}

// A discover run that found nothing (or failed) carries its one line as
// Summary and NOTHING in Details — setting both printed it twice in the GUI's
// Diagnostics row. The CLI must still print it, at the same two-space indent as
// every sibling line: the pre-refactor code used `fmt.Println("  ", err)`,
// whose operand separator made the error branch three spaces, and that is a
// typo rather than a layout worth reproducing.
func TestPrintDoctorDegenerateDiscoverPrintsSummaryOnceAtTwoSpaces(t *testing.T) {
	for _, summary := range []string{
		"no physical-side public transport sockets found — is the VPN connected?",
		"endpoint discovery is macOS-only",
	} {
		r := doctorReport{
			Checks: []doctorCheck{
				{Name: "config", Status: checkOK, Summary: "OK (loaded and validated)"},
				{Name: "discover", Status: checkWarn, Summary: summary},
			},
		}
		out := captureStdout(t, func() { printDoctor(r) })
		want := "discover (best-effort, macOS):\n  " + summary + "\n"
		if !strings.HasSuffix(out, want) {
			t.Errorf("discover block = %q, want it to end with %q", out, want)
		}
		if strings.Count(out, summary) != 1 {
			t.Errorf("summary %q printed %d times, want exactly 1", summary, strings.Count(out, summary))
		}
	}
}

// printDoctor renders a fixed layout keyed by check name, so two checks sharing
// a Name — or one whose name has no section — used to silently vanish. A
// dropped check reads exactly like a check that passed, which is the worst way
// for a diagnostic to be wrong, so both leftovers must reach stdout.
func TestPrintDoctorDropsNoCheck(t *testing.T) {
	r := doctorReport{
		Checks: []doctorCheck{
			{Name: "config", Status: checkOK, Summary: "first"},
			{Name: "config", Status: checkFail, Summary: "second, same name"},
			{Name: "tunnels", Status: checkOK, Details: []string{"utun4"}},
			{Name: "endpoints", Status: checkOK, Details: []string{"1.2.3.4"}},
			{Name: "brandNew", Status: checkWarn, Summary: "a check with no section yet",
				Fixes: []string{"do the thing"}},
		},
	}
	got := captureStdout(t, func() { printDoctor(r) })
	for _, want := range []string{
		"config:  first",                        // first wins the named section
		"config: second, same name",             // the duplicate still prints
		"brandNew: a check with no section yet", // an unknown name still prints
		"do the thing",                          // and keeps its fixes
	} {
		if !strings.Contains(got, want) {
			t.Errorf("printDoctor output is missing %q\ngot:\n%s", want, got)
		}
	}
}

// The shipped checks must not collide in the first place — the leftover printer
// above is a safety net, not the intended layout. Names are the report's
// machine-readable identity (the macOS Diagnostics pane maps them to titles), so
// a duplicate is a bug at the source.
func TestDoctorChecksHaveUniqueNames(t *testing.T) {
	cfg := config.Default()
	cfg.VPN.TunnelInterfaces = []string{"utun4"}
	cfg.VPN.Endpoints = []string{"198.51.100.7"}
	config.Normalize(&cfg)

	r := runDoctor(&cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	seen := map[string]bool{}
	for _, c := range r.Checks {
		if seen[c.Name] {
			t.Errorf("two doctor checks share the name %q", c.Name)
		}
		seen[c.Name] = true
	}
}
