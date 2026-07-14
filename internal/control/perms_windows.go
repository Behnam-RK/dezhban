package control

import (
	"fmt"
	"net"
	"os"
)

// checkDirSecure is a no-op on Windows, for the same reason secureSocket is: there
// is no unix mode/ownership model to judge the directory by, and the socket lives
// under ProgramData whose ACL already restricts writers to administrators.
func checkDirSecure(dir string) error { return nil }

// listenSecure binds the socket directly: with no chown/mode boundary to establish
// (see secureSocket), there is no permission window to close, so the unix side's
// stage-then-rename dance would buy nothing — and renaming a bound AF_UNIX socket
// is not something Windows supports. A socket left behind by a crash would fail the
// bind, so it is unlinked first.
func listenSecure(path, group string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("control: remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("control: listen %q: %w", path, err)
	}
	return ln, nil
}

// secureSocket is a no-op on Windows: there is no chown/mode model to apply, and
// the socket lives under the ProgramData directory whose ACL already restricts
// writers to administrators (the same assumption internal/command makes for the
// command file). The passwordless control path is not a supported Windows feature
// today — it is wired only because the daemon shares one run loop across OSes.
func secureSocket(path, group string) error { return nil }
