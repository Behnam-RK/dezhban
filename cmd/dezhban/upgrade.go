// dezhban upgrade: check/download/apply against a GitHub release. See
// docs/upgrade.md and internal/update for the design; this file is the CLI
// orchestration layer — root-gating, staging paths, the installer(8) call,
// and the stash/activate/health-check/rollback sequence around it. The
// verification, gate, and file-shuffling logic itself lives in
// internal/update, where it can be unit-tested without root or a real
// service.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/behnam-rk/dezhban/internal/logging"
	"github.com/behnam-rk/dezhban/internal/state"
	"github.com/behnam-rk/dezhban/internal/update"
)

const upgradeUsage = `usage: dezhban upgrade <subcommand>

  check      Ask GitHub for the latest release and report if one is newer (no root)
  download   Fetch and verify the latest .pkg, staged for apply (root — see below)
  apply      Install the staged .pkg and, unless --no-activate, restart into it (root)

download needs root too, not just apply: its staging directory is root-owned
on purpose. A writable-by-anyone staging area would let a local user swap the
verified .pkg for something else before apply installs it — exactly the
tampering window signature verification exists to close.

Self-apply is macOS only (Linux/Windows package managers own their own
upgrade path — this repo does not reimplement apt/dnf/winget). "upgrade check"
still works everywhere and is what the GUI polls in user context; the root
daemon itself never makes this call (see CLAUDE.md's invariants).

Applying is two separate steps on purpose (docs/upgrade.md): running the
.pkg's installer opens no gap at all — the current daemon keeps enforcing on
its OLD inode while the new files land. Only ACTIVATING (the restart that
actually runs the new binary) is the exposure, and it is gated: refused
unless the daemon is in a healthy "guard" or "standby" posture, never during
FULL BLOCK or an open switch window — re-checked at the instant of restart,
not at download time. --no-activate applies without restarting; activate
later with "sudo dezhban restart".`

func cmdUpgrade(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, upgradeUsage)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "check":
		return cmdUpgradeCheck(rest)
	case "download":
		return cmdUpgradeDownload(rest)
	case "apply":
		return cmdUpgradeApply(rest)
	case "help", "-h", "--help":
		fmt.Println(upgradeUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown upgrade subcommand %q\n\n%s\n", sub, upgradeUsage)
		return 2
	}
}

// upgradeStageDir holds the downloaded, verified .pkg awaiting apply.
// upgradeStashDir holds the pre-upgrade binary/app for rollback. Both live
// under the daemon's own state directory: machine-derived, safe-to-discard
// operational data, same classification learned.json already has.
func upgradeStageDir() string { return filepath.Join(stateDir(), "upgrade-stage") }
func upgradeStashDir() string { return filepath.Join(stateDir(), update.StashDirName) }

func cmdUpgradeCheck(args []string) int {
	fs := flag.NewFlagSet("upgrade check", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	_ = fs.Parse(args)

	client := &http.Client{Timeout: 10 * time.Second}
	res, err := update.Check(buildStamp.Version, client)
	if err != nil {
		fmt.Fprintln(os.Stderr, "upgrade check:", err)
		return 1
	}

	if *jsonOut {
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))
		return 0
	}

	if res.Current == "" {
		fmt.Printf("running a dev build (%s) — not comparable to a release; latest is v%s\n", buildStamp.Version, res.Latest)
		return 0
	}
	if !res.Available {
		fmt.Printf("up to date (v%s)\n", res.Current)
		return 0
	}
	fmt.Printf("update available: v%s -> v%s\n", res.Current, res.Latest)
	fmt.Printf("  %s\n", res.URL)
	if runtime.GOOS == "darwin" {
		fmt.Println("  sudo dezhban upgrade download && sudo dezhban upgrade apply")
	} else {
		fmt.Println("  self-upgrade isn't available on this OS — see docs/upgrade.md for the update path")
	}
	return 0
}

