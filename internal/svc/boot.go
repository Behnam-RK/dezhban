package svc

// Boot inspection: will this host start dezhban on its own after a reboot?
//
// This deliberately does NOT ask the service manager. Status()/Installed() query
// it, and on macOS an unprivileged caller cannot see the system domain at all —
// platformStatus falls back to kardianos's legacy `launchctl list`, which
// answers "not installed" for a job that is loaded and running (see the comment
// atop launchd_darwin.go). `dezhban doctor` is root-free by contract, so a check
// built on Status() would tell an ordinary user their boot service is missing
// while it is enforcing, which is worse than saying nothing.
//
// What IS readable without privilege is the unit file the installer wrote. That
// file is also the more direct answer to the question actually being asked: the
// service manager's *current* status says what is running now, whereas the unit
// says what will happen at the next boot. They are different questions, and
// "will the guard be there after I reboot" is the one ADR-0008's arm-at-boot
// behavior depends on.

// BootUnit is what the OS service manager has on disk for dezhban, read from the
// unit file rather than from the manager.
type BootUnit struct {
	// Path is where the unit lives. Empty when the platform has no unit file
	// this package knows how to find.
	Path string
	// Present reports that the unit file exists.
	Present bool
	// AtBoot reports that the unit is configured to start dezhban at boot —
	// launchd's RunAtLoad, systemd's enablement symlink. A unit can exist and
	// still not do this, which is the quiet failure worth naming: `start` works,
	// every reboot comes up unguarded.
	AtBoot bool
	// Determinable is false when this platform offers no root-free way to tell,
	// so a caller reports "cannot say" instead of reporting a false negative.
	// Every other field is meaningless when this is false.
	Determinable bool
}

// Boot reports how dezhban is registered to start at boot. It performs only
// unprivileged reads and never contacts the service manager, so it is safe from
// `doctor`, `status`, and the macOS app alike.
func Boot() BootUnit { return platformBoot() }
