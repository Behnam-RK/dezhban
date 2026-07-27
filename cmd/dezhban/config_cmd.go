package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/behnam-rk/dezhban/internal/config"
	"github.com/behnam-rk/dezhban/internal/control"
)

// configUsage is assembled rather than written out, because its "Keys" section
// used to be a hand-maintained copy of the settable-key set — one more list to
// forget. It is now generated from config.Tunables(), the same table `config
// schema` prints and the app reads, so a new key appears in the help by existing.
var configUsage = configUsageProse + wrapKeys(config.TunableKeys()) + `
  (VPN profiles are managed with 'dezhban vpn add/remove', not 'config set')`

const configUsageProse = `usage: dezhban config <subcommand>

Subcommands:
  path              Print the resolved config path
  show              Print the effective config as JSON
  get <key>         Print one config value
  set <key> <val>   Set a value, validate, and save
  set k=v [k=v ...] Set several values in one validated, atomic write
  reset <key> [...] Reset key(s) to the shipped default, validate, and save
  reset --all       Reset every tunable to defaults, preserving identity data
                    (blockedCountries, vpn.tunnelInterfaces/endpoints/profiles).
                    Delete the config file for a true wipe.
  preset list       List strict/balanced/relaxed, their cost, and which (if any)
                    matches the current config
  preset show <name>       Print one preset's key/value set
  preset diff [<name>]     Show keys that differ from a preset (default: the
                    matched-or-nearest one)
  preset apply <name>      Write a preset's values, validate, and save — same
                    path as 'set', so it applies live where it can and reports
                    what needs a restart
  schema            Describe every settable key: its default, cap, unit, whether
                    it can be turned off, and whether it applies live
  edit              Open the config in $EDITOR (created from defaults if missing)

Flags:
  --token-stdin     Read the control token from stdin and have the running
                    daemon perform the write — no root, applied immediately.
                    Falls back to a privileged write if no daemon answers; a
                    daemon that REFUSES is reported, never routed around.
                    See 'dezhban token'.
  --json            ('preset list'/'preset show'/'preset diff'/'schema' only)
                    print machine-readable JSON instead of prose

Keys (dotted; list values are comma-separated):
`

// wrapKeys renders the settable-key list for the usage text: two-space indent,
// space-separated, wrapped near 78 columns so it reads in a standard terminal.
func wrapKeys(keys []string) string {
	const width = 78
	var b strings.Builder
	line := " "
	for _, k := range keys {
		if len(line)+1+len(k) > width {
			b.WriteString(line)
			b.WriteString("\n")
			line = " "
		}
		line += " " + k
	}
	b.WriteString(line)
	return b.String()
}

// schemaEntry is what `config schema --json` emits: the declared Tunable
// verbatim (it already carries the lowerCamelCase tags every surface decodes)
// plus whether the key is one a strictness preset writes.
//
// Embedding rather than restating the eleven fields is deliberate — a parallel
// struct here would be exactly the kind of second copy this whole phase exists
// to delete.
type schemaEntry struct {
	config.Tunable
	// Preset reports that applying a strictness preset overwrites this key, so a
	// surface can warn that changing it by hand drifts the config to Custom.
	Preset bool `json:"preset"`
}

// presetWritten reports which keys a strictness preset sets. Taken from the
// preset definitions themselves rather than a list here, so the two cannot
// disagree; every preset writes the same key set, so the first one answers for all.
func presetWritten() map[string]bool {
	out := map[string]bool{}
	for _, p := range config.Presets() {
		for k := range p.Values {
			out[k] = true
		}
	}
	return out
}

