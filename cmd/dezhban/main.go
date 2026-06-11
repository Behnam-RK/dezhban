// Command dezhban is a cross-platform network kill switch: it watches the
// machine's public IP, resolves its country, and drives the OS firewall to cut
// traffic when the country matches a blocklist.
//
// Phase 0 wires the CLI skeleton, config, logging, and privilege checks. The
// monitor, decision, and firewall layers are filled in by later phases.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/behnam-rk/dezhban/internal/config"
	"github.com/behnam-rk/dezhban/internal/logging"
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

	if !*dryRun && !requireRoot("run") {
		return 1
	}

	// Filled in by Phase 1 (monitor) and Phase 3 (loop).
	log.Info("run not implemented yet", "dryRun", *dryRun, "phase", "0")
	return 0
}

func cmdBlock(args []string) int {
	fs := flag.NewFlagSet("block", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to config file (JSON)")
	_ = fs.Bool("force", false, "block regardless of detection (Phase 7)")
	_ = fs.Parse(args)
	if _, err := loadConfig(*cfgPath); err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}
	if !requireRoot("block") {
		return 1
	}
	fmt.Fprintln(os.Stderr, "block not implemented yet (Phase 2)")
	return 0
}

func cmdUnblock(args []string) int {
	fs := flag.NewFlagSet("unblock", flag.ExitOnError)
	_ = fs.Parse(args)
	if !requireRoot("unblock") {
		return 1
	}
	fmt.Fprintln(os.Stderr, "unblock not implemented yet (Phase 2)")
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
	// blocked-state (IsBlocked) is reported once the firewall backend lands (Phase 2).
	return 0
}
