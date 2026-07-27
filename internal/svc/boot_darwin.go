//go:build darwin

package svc

import (
	"os"
	"regexp"
)

// runAtLoad matches launchd's RunAtLoad key followed by its boolean value. The
// plist is XML, so the value is the next element after the key — `(?s)` lets the
// two sit on separate lines, which is exactly how kardianos renders them.
//
// A real XML plist parser would be the pedantic choice, but it would also mean
// hand-rolling one (the stdlib has no plist decoder) for a file this package
// itself wrote from a fixed template. The regex reads the template correctly and
// fails toward "not at boot" on anything it does not recognise, which is the
// safe direction: it can prompt an unnecessary `install`, never hide a host that
// silently comes up unguarded.
var runAtLoad = regexp.MustCompile(`(?s)<key>RunAtLoad</key>\s*<(true|false)/>`)

func platformBoot() BootUnit {
	u := BootUnit{Path: plistPath, Determinable: true}
	data, err := os.ReadFile(plistPath)
	if err != nil {
		// Any read error other than "absent" (a permission problem on the
		// LaunchDaemons directory, say) is reported as absent rather than as a
		// separate state: the user-visible advice — run `dezhban install` — is
		// the same, and the check's Details name the path so an unusual failure
		// is still traceable.
		return u
	}
	u.Present = true
	if m := runAtLoad.FindSubmatch(data); m != nil {
		u.AtBoot = string(m[1]) == "true"
	}
	return u
}
