import AppKit

/// Action plumbing shared by the menubar menu and the main window, so both
/// surfaces run identical semantics — one place owns the escalation rules.
///
/// Three tiers, mirroring DezhbanCLI's:
///   - `routine` — block/unblock/switch: control-socket-first, root fallback
///     ONLY when no daemon is listening, and a daemon REFUSAL is never escalated.
///   - `privileged` — start/stop: straight to the admin prompt (a daemon can't
///     manage its own lifecycle).
///   - `capturedPrivileged` / `capturedSequence` — panic and install/uninstall:
///     privileged with the full transcript handed back for display.
enum AppActions {
    /// Wired by AppDelegate at launch: nudges the 1s refresh immediately after an
    /// action completes instead of waiting out the current tick.
    static var refresh: () -> Void = {}

    /// Runs a routine posture op: unprivileged first, so a running daemon handles it
    /// over the control socket with no password — the normal path.
    ///
    /// Escalation is a fallback for exactly one case: no daemon is listening (service
    /// stopped, or control disabled), where the command must act on the firewall
    /// directly and that needs root. A daemon REFUSAL is never escalated — the daemon
    /// gates these ops deliberately (e.g. refusing a block while a switch window is
    /// open), and re-running as root would route around a decision, not an obstacle.
    static func routine(_ args: [String], _ label: String) {
        DispatchQueue.global(qos: .userInitiated).async {
            var result = DezhbanCLI.runRoutine(args)
            if !result.ok && !result.refused {
                result = DezhbanCLI.runPrivileged(args)
            }
            DispatchQueue.main.async {
                if !result.ok { failureAlert(label, output: result.output) }
                refresh()
            }
        }
    }

    /// Runs a privileged CLI action OFF the main thread — `runPrivileged` blocks
    /// through the admin-password prompt and the command's full run, which would
    /// otherwise freeze the UI. A cancelled prompt or a non-zero exit is reported
    /// (with real output) instead of swallowed.
    static func privileged(_ args: [String], _ label: String) {
        DispatchQueue.global(qos: .userInitiated).async {
            let result = DezhbanCLI.runPrivileged(args)
            DispatchQueue.main.async {
                if !result.ok { failureAlert(label, output: result.output) }
                refresh()
            }
        }
    }

    /// Runs one privileged command and hands the transcript to `present` on the
    /// main queue (menubar panic → alert; window panic → Logs pane).
    static func capturedPrivileged(_ args: [String], present: @escaping (CommandResult) -> Void) {
        DispatchQueue.global(qos: .userInitiated).async {
            let result = DezhbanCLI.runPrivileged(args)
            DispatchQueue.main.async {
                present(result)
                refresh()
            }
        }
    }

    /// Runs a sequence of privileged commands under ONE admin prompt, stopping at
    /// (and reporting) the first failure rather than plowing ahead once something's
    /// gone wrong. Elevating per command would make a three-step uninstall ask for
    /// the password three times. Resyncs the service-installed cache afterwards.
    ///
    /// `commands` is an autoclosure so that building it — `installCommands` resolves
    /// the config path, which shells out — happens on the background queue instead of
    /// on the caller's main thread. See DezhbanCLI.exec on why that distinction is a
    /// correctness one, not a responsiveness one.
    static func capturedSequence(_ commands: @escaping @autoclosure () -> [[String]],
                                 present: @escaping (CommandResult) -> Void) {
        DispatchQueue.global(qos: .userInitiated).async {
            let result = DezhbanCLI.runPrivileged(batch: commands())
            DispatchQueue.main.async {
                present(result)
                refresh()
                AppState.shared.refreshServiceState()
            }
        }
    }

    /// Mirrors `install-local.sh`'s ordering for uninstall: rules teardown
    /// (`panic`) and `stop` before `uninstall`, since a launchd unload can leave
    /// stale rules behind if they aren't torn down first.
    ///
    /// Evaluate this OFF the main thread — it resolves the config path, which shells
    /// out. `capturedSequence` takes its commands as an autoclosure for exactly this
    /// reason, so call sites can pass it without forcing it early.
    static var installCommands: [[String]] {
        [["install", "--config", DezhbanCLI.resolvedConfigPath()], ["start"]]
    }

    static var uninstallCommands: [[String]] {
        [["panic"], ["stop"], ["uninstall"]]
    }

