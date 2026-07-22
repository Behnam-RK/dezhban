// Command dezhban is a cross-platform network kill switch: it watches the
// machine's public IP, resolves its country, and drives the OS firewall to cut
// traffic when the country matches a blocklist.
//
// Phase 0 wires the CLI skeleton, config, logging, and privilege checks. The
// monitor, decision, and firewall layers are filled in by later phases.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/behnam-rk/dezhban/internal/command"
	"github.com/behnam-rk/dezhban/internal/config"
	"github.com/behnam-rk/dezhban/internal/control"
	"github.com/behnam-rk/dezhban/internal/decision"
	"github.com/behnam-rk/dezhban/internal/firewall"
	"github.com/behnam-rk/dezhban/internal/learned"
	"github.com/behnam-rk/dezhban/internal/logging"
	"github.com/behnam-rk/dezhban/internal/monitor"
	"github.com/behnam-rk/dezhban/internal/netdetect"
	"github.com/behnam-rk/dezhban/internal/privilege"
	"github.com/behnam-rk/dezhban/internal/runner"
	"github.com/behnam-rk/dezhban/internal/state"
	"github.com/behnam-rk/dezhban/internal/svc"
)

// The build stamps (version/commit/date) and their ReadBuildInfo fallback live
// in version.go; `buildStamp` is the resolved identity.

// verbose is the global -v/--verbose flag, stripped from args before dispatch.
// When set it overrides the configured log level to debug.
var verbose bool

// noSudo is the global --no-sudo flag: when set, privileged commands do NOT
// auto-re-exec under sudo and instead print the "must run as root" error.
var noSudo bool

const usage = `dezhban — network kill switch

Usage:
  dezhban [-v] <command> [flags]

Commands:
  run         Run the monitor→decision→enforcement loop
  block       Manually block network egress
  unblock     Remove dezhban's firewall rules
  status      Show version, config, and current state
  validate    Load and validate a config file (no root, no side effects)
  monitor     Live read-only view: IP, country, tunnel state, endpoints, verdict
  print-rules Print the firewall ruleset a block/guard would apply, without applying it
  doctor      Diagnose VPN guard config (tunnels, endpoints, lockout risks)
  panic       Force-remove dezhban's rules even if the daemon is dead
  install     Register dezhban as a boot-persistent OS service
  uninstall   Remove the OS service
  start       Start the installed service
  stop        Stop the installed service (removes firewall rules)
  restart     Restart the installed service (apply a config change)
  detect-vpn  Print detected VPN tunnel interfaces to help fill the vpn config
  switch      Open a bounded window to connect a brand-new VPN (learns its server)
  vpn         Manage VPN profiles and learned endpoints (list/add/remove/import/…)
  setup       Interactive wizard to create or update the config
  config      Inspect or change the config without hand-editing JSON
  completion  Print a shell completion script (bash|zsh|fish)
  upgrade     Check/download/apply a newer release (check: no root; apply: macOS, root)
  version     Print the version

Global flags:
  -v, --verbose   Override the configured log level to debug
  --no-sudo       Don't auto-elevate; print the root error instead
  --no-daemon     Don't use the daemon's control socket; act on the firewall directly

block, unblock and switch ask the running daemon over its control socket, which
needs no password (see the "daemon control" line in dezhban status). With no
daemon listening they fall back to acting on the firewall directly, needing root.

Privileged commands re-run themselves under sudo automatically when not root
(unix, interactive terminal). Use --no-sudo (or DEZHBAN_NO_SUDO=1) to opt out.

Config resolution (when --config is omitted): $DEZHBAN_CONFIG, else the system
path (see "dezhban config path"), else built-in defaults.

Run "dezhban <command> -h" for command flags.`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	args = stripVerbose(args)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "run":
		return cmdRun(rest)
	case "block":
		return cmdBlock(rest)
	case "unblock":
		return cmdUnblock(rest)
	case "status":
		return cmdStatus(rest)
	case "validate":
		return cmdValidate(rest)
	case "monitor":
		return cmdMonitor(rest)
	case "print-rules":
		return cmdPrintRules(rest)
	case "doctor":
		return cmdDoctor(rest)
	case "panic":
		return cmdPanic(rest)
	case "restart":
		return cmdRestart(rest)
	case "install", "uninstall", "start", "stop":
		return cmdService(cmd, rest)
	case "detect-vpn":
		return cmdDetectVPN(rest)
	case "switch":
		return cmdSwitch(rest)
	case "vpn":
		return cmdVPN(rest)
	case "setup":
		return cmdSetup(rest)
	case "config":
		return cmdConfig(rest)
	case "completion":
		return cmdCompletion(rest)
	case "upgrade":
		return cmdUpgrade(rest)
	case "version", "--version":
		return cmdVersion()
	case "help", "--help", "-h":
		fmt.Println(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s\n", cmd, usage)
		return 2
	}
}

// stripVerbose removes the global -v/--verbose flag (which may appear before or
// after the subcommand) from args and records it in the package-level verbose.
// Pulling it out here lets every subcommand's FlagSet stay unaware of it.
func stripVerbose(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "-v", "--v", "-verbose", "--verbose":
			verbose = true
		case "-no-sudo", "--no-sudo":
			noSudo = true
		case "-no-daemon", "--no-daemon":
			noDaemonFlag = true
		default:
			out = append(out, a)
		}
	}
	return out
}

// effectiveLevel is the log level after applying the global -v/--verbose override.
func effectiveLevel(cfg *config.Config) string {
	if verbose {
		return "debug"
	}
	return cfg.LogLevel
}

// newLogger builds a logger honoring the -v/--verbose override.
func newLogger(cfg *config.Config) *slog.Logger {
	return logging.New(effectiveLevel(cfg))
}

// requireRoot ensures the command runs as root. When it isn't, it auto-re-execs
// the whole invocation under sudo (unix, unless --no-sudo / no TTY); that call
// replaces the process and never returns. If elevation is unavailable it prints
// a clear error and returns false.
func requireRoot(cmd string) bool {
	if privilege.IsPrivileged() {
		return true
	}
	if canElevate() {
		fmt.Fprintf(os.Stderr, "dezhban %s needs root — re-running with sudo…\n", cmd)
		if err := reexecElevated(); err != nil {
			fmt.Fprintln(os.Stderr, "auto-sudo failed:", err)
		}
	}
	fmt.Fprintf(os.Stderr, "dezhban %s must run as root (try: sudo dezhban %s ...)\n", cmd, cmd)
	return false
}

// loadConfig is a small helper shared by the commands that take --config. It
// resolves the path (so --config can be omitted) before loading.
// reportRetired warns once per retired key found in the config file. The keys are
// inert, so this is the only signal an operator gets that a setting they wrote is
// not doing anything — silence here would let someone believe a discarded
// security setting took effect.
func reportRetired(cfg *config.Config, log *slog.Logger) {
	for _, r := range cfg.Retired {
		log.Warn("config key is retired and has no effect", "key", r.Key, "why", r.Reason)
	}
}

func loadConfig(path string) (*config.Config, error) {
	return config.Load(resolveConfigPath(path))
}

