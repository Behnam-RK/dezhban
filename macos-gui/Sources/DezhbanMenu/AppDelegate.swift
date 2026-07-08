import AppKit

/// AppDelegate owns the menubar item. A 1-second timer reads the daemon's state
/// file (a tiny local read — no geo-API polling from the GUI) and repaints the
/// icon; the dropdown menu is rebuilt from the current snapshot each time it opens.
final class AppDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate {
    private var statusItem: NSStatusItem!
    private let menu = NSMenu()
    private var timer: Timer?
    private var snapshot: Snapshot?
    private var lastMtime: Date?
    private var lastIconKey: String?

    /// Cached result of `DezhbanCLI.serviceInstalled()` (which shells out to
    /// `status --json`). `menuNeedsUpdate` reads this synchronously so opening
    /// the menubar menu never blocks on a subprocess; it's refreshed off the
    /// main thread at launch, after install/uninstall, and on each menu open
    /// (for the next open).
    private var serviceIsInstalled = false

    /// Floor for the staleness threshold. The daemon's actual poll cadence — carried
    /// in the snapshot as `pollIntervalSeconds` — scales this up (see staleThreshold),
    /// so a deliberately long pollInterval doesn't make an enforcing daemon read as
    /// "stopped" between polls. 90s tolerates a couple of missed default-cadence cycles.
    private let staleFloor: TimeInterval = 90

    func applicationDidFinishLaunching(_ notification: Notification) {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        menu.delegate = self
        // We compute item enablement ourselves (see addAction); without this, AppKit's
        // automatic validation force-enables any item whose target responds to its
        // selector, so the gating on "Block now"/"Start" etc. would be ignored.
        menu.autoenablesItems = false
        statusItem.menu = menu
        refresh()
        refreshServiceInstalled()
        timer = Timer.scheduledTimer(withTimeInterval: 1.0, repeats: true) { [weak self] _ in
            self?.refresh()
        }
    }

    // MARK: - state → icon

    /// Refreshes the menubar icon on the 1s timer. Re-decodes the state file only
    /// when it actually changed (the daemon rewrites it ~every 30s), but still
    /// recomputes the icon each tick so staleness can flip it to gray from the
    /// cached snapshot. Repaints the button only when the icon actually differs,
    /// avoiding an NSImage allocation every second.
    private func refresh() {
        let mtime = StateReader.modificationTime()
        if mtime != lastMtime {
            lastMtime = mtime
            snapshot = StateReader.read()
        }
        guard let button = statusItem.button else { return }
        let (symbol, color, help) = iconFor(snapshot)
        let key = "\(symbol)|\(help)"
        guard key != lastIconKey else { return }
        lastIconKey = key
        let image = NSImage(systemSymbolName: symbol, accessibilityDescription: "dezhban: \(help)")
        image?.isTemplate = true
        button.image = image
        button.contentTintColor = color
        button.toolTip = "dezhban — \(help)"
    }

