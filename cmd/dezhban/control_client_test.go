package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/behnam-rk/dezhban/internal/config"
	"github.com/behnam-rk/dezhban/internal/control"
)

// writeCfg writes a config JSON pointing control at sock and returns its path.
func writeCfg(t *testing.T, sock string, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "dezhban.json")
	if body == "" {
		body = `{"control":{"socket":"` + sock + `","group":""}}`
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// stubDaemon serves the control socket with a canned response, standing in for a
// running daemon. It returns the socket path and the ops it saw.
func stubDaemon(t *testing.T, resp control.Response) (string, *[]control.Op) {
	t.Helper()
	dir, err := os.MkdirTemp("", "dzc")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "c.sock")

	srv, err := control.New(sock, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("control.New: %v", err)
	}
	ctx := t.Context()
	srv.Start(ctx)
	t.Cleanup(srv.Wait)

	seen := &[]control.Op{}
	go func() {
		for {
			select {
			case cr := <-srv.Requests():
				*seen = append(*seen, cr.Req.Op)
				cr.Reply <- resp
			case <-ctx.Done():
				return
			}
		}
	}()
	return sock, seen
}

// The whole point of the feature: with a daemon listening, block/unblock/switch
// are handled over the socket and never reach the root path.
func TestTryControlUsesDaemon(t *testing.T) {
	sock, seen := stubDaemon(t, control.Response{OK: true, Posture: "full-block", Blocked: true})
	cfgPath := writeCfg(t, sock, "")

	code, handled := tryControl(cfgPath, control.Request{Op: control.OpBlock})
	if !handled || code != 0 {
		t.Fatalf("tryControl = (%d, %v), want (0, true) — the daemon should have handled it", code, handled)
	}
	if len(*seen) != 1 || (*seen)[0] != control.OpBlock {
		t.Fatalf("daemon saw %v, want one block op", *seen)
	}
}

// The normal invocation is `dezhban block` with no --config, so tryControl must
// resolve the config the same way every other command does ($DEZHBAN_CONFIG, then
// the system path). Loading the raw empty flag would silently read built-in
// defaults, miss a customized control.socket, and drop back to a password prompt.
func TestTryControlResolvesConfigWithoutFlag(t *testing.T) {
	sock, seen := stubDaemon(t, control.Response{OK: true})
	cfgPath := writeCfg(t, sock, "")
	t.Setenv("DEZHBAN_CONFIG", cfgPath)

	code, handled := tryControl("", control.Request{Op: control.OpBlock})
	if !handled || code != 0 {
		t.Fatalf("tryControl = (%d, %v) with no --config; the resolved config's socket was ignored", code, handled)
	}
	if len(*seen) != 1 {
		t.Fatalf("daemon saw %v, want the op to have reached the socket named by $DEZHBAN_CONFIG", *seen)
	}
}

// A refusal is an answer: the CLI must report it, NOT retry as root. Otherwise the
// daemon's gating (open switch window, allowSwitchOps=false) would be bypassable by
// anyone who can type sudo.
func TestTryControlRefusalIsNotRetriedAsRoot(t *testing.T) {
	sock, _ := stubDaemon(t, control.Response{OK: false, Error: "switch ops are disabled"})
	cfgPath := writeCfg(t, sock, "")

	code, handled := tryControl(cfgPath, control.Request{Op: control.OpOpenSwitch})
	if !handled {
		t.Fatal("a deliberate refusal was reported as unhandled; the caller would escalate to root and bypass the daemon's gating")
	}
	if code == 0 {
		t.Fatal("a refusal exited 0")
	}
}

// With no daemon listening, the CLI must fall back rather than fail — the tool has
// to work with the service stopped.
func TestTryControlFallsBackWhenNoDaemon(t *testing.T) {
	cfgPath := writeCfg(t, filepath.Join(t.TempDir(), "absent.sock"), "")

	if _, handled := tryControl(cfgPath, control.Request{Op: control.OpBlock}); handled {
		t.Fatal("tryControl claimed to handle the op with no daemon listening")
	}
}

