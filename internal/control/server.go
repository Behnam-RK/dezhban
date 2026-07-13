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
	path     string
	log      *slog.Logger
	ln       net.Listener
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
	// A socket left behind by a crash (or a KeepAlive restart that skipped the
	// deferred stop) would make Listen fail with EADDRINUSE, so unlink first. This
	// is safe: only root can write this directory, and a live daemon holding the
	// socket is a supervisor-level concern (launchd runs exactly one).
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("control: remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("control: listen %q: %w", path, err)
	}
	if err := secureSocket(path, group); err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return &Server{
		path:     path,
		log:      log,
		ln:       ln,
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
		for {
			conn, err := s.ln.Accept()
			if err != nil {
				// Closed listener is the normal shutdown path.
				if errors.Is(err, net.ErrClosed) {
					return
				}
				s.log.Debug("control: accept failed", "err", err)
				continue
			}
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.serve(ctx, conn)
			}()
		}
	}()
}

// Stop closes the listener and unlinks the socket. Safe to call more than once.
func (s *Server) Stop() {
	s.stopOnce.Do(func() {
		_ = s.ln.Close()
		_ = os.Remove(s.path)
	})
}

// Wait blocks until every accept/serve goroutine has exited.
func (s *Server) Wait() { s.wg.Wait() }

// serve handles one connection: one request in, one response out, then close.
func (s *Server) serve(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(connDeadline))

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
	case OpStatus, OpBlock, OpUnblock, OpOpenSwitch, OpCancelSwitch:
	default:
		s.reply(conn, errResponse(fmt.Sprintf("unknown op %q", req.Op)))
		return
	}

	cr := ConnRequest{Req: req, Reply: make(chan Response, 1)}
	select {
	case s.requests <- cr:
	case <-time.After(handoffTimeout):
		s.reply(conn, errResponse("daemon busy"))
		return
	case <-ctx.Done():
		s.reply(conn, errResponse("daemon shutting down"))
		return
	}

	select {
	case resp := <-cr.Reply:
		s.reply(conn, resp)
	case <-time.After(replyTimeout):
		s.reply(conn, errResponse("timed out waiting for daemon"))
	case <-ctx.Done():
		s.reply(conn, errResponse("daemon shutting down"))
	}
}

func (s *Server) reply(conn net.Conn, resp Response) {
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		s.log.Debug("control: reply failed", "err", err)
	}
}
