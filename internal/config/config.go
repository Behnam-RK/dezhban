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

// VPN configures the interface-aware kill-switch guard. Enforcement always uses
// the tunnel interface(s) and VPN endpoint(s); the destination-IP allowlist model
// it replaced is meaningless under a tunnel, where the firewall sees only
// encrypted outer packets to one address.
//
// There is no longer an Enabled flag. It used to select between two enforcement
// models AND double as the safety opt-in that stopped a misconfigured guard from
// locking a host out. The first job is gone (see docs/adr/0001); the second is
// now done properly by the STANDBY posture, which simply installs no rules until
// a tunnel has actually been observed (docs/adr/0002).
type VPN struct {
	// TunnelInterfaces are the VPN tunnel interface names (e.g. "utun4"). For now
	// these are concrete names; autodetect/pattern expansion lands with netdetect.
	TunnelInterfaces []string
	// Endpoints are the VPN server addresses reachable on the physical interface,
	// kept open so the tunnel can stay up and redial. Each entry may be an IP
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
	// server hostname and redial while the tunnel is down. ON by default
	// (2026-07-19 defaults review: redialability beats hiding DNS-query
	// metadata for this project's users); set false to close the metadata leak.
	AllowPhysicalDNS bool
	// AllowLocalNetwork passes traffic to private, link-local and multicast
	// destinations in every enforcing posture, so printers, NAS, the router's
	// admin page, AirPlay/Chromecast and local dev servers keep working while
	// the guard is armed. ON by default.
	//
	// This costs nothing against dezhban's threat model. The guard exists to stop
	// a standing direct connection exposing a sanctioned-country IP to a FOREIGN
	// service; RFC1918/ULA/link-local traffic never leaves the building, so it
	// cannot carry that exposure. It is destination-scoped, not interface-scoped,
	// so it can never become an internet path — packets to public addresses stay
	// blocked whatever the next hop is.
	//
	// The one real cost, and the reason the UI must say it plainly rather than
	// bury it: on an untrusted network (a café, a hotel) this lets you reach —
	// and be reached by — the other devices on that network.
	AllowLocalNetwork bool
	// AutoArm starts the daemon PASSIVE (posture "standby", no enforcement)
	// when no tunnel interface is present, arming the guard automatically the
	// moment a VPN connects. Never disarms on tunnel loss — a drop is exactly
	// the leak the kill switch exists for; only an explicit unblock with the
	// tunnel down returns to standby. ON by default (2026-07-19 defaults
	// review: a guard armed with no VPN cuts everything, and that mystery
	// blackout hurt new users more than the pre-first-connect gap); set false
	// for the stricter armed-from-startup posture.
	AutoArm bool
	// ArmAtBoot arms the guard directly at startup even when AutoArm's live
	// probe finds no tunnel interface present yet — provided a tunnel has been
	// observed up at least once on this host (internal/armed) and an endpoint
	// is known. Without it, a normal boot (this daemon typically starts before
	// the VPN client brings its interface up) lands in AutoArm's standby and
	// opens the network until the VPN connects; with it, the network stays
	// blocked across the reboot instead. ON by default (2026-07-22): this only
	// changes behavior on a host that has already proven its VPN works, so a
	// fresh install still cannot lock itself out — see
	// docs/adr/0008-arm-at-boot.md.
	ArmAtBoot bool
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
	// learned. Defaults to 5s; an explicit "0" disables manual switch windows
	// entirely (kept internally as a negative sentinel so Normalize can tell
	// "disabled" from "absent", exactly like RedialWindow); validated to
	// (0, Advanced.SwitchWindowMax] otherwise — no floor.
	//
	// Disabling is a TIGHTENING: the switch window is the only sanctioned
	// relaxation of the guard, so turning it off leaves nothing that can relax it.
	// The cost is that a brand-new VPN's server must be added to config by hand,
	// since there is no longer a window in which its handshake could be observed.
	// Independent of RedialWindow — disabling one never disables the other.
	SwitchWindow time.Duration
	// RedialWindow is the duration of the AUTOMATIC redial window: when
	// the tunnel drops while the guard is healthy (GUARD posture, not standby,
	// not FULL BLOCK), the daemon opens a switch-window relaxation for this long
	// so the VPN client can redial any server — including one dezhban has never
	// seen (rotating-pool and 443-fronted VPNs pick fresh IPs on every connect).
	// It closes early the moment a good exit is confirmed, learns the new
	// endpoint, and on expiry reverts fail-closed. Defaults to 30s; an explicit
	// "0" disables the automatic window (kept internally as a negative sentinel
	// so Normalize can tell "disabled" from "absent"); validated to
	// (0, Advanced.RedialWindowMax] otherwise — no floor.
	RedialWindow time.Duration
	// PauseMax caps an operator-requested bounded pause (`dezhban pause` / the
	// GUI's "Pause protection"): a deliberate, timed drop to the real ISP IP,
	// e.g. to reach a sanctioned-country-only service the VPN's exit can't
	// reach. It is a THIRD relaxation of the guard alongside the switch window
	// and the automatic redial window — sharing their bounded-timer
	// machinery, but with its own cap, never shared with SwitchWindowMax or
	// RedialWindowMax (collapsing caps silently truncates whichever
	// trigger has the larger budget). Defaults to 30m; an explicit "0"
	// disables pausing entirely (kept internally as a negative sentinel so
	// Normalize can tell "disabled" from "absent", exactly like SwitchWindow /
	// RedialWindow). Unlike those two, PauseMax is the cap itself — the
	// requested duration comes from the CLI/GUI call, not a config default.
	PauseMax time.Duration
	// Advanced holds tunables for behaviors that are otherwise baked-in design
	// decisions. Every field defaults in Normalize; an absent `advanced` block
	// keeps the recommended defaults. Touch only if you know why.
	Advanced Advanced
}

