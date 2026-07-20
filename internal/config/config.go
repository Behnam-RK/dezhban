// Package config defines dezhban's runtime configuration and loading.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Allowlist names the destinations that must stay reachable even while blocking,
// so recovery detection (geo-API lookups) keeps working. Loopback is always
// allowed implicitly by the firewall backends.
type Allowlist struct {
	// DNS resolver IPs that must stay reachable for hostname re-resolution.
	DNS []string `json:"dns"`
	// Hosts is extra host IPs to always allow (provider IPs are added at runtime).
	Hosts []string `json:"hosts"`
}

// VPN configures the interface-aware kill-switch guard for hosts behind a
// full-tunnel VPN. When Enabled, enforcement uses the tunnel interface(s) and
// VPN endpoint(s) instead of the destination-IP allowlist (which is meaningless
// under a tunnel). See docs/plans VPN mode. Disabled by default — the always-on
// guard can lock a host out if misconfigured, so it is opt-in.
type VPN struct {
	// Enabled turns on VPN guard mode.
	Enabled bool
	// TunnelInterfaces are the VPN tunnel interface names (e.g. "utun4"). For now
	// these are concrete names; autodetect/pattern expansion lands with netdetect.
	TunnelInterfaces []string
	// Endpoints are the VPN server addresses reachable on the physical interface,
	// kept open so the tunnel can stay up and reconnect. Each entry may be an IP
	// literal or a hostname (resolved and re-resolved at runtime) — hostnames let
	// third-party VPNs that publish a server name rather than a fixed IP be used.
	Endpoints []string
	// Autodetect requests discovery of the tunnel interface (netdetect); explicit
	// TunnelInterfaces always win.
	Autodetect bool
	// AutoDiscoverEndpoints continuously learns the live VPN server IP from the
	// active socket (macOS only) and keeps that egress open, so a rotating-pool
	// VPN (NordVPN/ProtonVPN/…) needs no endpoint typed by hand. On other
	// platforms it is ignored and the host falls back to Endpoints.
	AutoDiscoverEndpoints bool
	// AllowPhysicalDNS opens plain DNS (port 53) egress on the physical link in
	// guard and VPN full-block rulesets, so a VPN client can re-resolve its
	// server hostname and reconnect while the tunnel is down. ON by default
	// (2026-07-19 defaults review: reconnectability beats hiding DNS-query
	// metadata for this project's users); set false to close the metadata leak.
	AllowPhysicalDNS bool
	// AutoArm starts the daemon PASSIVE (posture "standby", no enforcement)
	// when no tunnel interface is present, arming the guard automatically the
	// moment a VPN connects. Never disarms on tunnel loss — a drop is exactly
	// the leak the kill switch exists for; only an explicit unblock with the
	// tunnel down returns to standby. ON by default (2026-07-19 defaults
	// review: a guard armed with no VPN cuts everything, and that mystery
	// blackout hurt new users more than the pre-first-connect gap); set false
	// for the stricter armed-from-startup posture.
	AutoArm bool
	// EndpointRefresh is how often hostnames are re-resolved and live discovery
	// re-run. Defaults to 1m — local work only, and the fast cadence promotes a
	// roamed-to server to learned within ~3 minutes.
	EndpointRefresh time.Duration
	// EndpointGrace is how long an autodiscovered endpoint stays in the allowed
	// set after a refresh stops reporting it — the window in which a dropped VPN
	// can redial the same server (its socket, the only thing discovery can see,
	// died with the tunnel). Defaults to 15m.
	EndpointGrace time.Duration
	// TunnelWatch is how often the tunnel interface(s) are sampled for up/down so
	// a drop cuts the network at once instead of waiting for the next geo poll.
	// Defaults to 1s.
	TunnelWatch time.Duration
	// Profiles are named VPNs whose server endpoints are always kept reachable
	// (the guard passes the union of all profiles' endpoints), so switching
	// between known VPNs needs no reconfiguration. See Profile.
	Profiles []Profile
	// SwitchWindow is the default duration of a `dezhban switch` window — a
	// bounded, explicitly-triggered relaxation during which a brand-new VPN's
	// handshake to an as-yet-unknown server is allowed so its endpoint can be
	// learned. Defaults to 15s; validated to [10s, Advanced.SwitchWindowMax].
	SwitchWindow time.Duration
	// ReconnectWindow is the duration of the AUTOMATIC reconnect window: when
	// the tunnel drops while the guard is healthy (GUARD posture, not standby,
	// not FULL BLOCK), the daemon opens a switch-window relaxation for this long
	// so the VPN client can redial any server — including one dezhban has never
	// seen (rotating-pool and 443-fronted VPNs pick fresh IPs on every connect).
	// It closes early the moment a good exit is confirmed, learns the new
	// endpoint, and on expiry reverts fail-closed. Defaults to 30s; an explicit
	// "0" disables the automatic window (kept internally as a negative sentinel
	// so Normalize can tell "disabled" from "absent"); validated to
	// [5s, Advanced.SwitchWindowMax] otherwise.
	ReconnectWindow time.Duration
	// Advanced holds tunables for behaviors that are otherwise baked-in design
	// decisions. Every field defaults in Normalize; an absent `advanced` block
	// keeps the recommended defaults. Touch only if you know why.
	Advanced Advanced
}

