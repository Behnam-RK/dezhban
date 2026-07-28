package main

import (
	"errors"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/armed"
	"github.com/behnam-rk/dezhban/internal/config"
	"github.com/behnam-rk/dezhban/internal/learned"
	"github.com/behnam-rk/dezhban/internal/svc"
)

// The three "will dezhban need me again" checks. Each takes already-loaded
// values, so the diagnosis is testable without a service manager, an armed.json,
// or a learned.json existing — which matters because the interesting cases are
// the ones a developer's machine is least likely to be in.

func hasFix(c doctorCheck, substr string) bool {
	return slices.ContainsFunc(c.Fixes, func(f string) bool { return strings.Contains(f, substr) })
}

func detailText(c doctorCheck) string { return strings.Join(c.Details, "\n") }

func TestBuildServiceCheck(t *testing.T) {
	const path = "/Library/LaunchDaemons/dezhban.plist"

	t.Run("platform cannot answer", func(t *testing.T) {
		c := buildServiceCheck(svc.BootUnit{}, false)
		if c.Status != checkWarn {
			t.Errorf("status = %q, want %q", c.Status, checkWarn)
		}
		// The one thing it must never do is report a false negative — an
		// unanswerable question is not the same as "not installed", and telling
		// someone to reinstall a working service is worse than saying nothing.
		if strings.Contains(c.Summary, "not registered") {
			t.Errorf("an unanswerable platform reported as not installed: %q", c.Summary)
		}
		if !hasFix(c, "dezhban status") {
			t.Errorf("fixes = %v, want a pointer at the privileged query", c.Fixes)
		}
	})

	t.Run("no unit, daemon enforcing now", func(t *testing.T) {
		c := buildServiceCheck(svc.BootUnit{Path: path, Determinable: true}, true)
		if c.Status != checkWarn {
			t.Errorf("status = %q, want %q", c.Status, checkWarn)
		}
		if !hasFix(c, "dezhban install") {
			t.Errorf("fixes = %v, want install", c.Fixes)
		}
		// Separating "nothing will start at boot" from "nothing is running" is
		// the entire point of the check; a live daemon must not read as a
		// contradiction of the warning.
		if !strings.Contains(detailText(c), "enforcing right now") {
			t.Errorf("a live daemon was not distinguished from a missing one:\n%s", detailText(c))
		}
	})

	t.Run("unit present but not at boot", func(t *testing.T) {
		c := buildServiceCheck(svc.BootUnit{Path: path, Present: true, Determinable: true}, true)
		if c.Status != checkWarn {
			t.Errorf("status = %q, want %q", c.Status, checkWarn)
		}
		if !strings.Contains(detailText(c), "every reboot comes up unguarded") {
			t.Errorf("the consequence was not stated:\n%s", detailText(c))
		}
	})

	t.Run("at boot but nothing running", func(t *testing.T) {
		c := buildServiceCheck(svc.BootUnit{Path: path, Present: true, AtBoot: true, Determinable: true}, false)
		if c.Status != checkWarn {
			t.Errorf("status = %q, want %q", c.Status, checkWarn)
		}
		if !hasFix(c, "dezhban start") {
			t.Errorf("fixes = %v, want start", c.Fixes)
		}
	})

	t.Run("healthy", func(t *testing.T) {
		c := buildServiceCheck(svc.BootUnit{Path: path, Present: true, AtBoot: true, Determinable: true}, true)
		if c.Status != checkOK {
			t.Errorf("status = %q, want %q", c.Status, checkOK)
		}
		// A healthy boot service is the answer to "why must I turn it on after
		// every reboot" — it rules enforcement out and leaves the login item,
		// so it has to say so rather than passing silently.
		if !strings.Contains(detailText(c), "login-item") {
			t.Errorf("a healthy service did not rule out the perception case:\n%s", detailText(c))
		}
	})
}