// control.enabled=false must not even try the socket.
func TestTryControlSkippedWhenDisabled(t *testing.T) {
	sock, seen := stubDaemon(t, control.Response{OK: true})
	cfgPath := writeCfg(t, sock, `{"control":{"enabled":false,"socket":"`+sock+`","group":""}}`)

	if _, handled := tryControl(cfgPath, control.Request{Op: control.OpBlock}); handled {
		t.Fatal("the socket was used despite control.enabled=false")
	}
	if len(*seen) != 0 {
		t.Fatalf("daemon saw %v with control disabled", *seen)
	}
}

// --no-daemon / DEZHBAN_NO_DAEMON is the escape hatch for a wedged daemon. It is a
// GLOBAL flag like --no-sudo: it must work on either side of the subcommand, and
// must never reach a per-command FlagSet (none of them define it).
func TestNoDaemonFlagAndEnv(t *testing.T) {
	t.Cleanup(func() { noDaemonFlag = false })

	for _, args := range [][]string{
		{"block", "--no-daemon"},
		{"--no-daemon", "block"}, // before the subcommand, like `dezhban --no-sudo block`
	} {
		noDaemonFlag = false
		rest := stripVerbose(args)
		if !noDaemon() {
			t.Errorf("--no-daemon not detected in %v", args)
		}
		if strings.Join(rest, " ") != "block" {
			t.Errorf("stripVerbose(%v) = %v; the flag must not reach the command's FlagSet", args, rest)
		}
	}

	noDaemonFlag = false
	if noDaemon() {
		t.Error("--no-daemon reported when absent")
	}
	t.Setenv("DEZHBAN_NO_DAEMON", "1")
	if !noDaemon() {
		t.Error("DEZHBAN_NO_DAEMON=1 not honored")
	}
}

// The socket defaults into the daemon's own state dir, so one root-owned directory
// holds everything the daemon owns (and uninstall purges it in one rm).
func TestControlSocketPathDefaultsToStateDir(t *testing.T) {
	cfg := config.Default()
	if got, want := controlSocketPath(&cfg), filepath.Join(stateDir(), "control.sock"); got != want {
		t.Errorf("controlSocketPath = %q, want %q", got, want)
	}
	cfg.Control.Socket = "/tmp/custom.sock"
	if got := controlSocketPath(&cfg); got != "/tmp/custom.sock" {
		t.Errorf("configured socket ignored: got %q", got)
	}
}

// Defaults are the product decision: passwordless out of the box, switch ops
// included. A regression here silently reintroduces the password prompts.
func TestControlDefaultsArePasswordless(t *testing.T) {
	cfg := config.Default()
	if !cfg.Control.Enabled {
		t.Error("control.enabled defaults to false; routine ops would prompt for a password")
	}
	if !cfg.Control.AllowSwitchOps {
		t.Error("control.allowSwitchOps defaults to false; switch would prompt for a password")
	}
}

// The control block must round-trip through the config file, or `config set` would
// silently drop it on the next save.
func TestControlConfigRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	body := `{"control":{"enabled":true,"socket":"/var/db/dezhban/control.sock","group":"staff","allowSwitchOps":false}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Control.Group != "staff" || cfg.Control.AllowSwitchOps {
		t.Fatalf("control block did not load: %+v", cfg.Control)
	}

	// Save and reload: the values must survive.
	data, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw["control"]; !ok {
		t.Fatal("saved config dropped the control block")
	}
	out := filepath.Join(dir, "out.json")
	if err := os.WriteFile(out, data, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2, err := config.Load(out)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg2.Control.Group != "staff" || cfg2.Control.AllowSwitchOps {
		t.Fatalf("control block did not round-trip: %+v", cfg2.Control)
	}
}
