//go:build darwin

package svc

import (
	"os"
	"path/filepath"
	"testing"
)

// platformBoot reads a fixed system path, so the part worth pinning is the
// parse: whether a launchd plist is read as "starts at boot" or not. Getting
// this backwards would either nag a correctly-installed user forever or, worse,
// stay quiet about a host that comes up unguarded after every reboot.
func TestRunAtLoadParse(t *testing.T) {
	// The shape kardianos renders, keys and values on separate lines.
	const enabled = `<?xml version="1.0" encoding="UTF-8"?>
<plist version='1.0'>
<dict>
    <key>Label</key><string>dezhban</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>`

	const disabled = `<?xml version="1.0" encoding="UTF-8"?>
<plist version='1.0'>
<dict>
    <key>Label</key><string>dezhban</string>
    <key>RunAtLoad</key>
    <false/>
</dict>
</plist>`

	// A hand-edited plist that dropped the key entirely. No match must read as
	// "not at boot" — the safe direction, since it can only prompt a needless
	// reinstall, never hide an unguarded boot.
	const absent = `<?xml version="1.0" encoding="UTF-8"?>
<plist version='1.0'>
<dict><key>Label</key><string>dezhban</string></dict>
</plist>`

	for _, tc := range []struct {
		name string
		data string
		want bool
	}{
		{"RunAtLoad true", enabled, true},
		{"RunAtLoad false", disabled, false},
		{"key absent", absent, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := false
			if m := runAtLoad.FindStringSubmatch(tc.data); m != nil {
				got = m[1] == "true"
			}
			if got != tc.want {
				t.Errorf("at boot = %v, want %v", got, tc.want)
			}
		})
	}
}

// Boot must never claim a platform answer it did not get. Determinable is the
// field every caller gates on, so it has to be set on the path that succeeds.
func TestBootIsDeterminableOnDarwin(t *testing.T) {
	if u := Boot(); !u.Determinable {
		t.Error("darwin reports the boot unit as undeterminable; the plist path is readable without root")
	}
}

// "Could not read it" must never be published as "it is not there". A plist the
// installer wrote but this process cannot open — a permission problem on the
// file or on /Library/LaunchDaemons — used to read as Present:false, which
// buildServiceCheck renders as "not registered to start at boot": a user whose
// guard is installed and enforcing right now, told to reinstall it. That is the
// same false negative Boot exists to avoid, reached from the other side, so only
// fs.ErrNotExist may mean absent.
func TestUnreadablePlistIsUndeterminableNotAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.plist")
	if err := os.WriteFile(path, []byte("<plist><key>RunAtLoad</key><true/></plist>"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Unreadable by mode. Skip when the test runs as root, which ignores the
	// mode bits entirely and would read the file regardless.
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("running with privileges that ignore file modes; nothing to test")
	}

	u := bootFrom(path)
	if u.Determinable {
		t.Error("an unreadable plist reported a determinable answer; " +
			"a read failure that is not ErrNotExist is not evidence of anything")
	}
	if u.Present {
		t.Error("an unreadable plist reported Present; it was never read")
	}

	// The contrast that gives the above its meaning: genuinely absent stays
	// determinable, so `doctor` can still say "not registered" when it is true.
	if a := bootFrom(filepath.Join(dir, "nope.plist")); !a.Determinable || a.Present {
		t.Errorf("an absent plist must stay determinable and not present, got %+v", a)
	}
}
