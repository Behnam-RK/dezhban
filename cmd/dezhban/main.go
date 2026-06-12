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

const usage = `dezhban — network kill switch

Usage:
  dezhban <command> [flags]

Commands:
  run        Run the monitor→decision→enforcement loop
  block      Manually block network egress
  unblock    Remove dezhban's firewall rules
  status     Show version, config, and current state
  panic      Force-remove dezhban's rules even if the daemon is dead
  install    Register dezhban as a boot-persistent OS service
  uninstall  Remove the OS service
  start      Start the installed service
  stop       Stop the installed service (removes firewall rules)
  version    Print the version

Run "dezhban <command> -h" for command flags.`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
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
	case "panic":
		return cmdPanic(rest)
	case "install", "uninstall", "start", "stop":
		return cmdService(cmd, rest)
	case "version", "--version", "-v":
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
	_ = fs.Parse(args)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}
	log := logging.New(cfg.LogLevel)

	if *dryRun {
		return runDryRun(cfg, log)
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
		return assembleOptions(cfg, l)
	}
	if err := svc.Run(build, log, cfg.LogLevel, *cfgPath); err != nil {
		log.Error("run loop failed", "err", err)
		return 1
	}
	return 0
}

// assembleOptions builds the run-loop options from config, wiring the monitor,
// decider, and firewall backend. It is shared by the `run` command and the
// service Start path; the logger is supplied by the caller so service mode can
// route output to the platform logger.
func assembleOptions(cfg *config.Config, log *slog.Logger) (runner.Options, error) {
	providers := monitor.ProvidersFromURLs(cfg.Providers, log)
	if len(providers) == 0 {
		return runner.Options{}, fmt.Errorf("no usable geo providers configured")
	}
	fw, err := firewall.New()
	if err != nil {
		return runner.Options{}, fmt.Errorf("firewall backend unavailable: %w", err)
	}
	log.Info("run loop started",
		"interval", cfg.PollInterval,
		"providers", len(providers),
		"blocked_countries", cfg.BlockedCountries,
		"fail_closed", cfg.FailClosed,
		"hysteresis", cfg.Hysteresis,
		"quorum", cfg.ProviderQuorum,
		"vpn", cfg.VPN.Enabled,
	)
	return runner.Options{
		Monitor:   monitor.New(providers, cfg.PollInterval, log, cfg.ProviderQuorum),
		Decider:   decision.New(cfg.BlockedCountries, cfg.FailClosed, cfg.Hysteresis),
		Backend:   fw,
		Log:       log,
		Interval:  cfg.PollInterval,
		VPN:       cfg.VPN.Enabled,
		Tunnels:   resolveTunnels(cfg, log),
		Endpoints: parseEndpoints(cfg.VPN.Endpoints, log),
		// Re-resolve the allowlist at each Block so rotated provider IPs stay
		// reachable for recovery detection while egress is cut.
		Allowlist: func() firewall.Allowlist { return buildAllowlist(cfg, log) },
	}, nil
}

// runDryRun polls the monitor and prints each reading without touching the
// firewall. Stops on SIGINT/SIGTERM.
func runDryRun(cfg *config.Config, log *slog.Logger) int {
	providers := monitor.ProvidersFromURLs(cfg.Providers, log)
	if len(providers) == 0 {
		log.Error("no usable geo providers configured")
		return 1
	}
	mon := monitor.New(providers, cfg.PollInterval, log, cfg.ProviderQuorum)

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
	_ = fs.Bool("force", false, "block regardless of detection (Phase 7)")
	_ = fs.Parse(args)
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}
	log := logging.New(cfg.LogLevel)
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
	case *guard || cfg.VPN.Enabled:
		// VPN mode. `--guard` installs the always-on interface guard (tunnel stays
		// open, physical egress locked to the endpoint); a plain `block` under
		// vpn.enabled is a full block that cuts the tunnel too.
		tunnels := resolveTunnels(cfg, log)
		if len(tunnels) == 0 || len(cfg.VPN.Endpoints) == 0 {
			log.Error("vpn mode needs tunnel interfaces (vpn.tunnelInterfaces or vpn.autodetect) and vpn.endpoints")
			return 1
		}
		endpoints := parseEndpoints(cfg.VPN.Endpoints, log)
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

// parseEndpoints converts configured VPN endpoint strings to addresses, warning
// on (and skipping) any that don't parse. Config validation already rejects bad
// endpoints when vpn.enabled, so this mainly guards the --guard-without-enabled path.
func parseEndpoints(eps []string, log *slog.Logger) []netip.Addr {
	var out []netip.Addr
	for _, s := range eps {
		if a, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil {
			out = append(out, a.Unmap())
		} else {
			log.Warn("ignoring invalid vpn endpoint", "addr", s, "err", err)
		}
	}
	return out
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

func cmdPanic(args []string) int {
	fs := flag.NewFlagSet("panic", flag.ExitOnError)
	_ = fs.Parse(args)
	if !requireRoot("panic") {
		return 1
	}
	fmt.Fprintln(os.Stderr, "panic not implemented yet (Phase 7)")
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
	fmt.Println("poll interval:   ", cfg.PollInterval)
	fmt.Println("fail-closed:     ", cfg.FailClosed)
	fmt.Println("hysteresis:      ", cfg.Hysteresis)
	fmt.Println("blocked countries:", strings.Join(blocked, ", "))
	fmt.Println("providers:       ", strings.Join(cfg.Providers, ", "))
	fmt.Println("log level:       ", cfg.LogLevel)

	if fw, err := firewall.New(); err != nil {
		fmt.Println("blocked:          unknown:", err)
	} else if blocked, err := fw.IsBlocked(); err != nil {
		// Reading pf rules needs root; report rather than fail the command.
		fmt.Println("blocked:          unknown:", err)
	} else {
		fmt.Println("blocked:         ", blocked)
	}
	return 0
}
