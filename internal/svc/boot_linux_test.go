//go:build linux

package svc

import (
	"os"
	"path/filepath"
	"testing"
)

// systemd has to be the running init for platformBoot to answer at all, and
// these tests are about what it answers once it does. Everything else about the
// host is supplied as paths.
func requireSystemd(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(systemdRunDir); err != nil {
		t.Skip("systemd is not the running init here; platformBoot correctly declines to answer")
	}
}

// unreadableDir returns a directory whose contents cannot be stat'd, or skips.
// Running as root ignores the mode bits entirely, which would make every
// assertion below vacuous rather than wrong — so say so and stop.
func unreadableDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	inner := filepath.Join(dir, "locked")
	if err := os.Mkdir(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(inner, "probe")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(inner, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(inner, 0o700) })
	if _, err := os.Stat(probe); err == nil {
		t.Skip("running with privileges that ignore directory modes; nothing to test")
	}
	return inner
}

// "Could not read it" must never be published as "it is not there". A unit the
// installer wrote that this process cannot stat — a permission problem on
// /etc/systemd/system — must not read as Present:false, which buildServiceCheck
// renders as "not registered to start at boot": a user whose guard is installed
// and enforcing, told to reinstall it. Only fs.ErrNotExist may mean absent.
func TestUnreadableUnitIsUndeterminableNotAbsent(t *testing.T) {
	requireSystemd(t)
	locked := unreadableDir(t)

	u := bootFrom(filepath.Join(locked, "dezhban.service"), filepath.Join(locked, "wants.service"))
	if u.Determinable {
		t.Error("an unreadable unit reported a determinable answer; " +
			"a stat failure that is not ErrNotExist is not evidence of anything")
	}
	if u.Present {
		t.Error("an unreadable unit reported Present; it was never read")
	}

	// The contrast that gives the above its meaning: genuinely absent stays
	// determinable, so `doctor` can still say "not registered" when it is true.
	dir := t.TempDir()
	a := bootFrom(filepath.Join(dir, "nope.service"), filepath.Join(dir, "nope.wants"))
	if !a.Determinable || a.Present {
		t.Errorf("an absent unit must stay determinable and not present, got %+v", a)
	}
}

// The same split on the enablement symlink, where it matters more: a unit that
// is present but whose wants entry cannot be read would otherwise report
// "installed, but not set to start at boot" for a service that IS enabled,
// sending the user to fix something that is not broken.
func TestUnreadableWantsLinkIsUndeterminableNotDisabled(t *testing.T) {
	requireSystemd(t)
	locked := unreadableDir(t)

	dir := t.TempDir()
	unit := filepath.Join(dir, "dezhban.service")
	if err := os.WriteFile(unit, []byte("[Service]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	u := bootFrom(unit, filepath.Join(locked, "dezhban.service"))
	if u.Determinable {
		t.Error("an unreadable wants link reported a determinable answer; " +
			"reporting AtBoot:false here tells an enabled host to enable itself")
	}
	if u.AtBoot {
		t.Error("an unreadable wants link reported AtBoot; it was never read")
	}

	// Absent wants entry, readable: that is a real answer — installed but not
	// enabled, which is the quiet failure this check exists to name.
	d := bootFrom(unit, filepath.Join(dir, "absent.service"))
	if !d.Determinable || !d.Present || d.AtBoot {
		t.Errorf("a present unit with no enablement must read as present-not-at-boot, got %+v", d)
	}

	// And the ordinary healthy shape still reads as enabled.
	wants := filepath.Join(dir, "wants.service")
	if err := os.Symlink(unit, wants); err != nil {
		t.Fatal(err)
	}
	if e := bootFrom(unit, wants); !e.Determinable || !e.Present || !e.AtBoot {
		t.Errorf("an installed and enabled unit must read as at-boot, got %+v", e)
	}
}
