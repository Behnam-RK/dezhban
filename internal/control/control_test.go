package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// tempSocket returns a short socket path. t.TempDir() embeds the test name, which
// on macOS blows past the ~104-byte sun_path limit and fails Listen with
// "invalid argument" — so build the path from a short MkdirTemp instead.
func tempSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "dzb")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "c.sock")
}

// newTestServer starts a server on a temp socket with no group (root-only mode
// bits — a test process is not root and cannot chown to "admin").
func newTestServer(t *testing.T) (*Server, context.CancelFunc) {
	t.Helper()
	path := tempSocket(t)
	srv, err := New(path, "", testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv.Start(ctx)
	t.Cleanup(func() {
		cancel()
		srv.Wait()
	})
	return srv, cancel
}

// handle drains one request from the run-loop channel and answers it, standing in
// for the daemon's select case.
func handle(t *testing.T, srv *Server, resp Response) {
	t.Helper()
	go func() {
		select {
		case cr := <-srv.Requests():
			cr.Reply <- resp
		case <-time.After(3 * time.Second):
			t.Error("no request reached the run loop")
		}
	}()
}

func TestRoundTrip(t *testing.T) {
	srv, _ := newTestServer(t)
	handle(t, srv, Response{OK: true, Posture: "full-block", Blocked: true})

	resp, err := Do(srv.Path(), Request{Op: OpBlock})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !resp.OK || resp.Posture != "full-block" || !resp.Blocked {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

// Ping must be answered by the accept goroutine alone — with nothing draining the
// request channel, it still succeeds.
func TestPingNeedsNoRunLoop(t *testing.T) {
	srv, _ := newTestServer(t)
	if !Ping(srv.Path()) {
		t.Fatal("ping failed with no run loop draining requests")
	}
}

func TestSocketModeRootOnlyWithoutGroup(t *testing.T) {
	srv, _ := newTestServer(t)
	fi, err := os.Stat(srv.Path())
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket perm = %o, want 0600 (no group configured must not be group/world reachable)", perm)
	}
}

func TestUnknownGroupFailsClosed(t *testing.T) {
	path := tempSocket(t)
	if _, err := New(path, "definitely-not-a-real-group", testLogger()); err == nil {
		t.Fatal("New succeeded with an unresolvable group; must fail closed")
	}
	// The half-configured socket must not be left behind for anyone to talk to.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket still present after failed New: %v", err)
	}
}

func TestStaleSocketIsReplaced(t *testing.T) {
	path := tempSocket(t)
	// Simulate a crash: a socket file left behind with no listener.
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("pre-listen: %v", err)
	}
	_ = ln.Close()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("plant stale file: %v", err)
	}

	srv, err := New(path, "", testLogger())
	if err != nil {
		t.Fatalf("New over stale socket: %v", err)
	}
	defer srv.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	if !Ping(path) {
		t.Fatal("server not reachable after replacing a stale socket")
	}
}

func TestMalformedRequestRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	conn, err := net.Dial("unix", srv.Path())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("this is not json\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if resp.OK || resp.Error == "" {
		t.Fatalf("malformed request accepted: %+v", resp)
	}
}

func TestWrongVersionRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	conn, err := net.Dial("unix", srv.Path())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := json.NewEncoder(conn).Encode(Request{V: 99, Op: OpBlock}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error, "unsupported protocol version") {
		t.Fatalf("bad-version request accepted: %+v", resp)
	}
}

func TestUnknownOpRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := Do(srv.Path(), Request{Op: Op("rm-rf")})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error, "unknown op") {
		t.Fatalf("unknown op accepted: %+v", resp)
	}
}

// An oversized payload must be cut off at the limit rather than buffered whole.
func TestOversizedRequestRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	conn, err := net.Dial("unix", srv.Path())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	// A syntactically valid request whose profile field dwarfs the read limit.
	huge := Request{V: Version, Op: OpOpenSwitch, Profile: strings.Repeat("A", maxRequestBytes*2)}
	// The server stops reading at the limit, answers, and closes — so the tail of
	// this write may fail with EPIPE. That IS the rejection: either the write dies
	// or we read back a refusal. What must never happen is the request succeeding.
	if err := json.NewEncoder(conn).Encode(huge); err != nil {
		return
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return // connection torn down without accepting the request
	}
	if resp.OK {
		t.Fatal("oversized request accepted")
	}
}

func TestDoOnMissingSocketIsUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.sock") // never created; length irrelevant
	_, err := Do(path, Request{Op: OpStatus})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable so the CLI falls back to the root path", err)
	}
	if Ping(path) {
		t.Fatal("Ping succeeded against a nonexistent socket")
	}
}

