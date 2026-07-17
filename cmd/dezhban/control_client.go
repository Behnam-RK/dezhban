package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/behnam-rk/dezhban/internal/config"
	"github.com/behnam-rk/dezhban/internal/control"
)

// ExitDaemonRefused is the exit code for "the daemon was reached and said no".
// It is distinct from a generic failure (1) so a caller — notably the menubar app —
// can tell a refusal from an unreachable daemon. A refusal must NOT be retried with
// elevated rights: the daemon's gating (an open switch window, allowSwitchOps=false)
// is a decision, not an obstacle, and re-running as root would bypass it.
const ExitDaemonRefused = 3

// verbosef prints a diagnostic only under -v/--verbose. The control fast path
// falls back silently by design (a stopped daemon is a normal state, not an
// error), so its reasons are diagnostics, not warnings.
func verbosef(format string, args ...any) {
	if verbose {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
}

// controlSocketPath resolves the control socket: the configured path, else
// <state dir>/control.sock (alongside state.json and command.json, so one root-
// owned directory holds everything the daemon owns and `uninstall` purges it all).
func controlSocketPath(cfg *config.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.Control.Socket) != "" {
		return cfg.Control.Socket
	}
	return filepath.Join(stateDir(), "control.sock")
}

// noDaemonFlag is the global --no-daemon flag, stripped from args before dispatch
// by stripVerbose (so it works before or after the subcommand, exactly like
// --no-sudo, and no per-command FlagSet has to know about it).
var noDaemonFlag bool

// noDaemon reports whether the socket fast path is suppressed. It mirrors
// --no-sudo/DEZHBAN_NO_SUDO: an escape hatch for "the daemon is wedged, act on the
// firewall directly", and what tests use to force the root path.
func noDaemon() bool {
	if noDaemonFlag {
		return true
	}
	if v := os.Getenv("DEZHBAN_NO_DAEMON"); v != "" {
		// Truthy disables; any unparseable-but-set value also counts as "disable"
		// (same rule as sudoDisabled, so the two flags behave identically).
		b, err := strconv.ParseBool(v)
		return err != nil || b
	}
	return false
}

// controlStatus is the human-readable answer to "will routine ops ask me for a
// password?" — the whole point of the socket, so `status` says it plainly.
func controlStatus(cfg *config.Config) string {
	if !cfg.Control.Enabled {
		return "disabled (control.enabled=false) — routine ops need sudo"
	}
	path := controlSocketPath(cfg)
	// Do, not Ping: Ping collapses every failure into false, which would report the
	// daemon as "not running" to the one user who most needs a real answer — a caller
	// outside control.group, for whom the socket is right there and simply not
	// openable. ErrForbidden is modeled precisely so this case can be named.
	resp, err := control.Do(path, control.Request{Op: control.OpPing})
	switch {
	case errors.Is(err, control.ErrForbidden):
		return fmt.Sprintf("forbidden (%s) — socket exists but you are not in the %q group; routine ops need sudo", path, cfg.Control.Group)
	case err != nil || !resp.OK:
		return fmt.Sprintf("unreachable (%s) — daemon not running; routine ops need sudo", path)
	}
	s := fmt.Sprintf("reachable (%s, group %q) — routine ops need no password", path, cfg.Control.Group)
	if !cfg.Control.AllowSwitchOps {
		s += "; switch ops need sudo (control.allowSwitchOps=false)"
	}
	return s
}

// tryControl attempts an op over the daemon's control socket — the passwordless
// path. It reports handled=true when the daemon answered (whether it accepted or
// deliberately refused): a refusal IS the answer, and must NOT be retried by
// escalating to root, or the daemon's own gating (an open switch window, disabled
// switch ops) would be trivially bypassable.
//
// handled=false means no daemon was reachable, so the caller falls back to its
// existing root path. That is what keeps the CLI working with the daemon stopped.
func tryControl(cfgPath string, req control.Request) (code int, handled bool) {
	// loadConfig, NOT config.Load: the raw flag is usually empty, and only the
	// resolver applies $DEZHBAN_CONFIG → the canonical system path. Loading the raw
	// value would read built-in defaults instead of the user's real config, so a
	// customized control.socket/group would be missed and the password prompt this
	// whole path exists to remove would quietly come back.
	cfg, err := loadConfig(cfgPath)
	if err != nil || !cfg.Control.Enabled {
		return 0, false
	}
	path := controlSocketPath(cfg)

	resp, err := control.Do(path, req)
	if err != nil {
		switch {
		case errors.Is(err, control.ErrForbidden):
			// The socket is there but this user can't open it. Say so — silently
			// falling back to a password prompt would leave them wondering why the
			// passwordless path they configured isn't working.
			fmt.Fprintf(os.Stderr, "control socket: permission denied (you are not in the %q group; falling back to sudo)\n", cfg.Control.Group)
		case errors.Is(err, control.ErrUnavailable):
			verbosef("control socket unavailable (%s) — falling back to direct firewall action", path)
		default:
			verbosef("control socket error: %v — falling back to direct firewall action", err)
		}
		return 0, false
	}
	if !resp.OK {
		if resp.Transient {
			// Not a decision — the server couldn't get the request to the run
			// loop (busy with an inline probe, shutting down). Fall back to the
			// root command-file path like an unreachable daemon: that file is
			// durable, so the loop honors it on its next poll no matter how
			// busy it is now. Reporting a refusal here would be wrong twice:
			// it's not one, and callers (the menubar app) rightly never
			// escalate refusals — the request would just die.
			verbosef("control socket: %s — falling back to direct/root path", resp.Error)
			return 0, false
		}
		fmt.Fprintln(os.Stderr, "daemon refused:", resp.Error)
		return ExitDaemonRefused, true
	}
	return 0, true
}