// resolveConfigPath decides which config file a command reads when its --config
// flag is empty, so the flag can usually be omitted. Precedence:
//  1. an explicit --config value
//  2. $DEZHBAN_CONFIG
//  3. the canonical system path (defaultConfigPath), if the file exists
//  4. "" — built-in defaults (config.Load treats an empty path as defaults)
func resolveConfigPath(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if env := strings.TrimSpace(os.Getenv("DEZHBAN_CONFIG")); env != "" {
		return env
	}
	if p := defaultConfigPath(); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to config file (JSON)")
	dryRun := fs.Bool("dry-run", false, "resolve and print country without touching the firewall")
	simCountry := fs.String("simulate-country", "", "TESTING: force the resolved country code (e.g. IR) to drive the verdict")
	simTunDown := fs.String("simulate-tunnel-down", "", "TESTING: report the tunnel as down after this delay (e.g. 8s) to exercise failover")
	_ = fs.Parse(args)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}
	log := newLogger(cfg)
	reportRetired(cfg, log)

	ov, err := parseOverrides(*simCountry, *simTunDown)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	if *dryRun {
		return runDryRun(cfg, log, ov)
	}
	if !requireRoot("run") {
		return 1
	}

	// Persistent log capture, always on: every daemon run appends to
	// <state dir>/logs/dezhban.log (size-rotated), whether launched from a shell
	// or by the service manager — stderr is lost when the shell closes and the
	// platform logger keeps no file an operator (or the GUI) can just read back.
	// Best-effort: a failure to open the file degrades to the primary logger
	// only, never blocks enforcement.
	var persist slog.Handler
	if fw, err := logging.OpenFile(defaultLogPath()); err != nil {
		log.Warn("persistent log capture unavailable", "path", defaultLogPath(), "err", err)
	} else {
		defer fw.Close()
		persist = logging.NewTextHandler(effectiveLevel(cfg), fw)
		log = slog.New(logging.Fanout(log.Handler(), persist))
	}

	// Run under the service manager. When launched from a shell this behaves like
	// a foreground daemon (kardianos handles SIGINT/SIGTERM and calls Stop, which
	// cancels the loop so its deferred Cleanup removes all rules); when launched by
	// launchd/systemd/Windows it runs as the managed service and logs to the
	// platform logger. The build closure assembles the run loop lazily so it can
	// use whichever logger the service selects.
	build := func(l *slog.Logger) (runner.Options, error) {
		return assembleOptions(cfg, l, ov)
	}
	if err := svc.Run(build, log, effectiveLevel(cfg), *cfgPath, persist); err != nil {
		log.Error("run loop failed", "err", err)
		return 1
	}
	return 0
}

// runOverrides carries the TESTING-only flags (--simulate-country,
// --simulate-tunnel-down) through the run-loop assembly. Zero value = no overrides.
type runOverrides struct {
	simCountry      string
	tunnelDownSet   bool
	tunnelDownAfter time.Duration
}

// parseOverrides validates the simulation flags into a runOverrides.
func parseOverrides(simCountry, simTunDown string) (runOverrides, error) {
	ov := runOverrides{simCountry: strings.TrimSpace(simCountry)}
	if s := strings.TrimSpace(simTunDown); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return runOverrides{}, fmt.Errorf("--simulate-tunnel-down: %w", err)
		}
		ov.tunnelDownSet = true
		ov.tunnelDownAfter = d
	}
	return ov, nil
}

// assembleOptions builds the run-loop options from config, wiring the monitor,
// decider, and firewall backend. It is shared by the `run` command and the
// service Start path; the logger is supplied by the caller so service mode can
// route output to the platform logger.
func assembleOptions(cfg *config.Config, log *slog.Logger, ov runOverrides) (runner.Options, error) {
	// Everything the daemon publishes to the outside world lives under this one
	// directory — state.json for the menubar app, control.sock for passwordless
	// routine ops. It must be traversable by the unprivileged user or both silently
	// stop working, so establish (and repair) its mode once, here, before anything
	// writes into it. Non-fatal: a stale mode degrades observability, it must never
	// stop the kill switch from enforcing.
	if err := state.EnsureDir(stateDir()); err != nil {
		log.Warn("state directory not reachable by unprivileged readers; the menubar app and control socket may not work", "err", err)
	}

	providers := monitor.ProvidersFromURLs(cfg.Providers, log)
	if len(providers) == 0 {
		return runner.Options{}, fmt.Errorf("no usable geo providers configured")
	}
	fw, err := firewall.New()
	if err != nil {
		return runner.Options{}, fmt.Errorf("firewall backend unavailable: %w", err)
	}

	var mon runner.Monitor = monitor.New(providers, cfg.PollInterval, log, cfg.ProviderQuorum)
	if ov.simCountry != "" {
		log.Warn("SIMULATION: forcing resolved country", "country", ov.simCountry)
		mon = monitor.NewSimMonitor(mon.(*monitor.Monitor), ov.simCountry)
	}

	tunnels := resolveTunnels(cfg, log)
	epSrc := buildEndpointSource(cfg, log, tunnels, true)
	watcher := buildWatcher(cfg, log, tunnels, ov)

	// Learned-endpoint store: feed persisted endpoints into resolution, and record
	// new ones the daemon learns. Load failures are non-fatal (learned data is
	// convenience, not load-bearing).
	learnedPath := defaultLearnedPath()
	adv := cfg.VPN.Advanced
	// Reload the store from disk on every access rather than caching it in memory,
	// so external maintenance edits (e.g. `dezhban vpn forget`, which rewrites
	// learned.json) are respected and never clobbered by a stale in-memory copy.
	// Both closures run only on the single run-loop goroutine, so there is no race.
	if _, lerr := learned.Load(learnedPath); lerr != nil {
		log.Warn("learned endpoints store unreadable; starting empty", "err", lerr)
	}
	epSrc.Learned = func() []netip.Addr {
		store, lerr := learned.Load(learnedPath)
		if lerr != nil {
			log.Debug("learned endpoints reload failed; skipping this cycle", "err", lerr)
			return nil
		}
		out := make([]netip.Addr, 0, len(store.Addrs()))
		for _, s := range store.Addrs() {
			// Unmap: learned.json stores whatever text it was given, and a
			// 4-in-6 form here would render as an inet6 rule that real IPv4
			// traffic never matches — a silently blocked endpoint. The policy
			// constructor normalises too; this keeps the value canonical for the
			// grace/prune bookkeeping that compares addresses before it.
			if a, perr := netip.ParseAddr(s); perr == nil {
				out = append(out, a.Unmap())
			}
		}
		return out
	}
	learnHook := func(profile, iface string, addrs []netip.Addr) {
		// Reload before mutating so a concurrent forget/edit is merged with, not
		// overwritten by, the new entry (Load returns a usable empty store on error).
		store, lerr := learned.Load(learnedPath)
		if lerr != nil {
			log.Warn("learned endpoints unreadable before save; recording onto empty store", "err", lerr)
		}
		store.Record(profile, iface, "switch-window", addrs, adv.LearnedMaxPerProfile, time.Now())
		store.Prune(adv.LearnedEndpointTTL, adv.LearnedMaxPerProfile, time.Now())
		if serr := store.Save(learnedPath); serr != nil {
			log.Warn("save learned endpoints failed", "err", serr)
		}
	}

	// Switch-window control: poll the root-owned command file. Only active when
	// the guard is on and a switch window is configured.
	switchEnabled := cfg.VPN.SwitchWindow > 0
	commandPath := defaultCommandPath()
	if switchEnabled {
		if derr := command.Discard(commandPath); derr != nil {
			log.Debug("discard stale command file failed", "err", derr)
		}
	}
	pollCommand := func() (command.Command, bool) {
		c, ok, cerr := command.Consume(commandPath, time.Now(), adv.CommandFreshness, command.RootOwned)
		if cerr != nil {
			log.Warn("rejected control command", "err", cerr)
			return command.Command{}, false
		}
		return c, ok
	}

	// Publish live posture to the state file for out-of-process observers (the
	// macOS menubar app, `status --json`). Best-effort: a write failure is logged
	// at debug and never affects enforcement.
	statePath := defaultStatePath()
	publish := func(s state.Snapshot) {
		// Stamp the running version here rather than in runner: this closure is
		// the single choke point every snapshot passes through (including the
		// terminal publishStopped one), and the build identity belongs to the
		// binary, not to the enforcement loop. `upgrade apply` reads it back to
		// tell a still-pending activation from one that already landed — see
		// state.Snapshot.Version.
		s.Version = buildStamp.Version
		if err := state.Write(statePath, s); err != nil {
			log.Debug("state publish failed", "path", statePath, "err", err)
		}
	}

	log.Info("run loop started",
		"interval", cfg.PollInterval,
		"providers", len(providers),
		"blocked_countries", cfg.BlockedCountries,
		"hysteresis", cfg.Hysteresis,
		"quorum", cfg.ProviderQuorum,
		"auto_discover_endpoints", cfg.VPN.AutoDiscoverEndpoints,
		"tunnel_watch", watcher != nil,
	)
	// The control socket is convenience, never enforcement: if it can't be created
	// (unresolvable group, unwritable dir), log it and run without it rather than
	// refusing to start the kill switch. The CLI falls back to the root path.
	var ctl *control.Server
	if cfg.Control.Enabled {
		ctl, err = control.New(controlSocketPath(cfg), cfg.Control.Group, log)
		if err != nil {
			log.Warn("control socket unavailable — routine ops will ask for a password", "err", err)
		} else {
			log.Info("control socket listening", "path", ctl.Path(), "group", cfg.Control.Group, "switch_ops", cfg.Control.AllowSwitchOps)
		}
	}

	return runner.Options{
		Monitor:           mon,
		Decider:           decision.New(cfg.BlockedCountries, cfg.Hysteresis),
		Backend:           fw,
		Log:               log,
		Interval:          cfg.PollInterval,
		Control:           ctl,
		AllowSwitchOps:    cfg.Control.AllowSwitchOps,
		Tunnels:           tunnels,
		Autodetect:        cfg.VPN.Autodetect,
		AllowPhysicalDNS:  cfg.VPN.AllowPhysicalDNS,
		AllowLocalNetwork: cfg.VPN.AllowLocalNetwork,
		ResolveEndpoints:  func(ctx context.Context) netdetect.EndpointSet { return epSrc.Resolve(ctx) },
		// Geo-provider IPs for the tunnel-scoped FULL BLOCK pass. Reuses the same
		// resolver `block --force` uses; the runner calls it at startup and on
		// each endpoint-refresh tick, since CDN-fronted providers rotate.
		ResolveProviders: func(context.Context) []netip.Addr {
			return buildProviderAllowlist(cfg, log).Hosts
		},
		ResolveEndpointsWith: func(ctx context.Context, tuns []string) netdetect.EndpointSet {
			return epSrc.ResolveWith(ctx, tuns)
		},
		EndpointRefresh:         cfg.VPN.EndpointRefresh,
		EndpointGrace:           cfg.VPN.EndpointGrace,
		AutoArm:                 cfg.VPN.AutoArm,
		Watcher:                 watcher,
		WindowProtos:            adv.WindowProtocols,
		WindowPorts:             adv.WindowPorts,
		WindowDiscoveryInterval: adv.WindowDiscoveryInterval,
		SwitchWindow:            cfg.VPN.SwitchWindow,
		SwitchWindowMax:         adv.SwitchWindowMax,
		ReconnectWindow:         cfg.VPN.ReconnectWindow,
		ReconnectWindowMax:      adv.ReconnectWindowMax,
		ReconnectMinUptime:      adv.ReconnectMinUptime,
		Learn:                   learnHook,
		PollCommand:             switchPollOrNil(switchEnabled, pollCommand),
		Publish:                 publish,
		BlockedCountries:        cfg.BlockedCountries,
	}, nil
}

