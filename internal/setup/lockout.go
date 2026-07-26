package setup

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/behnam-rk/dezhban/internal/config"
	"github.com/behnam-rk/dezhban/internal/netdetect"
)

// EndpointLockoutWarning returns a non-empty message when an IP endpoint sits
// inside a tunnel's own subnet — the single most common way to lock yourself
// out, because the pass that is supposed to let the VPN redial points at an
// address only reachable through the tunnel that is down.
func EndpointLockoutWarning(cfg *config.Config) string {
	var addrs []netip.Addr
	for _, ep := range config.EffectiveEndpoints(cfg, nil) {
		if a, err := netip.ParseAddr(ep); err == nil {
			addrs = append(addrs, a)
		}
	}
	if len(addrs) == 0 {
		return ""
	}
	bad, err := netdetect.CheckEndpointRouting(addrs, cfg.VPN.TunnelInterfaces)
	if err != nil || len(bad) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n⚠  WARNING: endpoint(s) sit inside a tunnel subnet — this will likely lock you out:\n")
	for _, r := range bad {
		fmt.Fprintf(&b, "     %s is within %s (%s)\n", r.Endpoint, r.Subnet, r.Iface)
	}
	b.WriteString("   Set the VPN server's PHYSICAL (public) address instead — see 'dezhban doctor --discover'.")
	return b.String()
}