// Retired names a config key that no longer does anything, so the loader can
// report it instead of ignoring it silently. A setting that is accepted and
// discarded without a word is the worst failure mode a security tool has.
type Retired struct {
	Key    string
	Reason string
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
	// redials, so the hint is advisory / display-only.
	IfaceHint string
}

// Advanced holds tunables for VPN guard / switch-window / endpoint-learning
// behaviors. These are design-decision constants surfaced as knobs; the defaults
// (applied in Normalize) are the recommended values.
type Advanced struct {
	// SwitchWindowMax caps a MANUAL switch window (an explicit `switch` command
	// or a `--for` override), anchored to the window's first open. Default 3m.
	// No floor — any positive value up to this cap is accepted.
	SwitchWindowMax time.Duration
	// RedialWindowMax caps the AUTOMATIC redial window (VPN.RedialWindow),
	// anchored the same way. Kept separate from SwitchWindowMax because the two
	// triggers have different exposure budgets: a longer automatic window lets a
	// slow VPN client finish redialing without the operator having to intervene.
	// Never let the two share one cap — that would silently truncate whichever
	// trigger has the larger intended budget. Default 10m. No floor.
	RedialWindowMax time.Duration
	// CommandFreshness is how recent a control-file command must be to be acted
	// on (replay/stale-file guard). Default 30s.
	CommandFreshness time.Duration
	// WindowDiscoveryInterval is how often endpoint discovery runs while a switch
	// window is open (fast, to learn the new server quickly). Default 1s — fast
	// enough that even the 5s default switchWindow gets several discovery ticks.
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
	// RedialMinUptime is the anti-flap gate on the automatic redial
	// window: an auto-window opens only if the tunnel had been up at least this
	// long, or a non-blocked exit was confirmed during that uptime. Without it a
	// VPN flapping up/down would chain windows and turn the guard into a sieve.
	// Default 15s; an explicit "0" disables the gate (negative sentinel
	// internally, same convention as VPN.RedialWindow).
	RedialMinUptime time.Duration
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
	// Hysteresis is the consecutive agreeing readings required before toggling.
	Hysteresis int
	// Providers are geo-location endpoint URLs, tried for redundancy.
	Providers []string
	// ProviderQuorum requires a majority of providers to agree on the country.
	ProviderQuorum bool
	// LogLevel is one of debug, info, warn, error.
	LogLevel string
	// VPN configures the interface-aware guard for full-tunnel VPN hosts.
	VPN VPN
	// Control configures the daemon's live control socket (passwordless routine ops).
	Control Control
	// Retired lists keys present in the file that no longer do anything. The
	// config still loads — the keys are simply inert — but callers report them so
	// an operator is never left believing a discarded setting took effect.
	Retired []Retired
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
	// AllowPauseOps permits opening/ending a pause over the socket. Default
	// true — independently gated from AllowSwitchOps because pause is a
	// separate relaxation (see VPN.PauseMax): set it false to force pause ops
	// back to the root-owned command file (`sudo dezhban pause`) without
	// touching switch-window availability.
	AllowPauseOps bool
	// AllowConfigOps permits writing configuration over the socket. Default
	// true. Unlike the two above, this op is additionally gated on the enrolled
	// control token (internal/token), so the socket's group membership alone
	// never authorises it; this flag exists for operators who want config
	// changes to require real root regardless of enrollment.
	AllowConfigOps bool
}