// switchPollOrNil returns the command poller only when the switch window is
// enabled, so the runner leaves the feature off otherwise.
func switchPollOrNil(enabled bool, poll func() (command.Command, bool)) func() (command.Command, bool) {
	if !enabled {
		return nil
	}
	return poll
}

// buildWatcher constructs the tunnel watcher, or returns nil when there is
// nothing to watch. It exists whenever tunnels are configured/autodetected (so
// VPN-mode observability and the legacy kill switch work) or when a tunnel-drop
// simulation is requested.
func buildWatcher(cfg *config.Config, log *slog.Logger, tunnels []string, ov runOverrides) *netdetect.Watcher {
	if len(tunnels) == 0 && !cfg.VPN.Autodetect && !ov.tunnelDownSet {
		return nil
	}
	// In autodetect mode the watcher must sample ALL tunnel-like interfaces, not
	// just the set known at startup: utunN names change across reconnects, so
	// pinning the watcher to the start-time list (which liveSample treats as an
	// allowlist) would blind it to a renumbered or newly-created tunnel and stop
	// the runner growing/pruning its guarded set. An empty Tunnels makes
	// liveSample consider every interface; the runner still starts from `tunnels`.
	// With autodetect off, explicit pins keep their allowlist semantics.
	watchTunnels := tunnels
	if cfg.VPN.Autodetect {
		watchTunnels = nil
	}
	w := &netdetect.Watcher{Tunnels: watchTunnels, Interval: cfg.VPN.TunnelWatch, Log: log}
	if ov.tunnelDownSet {
		log.Warn("SIMULATION: tunnel will be reported down", "after", ov.tunnelDownAfter)
		w.Sample = simTunnelSample(ov.tunnelDownAfter)
	}
	return w
}

// simTunnelSample reports the tunnel UP until downAfter has elapsed, then DOWN —
// a deterministic drop for exercising the failover path with no real VPN.
func simTunnelSample(downAfter time.Duration) func([]string) netdetect.TunnelState {
	start := time.Now()
	return func([]string) netdetect.TunnelState {
		if time.Since(start) >= downAfter {
			return netdetect.TunnelState{Up: false, Detail: "simulated drop"}
		}
		return netdetect.TunnelState{Up: true, Detail: "simulated up"}
	}
}