func cmdUpgradeDownload(args []string) int {
	fs := flag.NewFlagSet("upgrade download", flag.ExitOnError)
	versionFlag := fs.String("version", "", "exact version to fetch (default: latest)")
	_ = fs.Parse(args)

	if runtime.GOOS != "darwin" {
		fmt.Fprintln(os.Stderr, "upgrade download: self-upgrade is macOS-only — see docs/upgrade.md")
		return 1
	}
	// Root, not just "download": the staging directory lives under
	// /var/db/dezhban (root-owned, 0755 — see state.DirMode), and it has to.
	// A world-writable staging area would let any local user swap the
	// verified .pkg for something else between download and apply — a real
	// TOCTOU hole that would undermine the whole point of verifying it here.
	if !requireRoot("upgrade download") {
		return 1
	}

	client := &http.Client{Timeout: 60 * time.Second}
	version := *versionFlag
	if version == "" {
		res, err := update.Check(buildStamp.Version, client)
		if err != nil {
			fmt.Fprintln(os.Stderr, "upgrade download:", err)
			return 1
		}
		if !res.Available {
			fmt.Println("already up to date — nothing to download")
			return 0
		}
		version = res.Latest
	}

	stageDir := upgradeStageDir()
	if err := os.RemoveAll(stageDir); err != nil {
		fmt.Fprintln(os.Stderr, "upgrade download:", err)
		return 1
	}
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "upgrade download:", err)
		return 1
	}

	fmt.Printf("downloading v%s...\n", version)
	pkgPath, err := update.Download(stageDir, version, client)
	if err != nil {
		fmt.Fprintln(os.Stderr, "upgrade download:", err)
		_ = os.RemoveAll(stageDir)
		return 1
	}
	fmt.Printf("verified and staged: %s\n", pkgPath)
	fmt.Println("run: sudo dezhban upgrade apply")
	return 0
}

func cmdUpgradeApply(args []string) int {
	fs := flag.NewFlagSet("upgrade apply", flag.ExitOnError)
	noActivate := fs.Bool("no-activate", false, "install the payload but don't restart into it")
	_ = fs.Parse(args)

	if runtime.GOOS != "darwin" {
		fmt.Fprintln(os.Stderr, "upgrade apply: self-upgrade is macOS-only — see docs/upgrade.md")
		return 1
	}
	if !requireRoot("upgrade apply") {
		return 1
	}

	stageDir := upgradeStageDir()
	pkgPath, err := stagedPkg(stageDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "upgrade apply:", err)
		return 1
	}

	stashDir := upgradeStashDir()
	// A stash outliving its upgrade is expected in more than the scary case.
	// Both DEFERRED paths below (--no-activate, and a gate that refused to
	// activate) finish successfully with the stash still on disk on purpose —
	// activation hasn't happened yet, so the rollback copy is still live. The
	// operator's documented next step there is `sudo dezhban restart`, which
	// has no idea the stash exists and never clears it. So the common reason
	// to land here is a perfectly healthy deferred upgrade, not a crash
	// mid-apply — say so, and name the way out, instead of pointing at the
	// docs and leaving `upgrade` wedged for someone who did nothing wrong.
	if update.HasStash(stashDir) {
		fmt.Fprintln(os.Stderr, "upgrade apply: a rollback stash from a previous upgrade is still present at", stashDir)
		fmt.Fprintln(os.Stderr, "               that is expected if the last apply deferred activation (--no-activate, or")
		fmt.Fprintln(os.Stderr, "               the gate refusing while FULL BLOCK / a switch window was up) and you have")
		fmt.Fprintln(os.Stderr, "               since activated it with 'sudo dezhban restart'.")
		fmt.Fprintln(os.Stderr, "               if the running version is the one you want, discard it and retry:")
		fmt.Fprintln(os.Stderr, "                 sudo rm -rf", stashDir)
		fmt.Fprintln(os.Stderr, "               otherwise see docs/upgrade.md for restoring from it by hand.")
		return 1
	}

	// Stash the CURRENT binary/app BEFORE installer runs — this has to happen
	// first, or there is nothing left to stash once the .pkg has overwritten
	// them.
	fmt.Println("stashing the current version for rollback...")
	if err := update.StashFile(stashDir, "/usr/local/bin/dezhban"); err != nil {
		fmt.Fprintln(os.Stderr, "upgrade apply: could not stash the current binary:", err)
		return 1
	}
	if err := update.StashDir(stashDir, "/Applications/Dezhban.app"); err != nil {
		fmt.Fprintln(os.Stderr, "upgrade apply: could not stash the current app:", err)
		_ = update.ClearStash(stashDir)
		return 1
	}

	// Phase 1: apply. Zero enforcement gap — installer(8) does not stop the
	// daemon, and replacing /usr/local/bin/dezhban on Unix leaves the running
	// process on its OLD inode. It keeps enforcing with the code it already
	// has loaded; only the files on disk change.
	fmt.Println("installing", pkgPath, "...")
	if out, err := exec.Command("installer", "-pkg", pkgPath, "-target", "/").CombinedOutput(); err != nil {
		fmt.Fprintln(os.Stderr, "upgrade apply: installer failed:", err)
		fmt.Fprintln(os.Stderr, string(out))
		// Keep the stash rather than assume nothing changed: the .pkg has two
		// components (cli, app) each with their own postinstall, so a failure
		// partway through is a partial-replace, not necessarily a no-op. Safer
		// to leave the pre-upgrade copy available for manual recovery than to
		// guess it wasn't needed.
		fmt.Fprintln(os.Stderr, "the pre-upgrade version is stashed at", stashDir, "in case manual recovery is needed")
		return 1
	}
	_ = os.RemoveAll(stageDir)
	fmt.Println("applied — the new binary and app are on disk; the running daemon has not been touched yet")

	// Retired keys must never be silently dropped (the same rule that keeps
	// vpn.enabled/failClosed/allowlist parsed-but-reported applies here): the
	// NEW binary is on disk now, so ask IT — not this process's own, possibly
	// older, validate — what it thinks of the existing config, before this
	// process goes anywhere near restarting into it. Matched against
	// cmdValidate's actual wording ("no longer has any effect"), not the word
	// "retired" — that word never appears in what it prints.
	if out, err := exec.Command("/usr/local/bin/dezhban", "validate", "--config", defaultConfigPath()).CombinedOutput(); err == nil {
		if strings.Contains(string(out), "no longer has any effect") {
			fmt.Println("--- the new version reports config keys that no longer have an effect ---")
			fmt.Print(string(out))
			fmt.Println("---------------------------------------------------------------------------")
		}
	}

	if *noActivate {
		fmt.Println("--no-activate: not restarting. Activate later with: sudo dezhban restart")
		return 0
	}

	version := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(pkgPath), "dezhban-"), ".pkg")
	return activate(stashDir, version)
}