// configSchema describes every settable key. It deliberately reads no config
// file: the schema is what the keys ARE, not what this host has set, so it
// answers identically on a machine with no config yet — which is exactly when a
// first-run wizard needs it.
func configSchema(args []string) int {
	args, jsonOut := stripJSONFlag(args)
	if len(args) != 0 {
		fmt.Fprintf(os.Stderr, "config schema takes no arguments (got %q)\n", strings.Join(args, " "))
		return 2
	}

	written := presetWritten()
	tunables := config.Tunables()

	if jsonOut {
		out := make([]schemaEntry, 0, len(tunables))
		for _, t := range tunables {
			out = append(out, schemaEntry{Tunable: t, Preset: written[t.Key]})
		}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "config schema json:", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	for _, t := range tunables {
		fmt.Printf("%s\n", t.Key)
		fmt.Printf("  %s (%s)\n", t.Label, t.Kind)
		fmt.Printf("  %s\n", t.Help)

		facts := []string{"default " + quoteEmpty(t.Default)}
		if t.Unit != "" {
			facts = append(facts, "in "+t.Unit)
		}
		if t.CapKey != "" {
			facts = append(facts, "capped by "+t.CapKey)
		}
		if t.Disablable {
			facts = append(facts, `"0" turns it off`)
		}
		if t.Advanced {
			facts = append(facts, "advanced")
		}
		if written[t.Key] {
			facts = append(facts, "set by presets")
		}
		fmt.Printf("  %s\n", strings.Join(facts, "; "))

		if t.RestartReason != "" {
			fmt.Printf("  needs a restart: %s\n", t.RestartReason)
		} else {
			fmt.Printf("  applies live\n")
		}
		fmt.Printf("  docs: %s\n\n", t.DocAnchor)
	}
	return 0
}

// quoteEmpty renders an empty default as something visible, so "default " isn't
// mistaken for a truncated line. control.socket legitimately defaults to empty.
func quoteEmpty(v string) string {
	if v == "" {
		return `"" (unset)`
	}
	return v
}

// configField is a get/set pair for one dotted config key.
type configField struct {
	get func(*config.Config) string
	set func(*config.Config, string) error
}

// configFields maps dotted keys to accessors over a *Config. Kept small and
// explicit rather than reflective so validation errors stay clear.
var configFields = map[string]configField{
	"pollInterval": {
		get: func(c *config.Config) string { return c.PollInterval.String() },
		set: func(c *config.Config, v string) error { return setDuration(&c.PollInterval, v) },
	},
	"blockedCountries": {
		get: func(c *config.Config) string { return strings.Join(c.BlockedCountries, ",") },
		// config.Normalize (run on save) upper-cases and de-duplicates; just split here.
		set: func(c *config.Config, v string) error { c.BlockedCountries = splitList(v); return nil },
	},
	"hysteresis": {
		get: func(c *config.Config) string { return strconv.Itoa(c.Hysteresis) },
		set: func(c *config.Config, v string) error {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return fmt.Errorf("hysteresis: %w", err)
			}
			c.Hysteresis = n
			return nil
		},
	},
	"providers": {
		get: func(c *config.Config) string { return strings.Join(c.Providers, ",") },
		set: func(c *config.Config, v string) error { c.Providers = splitList(v); return nil },
	},
	"providerQuorum": {
		get: func(c *config.Config) string { return strconv.FormatBool(c.ProviderQuorum) },
		set: func(c *config.Config, v string) error { return setBool(&c.ProviderQuorum, v) },
	},
	"logLevel": {
		get: func(c *config.Config) string { return c.LogLevel },
		set: func(c *config.Config, v string) error { c.LogLevel = strings.ToLower(strings.TrimSpace(v)); return nil },
	},
	"vpn.tunnelInterfaces": {
		get: func(c *config.Config) string { return strings.Join(c.VPN.TunnelInterfaces, ",") },
		set: func(c *config.Config, v string) error { c.VPN.TunnelInterfaces = splitList(v); return nil },
	},
	"vpn.endpoints": {
		get: func(c *config.Config) string { return strings.Join(c.VPN.Endpoints, ",") },
		set: func(c *config.Config, v string) error { c.VPN.Endpoints = splitList(v); return nil },
	},
	"vpn.autoDetect": {
		get: func(c *config.Config) string { return strconv.FormatBool(c.VPN.AutoDetect) },
		set: func(c *config.Config, v string) error { return setBool(&c.VPN.AutoDetect, v) },
	},
	"vpn.autoDiscoverEndpoints": {
		get: func(c *config.Config) string { return strconv.FormatBool(c.VPN.AutoDiscoverEndpoints) },
		set: func(c *config.Config, v string) error { return setBool(&c.VPN.AutoDiscoverEndpoints, v) },
	},
	"vpn.allowPhysicalDNS": {
		get: func(c *config.Config) string { return strconv.FormatBool(c.VPN.AllowPhysicalDNS) },
		set: func(c *config.Config, v string) error { return setBool(&c.VPN.AllowPhysicalDNS, v) },
	},
	"vpn.allowLocalNetwork": {
		get: func(c *config.Config) string { return strconv.FormatBool(c.VPN.AllowLocalNetwork) },
		set: func(c *config.Config, v string) error { return setBool(&c.VPN.AllowLocalNetwork, v) },
	},
	"vpn.switchWindow": {
		get: func(c *config.Config) string {
			if c.VPN.SwitchWindow < 0 {
				return "0s" // explicitly disabled
			}
			return c.VPN.SwitchWindow.String()
		},
		set: func(c *config.Config, v string) error {
			if err := setDuration(&c.VPN.SwitchWindow, v); err != nil {
				return err
			}
			if c.VPN.SwitchWindow == 0 {
				// "0" means off, not "reset to default" — same explicit-opt-out
				// sentinel as vpn.redialWindow. Without this remap, Normalize
				// would silently coerce a plain 0 back to the 5s default and the
				// operator's "0" would have no effect (the worst kind of bug in a
				// security tool: a setting accepted, discarded, and never reported).
				c.VPN.SwitchWindow = config.Disabled
			}
			return nil
		},
	},
	"vpn.redialWindow": {
		get: func(c *config.Config) string {
			if c.VPN.RedialWindow < 0 {
				return "0s" // explicitly disabled
			}
			return c.VPN.RedialWindow.String()
		},
		set: func(c *config.Config, v string) error {
			if err := setDuration(&c.VPN.RedialWindow, v); err != nil {
				return err
			}
			if c.VPN.RedialWindow == 0 {
				c.VPN.RedialWindow = config.Disabled // "0" means off, not "reset to default"
			}
			return nil
		},
	},
	"vpn.pauseMax": {
		get: func(c *config.Config) string {
			if c.VPN.PauseMax < 0 {
				return "0s" // explicitly disabled
			}
			return c.VPN.PauseMax.String()
		},
		set: func(c *config.Config, v string) error {
			if err := setDuration(&c.VPN.PauseMax, v); err != nil {
				return err
			}
			if c.VPN.PauseMax == 0 {
				c.VPN.PauseMax = config.Disabled // "0" means pausing is off, not "reset to default"
			}
			return nil
		},
	},
	"vpn.endpointRefresh": {
		get: func(c *config.Config) string { return c.VPN.EndpointRefresh.String() },
		set: func(c *config.Config, v string) error { return setDuration(&c.VPN.EndpointRefresh, v) },
	},
	"vpn.endpointGrace": {
		get: func(c *config.Config) string { return c.VPN.EndpointGrace.String() },
		set: func(c *config.Config, v string) error { return setDuration(&c.VPN.EndpointGrace, v) },
	},
	"vpn.autoArm": {
		get: func(c *config.Config) string { return strconv.FormatBool(c.VPN.AutoArm) },
		set: func(c *config.Config, v string) error { return setBool(&c.VPN.AutoArm, v) },
	},
	"vpn.armAtBoot": {
		get: func(c *config.Config) string { return strconv.FormatBool(c.VPN.ArmAtBoot) },
		set: func(c *config.Config, v string) error { return setBool(&c.VPN.ArmAtBoot, v) },
	},
	"vpn.tunnelWatch": {
		get: func(c *config.Config) string { return c.VPN.TunnelWatch.String() },
		set: func(c *config.Config, v string) error { return setDuration(&c.VPN.TunnelWatch, v) },
	},
	"control.enabled": {
		get: func(c *config.Config) string { return strconv.FormatBool(c.Control.Enabled) },
		set: func(c *config.Config, v string) error { return setBool(&c.Control.Enabled, v) },
	},
	"control.socket": {
		get: func(c *config.Config) string { return c.Control.Socket },
		set: func(c *config.Config, v string) error { c.Control.Socket = strings.TrimSpace(v); return nil },
	},
	"control.group": {
		get: func(c *config.Config) string { return c.Control.Group },
		set: func(c *config.Config, v string) error { c.Control.Group = strings.TrimSpace(v); return nil },
	},
	"control.allowSwitchOps": {
		get: func(c *config.Config) string { return strconv.FormatBool(c.Control.AllowSwitchOps) },
		set: func(c *config.Config, v string) error { return setBool(&c.Control.AllowSwitchOps, v) },
	},
	"control.allowConfigOps": {
		get: func(c *config.Config) string { return strconv.FormatBool(c.Control.AllowConfigOps) },
		set: func(c *config.Config, v string) error { return setBool(&c.Control.AllowConfigOps, v) },
	},
	"control.allowPauseOps": {
		get: func(c *config.Config) string { return strconv.FormatBool(c.Control.AllowPauseOps) },
		set: func(c *config.Config, v string) error { return setBool(&c.Control.AllowPauseOps, v) },
	},
	"vpn.advanced.switchWindowMax": {
		get: func(c *config.Config) string { return c.VPN.Advanced.SwitchWindowMax.String() },
		set: func(c *config.Config, v string) error { return setDuration(&c.VPN.Advanced.SwitchWindowMax, v) },
	},
	"vpn.advanced.redialWindowMax": {
		get: func(c *config.Config) string { return c.VPN.Advanced.RedialWindowMax.String() },
		set: func(c *config.Config, v string) error { return setDuration(&c.VPN.Advanced.RedialWindowMax, v) },
	},
	"vpn.advanced.redialMinUptime": {
		get: func(c *config.Config) string {
			if c.VPN.Advanced.RedialMinUptime < 0 {
				return "0s" // explicitly disabled
			}
			return c.VPN.Advanced.RedialMinUptime.String()
		},
		set: func(c *config.Config, v string) error {
			if err := setDuration(&c.VPN.Advanced.RedialMinUptime, v); err != nil {
				return err
			}
			if c.VPN.Advanced.RedialMinUptime == 0 {
				// "0" means the redial backoff is off, not "reset to default" — same
				// explicit-opt-out sentinel as the three windows.
				c.VPN.Advanced.RedialMinUptime = config.Disabled
			}
			return nil
		},
	},
	"vpn.advanced.redialBudget": {
		get: func(c *config.Config) string { return c.VPN.Advanced.RedialBudget.String() },
		set: func(c *config.Config, v string) error {
			return setLimitDuration(&c.VPN.Advanced.RedialBudget, v, "vpn.advanced.redialBudget")
		},
	},
	"vpn.advanced.redialBudgetWindow": {
		get: func(c *config.Config) string { return c.VPN.Advanced.RedialBudgetWindow.String() },
		set: func(c *config.Config, v string) error {
			return setLimitDuration(&c.VPN.Advanced.RedialBudgetWindow, v, "vpn.advanced.redialBudgetWindow")
		},
	},
	"vpn.advanced.commandFreshness": {
		get: func(c *config.Config) string { return c.VPN.Advanced.CommandFreshness.String() },
		set: func(c *config.Config, v string) error { return setDuration(&c.VPN.Advanced.CommandFreshness, v) },
	},
	"vpn.advanced.windowDiscoveryInterval": {
		get: func(c *config.Config) string { return c.VPN.Advanced.WindowDiscoveryInterval.String() },
		set: func(c *config.Config, v string) error { return setDuration(&c.VPN.Advanced.WindowDiscoveryInterval, v) },
	},
	"vpn.advanced.tunnelPruneAfter": {
		get: func(c *config.Config) string { return c.VPN.Advanced.TunnelPruneAfter.String() },
		set: func(c *config.Config, v string) error { return setDuration(&c.VPN.Advanced.TunnelPruneAfter, v) },
	},
	"vpn.advanced.learnedEndpointTTL": {
		get: func(c *config.Config) string { return c.VPN.Advanced.LearnedEndpointTTL.String() },
		set: func(c *config.Config, v string) error { return setDuration(&c.VPN.Advanced.LearnedEndpointTTL, v) },
	},
	"vpn.advanced.learnedMaxPerProfile": {
		get: func(c *config.Config) string { return strconv.Itoa(c.VPN.Advanced.LearnedMaxPerProfile) },
		set: func(c *config.Config, v string) error {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return fmt.Errorf("learnedMaxPerProfile: %w", err)
			}
			c.VPN.Advanced.LearnedMaxPerProfile = n
			return nil
		},
	},
	"vpn.advanced.promoteAfterRefreshes": {
		get: func(c *config.Config) string { return strconv.Itoa(c.VPN.Advanced.PromoteAfterRefreshes) },
		set: func(c *config.Config, v string) error {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return fmt.Errorf("promoteAfterRefreshes: %w", err)
			}
			c.VPN.Advanced.PromoteAfterRefreshes = n
			return nil
		},
	},
	"vpn.advanced.endpointWarnThreshold": {
		get: func(c *config.Config) string { return strconv.Itoa(c.VPN.Advanced.EndpointWarnThreshold) },
		set: func(c *config.Config, v string) error {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return fmt.Errorf("endpointWarnThreshold: %w", err)
			}
			c.VPN.Advanced.EndpointWarnThreshold = n
			return nil
		},
	},
	"vpn.advanced.windowProtocols": {
		get: func(c *config.Config) string { return strings.Join(c.VPN.Advanced.WindowProtocols, ",") },
		set: func(c *config.Config, v string) error { c.VPN.Advanced.WindowProtocols = splitList(v); return nil },
	},
	"vpn.advanced.windowPorts": {
		get: func(c *config.Config) string { return joinInts(c.VPN.Advanced.WindowPorts) },
		set: func(c *config.Config, v string) error {
			ports, err := splitInts(v)
			if err != nil {
				return fmt.Errorf("windowPorts: %w", err)
			}
			c.VPN.Advanced.WindowPorts = ports
			return nil
		},
	},
}