// runDryRun polls the monitor and prints each reading without touching the
// firewall. Stops on SIGINT/SIGTERM.
func runDryRun(cfg *config.Config, log *slog.Logger, ov runOverrides) int {
	providers := monitor.ProvidersFromURLs(cfg.Providers, log)
	if len(providers) == 0 {
		log.Error("no usable geo providers configured")
		return 1
	}
	base := monitor.New(providers, cfg.PollInterval, log, cfg.ProviderQuorum)
	var mon interface {
		Poll(ctx context.Context) <-chan monitor.Result
	} = base
	if ov.simCountry != "" {
		log.Warn("SIMULATION: forcing resolved country", "country", ov.simCountry)
		mon = monitor.NewSimMonitor(base, ov.simCountry)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("monitor dry-run started", "interval", cfg.PollInterval, "providers", len(providers))
	for res := range mon.Poll(ctx) {
		if res.Err != nil {
			log.Warn("country lookup failed", "err", res.Err)
			continue
		}
		log.Info("tick",
			"ip", res.Reading.IP,
			"country", res.Reading.CountryCode,
			"provider", res.Reading.Provider,
		)
	}
	log.Info("monitor dry-run stopped")
	return 0
}

func cmdBlock(args []string) int {
	fs := flag.NewFlagSet("block", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to config file (JSON)")
	guard := fs.Bool("guard", false, "apply the VPN interface guard (pass tunnel + endpoint, block other egress)")
	force := fs.Bool("force", false, "force a hard full block of all egress, bypassing the VPN guard state machine")
	_ = fs.Parse(args)
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}
	log := newLogger(cfg)

	// Passwordless path: ask the running daemon to block. Skipped for --guard and
	// --force, which are deliberate low-level overrides of the state machine the
	// daemon owns — those still act on the firewall directly, as root.
	if !noDaemon() && !*guard && !*force {
		if code, handled := tryControl(*cfgPath, control.Request{Op: control.OpBlock}); handled {
			if code == 0 {
				fmt.Println("blocked (via daemon) — held until `dezhban unblock`")
			}
			return code
		}
	}

	if !requireRoot("block") {
		return 1
	}

	fw, err := firewall.New()
	if err != nil {
		log.Error("firewall backend unavailable", "err", err)
		return 1
	}

	switch {
	case *force:
		// Manual override: cut ALL egress (except loopback + the geo-API providers)
		// regardless of the guard's own state. The escape hatch when detection is
		// wrong or the operator wants an unconditional hard block. `unblock`/`panic`
		// reverse it. Build the allowlist BEFORE blocking, while DNS still works:
		// resolve the provider hostnames to IPs so recovery detection can still
		// reach them once egress is cut.
		al := buildProviderAllowlist(cfg, log)
		if err := fw.Block(al); err != nil {
			log.Error("forced block failed", "err", err)
			return 1
		}
		log.Info("network force-blocked (all egress cut except loopback + geo providers)", "hosts_allowed", len(al.Hosts))
	default:
		// `--guard` installs the always-on interface guard (tunnel stays open,
		// physical egress locked to the endpoint); a plain `block` is a full block
		// that cuts the tunnel too. Built through the same firewall.PolicyInput
		// constructor the daemon and print-rules use, so this manual override can
		// never drift from what the run loop would actually install — in
		// particular, it must NOT carry a physical dst-IP allowlist: a VPN posture
		// opens the tunnel endpoint, never a destination allowlist.
		tunnels := resolveTunnels(cfg, log)
		if len(tunnels) == 0 {
			log.Error("vpn mode needs tunnel interfaces (vpn.tunnelInterfaces or vpn.autodetect)")
			return 1
		}
		endpoints := resolveEndpointsOnce(cfg, log, tunnels)
		if len(endpoints) == 0 {
			log.Error("vpn mode needs at least one reachable endpoint (vpn.endpoints as IP/hostname, or vpn.autoDiscoverEndpoints with the VPN connected)")
			return 1
		}
		in := firewall.PolicyInput{
			Tunnels:           tunnels,
			Endpoints:         endpoints,
			AllowPhysicalDNS:  cfg.VPN.AllowPhysicalDNS,
			AllowLocalNetwork: cfg.VPN.AllowLocalNetwork,
			WindowProtos:      cfg.VPN.Advanced.WindowProtocols,
			WindowPorts:       cfg.VPN.Advanced.WindowPorts,
		}
		pol := in.FullBlock()
		if *guard {
			pol = in.Guard()
		}
		if err := fw.Apply(pol); err != nil {
			log.Error("block failed", "err", err)
			return 1
		}
		if *guard {
			log.Info("vpn guard active", "tunnels", tunnels, "endpoints", len(endpoints))
		} else {
			log.Info("network full-blocked (vpn)", "tunnels", tunnels)
		}
	}
	return 0
}

// resolveTunnels returns the VPN tunnel interface names to guard. Explicit
// config values always win; when none are set and vpn.autodetect is enabled, it
// discovers them via netdetect. It may return empty (autodetect found nothing) —
// callers must treat an empty guard set as a hard error, never proceed (an empty
// guard would be a total lockout).
func resolveTunnels(cfg *config.Config, log *slog.Logger) []string {
	if len(cfg.VPN.TunnelInterfaces) > 0 {
		return cfg.VPN.TunnelInterfaces
	}
	if !cfg.VPN.Autodetect {
		return nil
	}
	tun, err := netdetect.TunnelInterfaces()
	if err != nil {
		log.Warn("tunnel autodetect failed", "err", err)
		return nil
	}
	if len(tun) == 0 {
		log.Warn("tunnel autodetect found no tunnel interfaces")
		return nil
	}
	log.Info("autodetected tunnel interfaces", "tunnels", tun)
	return tun
}

// buildEndpointSource assembles the VPN endpoint resolver from config: IP
// literals and hostnames are split out of vpn.endpoints, and live discovery is
// attached when vpn.autoDiscoverEndpoints is on (macOS only). The same source
// powers the live run loop, one-shot resolution for block/print-rules, and the
// monitor view, so they agree on what "the endpoints" are.
func buildEndpointSource(cfg *config.Config, log *slog.Logger, tunnels []string, withDiscovery bool) *netdetect.EndpointSource {
	var literals []netip.Addr
	var hostnames []string
	// The union of the flat vpn.endpoints and all profile endpoints: switching
	// between known VPNs needs no reconfiguration because every profile's server
	// stays reachable.
	for _, ep := range config.EffectiveEndpoints(cfg, nil) {
		ep = strings.TrimSpace(ep)
		if ep == "" {
			continue
		}
		if a, err := netip.ParseAddr(ep); err == nil {
			literals = append(literals, a.Unmap())
		} else {
			hostnames = append(hostnames, ep)
		}
	}
	src := &netdetect.EndpointSource{
		Literals:  literals,
		Hostnames: hostnames,
		Tunnels:   tunnels,
		Log:       log,
	}
	if withDiscovery && cfg.VPN.AutoDiscoverEndpoints {
		if runtime.GOOS == "darwin" {
			src.Discover = netdetect.DiscoverEndpointsAddrs
		} else {
			log.Warn("vpn.autoDiscoverEndpoints is set but live discovery is only supported on macOS; " +
				"relying on vpn.endpoints (hostnames/IPs)")
		}
	}
	return src
}

// resolveEndpointsOnce resolves the endpoint set a single time (literals +
// hostnames + discovery), for the non-daemon commands that need a concrete list.
func resolveEndpointsOnce(cfg *config.Config, log *slog.Logger, tunnels []string) []netip.Addr {
	src := buildEndpointSource(cfg, log, tunnels, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return src.Resolve(ctx).Addrs
}

// buildProviderAllowlist resolves the configured geo-API providers to IPs, so
// `block --force` — the only remaining caller — can still reach them while all
// other egress is cut. This used to also fold in a user-configured
// destination allowlist (vpn.allowlist.dns/hosts); that key is retired
// (docs/adr/0001) because it belonged to the country-blocklist model, where
// the firewall was open at rest and needed an explicit list of exceptions.
// `--force` is a manual, temporary override, not a standing posture, so it has
// no equivalent need for user-supplied destinations.
func buildProviderAllowlist(cfg *config.Config, log *slog.Logger) firewall.Allowlist {
	var al firewall.Allowlist
	seen := make(map[netip.Addr]bool)
	add := func(a netip.Addr) {
		a = a.Unmap()
		if a.IsValid() && !seen[a] {
			seen[a] = true
			al.Hosts = append(al.Hosts, a)
		}
	}
	for _, raw := range cfg.Providers {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			log.Warn("cannot parse provider url for allowlist", "url", raw)
			continue
		}
		// Bound the lookup: this runs synchronously in the run loop's Block path,
		// so a hung resolver would otherwise stall enforcement.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
		cancel()
		if err != nil {
			log.Warn("cannot resolve provider for allowlist", "host", u.Hostname(), "err", err)
			continue
		}
		for _, ip := range ips {
			if a, ok := netip.AddrFromSlice(ip.IP); ok {
				add(a)
			}
		}
	}
	// The allowlist pins IPs at block time. If nothing resolved, recovery
	// detection can never reach a geo-API once egress is cut — the block would
	// become permanent. Warn loudly rather than silently lock the operator out.
	// NOTE: the legacy loop only rebuilds this on an Allow→Block transition, so a
	// provider that rotates CDN IPs mid-block becomes unreachable until the next
	// transition. Live mid-block refresh is Phase 4 (recovery probe) work.
	if len(al.Hosts) == 0 {
		log.Warn("no geo-API egress IPs in allowlist — recovery detection cannot work while blocked")
	}
	return al
}

func cmdUnblock(args []string) int {
	fs := flag.NewFlagSet("unblock", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to config file (JSON)")
	// unblock already removes dezhban's rules unconditionally; --force is accepted
	// for symmetry with `block --force` and documents the manual-override intent.
	force := fs.Bool("force", false, "remove rules unconditionally, bypassing the daemon (unblock is already unconditional)")
	_ = fs.Parse(args)

	// Passwordless path: ask the daemon to release the block and hand the geo state
	// machine back the wheel. --force bypasses it and rips the rules out directly —
	// which also leaves a running daemon free to re-block on its next verdict.
	if !noDaemon() && !*force {
		if code, handled := tryControl(*cfgPath, control.Request{Op: control.OpUnblock}); handled {
			if code == 0 {
				fmt.Println("unblocked (via daemon) — monitoring resumed")
			}
			return code
		}
	}

	if !requireRoot("unblock") {
		return 1
	}
	fw, err := firewall.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "firewall backend unavailable:", err)
		return 1
	}
	if err := fw.Unblock(); err != nil {
		fmt.Fprintln(os.Stderr, "unblock failed:", err)
		return 1
	}
	fmt.Println("dezhban: network unblocked")
	return 0
}

