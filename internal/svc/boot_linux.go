//go:build linux

package svc

import "os"

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

func platformBoot() BootUnit {
	// kardianos also supports upstart and sysvinit, whose unit layouts this
	// package does not read. Rather than reporting "no unit found" on such a
	// host — a false negative that would tell a correctly-installed user to
	// reinstall — say the question cannot be answered here.
	if _, err := os.Stat(systemdRunDir); err != nil {
		return BootUnit{}
	}
	u := BootUnit{Path: systemdUnitPath, Determinable: true}
	if _, err := os.Stat(systemdUnitPath); err != nil {
		return u
	}
	u.Present = true
	// Lstat, not Stat: the wants entry is a symlink into /etc/systemd/system,
	// and a dangling one (unit removed, enablement left behind) must read as
	// enabled-but-broken rather than as absent — the unit check above is what
	// reports the missing target.
	if _, err := os.Lstat(systemdWantsPath); err == nil {
		u.AtBoot = true
	}
	return u
}