func TestBuildArmAtBootCheck(t *testing.T) {
	const path = "/var/db/dezhban/armed.json"
	everUp := &armed.Record{TunnelEverUp: true, FirstUp: time.Now().Add(-72 * time.Hour), LastUp: time.Now()}

	t.Run("record unreadable", func(t *testing.T) {
		c := buildArmAtBootCheck(true, true, &armed.Record{}, errors.New("armed: parse: unexpected EOF"), path)
		if c.Status != checkWarn {
			t.Errorf("status = %q, want %q", c.Status, checkWarn)
		}
		if !strings.Contains(detailText(c), "unexpected EOF") {
			t.Errorf("the underlying error was swallowed:\n%s", detailText(c))
		}
	})

	// A corrupt record outranks the config setting: arm-at-boot is off in
	// practice either way, but only one of the two has a fix the user can act on
	// and the other would send them to change a setting that is already right.
	t.Run("record unreadable outranks the setting", func(t *testing.T) {
		c := buildArmAtBootCheck(false, true, &armed.Record{}, errors.New("boom"), path)
		if hasFix(c, "armAtBoot=true") {
			t.Errorf("an unreadable record was reported as a config problem: %v", c.Fixes)
		}
	})

	t.Run("turned off", func(t *testing.T) {
		c := buildArmAtBootCheck(false, true, everUp, nil, path)
		if c.Status != checkWarn {
			t.Errorf("status = %q, want %q", c.Status, checkWarn)
		}
		if !hasFix(c, "vpn.armAtBoot=true") {
			t.Errorf("fixes = %v", c.Fixes)
		}
	})

	t.Run("on, but no tunnel ever observed", func(t *testing.T) {
		c := buildArmAtBootCheck(true, true, &armed.Record{}, nil, path)
		if c.Status != checkWarn {
			t.Errorf("status = %q, want %q", c.Status, checkWarn)
		}
		// The precondition is the half that fails silently — the setting reads
		// "on" the whole time — so the check has to name what would satisfy it.
		if !strings.Contains(detailText(c), "Connect your VPN once") {
			t.Errorf("no route to satisfying the precondition:\n%s", detailText(c))
		}
	})

	t.Run("on, no tunnel configured either", func(t *testing.T) {
		c := buildArmAtBootCheck(true, false, &armed.Record{}, nil, path)
		if !strings.Contains(detailText(c), "Configure a tunnel first") {
			t.Errorf("advice assumed a tunnel that is not configured:\n%s", detailText(c))
		}
	})

	t.Run("armed", func(t *testing.T) {
		c := buildArmAtBootCheck(true, true, everUp, nil, path)
		if c.Status != checkOK {
			t.Errorf("status = %q, want %q", c.Status, checkOK)
		}
		if len(c.Fixes) != 0 {
			t.Errorf("a healthy check offered fixes: %v", c.Fixes)
		}
	})
}

