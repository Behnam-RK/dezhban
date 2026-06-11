//go:build !darwin

package firewall

import "errors"

// errNotImplemented is returned by every operation of the stub backend on
// platforms whose real backend lands later (Linux/Windows in Phase 5).
var errNotImplemented = errors.New("firewall backend not implemented for this OS yet")

// notImplBackend is a placeholder so the rest of the program compiles and links
// on non-macOS targets during Phase 2.
type notImplBackend struct{}

// New returns the stub backend on platforms without a real implementation.
func New() (FirewallBackend, error) {
	return &notImplBackend{}, nil
}

func (b *notImplBackend) Block(Allowlist) error    { return errNotImplemented }
func (b *notImplBackend) Unblock() error           { return errNotImplemented }
func (b *notImplBackend) IsBlocked() (bool, error) { return false, errNotImplemented }
func (b *notImplBackend) Cleanup() error           { return nil }