// cmdPanic is the standalone safety net: it tears down dezhban's firewall rules
// directly through the backend, with no running daemon involved. A crashed `run`
// leaves its block-all (or VPN guard) rules in place — by design, the kill switch
// must not fail open — so this is the escape hatch that restores connectivity.
// Cleanup targets only the `dezhban` tag/anchor/table/sublayer, so it removes
// both FULL-BLOCK and always-on GUARD rules and is a safe no-op on a clean system.
func cmdPanic(args []string) int {
	fs := flag.NewFlagSet("panic", flag.ExitOnError)
	_ = fs.Parse(args)
	if !requireRoot("panic") {
		return 1
	}
	fw, err := firewall.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "firewall backend unavailable:", err)
		return 1
	}
	// Cleanup is best-effort and idempotent: it restores any saved prior state
	// (e.g. pf) and removes dezhban's rules whether or not a daemon owns them.
	if err := fw.Cleanup(); err != nil {
		fmt.Fprintln(os.Stderr, "panic: teardown reported an error (rules may persist):", err)
		return 1
	}
	fmt.Println("dezhban: panic teardown complete — all dezhban rules removed, connectivity restored")
	return 0
}

// cmdRestart applies a config change to the running daemon — there is no live
// reload (kardianos has no SIGHUP-style reconfigure), so it is a stop followed by a
// start. It exists as one command rather than two because the two halves have to
// agree about the in-between state: `stop` on a service that is installed but not
// running must be a no-op, not an error. Composing it from two shell invocations put
// that judgement in the caller, where a failed stop aborted the start and left the
// daemon down with a new config it never read.
func cmdRestart(args []string) int {
	// --config is accepted and ignored, exactly as start/stop do: the installed
	// service unit already carries the config path it was registered with. Parsing it
	// (rather than ignoring args wholesale) is what makes a typo'd flag an error
	// instead of a silent no-op.
	fs := flag.NewFlagSet("restart", flag.ExitOnError)
	_ = fs.String("config", "", "ignored — the installed service uses the path it was registered with")
	_ = fs.Parse(args)

	if !requireRoot("restart") {
		return 1
	}
	if !svc.Installed() {
		fmt.Fprintln(os.Stderr, "restart: the service is not installed — run `dezhban install` first")
		return 1
	}
	if code := serviceAction("stop", ""); code != 0 {
		return code
	}
	// Wait for the stop to actually settle before starting. `launchctl unload` can
	// return before launchd has dropped the job, and serviceAction("start") skips the
	// load when it still sees the service running — which would report a successful
	// restart while leaving the daemon down with a config it never read.
	if !waitUntilStopped(5 * time.Second) {
		fmt.Fprintln(os.Stderr, "restart: the service did not stop within 5s; not starting it again")
		return 1
	}
	return serviceAction("start", "")
}

