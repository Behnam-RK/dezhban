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

        // VPN mode: show current mode with a checkmark; live toggling needs
        // tunnel/endpoint config the user must set deliberately, so the action
        // opens the config rather than risk a validation failure that stops the
        // daemon (see plan §B5).
        let vpnItem = addAction("VPN guard mode", #selector(openConfig))
        vpnItem.state = (s?.mode == "vpn") ? .on : .off
        vpnItem.toolTip = "Edit vpn.enabled + tunnels/endpoints in the config, then restart."

        addAction("Open config…", #selector(openConfig))
        addAction("View logs…", #selector(viewLogs))

        menu.addItem(.separator())

        let login = addAction("Launch at login", #selector(toggleLogin))
        login.state = LoginItem.isEnabled ? .on : .off

        menu.addItem(.separator())
        addAction("Quit", #selector(quit))
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
    /// queue; a cancelled prompt or a non-zero exit is reported instead of swallowed.
    private func runAction(_ args: [String], _ label: String) {
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let ok = DezhbanCLI.runPrivileged(args)
            DispatchQueue.main.async {
                if !ok { self?.notifyFailure(label) }
                self?.refresh()
            }
        }
    }

    private func notifyFailure(_ label: String) {
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = "dezhban: couldn’t \(label)"
        alert.informativeText = "The command failed or was cancelled. If it needs elevated rights, approve the admin prompt; otherwise check Console.app for details."
        alert.runModal()
    }

    @objc private func openConfig() {
        NSWorkspace.shared.open(URL(fileURLWithPath: DezhbanCLI.resolvedConfigPath()))
    }

    @objc private func viewLogs() {
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
        let s = Int(seconds.rounded())
        return String(format: "%d:%02d", s / 60, s % 60)
    }

    private func shortTime(_ t: Date) -> String {
        let f = DateFormatter()
        f.timeStyle = .short
        return f.string(from: t)
    }
}
