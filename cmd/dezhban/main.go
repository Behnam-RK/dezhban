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
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/behnam-rk/dezhban/internal/armed"
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
	"github.com/behnam-rk/dezhban/internal/render"
	"github.com/behnam-rk/dezhban/internal/runner"
	"github.com/behnam-rk/dezhban/internal/state"
	"github.com/behnam-rk/dezhban/internal/svc"
	"github.com/behnam-rk/dezhban/internal/token"
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
  block       Manually block all outbound traffic
  unblock     Remove dezhban's firewall rules
  status      Show version, config, and current state
  validate    Load and validate a config file (no root, no side effects)
  monitor     Live read-only view: IP, country, tunnel state, endpoints, verdict
  print-rules Print the firewall ruleset a block/guard would apply, without applying it
  doctor      Diagnose VPN guard config (tunnels, endpoints, lockout risks)
  panic       Force-remove dezhban's rules even if nothing is running
  install     Register dezhban as a boot-persistent OS service
  uninstall   Remove the OS service
  start       Start the installed service
  stop        Stop the installed service (removes firewall rules)
  restart     Restart the installed service (apply a config change)
  detect-vpn  Print detected VPN tunnel interfaces to help fill the vpn config
  switch      Open a bounded window to connect a brand-new VPN (learns its server)
  pause       Open a bounded pause: real ISP IP for a while, then re-arms itself
  resume      End an open pause early
  hold        Keep the next VPN drop cut: no automatic redial window
  vpn         Manage VPN profiles and learned endpoints (list/add/remove/import/…)
  setup       Interactive wizard to create or update the config
  config      Inspect or change the config without hand-editing JSON
  token       Enroll/remove the control token that authorises password-free config changes
  completion  Print a shell completion script (bash|zsh|fish)
  upgrade     Check/download/apply a newer release (check: no root; apply: macOS, root)
  version     Print the version

Global flags:
  -v, --verbose   Override the configured log level to debug
  --no-sudo       Don't auto-elevate; print the root error instead
  --no-daemon     Don't use the control socket; act on the firewall directly

block, unblock, switch, pause, resume and hold ask the running dezhban over its
control socket, which needs no password (see the "control socket" line in
dezhban status). With nothing listening, block/unblock fall back to acting on
the firewall directly; switch/pause/resume/hold fall back to the root-owned
command file, which needs a running dezhban to consume it — either way, needing
root.

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
	case "pause":
		return cmdPause(rest)
	case "resume":
		return cmdResume(rest)
	case "hold":
		return cmdHold(rest)
	case "vpn":
		return cmdVPN(rest)
	case "setup":
		return cmdSetup(rest)
	case "config":
		return cmdConfig(rest)
	case "token":
		return cmdToken(rest)
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

// reportRetired warns once per config key that is not what the schema calls it:
// keys that were retired, keys that were renamed, keys the schema does not
// recognise at all, and keys misspelled only in letter case. The first three
// share one property — the operator wrote a setting and it is doing nothing —
// and this is the only signal they get; silence would let someone believe a
// discarded security setting took effect. The fourth is the opposite and gets
// the opposite wording: its value IS live, so warning that it "has no effect"
// would be the same lie in reverse.
func reportRetired(cfg *config.Config, log *slog.Logger) {
	for _, r := range cfg.Retired {
		if r.TookEffect {
			log.Warn("config key is misspelled but took effect", "key", r.Key, "why", r.Reason)
			continue
		}
		log.Warn("config key has no effect", "key", r.Key, "why", r.Reason)
	}
}

// loadConfig is a small helper shared by the commands that take --config. It
// resolves the path (so --config can be omitted) before loading.
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

