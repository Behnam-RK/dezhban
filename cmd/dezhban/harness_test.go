package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/behnam-rk/dezhban/internal/config"
	"github.com/behnam-rk/dezhban/internal/control"
	"github.com/behnam-rk/dezhban/internal/firewall"
)

// runCLI runs the CLI's run() entry point in-process, capturing stdout and
// stderr separately, and returns them with the exit code. Global flag state
// (-v, --no-sudo, --no-daemon) is reset first so tests stay order-independent
// regardless of what ran before them — run() itself never resets it, since a
// real process only calls it once.
//
// Because it swaps the process-global os.Stdout/os.Stderr, no test in this
// file may call t.Parallel().
//
// Each pipe is drained by its own goroutine STARTED BEFORE run(), never read
// after it returns: a pipe holds only a fixed kernel buffer (64 KiB on Linux,
// smaller on macOS), so a command that prints more than that — `config
// schema`, `completion zsh`, `print-rules` on a large config — would block
// forever inside run() with nothing reading the other end.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	verbose, noSudo, noDaemonFlag = false, false, false

	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	drain := func(r *os.File) <-chan string {
		ch := make(chan string, 1)
		go func() {
			data, _ := io.ReadAll(r)
			_ = r.Close()
			ch <- string(data)
		}()
		return ch
	}
	outCh, errCh := drain(outR), drain(errR)

	os.Stdout, os.Stderr = outW, errW
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()

	code = run(args)

	// Closing the write ends is what ends each ReadAll — do it before
	// receiving, or the drains never see EOF.
	_ = outW.Close()
	_ = errW.Close()
	return <-outCh, <-errCh, code
}

// testConfigPath writes a valid, self-contained config to a temp file and
// returns its path. config.Default()'s own provider/endpoint set is empty, so
// this touches no network by default; a case that needs to exercise a real
// network-bound path (there are none in this file) would have to opt in
// explicitly.
func testConfigPath(t *testing.T, mutate func(*config.Config)) string {
	t.Helper()
	cfg := config.Default()
	if mutate != nil {
		mutate(&cfg)
	}
	p := filepath.Join(t.TempDir(), "dezhban.json")
	if err := config.Save(p, &cfg); err != nil {
		t.Fatal(err)
	}
	return p
}

// The bare dispatch table: no args, help, and an unknown command. These are
// what a user sees before any subcommand-specific flag parsing happens, and
// were entirely uncovered — run() itself was at 0%.
func TestRunDispatchBasics(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantCode   int
		wantStderr string
		wantStdout string
	}{
		{"no args", nil, 2, "Usage:", ""},
		{"help", []string{"help"}, 0, "", "Usage:"},
		{"--help", []string{"--help"}, 0, "", "Usage:"},
		{"-h", []string{"-h"}, 0, "", "Usage:"},
		{"unknown command", []string{"bogus-command"}, 2, `unknown command "bogus-command"`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, code := runCLI(t, c.args...)
			if code != c.wantCode {
				t.Errorf("code = %d, want %d (stdout=%q stderr=%q)", code, c.wantCode, stdout, stderr)
			}
			if c.wantStderr != "" && !strings.Contains(stderr, c.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, c.wantStderr)
			}
			if c.wantStdout != "" && !strings.Contains(stdout, c.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout, c.wantStdout)
			}
		})
	}
}

func TestCmdVersion(t *testing.T) {
	stdout, _, code := runCLI(t, "version")
	if code != 0 {
		t.Fatalf("version exited %d, want 0", code)
	}
	if !strings.Contains(stdout, "dezhban") {
		t.Errorf("version output = %q, want it to mention dezhban", stdout)
	}

	// -v adds the build detail lines; it can appear before the subcommand
	// because stripVerbose scans the whole arg list before dispatch.
	verboseOut, _, code := runCLI(t, "-v", "version")
	if code != 0 {
		t.Fatalf("version -v exited %d, want 0", code)
	}
	if !strings.Contains(verboseOut, "go:") {
		t.Errorf("verbose version output = %q, want a %q line", verboseOut, "go:")
	}
	if len(verboseOut) <= len(stdout) {
		t.Errorf("verbose output (%d bytes) should be longer than plain (%d bytes)", len(verboseOut), len(stdout))
	}
}

