//go:build darwin

package svc

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/kardianos/service"

	"github.com/behnam-rk/dezhban/internal/privilege"
)

// Domain-explicit launchd control.
//
// kardianos/service drives launchd with the legacy `launchctl load`/`unload`/
// `list` subcommands, which infer the target domain from the calling SESSION,
// not the uid. A root process inside a GUI login session — exactly what
// AppleScript's "do shell script … with administrator privileges" produces,
// i.e. the menubar app's elevation path — maps to the per-user domain: loading
// /Library/LaunchDaemons/dezhban.plist fails with "Expecting a LaunchAgents
// path … Load failed: 5", and `launchctl list` cannot see the system-domain
// job at all, so Running() reports a live daemon as stopped and the
// idempotence guards in serviceAction misfire. The modern subcommands name the
// domain explicitly, so they behave identically under a terminal sudo and
// under the GUI's elevation context.

// plistPath is where kardianos installs the system service unit.
const plistPath = "/Library/LaunchDaemons/" + Name + ".plist"

// pidLine matches the live-process line in `launchctl print` output; its
// presence is what "running" means (same semantics as kardianos's PID check).
var pidLine = regexp.MustCompile(`(?m)^\s*pid = \d+`)

func launchctl(args ...string) error {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// platformControl overrides start/stop with domain-explicit calls. Install and
// uninstall stay with kardianos (it owns rendering/removing the plist), except
// that uninstall gets a best-effort bootout first: kardianos stops via the
// legacy path and ignores its error, which from a GUI session would remove the
// plist while leaving the job resident until reboot.
func platformControl(action string) (handled bool, err error) {
	switch action {
	case "start":
		return true, launchctl("bootstrap", "system", plistPath)
	case "stop":
		return true, launchctl("bootout", "system/"+Name)
	case "uninstall":
		_ = launchctl("bootout", "system/"+Name)
	}
	return false, nil
}

// platformStatus queries the system domain directly — but only as root.
// Unprivileged callers cannot see the system domain on modern macOS:
// `launchctl print system/<label>` answers "Could not find service" even for
// a loaded, running job, indistinguishable from truly absent. Root is also
// the only case that matters for correctness: every control-flow guard
// (serviceAction's idempotence checks, waitUntilStopped) runs after
// requireRoot, and the GUI's batch is elevated.
func platformStatus() (service.Status, error) {
	if !privilege.IsPrivileged() {
		return kardianosStatus()
	}
	out, err := exec.Command("launchctl", "print", "system/"+Name).CombinedOutput()
	if err == nil {
		if pidLine.Match(out) {
			return service.StatusRunning, nil
		}
		return service.StatusStopped, nil
	}
	if strings.Contains(string(out), "Could not find service") {
		if _, statErr := os.Stat(plistPath); statErr == nil {
			return service.StatusStopped, nil
		}
		return service.StatusUnknown, service.ErrNotInstalled
	}
	// A context where even a single-service print is not permitted — fall back
	// to kardianos's legacy query rather than guessing.
	return kardianosStatus()
}
