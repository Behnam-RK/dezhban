package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/behnam-rk/dezhban/internal/command"
	"github.com/behnam-rk/dezhban/internal/config"
	"github.com/behnam-rk/dezhban/internal/control"
	"github.com/behnam-rk/dezhban/internal/learned"
	"github.com/behnam-rk/dezhban/internal/state"
	"github.com/behnam-rk/dezhban/internal/vpnimport"
)

const vpnUsage = `usage: dezhban vpn <subcommand>

Subcommands:
  list                          Show profiles, learned endpoints, and active state
  add <name> --endpoint H...    Add a VPN profile (repeat --endpoint; or --from FILE)
  remove <name>                 Remove a profile ( --learned to drop a learned entry)
  promote <name> [--as NAME]    Promote a learned entry into a saved profile
  forget <name> | --all         Drop learned endpoint entries
  import FILE [--name N]         Import a profile from a WG/OpenVPN/V2Ray config
                                  (--dry-run previews without saving)

Flags: --config PATH, --endpoint HOST (repeatable), --from FILE, --iface-hint PREFIX,
       --as NAME, --name NAME, --dry-run, --all, --learned, --yes`

// cmdSwitch opens (or cancels / inspects) a switch window on the running daemon
// by writing the root-owned control file. Connecting a brand-new VPN whose server
// isn't known yet: open a window, connect, the daemon learns and pins the server,
// then snaps shut.
func cmdSwitch(args []string) int {
	fs := flag.NewFlagSet("switch", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to config file (JSON)")
	forDur := fs.Duration("for", 0, "window duration (default: config vpn.switchWindow)")
	name := fs.String("name", "", "profile name to attribute the learned endpoint to")
	doCancel := fs.Bool("cancel", false, "cancel an open switch window")
	doStatus := fs.Bool("status", false, "print the current switch-window state and exit")
	noWait := fs.Bool("no-wait", false, "fire the command and return without waiting")
	_ = fs.Parse(args)

	statePath := defaultStatePath()
	if *doStatus {
		return printSwitchStatus(statePath)
	}

	dur := ""
	if *forDur > 0 {
		dur = forDur.String()
	}

	// Passwordless path first: the daemon opens/cancels the window itself when
	// control.allowSwitchOps is on (the default). Falls through to the root-owned
	// command file when no daemon is listening — that path always works, and is the
	// only one when the operator has turned switch ops off.
	if !noDaemon() {
		req := control.Request{Op: control.OpOpenSwitch, Duration: dur, Profile: *name}
		if *doCancel {
			req = control.Request{Op: control.OpCancelSwitch}
		}
		if code, handled := tryControl(*cfgPath, req); handled {
			if code != 0 {
				return code
			}
			if *doCancel {
				fmt.Println("switch window cancelled")
				return 0
			}
			fmt.Println("switch window open — connect your new VPN now.")
			if *noWait {
				return 0
			}
			return waitForSwitch(statePath)
		}
	}

	if !requireRoot("switch") {
		return 1
	}
	path := defaultCommandPath()
	if *doCancel {
		if err := command.Write(path, newCommand(command.OpCancelSwitchWindow, "", "")); err != nil {
			fmt.Fprintln(os.Stderr, "switch --cancel:", err)
			return 1
		}
		fmt.Println("switch window cancel requested")
		return 0
	}

	if err := command.Write(path, newCommand(command.OpOpenSwitchWindow, dur, *name)); err != nil {
		fmt.Fprintln(os.Stderr, "switch:", err)
		return 1
	}
	fmt.Println("switch window requested — connect your new VPN now.")
	if *noWait {
		return 0
	}
	return waitForSwitch(statePath)
}

// newCommand builds a control command stamped now with a fresh nonce.
func newCommand(op command.Op, dur, profile string) command.Command {
	return command.Command{Op: op, Duration: dur, Profile: profile, IssuedAt: time.Now(), Nonce: nonce()}
}

func nonce() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// waitForSwitch polls the state file and narrates the window until it closes or a
// generous timeout elapses. Best-effort: the daemon is the source of truth.
func waitForSwitch(statePath string) int {
	deadline := time.Now().Add(6 * time.Minute)
	sawOpen := false
	for time.Now().Before(deadline) {
		time.Sleep(750 * time.Millisecond)
		snap, err := state.Read(statePath)
		if err != nil {
			continue
		}
		if snap.Switch != nil && snap.Switch.Open {
			if !sawOpen {
				sawOpen = true
				fmt.Printf("  window open until %s — connect now…\n", snap.Switch.Until.Format(time.Kitchen))
			}
			continue
		}
		if sawOpen {
			fmt.Println("  window closed.")
			if snap.Posture == "guard" {
				fmt.Println("  guard active. If a new endpoint was learned, make it permanent with:")
				fmt.Printf("    sudo dezhban vpn promote <name>   (see: dezhban vpn list)\n")
			}
			return 0
		}
	}
	fmt.Println("  (no window state observed — is the daemon running? try: sudo dezhban start)")
	return 0
}

func printSwitchStatus(statePath string) int {
	snap, err := state.Read(statePath)
	if err != nil {
		fmt.Println("switch window: unknown (no state file; is the daemon running?)")
		return 0
	}
	if snap.Switch != nil && snap.Switch.Open {
		fmt.Printf("switch window: OPEN until %s (profile %q)\n", snap.Switch.Until.Format(time.RFC3339), snap.Switch.Profile)
	} else {
		fmt.Println("switch window: closed")
	}
	return 0
}

// cmdVPN manages VPN profiles and learned endpoints.
func cmdVPN(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, vpnUsage)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return cmdVPNList(rest)
	case "add":
		return cmdVPNAdd(rest)
	case "remove":
		return cmdVPNRemove(rest)
	case "promote":
		return cmdVPNPromote(rest)
	case "forget":
		return cmdVPNForget(rest)
	case "import":
		return cmdVPNImport(rest)
	case "help", "-h", "--help":
		fmt.Println(vpnUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown vpn subcommand %q\n\n%s\n", sub, vpnUsage)
		return 2
	}
}