// waitUntilStopped polls the service manager until the service is no longer running,
// or the budget runs out. Reports whether it stopped.
func waitUntilStopped(budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		if !svc.Running() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// cmdService handles install/uninstall/start/stop against the OS service manager.
// `install` embeds the config path into the boot invocation so the service loads
// the same config on every restart; the path is made absolute because the
// service manager runs from an unknown working directory.
func cmdService(action string, args []string) int {
	fs := flag.NewFlagSet(action, flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to config file the installed service loads on boot")
	_ = fs.Parse(args)

	if !requireRoot(action) {
		return 1
	}

	path := *cfgPath
	if action == "install" {
		if path == "" {
			path = defaultConfigPath()
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		// The service loads this path on every boot. If it's absent, config.Load
		// falls back to defaults (no blockedCountries) — a far weaker kill switch
		// than the operator likely intends. Warn loudly rather than register a
		// service that silently under-protects.
		if _, err := os.Stat(path); err != nil {
			fmt.Fprintf(os.Stderr, "warning: config %q not found — the service will start with defaults until you create it\n", path)
		}
	}

	return serviceAction(action, path)
}

// serviceAction runs one service-manager action, having already established root.
// start and stop are made IDEMPOTENT here: launchd's load/unload are edge triggers,
// so unloading a job that was never loaded fails with a bare "Input/output error"
// and loading one twice fails too. Being asked to reach a state you are already in
// is not an error — reporting it as one is what broke `restart` (a failing stop
// aborted the start) and made the GUI's config-apply leave the daemon down.
func serviceAction(action, path string) int {
	switch {
	// Stop consults Loaded(), not just Running(): a KeepAlive job whose daemon
	// is crash-looping sits "loaded but not running" (launchd's spawn-scheduled
	// throttle) — Running() is false, yet without the bootout it respawns. Only
	// a job the manager doesn't hold at all is truly "already stopped".
	case action == "stop" && !svc.Running() && !svc.Loaded():
		fmt.Println("dezhban service already stopped")
		return 0
	case action == "start" && svc.Running():
		fmt.Println("dezhban service already running")
		return 0
	}

	if err := svc.Control(action, path); err != nil {
		fmt.Fprintf(os.Stderr, "%s failed: %v\n", action, err)
		return 1
	}
	switch action {
	case "install":
		fmt.Printf("dezhban service installed (config: %s)\n", path)
		fmt.Println("start it now with: dezhban start")
	case "uninstall":
		fmt.Println("dezhban service uninstalled")
	case "start":
		fmt.Println("dezhban service started")
	case "stop":
		fmt.Println("dezhban service stopped (firewall rules removed)")
	}
	return 0
}

// defaultConfigPath is where the installed service looks for its config when no
// --config is given: /etc/dezhban/ on unix, %ProgramData%\dezhban\ on Windows.
func defaultConfigPath() string {
	if runtime.GOOS == "windows" {
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, "dezhban", "dezhban.json")
	}
	return "/etc/dezhban/dezhban.json"
}

// defaultStatePath is where the running daemon publishes its live posture and
// where observers (`status --json`, the macOS menubar app) read it. It sits in
// the same OS state dir the firewall backends already use, world-readable so the
// unprivileged logged-in user can read what the root daemon wrote.
func defaultStatePath() string {
	if runtime.GOOS == "windows" {
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, "dezhban", "state.json")
	}
	return "/var/db/dezhban/state.json"
}

// stateDir is the directory holding the daemon's state/command/learned files.
func stateDir() string { return filepath.Dir(defaultStatePath()) }

// defaultCommandPath is the root-owned control file the CLI writes and the daemon
// consumes (switch-window open/cancel).
func defaultCommandPath() string { return filepath.Join(stateDir(), "command.json") }

// defaultLearnedPath is the daemon-owned store of endpoints learned from switch
// windows / live discovery.
func defaultLearnedPath() string { return filepath.Join(stateDir(), "learned.json") }

// defaultLogPath is the daemon's persistent, size-rotated log file (0644, like
// state.json — readable history for the GUI and unprivileged operators).
func defaultLogPath() string { return filepath.Join(stateDir(), "logs", "dezhban.log") }

// cmdDetectVPN is a read-only setup helper for VPN mode. It prints the tunnel
// interface(s) it detects so the operator can fill vpn.tunnelInterfaces. It does
// NOT print an endpoint: autodetecting the VPN endpoint is unsafe (a wrong guess
// leaks physical egress), so the endpoint must be entered deliberately from the
// VPN client's own config. No privilege required.
func cmdDetectVPN(args []string) int {
	fs := flag.NewFlagSet("detect-vpn", flag.ExitOnError)
	_ = fs.Parse(args)

	tunnels, err := netdetect.TunnelInterfaces()
	if err != nil {
		fmt.Fprintln(os.Stderr, "detect-vpn: interface scan failed:", err)
		return 1
	}
	if len(tunnels) == 0 {
		fmt.Println("no VPN tunnel interfaces detected.")
		fmt.Println("connect your VPN first, then re-run; or set vpn.tunnelInterfaces manually.")
		return 0
	}
	fmt.Println("detected VPN tunnel interface(s):")
	for _, t := range tunnels {
		fmt.Println("  -", t)
	}
	fmt.Println("verify these belong to your VPN — on macOS the OS also creates system utun*")
	fmt.Println("interfaces; guarding the wrong one would not protect you.")
	fmt.Println()
	fmt.Println()
	fmt.Println("recommended config (autodetect handles interface renumbering across reconnects):")
	fmt.Println(`  "vpn": {`)
	fmt.Println(`    "enabled": true,`)
	fmt.Println(`    "autodetect": true,`)
	fmt.Println(`    "autoDiscoverEndpoints": true`)
	fmt.Println(`  }`)
	fmt.Println()
	fmt.Println("For commercial VPNs (Nord/Proton/…) that is all you need on macOS. For")
	fmt.Println("self-hosted VPNs, add a profile:  dezhban vpn add <name> --endpoint <server>")
	fmt.Println("(or import one:  dezhban vpn import <config-file>). To connect a brand-new")
	fmt.Println("VPN whose server isn't known yet:  dezhban switch, then connect it.")
	return 0
}

// cmdValidate loads and validates a config without running anything or touching
// the firewall — a fast, root-free pre-flight. config.Load already runs
// Validate(), so a clean load is a valid config; print a one-line summary so the
// operator can eyeball the loaded values.
func cmdValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to config file (JSON)")
	_ = fs.Parse(args)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config invalid:", err)
		return 1
	}
	src := resolveConfigPath(*cfgPath)
	if src == "" {
		src = "(built-in defaults — no config file found)"
	}
	blocked := cfg.BlockedCountries
	if len(blocked) == 0 {
		blocked = []string{"(none)"}
	}
	fmt.Printf("config OK: %s\n", src)
	fmt.Printf("  blocked countries: %s\n", strings.Join(blocked, ", "))
	fmt.Printf("  poll interval:     %s\n", cfg.PollInterval)
	fmt.Printf("  vpn tunnels:       %s\n", strings.Join(cfg.VPN.TunnelInterfaces, ", "))
	fmt.Printf("  vpn endpoints:     %s\n", strings.Join(cfg.VPN.Endpoints, ", "))
	// Retired keys are not an error — the config is valid and will run — but
	// `validate` is exactly where someone checks whether their file says what
	// they think it says, so a key that no longer does anything belongs here.
	for _, r := range cfg.Retired {
		fmt.Printf("\n  note: %q no longer has any effect.\n        %s\n", r.Key, r.Reason)
	}
	return 0
}

// cmdMonitor is a read-only live view of everything the decision rests on: the
// public IP and country, each tunnel's up/down state, the resolved + discovered
// endpoints with their source, and the verdict that WOULD fire — all without root
// or any firewall change. It is the safe way to watch detection and to confirm a
// VPN-guard config behaves before enabling it. Diagnostic logs go to stderr so
// stdout is just the snapshot.
func cmdMonitor(args []string) int {
	fs := flag.NewFlagSet("monitor", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to config file (JSON)")
	once := fs.Bool("once", false, "print a single snapshot and exit")
	simCountry := fs.String("simulate-country", "", "override the resolved country code (e.g. IR) to test the verdict")
	_ = fs.Parse(args)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}
	log := newLogger(cfg)

	providers := monitor.ProvidersFromURLs(cfg.Providers, log)
	if len(providers) == 0 {
		fmt.Fprintln(os.Stderr, "no usable geo providers configured")
		return 1
	}
	base := monitor.New(providers, cfg.PollInterval, log, cfg.ProviderQuorum)
	var mon interface {
		Once(ctx context.Context) (monitor.Reading, error)
	} = base
	if c := strings.TrimSpace(*simCountry); c != "" {
		fmt.Fprintf(os.Stderr, "SIMULATION: forcing country %s\n", strings.ToUpper(c))
		mon = monitor.NewSimMonitor(base, c)
	}

	tunnels := resolveTunnels(cfg, log)
	epSrc := buildEndpointSource(cfg, log, tunnels, true)
	blocked := make(map[string]bool, len(cfg.BlockedCountries))
	for _, c := range cfg.BlockedCountries {
		blocked[c] = true
	}

	snapshot := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, lookupErr := mon.Once(ctx)
		set := epSrc.Resolve(ctx)

		fmt.Println("── dezhban monitor ──")
		if lookupErr != nil {
			fmt.Printf("public IP:  (lookup failed: %v)\n", lookupErr)
		} else {
			fmt.Printf("public IP:  %s   country: %s   provider: %s\n", r.IP, r.CountryCode, r.Provider)
		}

		fmt.Println("tunnels:")
		if len(tunnels) == 0 {
			fmt.Println("  (none configured)")
		}
		for _, t := range tunnels {
			st := netdetect.SampleTunnels([]string{t})
			state := "DOWN"
			if st.Up {
				state = "UP"
			}
			fmt.Printf("  %s — %s (%s)\n", t, state, st.Detail)
		}

		fmt.Println("endpoints:")
		if len(set.Addrs) == 0 {
			fmt.Println("  (none resolved — set vpn.endpoints or enable vpn.autoDiscoverEndpoints)")
		}
		for _, a := range set.Addrs {
			fmt.Printf("  %s — %s\n", a, set.Sources[a])
		}

		// Verdict for THIS reading (hysteresis=1 shows the immediate call; the
		// configured hysteresis governs how many consecutive readings actually toggle).
		v := decision.New(cfg.BlockedCountries, 1).Evaluate(monitor.Result{Reading: r, Err: lookupErr})
		verdict := "ALLOW"
		if v == decision.Block {
			verdict = "BLOCK"
		}
		reason := "country not in blocklist"
		switch {
		case lookupErr != nil:
			// A lookup error is neutral: it holds the current posture rather than
			// escalating (docs/adr/0001) — under the guard, an unknown exit must
			// never be treated as if it were a confirmed-bad one.
			reason = "lookup failed — holding current posture (exit country unknown)"
		case blocked[r.CountryCode]:
			reason = "country in blocklist"
		}
		fmt.Printf("verdict:    %s — %s\n", verdict, reason)
		if cfg.Hysteresis > 1 {
			fmt.Printf("            (needs %d consecutive readings to toggle enforcement)\n", cfg.Hysteresis)
		}
		fmt.Println()
	}

	if *once {
		snapshot()
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	snapshot()
	t := time.NewTicker(cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0
		case <-t.C:
			snapshot()
		}
	}
}

