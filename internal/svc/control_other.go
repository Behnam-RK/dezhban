//go:build !darwin

package svc

import "github.com/kardianos/service"

// On Linux and Windows the kardianos control path is correct as-is; only
// darwin needs the domain-explicit launchctl override (see launchd_darwin.go).

func platformControl(string) (handled bool, err error) { return false, nil }

func platformStatus() (service.Status, error) { return kardianosStatus() }

// systemd/Windows have no launchd-style "loaded but parked" state that a stop
// must still tear down, and their stop is level-triggered anyway — running is
// the right proxy, which keeps the old stop-idempotence behavior on !darwin.
func platformLoaded() bool {
	st, err := kardianosStatus()
	return err == nil && st == service.StatusRunning
}
