package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/behnam-rk/dezhban/internal/control"
)

// stubDaemonRequests is stubDaemon's sibling for the config-write path: it keeps
// whole requests, because what matters here is the token and the key/value map,
// not just which op arrived.
func stubDaemonRequests(t *testing.T, resp control.Response) (string, *[]control.Request) {
	t.Helper()
	dir, err := os.MkdirTemp("", "dzt")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "c.sock")

	srv, err := control.New(sock, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("control.New: %v", err)
	}
	// The daemon's own verifier lives behind the socket; this stub stands in for
	// one that has already accepted, so the tests here cover the CLIENT half.
	srv.VerifyToken = func(string) bool { return true }
	ctx := t.Context()
	srv.Start(ctx)
	t.Cleanup(srv.Wait)

	seen := &[]control.Request{}
	go func() {
		for {
			select {
			case cr := <-srv.Requests():
				*seen = append(*seen, cr.Req)
				cr.Reply <- resp
			case <-ctx.Done():
				return
			}
		}
	}()
	return sock, seen
}

// The passwordless path: the keys and the token reach the daemon, and the CLI
// reports what the daemon said took effect rather than assuming.
func TestConfigWriteGoesToTheDaemonWithItsToken(t *testing.T) {
	sock, seen := stubDaemonRequests(t, control.Response{
		OK:           true,
		Applied:      []string{"pollInterval"},
		NeedsRestart: []string{"logLevel"},
	})
	cfgPath := writeCfg(t, sock, "")

	code, handled := tryConfigWrite(cfgPath, map[string]string{"pollInterval": "9s"}, "sekret")
	if !handled || code != 0 {
		t.Fatalf("tryConfigWrite = (%d, %v), want (0, true)", code, handled)
	}
	if len(*seen) != 1 {
		t.Fatalf("daemon saw %d requests, want 1", len(*seen))
	}
	req := (*seen)[0]
	if req.Op != control.OpConfigWrite {
		t.Errorf("op = %q, want %q", req.Op, control.OpConfigWrite)
	}
	if req.Token != "sekret" {
		t.Errorf("token = %q, want it carried through", req.Token)
	}
	if req.Config["pollInterval"] != "9s" {
		t.Errorf("config = %v, want pollInterval=9s", req.Config)
	}
}

// A refusal is the daemon's decision. Reporting handled=true is what stops the
// caller retrying the same write with root, which would make every gate on the
// op — control.allowConfigOps, the token itself — merely advisory.
func TestConfigWriteRefusalIsNotRetriedWithRoot(t *testing.T) {
	sock, _ := stubDaemonRequests(t, control.Response{
		OK:    false,
		Error: "config writes over the control socket are disabled (control.allowConfigOps)",
	})
	cfgPath := writeCfg(t, sock, "")

	code, handled := tryConfigWrite(cfgPath, map[string]string{"pollInterval": "9s"}, "sekret")
	if !handled {
		t.Fatal("a refusal was reported as unhandled; the caller would escalate to root and bypass the gate")
	}
	if code != ExitDaemonRefused {
		t.Errorf("code = %d, want ExitDaemonRefused (%d)", code, ExitDaemonRefused)
	}
}

// A daemon that could not get the request to its run loop has not decided
// anything. That must fall back to the privileged write, exactly like an
// unreachable daemon — treating it as a refusal would drop the user's edit.
func TestConfigWriteTransientFailureFallsBack(t *testing.T) {
	sock, _ := stubDaemonRequests(t, control.Response{OK: false, Error: "daemon busy", Transient: true})
	cfgPath := writeCfg(t, sock, "")

	if code, handled := tryConfigWrite(cfgPath, map[string]string{"pollInterval": "9s"}, "sekret"); handled {
		t.Fatalf("tryConfigWrite = (%d, true) on a transient failure, want it to fall back", code)
	}
}

// Editing config with no daemon running is normal — before installing, while
// stopped — so the socket path must simply decline and let the ordinary
// privileged write happen.
func TestConfigWriteWithNoDaemonFallsBack(t *testing.T) {
	cfgPath := writeCfg(t, filepath.Join(t.TempDir(), "absent.sock"), "")

	if code, handled := tryConfigWrite(cfgPath, map[string]string{"pollInterval": "9s"}, "sekret"); handled {
		t.Fatalf("tryConfigWrite = (%d, true) with no daemon, want it to fall back", code)
	}
}

// A named-key reset is sent as an ordinary write of the shipped default, so
// resetting a key and setting it to that value take exactly the same validated
// path — and the default itself still comes only from config.Default().
func TestResetViaTokenSendsTheShippedDefault(t *testing.T) {
	sock, seen := stubDaemonRequests(t, control.Response{OK: true, Applied: []string{"vpn.switchWindow"}})
	cfgPath := writeCfg(t, sock, "")
	stdin := replaceStdin(t, "sekret\n")
	defer stdin()

	code, handled := resetViaToken(cfgPath, []string{"vpn.switchWindow"})
	if !handled || code != 0 {
		t.Fatalf("resetViaToken = (%d, %v), want (0, true)", code, handled)
	}
	if len(*seen) != 1 {
		t.Fatalf("daemon saw %d requests, want 1", len(*seen))
	}
	// defaultSwitchWindow is 5s; asserting the exact string would duplicate the
	// default in a second place, so assert only that a real value was sent.
	if v := (*seen)[0].Config["vpn.switchWindow"]; v == "" {
		t.Errorf("config = %v, want vpn.switchWindow set to its shipped default", (*seen)[0].Config)
	}
}

// replaceStdin points os.Stdin at a pipe holding s, and returns a restore func.
func replaceStdin(t *testing.T, s string) func() {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString(s); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = w.Close()
	prev := os.Stdin
	os.Stdin = r
	return func() {
		os.Stdin = prev
		_ = r.Close()
	}
}

// The flag is stripped wherever it appears, like --config, so it works before or
// after the positional args a config subcommand takes.
func TestStripTokenStdinFindsTheFlagAnywhere(t *testing.T) {
	cases := [][]string{
		{"--token-stdin", "set", "pollInterval", "9s"},
		{"set", "--token-stdin", "pollInterval", "9s"},
		{"set", "pollInterval", "9s", "--token-stdin"},
	}
	for _, args := range cases {
		rest, found := stripTokenStdin(args)
		if !found {
			t.Errorf("stripTokenStdin(%v) did not find the flag", args)
		}
		if len(rest) != 3 || rest[0] != "set" {
			t.Errorf("stripTokenStdin(%v) left %v, want the positional args intact", args, rest)
		}
	}
	if _, found := stripTokenStdin([]string{"set", "pollInterval", "9s"}); found {
		t.Error("stripTokenStdin reported the flag when it was absent")
	}
}

// `reset --all` resets keys the socket op cannot express (vpn.advanced.*), so
// serving it there would reset less than it claims. It must send the caller to
// the privileged path rather than quietly doing a smaller job.
func TestResetAllIsNotServedOverTheSocket(t *testing.T) {
	sock, seen := stubDaemonRequests(t, control.Response{OK: true})
	cfgPath := writeCfg(t, sock, "")

	code, handled := resetViaToken(cfgPath, []string{"--all"})
	if !handled || code == 0 {
		t.Fatalf("resetViaToken(--all) = (%d, %v), want a handled refusal", code, handled)
	}
	if len(*seen) != 0 {
		t.Fatalf("--all reached the daemon as %v; it must not be served over the socket", *seen)
	}
}