// policyForMode builds the firewall Policy the named mode would apply. It is the
// single source print-rules renders from, and it builds postures through
// firewall.PolicyInput — the same constructor the run loop uses — so the preview
// cannot drift from what the daemon would actually install.
func policyForMode(cfg *config.Config, log *slog.Logger, mode string) (firewall.Policy, error) {
	tunnels := resolveTunnels(cfg, log)
	// The physical dst-IP allowlist belongs only to the legacy (non-VPN) model.
	// A VPN posture opens endpoints, not a physical allowlist, so the run loop
	// leaves it empty. Populate it only for non-VPN configs; otherwise a VPN config
	// with no static tunnels/endpoints (autoDiscover-only) would fail isVPNPolicy
	// and render phantom physical egress.
	// Built lazily because it resolves endpoints, which does DNS. The `legacy`
	// posture has no endpoints and never renders them, so resolving there would be
	// pointless network work that also logs resolution failures for addresses the
	// ruleset does not contain.
	vpnInput := func() firewall.PolicyInput {
		return firewall.PolicyInput{
			Tunnels:           tunnels,
			Endpoints:         resolveEndpointsOnce(cfg, log, tunnels),
			AllowPhysicalDNS:  cfg.VPN.AllowPhysicalDNS,
			AllowLocalNetwork: cfg.VPN.AllowLocalNetwork,
			WindowProtos:      cfg.VPN.Advanced.WindowProtocols,
			WindowPorts:       cfg.VPN.Advanced.WindowPorts,
		}
	}
	switch mode {
	case "guard":
		return vpnInput().Guard(), nil
	case "fullblock":
		return vpnInput().FullBlock(), nil
	case "switch":
		return vpnInput().SwitchWindow(), nil
	case "legacy":
		return firewall.Policy{}, fmt.Errorf("mode %q was removed: dezhban has a single guard state machine now (see docs/adr/0001-single-guard-mode.md)", mode)
	default:
		return firewall.Policy{}, fmt.Errorf("unknown mode %q (valid: guard, fullblock, switch)", mode)
	}
}

// cmdPrintRules renders the exact firewall ruleset a given policy would install
// and prints it to stdout WITHOUT applying it — the safe way to inspect a block
// or guard before risking a lockout. No root: rendering is pure. Diagnostic logs
// (allowlist resolution, etc.) go to stderr, so stdout is just the ruleset.
func cmdPrintRules(args []string) int {
	fs := flag.NewFlagSet("print-rules", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to config file (JSON)")
	mode := fs.String("mode", "guard", "policy to render: guard, fullblock, switch, or legacy")
	_ = fs.Parse(args)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}
	pol, err := policyForMode(cfg, newLogger(cfg), *mode)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	rules, err := firewall.RenderRules(pol)
	if err != nil {
		fmt.Fprintln(os.Stderr, "render failed:", err)
		return 1
	}
	fmt.Print(rules)
	return 0
}

// cmdDoctor diagnoses the VPN guard configuration without root or side effects:
// it validates config, lists tunnel interfaces and their subnets, and flags any
// endpoint that sits inside a tunnel's own subnet (a guaranteed lockout). With
// --discover it additionally runs the macOS-only best-effort hunt for the
// connected VPN's real server IP, automating the manual netstat/scutil dance.
func cmdDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to config file (JSON)")
	discover := fs.Bool("discover", false, "best-effort: find the connected VPN's real server IP (macOS only)")
	_ = fs.Parse(args)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config invalid:", err)
		return 1
	}
	log := newLogger(cfg)

	fmt.Println("dezhban doctor")
	fmt.Println()
	fmt.Println("config:  OK (loaded and validated)")
	fmt.Println()

	tunnels := resolveTunnels(cfg, log)
	fmt.Println("tunnels:")
	if len(tunnels) == 0 {
		fmt.Println("  (none — set vpn.tunnelInterfaces or vpn.autodetect)")
	} else {
		nets, _ := netdetect.TunnelSubnets(tunnels)
		subsByIface := map[string][]string{}
		for _, tn := range nets {
			subsByIface[tn.Iface] = append(subsByIface[tn.Iface], tn.Subnet.String())
		}
		for _, t := range tunnels {
			if subs := subsByIface[t]; len(subs) > 0 {
				fmt.Printf("  %s — %s\n", t, strings.Join(subs, ", "))
			} else {
				fmt.Printf("  %s — no subnet (interface down or absent?)\n", t)
			}
		}
	}
	fmt.Println()

	endpoints := resolveEndpointsOnce(cfg, log, tunnels)
	fmt.Println("endpoints (resolved: literals + hostnames + discovery):")
	var bad []netdetect.EndpointRoute
	if len(endpoints) == 0 {
		fmt.Println("  (none resolved)")
	} else {
		bad, _ = netdetect.CheckEndpointRouting(endpoints, tunnels)
		internal := map[string]netdetect.EndpointRoute{}
		for _, b := range bad {
			internal[b.Endpoint.String()] = b
		}
		for _, ep := range endpoints {
			if b, ok := internal[ep.String()]; ok {
				fmt.Printf("  %s — MISCONFIGURED: inside %s's subnet %s\n", ep, b.Iface, b.Subnet)
			} else {
				fmt.Printf("  %s — ok (assumed reachable on the physical interface)\n", ep)
			}
		}
	}
	if len(bad) > 0 {
		fmt.Println()
		fmt.Println("fixes:")
		for _, b := range bad {
			fmt.Printf("  - %s is a tunnel-internal address (inside %s %s); set vpn.endpoints to\n", b.Endpoint, b.Iface, b.Subnet)
			fmt.Println("    your VPN server's PUBLIC IP from your VPN client config.")
		}
	}

	// The guard blocks ALL egress on the physical link — which is what carries the
	// tunnel's own encrypted transport. With a tunnel up and no known server address,
	// arming it cuts every packet, kills the VPN, and leaves no socket for discovery to
	// learn from: an unrecoverable blackout, not a kill switch. The daemon refuses to
	// start in this state; doctor's whole job is to say so BEFORE you find out.
	lockout := len(tunnels) > 0 && len(endpoints) == 0
	if lockout {
		fmt.Println()
		fmt.Println("LOCKOUT RISK — dezhban will refuse to start:")
		fmt.Printf("  The VPN guard is on and %s is up, but no server address is known.\n", strings.Join(tunnels, ", "))
		fmt.Println("  The guard would block the tunnel's own transport and cut ALL traffic.")
		fmt.Println()
		fmt.Println("  Auto-discovery reads CONNECTED sockets. WireGuard (and other")
		fmt.Println("  NetworkExtension clients) send from an UNCONNECTED UDP socket, so they")
		fmt.Println("  never appear as a connected flow — discovery cannot find them. Name the")
		fmt.Println("  server explicitly:")
		fmt.Println()
		fmt.Println("    dezhban vpn import <wg0.conf|client.ovpn>   # reads the endpoint from it")
		fmt.Println("    dezhban vpn add <name> --endpoint <host-or-ip>")
		fmt.Println("    sudo dezhban config set vpn.endpoints=<server-ip>")
	}
	fmt.Println()

	// Touch ID discoverability (macOS): privileged ops (start/stop/panic, GUI
	// actions) authenticate through sudo, and sudo only offers Touch ID when
	// pam_tid is opted in via /etc/pam.d/sudo_local. Informational only — never
	// affects the exit code; password auth is degraded UX, not a lockout risk.
	if runtime.GOOS == "darwin" && !sudoTouchIDConfigured() {
		fmt.Println("touch id: not configured for sudo — privileged ops will ask for a password.")
		fmt.Println("  To authenticate with a fingerprint instead (survives OS updates):")
		fmt.Println()
		fmt.Println("    echo 'auth       sufficient     pam_tid.so' | sudo tee /etc/pam.d/sudo_local")
		fmt.Println()
	}

	if *discover {
		fmt.Println("discover (best-effort, macOS):")
		cands, err := netdetect.DiscoverEndpoints()
		switch {
		case err != nil:
			fmt.Println("  ", err)
		case len(cands) == 0:
			fmt.Println("  no physical-side public transport sockets found — is the VPN connected?")
		default:
			configured := map[string]bool{}
			for _, ep := range endpoints {
				configured[ep.String()] = true
			}
			for _, c := range cands {
				line := fmt.Sprintf("  %s:%d", c.Server, c.Port)
				if c.VPN != "" {
					line += " [" + c.VPN + "]"
				}
				if !configured[c.Server.String()] {
					line += "  <- not in vpn.endpoints"
				}
				fmt.Println(line)
			}
			fmt.Println("  add any missing server IP to vpn.endpoints and drop stale entries.")
		}
	}

	// Exit non-zero when a real lockout risk was found. A diagnostic that reports a
	// guaranteed blackout and still exits 0 is one `make doctor` in a script away from
	// being ignored — and these are exactly the two conditions the daemon refuses to
	// start on, so doctor must agree with it.
	if lockout || len(bad) > 0 {
		return 1
	}
	return 0
}

