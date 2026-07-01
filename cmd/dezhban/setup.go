package main

import (
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"

	"github.com/behnam-rk/dezhban/internal/config"
	"github.com/behnam-rk/dezhban/internal/firewall"
	"github.com/behnam-rk/dezhban/internal/netdetect"
)

// commonBlocked are the codes offered as checkboxes in the wizard; any others can
// be typed in the free-text field.
var commonBlocked = []struct{ label, code string }{
	{"Iran (IR)", "IR"},
	{"Russia (RU)", "RU"},
	{"China (CN)", "CN"},
	{"North Korea (KP)", "KP"},
	{"Syria (SY)", "SY"},
	{"Cuba (CU)", "CU"},
	{"Belarus (BY)", "BY"},
}

// cmdSetup runs an interactive wizard that builds a config and writes it, so the
// user never hand-edits JSON. It reuses the same detection/validation/preview
// helpers as detect-vpn, validate and print-rules. Requires a TTY.
func cmdSetup(args []string) int {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to write the config (default: canonical system path)")
	_ = fs.Parse(args)

	if !isInteractive() {
		fmt.Fprintln(os.Stderr, "dezhban setup needs an interactive terminal.")
		fmt.Fprintln(os.Stderr, "Non-interactive? Use 'dezhban config set <key> <value>' or edit the file directly.")
		return 1
	}

	// Seed from the current config so setup edits rather than clobbers; fall back
	// to defaults if none exists or the current file is unreadable/invalid.
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		d := config.Default()
		cfg = &d
	}

	// --- wizard state (huh binds to string/bool/[]string) ---
	pollInterval := cfg.PollInterval.String()
	hysteresis := strconv.Itoa(cfg.Hysteresis)
	logLevel := cfg.LogLevel
	failClosed := cfg.FailClosed
	quorum := cfg.ProviderQuorum
	vpnEnabled := cfg.VPN.Enabled

	blockedSet := map[string]bool{}
	for _, c := range cfg.BlockedCountries {
		blockedSet[strings.ToUpper(c)] = true
	}
	var checkedCountries []string
	countryOpts := make([]huh.Option[string], 0, len(commonBlocked))
	for _, cc := range commonBlocked {
		opt := huh.NewOption(cc.label, cc.code)
		if blockedSet[cc.code] {
			opt = opt.Selected(true)
			delete(blockedSet, cc.code)
		}
		countryOpts = append(countryOpts, opt)
	}
	// Any configured codes not in the common list seed the free-text field.
	var extraCodes []string
	for code := range blockedSet {
		extraCodes = append(extraCodes, code)
	}
	otherCountries := strings.Join(extraCodes, ",")

	basics := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Poll interval").Description("How often the country is checked, e.g. 30s.").
			Value(&pollInterval).Validate(validDuration),
		huh.NewMultiSelect[string]().Title("Blocked countries").
			Description("Traffic is cut when the detected country matches.").
			Options(countryOpts...).Value(&checkedCountries),
		huh.NewInput().Title("Other country codes").Description("Comma-separated ISO codes not listed above (optional).").
			Value(&otherCountries),
		huh.NewSelect[string]().Title("Log level").
			Options(huh.NewOptions("debug", "info", "warn", "error")...).Value(&logLevel),
		huh.NewConfirm().Title("Fail closed?").Description("Block when the country can't be determined (recommended).").
			Value(&failClosed),
		huh.NewConfirm().Title("Require provider quorum?").Description("Only act when a majority of providers agree.").
			Value(&quorum),
		huh.NewConfirm().Title("Behind a full-tunnel VPN?").
			Description("Enables the always-on interface guard (the primary, zero-leak mode).").
			Value(&vpnEnabled),
	))
	if err := runForm(basics); err != nil {
		return formExit(err)
	}

	// --- VPN branch ---
	var tunnels []string
	endpoints := strings.Join(cfg.VPN.Endpoints, ",")
	autoDiscover := cfg.VPN.AutoDiscoverEndpoints
	if vpnEnabled {
		detected, _ := netdetect.TunnelInterfaces()
		tunnelField := tunnelSelector(detected, cfg.VPN.TunnelInterfaces, &tunnels)
		if err := runForm(huh.NewForm(huh.NewGroup(
			tunnelField,
			huh.NewConfirm().Title("Auto-discover the VPN endpoint?").
				Description("macOS only: learn the server IP from the live socket (good for rotating-pool VPNs).").
				Value(&autoDiscover),
			huh.NewInput().Title("VPN endpoint(s)").
				Description("Server IP(s)/hostname(s) on the physical link, comma-separated. Leave blank only if auto-discovering.").
				Value(&endpoints),
		))); err != nil {
			return formExit(err)
		}
	}

	// --- assemble into the config ---
	applyWizard(cfg, wizardInput{
		pollInterval: pollInterval, hysteresis: hysteresis, logLevel: logLevel,
		failClosed: failClosed, quorum: quorum,
		countries:  append(checkedCountries, splitList(otherCountries)...),
		vpnEnabled: vpnEnabled, tunnels: tunnels, endpoints: splitList(endpoints),
		autoDiscover: autoDiscover,
	})

	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "\nthat config isn't valid yet:", err)
		fmt.Fprintln(os.Stderr, "re-run 'dezhban setup' and adjust the flagged field.")
		return 1
	}

	// --- lockout guard: warn if an endpoint sits inside a tunnel subnet ---
	if vpnEnabled {
		if warn := endpointLockoutWarning(cfg); warn != "" {
			var proceed bool
			fmt.Fprintln(os.Stderr, warn)
			if err := runForm(huh.NewForm(huh.NewGroup(
				huh.NewConfirm().Title("Save anyway?").
					Description("The flagged endpoint would very likely lock you out.").Value(&proceed),
			))); err != nil {
				return formExit(err)
			}
			if !proceed {
				fmt.Fprintln(os.Stderr, "setup cancelled — fix the endpoint (see 'dezhban doctor').")
				return 1
			}
		}
	}

	// --- preview the exact ruleset, then confirm ---
	mode := "legacy"
	if vpnEnabled {
		mode = "guard"
	}
	if pol, err := policyForMode(cfg, newLogger(cfg), mode); err == nil {
		if rules, err := firewall.RenderRules(pol); err == nil {
			fmt.Fprintf(os.Stderr, "\nRuleset this config would apply (%s mode):\n\n%s\n", mode, rules)
		}
	}

	path := *cfgPath
	if path == "" {
		path = writeTargetPath()
	}
	var save bool
	if err := runForm(huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title(fmt.Sprintf("Write config to %s?", path)).Value(&save),
	))); err != nil {
		return formExit(err)
	}
	if !save {
		fmt.Fprintln(os.Stderr, "not saved.")
		return 0
	}
	if err := config.Save(path, cfg); err != nil {
		return saveError(path, err)
	}
	fmt.Printf("saved %s\n", path)
	if vpnEnabled {
		fmt.Println("start the guard with: sudo dezhban run   (or install it: sudo dezhban install && sudo dezhban start)")
	}
	return 0
}