// Profile is a named VPN whose server endpoint(s) dezhban keeps reachable on the
// physical link. Endpoints use the same grammar as VPN.Endpoints (IP literal or
// hostname, no port).
type Profile struct {
	// Name uniquely identifies the profile (case-insensitive). Charset
	// [A-Za-z0-9._-], 1–64 chars.
	Name string
	// Endpoints are the profile's VPN server addresses (≥1 required).
	Endpoints []string
	// IfaceHint is an optional tunnel-interface name prefix (e.g. "wg",
	// "nordlynx") shown in `vpn list` output to help identify a profile. It never
	// gates enforcement — pinning an interface by name goes stale across
	// reconnects, so the hint is advisory / display-only.
	IfaceHint string
}

// Advanced holds tunables for VPN guard / switch-window / endpoint-learning
// behaviors. These are design-decision constants surfaced as knobs; the defaults
// (applied in Normalize) are the recommended values.
type Advanced struct {
	// SwitchWindowMax caps any switch window, including a `--for` override.
	// Default 5m.
	SwitchWindowMax time.Duration
	// CommandFreshness is how recent a control-file command must be to be acted
	// on (replay/stale-file guard). Default 30s.
	CommandFreshness time.Duration
	// WindowDiscoveryInterval is how often endpoint discovery runs while a switch
	// window is open (fast, to learn the new server quickly). Default 2s.
	WindowDiscoveryInterval time.Duration
	// TunnelPruneAfter is how long a dynamically-detected tunnel interface must be
	// absent from the system before it is dropped from the guard set. Explicit
	// TunnelInterfaces are never pruned. Default 60s.
	TunnelPruneAfter time.Duration
	// LearnedEndpointTTL is how long an unused learned endpoint is retained before
	// pruning. Default 720h (30 days).
	LearnedEndpointTTL time.Duration
	// LearnedMaxPerProfile caps learned endpoints per profile (LRU by last-seen).
	// Default 16.
	LearnedMaxPerProfile int
	// PromoteAfterRefreshes is how many consecutive endpoint refreshes a
	// discovered address must appear in (while the tunnel is up and the last
	// verdict was allow) before it is promoted to a learned endpoint during
	// normal guard. Default 3.
	PromoteAfterRefreshes int
	// EndpointWarnThreshold is the union-size at which doctor warns about
	// rule-list bloat. Default 256.
	EndpointWarnThreshold int
	// ReconnectMinUptime is the anti-flap gate on the automatic reconnect
	// window: an auto-window opens only if the tunnel had been up at least this
	// long, or a non-blocked exit was confirmed during that uptime. Without it a
	// VPN flapping up/down would chain windows and turn the guard into a sieve.
	// Default 15s; an explicit "0" disables the gate (negative sentinel
	// internally, same convention as VPN.ReconnectWindow).
	ReconnectMinUptime time.Duration
	// WindowProtocols / WindowPorts optionally restrict a switch window to the
	// given protocols ("udp"/"tcp") and destination ports instead of allowing all
	// outbound. Empty (default) = allow all outbound for the window's duration.
	// Only useful when every VPN you switch to uses a known fixed port set (e.g.
	// WireGuard on 51820); leaving it empty is the honest default (see modes.md).
	WindowProtocols []string
	WindowPorts     []int
}

// Config is dezhban's validated runtime configuration.
type Config struct {
	// PollInterval is how often the monitor checks the current country.
	PollInterval time.Duration
	// BlockedCountries are ISO-3166 alpha-2 codes that trigger a block.
	BlockedCountries []string
	// FailClosed blocks traffic when the country cannot be determined.
	FailClosed bool
	// Hysteresis is the consecutive agreeing readings required before toggling.
	Hysteresis int
	// Providers are geo-location endpoint URLs, tried for redundancy.
	Providers []string
	// Allowlist holds destinations kept reachable while blocking.
	Allowlist Allowlist
	// ProviderQuorum requires a majority of providers to agree on the country.
	ProviderQuorum bool
	// LogLevel is one of debug, info, warn, error.
	LogLevel string
	// VPN configures the interface-aware guard for full-tunnel VPN hosts.
	VPN VPN
	// Control configures the daemon's live control socket (passwordless routine ops).
	Control Control
}

// Control configures the daemon's control socket: a unix socket the CLI (and,
// through it, the menubar app) uses for routine posture changes, so an admin is
// not prompted for a password on every block/unblock/switch.
//
// The trust boundary is filesystem permissions — the socket is root-owned, mode
// 0660, group-owned by Group. Anyone in that group can drive the ops the daemon
// exposes. Ops are deliberately limited to postures the daemon's own state machine
// already sanctions; `panic` is NOT among them, so the lockout escape hatch always
// requires root and never depends on a running daemon.
type Control struct {
	// Enabled turns the control socket on. Default true.
	Enabled bool
	// Socket is the socket path. Empty → <state dir>/control.sock.
	Socket string
	// Group is the unix group permitted to talk to the daemon (macOS: "admin" —
	// the machine's administrators). Empty → root-only (0600), which leaves the
	// socket useless to unprivileged callers: an explicit opt-out.
	Group string
	// AllowSwitchOps permits opening/cancelling a switch window over the socket.
	// Default true — a switch window is the one op that RELAXES the guard, so it
	// gets its own switch: set it false to force switch ops back to the root-owned
	// command file (`sudo dezhban switch`).
	AllowSwitchOps bool
}