// A run loop that never drains must not hang the caller forever: the server
// reports "daemon busy" once the hand-off queue is full.
func TestBusyDaemonReplies(t *testing.T) {
	srv, _ := newTestServer(t)
	// Fill the hand-off buffer; nothing is draining Requests().
	for i := 0; i < requestBuffer; i++ {
		srv.requests <- ConnRequest{Req: Request{Op: OpStatus}, Reply: make(chan Response, 1)}
	}
	start := time.Now()
	resp, err := Do(srv.Path(), Request{Op: OpStatus})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.OK || resp.Error != "daemon busy" {
		t.Fatalf("resp = %+v, want a busy refusal", resp)
	}
	if elapsed := time.Since(start); elapsed > handoffTimeout+2*time.Second {
		t.Fatalf("busy reply took %s; hand-off timeout is not bounding it", elapsed)
	}
}

func TestStopUnlinksSocket(t *testing.T) {
	path := tempSocket(t)
	srv, err := New(path, "", testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv.Start(ctx)
	cancel()
	srv.Wait()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket survived shutdown: %v", err)
	}
}

// TestSlowRunLoopStillGetsItsReplyThrough guards the timeout budget. serve() used
// to arm one connection-wide deadline (connDeadline, 5s), but its own worst case is
// handoffTimeout+replyTimeout (12s) — so a run loop that took longer than 5s to
// answer (a slow Backend.Apply under load) had its reply write fail on an expired
// deadline, and the caller saw an EOF instead of the daemon's actual answer.
func TestSlowRunLoopStillGetsItsReplyThrough(t *testing.T) {
	srv, _ := newTestServer(t)

	// Answer from the run loop only after the old connection-wide deadline would
	// have fired. The reply must still arrive intact.
	go func() {
		select {
		case cr := <-srv.Requests():
			time.Sleep(connDeadline + time.Second)
			cr.Reply <- Response{OK: true, Posture: "GUARD"}
		case <-time.After(3 * time.Second):
			t.Error("no request reached the run loop")
		}
	}()

	resp, err := Do(srv.Path(), Request{Op: OpStatus})
	if err != nil {
		t.Fatalf("a reply issued after connDeadline must still reach the client: %v", err)
	}
	if !resp.OK || resp.Posture != "GUARD" {
		t.Fatalf("got %+v, want the run loop's own response", resp)
	}
}

// TestStopLeavesASupersededSocketAlone guards the shutdown unlink. Publishing
// renames OVER the path, so a daemon that starts while an older one is still
// shutting down owns the path — and the older one's Stop must not delete the live
// socket out from under it, which would strand it on an unreachable inode.
func TestStopLeavesASupersededSocketAlone(t *testing.T) {
	path := tempSocket(t)

	old, err := New(path, "", testLogger())
	if err != nil {
		t.Fatalf("New (old): %v", err)
	}
	// A second daemon publishes over the same path while the first is still alive.
	fresh, err := New(path, "", testLogger())
	if err != nil {
		t.Fatalf("New (fresh): %v", err)
	}
	t.Cleanup(fresh.Stop)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fresh.Start(ctx)

	old.Stop() // must not unlink the path: what sits there now belongs to `fresh`

	handle(t, fresh, Response{OK: true, Posture: "GUARD"})
	resp, err := Do(path, Request{Op: OpStatus})
	if err != nil {
		t.Fatalf("the surviving daemon must still be reachable after the old one stopped: %v", err)
	}
	if !resp.OK {
		t.Fatalf("got %+v, want the surviving daemon's response", resp)
	}
}

// TestPermissionDeniedIsForbiddenNotUnavailable pins the dial-error classification,
// which is subtle enough to invite a wrong "fix".
//
// A caller that cannot open the socket must get ErrForbidden ("you are not in the
// group"), never ErrUnavailable ("no daemon"): the CLI silently falls back to a
// password prompt on the latter, which is exactly the confusion this feature exists
// to remove.
//
// errors.Is IS the right test, despite net.DialTimeout returning a *net.OpError
// rather than a bare errno: OpError unwraps to os.SyscallError to syscall.Errno, and
// syscall.Errno implements Is() to match fs.ErrPermission on EACCES/EPERM.
// os.IsPermission, by contrast, returns FALSE on this same error — it does not
// unwrap through net.OpError — so "simplifying" classifyDialErr to use it would
// break the classification and silently reintroduce the sudo fallback. This test
// fails if anyone tries.
func TestPermissionDeniedIsForbiddenNotUnavailable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not gate the dial")
	}
	path := tempSocket(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	// Unopenable by anyone but root — stands in for a socket whose group we are not in.
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if _, err := Do(path, Request{Op: OpStatus}); err == nil {
		t.Fatal("dial to a 0000 socket unexpectedly succeeded")
	} else if !errors.Is(err, ErrForbidden) {
		t.Fatalf("got %v, want ErrForbidden — a socket we may not open must not look like an absent daemon", err)
	} else if errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v, want ErrForbidden only — ErrUnavailable makes the CLI fall back to sudo silently", err)
	}
}
