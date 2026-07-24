package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ConnRequest is a request handed to the run loop, with the channel to reply on.
// The run loop MUST always send exactly one Response (the accept goroutine is
// waiting on it, under a timeout); Reply is buffered so a late send never blocks.
type ConnRequest struct {
	Req   Request
	Reply chan Response
}

// Server accepts control connections and forwards each request to the run loop
// over Requests(). It NEVER touches the firewall Backend itself — that keeps the
// daemon's core invariant intact: the run-loop goroutine is the sole caller of
// Backend.Apply. The accept goroutine only parses, gates, and hands off.
type Server struct {
	path string
	log  *slog.Logger
	ln   net.Listener
	// self identifies the socket inode we published at path, so Stop can tell our
	// own socket from one a later daemon has since renamed over the top of it.
	// nil means "identity unknown" — Stop then leaves the path alone.
	self     os.FileInfo
	requests chan ConnRequest

	stopOnce sync.Once
	wg       sync.WaitGroup
}

// requestBuffer sizes the hand-off queue. The run loop drains it as one select
// case among many; a small buffer absorbs a burst (e.g. a GUI double-click)
// without ever letting a slow client stall enforcement.
const requestBuffer = 8

// New creates the socket and returns a Server ready to Start.
//
// It fails closed: a socket that cannot be given the intended ownership/mode is
// removed rather than left in place, because a world-writable control socket
// would hand posture control to any local user. group is the unix group allowed
// to talk to the daemon (macOS: "admin"); an empty group means root-only (0600),
// which effectively disables the passwordless path until an operator opts in.
func New(path, group string, log *slog.Logger) (*Server, error) {
	if path == "" {
		return nil, errors.New("control: empty socket path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("control: create dir %q: %w", dir, err)
	}
	// MkdirAll is a no-op on a directory that already exists, so the 0755 above says
	// nothing about the mode of one we did not just create — and the socket's parent
	// is part of the authorization boundary (whoever may unlink it may impersonate the
	// daemon). Judge what is actually there, and fail the control feature closed if it
	// is tamperable. Only the default path is guaranteed sound by construction
	// (state.EnsureDir owns the state dir's mode); a configured control.socket can
	// point anywhere.
	if err := checkDirSecure(dir); err != nil {
		return nil, err
	}
	// listenSecure publishes the socket at path only once it already carries its
	// intended ownership and mode, so it is never briefly reachable under a
	// umask-derived one. It also replaces a socket left behind by a crash.
	ln, err := listenSecure(path, group)
	if err != nil {
		return nil, err
	}
	// Remember which inode we published, for Stop's identity check. A failure here
	// is not fatal: it only costs us the check (Stop will decline to unlink), which
	// is the safe direction — a stale socket is recoverable, unlinking a live one
	// silently strips a running daemon of its control channel.
	self, err := os.Lstat(path)
	if err != nil {
		log.Debug("control: cannot stat published socket; Stop will not unlink", "err", err)
		self = nil
	}
	return &Server{
		path:     path,
		log:      log,
		ln:       ln,
		self:     self,
		requests: make(chan ConnRequest, requestBuffer),
	}, nil
}

// Path is the socket's filesystem path.
func (s *Server) Path() string { return s.path }

// Requests is the channel the run loop selects on.
func (s *Server) Requests() <-chan ConnRequest { return s.requests }

// Start runs the accept loop until ctx is cancelled or Stop is called.
func (s *Server) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		<-ctx.Done()
		s.Stop()
	}()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		// Backoff for a failing Accept. Some errors are persistent rather than
		// per-connection — fd exhaustion (EMFILE) is the realistic one — and retrying
		// those with a bare `continue` spins this goroutine at 100% CPU inside a daemon
		// whose entire job is to stay responsive enough to cut the network. Back off
		// instead, and reset the moment a connection succeeds.
		backoff := time.Duration(0)
		for {
			conn, err := s.ln.Accept()
			if err != nil {
				// Closed listener is the normal shutdown path.
				if errors.Is(err, net.ErrClosed) {
					return
				}
				if backoff == 0 {
					backoff = acceptBackoffMin
				} else if backoff < acceptBackoffMax {
					backoff *= 2
				}
				s.log.Debug("control: accept failed", "err", err, "retry_in", backoff)
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					return
				}
				continue
			}
			backoff = 0
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.serve(ctx, conn)
			}()
		}
	}()
}