func TestCmdValidate(t *testing.T) {
	good := testConfigPath(t, func(c *config.Config) {
		c.BlockedCountries = []string{"IR", "CN"}
	})
	stdout, _, code := runCLI(t, "validate", "--config", good)
	if code != 0 {
		t.Fatalf("validate exited %d, want 0 (stdout=%q)", code, stdout)
	}
	if !strings.Contains(stdout, "config OK") || !strings.Contains(stdout, "IR, CN") {
		t.Errorf("validate output = %q, want it to confirm the config and list the blocked countries", stdout)
	}

	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runCLI(t, "validate", "--config", bad)
	if code != 1 {
		t.Fatalf("validate on a broken file exited %d, want 1", code)
	}
	if !strings.Contains(stderr, "config invalid") {
		t.Errorf("stderr = %q, want it to say the config is invalid", stderr)
	}
}

func TestCmdPrintRules(t *testing.T) {
	cfgPath := testConfigPath(t, nil)

	for _, mode := range []string{"guard", "fullblock", "switch"} {
		t.Run(mode, func(t *testing.T) {
			stdout, stderr, code := runCLI(t, "print-rules", "--config", cfgPath, "--mode", mode)
			if code != 0 {
				t.Fatalf("print-rules --mode %s exited %d, want 0 (stderr=%q)", mode, code, stderr)
			}
			if strings.TrimSpace(stdout) == "" {
				t.Error("print-rules produced no ruleset")
			}
		})
	}

	// docs/adr/0001: `legacy` was deliberately removed, not silently rendered
	// as something else — this pins that it still errors by name.
	_, stderr, code := runCLI(t, "print-rules", "--config", cfgPath, "--mode", "legacy")
	if code == 0 {
		t.Fatal("print-rules --mode legacy succeeded, want a refusal (docs/adr/0001)")
	}
	if !strings.Contains(stderr, "0001") {
		t.Errorf("stderr = %q, want it to point at ADR-0001", stderr)
	}

	_, stderr, code = runCLI(t, "print-rules", "--config", cfgPath, "--mode", "bogus")
	if code == 0 {
		t.Fatal("print-rules --mode bogus succeeded, want a refusal")
	}
	if !strings.Contains(stderr, `"bogus"`) {
		t.Errorf("stderr = %q, want it to name the bad mode", stderr)
	}
}

func TestCmdDetectVPN(t *testing.T) {
	// Host-dependent output (tunnels may or may not be present), but the exit
	// code and the fact that it prints something are not.
	stdout, _, code := runCLI(t, "detect-vpn")
	if code != 0 {
		t.Fatalf("detect-vpn exited %d, want 0", code)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Error("detect-vpn produced no output")
	}
}

// TestCmdMonitorNoProviders exercises monitor's config/provider validation
// without a network call: an unrecognized provider URL matches nothing in
// ProvidersFromURLs, so this fails before any lookup is attempted.
func TestCmdMonitorNoProviders(t *testing.T) {
	cfgPath := testConfigPath(t, func(c *config.Config) {
		c.Providers = []string{"https://provider.invalid/not-a-known-geo-api"}
	})
	_, stderr, code := runCLI(t, "monitor", "--config", cfgPath, "--once")
	if code != 1 {
		t.Fatalf("monitor with no usable providers exited %d, want 1 (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stderr, "no usable geo providers configured") {
		t.Errorf("stderr = %q, want the no-providers refusal", stderr)
	}
}

func TestCmdCompletion(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			stdout, _, code := runCLI(t, "completion", shell)
			if code != 0 {
				t.Fatalf("completion %s exited %d, want 0", shell, code)
			}
			if strings.TrimSpace(stdout) == "" {
				t.Error("completion produced no script")
			}
		})
	}

	_, _, code := runCLI(t, "completion")
	if code != 2 {
		t.Errorf("completion with no shell exited %d, want 2", code)
	}
	_, stderr, code := runCLI(t, "completion", "powershell")
	if code != 2 {
		t.Errorf("completion powershell exited %d, want 2", code)
	}
	if !strings.Contains(stderr, `"powershell"`) {
		t.Errorf("stderr = %q, want it to name the unsupported shell", stderr)
	}
}

func TestCmdConfigShowPathSchema(t *testing.T) {
	cfgPath := testConfigPath(t, func(c *config.Config) {
		c.LogLevel = "debug"
	})

	stdout, _, code := runCLI(t, "config", "path", "--config", cfgPath)
	if code != 0 {
		t.Fatalf("config path exited %d, want 0", code)
	}
	if strings.TrimSpace(stdout) != cfgPath {
		t.Errorf("config path = %q, want %q", strings.TrimSpace(stdout), cfgPath)
	}

	stdout, _, code = runCLI(t, "config", "show", "--config", cfgPath)
	if code != 0 {
		t.Fatalf("config show exited %d, want 0", code)
	}
	var shown map[string]any
	if err := json.Unmarshal([]byte(stdout), &shown); err != nil {
		t.Fatalf("config show did not print valid JSON: %v\n%s", err, stdout)
	}
	if shown["logLevel"] != "debug" {
		t.Errorf("config show logLevel = %v, want %q", shown["logLevel"], "debug")
	}

	for _, args := range [][]string{{"config", "schema"}, {"config", "schema", "--json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, _, code := runCLI(t, args...)
			if code != 0 {
				t.Fatalf("%v exited %d, want 0", args, code)
			}
			if strings.TrimSpace(stdout) == "" {
				t.Error("schema produced no output")
			}
		})
	}
}