func cmdVPNList(args []string) int {
	cfgPath, args := stripConfigFlag(args)
	_ = args
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vpn list:", err)
		return 1
	}
	if len(cfg.VPN.Endpoints) > 0 {
		fmt.Printf("(default)   %s\n", strings.Join(cfg.VPN.Endpoints, ", "))
	}
	for _, p := range cfg.VPN.Profiles {
		hint := ""
		if p.IfaceHint != "" {
			hint = "  [iface " + p.IfaceHint + "*]"
		}
		fmt.Printf("%-11s %s%s\n", p.Name, strings.Join(p.Endpoints, ", "), hint)
	}
	store, lerr := learned.Load(defaultLearnedPath())
	if lerr != nil {
		fmt.Fprintln(os.Stderr, "vpn list: learned endpoints unavailable:", lerr)
	}
	if len(store.Entries) > 0 {
		fmt.Println("\nlearned (not yet saved as profiles):")
		for _, e := range store.Entries {
			var eps []string
			for _, ep := range e.Endpoints {
				eps = append(eps, ep.Addr)
			}
			fmt.Printf("  %-9s %s\n", e.Name, strings.Join(eps, ", "))
		}
	}
	// Live daemon state (best-effort; silent when no daemon is running or the
	// snapshot is unreadable). Matches the "active state" the command advertises.
	if snap, err := state.Read(defaultStatePath()); err == nil {
		if snap.Switch != nil && snap.Switch.Open {
			line := fmt.Sprintf("\nswitch window OPEN until %s", snap.Switch.Until.Format(time.Kitchen))
			if snap.Switch.Profile != "" {
				line += fmt.Sprintf(" (profile %q)", snap.Switch.Profile)
			}
			fmt.Println(line)
		}
		if snap.ActiveProfile != "" {
			fmt.Printf("last verified profile: %s\n", snap.ActiveProfile)
		}
	}
	return 0
}

