import AppKit
import DezhbanCore

/// AppDelegate owns the menubar item — the safety/glance surface. A 1-second
/// timer reads the daemon's state file (a tiny local read — no geo-API polling
/// from the GUI), repaints the icon, and publishes into the shared AppState the
/// main window renders from; the dropdown is rebuilt from the current snapshot
/// each time it opens.
///
/// The dropdown is a glance and the handful of actions that are time-critical
/// at the moment you reach for it: the status line, Open Dezhban, the switch
/// window with its countdown, Pause, hold the line, and Quit — plus Panic
/// behind ⌥, which must work even if the main window cannot open, so it never
/// moves behind it. Manual block and unblock are deliberately NOT here:
/// somebody who wants to cut their own internet can turn off Wi-Fi, so blocking
/// by hand is a power-user affordance and lives in the window's Overview with
/// everything else (MainWindow/DetailHostView).
final class AppDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate {
    private var statusItem: NSStatusItem!
    private let menu = NSMenu()
    private var timer: Timer?
    private var updateTimer: Timer?
    private var snapshot: Snapshot?
    private var lastMtime: Date?
    private var lastIconKey: String?
    private let watchdog = MainThreadWatchdog()
    /// Set while a background state-file read is outstanding, so a slow or
    /// stalled read can never stack a second one behind it on every tick.
    private var readInFlight = false

    /// Every ~24h, not more often — this is a background courtesy check, not
    /// a thing to hammer GitHub with. See UpdateChecker's doc comment.
    private static let updateCheckInterval: TimeInterval = 24 * 60 * 60

    func applicationDidFinishLaunching(_ notification: Notification) {
        NotificationManager.requestAuthorizationIfNeeded()
        // Resolve the config path once, off the main thread, before any pane asks for
        // it — every later read is then a memoized lookup rather than a shell-out on
        // whatever thread the caller happened to be on. See DezhbanCLI.exec.
        DezhbanCLI.warmConfigPath()
        // The token capability probe is deliberately NOT warmed here, unlike the
        // config path above. It is a keychain WRITE, so warming it at launch made
        // every session touch the login keychain for a feature the user may never
        // open — and on a Mac whose login keychain password has diverged from the
        // account password, that is an unexplained unlock dialog at every login.
        // `DetailHostView` warms it when the window first appears instead, so a
        // menubar-only session costs nothing. See `ControlToken.warmCapability`.
        AppActions.refresh = { [weak self] in self?.refresh() }
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        menu.delegate = self
        // We compute item enablement ourselves (see addAction); without this, AppKit's
        // automatic validation force-enables any item whose target responds to its
        // selector, so the gating on "Block now" etc. would be ignored.
        menu.autoenablesItems = false
        statusItem.menu = menu
        watchdog.start()
        refresh()
        // Launching the app shows the app: reaching the main window only through
        // the menubar dropdown made opening it a two-step discovery problem, and
        // the menubar item stays available either way.
        //
        // Except when the launch wasn't the user's doing. AppKit clears this flag
        // for launches it performed on their behalf rather than at their request —
        // a login item, a state restoration, opening a file — and a window
        // appearing unbidden at every boot is exactly the noise a menubar app
        // should not make. A missing flag reads as an ordinary launch, so the
        // window still opens if AppKit ever stops reporting this.
        let deliberateLaunch = (notification.userInfo?[NSApplication.launchIsDefaultUserInfoKey] as? Bool) ?? true
        // The Settings "Open minimized" choice decides what to do with that.
        // Its default, .bootOnly, is exactly the behaviour described above, so
        // anyone who never touches the setting sees no change.
        if LaunchPreference.current.opensWindow(deliberateLaunch: deliberateLaunch) {
            MainWindow.shared.open()
        }
        AppState.shared.refreshServiceState()
        AppState.shared.checkForUpdates()
        AppState.shared.offerFirstRunIfNeeded()
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
        pollStateFile()
        repaint()
    }

