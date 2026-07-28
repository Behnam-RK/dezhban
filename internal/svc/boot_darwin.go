//go:build darwin

package svc

import (
	"errors"
	"io/fs"
	"os"
	"regexp"
)

// runAtLoad matches launchd's RunAtLoad key followed by its boolean value. The
// plist is XML, so the value is the next element after the key — `\s*` is what
// lets the two sit on separate lines, which is exactly how kardianos renders
// them. (Not `(?s)`: that flag only changes what `.` matches, and there is no
// `.` in this pattern. It was here and did nothing.)
//
// A real XML plist parser would be the pedantic choice, but it would also mean
// hand-rolling one (the stdlib has no plist decoder) for a file this package
// itself wrote from a fixed template. The regex reads the template correctly and
// fails toward "not at boot" on anything it does not recognise, which is the
// safe direction: it can prompt an unnecessary `install`, never hide a host that
// silently comes up unguarded.
var runAtLoad = regexp.MustCompile(`<key>RunAtLoad</key>\s*<(true|false)/>`)

func platformBoot() BootUnit { return bootFrom(plistPath) }

// bootFrom is platformBoot against a named path, so the read-failure rules below
// are testable. platformBoot reads one fixed system location, and on a host that
// simply has no plist there — every CI runner, most dev machines — only the
// absent branch is ever reached; the branch that matters most would go
// unexercised precisely because it needs a file the test cannot arrange in
// /Library/LaunchDaemons.
func bootFrom(path string) BootUnit {
	u := BootUnit{Path: path, Determinable: true}
	data, err := os.ReadFile(path)
	if err != nil {
		// "Absent" is the only read failure that means what it looks like. Any
		// other error — a permission problem on the plist or on
		// /Library/LaunchDaemons — means the file could not be READ, which is
		// not evidence it is missing. Reporting it as missing would tell a user
		// whose boot service is installed and enforcing to reinstall it, and
		// that is precisely the false negative this package's doc comment says
		// Boot exists to avoid: it is the same mistake as trusting
		// `launchctl list`, arrived at from the other side.
		if !errors.Is(err, fs.ErrNotExist) {
			u.Determinable = false
		}
		return u
	}
	u.Present = true
	if m := runAtLoad.FindSubmatch(data); m != nil {
		u.AtBoot = string(m[1]) == "true"
	}
	return u
}