// Stop closes the listener and unlinks the socket. Safe to call more than once.
//
// The unlink is identity-checked. listenSecure publishes by renaming OVER path, so
// a daemon starting while this one is still shutting down leaves a different socket
// at the same path — and a blind os.Remove here would delete the live daemon's
// control channel, leaving it serving on an inode nobody can reach and silently
// pushing every routine op back to a password prompt. Remove only what we published.
func (s *Server) Stop() {
	s.stopOnce.Do(func() {
		_ = s.ln.Close()
		if s.self == nil {
			return
		}
		cur, err := os.Lstat(s.path)
		if err != nil || !os.SameFile(s.self, cur) {
			// Already gone, or superseded by another daemon's socket — not ours to remove.
			return
		}
		_ = os.Remove(s.path)
	})
}

// Wait blocks until every accept/serve goroutine has exited.
func (s *Server) Wait() { s.wg.Wait() }

// serve handles one connection: one request in, one response out, then close.
func (s *Server) serve(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	// Bound the READ only. A connection-wide deadline would also bound the wait for
	// the run loop — and that wait is legitimately longer (handoffTimeout +
	// replyTimeout) than connDeadline, so a slow Apply would trip it and drop the
	// reply the caller is still blocked on. The write gets its own fresh deadline in
	// reply(); this one exists solely so a peer that opens a connection and never
	// sends cannot pin a goroutine.
	_ = conn.SetReadDeadline(time.Now().Add(connDeadline))

	var req Request
	// Bound the read: an unbounded decode off a socket any admin can open is a
	// trivial memory-exhaustion lever.
	if err := json.NewDecoder(io.LimitReader(conn, maxRequestBytes)).Decode(&req); err != nil {
		s.reply(conn, errResponse("malformed request"))
		s.log.Debug("control: decode failed", "err", err)
		return
	}
	if req.V != Version {
		s.reply(conn, errResponse(fmt.Sprintf("unsupported protocol version %d (want %d)", req.V, Version)))
		return
	}

	// Ping never reaches the run loop: it is pure liveness, so answering it here
	// keeps `status` responsive even while the loop is mid-apply.
	if req.Op == OpPing {
		s.reply(conn, Response{OK: true})
		return
	}

	switch req.Op {
	case OpStatus, OpBlock, OpUnblock, OpOpenSwitch, OpCancelSwitch, OpPause, OpResume, OpReload:
	default:
		s.reply(conn, errResponse(fmt.Sprintf("unknown op %q", req.Op)))
		return
	}

	cr := ConnRequest{Req: req, Reply: make(chan Response, 1)}
	select {
	case s.requests <- cr:
	case <-time.After(handoffTimeout):
		s.reply(conn, busyResponse("daemon busy"))
		return
	case <-ctx.Done():
		s.reply(conn, busyResponse("daemon shutting down"))
		return
	}

	select {
	case resp := <-cr.Reply:
		s.reply(conn, resp)
	case <-time.After(replyTimeout):
		s.reply(conn, busyResponse("timed out waiting for daemon"))
	case <-ctx.Done():
		s.reply(conn, busyResponse("daemon shutting down"))
	}
}

func (s *Server) reply(conn net.Conn, resp Response) {
	// A fresh deadline, never the read's: reply may run up to
	// handoffTimeout+replyTimeout after the read deadline was armed, and inheriting
	// an already-expired deadline would fail every slow-path reply — precisely the
	// ones ("daemon busy", "timed out waiting for daemon") the caller most needs.
	_ = conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		s.log.Debug("control: reply failed", "err", err)
	}
}