    /// Where the installer leaves the uninstaller matching the installed
    /// version. Both `scripts/install.sh` and the `.pkg` put it here.
    static let uninstallerPath = "/usr/local/share/dezhban/uninstall.sh"

    /// The exact command that removes everything root owns. Shown to the user
    /// verbatim when Terminal cannot be opened, so it has to be copy-pasteable
    /// as printed.
    static func uninstallerCommand(keepConfig: Bool) -> String {
        let prefix = keepConfig ? "sudo KEEP_CONFIG=1 sh " : "sudo sh "
        return prefix + uninstallerPath
    }

    /// Opens Terminal.app running the root uninstaller. Reports whether Terminal
    /// actually took the command.
    ///
    /// Terminal rather than an in-app privileged sequence, because the script
    /// quits this app and deletes its bundle partway through — see
    /// `SettingsView.uninstallEverything`. The user types their password into
    /// `sudo` in a window they own, and watches the `panic` teardown land.
    ///
    /// The command is assembled from constants only, never from user input, so
    /// there is nothing here to quote-escape. Keep it that way: this string is
    /// executed as root.
    @discardableResult
    static func openUninstallerInTerminal(keepConfig: Bool) -> Bool {
        guard FileManager.default.fileExists(atPath: uninstallerPath) else {
            return false
        }
        let command = uninstallerCommand(keepConfig: keepConfig)
        let script = """
            tell application "Terminal"
                activate
                do script "\(command)"
            end tell
            """
        guard let apple = NSAppleScript(source: script) else { return false }
        var err: NSDictionary?
        apple.executeAndReturnError(&err)
        if let err = err {
            NSLog("DezhbanMenu: could not start the uninstaller in Terminal: %@", err)
            return false
        }
        return true
    }

    /// download then apply, under ONE admin prompt — same reasoning as
    /// installCommands: the prompt is the expensive thing, and these two
    /// steps are meaningless run apart (apply has nothing staged without
    /// download just having run).
    static var upgradeCommands: [[String]] {
        [["upgrade", "download"], ["upgrade", "apply"]]
    }

    /// Downloads and applies the staged .pkg, then relaunches the app — it
    /// was just replaced on disk out from under this running process (`upgrade
    /// apply` installs a new Dezhban.app regardless of whether daemon
    /// activation happened this instant or was deferred by the gate; see
    /// docs/usage/upgrade.md). `present` gets the full transcript either way, so a
    /// failure — or a deferred activation, which is still `ok` — is always
    /// visible, not silently swallowed by the relaunch.
    static func performUpgrade(present: @escaping (CommandResult) -> Void) {
        capturedSequence(upgradeCommands) { result in
            present(result)
            if result.ok {
                relaunch()
            }
        }
    }

    /// Relaunches Dezhban.app: spawns a detached watcher that waits for THIS
    /// process to actually exit, then opens the (just-replaced) app bundle
    /// fresh, and terminates this instance. Simpler and more robust than any
    /// in-process "restart" trick — this process's own code is the OLD
    /// version now sitting on an unlinked inode; the only clean way back to
    /// the new one is a fresh process launched from the new bundle on disk.
    private static func relaunch() {
        let pid = ProcessInfo.processInfo.processIdentifier
        let task = Process()
        task.executableURL = URL(fileURLWithPath: "/bin/sh")
        task.arguments = [
            "-c",
            "while kill -0 \(pid) 2>/dev/null; do sleep 0.2; done; open '/Applications/Dezhban.app'",
        ]
        try? task.run() // best-effort: if this fails, the user still has the new .pkg on disk and can reopen the app by hand
        NSApp.terminate(nil)
    }

    /// Confirms before an upgrade: it restarts this app unconditionally and
    /// may briefly restart enforcement (only if the daemon is in a safe
    /// posture — see docs/usage/upgrade.md), so it deserves the same "are you sure"
    /// treatment as Panic, not a silent one-click action.
    static func confirmUpgrade(to version: String) -> Bool {
        let alert = NSAlert()
        alert.alertStyle = .informational
        alert.messageText = "Download and install v\(version)?"
        alert.informativeText = "This restarts the app. If dezhban is in a safe posture (guard or standby), it also briefly restarts enforcement to activate the new version — never during FULL BLOCK or an open switch window."
        alert.addButton(withTitle: "Upgrade")
        alert.addButton(withTitle: "Cancel")
        return alert.runModal() == .alertFirstButtonReturn
    }

