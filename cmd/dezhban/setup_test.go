package main

import (
	"testing"

	"github.com/behnam-rk/dezhban/internal/config"
)

// applyWizard in auto mode must produce a "connect any VPN" config: no pinned
// interfaces (autodetect implied on Normalize), profiles carried, and
// allowPhysicalDNS honored.
func TestApplyWizardAutoMode(t *testing.T) {
	cfg := config.Default()
	applyWizard(&cfg, wizardInput{
		pollInterval: "30s", hysteresis: "3", logLevel: "info",
		configureVPN: true, autoMode: true,
		tunnels:          []string{"utun9"}, // must be ignored in auto mode
		endpoints:        []string{"vpn.example.com"},
		profiles:         []config.Profile{{Name: "home", Endpoints: []string{"203.0.113.7"}}},
		autoDiscover:     true,
		allowPhysicalDNS: true,
	})
	if len(cfg.VPN.TunnelInterfaces) != 0 {
		t.Errorf("auto mode must not pin interfaces, got %v", cfg.VPN.TunnelInterfaces)
	}
	if !cfg.VPN.AllowPhysicalDNS {
		t.Error("allowPhysicalDNS should be set")
	}
	if len(cfg.VPN.Profiles) != 1 || cfg.VPN.Profiles[0].Name != "home" {
		t.Errorf("profiles not carried: %+v", cfg.VPN.Profiles)
	}
	// Normalize (run on save) implies autodetect when no interfaces are pinned.
	config.Normalize(&cfg)
	if !cfg.VPN.Autodetect {
		t.Error("autodetect should be implied for an auto-mode config")
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("auto-mode config should validate: %v", err)
	}
}

// Advanced mode pins the chosen interfaces.
func TestApplyWizardAdvancedPin(t *testing.T) {
	cfg := config.Default()
	applyWizard(&cfg, wizardInput{
		pollInterval: "30s", hysteresis: "3", logLevel: "info",
		configureVPN: true, autoMode: false,
		tunnels:   []string{"utun4"},
		endpoints: []string{"203.0.113.7"},
	})
	if len(cfg.VPN.TunnelInterfaces) != 1 || cfg.VPN.TunnelInterfaces[0] != "utun4" {
		t.Errorf("advanced mode should pin utun4, got %v", cfg.VPN.TunnelInterfaces)
	}
}