func cmdVPNAdd(args []string) int {
	fs := flag.NewFlagSet("vpn add", flag.ExitOnError)
	var eps multiFlag
	fs.Var(&eps, "endpoint", "VPN server host or IP (repeatable)")
	from := fs.String("from", "", "import endpoints from a WireGuard/OpenVPN/V2Ray config file")
	hint := fs.String("iface-hint", "", "tunnel interface name prefix (display only)")
	yes := fs.Bool("yes", false, "don't print the endpoint preview before adding")
	cfgPath, rest := stripConfigFlag(args)
	pos := parseInterspersed(fs, rest)
	if len(pos) < 1 {
		fmt.Fprintln(os.Stderr, "vpn add: a profile name is required")
		return 2
	}
	name := pos[0]

	endpoints := []string(eps)
	if *from != "" {
		imported, format, err := importEndpoints(*from)
		if err != nil {
			fmt.Fprintln(os.Stderr, "vpn add --from:", err)
			return 1
		}
		fmt.Printf("found %d endpoint(s) in %s config: %s\n", len(imported), format, strings.Join(imported, ", "))
		endpoints = append(endpoints, imported...)
	}
	if len(endpoints) == 0 {
		fmt.Fprintln(os.Stderr, "vpn add: at least one --endpoint or --from FILE is required")
		return 2
	}
	if !*yes {
		fmt.Printf("add profile %q with endpoints: %s\n", name, strings.Join(endpoints, ", "))
	}
	if !requireRoot("vpn add") {
		return 1
	}
	return mutateConfig(cfgPath, func(c *config.Config) error {
		for _, p := range c.VPN.Profiles {
			if strings.EqualFold(p.Name, name) {
				return fmt.Errorf("profile %q already exists", name)
			}
		}
		c.VPN.Profiles = append(c.VPN.Profiles, config.Profile{Name: name, Endpoints: endpoints, IfaceHint: *hint})
		return nil
	}, fmt.Sprintf("profile %q added", name))
}

func cmdVPNRemove(args []string) int {
	fs := flag.NewFlagSet("vpn remove", flag.ExitOnError)
	fromLearned := fs.Bool("learned", false, "remove a learned entry instead of a profile")
	cfgPath, rest := stripConfigFlag(args)
	pos := parseInterspersed(fs, rest)
	if len(pos) < 1 {
		fmt.Fprintln(os.Stderr, "vpn remove: a name is required")
		return 2
	}
	name := pos[0]
	if !requireRoot("vpn remove") {
		return 1
	}
	if *fromLearned {
		return forgetLearned(name, false)
	}
	return mutateConfig(cfgPath, func(c *config.Config) error {
		out := c.VPN.Profiles[:0]
		found := false
		for _, p := range c.VPN.Profiles {
			if strings.EqualFold(p.Name, name) {
				found = true
				continue
			}
			out = append(out, p)
		}
		if !found {
			return fmt.Errorf("no profile named %q", name)
		}
		c.VPN.Profiles = out
		return nil
	}, fmt.Sprintf("profile %q removed", name))
}

func cmdVPNPromote(args []string) int {
	fs := flag.NewFlagSet("vpn promote", flag.ExitOnError)
	as := fs.String("as", "", "profile name to save under (default: the learned entry name)")
	cfgPath, rest := stripConfigFlag(args)
	pos := parseInterspersed(fs, rest)
	if len(pos) < 1 {
		fmt.Fprintln(os.Stderr, "vpn promote: a learned entry name is required (see: dezhban vpn list)")
		return 2
	}
	entryName := pos[0]
	if !requireRoot("vpn promote") {
		return 1
	}
	store, lerr := learned.Load(defaultLearnedPath())
	if lerr != nil {
		// Surface the underlying failure so a corrupt/unreadable learned.json isn't
		// misreported as "no learned entry".
		fmt.Fprintln(os.Stderr, "vpn promote: learned endpoints unreadable:", lerr)
		return 1
	}
	var eps []string
	var found bool
	for _, e := range store.Entries {
		if strings.EqualFold(e.Name, entryName) {
			found = true
			for _, ep := range e.Endpoints {
				eps = append(eps, ep.Addr)
			}
		}
	}
	if !found {
		fmt.Fprintf(os.Stderr, "vpn promote: no learned entry named %q\n", entryName)
		return 1
	}
	profileName := *as
	if profileName == "" {
		profileName = entryName
	}
	code := mutateConfig(cfgPath, func(c *config.Config) error {
		for _, p := range c.VPN.Profiles {
			if strings.EqualFold(p.Name, profileName) {
				return fmt.Errorf("profile %q already exists", profileName)
			}
		}
		c.VPN.Profiles = append(c.VPN.Profiles, config.Profile{Name: profileName, Endpoints: eps})
		return nil
	}, fmt.Sprintf("profile %q saved from learned entry %q", profileName, entryName))
	if code != 0 {
		return code
	}
	return forgetLearned(entryName, false)
}