// joinInts renders an int slice the same way splitInts parses it back —
// comma-separated, no spaces.
func joinInts(ns []int) string {
	if len(ns) == 0 {
		return ""
	}
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}

// splitInts parses a comma-separated list of integers (e.g. a port list).
// Empty input is an empty (nil) list, not an error.
func splitInts(v string) ([]int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	parts := strings.Split(v, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func cmdConfig(args []string) int {
	// The config subcommands take positional args (get <key>, set <key> <val>), so a
	// --config flag can appear anywhere; pull it out before dispatch and thread the
	// resolved path through, otherwise an explicit --config is silently ignored.
	cfgPath, args := stripConfigFlag(args)
	args, useToken := stripTokenStdin(args)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, configUsage)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "path":
		return configPath(cfgPath)
	case "show":
		return configShow(cfgPath)
	case "get":
		return configGet(cfgPath, rest)
	case "set":
		return configSet(cfgPath, rest, useToken)
	case "reset":
		return configReset(cfgPath, rest, useToken)
	case "preset":
		return configPreset(cfgPath, rest, useToken)
	case "schema":
		return configSchema(rest)
	case "edit":
		return configEdit(cfgPath)
	case "-h", "--help", "help":
		fmt.Println(configUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown config subcommand %q\n\n%s\n", sub, configUsage)
		return 2
	}
}

// stripConfigFlag extracts a --config/-config value (in either `--config PATH` or
// `--config=PATH` form) from anywhere in args, returning the value ("" if absent)
// and the remaining args. Mirrors stripVerbose's whole-list scan so the flag works
// regardless of position relative to the subcommand's positional args.
func stripConfigFlag(args []string) (string, []string) {
	path := ""
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--config" || a == "-config":
			if i+1 < len(args) {
				path = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--config="):
			path = strings.TrimPrefix(a, "--config=")
		case strings.HasPrefix(a, "-config="):
			path = strings.TrimPrefix(a, "-config=")
		default:
			out = append(out, a)
		}
	}
	return path, out
}