// TestBlockPlan covers the pure decision extracted from cmdBlock's default
// branch: no root, no firewall backend, no daemon. AutoDetect is off in every
// case so tunnel resolution never touches this machine's real interfaces —
// the two error cases must fire on config content alone, not on host state.
func TestBlockPlan(t *testing.T) {
	log := newLogger(&config.Config{LogLevel: "error"})

	t.Run("no tunnels configured", func(t *testing.T) {
		cfg := config.Default()
		cfg.VPN.AutoDetect = false
		_, err := blockPlan(&cfg, log, false)
		if err == nil {
			t.Fatal("blockPlan succeeded with no tunnel interfaces configured, want an error")
		}
		if !strings.Contains(err.Error(), "tunnel interfaces") {
			t.Errorf("err = %q, want it to name the missing tunnels", err)
		}
	})

	t.Run("no endpoints resolvable", func(t *testing.T) {
		cfg := config.Default()
		cfg.VPN.AutoDetect = false
		cfg.VPN.TunnelInterfaces = []string{"dzh-test-tun0"} // pinned, never expected to exist
		cfg.VPN.AutoDiscoverEndpoints = false
		_, err := blockPlan(&cfg, log, false)
		if err == nil {
			t.Fatal("blockPlan succeeded with no endpoints configured or discoverable, want an error")
		}
		if !strings.Contains(err.Error(), "endpoint") {
			t.Errorf("err = %q, want it to name the missing endpoint", err)
		}
	})

	baseCfg := func() config.Config {
		cfg := config.Default()
		cfg.VPN.AutoDetect = false
		cfg.VPN.TunnelInterfaces = []string{"dzh-test-tun0"}
		cfg.VPN.Endpoints = []string{"203.0.113.9"}
		return cfg
	}

	t.Run("full block", func(t *testing.T) {
		cfg := baseCfg()
		d, err := blockPlan(&cfg, log, false)
		if err != nil {
			t.Fatalf("blockPlan: %v", err)
		}
		if d.Policy.Mode != firewall.ModeFullBlock {
			t.Errorf("Mode = %v, want ModeFullBlock", d.Policy.Mode)
		}
	})

	t.Run("guard", func(t *testing.T) {
		cfg := baseCfg()
		d, err := blockPlan(&cfg, log, true)
		if err != nil {
			t.Fatalf("blockPlan: %v", err)
		}
		if d.Policy.Mode != firewall.ModeGuard {
			t.Errorf("Mode = %v, want ModeGuard", d.Policy.Mode)
		}
		if len(d.Tunnels) != 1 || d.Tunnels[0] != "dzh-test-tun0" {
			t.Errorf("Tunnels = %v, want [dzh-test-tun0]", d.Tunnels)
		}
	})
}

// TestParseOverrides covers cmdRun's flag-validation step in isolation.
// assembleOptions and runDryRun, cmdRun's other two extracted pieces, are
// deliberately not covered here: both read hardcoded, unconfigurable system
// paths (defaultStatePath, defaultLearnedPath, defaultArmedPath,
// defaultCommandPath, defaultTokenPath) and assembleOptions calls
// state.EnsureDir on the real state directory — calling either from a test
// would touch this machine's real dezhban installation state, which no test
// may do.
func TestParseOverrides(t *testing.T) {
	ov, err := parseOverrides("  ir  ", "")
	if err != nil {
		t.Fatalf("parseOverrides: %v", err)
	}
	if ov.simCountry != "ir" {
		t.Errorf("simCountry = %q, want %q (trimmed, case preserved)", ov.simCountry, "ir")
	}
	if ov.tunnelDownSet {
		t.Error("tunnelDownSet = true with no --simulate-tunnel-down given")
	}

	ov, err = parseOverrides("", "8s")
	if err != nil {
		t.Fatalf("parseOverrides: %v", err)
	}
	if !ov.tunnelDownSet || ov.tunnelDownAfter != 8*time.Second {
		t.Errorf("tunnelDownSet/After = %v/%v, want true/8s", ov.tunnelDownSet, ov.tunnelDownAfter)
	}

	if _, err := parseOverrides("", "not-a-duration"); err == nil {
		t.Fatal("parseOverrides accepted a malformed --simulate-tunnel-down, want an error")
	}
}

