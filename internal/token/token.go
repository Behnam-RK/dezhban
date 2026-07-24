// Package token is the daemon's shared secret with a trusted local client.
//
// The control socket's only gate today is filesystem permissions: it is created
// root-owned, mode 0660, and chowned to an admin group, and dezhban stays
// stdlib-only so there are no SO_PEERCRED peer credentials to check. Anyone who
// can open the socket is therefore authorised. That is an acceptable bar for ops
// that only move between the daemon's own fail-closed postures, but not for one
// that writes configuration.
//
// A token raises that bar rather than lowering it. The client proves it holds a
// secret the user enrolled — on macOS, held in the login keychain behind
// biometry, so producing it is a Touch ID prompt — and the daemon checks it
// against a root-owned hash no unprivileged process can read or replace. Adding
// a config-writing op behind this is a net tightening, not an escalation:
// filesystem permissions alone would have been a weaker gate.
//
// Only the hash is stored on disk. The daemon never holds the token itself, so
// a readable state directory would leak nothing usable.
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FileMode is the hash file's permission: readable and writable by root only.
// The daemon runs as root and the enrolling client elevates once to write it,
// so nothing legitimate needs wider access — and anything that could read it
// could forge the proof it exists to check.
const FileMode fs.FileMode = 0o600

// ErrNotEnrolled means no hash file exists: nobody has enrolled a token on this
// host. Callers must treat it as "token ops are unavailable", never as "any
// token will do".
var ErrNotEnrolled = errors.New("no control token enrolled")

// New returns a fresh 256-bit token, hex-encoded. Callers hand it to the user's
// keychain and its hash to Save; it is never written anywhere by this package.
func New() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate control token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// hashOf is a plain SHA-256. No password KDF: New's output is 256 bits of
// entropy, so there is no dictionary to stretch against, and a slow hash on the
// verify path would only add latency to every request.
func hashOf(tok string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(tok)))
	return hex.EncodeToString(sum[:])
}

// Save writes the hash of tok, replacing any previous enrollment. Written to a
// temporary file in the same directory and renamed, so a crash mid-write leaves
// either the old enrollment or the new one — never a truncated hash that would
// lock out a correct token.
func Save(path, tok string) error {
	if strings.TrimSpace(tok) == "" {
		return errors.New("refusing to enroll an empty control token")
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".control-token-*")
	if err != nil {
		return fmt.Errorf("stage control token: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds

	if err := tmp.Chmod(FileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure control token: %w", err)
	}
	if _, err := tmp.WriteString(hashOf(tok) + "\n"); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write control token: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write control token: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install control token: %w", err)
	}
	return nil
}

// Remove un-enrolls, so a lost keychain item can be recovered from without
// leaving a hash no client can ever satisfy. Absent is success.
func Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove control token: %w", err)
	}
	return nil
}

// Enrolled reports whether a hash exists, without reading it. Used to describe
// the host's state honestly rather than to make an authorisation decision.
func Enrolled(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// Verify reports whether presented matches the enrolled token.
//
// It returns ErrNotEnrolled when there is no enrollment, which callers must
// refuse on: an absent hash means the feature is unavailable, and treating it as
// "allow" would turn a missing file into an open door. An empty presented token
// is refused before any comparison, so a client that simply omits the field can
// never match a corrupt or empty hash file.
func Verify(path, presented string) (bool, error) {
	if strings.TrimSpace(presented) == "" {
		return false, nil
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, ErrNotEnrolled
	case err != nil:
		return false, fmt.Errorf("read control token: %w", err)
	}
	want := strings.TrimSpace(string(data))
	if want == "" {
		return false, ErrNotEnrolled
	}
	// Constant time: the comparison is against a secret-derived value, and a
	// length-or-prefix leak would let a caller with socket access recover the
	// hash byte by byte.
	got := hashOf(presented)
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1, nil
}
