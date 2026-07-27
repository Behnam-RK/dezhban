//go:build !darwin && !linux

package svc

// Windows keeps its service registration in the registry, not in a file this
// package can stat, and reading it truthfully means going through the service
// manager — which is the thing Boot exists to avoid depending on. Report that
// the question cannot be answered rather than guessing; a caller renders that
// as "cannot say", never as "not installed".
func platformBoot() BootUnit { return BootUnit{} }
