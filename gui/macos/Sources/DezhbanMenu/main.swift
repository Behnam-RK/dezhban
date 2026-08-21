import AppKit
import DezhbanCore

/// A minimal programmatic main menu. Without one, the SwiftUI main window's
/// text fields have no Edit menu (no ⌘C/⌘V/⌘X/⌘A) and ⌘W/⌘Q do nothing while
/// the window is key. Targets are nil so AppKit routes through the responder
/// chain as usual.
func makeMainMenu() -> NSMenu {
    let main = NSMenu()

    let appItem = NSMenuItem()
    main.addItem(appItem)
    let appMenu = NSMenu()
    appMenu.addItem(withTitle: "Quit Dezhban",
                    action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
    appItem.submenu = appMenu

    let editItem = NSMenuItem()
    main.addItem(editItem)
    let editMenu = NSMenu(title: "Edit")
    editMenu.addItem(withTitle: "Undo", action: Selector(("undo:")), keyEquivalent: "z")
    editMenu.addItem(withTitle: "Redo", action: Selector(("redo:")), keyEquivalent: "Z")
    editMenu.addItem(.separator())
    editMenu.addItem(withTitle: "Cut", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
    editMenu.addItem(withTitle: "Copy", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
    editMenu.addItem(withTitle: "Paste", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
    editMenu.addItem(withTitle: "Select All", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
    editItem.submenu = editMenu

    // The titlebar's toggle is an NSToolbarItem (MainWindow's NSToolbarDelegate);
    // this is the keyboard route to the same responder-chain action, which
    // MainSplitViewController supplies. A sidebar app without it is a defect in
    // its own right — ⌃⌘S is the system-standard equivalent.
    let viewItem = NSMenuItem()
    main.addItem(viewItem)
    let viewMenu = NSMenu(title: "View")
    let toggleSidebar = viewMenu.addItem(
        withTitle: "Toggle Sidebar",
        action: #selector(NSSplitViewController.toggleSidebar(_:)),
        keyEquivalent: "s")
    toggleSidebar.keyEquivalentModifierMask = [.control, .command]
    viewItem.submenu = viewMenu

    let windowItem = NSMenuItem()
    main.addItem(windowItem)
    let windowMenu = NSMenu(title: "Window")
    windowMenu.addItem(withTitle: "Close", action: #selector(NSWindow.performClose(_:)), keyEquivalent: "w")
    windowMenu.addItem(withTitle: "Minimize", action: #selector(NSWindow.performMiniaturize(_:)), keyEquivalent: "m")
    windowItem.submenu = windowMenu
    NSApplication.shared.windowsMenu = windowMenu

    return main
}

/// Quits at once if another copy of this bundle already owns the session.
///
/// The login item is a launchd agent now, and `register()` on a `RunAtLoad` job
/// `exec`s the binary immediately — from the Settings toggle and from the
/// migration, both of which run while the app is up. launchd does not go through
/// LaunchServices, so nothing else dedupes it. Without this the user gets two
/// menubar items, and the duplicate carries `--background`, so under the default
/// "Only at login" it opens no window and there is no way to tell which icon is
/// which. See `SingleInstance` and docs/adr/0014-login-item-launch-marker.md.
///
/// Only the loser exits — `SingleInstance.shouldYield` is a total order, so a
/// simultaneous pair cannot both stand down and leave the Mac with no app.
func yieldToRunningInstance() {
    // No bundle identifier means a bare `swift run` binary, which LaunchServices
    // does not track: nothing to compare against, and no agent to have spawned us.
    guard let id = Bundle.main.bundleIdentifier else { return }
    let mePID = ProcessInfo.processInfo.processIdentifier
    let running = NSRunningApplication.runningApplications(withBundleIdentifier: id)
    let others = running
        .filter { $0.processIdentifier != mePID }
        .map { InstanceIdentity(pid: $0.processIdentifier, launchedAt: $0.launchDate) }
    guard !others.isEmpty else { return }
    let own = InstanceIdentity(
        pid: mePID,
        launchedAt: NSRunningApplication.current.launchDate)
    guard SingleInstance.shouldYield(own: own, others: others) else { return }
    NSLog("DezhbanMenu: another instance is already running (pid \(others.map(\.pid))); exiting")
    exit(0)
}

// Regular app (not an LSUIElement agent): the Dock tile doubles as a state
// display — AppDelegate swaps NSApp.applicationIconImage to match the
// enforcement posture, and that needs a Dock icon to exist. The bundled
// Info.plist sets LSUIElement=false for the same reason.
let app = NSApplication.shared
// Before the delegate installs a menubar item or a timer: see above.
yieldToRunningInstance()
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.regular)
app.mainMenu = makeMainMenu()
app.run()
