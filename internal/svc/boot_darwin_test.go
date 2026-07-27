//go:build darwin

package svc

import "testing"

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