// fileConfig is the on-disk JSON shape. Durations are strings (e.g. "30s")
// because JSON has no native duration type. Pointer fields distinguish
// "absent" (keep default) from a zero value the user set deliberately.
type fileConfig struct {
	PollInterval     string   `json:"pollInterval"`
	BlockedCountries []string `json:"blockedCountries"`
	// FailClosed and Allowlist are retired (docs/adr/0006; the fallback model
	// they belonged to is gone, docs/adr/0001). Kept here, DETECTION-ONLY, so
	// apply() can report a config that still sets them via cfg.Retired instead
	// of silently accepting and discarding a security-relevant key. Never read
	// into the runtime Config and never written back by Save.
	FailClosed     *bool        `json:"failClosed,omitempty"`
	Hysteresis     *int         `json:"hysteresis"`
	Providers      []string     `json:"providers"`
	Allowlist      *Allowlist   `json:"allowlist,omitempty"`
	ProviderQuorum *bool        `json:"providerQuorum"`
	LogLevel       string       `json:"logLevel"`
	VPN            *fileVPN     `json:"vpn"`
	Control        *fileControl `json:"control,omitempty"`
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
	AllowPauseOps  *bool   `json:"allowPauseOps,omitempty"`
	AllowConfigOps *bool   `json:"allowConfigOps,omitempty"`
}

// fileVPN is the on-disk shape of the VPN block. A pointer in fileConfig lets an
// absent block keep defaults.
type fileVPN struct {
	// Enabled is RETIRED. It is kept here only so a pre-merge config can be
	// recognised and reported, never written: a pointer with omitempty means an
	// absent key stays absent on save, and toFile never sets it. See
	// docs/adr/0001-single-guard-mode.md.
	Enabled          *bool    `json:"enabled,omitempty"`
	TunnelInterfaces []string `json:"tunnelInterfaces"`
	Endpoints        []string `json:"endpoints"`
	// Pointers: all default to TRUE, so an explicit false must be
	// distinguishable from an absent key (same convention as fileControl).
	Autodetect            *bool         `json:"autodetect,omitempty"`
	AutoDiscoverEndpoints *bool         `json:"autoDiscoverEndpoints,omitempty"`
	AllowPhysicalDNS      *bool         `json:"allowPhysicalDNS,omitempty"`
	AllowLocalNetwork     *bool         `json:"allowLocalNetwork,omitempty"`
	AutoArm               *bool         `json:"autoArm,omitempty"`
	ArmAtBoot             *bool         `json:"armAtBoot,omitempty"`
	EndpointRefresh       string        `json:"endpointRefresh"`
	EndpointGrace         string        `json:"endpointGrace,omitempty"`
	TunnelWatch           string        `json:"tunnelWatch"`
	Profiles              []fileProfile `json:"profiles,omitempty"`
	SwitchWindow          string        `json:"switchWindow,omitempty"`
	RedialWindow          string        `json:"redialWindow,omitempty"`
	PauseMax              string        `json:"pauseMax,omitempty"`
	Advanced              *fileAdvanced `json:"advanced,omitempty"`
}

