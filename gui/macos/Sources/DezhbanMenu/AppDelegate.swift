import AppKit

/// AppDelegate owns the menubar item — the safety/glance surface. A 1-second
/// timer reads the daemon's state file (a tiny local read — no geo-API polling
/// from the GUI), repaints the icon, and publishes into the shared AppState the
/// main window renders from; the dropdown is rebuilt from the current snapshot
/// each time it opens.
///
/// The dropdown deliberately carries only the emergency/time-critical set —
/// status line, Open Dezhban, Block/Unblock, the switch window, Panic, Quit.
/// Panic and Block must work even if the main window can't open, so they never
/// move behind it. Everything else lives in the window (MainWindow/MainView).
final class AppDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate {
    private var statusItem: NSStatusItem!
    private let menu = NSMenu()
    private var timer: Timer?
    private var updateTimer: Timer?
    private var snapshot: Snapshot?
    private var lastMtime: Date?
    private var lastIconKey: String?

    /// Every ~24h, not more often — this is a background courtesy check, not
    /// a thing to hammer GitHub with. See UpdateChecker's doc comment.
    private static let updateCheckInterval: TimeInterval = 24 * 60 * 60

    func applicationDidFinishLaunching(_ notification: Notification) {
        NotificationManager.requestAuthorizationIfNeeded()
        // Resolve the config path once, off the main thread, before any pane asks for
        // it — every later read is then a memoized lookup rather than a shell-out on
        // whatever thread the caller happened to be on. See DezhbanCLI.exec.
        DezhbanCLI.warmConfigPath()
        AppActions.refresh = { [weak self] in self?.refresh() }
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        menu.delegate = self
        // We compute item enablement ourselves (see addAction); without this, AppKit's
        // automatic validation force-enables any item whose target responds to its
        // selector, so the gating on "Block now" etc. would be ignored.
        menu.autoenablesItems = false
        statusItem.menu = menu
        refresh()
        AppState.shared.refreshServiceState()
        AppState.shared.checkForUpdates()
        timer = Timer.scheduledTimer(withTimeInterval: 1.0, repeats: true) { [weak self] _ in
            self?.refresh()
        }
        updateTimer = Timer.scheduledTimer(withTimeInterval: Self.updateCheckInterval, repeats: true) { _ in
            AppState.shared.checkForUpdates()
        }
    }