func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to config file (JSON)")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON (merges the live state file with service/config status)")
	_ = fs.Parse(args)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}

	if *jsonOut {
		return statusJSON(cfg)
	}

	blocked := cfg.BlockedCountries
	if len(blocked) == 0 {
		blocked = []string{"(none)"}
	}

	fmt.Println(buildStamp.line())
	fmt.Println("privileged:      ", privilege.IsPrivileged())
	fmt.Println("service:         ", svc.Status())
	fmt.Println("daemon control:  ", controlStatus(cfg))
	fmt.Println("poll interval:   ", cfg.PollInterval)
	fmt.Println("hysteresis:      ", cfg.Hysteresis)
	fmt.Println("blocked countries:", strings.Join(blocked, ", "))
	fmt.Println("providers:       ", strings.Join(cfg.Providers, ", "))
	fmt.Println("log level:       ", cfg.LogLevel)
	// What stays reachable on the PHYSICAL link while the guard is armed. These
	// are the only standing exceptions to "only the tunnel may egress", so they
	// belong in status rather than buried in the config file — an operator
	// checking their posture should not have to infer them.
	{
		var open []string
		if cfg.VPN.AllowLocalNetwork {
			open = append(open, "local network")
		}
		if cfg.VPN.AllowPhysicalDNS {
			open = append(open, "DNS")
		}
		if len(open) == 0 {
			open = []string{"(nothing — tunnel and VPN server only)"}
		}
		fmt.Println("also reachable:  ", strings.Join(open, ", "))
	}

	{
		tunnels := cfg.VPN.TunnelInterfaces
		if len(tunnels) == 0 && cfg.VPN.Autodetect {
			tunnels = []string{"(autodetect)"}
		}
		fmt.Println("vpn tunnels:     ", strings.Join(tunnels, ", "))
		fmt.Println("vpn endpoints:   ", strings.Join(config.EffectiveEndpoints(cfg, nil), ", "))
		if len(cfg.VPN.Profiles) > 0 {
			names := make([]string, len(cfg.VPN.Profiles))
			for i, p := range cfg.VPN.Profiles {
				names[i] = p.Name
			}
			fmt.Println("vpn profiles:    ", strings.Join(names, ", "))
		}
		fmt.Println("switch window:   ", cfg.VPN.SwitchWindow)
		if cfg.VPN.ReconnectWindow > 0 {
			fmt.Println("reconnect window:", cfg.VPN.ReconnectWindow)
		} else {
			fmt.Println("reconnect window: off")
		}
		// Live switch-window / active-profile state from the daemon's snapshot.
		if snap, err := state.Read(defaultStatePath()); err == nil {
			if snap.Switch != nil && snap.Switch.Open {
				kind := "switch state:    "
				if snap.Switch.Trigger == state.TriggerAuto {
					kind = "reconnect state: " // auto window opened by a tunnel drop
				}
				fmt.Printf("%s OPEN until %s\n", kind, snap.Switch.Until.Format(time.Kitchen))
			}
			if snap.ActiveProfile != "" {
				fmt.Println("active profile:  ", snap.ActiveProfile)
			}
		}
	}

	if fw, err := firewall.New(); err != nil {
		fmt.Println("blocked:          unknown:", err)
	} else if blocked, err := fw.IsBlocked(); err != nil {
		// Reading firewall rules needs root; report rather than fail the command.
		fmt.Println("blocked:          unknown:", err)
	} else {
		fmt.Println("blocked:         ", blocked)
	}
	return 0
}

// statusJSON prints a machine-readable status: the live posture from the state
// file (if the daemon is running and has published one) merged with service and
// config status. It is the stable contract for tooling and scripts that want
// authoritative service state alongside the snapshot. Read-only, no root required.
func statusJSON(cfg *config.Config) int {
	statePath := defaultStatePath()
	out := struct {
		Version          string          `json:"version"`
		Commit           string          `json:"commit,omitempty"`    // build stamp; empty outside a git tree
		BuildDate        string          `json:"buildDate,omitempty"` // RFC3339
		Privileged       bool            `json:"privileged"`
		Service          string          `json:"service"`
		StatePath        string          `json:"statePath"`
		State            *state.Snapshot `json:"state,omitempty"`    // nil when no snapshot has been published yet
		StateAge         string          `json:"stateAge,omitempty"` // wall-clock age of the snapshot
		PollInterval     string          `json:"pollInterval"`
		BlockedCountries []string        `json:"blockedCountries"`
		// No `vpnEnabled`: with one enforcement model it could only ever be true,
		// and a constant field invites consumers to branch on nothing. Read
		// `state.posture` instead — that is where the real distinction lives.
	}{
		Version:          buildStamp.Version,
		Commit:           buildStamp.short(),
		BuildDate:        buildStamp.Date,
		Privileged:       privilege.IsPrivileged(),
		Service:          svc.Status(),
		StatePath:        statePath,
		PollInterval:     cfg.PollInterval.String(),
		BlockedCountries: cfg.BlockedCountries,
	}
	if snap, err := state.Read(statePath); err == nil {
		out.State = &snap
		out.StateAge = time.Since(snap.Time).Round(time.Second).String()
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "status json:", err)
		return 1
	}
	fmt.Println(string(data))
	return 0
}
