// Package svc runs dezhban as a managed background service on each OS, using one
// cross-platform API (github.com/kardianos/service) that maps to launchd on
// macOS, systemd/upstart/sysv on Linux, and the Windows Service manager.
//
// The service wraps the Phase 3 run loop (internal/runner). Start launches the
// loop in a goroutine and returns promptly (service managers require it); Stop
// cancels the loop's context and waits (bounded) for runner.Run's deferred
// Cleanup to remove every firewall rule — stopping the service must never leave
// a block-all rule behind that locks the operator out of their own network. If
// the loop ever ends on its own (startup refusal, run failure), the process
// exits rather than lingering as a zombie the manager still counts as running.
package svc

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/kardianos/service"

	"github.com/behnam-rk/dezhban/internal/logging"
	"github.com/behnam-rk/dezhban/internal/runner"
)

// Service identity registered with the OS service manager.
const (
	Name        = "dezhban"
	displayName = "dezhban network kill switch"
	description = "Cuts network egress when the machine's public IP resolves to a blocklisted country."
)

// Builder assembles the run-loop options. It is called once, inside Start, with
// the logger the service should use — which is the platform logger when running
// under a service manager. Returning an error aborts startup cleanly.
type Builder func(log *slog.Logger) (runner.Options, error)

// program implements service.Interface, wrapping the run loop.
type program struct {
	build  Builder
	log    *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}
}

// stopTimeout bounds Stop's wait for the run loop's teardown. Unbounded, a
// teardown wedged on a firewall call leaves a zombie: the run loop is gone but
// the process never exits, the service manager still counts it as running, so
// `start` no-ops and only a kill recovers (observed live — a daemon that
// published its final "stopped" snapshot and then lingered for hours). If this
// fires, dezhban rules may still be present; `dezhban panic` is the documented
// recovery for that, whereas a zombie has none.
const stopTimeout = 30 * time.Second

// Start builds the run loop and launches it in a goroutine, returning at once.
func (p *program) Start(service.Service) error {
	opts, err := p.build(p.log)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	go func() {
		defer close(p.done)
		err := runner.Run(ctx, opts)
		if err != nil {
			p.log.Error("run loop failed", "err", err)
		}
		if ctx.Err() != nil {
			return // Stop requested this exit; the manager finishes the shutdown
		}
		// The loop ended on its OWN — a startup refusal or a run failure (every
		// clean exit rides the context). service.Run has no path from here to a
		// process exit, so without this the process lingers as a zombie (see
		// stopTimeout). Exit instead: runner.Run's defers (Cleanup, the final
		// stopped snapshot) have already run, and a dead process is something
		// KeepAlive or a later `start` can actually respawn.
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}()
	return nil
}

// Stop cancels the run loop and waits (bounded by stopTimeout) for runner.Run's
// deferred Cleanup to remove all firewall rules, so a normal stop never lets the
// process exit with a block-all rule still live. On timeout it stops waiting and
// lets the exit proceed — see stopTimeout for why a zombie is the worse outcome.
func (p *program) Stop(service.Service) error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.done != nil {
		select {
		case <-p.done:
		case <-time.After(stopTimeout):
			p.log.Error("run loop teardown timed out — exiting without it; "+
				"dezhban firewall rules may still be present (recover with `dezhban panic`)",
				"timeout", stopTimeout)
		}
	}
	return nil
}

// serviceConfig builds the kardianos config. configPath is embedded as the run
// argument so the manager invokes `dezhban run --config <path>` on boot. Restart
// options are enabled so a crash re-enforces the kill switch.
func serviceConfig(configPath string) *service.Config {
	args := []string{"run"}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}
	return &service.Config{
		Name:        Name,
		DisplayName: displayName,
		Description: description,
		Arguments:   args,
		Option: service.KeyValue{
			"RunAtLoad":              true,      // launchd: start at boot
			"KeepAlive":              true,      // launchd: restart on crash
			"Restart":                "always",  // systemd: restart on exit
			"OnFailure":              "restart", // windows: restart on failure
			"OnFailureDelayDuration": "5s",
			"OnFailureResetPeriod":   10,
		},
	}
}

// Run runs dezhban under the service manager (or interactively, when launched
// from a shell). It selects the logger: under a service manager, slog is routed
// to the platform logger so the run loop's output reaches journald/syslog/Event
// Log instead of an unread stderr. level and configPath shape that logger and
// the boot invocation respectively; baseLog is used interactively.
func Run(build Builder, baseLog *slog.Logger, level, configPath string) error {
	prog := &program{build: build, log: baseLog}
	s, err := service.New(prog, serviceConfig(configPath))
	if err != nil {
		return err
	}
	if !service.Interactive() {
		if sl, err := s.Logger(nil); err == nil {
			prog.log = logging.NewService(level, sl)
		}
	}
	return s.Run()
}

// Status reports whether the service is installed and running, as a short string
// for `dezhban status`. Querying status may require privilege on some platforms;
// any error is surfaced rather than hidden.
func Status() string {
	s, err := service.New(&program{}, serviceConfig(""))
	if err != nil {
		return "unknown: " + err.Error()
	}
	st, err := s.Status()
	switch {
	case errors.Is(err, service.ErrNotInstalled):
		return "not installed"
	case err != nil:
		return "unknown: " + err.Error()
	case st == service.StatusRunning:
		return "installed, running"
	case st == service.StatusStopped:
		return "installed, stopped"
	default:
		return "installed"
	}
}

// Installed and Running report the service manager's view of the service. They are
// what makes start/stop idempotent: launchd's `launchctl load`/`unload` are edge
// operations, not level ones — unloading a job that was never loaded fails with a
// bare "Input/output error", and loading one twice fails too. Asking first turns
// both into no-ops, so `stop` on an already-stopped service means "you are in the
// state you asked for", not a failure that aborts whatever came next.
// Installed reports false ONLY for a definite "not installed" from the service
// manager. Any other error (a failed query, an unavailable launchctl) leaves this
// true on purpose: callers use it to refuse an action and send the user to
// `install`, and doing that to an already-installed service just walks them into
// kardianos's "Init already exists". An unknown status should let the real action
// run and fail with its own real error, not be guessed at here.
func Installed() bool {
	_, err := status()
	return !errors.Is(err, service.ErrNotInstalled)
}

func Running() bool {
	st, err := status()
	return err == nil && st == service.StatusRunning
}

// Loaded reports whether the service manager still holds the job at all,
// running or not. It exists for stop's idempotence guard: on launchd a
// KeepAlive job can be bootstrapped but parked ("spawn scheduled") between
// respawns of a crash-looping daemon — Running() is false, yet the job still
// needs its bootout or it will come back. On other platforms this is
// equivalent to Running().
func Loaded() bool {
	return platformLoaded()
}

// status defers to the platform override (darwin queries launchd's system
// domain explicitly; see launchd_darwin.go) with kardianos as the base case.
func status() (service.Status, error) {
	return platformStatus()
}

func kardianosStatus() (service.Status, error) {
	s, err := service.New(&program{}, serviceConfig(""))
	if err != nil {
		return service.StatusUnknown, err
	}
	return s.Status()
}

// Control performs an install/uninstall/start/stop (and other kardianos control
// actions) against the registered service. configPath is embedded so a freshly
// installed service knows which config to load on boot. Actions the platform
// override claims (darwin start/stop) never reach kardianos.
func Control(action, configPath string) error {
	if handled, err := platformControl(action); handled {
		return err
	}
	s, err := service.New(&program{}, serviceConfig(configPath))
	if err != nil {
		return err
	}
	return service.Control(s, action)
}