    /// Clicking the Dock icon (re)opens the main window — the standard macOS
    /// contract for a regular app whose windows are all closed.
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        MainWindow.shared.open()
        return false
    }

    // MARK: - state → icon

    /// Refreshes the menubar icon on the 1s timer. Re-decodes the state file only
    /// when it actually changed (the daemon rewrites it ~every 30s), but still
    /// recomputes the icon each tick so staleness can flip it to gray from the
    /// cached snapshot. Repaints the button only when the icon actually differs,
    /// avoiding an NSImage allocation every second. Publishes the snapshot and a
    /// once-a-second `now` into AppState so the window's countdowns stay live off
    /// this same single timer.
    private func refresh() {
        let mtime = StateReader.modificationTime()
        if mtime != lastMtime {
            lastMtime = mtime
            snapshot = StateReader.read()
            AppState.shared.snapshot = snapshot
        }
        AppState.shared.now = Date()
        guard let button = statusItem.button else { return }
        let (state, symbol, help) = PostureUI.iconFor(snapshot)
        let key = "\(state)|\(help)"
        guard key != lastIconKey else { return }
        lastIconKey = key
        if let brand = Self.menubarIcon(state) {
            // Full-color brand state icon (bundled by build-app.sh from gui/assets/png).
            // Color IS the state — teal on, gray off, red blocked, amber warning —
            // drawn as-is (not templated, not tinted), so it reads identically on
            // light and dark menu bars.
            brand.accessibilityDescription = "dezhban: \(help)"
            button.image = brand
        } else {
            // Fallback for a bare `swift run` binary with no bundle resources: a
            // template SF Symbol in the menu bar's own foreground color, state
            // carried by the symbol's shape.
            let image = NSImage(systemSymbolName: symbol, accessibilityDescription: "dezhban: \(help)")
            image?.isTemplate = true
            button.image = image
        }
        button.contentTintColor = nil
        button.toolTip = "dezhban — \(help)"
        // The Dock tile shows a COARSER state than the menu bar: only "blocked" is
        // ever distinct there (see PostureUI.dockState) — "off"/"warning" show the
        // default guard look instead. nil falls back to the bundle's static AppIcon
        // (e.g. outside the assembled .app bundle).
        NSApp.applicationIconImage = PostureUI.dockIcon(PostureUI.dockState(for: state))

        // Essential-transition notifications. The FIRST classification after
        // launch is recorded silently — notifying the user about the state the
        // world was already in when the app opened is noise, not news.
        let essential = essentialClass(state, help)
        if let prev = lastEssential, prev != essential {
            NotificationManager.post(title: Self.essentialTitles[essential] ?? "Dezhban", body: "dezhban — \(help)")
        }
        lastEssential = essential
    }

    // MARK: - essential-event notifications

    private var lastEssential: String?

    /// Collapses (icon state, help) into the coarse classes worth interrupting a
    /// person for. Standby and stopped both draw the gray icon but mean very
    /// different things, so they class by the help text, not the icon.
    private func essentialClass(_ state: String, _ help: String) -> String {
        if help == "stopped" { return "stopped" }
        if help.hasPrefix("standby") { return "standby" }
        return state // on / off / blocked / warning
    }

    private static let essentialTitles: [String: String] = [
        "on": "Guard armed",
        "blocked": "Egress blocked",
        "warning": "Warning",
        "standby": "Standby — not enforcing",
        "stopped": "Protection stopped",
        "off": "Not enforcing",
    ]

    /// Menubar brand state images, loaded once from the app bundle's Resources
    /// (put there by build-app.sh from gui/assets/png) and cached per state. Empty
    /// when running outside the bundle, which triggers the SF Symbol fallback
    /// in refresh(). (Dock-size counterparts live in PostureUI.dockIcon, shared
    /// with the window's Overview hero.)
    private static var menubarIcons: [String: NSImage] = [:]

    private static func menubarIcon(_ state: String) -> NSImage? {
        if let img = menubarIcons[state] { return img }
        guard let url = Bundle.main.url(forResource: "menubar-state-\(state)", withExtension: "png"),
              let img = NSImage(contentsOf: url) else { return nil }
        // The bundled bitmap is the designer's menubar master (88px tall = 22pt
        // @4x, not square). Scale to the 22pt menu bar item height, preserving
        // the glyph's aspect ratio.
        let height: CGFloat = 22
        img.size = NSSize(width: img.size.width * height / img.size.height, height: height)
        img.isTemplate = false
        menubarIcons[state] = img
        return img
    }

    private var isRunning: Bool { PostureUI.isLive(snapshot) }

    // MARK: - menu

    func menuNeedsUpdate(_ menu: NSMenu) {
        menu.removeAllItems()
        let s = snapshot

        // One-line status header (disabled, informational) — the glance. The
        // full detail block lives in the window's Overview now.
        if let s = s, isRunning {
            addInfo(statusLine(s))
        } else {
            addInfo(DezhbanCLI.binaryPath() == nil
                ? "dezhban CLI not found — install it first"
                : "Kill switch stopped")
        }

        menu.addItem(.separator())

        let open = addAction("Open Dezhban…", #selector(openMainWindow))
        open.keyEquivalent = "o"

        menu.addItem(.separator())

        // Manual block / unblock, gated on current posture. These go to the running
        // daemon over its control socket, so they normally need no password — say so,
        // and say the opposite when they'd have to fall back to a direct root action.
        let blocked = s?.blocked ?? false
        // With the guard holding a downed tunnel, Unblock doubles as the
        // "my VPN is off on purpose — release the line" action.
        let guardHolds = isRunning && PostureUI.guardHoldsDownedTunnel(s)
        addAction("Block now", #selector(blockNow), enabled: isRunning && !blocked)
            .toolTip = routineHint("Cuts all egress and holds it until you unblock.")
        addAction("Unblock", #selector(unblockNow), enabled: isRunning && (blocked || guardHolds))
            .toolTip = routineHint("Releases a manual block and resumes monitoring.")

        // Switch window: connect a brand-new VPN whose server isn't known yet.
        // Time-critical mid-flow, and the countdown is glanceable — so it stays.
        if let sw = s?.switch, sw.open {
            let left = max(0, sw.until.timeIntervalSinceNow)
            addAction("Cancel VPN switch (\(PostureUI.mmss(left)) left)", #selector(cancelSwitch),
                      enabled: isRunning)
                .toolTip = routineHint("Closes the window and restores the guard.")
        } else {
            addAction("Switching VPN…", #selector(openSwitch), enabled: isRunning)
                .toolTip = routineHint("Briefly relaxes the guard so a new VPN can connect.")
        }

        // Panic is the lockout escape hatch: it must never depend on the main
        // window opening, so it keeps a first-class menubar item.
        addAction("Panic — force unblock…", #selector(confirmPanic), enabled: DezhbanCLI.binaryPath() != nil)

        menu.addItem(.separator())
        addAction("Quit", #selector(quit))

        // Keep the reachability/installed caches honest for the next open
        // without blocking this one.
        AppState.shared.refreshServiceState()
    }

    /// The dropdown's one-line glance: posture, plus exit country/provider when known.
    private func statusLine(_ s: Snapshot) -> String {
        var line = PostureUI.humanPosture(s).capitalized
        if let e = s.enforcementErr, !e.isEmpty {
            line = "⚠︎ Enforcement failed — open Dezhban for details"
        } else if let cc = s.countryCode, !cc.isEmpty {
            line += " — \(cc)"
            if let p = s.provider, !p.isEmpty { line += " via \(p)" }
        }
        return line
    }

    /// Appends the password expectation to a routine action's tooltip, so the menu
    /// tells the truth about what the click will cost before it costs it.
    private func routineHint(_ what: String) -> String {
        AppState.shared.controlIsReachable
            ? "\(what) No password needed — the running daemon handles it."
            : "\(what) Will ask for your password (the daemon isn’t reachable)."
    }

    // MARK: - actions

    @objc private func openMainWindow() { MainWindow.shared.open() }

    // Routine posture ops: handled by the running daemon over its control socket,
    // with no password — semantics in AppActions.routine (refusals never escalate).
    @objc private func blockNow() { AppActions.routine(["block"], "block") }
    @objc private func unblockNow() { AppActions.routine(["unblock"], "unblock") }
    @objc private func openSwitch() { AppActions.routine(["switch", "--no-wait"], "open a switch window") }
    @objc private func cancelSwitch() { AppActions.routine(["switch", "--cancel"], "cancel the switch window") }

    /// Menubar panic: confirmation, then a direct privileged run with the result
    /// in an NSAlert (scrollable transcript) — deliberately NOT routed through
    /// the main window, which might be exactly what's broken.
    @objc private func confirmPanic() {
        guard AppActions.confirmPanic() else { return }
        AppActions.capturedPrivileged(["panic"]) { result in
            AppActions.outputAlert(title: "dezhban — panic", ok: result.ok, output: result.output)
        }
    }

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
}