// stripTokenStdin extracts the --token-stdin flag from anywhere in args, the same
// whole-list scan stripConfigFlag uses.
//
// The token arrives on stdin, never as an argument or an environment variable:
// argv and the environment of a running process are readable by other processes
// on some platforms and land in shell history on all of them, and this is a
// credential, not a setting.
func stripTokenStdin(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	found := false
	for _, a := range args {
		if a == "--token-stdin" || a == "-token-stdin" {
			found = true
			continue
		}
		out = append(out, a)
	}
	return out, found
}

// readTokenStdin reads the control token from stdin, trimming the trailing
// newline a pipe or heredoc adds.
func readTokenStdin() (string, error) {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
	if err != nil {
		return "", fmt.Errorf("read token from stdin: %w", err)
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", errors.New("--token-stdin: no token on stdin")
	}
	return tok, nil
}

// tryConfigWrite attempts the passwordless config-write path: the daemon
// validates, writes, and adopts the change in one request, so the caller needs
// neither root nor a separate reload.
//
// handled=true means the daemon answered. A refusal is an answer: it is NOT
// retried by falling back to the elevated write, because the daemon's gating
// (control.allowConfigOps, an unenrolled or wrong token) is a decision, and
// routing around it with a password prompt would make the gate advisory. Only an
// unreachable or transiently-busy daemon returns handled=false, which is what
// keeps `config set` working with the daemon stopped.
func tryConfigWrite(cfgPath string, pairs map[string]string, token string) (code int, handled bool) {
	cfg, err := loadConfig(cfgPath)
	if err != nil || !cfg.Control.Enabled {
		return 0, false
	}
	// What the caller's input parses to, computed from the pre-write config —
	// the same "before" side noteCoercions gets for free on the privileged path,
	// which holds the config across the write. Here the DAEMON writes, so this
	// process never sees the normalised result and has to take both readings
	// itself: this one now, and the stored one off disk after the reply. cfg is
	// mutated in the process and is not read again below.
	typed := typedValues(cfg, pairs)
	resp, err := control.Do(controlSocketPath(cfg), control.Request{
		Op:     control.OpConfigWrite,
		Token:  token,
		Config: pairs,
	})
	if err != nil {
		verbosef("control socket: %v — falling back to a privileged write", err)
		return 0, false
	}
	if !resp.OK {
		if resp.Transient {
			verbosef("control socket: %s — falling back to a privileged write", resp.Error)
			return 0, false
		}
		fmt.Fprintln(os.Stderr, "daemon refused:", resp.Error)
		return ExitDaemonRefused, true
	}
	reportWriteOutcome(resp.Applied, resp.NeedsRestart)
	// A coercion note is owed on THIS path too, and it used to be missing here:
	// `Saved and applied: vpn.advanced.tunnelPruneAfter` is a true report of a
	// value the operator did not type, and the token path is the one the macOS
	// app and any script prefer. Read back from disk because that is where the
	// daemon put it.
	if saved, serr := loadConfig(cfgPath); serr == nil {
		noteCoercions(saved, typed)
	}
	return 0, true
}

// typedValues renders what pairs parse to, applying them to cfg the way the
// write path will and reading them back through the same accessors — the input
// side of noteCoercions for a caller that will not hold the written config.
// Mutates cfg.
//
// A key the accessors reject is skipped rather than reported: the write itself
// refuses an unknown key by name and an invalid value entire, so there is no
// stored value for it to differ from and nothing to note.
func typedValues(cfg *config.Config, pairs map[string]string) map[string]string {
	keys := make([]string, 0, len(pairs))
	for k, v := range pairs {
		f, ok := configFields[k]
		if !ok {
			continue
		}
		if err := f.set(cfg, v); err != nil {
			continue
		}
		keys = append(keys, k)
	}
	return renderKeys(cfg, keys)
}

// restartMarker introduces the list of keys the running daemon could not adopt.
//
// It is not merely prose: the macOS app scrapes this exact prefix out of `config
// set`'s stdout to decide whether to offer a restart at all
// (ConfigApply.pendingRestartKeys), deliberately, so the live/restart
// classification lives only in the daemon. Reword it and the app silently stops
// offering the restart, reporting a pending key as fully applied — so it is a
// single constant, pinned by TestRestartMarkerIsTheContractTheAppScrapes.
const restartMarker = "Restart dezhban to apply:"

// reportWriteOutcome is the single renderer for "what happened to the settings I
// just saved", used by both write paths — the token/socket one and a privileged
// write followed by a reload — so a config change reads identically either way.
func reportWriteOutcome(applied, needsRestart []string) {
	if len(applied) == 0 && len(needsRestart) == 0 {
		fmt.Println("Saved. No change to what the daemon is enforcing.")
		return
	}
	if len(applied) > 0 {
		fmt.Printf("Saved and applied: %s\n", strings.Join(applied, ", "))
	}
	if len(needsRestart) > 0 {
		prefix := ""
		if len(applied) == 0 {
			prefix = "Saved. "
		}
		fmt.Printf("%s%s %s\n", prefix, restartMarker, strings.Join(needsRestart, ", "))
	}
}

// writeTargetPath is where config set/edit persist to: the resolved path (honoring
// an explicit --config), or the canonical system path when nothing exists yet.
func writeTargetPath(flagVal string) string {
	if p := resolveConfigPath(flagVal); p != "" {
		return p
	}
	return defaultConfigPath()
}

