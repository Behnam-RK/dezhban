package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"time"
)

// ErrUnavailable means the daemon is not reachable on the socket (not running,
// or control disabled). Callers treat it as "fall back to the root path", not as
// a failure — the CLI still works with no daemon.
var ErrUnavailable = errors.New("control: daemon not reachable")

// ErrForbidden means the socket exists but this user may not open it — i.e. the
// caller is not in control.group. Distinguished from ErrUnavailable so the CLI can
// say something useful ("you are not in the admin group") instead of silently
// falling back to a password prompt with no explanation.
var ErrForbidden = errors.New("control: permission denied on control socket")

// Do sends one request and returns the daemon's response. A non-OK response is
// returned as a Response with OK=false (not an error): it means the daemon was
// reached and deliberately refused, which callers must NOT retry via the root
// path — the refusal is the answer.
func Do(path string, req Request) (Response, error) {
	req.V = Version
	conn, err := net.DialTimeout("unix", path, dialTimeout)
	if err != nil {
		return Response{}, classifyDialErr(path, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(replyTimeout + dialTimeout))

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, fmt.Errorf("control: send: %w", err)
	}
	var resp Response
	if err := json.NewDecoder(io.LimitReader(conn, maxRequestBytes)).Decode(&resp); err != nil {
		return Response{}, fmt.Errorf("control: read reply: %w", err)
	}
	return resp, nil
}

// Ping reports whether a daemon is listening and answering on path.
func Ping(path string) bool {
	resp, err := Do(path, Request{Op: OpPing})
	return err == nil && resp.OK
}

// classifyDialErr turns a dial failure into the two cases a caller acts on
// differently: "no daemon" (fall back to root) vs "not permitted" (tell the user
// why, because falling back would silently re-introduce the password prompt this
// feature exists to remove).
func classifyDialErr(path string, err error) error {
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("%w (%s)", ErrForbidden, path)
	}
	return fmt.Errorf("%w (%s): %v", ErrUnavailable, path, err)
}
