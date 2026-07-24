package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/behnam-rk/dezhban/internal/config"
)

const configUsage = `usage: dezhban config <subcommand>

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
  edit              Open the config in $EDITOR (created from defaults if missing)

Keys (dotted; list values are comma-separated):
  pollInterval blockedCountries hysteresis providers providerQuorum logLevel
  vpn.tunnelInterfaces vpn.endpoints vpn.autodetect
  vpn.autoDiscoverEndpoints vpn.allowPhysicalDNS vpn.allowLocalNetwork
  vpn.autoArm vpn.armAtBoot vpn.switchWindow
  vpn.redialWindow vpn.pauseMax vpn.endpointRefresh vpn.endpointGrace vpn.tunnelWatch
  control.enabled control.socket control.group control.allowSwitchOps
  control.allowPauseOps control.allowConfigOps
  (VPN profiles are managed with 'dezhban vpn add/remove', not 'config set')`

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
	"vpn.autodetect": {
		get: func(c *config.Config) string { return strconv.FormatBool(c.VPN.Autodetect) },
		set: func(c *config.Config, v string) error { return setBool(&c.VPN.Autodetect, v) },
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
}

func cmdConfig(args []string) int {
	// The config subcommands take positional args (get <key>, set <key> <val>), so a
	// --config flag can appear anywhere; pull it out before dispatch and thread the
	// resolved path through, otherwise an explicit --config is silently ignored.
	cfgPath, args := stripConfigFlag(args)
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
		return configSet(cfgPath, rest)
	case "reset":
		return configReset(cfgPath, rest)
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
func configSet(flagVal string, args []string) int {
	pairs, err := parseSetPairs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, "usage: dezhban config set <key> <value>")
		fmt.Fprintln(os.Stderr, "       dezhban config set <key>=<value> [<key>=<value> ...]")
		return 2
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
	path := writeTargetPath(flagVal)
	if err := writeConfig(path, cfg); err != nil {
		return saveError(path, err)
	}
	for _, p := range pairs {
		fmt.Printf("set %s = %s  (%s)\n", p.key, configFields[p.key].get(cfg), path)
	}
	// Writing the file used to be the whole story, which is why "I changed a
	// setting and nothing happened" was the most common complaint: the daemon
	// read its config once at startup and nobody ever told it to look again.
	notifyReload(flagVal)
	return 0
}

// configReset restores config keys to their shipped defaults — the CLI twin of
// the GUI's per-field ↺. `--all` resets every tunable but preserves identity
// data (what the user protects and how to reach their VPNs); resetting those to
// empty would not be "defaults", it would be data loss.
func configReset(flagVal string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dezhban config reset <key> [key ...] | --all")
		return 2
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
