package svc

import (
	"os"
	"strings"
	"testing"
)

// installScript is read, not executed: this asserts a text contract, and the
// script itself needs root, a network, and a real service manager.
const installScript = "../../scripts/install.sh"

// The installer decides whether an upgrade has a live daemon to restart by
// grepping `dezhban status --json` for the string Status returns for a running
// service. Nothing else connects those two files — no import, no shared type,
// no build error — so a rename on the Go side leaves the installer matching a
// string that can no longer appear.
//
// The failure is silent and actively misleading rather than loud: was_running
// stays 0, the stop/restart is skipped, and the footer tells the user "The
// service was not running, so it was left stopped" while their daemon is in
// fact still running the OLD build. They have no reason to run `dezhban
// restart`, so the upgrade never actually takes effect.
//
// Asserting on the constant, not a second copy of the literal, is the point:
// this test can only pass when both sides say the same thing.
func TestInstallerGrepsTheLiveServiceString(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(installScript)
	if err != nil {
		t.Fatalf("read %s: %v", installScript, err)
	}
	if !strings.Contains(string(data), StatusInstalledRunning) {
		t.Errorf("%s no longer matches svc.StatusInstalledRunning (%q).\n"+
			"These two must agree: the installer greps `dezhban status --json` for that exact string to\n"+
			"decide whether to restart a running daemon after an upgrade. Update the grep in install.sh's\n"+
			"was_running check, or revert the constant.",
			installScript, StatusInstalledRunning)
	}
}