    /// Confirms an explicit "Restart dezhban" from Settings. A restart always
    /// briefly stops enforcement while the service comes back up — see
    /// ConfigApply.restartNow — so the wording is stronger exactly when that gap
    /// matters: during FULL BLOCK or an open switch/redial/pause window, lifting
    /// enforcement (even briefly) is the one thing this tool exists to prevent,
    /// so `exposedNow` switches to a `.critical` alert that says so plainly
    /// rather than a generic "are you sure?". This deliberately does NOT reuse
    /// `update.CanActivate` (Go): that gate stops the *updater* from silently
    /// restarting through a block; an operator explicitly asking is a different
    /// act, and refusing it outright would remove a recovery tool rather than
    /// just asking them to confirm the exposure.
    ///
    /// `unsavedEdits` names the other thing a restart does NOT do: a restart is
    /// a lifecycle action, not a write, so pending edits in the Settings pane
    /// survive in the fields and stay unapplied. Saying so is the difference
    /// between "the restart ignored my change" reading as a bug and reading as
    /// what it is — the pane's own confirmation for applying a preset names the
    /// same state for the same reason.
    static func confirmRestart(exposedNow: Bool, unsavedEdits: Bool = false) -> Bool {
        let alert = NSAlert()
        alert.alertStyle = exposedNow ? .critical : .warning
        alert.messageText = "Restart dezhban?"
        var text = exposedNow
            ? "dezhban is currently blocking traffic or running an open window. Restarting briefly stops "
                + "enforcement — your real IP may be exposed for a few seconds while the service comes back up."
            : "This briefly stops network filtering, usually for under a few seconds, while the service restarts."
        if unsavedEdits {
            text += "\n\nYou have unsaved changes in this pane. Restarting doesn't save or apply them — "
                + "they'll still be here, still unapplied, afterwards. Use Apply for that."
        }
        alert.informativeText = text
        alert.addButton(withTitle: "Restart")
        alert.addButton(withTitle: "Cancel")
        return alert.runModal() == .alertFirstButtonReturn
    }

    /// Panic is the last-resort override, so — unlike Block/Unblock — it asks
    /// for confirmation before tearing down every dezhban rule.
    static func confirmPanic() -> Bool {
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = "Force unblock all firewall rules?"
        alert.informativeText = "This immediately removes all dezhban firewall rules, including VPN-guard rules. Continue?"
        alert.addButton(withTitle: "Panic — Force Unblock")
        alert.addButton(withTitle: "Cancel")
        return alert.runModal() == .alertFirstButtonReturn
    }

    /// Failure alert with the captured output in a small scrollable accessory
    /// view when there is any — real stderr/exit info instead of silence.
    static func failureAlert(_ label: String, output: String) {
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = "dezhban: couldn’t \(label)"
        alert.informativeText = "The command failed or was cancelled. If it needs elevated rights, approve the admin prompt."
        let trimmed = output.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmed.isEmpty {
            alert.accessoryView = outputAccessory(trimmed)
        }
        alert.runModal()
    }

    /// Result alert carrying a full transcript — the menubar panic path, which
    /// must not depend on the main window being openable.
    static func outputAlert(title: String, ok: Bool, output: String) {
        let alert = NSAlert()
        alert.alertStyle = ok ? .informational : .warning
        alert.messageText = title
        let trimmed = output.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty {
            alert.informativeText = "(no output)"
        } else {
            alert.accessoryView = outputAccessory(trimmed)
        }
        alert.runModal()
    }

    /// A small scrollable, monospaced text view for embedding captured CLI
    /// output in an NSAlert.
    private static func outputAccessory(_ text: String) -> NSView {
        let scroll = NSScrollView(frame: NSRect(x: 0, y: 0, width: 420, height: 140))
        scroll.hasVerticalScroller = true
        scroll.borderType = .bezelBorder
        let tv = NSTextView(frame: scroll.bounds)
        tv.isEditable = false
        tv.font = NSFont.monospacedSystemFont(ofSize: 11, weight: .regular)
        tv.string = text
        scroll.documentView = tv
        return scroll
    }
}
