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
	handle(t, srv, Response{OK: true, Mode: "vpn", Posture: "full-block", Blocked: true})

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
