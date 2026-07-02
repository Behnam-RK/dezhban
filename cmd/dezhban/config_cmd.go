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
  edit              Open the config in $EDITOR (created from defaults if missing)

Keys (dotted; list values are comma-separated):
  pollInterval blockedCountries failClosed hysteresis providers
  allowlist.dns allowlist.hosts providerQuorum logLevel
  vpn.enabled vpn.tunnelInterfaces vpn.endpoints vpn.autodetect
  vpn.autoDiscoverEndpoints vpn.endpointRefresh vpn.tunnelWatch`

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
	"failClosed": {
		get: func(c *config.Config) string { return strconv.FormatBool(c.FailClosed) },
		set: func(c *config.Config, v string) error { return setBool(&c.FailClosed, v) },
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
	"allowlist.dns": {
		get: func(c *config.Config) string { return strings.Join(c.Allowlist.DNS, ",") },
		set: func(c *config.Config, v string) error { c.Allowlist.DNS = splitList(v); return nil },
	},
	"allowlist.hosts": {
		get: func(c *config.Config) string { return strings.Join(c.Allowlist.Hosts, ",") },
		set: func(c *config.Config, v string) error { c.Allowlist.Hosts = splitList(v); return nil },
	},
	"providerQuorum": {
		get: func(c *config.Config) string { return strconv.FormatBool(c.ProviderQuorum) },
		set: func(c *config.Config, v string) error { return setBool(&c.ProviderQuorum, v) },
	},
	"logLevel": {
		get: func(c *config.Config) string { return c.LogLevel },
		set: func(c *config.Config, v string) error { c.LogLevel = strings.ToLower(strings.TrimSpace(v)); return nil },
	},
	"vpn.enabled": {
		get: func(c *config.Config) string { return strconv.FormatBool(c.VPN.Enabled) },
		set: func(c *config.Config, v string) error { return setBool(&c.VPN.Enabled, v) },
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
	"vpn.endpointRefresh": {
		get: func(c *config.Config) string { return c.VPN.EndpointRefresh.String() },
		set: func(c *config.Config, v string) error { return setDuration(&c.VPN.EndpointRefresh, v) },
	},
	"vpn.tunnelWatch": {
		get: func(c *config.Config) string { return c.VPN.TunnelWatch.String() },
		set: func(c *config.Config, v string) error { return setDuration(&c.VPN.TunnelWatch, v) },
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

func configSet(flagVal string, args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: dezhban config set <key> <value>")
		return 2
	}
	key, val := args[0], args[1]
	field, ok := configFields[key]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown key %q\nvalid keys: %s\n", key, knownKeys())
		return 2
	}
	cfg, err := loadConfig(flagVal)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}
	if err := field.set(cfg, val); err != nil {
		fmt.Fprintln(os.Stderr, "invalid value:", err)
		return 1
	}
	path := writeTargetPath(flagVal)
	if err := writeConfig(path, cfg); err != nil {
		return saveError(path, err)
	}
	fmt.Printf("set %s = %s  (%s)\n", key, field.get(cfg), path)
	return 0
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
