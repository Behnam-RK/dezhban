// Package svc runs dezhban as a managed background service on each OS, using one
// cross-platform API (github.com/kardianos/service) that maps to launchd on
// macOS, systemd/upstart/sysv on Linux, and the Windows Service manager.
//
// The service wraps the Phase 3 run loop (internal/runner). Start launches the
// loop in a goroutine and returns promptly (service managers require it); Stop
// cancels the loop's context and waits for runner.Run's deferred Cleanup to
// remove every firewall rule — stopping the service must never leave a block-all
// rule behind that locks the operator out of their own network.
package svc

import (
	"context"
	"errors"
	"log/slog"

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
		if err := runner.Run(ctx, opts); err != nil {
			p.log.Error("run loop failed", "err", err)
		}
	}()
	return nil
}

// Stop cancels the run loop and blocks until it has finished — runner.Run's
// deferred Cleanup removes all firewall rules before this returns, so the
// service manager never sees the process exit with a block-all rule still live.
func (p *program) Stop(service.Service) error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.done != nil {
		<-p.done
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
func Installed() bool {
	_, err := status()
	return !errors.Is(err, service.ErrNotInstalled) && err == nil
}

func Running() bool {
	st, err := status()
	return err == nil && st == service.StatusRunning
}

func status() (service.Status, error) {
	s, err := service.New(&program{}, serviceConfig(""))
	if err != nil {
		return service.StatusUnknown, err
	}
	return s.Status()
}

// Control performs an install/uninstall/start/stop (and other kardianos control
// actions) against the registered service. configPath is embedded so a freshly
// installed service knows which config to load on boot.
func Control(action, configPath string) error {
	s, err := service.New(&program{}, serviceConfig(configPath))
	if err != nil {
		return err
	}
	return service.Control(s, action)
}