func TestBuildEndpointRetentionCheck(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	const ttl = 24 * time.Hour
	const maxPer = 8

	entry := func(name string, eps ...learned.Endpoint) learned.Entry {
		return learned.Entry{Name: name, Endpoints: eps}
	}
	// seen is an endpoint first met `first` ago and last used `last` ago.
	seen := func(addr string, first, last time.Duration) learned.Endpoint {
		return learned.Endpoint{Addr: addr, FirstSeen: now.Add(-first), LastSeen: now.Add(-last)}
	}

	t.Run("store unreadable", func(t *testing.T) {
		c := buildEndpointRetentionCheck(&learned.Store{}, errors.New("learned: parse: bad json"), ttl, maxPer, 0, now)
		if c.Status != checkWarn {
			t.Errorf("status = %q, want %q", c.Status, checkWarn)
		}
	})

	// Nothing learned is only a problem when nothing is configured either. With
	// a static endpoint the guard already passes the server and a drop redials
	// unaided, which is the good outcome, not a gap.
	t.Run("empty but statically configured", func(t *testing.T) {
		c := buildEndpointRetentionCheck(&learned.Store{}, nil, ttl, maxPer, 1, now)
		if c.Status != checkOK {
			t.Errorf("status = %q, want %q", c.Status, checkOK)
		}
	})

	t.Run("empty and nothing configured", func(t *testing.T) {
		c := buildEndpointRetentionCheck(&learned.Store{}, nil, ttl, maxPer, 0, now)
		if c.Status != checkWarn {
			t.Errorf("status = %q, want %q", c.Status, checkWarn)
		}
		if !hasFix(c, "--endpoint") {
			t.Errorf("fixes = %v", c.Fixes)
		}
	})

	t.Run("everything aged out", func(t *testing.T) {
		s := &learned.Store{Entries: []learned.Entry{
			entry("work", seen("198.51.100.7", 90*24*time.Hour, 60*24*time.Hour)),
		}}
		c := buildEndpointRetentionCheck(s, nil, ttl, maxPer, 0, now)
		if c.Status != checkWarn {
			t.Errorf("status = %q, want %q", c.Status, checkWarn)
		}
		if !hasFix(c, "learnedEndpointTTL") {
			t.Errorf("fixes = %v, want the retention knob", c.Fixes)
		}
		// The two diagnoses point opposite ways — retain longer vs stop trying
		// to retain — so confusing one for the other sends the user to the
		// wrong knob entirely.
		if hasFix(c, "learnedMaxPerProfile") {
			t.Errorf("aged-out endpoints were diagnosed as rotation: %v", c.Fixes)
		}
	})

	t.Run("rotating server address", func(t *testing.T) {
		// A full store whose entries were nearly all met for the first time
		// inside the retention window: dezhban is learning addresses, not
		// reusing them.
		var eps []learned.Endpoint
		for i := range maxPer {
			eps = append(eps, seen(
				rotatedAddr(i), time.Duration(i+1)*time.Hour, time.Duration(i)*time.Minute))
		}
		s := &learned.Store{Entries: []learned.Entry{entry("rotator", eps...)}}
		c := buildEndpointRetentionCheck(s, nil, ttl, maxPer, 0, now)
		if c.Status != checkWarn {
			t.Errorf("status = %q, want %q", c.Status, checkWarn)
		}
		if !strings.Contains(c.Summary, "rotates") {
			t.Errorf("summary = %q, want the rotation diagnosis", c.Summary)
		}
		// A hostname re-resolves on vpn.endpointRefresh and follows the
		// rotation; raising the cap only stores more addresses that will not be
		// used again. The hostname must therefore lead.
		if len(c.Fixes) == 0 || !strings.Contains(c.Fixes[0], "hostname") {
			t.Errorf("fixes = %v, want the hostname advice first", c.Fixes)
		}
	})

	t.Run("healthy retention", func(t *testing.T) {
		s := &learned.Store{Entries: []learned.Entry{
			entry("work", seen("198.51.100.7", 90*24*time.Hour, time.Minute)),
		}}
		c := buildEndpointRetentionCheck(s, nil, ttl, maxPer, 0, now)
		if c.Status != checkOK {
			t.Errorf("status = %q, want %q\nsummary: %s", c.Status, checkOK, c.Summary)
		}
	})
}

// rotatedAddr makes distinct addresses for the rotation fixture. The check only
// ever counts and groups them, so they need to differ, not to be routable.
func rotatedAddr(i int) string { return "198.51.100." + strconv.Itoa(i+1) }

// printDoctor keys a fixed layout by check name and appends anything it does not
// recognise, unformatted, after the last section. That fallback exists so a
// finding can never vanish — but it is a safety net, not the layout, and a check
// that lands in it has no considered place in the report. Pin that runDoctor
// never emits one.
func TestEveryCheckHasASection(t *testing.T) {
	cfg := config.Default()
	cfg.VPN.TunnelInterfaces = []string{"utun4"}
	cfg.VPN.Endpoints = []string{"198.51.100.7"}
	config.Normalize(&cfg)

	r := runDoctor(&cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	if len(r.Checks) == 0 {
		t.Fatal("runDoctor produced no checks")
	}
	for _, c := range r.Checks {
		if !slices.Contains(sectionedChecks, c.Name) {
			t.Errorf("check %q has no section in printDoctor; add one and list it in sectionedChecks", c.Name)
		}
	}
}