// writeConfigKeys applies dotted key/value assignments to the config at path and
// saves it: the same load → apply → validate → atomic-write cycle `config set`
// performs, exposed so the running daemon can serve a config-write control op
// without shelling out to itself.
//
// Routing both through configFields is the point. A daemon that accepted a whole
// config document from a socket client would be trusting that client to compose
// a safe one; a key/value map can only express changes the CLI would also have
// accepted, validated by the same code, and an unknown key is refused by name
// rather than silently ignored.
//
// Applied in sorted key order so a rejected batch reports the same key whichever
// order the map happened to iterate in — the keys are independent (validation
// runs once, over the finished config), so order changes nothing else.
func writeConfigKeys(path string, pairs map[string]string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		field, ok := configFields[k]
		if !ok {
			return fmt.Errorf("unknown key %q", k)
		}
		if err := field.set(cfg, pairs[k]); err != nil {
			return fmt.Errorf("invalid value for %s: %w", k, err)
		}
	}
	// Save validates the finished config and writes it atomically, so a batch
	// with one bad value leaves the file untouched rather than half-applied.
	return config.Save(path, cfg)
}

func configPath(flagVal string) int {
	if p := resolveConfigPath(flagVal); p != "" {
		fmt.Println(p)
		return 0
	}
	fmt.Printf("%s (not present — using built-in defaults)\n", defaultConfigPath())
	return 0
}

func configShow(flagVal string) int {
	cfg, err := loadConfig(flagVal)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}
	data, err := config.Marshal(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "encode error:", err)
		return 1
	}
	fmt.Print(string(data))
	return 0
}

func configGet(flagVal string, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: dezhban config get <key>")
		return 2
	}
	field, ok := configFields[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown key %q\nvalid keys: %s\n", args[0], knownKeys())
		return 2
	}
	cfg, err := loadConfig(flagVal)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}
	fmt.Println(field.get(cfg))
	return 0
}

// configSet applies one or more key/value assignments in a SINGLE load-validate-save
// cycle: `config set <key> <value>`, or `config set key=value [key=value ...]`.
//
// The multi-pair form is not sugar. Each invocation is a privileged write, so a
// caller with seven fields to change (the menubar app's VPN panel) used to pay seven
// separate elevations — seven password prompts. It also had to hand-order the writes
// so the config was never briefly invalid between them, because each write validated
// the whole file. Applying every pair to one in-memory config and validating once
// makes both problems disappear: one prompt, one atomic write, no intermediate state
// that has to be legal.
func configSet(flagVal string, args []string, useToken bool) int {
	pairs, err := parseSetPairs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, "usage: dezhban config set <key> <value>")
		fmt.Fprintln(os.Stderr, "       dezhban config set <key>=<value> [<key>=<value> ...]")
		return 2
	}
	// The passwordless path, when the caller holds the control token: the daemon
	// validates, writes, and adopts in one request. Tried FIRST, because falling
	// back the other way round would mean prompting for a password before
	// discovering it was never needed.
	if useToken {
		tok, terr := readTokenStdin()
		if terr != nil {
			fmt.Fprintln(os.Stderr, terr)
			return 2
		}
		m := make(map[string]string, len(pairs))
		for _, p := range pairs {
			m[p.key] = p.val
		}
		if code, handled := tryConfigWrite(flagVal, m, tok); handled {
			return code
		}
	}
	cfg, err := loadConfig(flagVal)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}
	for _, p := range pairs {
		field, ok := configFields[p.key]
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown key %q\nvalid keys: %s\n", p.key, knownKeys())
			return 2
		}
		if err := field.set(cfg, p.val); err != nil {
			// Nothing has been written yet — the whole batch is rejected, so a bad
			// value in the fifth pair can't leave the first four persisted.
			fmt.Fprintf(os.Stderr, "invalid value for %s: %v\n", p.key, err)
			return 1
		}
	}
	setKeys := make([]string, len(pairs))
	for i, p := range pairs {
		setKeys[i] = p.key
	}
	typed := renderKeys(cfg, setKeys)
	path := writeTargetPath(flagVal)
	if err := writeConfig(path, cfg); err != nil {
		return saveError(path, err)
	}
	for _, p := range pairs {
		fmt.Printf("set %s = %s  (%s)\n", p.key, configFields[p.key].get(cfg), path)
	}
	noteCoercions(cfg, typed)
	// Writing the file used to be the whole story, which is why "I changed a
	// setting and nothing happened" was the most common complaint: the daemon
	// read its config once at startup and nobody ever told it to look again.
	notifyReload(flagVal)
	return 0
}

// renderKeys renders what the caller's input parsed to, BEFORE the write
// normalises it — the input side of noteCoercions. Keys with no configFields
// entry are skipped; every caller has already rejected those.
func renderKeys(cfg *config.Config, keys []string) map[string]string {
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if f, ok := configFields[k]; ok {
			out[k] = f.get(cfg)
		}
	}
	return out
}

// noteCoercions prints one line per key whose stored value is not what the
// caller's input parsed to. writeConfig → config.Marshal runs Normalize in
// place, so the "set k = v" echo already reports the value that was actually
// stored — but it reports it silently, and a user who typed
// `vpn.advanced.tunnelPruneAfter=0` meaning "off" has to notice for themselves
// that `10m0s` came back. Nine of the twelve advanced keys coerce a
// non-positive value to their shipped default like that (only
// redialMinUptime carries the config.Disabled sentinel instead), so the
// difference deserves a sentence rather than a stare.
//
// Both sides are rendered through the same field accessor, so a difference is
// always a real change of value and never a formatting one ("1h" and "1h0m0s"
// both render as "1h0m0s").
func noteCoercions(cfg *config.Config, typed map[string]string) {
	keys := make([]string, 0, len(typed))
	for k := range typed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if now := configFields[k].get(cfg); now != typed[k] {
			fmt.Printf("note: %s was normalised on write: %s → %s\n", k, typed[k], now)
		}
	}
}