// fileConfig is the on-disk JSON shape. Durations are strings (e.g. "30s")
// because JSON has no native duration type. Pointer fields distinguish
// "absent" (keep default) from a zero value the user set deliberately.
type fileConfig struct {
	PollInterval     string       `json:"pollInterval"`
	BlockedCountries []string     `json:"blockedCountries"`
	FailClosed       *bool        `json:"failClosed"`
	Hysteresis       *int         `json:"hysteresis"`
	Providers        []string     `json:"providers"`
	Allowlist        Allowlist    `json:"allowlist"`
	ProviderQuorum   *bool        `json:"providerQuorum"`
	LogLevel         string       `json:"logLevel"`
	VPN              *fileVPN     `json:"vpn"`
	Control          *fileControl `json:"control,omitempty"`
}

// fileControl is the on-disk shape of the control block. The pointers distinguish
// "absent" (keep the default — both bools default to true) from a deliberate zero.
// Group is a pointer for the same reason: an explicit "" means root-only, and must
// not be mistaken for an absent key that keeps the platform default.
type fileControl struct {
	Enabled        *bool   `json:"enabled,omitempty"`
	Socket         string  `json:"socket,omitempty"`
	Group          *string `json:"group,omitempty"`
	AllowSwitchOps *bool   `json:"allowSwitchOps,omitempty"`
}

// fileVPN is the on-disk shape of the VPN block. A pointer in fileConfig lets an
// absent block keep defaults.
type fileVPN struct {
	Enabled               bool     `json:"enabled"`
	TunnelInterfaces      []string `json:"tunnelInterfaces"`
	Endpoints             []string `json:"endpoints"`
	Autodetect            bool     `json:"autodetect"`
	AutoDiscoverEndpoints bool     `json:"autoDiscoverEndpoints"`
	// Pointers: both default to TRUE, so an explicit false must be
	// distinguishable from an absent key (same convention as fileControl).
	AllowPhysicalDNS *bool         `json:"allowPhysicalDNS,omitempty"`
	AutoArm          *bool         `json:"autoArm,omitempty"`
	EndpointRefresh  string        `json:"endpointRefresh"`
	EndpointGrace    string        `json:"endpointGrace,omitempty"`
	TunnelWatch      string        `json:"tunnelWatch"`
	Profiles         []fileProfile `json:"profiles,omitempty"`
	SwitchWindow     string        `json:"switchWindow,omitempty"`
	ReconnectWindow  string        `json:"reconnectWindow,omitempty"`
	Advanced         *fileAdvanced `json:"advanced,omitempty"`
}

type fileProfile struct {
	Name      string   `json:"name"`
	Endpoints []string `json:"endpoints"`
	IfaceHint string   `json:"ifaceHint,omitempty"`
}

type fileAdvanced struct {
	SwitchWindowMax         string   `json:"switchWindowMax,omitempty"`
	CommandFreshness        string   `json:"commandFreshness,omitempty"`
	WindowDiscoveryInterval string   `json:"windowDiscoveryInterval,omitempty"`
	TunnelPruneAfter        string   `json:"tunnelPruneAfter,omitempty"`
	LearnedEndpointTTL      string   `json:"learnedEndpointTTL,omitempty"`
	LearnedMaxPerProfile    int      `json:"learnedMaxPerProfile,omitempty"`
	PromoteAfterRefreshes   int      `json:"promoteAfterRefreshes,omitempty"`
	EndpointWarnThreshold   int      `json:"endpointWarnThreshold,omitempty"`
	WindowProtocols         []string `json:"windowProtocols,omitempty"`
	WindowPorts             []int    `json:"windowPorts,omitempty"`
	ReconnectMinUptime      string   `json:"reconnectMinUptime,omitempty"`
}

// Default returns a Config with safe, security-first defaults.
func Default() Config {
	return Config{
		// 15s poll × hysteresis 2 confirms a forbidden exit in ~30s worst-case
		// (2026-07-19 defaults review) — the default provider order keeps that
		// volume on unmetered endpoints.
		PollInterval:     15 * time.Second,
		BlockedCountries: nil,
		FailClosed:       true,
		Hysteresis:       2,
		// Ordered by rate-limit headroom: providers are tried in order, so the
		// FIRST reachable one absorbs nearly all poll traffic (at the 15s default
		// that is ~5.8k lookups/day). geojs and country.is are unmetered; the
		// tail (ipinfo 50k/mo, ipapi.co 1k/day) only sees traffic when everything
		// above it is failing.
		Providers: []string{
			"https://get.geojs.io/v1/ip/country.json",
			"https://api.country.is/",
			"http://ip-api.com/json",
			"https://ipwho.is/",
			"https://freeipapi.com/api/json",
			"https://ifconfig.co/json",
			"https://ipinfo.io/json",
			"https://ipapi.co/json/",
		},
		Allowlist:      Allowlist{},
		ProviderQuorum: false,
		LogLevel:       "info",
		// Mirrors the absent-vpn-block defaults in apply(): both on (2026-07-19
		// defaults review). Keep the two in sync.
		VPN: VPN{
			AllowPhysicalDNS: true,
			AutoArm:          true,
		},
		Control: Control{
			Enabled: true,
			// Socket empty → resolved against the daemon's state dir by main.
			Group:          defaultControlGroup,
			AllowSwitchOps: true,
		},
	}
}