// stagedPkg finds the single .pkg upgrade download left in dir.
func stagedPkg(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return "", fmt.Errorf("nothing staged — run 'sudo dezhban upgrade download' first")
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".pkg") {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("no .pkg found in %s", dir)
}

// slogCloser pairs a logger with the file handle backing it, so callers can
// flush/close explicitly instead of leaking the handle for the process's
// remaining lifetime (this runs inside a one-shot CLI command, not the
// long-lived daemon, which owns closing its own log handle differently).
type slogCloser struct {
	*slog.Logger
	fw *logging.FileWriter
}

func (s *slogCloser) Close() { _ = s.fw.Close() }

// upgradeLogger writes into the daemon's own persistent log file (rather than
// a separate one), so the activation window this opens is auditable in the
// same place every other enforcement event already is.
func upgradeLogger() (*slogCloser, error) {
	fw, err := logging.OpenFile(defaultLogPath())
	if err != nil {
		return nil, err
	}
	return &slogCloser{Logger: slog.New(logging.NewTextHandler("info", fw)), fw: fw}, nil
}

// activate performs the ONLY step that opens an enforcement gap: it re-checks
// the activation gate at this exact instant (not whatever it was at download
// time — a payload staged before FULL BLOCK engaged must not activate into
// it), discloses the window to the operator, restarts, and waits for a
// healthy snapshot. A restore-on-failure keeps the stash as the safety net;
// success clears it, since the stash only exists for this risk window.
func activate(stashDir, version string) int {
	gate := update.CanActivate(defaultStatePath())
	if !gate.OK {
		fmt.Println("applied, but NOT activated:", gate.Reason)
		fmt.Println("the previous version is still running normally. retry activation later with: sudo dezhban restart")
		fmt.Println("(the rollback stash is kept until activation actually succeeds)")
		return 0
	}

	log, err := upgradeLogger()
	if err != nil {
		// Non-fatal: durable audit logging is a nice-to-have, not a reason to
		// refuse an otherwise-safe activation. The stdout messages below still
		// disclose the window to whoever is running this.
		fmt.Fprintln(os.Stderr, "warning: could not open the log file for the activation audit trail:", err)
	}

	fmt.Println("activating: restarting the daemon into the new version.")
	fmt.Println("enforcement pauses for the duration of the restart — typically ~2s, up to 30s if teardown is slow.")
	if log != nil {
		log.Info("upgrade: activation window opening", "gateReason", gate.Reason, "posture", gate.Posture)
	}

	restartedAt := time.Now()
	if code := cmdRestart(nil); code != 0 {
		if log != nil {
			log.Warn("upgrade: restart into the new version failed; rolling back")
			log.Close()
		}
		fmt.Fprintln(os.Stderr, "upgrade apply: restart failed — rolling back to the previous version")
		return rollback(stashDir)
	}

	snap, healthy := waitForHealthySnapshot(defaultStatePath(), restartedAt, 30*time.Second)
	if log != nil {
		if healthy {
			log.Info("upgrade: activation window closed — new version healthy", "posture", snap.Posture)
		} else {
			log.Warn("upgrade: activation window closed — new version did not report healthy in time", "posture", snap.Posture, "enforcementErr", snap.EnforcementErr)
		}
		log.Close()
	}
	if !healthy {
		fmt.Fprintln(os.Stderr, "upgrade apply: the new version did not report healthy within 30s — rolling back")
		return rollback(stashDir)
	}

	if err := update.ClearStash(stashDir); err != nil {
		// Non-fatal: the upgrade succeeded, the stash is just left-over disk
		// that a future upgrade will refuse to run over (HasStash's own
		// check) until it's cleared by hand — surfaced, not silently ignored.
		fmt.Fprintln(os.Stderr, "warning: upgrade succeeded but could not clear the rollback stash:", err)
	}
	fmt.Printf("upgrade complete — now running v%s (posture: %s)\n", version, snap.Posture)
	return 0
}

