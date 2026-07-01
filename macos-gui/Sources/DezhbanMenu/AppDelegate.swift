import AppKit

/// AppDelegate owns the menubar item. A 1-second timer reads the daemon's state
/// file (a tiny local read — no geo-API polling from the GUI) and repaints the
/// icon; the dropdown menu is rebuilt from the current snapshot each time it opens.
final class AppDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate {
    private var statusItem: NSStatusItem!
    private let menu = NSMenu()
    private var timer: Timer?
    private var snapshot: Snapshot?

    /// A snapshot older than this reads as stopped/unknown. The daemon publishes
    /// every poll (default 30s), so 90s tolerates a couple of missed cycles.
    private let staleAfter: TimeInterval = 90

    func applicationDidFinishLaunching(_ notification: Notification) {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        menu.delegate = self
        statusItem.menu = menu
        refresh()
        timer = Timer.scheduledTimer(withTimeInterval: 1.0, repeats: true) { [weak self] _ in
            self?.refresh()
        }
    }

    // MARK: - state → icon

    /// Reads the latest snapshot and updates the menubar icon. Cheap enough to run
    /// every second.
    private func refresh() {
        snapshot = StateReader.read()
        guard let button = statusItem.button else { return }
        let (symbol, color, help) = iconFor(snapshot)
        let image = NSImage(systemSymbolName: symbol, accessibilityDescription: "dezhban: \(help)")
        image?.isTemplate = true
        button.image = image
        button.contentTintColor = color
        button.toolTip = "dezhban — \(help)"
    }

    /// Maps a snapshot (or its absence/staleness) to an SF Symbol + tint + label.
    private func iconFor(_ s: Snapshot?) -> (symbol: String, color: NSColor, help: String) {
        guard let s = s, s.age <= staleAfter, s.posture != "stopped" else {
            return ("shield", .systemGray, "stopped")
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

    private var isRunning: Bool {
        guard let s = snapshot else { return false }
        return s.age <= staleAfter && s.posture != "stopped"
    }

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
            addInfo("Mode: \(s.mode == "vpn" ? "VPN guard" : "legacy")")
            if s.mode == "vpn" {
                if let tuns = s.tunnels, let t = tuns.first {
                    addInfo("Tunnel: \(t.up ? "up" : "down")\(t.detail.map { " (\($0))" } ?? "")")
                }
                if let eps = s.endpoints, !eps.isEmpty {
                    addInfo("Endpoints: \(eps.joined(separator: ", "))")
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

    @objc private func startService() { DezhbanCLI.runPrivileged(["start"]); refresh() }
    @objc private func stopService() { DezhbanCLI.runPrivileged(["stop"]); refresh() }
    @objc private func blockNow() { DezhbanCLI.runPrivileged(["block"]); refresh() }
    @objc private func unblockNow() { DezhbanCLI.runPrivileged(["unblock"]); refresh() }

    @objc private func openConfig() {
        NSWorkspace.shared.open(URL(fileURLWithPath: DezhbanCLI.configPath))
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
}
