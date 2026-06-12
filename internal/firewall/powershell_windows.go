//go:build windows

package firewall

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// psTimeout bounds any single PowerShell invocation. Firewall cmdlets are local
// and fast; a hang means something is wrong.
const psTimeout = 20 * time.Second

// powershell runs a PowerShell script and returns its stdout. On failure it
// wraps the error with captured stderr so callers get a debuggable message.
//
// This is the Windows analogue of pfctl()/nft(): we drive the OS firewall by
// shelling to PowerShell's NetSecurity cmdlets rather than linking tailscale/wf.
// The plan tentatively preferred the WFP library but explicitly sanctioned the
// `New-NetFirewallRule -Group dezhban` path as the alternative; we take it to
// keep dezhban dependency-light and cross-compilable, and because grouped rules
// give the same removable-as-a-unit teardown the WFP sublayer would.
func powershell(script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), psTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb

	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("powershell: %w: %s",
			err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}
