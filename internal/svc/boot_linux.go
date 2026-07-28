//go:build linux

package svc

import (
	"errors"
	"io/fs"
	"os"
)

// systemd paths. kardianos renders the unit into /etc/systemd/system and then
// runs `systemctl enable`, which is what creates the wants symlink — so the
// symlink, not the unit, is what "starts at boot" means here. A unit present
// without it is the systemd shape of the same quiet failure launchd expresses as
// RunAtLoad=false: `start` works, every reboot comes up unguarded.
const (
	systemdUnitPath  = "/etc/systemd/system/" + Name + ".service"
	systemdWantsPath = "/etc/systemd/system/multi-user.target.wants/" + Name + ".service"
	// systemdRunDir exists only when systemd is the running init. It is the
	// documented way to ask that question without executing anything.
	systemdRunDir = "/run/systemd/system"
)

func platformBoot() BootUnit { return bootFrom(systemdUnitPath, systemdWantsPath) }

// bootFrom is platformBoot against named paths, so the read-failure rules below
// are testable. Same reason the darwin file splits it out: platformBoot reads
// fixed system locations, and on a host that simply has no unit there — every CI
// runner, most dev machines — only the absent branch is ever reached, leaving the
// branch that matters most unexercised.
func bootFrom(unitPath, wantsPath string) BootUnit {
	// kardianos also supports upstart and sysvinit, whose unit layouts this
	// package does not read. Rather than reporting "no unit found" on such a
	// host — a false negative that would tell a correctly-installed user to
	// reinstall — say the question cannot be answered here.
	if _, err := os.Stat(systemdRunDir); err != nil {
		return BootUnit{}
	}
	u := BootUnit{Path: unitPath, Determinable: true}
	// "Absent" is the only read failure that means what it looks like. Any other
	// error — a permission problem on the unit or on /etc/systemd/system — means
	// the file could not be READ, which is not evidence it is missing. Reporting
	// it as missing would tell a user whose boot service is installed and
	// enforcing to reinstall it, which is the false negative this package's doc
	// comment says Boot exists to avoid. Same rule as boot_darwin.go; a rule
	// applied on one platform and not the other is how it comes back.
	if _, err := os.Stat(unitPath); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			u.Determinable = false
		}
		return u
	}
	u.Present = true
	// Lstat, not Stat: the wants entry is a symlink into /etc/systemd/system,
	// and a dangling one (unit removed, enablement left behind) must read as
	// enabled-but-broken rather than as absent — the unit check above is what
	// reports the missing target.
	//
	// The same absent-versus-unreadable split applies, and it matters MORE here:
	// a unit that is present but whose enablement cannot be read would otherwise
	// report "installed, but not set to start at boot" for a service that is
	// enabled, sending the user to fix something that is not broken.
	if _, err := os.Lstat(wantsPath); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			u.Determinable = false
		}
		return u
	}
	u.AtBoot = true
	return u
}