type fileProfile struct {
	Name      string   `json:"name"`
	Endpoints []string `json:"endpoints"`
	IfaceHint string   `json:"ifaceHint,omitempty"`
}

type fileAdvanced struct {
	SwitchWindowMax         string   `json:"switchWindowMax,omitempty"`
	RedialWindowMax         string   `json:"redialWindowMax,omitempty"`
	CommandFreshness        string   `json:"commandFreshness,omitempty"`
	WindowDiscoveryInterval string   `json:"windowDiscoveryInterval,omitempty"`
	TunnelPruneAfter        string   `json:"tunnelPruneAfter,omitempty"`
	LearnedEndpointTTL      string   `json:"learnedEndpointTTL,omitempty"`
	LearnedMaxPerProfile    int      `json:"learnedMaxPerProfile,omitempty"`
	PromoteAfterRefreshes   int      `json:"promoteAfterRefreshes,omitempty"`
	EndpointWarnThreshold   int      `json:"endpointWarnThreshold,omitempty"`
	WindowProtocols         []string `json:"windowProtocols,omitempty"`
	WindowPorts             []int    `json:"windowPorts,omitempty"`
	RedialMinUptime         string   `json:"redialMinUptime,omitempty"`
}

// Default returns a Config with safe, security-first defaults.
func Default() Config {
	return Config{
		// 15s poll × hysteresis 2 confirms a forbidden exit in ~30s worst-case
		// (2026-07-19 defaults review) — the default provider order keeps that
		// volume on unmetered endpoints.
		PollInterval:     15 * time.Second,
		BlockedCountries: nil,
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
		ProviderQuorum: false,
		LogLevel:       "info",
		// Mirrors the absent-vpn-block defaults in apply(): all on (2026-07-19
		// defaults review; autodetect/auto-discover added 2026-07-22; armAtBoot
		// added 2026-07-22). Keep the two in sync.
		VPN: VPN{
			Autodetect:            true,
			AutoDiscoverEndpoints: true,
			AllowPhysicalDNS:      true,
			AllowLocalNetwork:     true,
			AutoArm:               true,
			ArmAtBoot:             true,
		},
		Control: Control{
			Enabled: true,
			// Socket empty → resolved against the daemon's state dir by main.
			Group:          defaultControlGroup,
			AllowSwitchOps: true,
			AllowPauseOps:  true,
			AllowConfigOps: true,
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
			// Anything the schema does not recognise is recorded rather than
			// ignored — see unknown.go for why silence is the wrong default here.
			for _, key := range unknownKeys(data) {
				cfg.Retired = append(cfg.Retired, Retired{Key: key, Reason: describeUnknown(key)})
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
		cfg.Retired = append(cfg.Retired, Retired{
			Key:    "failClosed",
			Reason: "belonged to the retired country-blocklist model; the guard's standing rules are the fail-closed block now (docs/adr/0001, docs/adr/0006)",
		})
	}
	if fc.Hysteresis != nil {
		cfg.Hysteresis = *fc.Hysteresis
	}
	if fc.Providers != nil {
		cfg.Providers = fc.Providers
	}
	if fc.Allowlist != nil {
		cfg.Retired = append(cfg.Retired, Retired{
			Key:    "allowlist",
			Reason: "belonged to the retired country-blocklist model; a VPN posture opens the tunnel endpoint, not a physical destination allowlist (docs/adr/0001)",
		})
	}
	if fc.ProviderQuorum != nil {
		cfg.ProviderQuorum = *fc.ProviderQuorum
	}
	if fc.LogLevel != "" {
		cfg.LogLevel = fc.LogLevel
	}
	if fc.VPN != nil {
		v := VPN{
			TunnelInterfaces:      fc.VPN.TunnelInterfaces,
			Endpoints:             fc.VPN.Endpoints,
			Autodetect:            true, // default on; explicit false below
			AutoDiscoverEndpoints: true, // default on; explicit false below
			AllowPhysicalDNS:      true, // default on; explicit false below
			AllowLocalNetwork:     true, // default on; explicit false below
			AutoArm:               true, // default on; explicit false below
			ArmAtBoot:             true, // default on; explicit false below
		}
		if fc.VPN.Autodetect != nil {
			v.Autodetect = *fc.VPN.Autodetect
		}
		if fc.VPN.AutoDiscoverEndpoints != nil {
			v.AutoDiscoverEndpoints = *fc.VPN.AutoDiscoverEndpoints
		}
		if fc.VPN.AllowPhysicalDNS != nil {
			v.AllowPhysicalDNS = *fc.VPN.AllowPhysicalDNS
		}
		if fc.VPN.AllowLocalNetwork != nil {
			v.AllowLocalNetwork = *fc.VPN.AllowLocalNetwork
		}
		if fc.VPN.AutoArm != nil {
			v.AutoArm = *fc.VPN.AutoArm
		}
		if fc.VPN.ArmAtBoot != nil {
			v.ArmAtBoot = *fc.VPN.ArmAtBoot
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
			if d < 0 {
				return fmt.Errorf("vpn.switchWindow: must not be negative (got %s); use \"0\" to disable", d)
			}
			if d == 0 {
				// Same explicit-opt-out sentinel as redialWindow. Without it
				// Normalize would coerce 0 back to the default and silently ignore
				// the operator asking for a strictly zero-leak posture — the worst
				// kind of bug in a security tool: a setting that is accepted,
				// discarded, and never reported.
				v.SwitchWindow = Disabled
			} else {
				v.SwitchWindow = d
			}
		}
		if fc.VPN.RedialWindow != "" {
			d, err := time.ParseDuration(fc.VPN.RedialWindow)
			if err != nil {
				return fmt.Errorf("vpn.redialWindow: %w", err)
			}
			if d < 0 {
				return fmt.Errorf("vpn.redialWindow: must not be negative (got %s); use \"0\" to disable", d)
			}
			if d == 0 {
				v.RedialWindow = Disabled // explicit opt-out, survives Normalize
			} else {
				v.RedialWindow = d
			}
		}
		if fc.VPN.PauseMax != "" {
			d, err := time.ParseDuration(fc.VPN.PauseMax)
			if err != nil {
				return fmt.Errorf("vpn.pauseMax: %w", err)
			}
			if d < 0 {
				return fmt.Errorf("vpn.pauseMax: must not be negative (got %s); use \"0\" to disable", d)
			}
			if d == 0 {
				v.PauseMax = Disabled // explicit opt-out, survives Normalize
			} else {
				v.PauseMax = d
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
		if fc.VPN.Enabled != nil {
			cfg.Retired = append(cfg.Retired, Retired{
				Key:    "vpn.enabled",
				Reason: "dezhban now has a single guard state machine; with no tunnel it rests in standby rather than enforcing (docs/adr/0001, 0002)",
			})
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
		if fc.Control.AllowConfigOps != nil {
			cfg.Control.AllowConfigOps = *fc.Control.AllowConfigOps
		}
		if fc.Control.AllowPauseOps != nil {
			cfg.Control.AllowPauseOps = *fc.Control.AllowPauseOps
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
	if err := parse("redialWindowMax", fa.RedialWindowMax, &a.RedialWindowMax); err != nil {
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
	if fa.RedialMinUptime != "" {
		d, err := time.ParseDuration(fa.RedialMinUptime)
		if err != nil {
			return a, fmt.Errorf("vpn.advanced.redialMinUptime: %w", err)
		}
		if d < 0 {
			return a, fmt.Errorf("vpn.advanced.redialMinUptime: must not be negative (got %s); use \"0\" to disable", d)
		}
		if d == 0 {
			a.RedialMinUptime = Disabled // explicit opt-out of the anti-flap gate
		} else {
			a.RedialMinUptime = d
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
	hysteresis := c.Hysteresis
	quorum := c.ProviderQuorum
	autodetect := c.VPN.Autodetect
	autoDiscover := c.VPN.AutoDiscoverEndpoints
	physDNS := c.VPN.AllowPhysicalDNS
	localNet := c.VPN.AllowLocalNetwork
	autoArm := c.VPN.AutoArm
	armAtBoot := c.VPN.ArmAtBoot
	ctlEnabled := c.Control.Enabled
	ctlGroup := c.Control.Group
	ctlSwitchOps := c.Control.AllowSwitchOps
	ctlPauseOps := c.Control.AllowPauseOps
	ctlConfigOps := c.Control.AllowConfigOps
	return fileConfig{
		PollInterval: c.PollInterval.String(),
		// FailClosed and Allowlist are deliberately omitted (nil): they are
		// retired, detection-only on read, and must never be written back by
		// Save even if the loaded file still had them (apply() already reported
		// them into cfg.Retired; that is the only trace they leave).
		BlockedCountries: c.BlockedCountries,
		Hysteresis:       &hysteresis,
		Providers:        c.Providers,
		ProviderQuorum:   &quorum,
		LogLevel:         c.LogLevel,
		VPN: &fileVPN{
			TunnelInterfaces:      c.VPN.TunnelInterfaces,
			Endpoints:             c.VPN.Endpoints,
			Autodetect:            &autodetect,
			AutoDiscoverEndpoints: &autoDiscover,
			AllowPhysicalDNS:      &physDNS,
			AllowLocalNetwork:     &localNet,
			AutoArm:               &autoArm,
			ArmAtBoot:             &armAtBoot,
			EndpointRefresh:       c.VPN.EndpointRefresh.String(),
			EndpointGrace:         durString(c.VPN.EndpointGrace),
			TunnelWatch:           c.VPN.TunnelWatch.String(),
			Profiles:              toFileProfiles(c.VPN.Profiles),
			SwitchWindow:          optDurString(c.VPN.SwitchWindow),
			RedialWindow:          optDurString(c.VPN.RedialWindow),
			PauseMax:              optDurString(c.VPN.PauseMax),
			Advanced:              toFileAdvanced(c.VPN.Advanced),
		},
		Control: &fileControl{
			Enabled:        &ctlEnabled,
			Socket:         c.Control.Socket,
			Group:          &ctlGroup,
			AllowSwitchOps: &ctlSwitchOps,
			AllowPauseOps:  &ctlPauseOps,
			AllowConfigOps: &ctlConfigOps,
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
	if a.RedialWindowMax != defaultRedialWindowMax {
		fa.RedialWindowMax = durString(a.RedialWindowMax)
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
	if a.RedialMinUptime != defaultRedialMinUptime {
		fa.RedialMinUptime = optDurString(a.RedialMinUptime)
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
//
// The write is atomic — staged in the same directory and renamed — so an
// interrupted save leaves either the old config or the new one, never a
// truncated file. That matters more than it looks: this config is what arms the
// guard at boot, so a half-written file would not merely lose a setting, it
// would leave the host unprotected on the next start. Same convention as the
// daemon's other on-disk records (internal/learned, internal/armed).
func Save(path string, c *Config) error {
	data, err := Marshal(c)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create config dir %q: %w", dir, err)
		}
	}
	tmp, err := os.CreateTemp(dir, ".dezhban-config-*")
	if err != nil {
		return fmt.Errorf("stage config %q: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds

	// CreateTemp makes 0600; the published file must stay readable by the
	// unprivileged tools that inspect it.
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write config %q: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write config %q: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config %q: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install config %q: %w", path, err)
	}
	return nil
}

// Normalize canonicalizes values in place: upper-case + trimmed + de-duplicated
// country codes, lower-case log level, trimmed tunnel/endpoint entries, and the
// VPN cadence defaults. It runs on both Load and every write path (via Marshal),
// so the on-disk form is stable regardless of how a value was entered.
func Normalize(cfg *Config) {
	switch {
	case cfg.BlockedCountries == nil:
		// Absent key (not merely empty) → recommended default (2026-07-22
		// defaults review). An explicit "blockedCountries": [] is a deliberate
		// choice to block nothing and must stay that way — see the len>0 branch
		// below, which never fires for a genuinely empty, non-nil slice.
		cfg.BlockedCountries = []string{"IR", "RU", "KP"}
	case len(cfg.BlockedCountries) > 0:
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
	if cfg.VPN.SwitchWindow == 0 {
		cfg.VPN.SwitchWindow = defaultSwitchWindow
	}
	if cfg.VPN.RedialWindow == 0 {
		cfg.VPN.RedialWindow = defaultRedialWindow
	}
	if cfg.VPN.PauseMax == 0 {
		cfg.VPN.PauseMax = defaultPauseMax
	}
	normalizeAdvanced(&cfg.VPN.Advanced)
}

// normalizeAdvanced fills each Advanced knob with its recommended default when
// unset, so the rest of the code never special-cases a zero value.
func normalizeAdvanced(a *Advanced) {
	if a.SwitchWindowMax <= 0 {
		a.SwitchWindowMax = defaultSwitchWindowMax
	}
	if a.RedialWindowMax <= 0 {
		a.RedialWindowMax = defaultRedialWindowMax
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
	if a.RedialMinUptime == 0 {
		a.RedialMinUptime = defaultRedialMinUptime
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
	defaultSwitchWindow            = 5 * time.Second // 2026-07-22 defaults review: windows exist to be closed fast
	defaultSwitchWindowMax         = 3 * time.Minute
	defaultCommandFreshness        = 30 * time.Second
	defaultWindowDiscoveryInterval = 1 * time.Second // fast enough for a 5s window to get several discovery ticks
	defaultTunnelPruneAfter        = 60 * time.Second
	defaultLearnedEndpointTTL      = 720 * time.Hour // 30 days
	defaultLearnedMaxPerProfile    = 16
	defaultPromoteAfterRefreshes   = 3
	defaultEndpointWarnThreshold   = 256

	defaultRedialWindow    = 30 * time.Second
	defaultRedialWindowMax = 10 * time.Minute
	defaultRedialMinUptime = 15 * time.Second
	defaultPauseMax        = 30 * time.Minute
	defaultEndpointGrace   = 15 * time.Minute

	maxProfileName = 64

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
	{
		// Tunnel interfaces are discovered at runtime when Autodetect is set
		// (Normalize implies it when none are pinned), so there is no
		// "tunnelInterfaces or autodetect" gate any more — an enabled guard always
		// has a way to find its tunnel.
		//
		// A config with no endpoints is VALID and rests in STANDBY. It used to be a
		// load-time error, because `vpn.enabled: true` was a promise to enforce and
		// a guard that can never learn a server address can never let the tunnel
		// redial. With the mode flag gone, every config is a guard config, so
		// rejecting here would make a fresh install — which legitimately knows no
		// endpoints yet — fail to load at all.
		//
		// The protection did not disappear, it moved to where it can tell the
		// difference: the runner refuses to ARM a guard that has tunnels but no
		// endpoints (that specific pair is the unrecoverable blackout), and `doctor`
		// reports the same condition as a lockout risk before you hit it. Knowing
		// no endpoints AND no tunnel is simply standby, which is safe.
		//
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
	// SwitchWindow < 0 is the explicit "disabled" sentinel and always valid: it
	// removes the only sanctioned relaxation of the guard, which is a tightening.
	// No floor: any positive duration up to the cap is accepted.
	if v.SwitchWindow > 0 && v.SwitchWindow > max {
		return fmt.Errorf("vpn.switchWindow %s exceeds vpn.advanced.switchWindowMax %s (or \"0\" to disable)", v.SwitchWindow, max)
	}

	rmax := v.Advanced.RedialWindowMax
	if rmax <= 0 {
		rmax = defaultRedialWindowMax
	}
	// RedialWindow < 0 is the explicit "disabled" sentinel and always valid.
	// Capped separately from the manual window — see Advanced.RedialWindowMax.
	if v.RedialWindow > 0 && v.RedialWindow > rmax {
		return fmt.Errorf("vpn.redialWindow %s exceeds vpn.advanced.redialWindowMax %s (or \"0\" to disable)", v.RedialWindow, rmax)
	}
	return nil
}

func validateAdvanced(a Advanced) error {
	// No floor on switchWindowMax/redialWindowMax (2026-07-22 defaults
	// review): any positive value is accepted; Normalize already fills a
	// non-positive value with its default before Validate ever runs.
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
