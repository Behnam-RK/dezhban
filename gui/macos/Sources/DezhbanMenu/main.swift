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

/// The hand-off request beside this install's instance lock, once known.
///
/// A global because both ends of the launch need it: `acquireSessionOwnership()`
/// writes it from a losing copy, and `AppDelegate` consumes it — including on the
/// ordinary 1-second tick, which is what makes a request that arrived before the
/// observer existed still get honoured.
var sessionHandoff: HandoffRequest?

/// Retracts every login registration and exits, without starting the app.
///
/// The uninstaller needs this. A LaunchServices login item disappeared with its
/// bundle; the launchd agent that replaced it does not — and `launchctl bootout`
/// only unloads the job for this boot, leaving the registration that created it
/// to reload at the next login against a plist inside a bundle that has been
/// deleted. Only `SMAppService.unregister()` actually retracts it, and only the
/// app can call it, so `packaging/macos/uninstall.sh` runs the binary this way
/// (as the console user, in their GUI session) before deleting anything.
///
/// Handled before the instance lock, deliberately: this is not a second copy of
/// the app competing for the session, it is a one-shot errand, and it must work
/// while the app is running — which is exactly when the uninstaller finds it.
func retractLoginRegistrationsAndExit() {
    LoginItem.retractAll()
    exit(0)
}

/// Exits if another copy of this install already owns the session.
///
/// The login item is a launchd agent now, and `register()` on a `RunAtLoad` job
/// `exec`s the binary immediately — from the Settings toggle and from the
/// migration, both of which run while the app is up. launchd does not go through
/// LaunchServices, so nothing else dedupes it. Without this the user gets two
/// menubar items, and the duplicate carries `--background`, so under the default
/// "Only at login" it opens no window and there is no way to tell which icon is
/// which. See `InstanceLock` and docs/adr/0014-login-item-launch-marker.md.
///
/// Returns the lock on success. The caller must keep it alive for the lifetime of
/// the process — the lock IS the open file descriptor.
func acquireSessionOwnership() -> InstanceLock? {
    // Set for the winner, read by AppDelegate. A hand-off request that arrives
    // before the observer exists lands here instead of nowhere.
    defer { _ = sessionHandoff }
    // No bundle identifier means a bare `swift run` binary: no agent could have
    // spawned it, and nothing to scope a lock to.
    guard let id = Bundle.main.bundleIdentifier,
          let support = FileManager.default
              .urls(for: .applicationSupportDirectory, in: .userDomainMask).first
    else { return nil }

    let lock = InstanceLock.forBundle(
        path: Bundle.main.bundleURL.path, identifier: id, supportDirectory: support)
    sessionHandoff = HandoffRequest.beside(lock: lock.url)
    switch lock.acquire() {
    case .acquired:
        // Anything already on disk was meant for a predecessor, not for us.
        sessionHandoff?.discard()
        return lock
    case .unavailable(let why):
        // Never refuse to start over this. A duplicate icon is a smaller failure
        // than an app that will not launch because a support directory is broken.
        NSLog("DezhbanMenu: instance lock unavailable, starting anyway: \(why)")
        sessionHandoff?.discard()
        return lock
    case .heldByAnother:
        // A background launch loses silently — that copy was never going to show
        // the user anything. A launch the user performed must not be a no-op, so
        // hand them over to the instance that owns the session.
        if !LaunchVisibility.isBackgroundLaunch(arguments: CommandLine.arguments) {
            let mePID = ProcessInfo.processInfo.processIdentifier
            let incumbent = NSRunningApplication
                .runningApplications(withBundleIdentifier: id)
                .first {
                    $0.processIdentifier != mePID && !$0.isTerminated
                        && $0.bundleURL?.resolvingSymlinksInPath().standardizedFileURL
                        == Bundle.main.bundleURL.resolvingSymlinksInPath().standardizedFileURL
                }
            incumbent?.activate()
            // Ask it to open its window — which it may not currently have, since
            // the incumbent may be a --background login launch — but only when
            // this launch would have opened one itself. "Open minimized: Always"
            // means always: a second launch of the same app must not become the
            // one way to make a window appear, or the setting means one thing on
            // the first launch and the opposite on the second.
            //
            // A notification rather than re-opening the bundle through
            // NSWorkspace: asking LaunchServices to open the app we are in the
            // middle of quitting could spawn yet another copy, which would find
            // the lock held and ask again.
            //
            // Scoped to this install by posting the bundle PATH as the object.
            // The name derives from the bundle id, and the lock deliberately lets
            // dist/Dezhban.app run beside an installed copy — an unscoped
            // notification would have a duplicate launch of one install open the
            // other install's window.
            if LaunchPreference.current.opensWindow(backgroundLaunch: false) {
                // The file first, then the notification. The notification is the
                // fast path but is never queued, and the incumbent may still be
                // starting up with no observer installed — the file is the one
                // that waits, and its ordinary once-a-second tick finds it.
                sessionHandoff?.post()
                DistributedNotificationCenter.default().postNotificationName(
                    NSNotification.Name(AppDelegate.openWindowNotification),
                    object: Bundle.main.bundleURL.resolvingSymlinksInPath().standardizedFileURL.path,
                    userInfo: nil, deliverImmediately: true)
            }
        }
        NSLog("DezhbanMenu: another copy of this install owns the session; exiting")
        exit(0)
    }
}

// Both of these run before NSApplication exists: the errand mode never becomes
// an app at all, and a duplicate must exit before it can put a tile in the Dock.
if CommandLine.arguments.contains("--unregister-login-item") {
    retractLoginRegistrationsAndExit()
}
// Held for the lifetime of the process — the lock is the open file descriptor,
// so letting this go out of scope would release the session to the next starter.
let sessionLock = acquireSessionOwnership()

// Regular app (not an LSUIElement agent): the Dock tile doubles as a state
// display — AppDelegate swaps NSApp.applicationIconImage to match the
// enforcement posture, and that needs a Dock icon to exist. The bundled
// Info.plist sets LSUIElement=false for the same reason.
let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.regular)
app.mainMenu = makeMainMenu()
app.run()