    /// Stats and decodes the state file on a background queue, publishing the
    /// result back on main. Both filesystem calls are cheap in the normal case,
    /// but the main thread must never be the one waiting on I/O: a state file on
    /// a stalled mount, or a slow stat under memory pressure, freezes the entire
    /// UI. That is the leading suspect for the reported beachballs, and it is a
    /// hazard worth removing whether or not it turns out to be the cause. A tick
    /// is skipped entirely while a previous read is outstanding, so slow reads
    /// can never stack up behind each other.
    private func pollStateFile() {
        guard !readInFlight else { return }
        readInFlight = true
        let known = lastMtime
        DispatchQueue.global(qos: .utility).async { [weak self] in
            let mtime = StateReader.modificationTime()
            // Re-decode only when the file actually changed — the daemon rewrites
            // it about once per poll.
            let changed = mtime != known
            let fresh = changed ? StateReader.read() : nil
            DispatchQueue.main.async {
                guard let self else { return }
                self.readInFlight = false
                guard changed else { return }
                self.lastMtime = mtime
                self.snapshot = fresh
                AppState.shared.snapshot = fresh
                self.repaint()
            }
        }
    }

    /// Repaints the menubar icon from the cached snapshot. Runs every tick rather
    /// than only when the file changes, so staleness can flip the icon to gray
    /// without the daemon having written anything.
    private func repaint() {
        AppState.shared.now = Date()
        guard let button = statusItem.button else { return }
        let (state, symbol, help) = PostureUI.iconFor(snapshot)
        let key = "\(state)|\(help)"
        guard key != lastIconKey else { return }
        lastIconKey = key
        if let brand = Self.menubarIcon(state) {
            // Full-color brand state icon (bundled by build-app.sh from gui/artifacts/png).
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
        let essential = essentialClass(state, snapshot)
        if let prev = lastEssential, prev != essential {
            NotificationManager.post(title: Self.essentialTitles[essential] ?? "Dezhban", body: "dezhban — \(help)")
        }
        lastEssential = essential
    }

    // MARK: - essential-event notifications

    private var lastEssential: String?

    /// Collapses (icon state, snapshot) into the coarse classes worth
    /// interrupting a person for. Standby and stopped both draw the "off" icon
    /// but mean very different things, so they class off the snapshot's own
    /// posture/liveness rather than parsing the rendered prose — the daemon's
    /// Display.Key is deliberately coarse (five brand states), so "off" alone
    /// can't tell them apart, but the stable posture string can.
    private func essentialClass(_ state: String, _ snap: Snapshot?) -> String {
        guard let snap = snap, PostureUI.isLive(snap) else { return "stopped" }
        if snap.posture == "standby" { return "standby" }
        return state // on / blocked / warning / paused
    }

    private static let essentialTitles: [String: String] = [
        "on": "Guard armed",
        "blocked": "Traffic cut",
        "warning": "Warning",
        "paused": "Paused — using your real IP",
        "standby": "Standby — nothing is being blocked",
        "stopped": "Guard stopped",
        // "off" is unreachable from essentialClass today (standby and stopped —
        // the only sources of the "off" icon state — are classified separately
        // above); kept for defensive completeness against the map lookup's
        // `?? "Dezhban"` fallback.
        "off": "Standby — nothing is being blocked",
    ]

    /// Menubar brand state images, loaded once from the app bundle's Resources
    /// (put there by build-app.sh from gui/artifacts/png) and cached per state. Empty
    /// when running outside the bundle, which triggers the SF Symbol fallback
    /// in refresh(). (The Dock tile's own two-file family is PostureUI.dockIcon;
    /// the window's Overview hero uses PostureUI.stateTile, which — like this
    /// one — ships all five states.)
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
                : "Stopped")
        }

        menu.addItem(.separator())

        let open = addAction("Open Dezhban…", #selector(openMainWindow))
        open.keyEquivalent = "o"
        open.toolTip = "Everything else — settings, diagnostics, manual block, logs, help. Hold ⌥ for Panic."

        // Panic is the lockout escape hatch: it must never depend on the main
        // window opening, so it keeps a menubar item. It sits behind ⌥ as the
        // alternate to "Open Dezhban…" — one keystroke away in a fixed place,
        // but not one slip away from a menu people open to check a countdown.
        //
        // An alternate must be adjacent to its sibling and share its key
        // equivalent, differing only by the extra modifier: ⌘O opens the window,
        // ⌥ swaps the item, ⌘⌥O panics.
        let panic = addAction("Panic — force unblock…", #selector(confirmPanic),
                              enabled: DezhbanCLI.binaryPath() != nil)
        panic.keyEquivalent = "o"
        panic.keyEquivalentModifierMask = [.command, .option]
        panic.isAlternate = true
        panic.toolTip = "Removes every rule dezhban installed. Works even when nothing is running."

        menu.addItem(.separator())

        // Manual block and unblock are NOT here. Somebody who wants to cut their
        // own internet can turn off Wi-Fi; blocking by hand is a power-user and
        // debugging affordance, and it lives in the window's Overview with the
        // rest of them. What stays is what is time-critical at the moment you
        // reach for the menubar.

        // Switch window: connect a brand-new VPN whose server isn't known yet.
        // Time-critical mid-flow, and the countdown is glanceable — so it stays.
        // `switch --cancel` refuses to touch an open PAUSE (see the glossary's
        // Pause entry) — `resume` is the only way to end one early, so a pause
        // gets its own item instead of the generic Cancel one.
        if let sw = s?.switch, sw.open, sw.isPause {
            addAction("Resume now" + sw.leftSuffix(asOf: Date()), #selector(resumeNow), enabled: isRunning)
                .toolTip = AppState.shared.routineHint("Ends the pause early and re-arms the guard.")
        } else if let sw = s?.switch, sw.open {
            addAction("Cancel VPN switch" + sw.leftSuffix(asOf: Date()), #selector(cancelSwitch),
                      enabled: isRunning)
                .toolTip = AppState.shared.routineHint("Closes the window and restores the guard.")
        } else {
            addAction("Switching VPN…", #selector(openSwitch), enabled: isRunning)
                .toolTip = AppState.shared.routineHint("Briefly relaxes the guard so a new VPN can connect.")
            // Two independent reasons to grey this out, and the tooltip names
            // the one that actually applies. A single "vpn.pauseMax is 0"
            // string covered both, so a stopped daemon with a perfectly normal
            // 30m pauseMax sent the user off to fix a key that was already
            // right — `status --json` is read-only and answers with the daemon
            // down, so pauseIsEnabled is true in exactly that case.
            let pauseAllowed = AppState.shared.pauseIsEnabled
            let pauseEnabled = isRunning && pauseAllowed
            let pauseItem = addAction("Pause — use my real IP", #selector(pauseNow), enabled: pauseEnabled)
            pauseItem.toolTip = {
                if !pauseAllowed { return "Disabled — vpn.pauseMax is \"0\" in your config." }
                if !isRunning { return "Unavailable — dezhban isn’t running. Start it first." }
                return AppState.shared.routineHint(
                    "Deliberately drops to your real ISP IP, then re-arms the guard automatically.")
            }()
            // The offered lengths come from the daemon (`pause --list --json`),
            // so this menu and `dezhban pause --list` can never offer different
            // choices. Clicking the parent still pauses for the built-in
            // default; the submenu is for picking deliberately.
            if pauseEnabled, let options = AppState.shared.pauseOptions, !options.isEmpty {
                let submenu = NSMenu()
                for option in options {
                    let item = NSMenuItem(title: option.label,
                                          action: #selector(pauseForOption(_:)),
                                          keyEquivalent: "")
                    item.target = self
                    item.representedObject = option.value
                    // An over-cap length is shown disabled with the reason,
                    // never hidden and never quietly shortened — a cap you
                    // cannot see is one you keep bumping into.
                    item.isEnabled = option.isAvailable
                    item.toolTip = option.isAvailable ? option.why : option.unavailable
                    submenu.addItem(item)
                }
                submenu.autoenablesItems = false
                pauseItem.submenu = submenu
            }

            // The mirror image of Pause, and placed beside it so the pair reads
            // as the two answers to "I am about to change my VPN situation":
            // pause = let me use my real IP, hold the line = keep me cut.
            // It only ever suppresses a relaxation, so it is safe to offer
            // whenever the daemon is running.
            if s?.holdArmed ?? false {
                addAction("Don’t hold the line", #selector(cancelHold), enabled: isRunning)
                    .toolTip = "Armed — the next VPN drop stays cut. Choose this to let it redial normally instead."
            } else {
                addAction("Hold the line — keep me cut", #selector(holdLine), enabled: isRunning)
                    .toolTip = isRunning
                        ? AppState.shared.routineHint(
                            "For when YOU are disconnecting: the next VPN drop stays cut instead of "
                                + "opening a window so it can redial.")
                        : "Unavailable — dezhban isn’t running. Start it first."
            }
        }

        menu.addItem(.separator())
        addAction("Quit", #selector(quit))

        // Keep the reachability/installed caches honest for the next open
        // without blocking this one.
        AppState.shared.refreshServiceState()
    }

    /// The dropdown's one-line glance: posture, plus exit country/provider when known.
    private func statusLine(_ s: Snapshot) -> String {
        var line = PostureUI.humanPosture(s)
        if let e = s.enforcementErr, !e.isEmpty {
            line = "⚠︎ Enforcement failed — open Dezhban for details"
        } else if let cc = s.countryLabel, !cc.isEmpty {
            // Only if the headline does not already END with it. `render.Text`'s
            // FULL BLOCK headline is "Full block — Iran (IR)", so appending
            // unconditionally produced "Full block — Iran (IR) — Iran (IR) via
            // ipinfo": the country twice, with two em-dash separators, in the
            // one-line glance for the exact state this tool exists for.
            //
            // hasSuffix, not contains: the label is the last thing that headline
            // says, and `contains` matches anywhere. A daemon older than
            // `countryName` sends only the code, so `cc` is then a bare two-letter
            // token — and "VPN down — traffic cut" CONTAINS "PN" (Pitcairn),
            // which suppressed the country and left a dangling "via ipinfo".
            if !line.hasSuffix(cc) { line += " — \(cc)" }
            if let p = s.provider, !p.isEmpty { line += " via \(p)" }
        }
        return line
    }

    // MARK: - actions

    @objc private func openMainWindow() { MainWindow.shared.open() }

    // Routine posture ops: handled by the running daemon over its control socket,
    // with no password — semantics in AppActions.routine (refusals never escalate).
    @objc private func openSwitch() { AppActions.routine(["switch", "--no-wait"], "open a switch window") }
    @objc private func cancelSwitch() { AppActions.routine(["switch", "--cancel"], "cancel the switch window") }
    @objc private func pauseNow() { AppActions.routine(["pause"], "pause the guard") }
    @objc private func resumeNow() { AppActions.routine(["resume"], "resume the guard") }
    /// Pauses for a length chosen from the submenu. The value travels verbatim
    /// from the daemon's own list, so the app never formats a duration itself.
    @objc private func pauseForOption(_ sender: NSMenuItem) {
        guard let value = sender.representedObject as? String else { return }
        AppActions.routine(["pause", value], "pause the guard for \(sender.title)")
    }
    @objc private func holdLine() { AppActions.routine(["hold"], "hold the line") }
    @objc private func cancelHold() { AppActions.routine(["hold", "--cancel"], "cancel hold the line") }

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
