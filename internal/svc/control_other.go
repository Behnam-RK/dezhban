//go:build !darwin

package svc

import "github.com/kardianos/service"

// On Linux and Windows the kardianos control path is correct as-is; only
// darwin needs the domain-explicit launchctl override (see launchd_darwin.go).

func platformControl(string) (handled bool, err error) { return false, nil }

func platformStatus() (service.Status, error) { return kardianosStatus() }