// TestCmdDoctorJSON only checks that --json produces valid JSON. The exit
// code and the report's actual findings are host-dependent (real tunnel,
// service, and lockout checks), so this does not assert on either.
func TestCmdDoctorJSON(t *testing.T) {
	cfgPath := testConfigPath(t, nil)
	stdout, stderr, code := runCLI(t, "doctor", "--config", cfgPath, "--json")
	if code != 0 && code != 1 {
		t.Fatalf("doctor --json exited %d, want 0 or 1 (stderr=%q)", code, stderr)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("doctor --json did not print valid JSON: %v\n%s", err, stdout)
	}
}

// TestCmdUpgradeCanActivate only checks the JSON shape and that the exit code
// agrees with "ok" — the verdict itself depends on defaultStatePath(), a real
// unconfigurable system path this test must not assume anything about.
func TestCmdUpgradeCanActivate(t *testing.T) {
	stdout, _, code := runCLI(t, "upgrade", "can-activate", "--json")
	var res struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("upgrade can-activate --json did not print valid JSON: %v\n%s", err, stdout)
	}
	if res.Reason == "" {
		t.Error("upgrade can-activate --json printed no reason")
	}
	wantCode := 1
	if res.OK {
		wantCode = 0
	}
	if code != wantCode {
		t.Errorf("code = %d, want %d for ok=%v", code, wantCode, res.OK)
	}
}

// TestCmdVPNList only asserts on the profile/default lines this test itself
// controls via cfg — it must NOT assert on the "learned" or "active state"
// sections, which read the real (host-dependent, possibly populated on a dev
// machine) /var/db/dezhban files with no config override.
func TestCmdVPNList(t *testing.T) {
	cfgPath := testConfigPath(t, func(c *config.Config) {
		c.VPN.Profiles = []config.Profile{
			{Name: "home", Endpoints: []string{"203.0.113.9"}, TunnelHint: "wg"},
		}
	})
	stdout, _, code := runCLI(t, "vpn", "list", "--config", cfgPath)
	if code != 0 {
		t.Fatalf("vpn list exited %d, want 0", code)
	}
	if !strings.Contains(stdout, "home") || !strings.Contains(stdout, "203.0.113.9") {
		t.Errorf("vpn list = %q, want it to list the configured profile", stdout)
	}

	_, stderr, code := runCLI(t, "vpn")
	if code != 2 {
		t.Errorf("vpn with no subcommand exited %d, want 2", code)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("vpn with no subcommand printed no usage")
	}
}

// TestCmdTokenStatus only checks the exit code (always 0) — the message
// itself depends on real, unconfigurable host state
// (defaultTokenPath/stateDir have no config override), which this test must
// not assume anything about.
func TestCmdTokenStatus(t *testing.T) {
	_, _, code := runCLI(t, "token", "status")
	if code != 0 {
		t.Fatalf("token status exited %d, want 0", code)
	}
}

// controlStatus is fully config-driven (Control.Socket overrides the default
// path), so all three branches are deterministic and hermetic.
func TestControlStatus(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		cfg := config.Default()
		cfg.Control.Enabled = false
		got := controlStatus(&cfg)
		if !strings.Contains(got, "disabled") {
			t.Errorf("controlStatus = %q, want it to say disabled", got)
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		cfg := config.Default()
		cfg.Control.Enabled = true
		cfg.Control.Socket = filepath.Join(t.TempDir(), "no-daemon-here.sock")
		got := controlStatus(&cfg)
		if !strings.Contains(got, "unreachable") {
			t.Errorf("controlStatus = %q, want it to say unreachable", got)
		}
	})

	t.Run("reachable", func(t *testing.T) {
		// A short dir, not t.TempDir(): that path nests under a per-test name
		// long enough to overrun the platform's unix socket sun_path limit.
		dir, err := os.MkdirTemp("", "dzh")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		sockPath := filepath.Join(dir, "c.sock")
		srv, err := control.New(sockPath, "", newLogger(&config.Config{LogLevel: "error"}))
		if err != nil {
			t.Fatalf("control.New: %v", err)
		}
		srv.Start(t.Context())
		defer srv.Stop()
		go func() {
			for req := range srv.Requests() {
				req.Reply <- control.Response{OK: true}
			}
		}()

		cfg := config.Default()
		cfg.Control.Enabled = true
		cfg.Control.Socket = sockPath
		cfg.Control.Group = "admin"
		got := controlStatus(&cfg)
		if !strings.Contains(got, "reachable") || !strings.Contains(got, "admin") {
			t.Errorf("controlStatus = %q, want it to say reachable and name the group", got)
		}
	})
}