func cmdVPNForget(args []string) int {
	fs := flag.NewFlagSet("vpn forget", flag.ExitOnError)
	all := fs.Bool("all", false, "drop all learned entries")
	pos := parseInterspersed(fs, args)
	if !*all && len(pos) < 1 {
		fmt.Fprintln(os.Stderr, "vpn forget: a name or --all is required")
		return 2
	}
	if !requireRoot("vpn forget") {
		return 1
	}
	name := ""
	if len(pos) > 0 {
		name = pos[0]
	}
	return forgetLearned(name, *all)
}

// cmdVPNImport parses a client config and adds (or previews) a profile from it.
func cmdVPNImport(args []string) int {
	fs := flag.NewFlagSet("vpn import", flag.ExitOnError)
	name := fs.String("name", "", "profile name (default: the file's base name)")
	dryRun := fs.Bool("dry-run", false, "print what would be imported without saving")
	cfgPath, rest := stripConfigFlag(args)
	pos := parseInterspersed(fs, rest)
	if len(pos) < 1 {
		fmt.Fprintln(os.Stderr, "vpn import: a config file path is required")
		return 2
	}
	file := pos[0]
	eps, format, err := importEndpoints(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vpn import:", err)
		return 1
	}
	profileName := *name
	if profileName == "" {
		profileName = baseName(file)
	}
	fmt.Printf("%s: found %d endpoint(s): %s\n", format, len(eps), strings.Join(eps, ", "))
	if *dryRun {
		fmt.Printf("(dry run) would add profile %q\n", profileName)
		return 0
	}
	if !requireRoot("vpn import") {
		return 1
	}
	return mutateConfig(cfgPath, func(c *config.Config) error {
		for _, p := range c.VPN.Profiles {
			if strings.EqualFold(p.Name, profileName) {
				return fmt.Errorf("profile %q already exists", profileName)
			}
		}
		c.VPN.Profiles = append(c.VPN.Profiles, config.Profile{Name: profileName, Endpoints: eps})
		return nil
	}, fmt.Sprintf("profile %q imported from %s", profileName, file))
}

// importEndpoints is the thin CLI wrapper over vpnimport.Extract.
func importEndpoints(path string) (endpoints []string, format string, err error) {
	return vpnimport.Extract(path)
}

func baseName(path string) string {
	b := path
	if i := strings.LastIndexAny(b, `/\`); i >= 0 {
		b = b[i+1:]
	}
	if i := strings.LastIndex(b, "."); i > 0 {
		b = b[:i]
	}
	// Sanitize to the profile name charset.
	b = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, b)
	if b == "" {
		return "imported"
	}
	return b
}

// forgetLearned removes learned entries directly (the caller has already
// elevated). Editing the daemon-owned file directly is safe under root.
func forgetLearned(name string, all bool) int {
	path := defaultLearnedPath()
	store, err := learned.Load(path)
	if err != nil {
		// Load returns a usable empty store even on a corrupt/unreadable file;
		// warn but continue so `vpn forget --all` can overwrite and recover it.
		fmt.Fprintln(os.Stderr, "vpn forget (continuing on load error):", err)
	}
	if all {
		store.Entries = nil
	} else if !store.Forget(name) {
		fmt.Fprintf(os.Stderr, "vpn forget: no learned entry named %q\n", name)
		return 1
	}
	if err := store.Save(path); err != nil {
		fmt.Fprintln(os.Stderr, "vpn forget: save:", err)
		return 1
	}
	fmt.Println("learned endpoints updated")
	return 0
}

// mutateConfig loads the config at cfgPath, applies fn, validates, and saves. On
// success it prints okMsg. It returns a process exit code.
func mutateConfig(cfgPath string, fn func(*config.Config) error, okMsg string) int {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := fn(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	out := resolveConfigPath(cfgPath)
	if out == "" {
		out = defaultConfigPath()
	}
	if err := config.Save(out, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "save config:", err)
		return 1
	}
	fmt.Println("dezhban:", okMsg)
	fmt.Println("restart the daemon to apply:  sudo dezhban stop && sudo dezhban start")
	return 0
}

// parseInterspersed parses flags that may appear before OR after positional
// arguments (Go's flag package otherwise stops at the first positional). Returns
// the positional args in order.
func parseInterspersed(fs *flag.FlagSet, args []string) []string {
	var positional []string
	rest := args
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return positional
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}
	return positional
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, strings.TrimSpace(v))
	return nil
}