// configReset restores config keys to their shipped defaults — the CLI twin of
// the GUI's per-field ↺. `--all` resets every tunable but preserves identity
// data (what the user protects and how to reach their VPNs); resetting those to
// empty would not be "defaults", it would be data loss.
func configReset(flagVal string, args []string, useToken bool) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dezhban config reset <key> [key ...] | --all")
		return 2
	}
	if useToken {
		if code, handled := resetViaToken(flagVal, args); handled {
			return code
		}
	}
	cfg, err := loadConfig(flagVal)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}
	def := config.Default()
	config.Normalize(&def)

	var keys []string
	if len(args) == 1 && args[0] == "--all" {
		preserved := struct {
			blocked   []string
			tunnels   []string
			endpoints []string
			profiles  []config.Profile
		}{cfg.BlockedCountries, cfg.VPN.TunnelInterfaces, cfg.VPN.Endpoints, cfg.VPN.Profiles}
		*cfg = def
		cfg.BlockedCountries = preserved.blocked
		cfg.VPN.TunnelInterfaces = preserved.tunnels
		cfg.VPN.Endpoints = preserved.endpoints
		cfg.VPN.Profiles = preserved.profiles
		fmt.Println("reset all tunables to defaults (preserved: blockedCountries, vpn.tunnelInterfaces/endpoints/profiles)")
	} else {
		keys = args
		for _, k := range keys {
			field, ok := configFields[k]
			if !ok {
				fmt.Fprintf(os.Stderr, "unknown key %q\nvalid keys: %s\n", k, knownKeys())
				return 2
			}
			// The shipped default, rendered through the same accessor pair the
			// GUI and `set` use, so every key resets the way it is edited.
			if err := field.set(cfg, field.get(&def)); err != nil {
				fmt.Fprintf(os.Stderr, "reset %s: %v\n", k, err)
				return 1
			}
		}
	}

	path := writeTargetPath(flagVal)
	if err := writeConfig(path, cfg); err != nil {
		return saveError(path, err)
	}
	for _, k := range keys {
		fmt.Printf("reset %s = %s  (%s)\n", k, configFields[k].get(cfg), path)
	}
	// A reset is a config write like any other, and returning to a default is
	// just as much a change the daemon has to be told about.
	notifyReload(flagVal)
	return 0
}

const presetUsage = `usage: dezhban config preset <subcommand>

Subcommands:
  list              List strict/balanced/relaxed, their cost, and which (if
                    any) matches the current config
  show <name>       Print one preset's key/value set
  diff [<name>]     Show keys that differ from a preset (default: the
                    matched-or-nearest one)
  apply <name>      Write a preset's values, validate, and save — the same
                    path 'config set' uses, so it applies live where it can
                    and reports what needs a restart

A preset is a write-time macro over the keys that answer "how strict am I" —
applying one changes ordinary config values, nothing else. It never touches
identity (blockedCountries, tunnel interfaces, endpoints, profiles).`

// presetJSON is the --json shape for 'preset list'/'preset show'. Values is
// only populated by 'show' — 'list' omits it (a summary, not the full set).
// Conflicts is likewise 'list'-only: it needs the current config to compute, and
// 'show' describes a preset in the abstract.
type presetJSON struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Cost    string `json:"cost"`
	Matched bool   `json:"matched,omitempty"`
	// Conflicts is empty for an appliable preset — see config.PresetConflicts.
	// A picker should offer a preset with entries here as unavailable rather
	// than let the user choose it and hit the failure at apply time.
	Conflicts []string          `json:"conflicts,omitempty"`
	Values    map[string]string `json:"values,omitempty"`
}

// changeJSON mirrors config.Change in the same lowerCamelCase convention the
// rest of the CLI's --json output uses, rather than marshaling the Go type
// (whose exported fields would serialize capitalized) directly.
type changeJSON struct {
	Key           string `json:"key"`
	From          string `json:"from"`
	To            string `json:"to"`
	RestartReason string `json:"restartReason,omitempty"`
}

// stripJSONFlag extracts --json from anywhere in args, the same whole-list
// scan stripConfigFlag/stripTokenStdin use.
func stripJSONFlag(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	found := false
	for _, a := range args {
		if a == "--json" || a == "-json" {
			found = true
			continue
		}
		out = append(out, a)
	}
	return out, found
}

// nearestPreset picks the preset with the fewest drifted keys from cfg —
// used as 'preset diff's default target when the config matches none exactly,
// so the diff always has something concrete to show against. Ties (equal
// drift count) favor the earlier preset in config.Presets' Strict → Balanced →
// Relaxed order.
func nearestPreset(cfg *config.Config) config.Preset {
	presets := config.Presets()
	best := presets[0]
	bestDrift := len(config.PresetDrift(cfg, best))
	for _, p := range presets[1:] {
		if n := len(config.PresetDrift(cfg, p)); n < bestDrift {
			best, bestDrift = p, n
		}
	}
	return best
}

func configPreset(flagVal string, args []string, useToken bool) int {
	args, jsonOut := stripJSONFlag(args)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, presetUsage)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return presetList(flagVal, jsonOut)
	case "show":
		return presetShow(rest, jsonOut)
	case "diff":
		return presetDiffCmd(flagVal, rest, jsonOut)
	case "apply":
		if jsonOut {
			// --json is only meaningful for a command that prints something to
			// format — apply's output is the ordinary "set k = v" lines `config
			// set` already prints, not a JSON-able report. Rejecting a flag
			// that would otherwise be silently ignored beats a script believing
			// it asked for machine-readable output and got prose instead.
			fmt.Fprintln(os.Stderr, "config preset apply does not support --json")
			return 2
		}
		return presetApply(flagVal, rest, useToken)
	case "-h", "--help", "help":
		fmt.Println(presetUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown config preset subcommand %q\n\n%s\n", sub, presetUsage)
		return 2
	}
}

func presetByNameOrUsage(name, usage string) (config.Preset, int) {
	p, ok := config.PresetByName(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown preset %q (want strict, balanced, or relaxed)\n%s\n", name, usage)
		return config.Preset{}, 2
	}
	return p, 0
}

func presetList(flagVal string, jsonOut bool) int {
	cfg, err := loadConfig(flagVal)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}
	matched, exact := config.MatchPreset(cfg)

	if jsonOut {
		out := make([]presetJSON, 0, 3)
		for _, p := range config.Presets() {
			out = append(out, presetJSON{
				Name: p.Name, Summary: p.Summary, Cost: p.Cost,
				Matched:   exact && p.Name == matched,
				Conflicts: config.PresetConflicts(cfg, p),
			})
		}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "preset list json:", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	for _, p := range config.Presets() {
		marker := ""
		if exact && p.Name == matched {
			marker = "  (current)"
		}
		fmt.Printf("%-9s %s%s\n", p.Name, p.Summary, marker)
		fmt.Printf("          cost: %s\n", p.Cost)
		// Listed but unavailable, said here rather than discovered at apply
		// time: offering a preset that cannot be written is the part of this
		// command a user would act on.
		for _, c := range config.PresetConflicts(cfg, p) {
			fmt.Printf("          cannot apply: %s\n", c)
		}
	}
	if !exact {
		near := nearestPreset(cfg)
		drift := config.PresetDrift(cfg, near)
		fmt.Printf("\ncurrent: Custom (%d key(s) differ from %s)\n", len(drift), near.Name)
	}
	return 0
}