// Load reads config from path, layering it over defaults. A missing path is not
// an error: defaults are returned. The result is always validated.
func Load(path string) (*Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			// No file: defaults only.
		case err != nil:
			return nil, fmt.Errorf("read config %q: %w", path, err)
		default:
			var fc fileConfig
			if err := json.Unmarshal(data, &fc); err != nil {
				return nil, fmt.Errorf("parse config %q: %w", path, err)
			}
			if err := apply(&cfg, fc); err != nil {
				return nil, fmt.Errorf("config %q: %w", path, err)
			}
		}
	}

	Normalize(&cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// apply overlays non-empty fields from fc onto cfg.
func apply(cfg *Config, fc fileConfig) error {
	if fc.PollInterval != "" {
		d, err := time.ParseDuration(fc.PollInterval)
		if err != nil {
			return fmt.Errorf("pollInterval: %w", err)
		}
		cfg.PollInterval = d
	}
	if fc.BlockedCountries != nil {
		cfg.BlockedCountries = fc.BlockedCountries
	}
	if fc.FailClosed != nil {
		cfg.FailClosed = *fc.FailClosed
	}
	if fc.Hysteresis != nil {
		cfg.Hysteresis = *fc.Hysteresis
	}
	if fc.Providers != nil {
		cfg.Providers = fc.Providers
	}
	if fc.Allowlist.DNS != nil || fc.Allowlist.Hosts != nil {
		cfg.Allowlist = fc.Allowlist
	}
	if fc.ProviderQuorum != nil {
		cfg.ProviderQuorum = *fc.ProviderQuorum
	}
	if fc.LogLevel != "" {
		cfg.LogLevel = fc.LogLevel
	}
	if fc.VPN != nil {
		v := VPN{
			Enabled:               fc.VPN.Enabled,
			TunnelInterfaces:      fc.VPN.TunnelInterfaces,
			Endpoints:             fc.VPN.Endpoints,
			Autodetect:            fc.VPN.Autodetect,
			AutoDiscoverEndpoints: fc.VPN.AutoDiscoverEndpoints,
			AllowPhysicalDNS:      true, // default on; explicit false below
			AutoArm:               true, // default on; explicit false below
		}
		if fc.VPN.AllowPhysicalDNS != nil {
			v.AllowPhysicalDNS = *fc.VPN.AllowPhysicalDNS
		}
		if fc.VPN.AutoArm != nil {
			v.AutoArm = *fc.VPN.AutoArm
		}
		if fc.VPN.EndpointRefresh != "" {
			d, err := time.ParseDuration(fc.VPN.EndpointRefresh)
			if err != nil {
				return fmt.Errorf("vpn.endpointRefresh: %w", err)
			}
			v.EndpointRefresh = d
		}
		if fc.VPN.EndpointGrace != "" {
			d, err := time.ParseDuration(fc.VPN.EndpointGrace)
			if err != nil {
				return fmt.Errorf("vpn.endpointGrace: %w", err)
			}
			if d < 0 {
				return fmt.Errorf("vpn.endpointGrace: must not be negative (got %s)", d)
			}
			v.EndpointGrace = d
		}
		if fc.VPN.TunnelWatch != "" {
			d, err := time.ParseDuration(fc.VPN.TunnelWatch)
			if err != nil {
				return fmt.Errorf("vpn.tunnelWatch: %w", err)
			}
			v.TunnelWatch = d
		}
		if fc.VPN.SwitchWindow != "" {
			d, err := time.ParseDuration(fc.VPN.SwitchWindow)
			if err != nil {
				return fmt.Errorf("vpn.switchWindow: %w", err)
			}
			v.SwitchWindow = d
		}
		if fc.VPN.ReconnectWindow != "" {
			d, err := time.ParseDuration(fc.VPN.ReconnectWindow)
			if err != nil {
				return fmt.Errorf("vpn.reconnectWindow: %w", err)
			}
			if d < 0 {
				return fmt.Errorf("vpn.reconnectWindow: must not be negative (got %s); use \"0\" to disable", d)
			}
			if d == 0 {
				v.ReconnectWindow = Disabled // explicit opt-out, survives Normalize
			} else {
				v.ReconnectWindow = d
			}
		}
		for _, p := range fc.VPN.Profiles {
			v.Profiles = append(v.Profiles, Profile{
				Name:      p.Name,
				Endpoints: p.Endpoints,
				IfaceHint: p.IfaceHint,
			})
		}
		if fc.VPN.Advanced != nil {
			adv, err := applyAdvanced(fc.VPN.Advanced)
			if err != nil {
				return err
			}
			v.Advanced = adv
		}
		cfg.VPN = v
	}
	if fc.Control != nil {
		if fc.Control.Enabled != nil {
			cfg.Control.Enabled = *fc.Control.Enabled
		}
		if fc.Control.Socket != "" {
			cfg.Control.Socket = fc.Control.Socket
		}
		if fc.Control.Group != nil {
			cfg.Control.Group = *fc.Control.Group
		}
		if fc.Control.AllowSwitchOps != nil {
			cfg.Control.AllowSwitchOps = *fc.Control.AllowSwitchOps
		}
	}
	return nil
}

// applyAdvanced converts the on-disk advanced DTO to the runtime struct. Empty
// duration strings and zero ints stay zero here; Normalize fills the defaults.
func applyAdvanced(fa *fileAdvanced) (Advanced, error) {
	var a Advanced
	parse := func(name, s string, dst *time.Duration) error {
		if s == "" {
			return nil
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("vpn.advanced.%s: %w", name, err)
		}
		*dst = d
		return nil
	}
	if err := parse("switchWindowMax", fa.SwitchWindowMax, &a.SwitchWindowMax); err != nil {
		return a, err
	}
	if err := parse("commandFreshness", fa.CommandFreshness, &a.CommandFreshness); err != nil {
		return a, err
	}
	if err := parse("windowDiscoveryInterval", fa.WindowDiscoveryInterval, &a.WindowDiscoveryInterval); err != nil {
		return a, err
	}
	if err := parse("tunnelPruneAfter", fa.TunnelPruneAfter, &a.TunnelPruneAfter); err != nil {
		return a, err
	}
	if err := parse("learnedEndpointTTL", fa.LearnedEndpointTTL, &a.LearnedEndpointTTL); err != nil {
		return a, err
	}
	if fa.ReconnectMinUptime != "" {
		d, err := time.ParseDuration(fa.ReconnectMinUptime)
		if err != nil {
			return a, fmt.Errorf("vpn.advanced.reconnectMinUptime: %w", err)
		}
		if d < 0 {
			return a, fmt.Errorf("vpn.advanced.reconnectMinUptime: must not be negative (got %s); use \"0\" to disable", d)
		}
		if d == 0 {
			a.ReconnectMinUptime = Disabled // explicit opt-out of the anti-flap gate
		} else {
			a.ReconnectMinUptime = d
		}
	}
	a.LearnedMaxPerProfile = fa.LearnedMaxPerProfile
	a.PromoteAfterRefreshes = fa.PromoteAfterRefreshes
	a.EndpointWarnThreshold = fa.EndpointWarnThreshold
	a.WindowProtocols = fa.WindowProtocols
	a.WindowPorts = fa.WindowPorts
	return a, nil
}

// toFileConfig is the inverse of apply: it projects a validated Config onto the
// on-disk DTO so it round-trips through Load. Durations serialize as strings. The
// vpn block is ALWAYS emitted — gating it on Enabled would silently drop
// tunnelInterfaces/endpoints whenever a config was saved with the guard disabled
// (e.g. `config set vpn.enabled false`), making them unrecoverable on re-enable.
func toFileConfig(c *Config) fileConfig {
	failClosed := c.FailClosed
	hysteresis := c.Hysteresis
	quorum := c.ProviderQuorum
	physDNS := c.VPN.AllowPhysicalDNS
	autoArm := c.VPN.AutoArm
	ctlEnabled := c.Control.Enabled
	ctlGroup := c.Control.Group
	ctlSwitchOps := c.Control.AllowSwitchOps
	return fileConfig{
		PollInterval:     c.PollInterval.String(),
		BlockedCountries: c.BlockedCountries,
		FailClosed:       &failClosed,
		Hysteresis:       &hysteresis,
		Providers:        c.Providers,
		Allowlist:        c.Allowlist,
		ProviderQuorum:   &quorum,
		LogLevel:         c.LogLevel,
		VPN: &fileVPN{
			Enabled:               c.VPN.Enabled,
			TunnelInterfaces:      c.VPN.TunnelInterfaces,
			Endpoints:             c.VPN.Endpoints,
			Autodetect:            c.VPN.Autodetect,
			AutoDiscoverEndpoints: c.VPN.AutoDiscoverEndpoints,
			AllowPhysicalDNS:      &physDNS,
			AutoArm:               &autoArm,
			EndpointRefresh:       c.VPN.EndpointRefresh.String(),
			EndpointGrace:         durString(c.VPN.EndpointGrace),
			TunnelWatch:           c.VPN.TunnelWatch.String(),
			Profiles:              toFileProfiles(c.VPN.Profiles),
			SwitchWindow:          durString(c.VPN.SwitchWindow),
			ReconnectWindow:       optDurString(c.VPN.ReconnectWindow),
			Advanced:              toFileAdvanced(c.VPN.Advanced),
		},
		Control: &fileControl{
			Enabled:        &ctlEnabled,
			Socket:         c.Control.Socket,
			Group:          &ctlGroup,
			AllowSwitchOps: &ctlSwitchOps,
		},
	}
}

func toFileProfiles(ps []Profile) []fileProfile {
	if len(ps) == 0 {
		return nil
	}
	out := make([]fileProfile, len(ps))
	for i, p := range ps {
		out[i] = fileProfile{Name: p.Name, Endpoints: p.Endpoints, IfaceHint: p.IfaceHint}
	}
	return out
}

// durString serializes a duration, or "" when zero so omitempty drops it.
func durString(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return d.String()
}

// optDurString serializes a duration that supports the explicit-disable
// sentinel: negative → "0s" (the user turned it off, and that must round-trip),
// positive → its string, zero (absent) → "" so omitempty drops it.
func optDurString(d time.Duration) string {
	if d < 0 {
		return "0s"
	}
	return durString(d)
}

// toFileAdvanced projects the runtime Advanced onto the on-disk DTO, emitting
// only fields that differ from their Normalize defaults so a config that never
// set `advanced` round-trips without gaining the block.
func toFileAdvanced(a Advanced) *fileAdvanced {
	fa := fileAdvanced{
		WindowProtocols: a.WindowProtocols,
		WindowPorts:     a.WindowPorts,
	}
	nonDefault := len(a.WindowProtocols) > 0 || len(a.WindowPorts) > 0
	if a.SwitchWindowMax != defaultSwitchWindowMax {
		fa.SwitchWindowMax = durString(a.SwitchWindowMax)
		nonDefault = true
	}
	if a.CommandFreshness != defaultCommandFreshness {
		fa.CommandFreshness = durString(a.CommandFreshness)
		nonDefault = true
	}
	if a.WindowDiscoveryInterval != defaultWindowDiscoveryInterval {
		fa.WindowDiscoveryInterval = durString(a.WindowDiscoveryInterval)
		nonDefault = true
	}
	if a.TunnelPruneAfter != defaultTunnelPruneAfter {
		fa.TunnelPruneAfter = durString(a.TunnelPruneAfter)
		nonDefault = true
	}
	if a.LearnedEndpointTTL != defaultLearnedEndpointTTL {
		fa.LearnedEndpointTTL = durString(a.LearnedEndpointTTL)
		nonDefault = true
	}
	if a.LearnedMaxPerProfile != defaultLearnedMaxPerProfile {
		fa.LearnedMaxPerProfile = a.LearnedMaxPerProfile
		nonDefault = true
	}
	if a.PromoteAfterRefreshes != defaultPromoteAfterRefreshes {
		fa.PromoteAfterRefreshes = a.PromoteAfterRefreshes
		nonDefault = true
	}
	if a.EndpointWarnThreshold != defaultEndpointWarnThreshold {
		fa.EndpointWarnThreshold = a.EndpointWarnThreshold
		nonDefault = true
	}
	if a.ReconnectMinUptime != defaultReconnectMinUptime {
		fa.ReconnectMinUptime = optDurString(a.ReconnectMinUptime)
		nonDefault = true
	}
	if !nonDefault {
		return nil
	}
	return &fa
}

// Marshal canonicalizes c, validates it, and returns its pretty-printed on-disk
// JSON (the same bytes Save writes). Used by `dezhban config show` and Save.
// Normalize runs first so every write path — `config set`, the setup wizard,
// programmatic Save — canonicalizes identically to Load, without each caller
// re-implementing case/trim/dedup by hand. c is normalized in place.
func Marshal(c *Config) ([]byte, error) {
	Normalize(c)
	if err := c.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(toFileConfig(c), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return append(data, '\n'), nil
}

// Save validates c and writes it as pretty-printed JSON to path, creating parent
// directories as needed. The file is world-readable (0644) so unprivileged
// inspect commands can read the config the root daemon uses; it holds no secrets.
func Save(path string, c *Config) error {
	data, err := Marshal(c)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create config dir %q: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write config %q: %w", path, err)
	}
	return nil
}

// Normalize canonicalizes values in place: upper-case + trimmed + de-duplicated
// country codes, lower-case log level, trimmed tunnel/endpoint entries, and the
// VPN cadence defaults. It runs on both Load and every write path (via Marshal),
// so the on-disk form is stable regardless of how a value was entered.
func Normalize(cfg *Config) {
	if len(cfg.BlockedCountries) > 0 {
		seen := make(map[string]bool, len(cfg.BlockedCountries))
		out := make([]string, 0, len(cfg.BlockedCountries))
		for _, c := range cfg.BlockedCountries {
			u := strings.ToUpper(strings.TrimSpace(c))
			if u == "" || seen[u] {
				continue
			}
			seen[u] = true
			out = append(out, u)
		}
		cfg.BlockedCountries = out
	}
	cfg.LogLevel = strings.ToLower(strings.TrimSpace(cfg.LogLevel))
	for i, iface := range cfg.VPN.TunnelInterfaces {
		cfg.VPN.TunnelInterfaces[i] = strings.TrimSpace(iface)
	}
	for i, ep := range cfg.VPN.Endpoints {
		cfg.VPN.Endpoints[i] = strings.TrimSpace(ep)
	}
	for pi := range cfg.VPN.Profiles {
		cfg.VPN.Profiles[pi].Name = strings.TrimSpace(cfg.VPN.Profiles[pi].Name)
		cfg.VPN.Profiles[pi].IfaceHint = strings.TrimSpace(cfg.VPN.Profiles[pi].IfaceHint)
		for ei := range cfg.VPN.Profiles[pi].Endpoints {
			cfg.VPN.Profiles[pi].Endpoints[ei] = strings.TrimSpace(cfg.VPN.Profiles[pi].Endpoints[ei])
		}
	}
	// Autodetect is the recommended default: when the guard is enabled with no
	// explicit tunnel interfaces, discover them at runtime. This keeps a config
	// from pinning a stale utunN across reconnects. Explicit TunnelInterfaces
	// still win. (Strictly compat-safe: a config previously rejected for having
	// neither now validates; one that already had either is unchanged.)
	if cfg.VPN.Enabled && len(cfg.VPN.TunnelInterfaces) == 0 {
		cfg.VPN.Autodetect = true
	}
	// VPN guard cadence defaults. Set unconditionally — these are only read in
	// VPN mode, but defaulting here keeps Validate and the runner simple.
	if cfg.VPN.EndpointRefresh <= 0 {
		cfg.VPN.EndpointRefresh = time.Minute
	}
	if cfg.VPN.EndpointGrace <= 0 {
		cfg.VPN.EndpointGrace = defaultEndpointGrace
	}
	if cfg.VPN.TunnelWatch <= 0 {
		cfg.VPN.TunnelWatch = 1 * time.Second
	}
	if cfg.VPN.SwitchWindow <= 0 {
		cfg.VPN.SwitchWindow = defaultSwitchWindow
	}
	if cfg.VPN.ReconnectWindow == 0 {
		cfg.VPN.ReconnectWindow = defaultReconnectWindow
	}
	normalizeAdvanced(&cfg.VPN.Advanced)
}

// normalizeAdvanced fills each Advanced knob with its recommended default when
// unset, so the rest of the code never special-cases a zero value.
func normalizeAdvanced(a *Advanced) {
	if a.SwitchWindowMax <= 0 {
		a.SwitchWindowMax = defaultSwitchWindowMax
	}
	if a.CommandFreshness <= 0 {
		a.CommandFreshness = defaultCommandFreshness
	}
	if a.WindowDiscoveryInterval <= 0 {
		a.WindowDiscoveryInterval = defaultWindowDiscoveryInterval
	}
	if a.TunnelPruneAfter <= 0 {
		a.TunnelPruneAfter = defaultTunnelPruneAfter
	}
	if a.LearnedEndpointTTL <= 0 {
		a.LearnedEndpointTTL = defaultLearnedEndpointTTL
	}
	if a.LearnedMaxPerProfile <= 0 {
		a.LearnedMaxPerProfile = defaultLearnedMaxPerProfile
	}
	if a.PromoteAfterRefreshes <= 0 {
		a.PromoteAfterRefreshes = defaultPromoteAfterRefreshes
	}
	if a.EndpointWarnThreshold <= 0 {
		a.EndpointWarnThreshold = defaultEndpointWarnThreshold
	}
	if a.ReconnectMinUptime == 0 {
		a.ReconnectMinUptime = defaultReconnectMinUptime
	}
	// Canonicalize protocol strings so validation and pf/nft/WFP rendering agree:
	// the renderers emit these values verbatim, so a stray space or capital (" UDP",
	// "Tcp") would otherwise leak into the ruleset. Normalize runs before Validate.
	for i, p := range a.WindowProtocols {
		a.WindowProtocols[i] = strings.ToLower(strings.TrimSpace(p))
	}
}

// Switch-window / learning defaults. These are the recommended values; the
// vpn.advanced config block overrides any of them.
const (
	defaultSwitchWindow            = 15 * time.Second // 2026-07-19 defaults review: windows exist to be closed fast
	defaultSwitchWindowMax         = 5 * time.Minute
	defaultCommandFreshness        = 30 * time.Second
	defaultWindowDiscoveryInterval = 2 * time.Second
	defaultTunnelPruneAfter        = 60 * time.Second
	defaultLearnedEndpointTTL      = 720 * time.Hour // 30 days
	defaultLearnedMaxPerProfile    = 16
	defaultPromoteAfterRefreshes   = 3
	defaultEndpointWarnThreshold   = 256

	defaultReconnectWindow    = 30 * time.Second
	defaultReconnectMinUptime = 15 * time.Second
	defaultEndpointGrace      = 15 * time.Minute

	minSwitchWindow    = 10 * time.Second // floor for switchWindow / --for
	minReconnectWindow = 5 * time.Second  // floor for the automatic reconnect window
	maxProfileName     = 64

	// Disabled marks a duration the user explicitly set to "0" (feature
	// off). The distinct sentinel survives Normalize, which treats a plain zero
	// as "absent — fill the default".
	Disabled time.Duration = -1
)

// targetKind classifies an allowlist/endpoint entry.
type targetKind int

const (
	kindInvalid targetKind = iota
	kindIP
	kindHost
)

// classifyTarget reports whether s is an IP literal, a plausible DNS hostname,
// or neither. It performs NO DNS resolution — classification stays offline so
// validation is root-free and fast; real resolution happens later in the run
// path (internal/netdetect.EndpointSource).
func classifyTarget(s string) targetKind {
	s = strings.TrimSpace(s)
	if s == "" {
		return kindInvalid
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return kindIP
	}
	if isPlausibleHostname(s) {
		return kindHost
	}
	return kindInvalid
}

// isPlausibleHostname is a light, offline RFC-1123-ish syntax check: dotted
// labels of ASCII letters, digits and hyphens, each label 1..63 chars and not
// hyphen-bounded, total length <= 253. The final (top-level) label must not be
// all-numeric, which rejects truncated or malformed IPs (e.g. "203.0.113") that
// would otherwise masquerade as hostnames and pass validation but never resolve.
func isPlausibleHostname(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	labels := strings.Split(s, ".")
	for _, lab := range labels {
		if l := len(lab); l == 0 || l > 63 {
			return false
		}
		if lab[0] == '-' || lab[len(lab)-1] == '-' {
			return false
		}
		for i := 0; i < len(lab); i++ {
			c := lab[i]
			ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '-'
			if !ok {
				return false
			}
		}
	}
	// A real hostname's top-level label is never all digits; an all-numeric final
	// label means this is a malformed IP, not a host to resolve.
	if last := labels[len(labels)-1]; isAllDigits(last) {
		return false
	}
	return true
}

// isAllDigits reports whether s is non-empty and contains only ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Validate checks invariants the rest of the program relies on.
func (c *Config) Validate() error {
	if c.PollInterval <= 0 {
		return fmt.Errorf("pollInterval must be > 0, got %s", c.PollInterval)
	}
	if c.Hysteresis < 1 {
		return fmt.Errorf("hysteresis must be >= 1, got %d", c.Hysteresis)
	}
	if len(c.Providers) == 0 {
		return errors.New("at least one provider is required")
	}
	for _, code := range c.BlockedCountries {
		if len(code) != 2 {
			return fmt.Errorf("blocked country %q must be a 2-letter ISO-3166 code", code)
		}
	}
	if c.VPN.Enabled {
		// Tunnel interfaces are discovered at runtime when Autodetect is set
		// (Normalize implies it when none are pinned), so there is no
		// "tunnelInterfaces or autodetect" gate any more — an enabled guard always
		// has a way to find its tunnel.
		//
		// At least one endpoint across the UNION of legacy endpoints, profile
		// endpoints, and auto-discovery: a guard with no way to learn any server
		// address can never let the tunnel reconnect. learned.json is deliberately
		// NOT consulted here — validation must be deterministic offline.
		if len(c.VPN.Endpoints) == 0 && !c.VPN.AutoDiscoverEndpoints && !anyProfileHasEndpoint(c.VPN.Profiles) {
			return errors.New("vpn.enabled requires at least one endpoint (vpn.endpoints or vpn.profiles[].endpoints) or vpn.autoDiscoverEndpoints")
		}
		// Endpoints may be IP literals or hostnames; hostnames are resolved at
		// runtime. Reject only entries that are neither.
		for _, ep := range c.VPN.Endpoints {
			if classifyTarget(ep) == kindInvalid {
				return fmt.Errorf("vpn.endpoints: %q is neither an IP address nor a valid hostname", ep)
			}
		}
		if err := validateProfiles(c.VPN.Profiles); err != nil {
			return err
		}
		// Validate the advanced block first: validateSwitchWindow bounds
		// vpn.switchWindow against advanced.switchWindowMax, so an invalid max must
		// surface as its own direct error rather than a confusing derived range.
		if err := validateAdvanced(c.VPN.Advanced); err != nil {
			return err
		}
		if err := validateSwitchWindow(c.VPN); err != nil {
			return err
		}
	}
	return nil
}

func anyProfileHasEndpoint(ps []Profile) bool {
	for _, p := range ps {
		if len(p.Endpoints) > 0 {
			return true
		}
	}
	return false
}

func validateProfiles(ps []Profile) error {
	seen := make(map[string]bool, len(ps))
	for i, p := range ps {
		if p.Name == "" {
			return fmt.Errorf("vpn.profiles[%d]: name is required", i)
		}
		if !isValidProfileName(p.Name) {
			return fmt.Errorf("vpn.profiles[%q]: name must be 1-%d chars of [A-Za-z0-9._-]", p.Name, maxProfileName)
		}
		key := strings.ToLower(p.Name)
		if seen[key] {
			return fmt.Errorf("vpn.profiles: duplicate profile name %q", p.Name)
		}
		seen[key] = true
		if len(p.Endpoints) == 0 {
			return fmt.Errorf("vpn.profiles[%q]: at least one endpoint is required", p.Name)
		}
		for _, ep := range p.Endpoints {
			if classifyTarget(ep) == kindInvalid {
				return fmt.Errorf("vpn.profiles[%q]: endpoint %q is neither an IP address nor a valid hostname", p.Name, ep)
			}
		}
	}
	return nil
}

func isValidProfileName(name string) bool {
	if len(name) == 0 || len(name) > maxProfileName {
		return false
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

func validateSwitchWindow(v VPN) error {
	max := v.Advanced.SwitchWindowMax
	if max <= 0 {
		max = defaultSwitchWindowMax
	}
	if v.SwitchWindow < minSwitchWindow || v.SwitchWindow > max {
		return fmt.Errorf("vpn.switchWindow %s out of range [%s, %s]", v.SwitchWindow, minSwitchWindow, max)
	}
	// ReconnectWindow < 0 is the explicit "disabled" sentinel and always valid.
	if v.ReconnectWindow > 0 && (v.ReconnectWindow < minReconnectWindow || v.ReconnectWindow > max) {
		return fmt.Errorf("vpn.reconnectWindow %s out of range [%s, %s] (or \"0\" to disable)", v.ReconnectWindow, minReconnectWindow, max)
	}
	return nil
}

func validateAdvanced(a Advanced) error {
	if a.SwitchWindowMax > 0 && a.SwitchWindowMax < minSwitchWindow {
		return fmt.Errorf("vpn.advanced.switchWindowMax %s must be >= %s", a.SwitchWindowMax, minSwitchWindow)
	}
	for _, p := range a.WindowProtocols {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "udp", "tcp":
		default:
			return fmt.Errorf("vpn.advanced.windowProtocols: %q must be \"udp\" or \"tcp\"", p)
		}
	}
	for _, port := range a.WindowPorts {
		if port < 1 || port > 65535 {
			return fmt.Errorf("vpn.advanced.windowPorts: %d out of range 1-65535", port)
		}
	}
	return nil
}

// EffectiveEndpoints returns the deduplicated union of the flat vpn.endpoints,
// all profile endpoints, and any extra addresses (e.g. learned entries the
// caller loaded), preserving first-seen order. It is the single source the
// runner, print-rules, doctor, and the setup lockout check all use, so a preview
// can never disagree with what enforcement will open.
func EffectiveEndpoints(cfg *Config, extra []string) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(items []string) {
		for _, e := range items {
			e = strings.TrimSpace(e)
			if e == "" || seen[e] {
				continue
			}
			seen[e] = true
			out = append(out, e)
		}
	}
	add(cfg.VPN.Endpoints)
	for _, p := range cfg.VPN.Profiles {
		add(p.Endpoints)
	}
	add(extra)
	return out
}