    /// Recomputes `serviceIsInstalled` off the main thread. Skips the subprocess
    /// entirely when the CLI is absent (nothing to install a service from).
    private func refreshServiceInstalled() {
        guard DezhbanCLI.binaryPath() != nil else {
            serviceIsInstalled = false
            return
        }
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let installed = DezhbanCLI.serviceInstalled()
            DispatchQueue.main.async { self?.serviceIsInstalled = installed }
        }
    }

    /// The age past which a snapshot reads as stopped, derived from the daemon's own
    /// poll cadence (3× the interval) so it scales with a custom pollInterval, floored
    /// at staleFloor. Falls back to the floor when the field is absent (older daemon).
    private func staleThreshold(_ s: Snapshot) -> TimeInterval {
        guard let p = s.pollIntervalSeconds, p > 0 else { return staleFloor }
        return max(staleFloor, TimeInterval(p) * 3)
    }

    /// Whether a snapshot represents a live, enforcing daemon (fresh and not
    /// stopped). Single source of truth for both the icon and the menu's gating.
    private func isLive(_ s: Snapshot?) -> Bool {
        guard let s = s else { return false }
        return s.age <= staleThreshold(s) && s.posture != "stopped"
    }

    /// Maps a snapshot (or its absence/staleness) to an SF Symbol + tint + label.
    private func iconFor(_ s: Snapshot?) -> (symbol: String, color: NSColor, help: String) {
        guard let s = s, isLive(s) else {
            return ("shield", .systemGray, "stopped")
        }
        // A failed firewall action means the intended posture was NOT achieved (e.g. a
        // failed block leaves posture "allow" during a live leak). Surface it as a
        // warning regardless of posture so a green shield never masks a failed enforce.
        if let e = s.enforcementErr, !e.isEmpty {
            return ("exclamationmark.triangle.fill", .systemRed, "enforcement error")
        }
        switch s.posture {
        case "block", "full-block":
            return ("shield.slash.fill", .systemRed, humanPosture(s))
        case "switch-window":
            // The switch window relaxes egress (all outbound, or a proto/port subset
            // if restricted) — the real IP may be exposed. Never show a green "safe"
            // shield here; warn so the user notices it's open.
            return ("exclamationmark.shield.fill", .systemYellow, humanPosture(s))
        default: // allow, guard
            return ("shield.fill", .systemGreen, humanPosture(s))
        }
    }

    private func humanPosture(_ s: Snapshot) -> String {
        switch s.posture {
        case "allow": return "allowing"
        case "block": return "blocking"
        case "guard": return "guarding (VPN)"
        case "full-block": return "full block (VPN)"
        case "switch-window": return "switch window — egress relaxed (real IP may be exposed)"
        case "stopped": return "stopped"
        default: return s.posture
        }
    }

    private var isRunning: Bool { isLive(snapshot) }

    // MARK: - menu

    func menuNeedsUpdate(_ menu: NSMenu) {
        menu.removeAllItems()
        let s = snapshot

        // Status header (disabled, informational).
        if let s = s, isRunning {
            addInfo("Status: \(humanPosture(s).capitalized)")
            if let ip = s.ip, !ip.isEmpty {
                let cc = s.countryCode ?? "??"
                let prov = s.provider.map { " via \($0)" } ?? ""
                addInfo("IP: \(ip) (\(cc)\(prov))")
            } else if let err = s.lookupErr, !err.isEmpty {
                addInfo("Last lookup failed: \(err)")
            }
            if let e = s.enforcementErr, !e.isEmpty {
                addInfo("⚠︎ Enforcement failed: \(e)")
            }
            addInfo("Mode: \(s.mode == "vpn" ? "VPN guard" : "legacy")")
            if s.mode == "vpn" {
                if let tuns = s.tunnels, let t = tuns.first {
                    addInfo("Tunnel: \(t.up ? "up" : "down")\(t.detail.map { " (\($0))" } ?? "")")
                }
                if let eps = s.endpoints, !eps.isEmpty {
                    addInfo("Endpoints: \(eps.joined(separator: ", "))")
                }
                if let p = s.activeProfile, !p.isEmpty {
                    addInfo("VPN: \(p)")
                }
                if let sw = s.switch, sw.open {
                    addInfo("⏳ Switch window OPEN until \(shortTime(sw.until))")
                }
            }
            if let bc = s.blockedCountries, !bc.isEmpty {
                addInfo("Blocking: \(bc.joined(separator: ", "))")
            }
            addInfo("Updated \(agoString(s.age))")
        } else {
            addInfo(DezhbanCLI.binaryPath() == nil
                ? "dezhban CLI not found — install it first"
                : "Kill switch stopped")
        }

        menu.addItem(.separator())

        // Service control.
        if isRunning {
            addAction("Stop kill switch", #selector(stopService))
        } else {
            addAction("Start kill switch", #selector(startService),
                      enabled: DezhbanCLI.binaryPath() != nil)
        }

        // Manual block / unblock, gated on current posture.
        let blocked = s?.blocked ?? false
        addAction("Block now", #selector(blockNow), enabled: isRunning && !blocked)
        addAction("Unblock", #selector(unblockNow), enabled: isRunning && blocked)

        // Switch window: connect a brand-new VPN whose server isn't known yet.
        if s?.mode == "vpn" {
            if let sw = s?.switch, sw.open {
                let left = max(0, sw.until.timeIntervalSinceNow)
                addAction("Cancel VPN switch (\(mmss(left)) left)", #selector(cancelSwitch),
                          enabled: isRunning)
            } else {
                addAction("Switching VPN…", #selector(openSwitch), enabled: isRunning)
            }
        }

        menu.addItem(.separator())

        // Diagnostics / panic / install-uninstall (Phase 10). All reuse the
        // output-capture plumbing in DezhbanCLI + OutputPanel.
        addAction("Run diagnostics…", #selector(runDiagnostics), enabled: DezhbanCLI.binaryPath() != nil)
        addAction("Panic — force unblock…", #selector(confirmPanic), enabled: DezhbanCLI.binaryPath() != nil)
        if serviceIsInstalled {
            addAction("Uninstall service…", #selector(uninstallService))
        } else {
            addAction("Install service…", #selector(installService), enabled: DezhbanCLI.binaryPath() != nil)
        }
        // Keep the cache honest for the next open without blocking this one.
        refreshServiceInstalled()

        menu.addItem(.separator())

        // VPN mode: show current mode with a checkmark; opens the validated
        // in-app config panel (Phase 11) rather than a blind file edit.
        let vpnItem = addAction("VPN guard mode", #selector(openVPNConfigPanel))
        vpnItem.state = (s?.mode == "vpn") ? .on : .off
        vpnItem.toolTip = "Configure vpn.enabled + tunnels/endpoints, then apply (restarts dezhban)."

        addAction("Open config file…", #selector(openConfig))
        addLogsMenu()

        menu.addItem(.separator())
        addAction("About Dezhban…", #selector(showAbout))

        menu.addItem(.separator())

        let login = addAction("Launch at login", #selector(toggleLogin))
        login.state = LoginItem.isEnabled ? .on : .off

        menu.addItem(.separator())
        addAction("Quit", #selector(quit))
    }

    /// Builds the "View logs…" submenu: a scoped `log show`/`log stream`
    /// against the output panel (no hand-written predicate for the user to
    /// type), plus the old full-app Console.app escape hatch.
    private func addLogsMenu() {
        let parent = NSMenuItem(title: "View logs…", action: nil, keyEquivalent: "")
        let sub = NSMenu()

        // These run `/usr/bin/log` directly and don't need the dezhban binary,
        // so keep them available even when the CLI is uninstalled/mislocated —
        // reading the unified log is exactly the diagnostic you want then.
        let showItem = NSMenuItem(title: "Show last hour", action: #selector(showRecentLogs), keyEquivalent: "")
        showItem.target = self
        sub.addItem(showItem)

        let streamItem = NSMenuItem(title: "Stream live…", action: #selector(streamLogs), keyEquivalent: "")
        streamItem.target = self
        sub.addItem(streamItem)

        sub.addItem(.separator())

        let consoleItem = NSMenuItem(title: "Open in Console.app", action: #selector(openConsole), keyEquivalent: "")
        consoleItem.target = self
        sub.addItem(consoleItem)

        parent.submenu = sub
        menu.addItem(parent)
    }

    // MARK: - actions

    @objc private func startService() { runAction(["start"], "start the kill switch") }
    @objc private func stopService() { runAction(["stop"], "stop the kill switch") }
    @objc private func blockNow() { runAction(["block"], "block") }
    @objc private func unblockNow() { runAction(["unblock"], "unblock") }
    @objc private func openSwitch() { runAction(["switch", "--no-wait"], "open a switch window") }
    @objc private func cancelSwitch() { runAction(["switch", "--cancel"], "cancel the switch window") }

    /// Runs a privileged CLI action OFF the main thread — `runPrivileged` blocks
    /// through the admin-password prompt and the command's full run, which would
    /// otherwise freeze the menubar. Refreshes and surfaces failures back on the main
    /// queue; a cancelled prompt or a non-zero exit is reported (with real output)
    /// instead of swallowed.
    private func runAction(_ args: [String], _ label: String) {
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = DezhbanCLI.runPrivileged(args)
            DispatchQueue.main.async {
                if !result.ok { self?.notifyFailure(label, output: result.output) }
                self?.refresh()
            }
        }
    }

    /// Failure alert with the captured output in a small scrollable accessory
    /// view when there is any — real stderr/exit info instead of silence.
    private func notifyFailure(_ label: String, output: String) {
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = "dezhban: couldn’t \(label)"
        alert.informativeText = "The command failed or was cancelled. If it needs elevated rights, approve the admin prompt."
        let trimmed = output.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmed.isEmpty {
            alert.accessoryView = makeOutputAccessory(trimmed)
        }
        alert.runModal()
    }

    /// A small scrollable, monospaced text view for embedding captured CLI
    /// output in an NSAlert (short-output case; the shared OutputPanel handles
    /// the longer/unbounded cases like diagnostics and log streaming).
    private func makeOutputAccessory(_ text: String) -> NSView {
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

    /// Unprivileged, read-only `doctor` run → shared output panel.
    @objc private func runDiagnostics() {
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = DezhbanCLI.run(["doctor", "--config", DezhbanCLI.resolvedConfigPath()])
            DispatchQueue.main.async {
                OutputPanel.shared.show(title: "dezhban — diagnostics", text: result.output.isEmpty ? "(no output)" : result.output)
                self?.refresh()
            }
        }
    }

    /// Panic is the last-resort override, so — unlike Block/Unblock — it asks
    /// for confirmation before tearing down every dezhban rule.
    @objc private func confirmPanic() {
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = "Force unblock all firewall rules?"
        alert.informativeText = "This immediately removes all dezhban firewall rules, including VPN-guard rules. Continue?"
        alert.addButton(withTitle: "Panic — Force Unblock")
        alert.addButton(withTitle: "Cancel")
        guard alert.runModal() == .alertFirstButtonReturn else { return }
        runCapturedPrivileged(["panic"], title: "dezhban — panic")
    }

    /// Runs one privileged command and shows its captured output in the shared
    /// panel — the single-command counterpart to `runSequence` below.
    private func runCapturedPrivileged(_ args: [String], title: String) {
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = DezhbanCLI.runPrivileged(args)
            DispatchQueue.main.async {
                OutputPanel.shared.show(title: title, text: result.output.isEmpty ? "(no output)" : result.output)
                self?.refresh()
            }
        }
    }

    /// Mirrors `install-local.sh`'s ordering: rules teardown (`panic`) and
    /// `stop` before `uninstall`, since a launchd unload can leave stale rules
    /// behind if they aren't torn down first.
    @objc private func installService() {
        runSequence([["install", "--config", DezhbanCLI.resolvedConfigPath()], ["start"]], title: "dezhban — install service")
    }

    @objc private func uninstallService() {
        runSequence([["panic"], ["stop"], ["uninstall"]], title: "dezhban — uninstall service")
    }

    /// Runs a sequence of privileged commands, stopping at (and reporting) the
    /// first failure rather than plowing ahead once something's gone wrong.
    private func runSequence(_ commands: [[String]], title: String) {
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            var log = ""
            for cmd in commands {
                let result = DezhbanCLI.runPrivileged(cmd)
                log += "$ dezhban \(cmd.joined(separator: " "))\n\(result.output)\n\n"
                if !result.ok { break }
            }
            DispatchQueue.main.async {
                OutputPanel.shared.show(title: title, text: log)
                self?.refresh()
                // install/uninstall flips service-installed state — resync the cache.
                self?.refreshServiceInstalled()
            }
        }
    }

    /// Real-data About panel: version, resolved config path, binary path, and
    /// current service status from the last-read snapshot — no new CLI calls
    /// beyond `version` (everything else is already fetched for the main menu).
    /// Reuses the shared output panel rather than a bespoke About window.
    @objc private func showAbout() {
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let version = DezhbanCLI.run(["version"]).output.trimmingCharacters(in: .whitespacesAndNewlines)
            let cfgPath = DezhbanCLI.resolvedConfigPath()
            let binPath = DezhbanCLI.binaryPath() ?? "(not found — install it first)"
            DispatchQueue.main.async {
                guard let self = self else { return }
                let status = self.isRunning ? self.humanPosture(self.snapshot!) : "stopped"
                let text = """
                \(version.isEmpty ? "dezhban (version unknown)" : version)

                Config path:     \(cfgPath)
                Binary path:     \(binPath)
                Service status:  \(status)
                """
                OutputPanel.shared.show(title: "About Dezhban", text: text)
            }
        }
    }

    @objc private func openConfig() {
        NSWorkspace.shared.open(URL(fileURLWithPath: DezhbanCLI.resolvedConfigPath()))
    }

    @objc private func openVPNConfigPanel() {
        VPNConfigPanel.shared.open()
    }

    @objc private func showRecentLogs() {
        DispatchQueue.global(qos: .userInitiated).async {
            let result = DezhbanCLI.showRecentLogs()
            DispatchQueue.main.async {
                OutputPanel.shared.show(title: "dezhban — logs (last hour)", text: result.output.isEmpty ? "(no matching log lines)" : result.output)
            }
        }
    }

    private var activeLogStream: StreamingProcess?

    @objc private func streamLogs() {
        let proc = StreamingProcess(DezhbanCLI.logBinary, DezhbanCLI.streamLogsArgs)
        activeLogStream = proc
        OutputPanel.shared.showStreaming(title: "dezhban — live logs") { [weak self] in
            proc.stop()
            self?.activeLogStream = nil
        }
        if !proc.start(onOutput: { text in OutputPanel.shared.append(text) }) {
            // Revert to a non-streaming panel: `show` hides the Stop button and
            // clears the onStop handler (via stopPreviousStream), so neither
            // lingers with no process behind it.
            activeLogStream = nil
            OutputPanel.shared.show(title: "dezhban — live logs", text: "failed to start log stream\n")
        }
    }

    @objc private func openConsole() {
        NSWorkspace.shared.open(URL(fileURLWithPath: "/System/Applications/Utilities/Console.app"))
    }

    @objc private func toggleLogin() { LoginItem.toggle() }

    @objc private func quit() { NSApp.terminate(nil) }

    // MARK: - menu builders

    @discardableResult
    private func addAction(_ title: String, _ sel: Selector, enabled: Bool = true) -> NSMenuItem {
        let item = NSMenuItem(title: title, action: sel, keyEquivalent: "")
        item.target = self
        item.isEnabled = enabled
        menu.addItem(item)
        return item
    }

    private func addInfo(_ title: String) {
        let item = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        item.isEnabled = false
        menu.addItem(item)
    }

    private func agoString(_ seconds: TimeInterval) -> String {
        let s = Int(seconds.rounded())
        if s < 60 { return "\(s)s ago" }
        return "\(s / 60)m ago"
    }

    private func mmss(_ seconds: TimeInterval) -> String {
        // Round DOWN so the countdown never shows more time than is actually left
        // (e.g. 59.6s reads "0:59", not "1:00"). For this switch-window exposure
        // timer, under-stating the remaining time is the safe direction.
        let s = max(0, Int(seconds.rounded(.down)))
        return String(format: "%d:%02d", s / 60, s % 60)
    }

    private static let shortTimeFormatter: DateFormatter = {
        let f = DateFormatter()
        f.timeStyle = .short
        return f
    }()

    private func shortTime(_ t: Date) -> String {
        AppDelegate.shortTimeFormatter.string(from: t)
    }
}