// rollback restores the stashed pre-upgrade binary/app and restarts back into
// it. Best-effort by design: if THIS also fails, the operator is already
// being told loudly, and `dezhban panic` remains the escape hatch regardless
// of which version is on disk.
func rollback(stashDir string) int {
	fmt.Println("restoring the previous version...")
	if err := update.RestoreFile(stashDir, "dezhban", "/usr/local/bin/dezhban"); err != nil {
		fmt.Fprintln(os.Stderr, "rollback: could not restore the binary:", err)
		fmt.Fprintln(os.Stderr, "the stash is kept at", stashDir, "— restore it by hand, or reinstall")
		return 1
	}
	if err := update.RestoreDir(stashDir, "Dezhban.app", "/Applications/Dezhban.app"); err != nil {
		fmt.Fprintln(os.Stderr, "rollback: could not restore the app:", err)
	}
	if code := cmdRestart(nil); code != 0 {
		fmt.Fprintln(os.Stderr, "rollback: the restored version also failed to restart cleanly — run 'dezhban panic' and investigate")
		return 1
	}
	fmt.Println("rolled back — the previous version is running again")
	return 1 // the upgrade itself still failed; report non-zero even though rollback succeeded
}

// waitForHealthySnapshot polls the state file for a snapshot published AFTER
// `after` (so a stale pre-restart snapshot can never read as proof the NEW
// process is healthy) that is neither the terminal "stopped" posture nor
// carrying an EnforcementErr — the same "a set EnforcementErr means posture
// was not actually achieved" rule state.Snapshot's own doc comment states.
func waitForHealthySnapshot(path string, after time.Time, budget time.Duration) (state.Snapshot, bool) {
	deadline := time.Now().Add(budget)
	var last state.Snapshot
	for {
		if snap, err := state.Read(path); err == nil {
			last = snap
			if snap.Time.After(after) && snap.Posture != "stopped" && snap.EnforcementErr == "" {
				return snap, true
			}
		}
		if time.Now().After(deadline) {
			return last, false
		}
		time.Sleep(500 * time.Millisecond)
	}
}