// tunnelSelector returns a MultiSelect over detected tunnels (preselecting the
// configured ones), or a free-text Input when nothing was detected.
func tunnelSelector(detected, configured []string, dst *[]string) huh.Field {
	if len(detected) == 0 {
		// No live tunnels — fall back to comma-separated entry via a shim.
		joined := strings.Join(configured, ",")
		return huh.NewInput().Title("Tunnel interface(s)").
			Description("None detected. Enter comma-separated names (e.g. utun4).").
			Value(&joined).Validate(func(string) error {
			*dst = splitList(joined)
			return nil
		})
	}
	cfgSet := map[string]bool{}
	for _, t := range configured {
		cfgSet[t] = true
	}
	opts := make([]huh.Option[string], 0, len(detected))
	for _, t := range detected {
		opt := huh.NewOption(t, t)
		if cfgSet[t] {
			opt = opt.Selected(true)
		}
		opts = append(opts, opt)
	}
	return huh.NewMultiSelect[string]().Title("Tunnel interface(s)").
		Description("Detected tunnels — pick the VPN's.").Options(opts...).Value(dst)
}

// wizardInput carries the collected answers into the config.
type wizardInput struct {
	pollInterval, hysteresis, logLevel string
	failClosed, quorum                 bool
	countries                          []string
	vpnEnabled                         bool
	tunnels, endpoints                 []string
	autoDiscover                       bool
}

// applyWizard writes collected answers onto cfg. Validation happens after.
func applyWizard(cfg *config.Config, in wizardInput) {
	if d, err := time.ParseDuration(in.pollInterval); err == nil {
		cfg.PollInterval = d
	}
	if n, err := strconv.Atoi(strings.TrimSpace(in.hysteresis)); err == nil {
		cfg.Hysteresis = n
	}
	cfg.LogLevel = in.logLevel
	cfg.FailClosed = in.failClosed
	cfg.ProviderQuorum = in.quorum
	cfg.BlockedCountries = dedupeUpper(in.countries)

	cfg.VPN.Enabled = in.vpnEnabled
	if in.vpnEnabled {
		cfg.VPN.TunnelInterfaces = in.tunnels
		cfg.VPN.Endpoints = in.endpoints
		cfg.VPN.AutoDiscoverEndpoints = in.autoDiscover
	}
}

// endpointLockoutWarning returns a non-empty message if any IP endpoint sits
// inside a tunnel's own subnet — the #1 lockout cause.
func endpointLockoutWarning(cfg *config.Config) string {
	var addrs []netip.Addr
	for _, ep := range cfg.VPN.Endpoints {
		if a, err := netip.ParseAddr(ep); err == nil {
			addrs = append(addrs, a)
		}
	}
	if len(addrs) == 0 {
		return ""
	}
	bad, err := netdetect.CheckEndpointRouting(addrs, cfg.VPN.TunnelInterfaces)
	if err != nil || len(bad) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n⚠  WARNING: endpoint(s) sit inside a tunnel subnet — this will likely lock you out:\n")
	for _, r := range bad {
		fmt.Fprintf(&b, "     %s is within %s (%s)\n", r.Endpoint, r.Subnet, r.Iface)
	}
	b.WriteString("   Set the VPN server's PHYSICAL (public) address instead — see 'dezhban doctor --discover'.")
	return b.String()
}

func dedupeUpper(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		u := strings.ToUpper(strings.TrimSpace(s))
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

// runForm runs a huh form with a consistent theme.
func runForm(f *huh.Form) error {
	return f.WithTheme(huh.ThemeBase16()).Run()
}

// formExit maps a form error to an exit code, treating user-abort as a clean cancel.
func formExit(err error) int {
	if errors.Is(err, huh.ErrUserAborted) {
		fmt.Fprintln(os.Stderr, "setup cancelled.")
		return 130
	}
	fmt.Fprintln(os.Stderr, "setup error:", err)
	return 1
}

// validDuration validates a huh Input holding a positive Go duration.
func validDuration(s string) error {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return errors.New("enter a duration like 30s or 5m")
	}
	if d <= 0 {
		return errors.New("must be greater than zero")
	}
	return nil
}

// isInteractive reports whether both stdin and stdout are terminals.
func isInteractive() bool {
	return isCharDevice(os.Stdin) && isCharDevice(os.Stdout)
}

func isCharDevice(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