func presetShow(rest []string, jsonOut bool) int {
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: dezhban config preset show <strict|balanced|relaxed>")
		return 2
	}
	p, code := presetByNameOrUsage(rest[0], "usage: dezhban config preset show <strict|balanced|relaxed>")
	if code != 0 {
		return code
	}

	if jsonOut {
		data, err := json.MarshalIndent(presetJSON{Name: p.Name, Summary: p.Summary, Cost: p.Cost, Values: p.Values}, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "preset show json:", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	fmt.Printf("%s — %s\n", p.Name, p.Summary)
	fmt.Printf("cost: %s\n\n", p.Cost)
	keys := make([]string, 0, len(p.Values))
	for k := range p.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %s = %s\n", k, p.Values[k])
	}
	return 0
}

func presetDiffCmd(flagVal string, rest []string, jsonOut bool) int {
	if len(rest) > 1 {
		fmt.Fprintln(os.Stderr, "usage: dezhban config preset diff [<strict|balanced|relaxed>]")
		return 2
	}
	cfg, err := loadConfig(flagVal)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}

	var target config.Preset
	if len(rest) == 1 {
		var code int
		target, code = presetByNameOrUsage(rest[0], "usage: dezhban config preset diff [<strict|balanced|relaxed>]")
		if code != 0 {
			return code
		}
	} else if name, exact := config.MatchPreset(cfg); exact {
		target, _ = config.PresetByName(name)
	} else {
		target = nearestPreset(cfg)
	}

	changes := config.PresetDrift(cfg, target)
	conflicts := config.PresetConflicts(cfg, target)

	if jsonOut {
		out := make([]changeJSON, 0, len(changes))
		for _, c := range changes {
			out = append(out, changeJSON{Key: c.Key, From: c.From, To: c.To, RestartReason: c.RestartReason})
		}
		data, err := json.MarshalIndent(struct {
			Preset    string       `json:"preset"`
			Changes   []changeJSON `json:"changes"`
			Conflicts []string     `json:"conflicts,omitempty"`
		}{target.Name, out, conflicts}, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "preset diff json:", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	if len(changes) == 0 {
		fmt.Printf("no drift from %s\n", target.Name)
		return 0
	}
	fmt.Printf("drift from %s:\n", target.Name)
	for _, c := range changes {
		fmt.Printf("  %s: %s → %s\n", c.Key, c.From, c.To)
	}
	// A diff that lists changes the corresponding apply would refuse is a diff
	// the user cannot act on — say so here, in the same place they read it.
	for _, c := range conflicts {
		fmt.Printf("cannot apply: %s\n", c)
	}
	return 0
}

func presetApply(flagVal string, rest []string, useToken bool) int {
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: dezhban config preset apply <strict|balanced|relaxed>")
		return 2
	}
	p, code := presetByNameOrUsage(rest[0], "usage: dezhban config preset apply <strict|balanced|relaxed>")
	if code != 0 {
		return code
	}
	// Loaded ONCE, up front, and its error is fatal rather than swallowed: the
	// conflict pre-flight, the Strict warning, and the privileged write further
	// down all need it, and a config that cannot be read is not a condition any
	// of them can do anything useful with. The token path is unaffected —
	// tryConfigWrite loads the config itself and declines to handle the write
	// when it can't, so reporting the failure here says the same thing one step
	// earlier.
	cfg, err := loadConfig(flagVal)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}

	// Pre-flight against the operator's own advanced caps, BEFORE either write
	// path AND before the banner below. Without it the write still fails safely
	// — Validate rejects it and nothing is persisted — but the error names the
	// validation rule ("vpn.switchWindow 30s exceeds
	// vpn.advanced.switchWindowMax 10s") rather than the actual conflict,
	// leaving the user to work out that a preset they were offered can never
	// apply to their config. It runs ahead of the banner because "applying
	// relaxed: …" followed by a full cost paragraph is a report of a write that
	// is about to be refused — and a cost the user never paid.
	if conflicts := config.PresetConflicts(cfg, p); len(conflicts) > 0 {
		for _, c := range conflicts {
			fmt.Fprintln(os.Stderr, "cannot apply:", c)
		}
		return 1
	}

	fmt.Printf("applying %s: %s\n", p.Name, p.Summary)
	fmt.Printf("cost: %s\n", p.Cost)

	// Strict disables vpn.allowPhysicalDNS. A VPN endpoint given as a hostname
	// needs the physical link's DNS to re-resolve its server while the tunnel
	// is down — without it, a redial after a drop can silently never find the
	// server again. Warn rather than block: the operator may already know the
	// server's IP never changes, or intend to pin a literal IP separately.
	if p.Name == "strict" {
		for _, ep := range config.EffectiveEndpoints(cfg, nil) {
			if ep == "" {
				continue
			}
			if _, err := netip.ParseAddr(strings.TrimSpace(ep)); err != nil {
				fmt.Println("warning: a configured VPN endpoint is a hostname, and Strict turns off")
				fmt.Println("         vpn.allowPhysicalDNS — it will not be able to re-resolve while the")
				fmt.Println("         tunnel is down. Consider a literal IP for vpn.endpoints, or a preset")
				fmt.Println("         other than Strict.")
				break
			}
		}
	}

	if useToken {
		tok, terr := readTokenStdin()
		if terr != nil {
			fmt.Fprintln(os.Stderr, terr)
			return 2
		}
		if code, handled := tryConfigWrite(flagVal, p.Values, tok); handled {
			return code
		}
	}

	keys := make([]string, 0, len(p.Values))
	for k := range p.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		field, ok := configFields[k]
		if !ok {
			// Every preset key is a real configFields entry — pinned by
			// internal/config's TestPresetsAreWellFormed and this package's
			// TestSettableKeysAndReloadKeysAgree. Reaching this is a bug.
			fmt.Fprintf(os.Stderr, "preset %s: %q has no way to set it (this is a bug)\n", p.Name, k)
			return 1
		}
		if err := field.set(cfg, p.Values[k]); err != nil {
			fmt.Fprintf(os.Stderr, "invalid value for %s: %v\n", k, err)
			return 1
		}
	}
	typed := renderKeys(cfg, keys)
	path := writeTargetPath(flagVal)
	if err := writeConfig(path, cfg); err != nil {
		return saveError(path, err)
	}
	for _, k := range keys {
		fmt.Printf("set %s = %s  (%s)\n", k, configFields[k].get(cfg), path)
	}
	noteCoercions(cfg, typed)
	notifyReload(flagVal)
	return 0
}