// stampAndRender finalizes a snapshot the way the daemon's publish closure
// does: stamping the running build version and attaching the rendered
// Display, so a state.json (and therefore status --json and the menubar app)
// never carries a Display computed from a different Snapshot than the one it
// sits beside. Factored out of the closure in cmdRun so it can be tested
// without standing up the whole run loop.
func stampAndRender(s state.Snapshot, version string) state.Snapshot {
	s.Version = version
	d := render.Text(s)
	s.Display = &state.Display{Key: d.Key, Headline: d.Headline, Detail: d.Detail}
	return s
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
		return assembleOptions(cfg, resolveConfigPath(*cfgPath), l, ov)
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
// cfgPath is the already-resolved path the daemon read cfg from — kept so a
// live reload re-reads exactly the same file, rather than re-running resolution
// and possibly landing on a different one mid-run. Empty means built-in
// defaults, which is a config that cannot change, so reloading is not offered.
func assembleOptions(cfg *config.Config, cfgPath string, log *slog.Logger, ov runOverrides) (runner.Options, error) {
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

	// Arm-at-boot record: whether a configured tunnel has ever been observed up
	// on this host (internal/armed). Loaded once — unlike learned.json this is
	// consulted only at startup, and re-loading on every access would just
	// re-read the same latched fact. armAtBoot is forced off on Windows: the
	// WFP renderer's -InterfaceAlias rules are not verified to accept an
	// interface name that does not exist yet, so arming a rule set naming a
	// not-yet-present tunnel there is unproven — see docs/adr/0008-arm-at-boot.md.
	armedPath := defaultArmedPath()
	armAtBoot := cfg.VPN.ArmAtBoot
	if armAtBoot && runtime.GOOS == "windows" {
		armAtBoot = false
		log.Warn("vpn.armAtBoot has no effect on Windows yet — staying in standby until the tunnel appears")
	}
	armedRec, aerr := armed.Load(armedPath)
	if aerr != nil {
		log.Warn("armed record unreadable; treating this host as never having seen a tunnel", "err", aerr)
	}
	markTunnelUp := func(t time.Time) {
		if merr := armed.MarkUp(armedPath, t); merr != nil {
			log.Debug("armed record write failed", "err", merr)
		}
	}

	// Switch-window / pause control: poll the root-owned command file. Wired
	// unconditionally, because vpn.switchWindow and vpn.pauseMax are both
	// live-appliable — a daemon that started with them disabled must still hear a
	// command once one is re-enabled, or the reload would report the key as
	// applied while the root path stayed deaf until a restart. The runner gates
	// every command on the live value, so wiring the poller enables nothing by
	// itself. Discarding a stale file from a prior run is unconditional for the
	// same reason.
	commandPath := defaultCommandPath()
	if derr := command.Discard(commandPath); derr != nil {
		log.Debug("discard stale command file failed", "err", derr)
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
		s = stampAndRender(s, buildStamp.Version)
		if err := state.Write(statePath, s); err != nil {
			log.Debug("state publish failed", "path", statePath, "err", err)
		}
	}

	// Live reload: re-read the same file and hand the run loop what it can adopt,
	// plus the names of the keys it cannot. `running` tracks the configuration
	// actually in force so successive reloads diff against reality rather than
	// against whatever was on disk at startup.
	//
	// Restart-required keys are reported, never applied. Telling a user their
	// setting took effect while the daemon still enforces the old value is the
	// same failure as silently discarding it.
	running := *cfg
	var reload func() (runner.LiveSettings, runner.ReloadReport, error)
	if cfgPath != "" {
		reload = func() (runner.LiveSettings, runner.ReloadReport, error) {
			next, lerr := config.Load(cfgPath)
			if lerr != nil {
				// A malformed edit must not disturb a running kill switch: report
				// the parse failure and keep enforcing what is already in force.
				return runner.LiveSettings{}, runner.ReloadReport{}, lerr
			}
			live, needRestart := config.SplitByRestart(config.Changes(&running, next))
			report := runner.ReloadReport{
				Applied:      changeKeys(live),
				NeedsRestart: changeKeys(needRestart),
			}
			// Only the live half is adopted, so `running` keeps restart-required
			// keys at the values still being enforced. A later reload therefore
			// keeps reporting them as pending until a restart actually lands.
			prev := running
			running = *config.MergeLive(&running, next)
			ls := liveSettingsFrom(&running)
			if !deciderChanged(&prev, &running) {
				// Withhold the replacement: adopting one resets the hysteresis
				// streak, and a reload that did not touch the country list or
				// hysteresis must not cancel a flip already being counted toward.
				ls.Decider = nil
			}
			return ls, report, nil
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
			// The token verifier is wired whenever the socket exists, never
			// conditionally on control.allowConfigOps: that key is live-reloadable,
			// so a verifier installed only when it happened to be true at startup
			// would leave the op permanently refused after someone turned it on.
			// Policy is enforced in the run loop, where the current value lives.
			tokenPath := defaultTokenPath()
			ctl.VerifyToken = func(presented string) bool {
				ok, err := token.Verify(tokenPath, presented)
				if err != nil {
					// Including ErrNotEnrolled: no enrollment means the feature is
					// unavailable, not that anything goes.
					log.Debug("control: token verification refused", "err", err)
					return false
				}
				return ok
			}
			log.Info("control socket listening",
				"path", ctl.Path(),
				"group", cfg.Control.Group,
				"switch_ops", cfg.Control.AllowSwitchOps,
				"config_ops", cfg.Control.AllowConfigOps,
				"token_enrolled", token.Enrolled(tokenPath),
			)
		}
	}

	// Config writes over the socket need somewhere to write. With no config file
	// resolved the daemon is running on built-in defaults, and inventing a file
	// for it to persist into would change which config a later start reads —
	// so the op is simply unavailable, and says so by name.
	var writeConfigKeysAt func(map[string]string) error
	if cfgPath != "" {
		writeConfigKeysAt = func(pairs map[string]string) error {
			return writeConfigKeys(cfgPath, pairs)
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
		PauseMax:          cfg.VPN.PauseMax,
		AllowPauseOps:     cfg.Control.AllowPauseOps,
		Tunnels:           tunnels,
		AutoDetect:        cfg.VPN.AutoDetect,
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
		ArmAtBoot:               armAtBoot,
		TunnelEverUp:            armedRec.TunnelEverUp,
		MarkTunnelUp:            markTunnelUp,
		Watcher:                 watcher,
		WindowProtos:            adv.WindowProtocols,
		WindowPorts:             adv.WindowPorts,
		WindowDiscoveryInterval: adv.WindowDiscoveryInterval,
		SwitchWindow:            cfg.VPN.SwitchWindow,
		SwitchWindowMax:         adv.SwitchWindowMax,
		RedialWindow:            cfg.VPN.RedialWindow,
		RedialWindowMax:         adv.RedialWindowMax,
		RedialMinUptime:         adv.RedialMinUptime,
		RedialBudget:            adv.RedialBudget,
		RedialBudgetWindow:      adv.RedialBudgetWindow,
		Learn:                   learnHook,
		PollCommand:             pollCommand,
		Publish:                 publish,
		BlockedCountries:        cfg.BlockedCountries,
		ReloadConfig:            reload,
		WriteConfig:             writeConfigKeysAt,
		AllowConfigOps:          cfg.Control.AllowConfigOps,
	}, nil
}

// changeKeys reduces a change list to the key names a caller reports back.
func changeKeys(changes []config.Change) []string {
	if len(changes) == 0 {
		return nil
	}
	out := make([]string, 0, len(changes))
	for _, ch := range changes {
		out = append(out, ch.Key)
	}
	return out
}

// deciderChanged reports whether the country decider has to be rebuilt: only the
// blocked-country list and the hysteresis count feed it.
//
// It gates the rebuild because adopting a fresh Decider resets the in-progress
// agreement streak. That is correct when either input changed — readings counted
// under the old list say nothing about the new one — and wrong otherwise: an
// unrelated config edit would cancel an escalation to FULL BLOCK, or a recovery,
// that real readings were already counting toward, and a caller writing settings
// once per poll interval could defer a flip indefinitely.
func deciderChanged(prev, cur *config.Config) bool {
	return prev.Hysteresis != cur.Hysteresis ||
		!slices.Equal(prev.BlockedCountries, cur.BlockedCountries)
}

// liveSettingsFrom maps a config to the settings a running loop can adopt. It is
// the reload-time counterpart of the same fields in assembleOptions, and a test
// pins the two together so a key cannot be wired at startup but forgotten on
// reload — which would silently revert it on the next config edit.
func liveSettingsFrom(cfg *config.Config) runner.LiveSettings {
	adv := cfg.VPN.Advanced
	return runner.LiveSettings{
		Interval:                cfg.PollInterval,
		Decider:                 decision.New(cfg.BlockedCountries, cfg.Hysteresis),
		BlockedCountries:        cfg.BlockedCountries,
		AllowPhysicalDNS:        cfg.VPN.AllowPhysicalDNS,
		AllowLocalNetwork:       cfg.VPN.AllowLocalNetwork,
		AutoArm:                 cfg.VPN.AutoArm,
		SwitchWindow:            cfg.VPN.SwitchWindow,
		SwitchWindowMax:         adv.SwitchWindowMax,
		RedialWindow:            cfg.VPN.RedialWindow,
		RedialWindowMax:         adv.RedialWindowMax,
		RedialMinUptime:         adv.RedialMinUptime,
		RedialBudget:            adv.RedialBudget,
		RedialBudgetWindow:      adv.RedialBudgetWindow,
		PauseMax:                cfg.VPN.PauseMax,
		WindowDiscoveryInterval: adv.WindowDiscoveryInterval,
		EndpointRefresh:         cfg.VPN.EndpointRefresh,
		EndpointGrace:           cfg.VPN.EndpointGrace,
		AllowSwitchOps:          cfg.Control.AllowSwitchOps,
		AllowPauseOps:           cfg.Control.AllowPauseOps,
		AllowConfigOps:          cfg.Control.AllowConfigOps,
	}
}

// buildWatcher constructs the tunnel watcher, or returns nil when there is
// nothing to watch. It exists whenever tunnels are configured/autodetected —
// its up/down samples are what drive the guard's posture decisions — or when a
// tunnel-drop simulation is requested.
func buildWatcher(cfg *config.Config, log *slog.Logger, tunnels []string, ov runOverrides) *netdetect.Watcher {
	if len(tunnels) == 0 && !cfg.VPN.AutoDetect && !ov.tunnelDownSet {
		return nil
	}
	// In autodetect mode the watcher must sample ALL tunnel-like interfaces, not
	// just the set known at startup: utunN names change across redials, so
	// pinning the watcher to the start-time list (which liveSample treats as an
	// allowlist) would blind it to a renumbered or newly-created tunnel and stop
	// the runner growing/pruning its guarded set. An empty Tunnels makes
	// liveSample consider every interface; the runner still starts from `tunnels`.
	// With autodetect off, explicit pins keep their allowlist semantics.
	watchTunnels := tunnels
	if cfg.VPN.AutoDetect {
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
	guard := fs.Bool("guard", false, "apply the VPN interface guard (pass tunnel + endpoint, block other traffic)")
	force := fs.Bool("force", false, "force a hard full block of all traffic, bypassing the VPN guard state machine")
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
				fmt.Println("blocked — held until `dezhban unblock`")
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
			log.Error("vpn mode needs tunnel interfaces (vpn.tunnelInterfaces or vpn.autoDetect)")
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
// config values always win; when none are set and vpn.autoDetect is enabled, it
// discovers them via netdetect. It may return empty (autodetect found nothing) —
// callers must treat an empty guard set as a hard error, never proceed (an empty
// guard would be a total lockout).
func resolveTunnels(cfg *config.Config, log *slog.Logger) []string {
	if len(cfg.VPN.TunnelInterfaces) > 0 {
		return cfg.VPN.TunnelInterfaces
	}
	if !cfg.VPN.AutoDetect {
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
	// NOTE: `block --force` resolves this once, at block time, so a provider
	// that rotates CDN IPs mid-block becomes unreachable until the next block.
	// (The guard postures don't use this allowlist at all — they pass endpoints,
	// and refresh geo providers while healthy.)
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
				fmt.Println("unblocked — monitoring resumed")
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

// defaultArmedPath is the daemon-owned record of whether a configured tunnel
// has ever been observed up on this host — the persisted fact vpn.armAtBoot
// arms from. See internal/armed and docs/adr/0008-arm-at-boot.md.
func defaultArmedPath() string { return filepath.Join(stateDir(), "armed.json") }

// defaultTokenPath is the root-owned hash of the enrolled control token. It sits
// in the daemon's state dir with the other root-owned records, and deliberately
// NOT beside the world-readable state.json content: anything that could read it
// could forge the proof it exists to check. See internal/token.
func defaultTokenPath() string { return filepath.Join(stateDir(), "control.token") }

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
	fmt.Println("recommended config (autoDetect handles interface renumbering across redials):")
	fmt.Println(`  "vpn": {`)
	fmt.Println(`    "enabled": true,`)
	fmt.Println(`    "autoDetect": true,`)
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
	// Inert keys — retired, renamed, or simply unrecognised — are not an error;
	// the config is valid and will run. But `validate` is exactly where someone
	// checks whether their file says what they think it says, so a key that does
	// nothing belongs here more than anywhere. A key misspelled only in letter
	// case belongs here for the opposite reason: it is doing something, under a
	// name the file does not admit to.
	for _, r := range cfg.Retired {
		if r.TookEffect {
			fmt.Printf("\n  note: %q is not the schema's spelling, but it TOOK EFFECT.\n        %s\n", r.Key, r.Reason)
			continue
		}
		fmt.Printf("\n  note: %q has no effect.\n        %s\n", r.Key, r.Reason)
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
	// Every posture here is a VPN posture, so Policy.Allowlist stays empty — a
	// posture opens endpoints, not a physical dst-IP allowlist (that field is
	// live only for `block --force`, which builds its Policy directly). Input
	// construction is deferred into a closure because resolving endpoints does
	// DNS: the error branches below must not pay for network work (and log
	// resolution failures) for a ruleset that will never render.
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
	mode := fs.String("mode", "guard", "policy to render: guard, fullblock, or switch")
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

// checkStatus classifies one doctorReport check for a machine consumer (the
// macOS Diagnostics pane) without it having to parse Summary/Details prose.
type checkStatus string

const (
	checkOK   checkStatus = "ok"
	checkWarn checkStatus = "warn"
	checkFail checkStatus = "fail"
)

// doctorCheck is one section of `doctor`'s output, structured. Details/Fixes
// are the exact lines printDoctor prints under the check's header — kept as
// data so a second renderer (the GUI's Diagnostics pane, over --json) never
// has to reparse human prose to find out what's wrong.
type doctorCheck struct {
	Name    string      `json:"name"`
	Status  checkStatus `json:"status"`
	Summary string      `json:"summary"`
	// Details are the check's findings, one line each. An EMPTY string is a
	// paragraph break, not a finding — the only piece of layout this contract
	// carries, and it is here because both renderers need it: printDoctor emits
	// a blank line, and the GUI's checkRow emits vertical space. A renderer that
	// treats it as an ordinary line gets a stray empty row, so new consumers
	// must handle it. Anything more elaborate than a break belongs in Fixes.
	Details []string `json:"details,omitempty"`
	// Fixes are the commands or actions that resolve the check, never prose
	// about them — the GUI badges each one, so a sentence dressed as a fix
	// reads as a command the user should run.
	Fixes []string `json:"fixes,omitempty"`
}

// doctorReport is `doctor`'s complete findings, machine-readable via
// `doctor --json` and human-readable via printDoctor — the same data render
// two ways, so the CLI and the GUI can never disagree about what doctor found.
type doctorReport struct {
	Checks []doctorCheck `json:"checks"`
	OK     bool          `json:"ok"`
}

// buildTunnelsCheck formats the resolved tunnel set and their subnets (already
// looked up by the caller — this function does no I/O) into a doctorCheck.
// Pure, so the "no subnet" and "has a subnet" branches are directly testable
// without a real OS network interface.
func buildTunnelsCheck(tunnels []string, nets []netdetect.TunnelNet) doctorCheck {
	c := doctorCheck{Name: "tunnels", Status: checkOK}
	if len(tunnels) == 0 {
		c.Status = checkWarn
		c.Details = []string{"(none — set vpn.tunnelInterfaces or vpn.autoDetect)"}
		return c
	}
	subsByIface := map[string][]string{}
	for _, tn := range nets {
		subsByIface[tn.Iface] = append(subsByIface[tn.Iface], tn.Subnet.String())
	}
	for _, t := range tunnels {
		if subs := subsByIface[t]; len(subs) > 0 {
			c.Details = append(c.Details, fmt.Sprintf("%s — %s", t, strings.Join(subs, ", ")))
		} else {
			c.Details = append(c.Details, fmt.Sprintf("%s — no subnet (interface down or absent?)", t))
		}
	}
	return c
}

// buildEndpointsCheck formats the resolved endpoint set and any misrouted
// ("bad") ones (already computed by the caller) into a doctorCheck. Pure, so
// the MISCONFIGURED/fix-text formatting is directly testable with a synthetic
// `bad` slice — real subnet containment needs a live OS tunnel interface,
// which a portable unit test cannot depend on.
func buildEndpointsCheck(endpoints []netip.Addr, bad []netdetect.EndpointRoute) doctorCheck {
	c := doctorCheck{Name: "endpoints", Status: checkOK}
	if len(endpoints) == 0 {
		c.Status = checkWarn
		c.Details = []string{"(none resolved)"}
		return c
	}
	internal := map[string]netdetect.EndpointRoute{}
	for _, b := range bad {
		internal[b.Endpoint.String()] = b
	}
	for _, ep := range endpoints {
		if b, ok := internal[ep.String()]; ok {
			c.Details = append(c.Details, fmt.Sprintf("%s — MISCONFIGURED: inside %s's subnet %s", ep, b.Iface, b.Subnet))
		} else {
			c.Details = append(c.Details, fmt.Sprintf("%s — ok (assumed reachable on the physical interface)", ep))
		}
	}
	if len(bad) > 0 {
		c.Status = checkFail
		for _, b := range bad {
			c.Fixes = append(c.Fixes,
				fmt.Sprintf("%s is a tunnel-internal address (inside %s %s); set vpn.endpoints to\n"+
					"    your VPN server's PUBLIC IP from your VPN client config.", b.Endpoint, b.Iface, b.Subnet))
		}
	}
	return c
}

// buildLockoutCheck formats the "guard would block its own tunnel's transport"
// warning. Pure — the caller decides whether the lockout condition holds.
func buildLockoutCheck(tunnels []string) doctorCheck {
	return doctorCheck{
		Name:    "lockout",
		Status:  checkFail,
		Summary: "dezhban will refuse to start",
		Details: []string{
			fmt.Sprintf("The VPN guard is on and %s is up, but no server address is known.", strings.Join(tunnels, ", ")),
			"The guard would block the tunnel's own transport and cut ALL traffic.",
			"",
			"Auto-discovery reads CONNECTED sockets. WireGuard (and other",
			"NetworkExtension clients) send from an UNCONNECTED UDP socket, so they",
			"never appear as a connected flow — discovery cannot find them. Name the",
			"server explicitly:",
		},
		Fixes: []string{
			"dezhban vpn import <wg0.conf|client.ovpn>   # reads the endpoint from it",
			"dezhban vpn add <name> --endpoint <host-or-ip>",
			"sudo dezhban config set vpn.endpoints=<server-ip>",
		},
	}
}

// buildServiceCheck answers "will dezhban be there after I reboot". Pure — the
// caller supplies the unit facts (svc.Boot) and whether a daemon looks alive
// right now (the same staleness rule `status` and the app use).
//
// The two questions are deliberately separate. "Something is enforcing now" and
// "something will enforce after a reboot" have different answers and different
// fixes, and conflating them is what makes the reported symptom — "I have to
// turn it on again after every reboot" — so hard to place: it is equally
// consistent with no boot service, a boot service that is installed but not
// enabled, and a perfectly good boot service whose only absentee is the menubar
// app at login. This check separates the three by name.
func buildServiceCheck(unit svc.BootUnit, daemonLive bool) doctorCheck {
	c := doctorCheck{Name: "service", Status: checkOK}

	if !unit.Determinable {
		c.Status = checkWarn
		// Deliberately does not say "not installed" or name a platform: this is
		// reached both where no unit file exists to read (Windows) and where one
		// may exist but could not be read. Guessing between them is how a
		// correctly-installed user gets told to reinstall.
		c.Summary = "cannot tell without asking the service manager."
		c.Details = []string{"Nothing readable here says what happens at boot. Ask it directly (needs root):"}
		c.Fixes = []string{"sudo dezhban status"}
		return c
	}

	switch {
	case !unit.Present:
		c.Status = checkWarn
		c.Summary = "not registered to start at boot."
		c.Details = []string{
			fmt.Sprintf("No service unit at %s, so nothing", unit.Path),
			"arms the guard after a reboot until you start dezhban by hand.",
		}
		if daemonLive {
			c.Details = append(c.Details,
				"",
				"dezhban IS enforcing right now — this is about reboots, not about",
				"the guard being off today.")
		}
		c.Fixes = []string{"sudo dezhban install"}

	case !unit.AtBoot:
		c.Status = checkWarn
		c.Summary = "installed, but not set to start at boot."
		c.Details = []string{
			fmt.Sprintf("%s exists but does not ask", unit.Path),
			"the service manager to start dezhban at boot, so `start` works and",
			"every reboot comes up unguarded. Reinstalling rewrites the unit:",
		}
		c.Fixes = []string{"sudo dezhban install"}

	case !daemonLive:
		c.Status = checkWarn
		c.Summary = "set to start at boot, but nothing is enforcing right now."
		c.Details = []string{
			"The next reboot will arm the guard. Until then this host is unguarded.",
		}
		c.Fixes = []string{"sudo dezhban start"}

	default:
		c.Summary = "registered to start at boot, and enforcing now."
		// The point of saying this out loud: it rules out the enforcement
		// explanation for "I have to turn it on after every reboot" and leaves
		// only the presentation one, which has an entirely different fix.
		c.Details = []string{
			"If the menubar app is missing after a login, that is a login-item",
			"question — the guard is already up without it.",
		}
	}
	return c
}

// buildArmAtBootCheck reports whether the NEXT boot arms the guard immediately
// or opens into standby until a live tunnel probe succeeds. Pure.
//
// vpn.armAtBoot may only override standby's live probe when an endpoint is known
// AND a configured tunnel has been observed up at least once on this host — the
// second half being the fact armed.json persists (ADR-0008). Both halves are
// silent when they fail: the setting reads as "on" in the config while the
// precondition behind it never holds, and every reboot re-opens for however long
// the VPN takes to redial. Naming which half is missing is this check's whole job.
func buildArmAtBootCheck(armAtBoot bool, haveTunnel bool, rec *armed.Record, loadErr error, path string) doctorCheck {
	c := doctorCheck{Name: "armAtBoot", Status: checkOK}

	// A corrupt record is not a crash — armed.Load hands back a zero value so
	// the daemon treats the host as never having seen a tunnel — but it IS the
	// state in which arm-at-boot silently stops working, so it outranks the
	// config setting below.
	if loadErr != nil {
		c.Status = checkWarn
		c.Summary = "the arm-at-boot record could not be read; boot will fall back to standby."
		c.Details = []string{
			loadErr.Error(),
			"",
			"dezhban treats an unreadable record as \"no tunnel has ever been up\",",
			"which is safe but means the next reboot waits for a live tunnel instead",
			"of arming straight away. dezhban rewrites it the next time a tunnel",
			"comes up.",
		}
		return c
	}

	if !armAtBoot {
		c.Status = checkWarn
		c.Summary = "off — after a reboot the guard waits for a live tunnel before arming."
		c.Details = []string{
			"That leaves a gap between boot and the VPN connecting, during which",
			"traffic uses your real address. Turning it on closes the gap on a host",
			"whose VPN has already worked once.",
		}
		c.Fixes = []string{"sudo dezhban config set vpn.armAtBoot=true"}
		return c
	}

	if !rec.TunnelEverUp {
		c.Status = checkWarn
		c.Summary = "on, but no tunnel has been observed up yet, so it cannot arm."
		c.Details = []string{
			fmt.Sprintf("The record at %s has not seen a tunnel come up on this host.", path),
			"Arm-at-boot needs that observation — arming without it would fail closed",
			"on a machine that has never had a working VPN, which is a lockout by",
			"design rather than a guard.",
			"",
		}
		if haveTunnel {
			c.Details = append(c.Details,
				"Connect your VPN once with dezhban running and this becomes permanent.")
		} else {
			c.Details = append(c.Details,
				"Configure a tunnel first, then connect it once with dezhban running.")
		}
		return c
	}

	c.Summary = "on — the next reboot arms the guard without waiting for a tunnel."
	c.Details = []string{
		fmt.Sprintf("A tunnel was first seen up %s and last seen %s.",
			rec.FirstUp.Local().Format(time.RFC1123), rec.LastUp.Local().Format(time.RFC1123)),
	}
	return c
}

// buildEndpointRetentionCheck reports on the learned-endpoint store, which is
// what lets a dropped tunnel redial with no window at all: the guard passes
// known server addresses on the physical link, so a drop whose endpoint is still
// known needs no relaxation and no interaction. Pure.
//
// When someone is being forced to open a window by hand after every drop, the
// cause is almost always in here, and it is one of two opposite things:
// retention that is too short (addresses were learned and then thrown away), or
// a VPN that rotates its server address (they were learned and are simply never
// the same twice). The remedies point in opposite directions, so the check names
// which one it is rather than printing counts and leaving the reader to guess.
func buildEndpointRetentionCheck(store *learned.Store, loadErr error, ttl time.Duration, maxPerProfile int, staticEndpoints int, now time.Time) doctorCheck {
	c := doctorCheck{Name: "endpointRetention", Status: checkOK}

	if loadErr != nil {
		c.Status = checkWarn
		c.Summary = "the learned-endpoint store could not be read; every drop starts from nothing."
		c.Details = []string{loadErr.Error()}
		return c
	}

	total := 0
	for _, e := range store.Entries {
		total += len(e.Endpoints)
	}
	if total == 0 {
		if staticEndpoints > 0 {
			c.Summary = "nothing learned yet — the configured server addresses cover the guard."
			return c
		}
		c.Status = checkWarn
		c.Summary = "nothing learned, and no server address configured either."
		c.Details = []string{
			"A drop has no known address to redial through, so it needs a window",
			"every time. Naming the server once removes the interaction entirely.",
		}
		c.Fixes = []string{"dezhban vpn add <name> --endpoint <host-or-ip>"}
		return c
	}

	var rotating, staleOnly []string
	for _, e := range store.Entries {
		fresh, recentlyNew := 0, 0
		for _, ep := range e.Endpoints {
			if ttl <= 0 || now.Sub(ep.LastSeen) <= ttl {
				fresh++
			}
			// A FirstSeen inside the retention window means this address is not
			// one dezhban has been reusing — it is one it met for the first time
			// recently. Many of those at once is what rotation looks like from
			// in here; the store cannot report addresses it has already pruned,
			// so first-sightings are the honest proxy for churn.
			if ttl <= 0 || now.Sub(ep.FirstSeen) <= ttl {
				recentlyNew++
			}
		}
		c.Details = append(c.Details, fmt.Sprintf("%s — %d stored, %d within the %s retention window",
			e.Name, len(e.Endpoints), fresh, ttl))

		switch {
		case fresh == 0:
			staleOnly = append(staleOnly, e.Name)
		case maxPerProfile > 0 && len(e.Endpoints) >= maxPerProfile && recentlyNew > maxPerProfile/2:
			rotating = append(rotating, e.Name)
		}
	}

	switch {
	case len(staleOnly) > 0:
		c.Status = checkWarn
		c.Summary = fmt.Sprintf("every learned address for %s has aged out.", strings.Join(staleOnly, ", "))
		c.Details = append(c.Details, "",
			"They were learned and then discarded, so the next drop redials with",
			"nothing known and needs a window. Retaining them for longer removes",
			"that interaction.")
		c.Fixes = []string{"sudo dezhban config set vpn.advanced.learnedEndpointTTL=720h"}

	case len(rotating) > 0:
		c.Status = checkWarn
		c.Summary = fmt.Sprintf("%s looks like it rotates its server address.", strings.Join(rotating, ", "))
		c.Details = append(c.Details, "",
			"The store is full and most of what is in it was seen for the first time",
			"recently, which means the address is rarely the same twice. Retaining",
			"more of them only delays the problem — a hostname is the real fix,",
			"because dezhban re-resolves it on vpn.endpointRefresh and follows the",
			"rotation instead of chasing it.")
		c.Fixes = []string{
			"dezhban vpn add <name> --endpoint <server-hostname>",
			"sudo dezhban config set vpn.advanced.learnedMaxPerProfile=32",
		}

	default:
		c.Summary = fmt.Sprintf("%d address(es) retained — a drop can redial without a window.", total)
	}
	return c
}

// runDoctor builds the report: validates config, lists tunnel interfaces and
// their subnets, and flags any endpoint that sits inside a tunnel's own subnet
// (a guaranteed lockout). With discover=true it additionally runs the
// macOS-only best-effort hunt for the connected VPN's real server IP,
// automating the manual netstat/scutil dance.
//
// Its I/O is what resolveTunnels/resolveEndpointsOnce/netdetect do, plus three
// unprivileged reads of files dezhban itself owns — the state snapshot, the
// arm-at-boot record, and the learned-endpoint store. Every check derived from
// those is built by a pure function taking the loaded value, so the diagnosis
// is testable without any of them existing. No printing.
func runDoctor(cfg *config.Config, log *slog.Logger, discover bool) doctorReport {
	var checks []doctorCheck

	checks = append(checks, doctorCheck{
		Name: "config", Status: checkOK, Summary: "OK (loaded and validated)",
	})

	tunnels := resolveTunnels(cfg, log)
	nets, _ := netdetect.TunnelSubnets(tunnels)
	checks = append(checks, buildTunnelsCheck(tunnels, nets))

	endpoints := resolveEndpointsOnce(cfg, log, tunnels)
	var bad []netdetect.EndpointRoute
	if len(endpoints) > 0 {
		bad, _ = netdetect.CheckEndpointRouting(endpoints, tunnels)
	}
	checks = append(checks, buildEndpointsCheck(endpoints, bad))

	// The guard blocks ALL egress on the physical link — which is what carries the
	// tunnel's own encrypted transport. With a tunnel up and no known server address,
	// arming it cuts every packet, kills the VPN, and leaves no socket for discovery to
	// learn from: an unrecoverable blackout, not a kill switch. The daemon refuses to
	// start in this state; doctor's whole job is to say so BEFORE you find out.
	lockout := len(tunnels) > 0 && len(endpoints) == 0
	if lockout {
		checks = append(checks, buildLockoutCheck(tunnels))
	}

	// Will this host guard itself again after a reboot, and can a drop redial
	// without asking anyone? Both are answered from unprivileged reads —
	// svc.Boot reads the unit file rather than querying the service manager
	// (which cannot answer truthfully to a non-root caller on macOS), and the
	// two daemon-owned records are 0644 exactly so the CLI can read them.
	//
	// Informational, like touchID: none of these is a lockout risk, so none of
	// them moves the exit code. They diagnose interaction that should not have
	// been necessary, not a guard that is about to fail closed.
	now := time.Now()
	snap, snapErr := state.Read(defaultStatePath())
	daemonLive := snapErr == nil && !render.IsStale(snap, now)
	checks = append(checks, buildServiceCheck(svc.Boot(), daemonLive))

	armedPath := defaultArmedPath()
	armedRec, armedErr := armed.Load(armedPath)
	checks = append(checks, buildArmAtBootCheck(cfg.VPN.ArmAtBoot, len(tunnels) > 0, armedRec, armedErr, armedPath))

	store, learnedErr := learned.Load(defaultLearnedPath())
	checks = append(checks, buildEndpointRetentionCheck(store, learnedErr,
		cfg.VPN.Advanced.LearnedEndpointTTL, cfg.VPN.Advanced.LearnedMaxPerProfile,
		len(cfg.VPN.Endpoints), now))

	// Touch ID discoverability (macOS): privileged ops (start/stop/panic, GUI
	// actions) authenticate through sudo, and sudo only offers Touch ID when
	// pam_tid is opted in via /etc/pam.d/sudo_local. Informational only — never
	// affects the exit code; password auth is degraded UX, not a lockout risk.
	if runtime.GOOS == "darwin" && !sudoTouchIDConfigured() {
		checks = append(checks, doctorCheck{
			Name:    "touchID",
			Status:  checkWarn,
			Summary: "not configured for sudo — privileged ops will ask for a password.",
			Details: []string{"To authenticate with a fingerprint instead (survives OS updates):"},
			// The command is a Fix, not a Detail: it is the thing to run, so it
			// belongs where every other runnable line lives (and where the GUI
			// badges it) rather than as a detail line the CLI had to indent
			// differently from its siblings to make it look like one.
			Fixes: []string{"echo 'auth       sufficient     pam_tid.so' | sudo tee /etc/pam.d/sudo_local"},
		})
	}

	if discover {
		discoverCheck := doctorCheck{Name: "discover", Status: checkOK}
		cands, err := netdetect.DiscoverEndpoints()
		switch {
		// Summary only, never also as a Detail: the GUI renders Summary in the
		// row's title and Details beneath it, so setting both printed the same
		// sentence twice.
		case err != nil:
			discoverCheck.Status = checkWarn
			discoverCheck.Summary = err.Error()
		case len(cands) == 0:
			discoverCheck.Status = checkWarn
			discoverCheck.Summary = "no physical-side public transport sockets found — is the VPN connected?"
		default:
			configured := map[string]bool{}
			for _, ep := range endpoints {
				configured[ep.String()] = true
			}
			for _, c := range cands {
				line := fmt.Sprintf("%s:%d", c.Server, c.Port)
				if c.VPN != "" {
					line += " [" + c.VPN + "]"
				}
				if !configured[c.Server.String()] {
					line += "  <- not in vpn.endpoints"
				}
				discoverCheck.Details = append(discoverCheck.Details, line)
			}
			discoverCheck.Fixes = []string{"add any missing server IP to vpn.endpoints and drop stale entries."}
		}
		checks = append(checks, discoverCheck)
	}

	// A diagnostic that reports a guaranteed blackout and still exits 0 is one
	// `make doctor` in a script away from being ignored — and these are exactly
	// the two conditions the daemon refuses to start on, so doctor must agree
	// with it.
	return doctorReport{Checks: checks, OK: !(lockout || len(bad) > 0)}
}

// unattendedSections are the checks that answer "will dezhban need me again" —
// after a reboot, or after the next drop. Grouped because they read as one
// question and print as one block.
var unattendedSections = []struct{ name, heading string }{
	{"service", "boot service"},
	{"armAtBoot", "arm at boot"},
	{"endpointRetention", "learned endpoints"},
}

// sectionedChecks names every check printDoctor has a hand-written section for.
// The leftover printer at the bottom of printDoctor is a safety net for a check
// added without one — TestEveryCheckHasASection pins that runDoctor never
// actually needs it, so a new check gets a considered place in the layout
// instead of being appended, unformatted, after `discover`.
var sectionedChecks = []string{
	"config", "tunnels", "endpoints", "lockout",
	"service", "armAtBoot", "endpointRetention",
	"touchID", "discover",
}

// printDoctor renders a doctorReport in the text layout `doctor` has always
// printed — this function's job is to keep that layout, not to reinterpret it.
//
// It is byte-identical to the pre-doctorReport version with ONE deliberate
// exception: `--discover`'s error line used to be printed as
// `fmt.Println("  ", err)`, and Println's operand separator made that THREE
// spaces where every one of its sibling lines used two. That was a typo, not a
// layout, and it is the one branch the byte-for-byte comparison could not cover
// (DiscoverEndpoints only errors on a non-macOS host or a failed netstat), so
// it is normalised to two rather than reproduced.
func printDoctor(r doctorReport) {
	// Index by name, FIRST wins, and record which checks the fixed layout below
	// actually consumes. That closes two silent-drop holes at once: a second
	// check sharing a Name used to overwrite the first, and a check whose name
	// has no section here (one added to runDoctor tomorrow) would never print
	// at all. Both leftovers are printed generically at the end — a diagnostic
	// that quietly omits a finding is the one bug this command cannot afford,
	// since a dropped check reads exactly like a check that passed.
	byName := map[string]int{}
	shown := make([]bool, len(r.Checks))
	for i, c := range r.Checks {
		if _, dup := byName[c.Name]; !dup {
			byName[c.Name] = i
		}
	}
	get := func(name string) (doctorCheck, bool) {
		i, ok := byName[name]
		if !ok {
			return doctorCheck{}, false
		}
		shown[i] = true
		return r.Checks[i], true
	}

	fmt.Println("dezhban doctor")
	fmt.Println()
	cfgCheck, _ := get("config")
	fmt.Printf("config:  %s\n", cfgCheck.Summary)
	fmt.Println()

	fmt.Println("tunnels:")
	tun, _ := get("tunnels")
	printDetails(tun.Details)
	fmt.Println()

	fmt.Println("endpoints (resolved: literals + hostnames + discovery):")
	ep, _ := get("endpoints")
	printDetails(ep.Details)
	if len(ep.Fixes) > 0 {
		fmt.Println()
		fmt.Println("fixes:")
		for _, f := range ep.Fixes {
			fmt.Printf("  - %s\n", f)
		}
	}

	if lockout, ok := get("lockout"); ok {
		fmt.Println()
		fmt.Printf("LOCKOUT RISK — %s:\n", lockout.Summary)
		printDetails(lockout.Details)
		fmt.Println()
		for _, f := range lockout.Fixes {
			fmt.Printf("    %s\n", f)
		}
	}
	fmt.Println()

	// The three "will this need me again" checks share one shape — heading,
	// summary, details, fixes — so they share one printer rather than three
	// copies that would drift apart the first time one of them grew a line.
	for _, s := range unattendedSections {
		c, ok := get(s.name)
		if !ok {
			continue
		}
		fmt.Printf("%s: %s\n", s.heading, c.Summary)
		printDetails(c.Details)
		if len(c.Fixes) > 0 {
			fmt.Println()
			for _, f := range c.Fixes {
				fmt.Printf("    %s\n", f)
			}
		}
		fmt.Println()
	}

	if touchID, ok := get("touchID"); ok {
		fmt.Printf("touch id: %s\n", touchID.Summary)
		printDetails(touchID.Details)
		fmt.Println()
		for _, f := range touchID.Fixes {
			fmt.Printf("    %s\n", f)
		}
		fmt.Println()
	}

	if discover, ok := get("discover"); ok {
		fmt.Println("discover (best-effort, macOS):")
		// A degenerate discover run (an error, or nothing found) carries its one
		// line as Summary rather than duplicating it into Details, so print that
		// where the detail lines would have gone.
		if len(discover.Details) == 0 && discover.Summary != "" {
			fmt.Printf("  %s\n", discover.Summary)
		}
		printDetails(discover.Details)
		for _, f := range discover.Fixes {
			fmt.Printf("  %s\n", f)
		}
	}

	// Whatever the layout above did not consume, in the order runDoctor emitted
	// it. Normally empty — TestDoctorChecksHaveUniqueNames pins that the shipped
	// checks are unique and this function has a section for each — so reaching
	// this loop means a check was added without a home here, and it prints
	// plainly rather than vanishing.
	for i, c := range r.Checks {
		if shown[i] {
			continue
		}
		fmt.Printf("%s: %s\n", c.Name, c.Summary)
		printDetails(c.Details)
		for _, f := range c.Fixes {
			fmt.Printf("    %s\n", f)
		}
		fmt.Println()
	}
}

// printDetails prints a check's Details at the standard two-space indent,
// honouring the empty-string paragraph break (see doctorCheck.Details).
func printDetails(details []string) {
	for _, line := range details {
		if line == "" {
			fmt.Println()
			continue
		}
		fmt.Printf("  %s\n", line)
	}
}

// cmdDoctor diagnoses the VPN guard configuration without root or side effects.
// See runDoctor for what it checks; --json prints the same findings as
// doctorReport instead of the human layout, for the macOS Diagnostics pane.
func cmdDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to config file (JSON)")
	discover := fs.Bool("discover", false, "best-effort: find the connected VPN's real server IP (macOS only)")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON instead of the human report")
	_ = fs.Parse(args)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config invalid:", err)
		return 1
	}
	log := newLogger(cfg)

	report := runDoctor(cfg, log, *discover)
	if *jsonOut {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "doctor json:", err)
			return 1
		}
		fmt.Println(string(data))
	} else {
		printDoctor(report)
	}
	if !report.OK {
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
	snap, staterr := state.Read(defaultStatePath())
	switch {
	case staterr != nil:
		snap = state.Snapshot{Posture: render.PostureStopped}
	case render.IsStale(snap, time.Now()):
		// A crashed or SIGKILLed daemon leaves its last posture on disk
		// indefinitely — state.Read succeeding is not evidence anything is
		// still enforcing. Same staleness rule the macOS app's PostureUI
		// uses for its icon, so the CLI and the GUI agree on what "alive"
		// means rather than this headline saying "Guarding" while the
		// service line two rows down says otherwise.
		snap = state.Snapshot{Posture: render.PostureStopped}
	}
	disp := render.Text(snap)
	fmt.Printf("%s — %s\n", disp.Headline, disp.Detail)
	fmt.Println("privileged:      ", privilege.IsPrivileged())
	fmt.Println("service:         ", svc.Status())
	fmt.Println("control socket:  ", controlStatus(cfg))
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
		if len(tunnels) == 0 && cfg.VPN.AutoDetect {
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
		if cfg.VPN.SwitchWindow > 0 {
			fmt.Println("switch window:   ", cfg.VPN.SwitchWindow)
		} else {
			// The Disabled sentinel is negative — print "off", never "-1ns".
			fmt.Println("switch window:    off")
		}
		if cfg.VPN.RedialWindow > 0 {
			fmt.Println("redial window:", cfg.VPN.RedialWindow)
		} else {
			fmt.Println("redial window: off")
		}
		if cfg.VPN.PauseMax > 0 {
			fmt.Println("pause max:       ", cfg.VPN.PauseMax)
		} else {
			fmt.Println("pause max:        off")
		}
		// Live active-profile state from the daemon's snapshot. Window state and
		// the pending hysteresis streak are already in the headline/detail
		// printed above — repeating them here would be the same duplicated
		// rendering this command used to have.
		if staterr == nil && snap.ActiveProfile != "" {
			fmt.Println("active profile:  ", snap.ActiveProfile)
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
		Version    string `json:"version"`
		Commit     string `json:"commit,omitempty"`    // build stamp; empty outside a git tree
		BuildDate  string `json:"buildDate,omitempty"` // RFC3339
		Privileged bool   `json:"privileged"`
		Service    string `json:"service"`
		// ControlReachable is the machine-readable form of controlStatus's
		// sentence: whether routine ops will reach the daemon with no password
		// prompt. Added so a consumer (the macOS app) doesn't have to scrape the
		// human "daemon control:" status line for a substring.
		ControlReachable bool            `json:"controlReachable"`
		StatePath        string          `json:"statePath"`
		State            *state.Snapshot `json:"state,omitempty"`    // nil when no snapshot has been published yet
		StateAge         string          `json:"stateAge,omitempty"` // wall-clock age of the snapshot
		// StateStale is render.IsStale applied to State: the snapshot is too old
		// to trust, so its posture (and its embedded display sentences) describe
		// what a daemon was doing, not what one is doing now. A crashed or
		// SIGKILLed daemon leaves "guard"/"Guarding" on disk forever, so a
		// consumer that branches on `state.posture` alone will report a host as
		// protected indefinitely after enforcement stopped.
		//
		// The snapshot itself is passed through verbatim rather than overwritten
		// — it is a stable contract, and the raw last-known posture is worth
		// having — so this flag is how the prose `status` (which substitutes
		// "Stopped" outright) and this one avoid contradicting each other.
		// Always emitted, never omitempty: this is a safety-adjacent flag, and
		// `omitempty` would make "the snapshot is fresh" and "this CLI is too
		// old to have the field" the same absence on the wire. The sibling
		// advisory bools (ControlReachable, PauseEnabled) are emitted
		// unconditionally for the same reason.
		StateStale       bool     `json:"stateStale"`
		PollInterval     string   `json:"pollInterval"`
		BlockedCountries []string `json:"blockedCountries"`
		// PauseEnabled is whether `dezhban pause`/the control-socket pause op
		// will do anything (vpn.pauseMax > 0). A consumer (the macOS app) uses
		// this to grey out its own Pause control with a reason, advisory only —
		// same convention as ControlReachable — since the CLI/daemon still
		// refuse for real regardless of what this said last.
		PauseEnabled bool `json:"pauseEnabled"`
		// No `vpnEnabled`: with one enforcement model it could only ever be true,
		// and a constant field invites consumers to branch on nothing. Read
		// `state.posture` instead — that is where the real distinction lives.
	}{
		Version:          buildStamp.Version,
		Commit:           buildStamp.short(),
		BuildDate:        buildStamp.Date,
		Privileged:       privilege.IsPrivileged(),
		Service:          svc.Status(),
		ControlReachable: controlReachable(cfg),
		StatePath:        statePath,
		PollInterval:     cfg.PollInterval.String(),
		BlockedCountries: cfg.BlockedCountries,
		PauseEnabled:     cfg.VPN.PauseMax > 0,
	}
	if snap, err := state.Read(statePath); err == nil {
		out.State = &snap
		out.StateAge = time.Since(snap.Time).Round(time.Second).String()
		out.StateStale = render.IsStale(snap, time.Now())
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "status json:", err)
		return 1
	}
	fmt.Println(string(data))
	return 0
}
