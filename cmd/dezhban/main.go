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
	"strings"
	"syscall"

	"github.com/behnam-rk/dezhban/internal/config"
	"github.com/behnam-rk/dezhban/internal/firewall"
	"github.com/behnam-rk/dezhban/internal/logging"
	"github.com/behnam-rk/dezhban/internal/monitor"
	"github.com/behnam-rk/dezhban/internal/privilege"
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

	// The monitor→decision→enforcement loop lands in Phase 3.
	log.Info("run loop not implemented yet (Phase 3)")
	return 0
}

// runDryRun polls the monitor and prints each reading without touching the
// firewall. Stops on SIGINT/SIGTERM.
func runDryRun(cfg *config.Config, log *slog.Logger) int {
	providers := monitor.ProvidersFromURLs(cfg.Providers, log)
	if len(providers) == 0 {
		log.Error("no usable geo providers configured")
		return 1
	}
	mon := monitor.New(providers, cfg.PollInterval, log)

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
	if err := fw.Block(al); err != nil {
		log.Error("block failed", "err", err)
		return 1
	}
	log.Info("network blocked", "dns_allowed", len(al.DNS), "hosts_allowed", len(al.Hosts))
	return 0
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
		ips, err := net.LookupIP(u.Hostname())
		if err != nil {
			log.Warn("cannot resolve provider for allowlist", "host", u.Hostname(), "err", err)
			continue
		}
		for _, ip := range ips {
			if a, ok := netip.AddrFromSlice(ip); ok {
				add(a)
			}
		}
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