// resetViaToken sends a reset as an ordinary config-write of the shipped default
// values — a reset is a write like any other, and routing it through the same op
// keeps one validated path instead of two.
//
// `--all` is deliberately NOT offered here, and the reason survives every key
// becoming settable (vpn.advanced.* now is; that gap is closed). The local
// `--all` is `*cfg = config.Default()` with identity preserved — it resets
// whatever the struct holds, including a field added tomorrow. The socket op
// can only carry an ENUMERATION of key=value pairs, so it resets whatever
// configFields currently remembers, which is the same thing only for as long as
// nobody adds a field without a configFields entry. "Reset everything" is
// exactly the command that must not quietly mean "reset the part we listed", so
// refusing sends the caller to the privileged path, which does the whole job.
func resetViaToken(flagVal string, args []string) (code int, handled bool) {
	if len(args) == 1 && args[0] == "--all" {
		fmt.Fprintln(os.Stderr, "config reset --all cannot go over the control socket (it resets keys the socket op cannot express)")
		fmt.Fprintln(os.Stderr, "run it with root instead: sudo dezhban config reset --all")
		return 2, true
	}
	def := config.Default()
	config.Normalize(&def)

	pairs := make(map[string]string, len(args))
	for _, k := range args {
		field, ok := configFields[k]
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown key %q\nvalid keys: %s\n", k, knownKeys())
			return 2, true
		}
		// The shipped default rendered through the same accessor `set` uses, so a
		// key resets to exactly what setting it to that value would produce.
		pairs[k] = field.get(&def)
	}
	tok, err := readTokenStdin()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2, true
	}
	return tryConfigWrite(flagVal, pairs, tok)
}

type setPair struct{ key, val string }

// parseSetPairs accepts either the two-positional form (`<key> <value>`, kept so
// every existing invocation and script still works) or one-or-more `key=value`
// args. The two are not mixed: a bare 2-arg call whose first arg has no "=" is the
// legacy form, everything else must be key=value.
func parseSetPairs(args []string) ([]setPair, error) {
	if len(args) == 0 {
		return nil, errors.New("config set: no key given")
	}
	if len(args) == 2 && !strings.Contains(args[0], "=") {
		return []setPair{{key: args[0], val: args[1]}}, nil
	}
	pairs := make([]setPair, 0, len(args))
	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("config set: %q is not key=value", a)
		}
		pairs = append(pairs, setPair{key: k, val: v})
	}
	return pairs, nil
}

func configEdit(flagVal string) int {
	path := writeTargetPath(flagVal)

	// Seed an UNPRIVILEGED temp file with the current config (or defaults), let
	// $EDITOR run as the invoking user on that temp, validate it, then elevate only
	// the final write. Running $EDITOR under a whole-process sudo re-exec would run
	// the editor (and any EDITOR=… shell it names) as root — avoid that. Validating
	// the temp before persisting also means a broken edit never overwrites the
	// live config.
	seed, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(os.Stderr, "read config:", err)
			return 1
		}
		def := config.Default()
		if seed, err = config.Marshal(&def); err != nil {
			fmt.Fprintln(os.Stderr, "encode defaults:", err)
			return 1
		}
	}

	tmp, err := os.CreateTemp("", "dezhban-config-*.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create temp:", err)
		return 1
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(seed); err != nil {
		_ = tmp.Close()
		fmt.Fprintln(os.Stderr, "write temp:", err)
		return 1
	}
	if err := tmp.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "write temp:", err)
		return 1
	}

	editor := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"))
	if editor == "" {
		if runtime.GOOS == "windows" {
			editor = "notepad"
		} else {
			editor = "vi"
		}
	}
	cmd := exec.Command(editor, tmpPath)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "editor %q failed: %v\n", editor, err)
		return 1
	}

	edited, err := config.Load(tmpPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "not saved — edited config is invalid:", err)
		return 1
	}
	if err := writeConfig(path, edited); err != nil {
		return saveError(path, err)
	}
	fmt.Println("config saved:", path)
	return 0
}

// saveError renders a save failure, with a sudo hint on permission denial (the
// canonical config lives under /etc, writable only by root).
func saveError(path string, err error) int {
	if errors.Is(err, fs.ErrPermission) {
		fmt.Fprintf(os.Stderr, "permission denied writing %s — try: sudo dezhban config ...\n", path)
		return 1
	}
	fmt.Fprintln(os.Stderr, "save failed:", err)
	return 1
}

func knownKeys() string {
	keys := make([]string, 0, len(configFields))
	for k := range configFields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

func setBool(dst *bool, v string) error {
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("expected true/false, got %q", v)
	}
	*dst = b
	return nil
}

func setDuration(dst *time.Duration, v string) error {
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("expected a duration like \"30s\": %w", err)
	}
	*dst = d
	return nil
}

// setLimitDuration is setDuration for a key that is a LIMIT rather than a
// feature. "0" is refused by name instead of being accepted and then silently
// restored to the default by Normalize: on every other duration here "0" means
// off, so someone typing it deserves to be told that off is not a thing a bound
// can be, rather than to walk away believing the limit was lifted.
func setLimitDuration(dst *time.Duration, v string, key string) error {
	var d time.Duration
	if err := setDuration(&d, v); err != nil {
		return err
	}
	if d <= 0 {
		return fmt.Errorf("%s is a limit, not a feature — there is no \"off\" for it. "+
			"Raise it to relax the bound, or set vpn.redialWindow to \"0\" to turn the "+
			"automatic redial window off entirely", key)
	}
	*dst = d
	return nil
}

// splitList parses a comma-separated value into a trimmed, empty-dropped slice.
func splitList(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
