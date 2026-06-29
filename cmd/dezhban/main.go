// Command dezhban is a cross-platform network kill switch: it watches the
// machine's public IP, resolves its country, and drives the OS firewall to cut
// traffic when the country matches a blocklist.
//
// Phase 0 wires the CLI skeleton, config, logging, and privilege checks. The
// monitor, decision, and firewall layers are filled in by later phases.
package main

import (
	"context"
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

	"github.com/behnam-rk/dezhban/internal/config"
	"github.com/behnam-rk/dezhban/internal/decision"
	"github.com/behnam-rk/dezhban/internal/firewall"
	"github.com/behnam-rk/dezhban/internal/logging"
	"github.com/behnam-rk/dezhban/internal/monitor"
	"github.com/behnam-rk/dezhban/internal/netdetect"
	"github.com/behnam-rk/dezhban/internal/privilege"
	"github.com/behnam-rk/dezhban/internal/runner"
	"github.com/behnam-rk/dezhban/internal/svc"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

// verbose is the global -v/--verbose flag, stripped from args before dispatch.
// When set it overrides the configured log level to debug.
var verbose bool

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
  detect-vpn  Print detected VPN tunnel interfaces to help fill the vpn config
  version     Print the version

Global flags:
  -v, --verbose   Override the configured log level to debug

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
	case "install", "uninstall", "start", "stop":
		return cmdService(cmd, rest)
	case "detect-vpn":
		return cmdDetectVPN(rest)
	case "version", "--version":
		fmt.Println("dezhban", version)
		return 0
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

// requireRoot prints a clear error and returns false if not privileged.
func requireRoot(cmd string) bool {
	if privilege.IsPrivileged() {
		return true
	}
	fmt.Fprintf(os.Stderr, "dezhban %s must run as root (try: sudo dezhban %s ...)\n", cmd, cmd)
	return false
}

// loadConfig is a small helper shared by the commands that take --config.
func loadConfig(path string) (*config.Config, error) {
	return config.Load(path)
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

	// Run under the service manager. When launched from a shell this behaves like
	// a foreground daemon (kardianos handles SIGINT/SIGTERM and calls Stop, which
	// cancels the loop so its deferred Cleanup removes all rules); when launched by
	// launchd/systemd/Windows it runs as the managed service and logs to the
	// platform logger. The build closure assembles the run loop lazily so it can
	// use whichever logger the service selects.
	build := func(l *slog.Logger) (runner.Options, error) {
		return assembleOptions(cfg, l, ov)
	}
	if err := svc.Run(build, log, effectiveLevel(cfg), *cfgPath); err != nil {
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

	log.Info("run loop started",
		"interval", cfg.PollInterval,
		"providers", len(providers),
		"blocked_countries", cfg.BlockedCountries,
		"fail_closed", cfg.FailClosed,
		"hysteresis", cfg.Hysteresis,
		"quorum", cfg.ProviderQuorum,
		"vpn", cfg.VPN.Enabled,
		"auto_discover_endpoints", cfg.VPN.AutoDiscoverEndpoints,
		"tunnel_watch", watcher != nil,
	)
	return runner.Options{
		Monitor:          mon,
		Decider:          decision.New(cfg.BlockedCountries, cfg.FailClosed, cfg.Hysteresis),
		Backend:          fw,
		Log:              log,
		Interval:         cfg.PollInterval,
		VPN:              cfg.VPN.Enabled,
		Tunnels:          tunnels,
		ResolveEndpoints: func(ctx context.Context) netdetect.EndpointSet { return epSrc.Resolve(ctx) },
		EndpointRefresh:  cfg.VPN.EndpointRefresh,
		Watcher:          watcher,
		// Re-resolve the allowlist at each Block so rotated provider IPs stay
		// reachable for recovery detection while egress is cut.
		Allowlist: func() firewall.Allowlist { return buildAllowlist(cfg, log) },
	}, nil
}

// buildWatcher constructs the tunnel watcher, or returns nil when there is
// nothing to watch. It exists whenever tunnels are configured/autodetected (so
// VPN-mode observability and the legacy kill switch work) or when a tunnel-drop
// simulation is requested.
func buildWatcher(cfg *config.Config, log *slog.Logger, tunnels []string, ov runOverrides) *netdetect.Watcher {
	if len(tunnels) == 0 && !cfg.VPN.Autodetect && !ov.tunnelDownSet {
		return nil
	}
	w := &netdetect.Watcher{Tunnels: tunnels, Interval: cfg.VPN.TunnelWatch, Log: log}
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
	if !requireRoot("block") {
		return 1
	}

	fw, err := firewall.New()
	if err != nil {
		log.Error("firewall backend unavailable", "err", err)
		return 1
	}
	// Build the allowlist BEFORE blocking, while DNS still works: resolve the
	// geo-API provider hostnames to IPs so recovery detection can keep reaching
	// them once egress is cut.
	al := buildAllowlist(cfg, log)

	switch {
	case *force:
		// Manual override: cut ALL egress (except loopback + allowlist) regardless
		// of VPN config or guard state. The escape hatch when detection is wrong or
		// the operator wants an unconditional hard block. `unblock`/`panic` reverse it.
		if err := fw.Block(al); err != nil {
			log.Error("forced block failed", "err", err)
			return 1
		}
		log.Info("network force-blocked (all egress cut except allowlist)", "dns_allowed", len(al.DNS), "hosts_allowed", len(al.Hosts))
	case *guard || cfg.VPN.Enabled:
		// VPN mode. `--guard` installs the always-on interface guard (tunnel stays
		// open, physical egress locked to the endpoint); a plain `block` under
		// vpn.enabled is a full block that cuts the tunnel too.
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
		mode := firewall.ModeFullBlock
		if *guard {
			mode = firewall.ModeGuard
		}
		pol := firewall.Policy{
			Mode:         mode,
			Allowlist:    al,
			TunnelIfaces: tunnels,
			VPNEndpoints: endpoints,
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
	default:
		if err := fw.Block(al); err != nil {
			log.Error("block failed", "err", err)
			return 1
		}
		log.Info("network blocked", "dns_allowed", len(al.DNS), "hosts_allowed", len(al.Hosts))
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
	for _, ep := range cfg.VPN.Endpoints {
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

// buildAllowlist converts the config allowlist into a firewall.Allowlist and
// augments it with the resolved IPs of the configured geo-API providers, so the
// monitor can still reach them while a block is in force.
func buildAllowlist(cfg *config.Config, log *slog.Logger) firewall.Allowlist {
	var al firewall.Allowlist
	for _, s := range cfg.Allowlist.DNS {
		if a, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil {
			al.DNS = append(al.DNS, a.Unmap())
		} else {
			log.Warn("ignoring invalid DNS allowlist address", "addr", s, "err", err)
		}
	}

	seen := make(map[netip.Addr]bool)
	add := func(a netip.Addr) {
		a = a.Unmap()
		if a.IsValid() && !seen[a] {
			seen[a] = true
			al.Hosts = append(al.Hosts, a)
		}
	}
	for _, s := range cfg.Allowlist.Hosts {
		if a, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil {
			add(a)
		} else {
			log.Warn("ignoring invalid host allowlist address", "addr", s, "err", err)
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
	// unblock already removes dezhban's rules unconditionally; --force is accepted
	// for symmetry with `block --force` and documents the manual-override intent.
	_ = fs.Bool("force", false, "remove rules unconditionally (unblock is already unconditional)")
	_ = fs.Parse(args)
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
	fmt.Println("add to your config:")
	fmt.Println(`  "vpn": {`)
	fmt.Println(`    "enabled": true,`)
	fmt.Printf("    \"tunnelInterfaces\": [%s],\n", quoteJoin(tunnels))
	fmt.Println(`    "endpoints": ["vpn.example.com"],`)
	fmt.Println(`    "autoDiscoverEndpoints": true`)
	fmt.Println(`  }`)
	fmt.Println()
	fmt.Println("endpoints may be IP(s) OR hostname(s) (re-resolved at runtime). For a")
	fmt.Println("rotating-pool VPN (NordVPN/ProtonVPN/…), set autoDiscoverEndpoints: true and")
	fmt.Println("dezhban learns the live server IP from the active socket (macOS). Verify with")
	fmt.Println("`dezhban monitor` or `dezhban doctor --discover` before enabling the guard.")
	return 0
}

// quoteJoin renders a string slice as a JSON array body: "a", "b".
func quoteJoin(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = `"` + s + `"`
	}
	return strings.Join(q, ", ")
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
	src := *cfgPath
	if src == "" {
		src = "(defaults — no --config given)"
	}
	blocked := cfg.BlockedCountries
	if len(blocked) == 0 {
		blocked = []string{"(none)"}
	}
	fmt.Printf("config OK: %s\n", src)
	fmt.Printf("  blocked countries: %s\n", strings.Join(blocked, ", "))
	fmt.Printf("  poll interval:     %s\n", cfg.PollInterval)
	fmt.Printf("  fail-closed:       %t\n", cfg.FailClosed)
	fmt.Printf("  vpn guard:         %t\n", cfg.VPN.Enabled)
	if cfg.VPN.Enabled {
		fmt.Printf("  vpn tunnels:       %s\n", strings.Join(cfg.VPN.TunnelInterfaces, ", "))
		fmt.Printf("  vpn endpoints:     %s\n", strings.Join(cfg.VPN.Endpoints, ", "))
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
		v := decision.New(cfg.BlockedCountries, cfg.FailClosed, 1).Evaluate(monitor.Result{Reading: r, Err: lookupErr})
		verdict := "ALLOW"
		if v == decision.Block {
			verdict = "BLOCK"
		}
		reason := "country not in blocklist"
		switch {
		case lookupErr != nil && cfg.FailClosed:
			reason = "lookup failed (fail-closed)"
		case lookupErr != nil:
			reason = "lookup failed (fail-open)"
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

// policyForMode builds the firewall Policy the named mode would apply, mirroring
// the run loop's guard/full-block construction (runner.runVPN). It is the single
// source print-rules renders from. NOTE: keep in sync with runner.runVPN; a
// future refactor should extract a shared constructor in the firewall package.
func policyForMode(cfg *config.Config, log *slog.Logger, mode string) (firewall.Policy, error) {
	al := buildAllowlist(cfg, log)
	tunnels := resolveTunnels(cfg, log)
	switch mode {
	case "guard":
		return firewall.Policy{
			Mode:         firewall.ModeGuard,
			Allowlist:    al,
			TunnelIfaces: tunnels,
			VPNEndpoints: resolveEndpointsOnce(cfg, log, tunnels),
		}, nil
	case "fullblock":
		return firewall.Policy{
			Mode:         firewall.ModeFullBlock,
			Allowlist:    al,
			TunnelIfaces: tunnels,
			VPNEndpoints: resolveEndpointsOnce(cfg, log, tunnels),
		}, nil
	case "legacy":
		// Legacy direct model: full block with the dst-IP allowlist, no tunnel.
		return firewall.Policy{Mode: firewall.ModeFullBlock, Allowlist: al}, nil
	default:
		return firewall.Policy{}, fmt.Errorf("unknown mode %q (valid: guard, fullblock, legacy)", mode)
	}
}

// cmdPrintRules renders the exact firewall ruleset a given policy would install
// and prints it to stdout WITHOUT applying it — the safe way to inspect a block
// or guard before risking a lockout. No root: rendering is pure. Diagnostic logs
// (allowlist resolution, etc.) go to stderr, so stdout is just the ruleset.
func cmdPrintRules(args []string) int {
	fs := flag.NewFlagSet("print-rules", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to config file (JSON)")
	mode := fs.String("mode", "guard", "policy to render: guard, fullblock, or legacy")
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
	fmt.Printf("  vpn guard enabled: %t\n", cfg.VPN.Enabled)
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
	fmt.Println()

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
	return 0
}

func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to config file (JSON)")
	_ = fs.Parse(args)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}

	blocked := cfg.BlockedCountries
	if len(blocked) == 0 {
		blocked = []string{"(none)"}
	}

	fmt.Println("dezhban", version)
	fmt.Println("privileged:      ", privilege.IsPrivileged())
	fmt.Println("service:         ", svc.Status())
	fmt.Println("poll interval:   ", cfg.PollInterval)
	fmt.Println("fail-closed:     ", cfg.FailClosed)
	fmt.Println("hysteresis:      ", cfg.Hysteresis)
	fmt.Println("blocked countries:", strings.Join(blocked, ", "))
	fmt.Println("providers:       ", strings.Join(cfg.Providers, ", "))
	fmt.Println("log level:       ", cfg.LogLevel)

	// VPN mode fields: only meaningful when the guard is configured.
	fmt.Println("vpn enabled:     ", cfg.VPN.Enabled)
	if cfg.VPN.Enabled {
		tunnels := cfg.VPN.TunnelInterfaces
		if len(tunnels) == 0 && cfg.VPN.Autodetect {
			tunnels = []string{"(autodetect)"}
		}
		fmt.Println("vpn tunnels:     ", strings.Join(tunnels, ", "))
		fmt.Println("vpn endpoints:   ", strings.Join(cfg.VPN.Endpoints, ", "))
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
