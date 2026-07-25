package main

import (
	"net/netip"
	"testing"

	"github.com/behnam-rk/dezhban/internal/config"
	"github.com/behnam-rk/dezhban/internal/netdetect"
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
