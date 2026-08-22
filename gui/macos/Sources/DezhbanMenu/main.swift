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

/// The hand-off request beside this install's session lock, once known.
///
/// A global because both ends of the launch need it: `acquireSessionOwnership()`
/// writes it from a losing copy, and `AppDelegate` claims it — from the
/// notification handler, and from a bounded backstop after launch, which is what
/// makes a request that arrived before the observer existed still get honoured.
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
/// Handled before the session lock, deliberately: this is not a second copy of
/// the app competing for the session, it is a one-shot errand, and it must work
/// while the app is running — which is exactly when the uninstaller finds it.
func retractLoginRegistrationsAndExit() {
    // The exit status is the whole channel. `unregister()` only logs its throw and
    // the uninstaller discards this process's output, so a refusal used to leave
    // the Login Items entry behind — pointing at a bundle about to be deleted, and
    // unreachable afterwards — while the script printed "service unregistered,
    // files deleted".
    exit(LoginItem.retractAll() ? 0 : 1)
}

/// Exits if another copy of this install already owns the session.
///
/// The login item is a launchd agent now, and `register()` on a `RunAtLoad` job
/// `exec`s the binary immediately — from the Settings toggle and from the
/// migration, both of which run while the app is up. launchd does not go through
/// LaunchServices, so nothing else dedupes it. Without this the user gets two
/// menubar items, and the duplicate carries `--background`, so under the default
/// "Only at login" it opens no window and there is no way to tell which icon is
/// which. See `SessionLock` and docs/adr/0014-login-item-launch-marker.md.
///
/// Returns the lock on success. The caller must keep it alive for the lifetime of
/// the process — the lock IS the open file descriptor.
func acquireSessionOwnership() -> SessionLock? {
    // No bundle identifier means a bare `swift run` binary: no agent could have
    // spawned it, and nothing to scope a lock to.
    guard let id = Bundle.main.bundleIdentifier,
          let support = FileManager.default
              .urls(for: .applicationSupportDirectory, in: .userDomainMask).first
    else { return nil }

    let lock = SessionLock.forBundle(
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
        NSLog("DezhbanMenu: session lock unavailable, starting anyway: \(why)")
        // No discard here. `discard()` is for a process that has just *taken* the
        // lock, on the grounds that anything on disk was meant for a predecessor —
        // and this process took nothing. A transient open() failure in a third
        // launch would otherwise delete a request the real session owner was about
        // to claim, losing that user's double-click.
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
            // the incumbent may be a --background login launch.
            //
            // NOT gated on "Open minimized". It was, on the reasoning that
            // "Always" has to mean always — but the preference governs the
            // *launch*, and a user-initiated launch of an already-running app is
            // not one: once the incumbent has finished starting, LaunchServices
            // turns the same double-click into a reopen, and
            // `applicationShouldHandleReopen` opens the window unconditionally in
            // every mode, by design ("the Dock icon and Open Dezhban… open the
            // window regardless"). So gating here bought no consistency at all —
            // it gave the same gesture opposite answers depending on whether the
            // incumbent happened to have finished starting yet — and cost the one
            // launch that had no other route to a window.
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
            // The file first, then the notification. The notification is the fast
            // path but is never queued, and the incumbent may still be starting up
            // with no observer installed — the file is the one that waits, and the
            // incumbent's launch-time backstop finds it. Whichever of the two gets
            // there claims it, so the window opens once (see HandoffRequest).
            var fileLanded = true
            if case .failure(let error) = sessionHandoff?.post() ?? .success(()) {
                NSLog("DezhbanMenu: could not record the hand-off request: \(error)")
                fileLanded = false
            }
            // Whether the file landed travels WITH the notification, because this
            // process is the only one that knows. The incumbent requires a file
            // outside its launch-time backstop window — that is what keeps an
            // unauthenticated notification from being an activate-on-demand channel
            // — so without this, a failed write (read-only or full home, wrong
            // permissions on the support directory: the same conditions the session
            // lock is written to tolerate) turned a launch the notification alone
            // used to handle into a visible no-op, which is the one outcome this
            // whole mechanism exists to prevent.
            DistributedNotificationCenter.default().postNotificationName(
                NSNotification.Name(AppDelegate.openWindowNotification),
                object: Bundle.main.bundleURL.resolvingSymlinksInPath().standardizedFileURL.path,
                userInfo: fileLanded ? nil : [AppDelegate.handoffFilelessKey: "1"],
                deliverImmediately: true)
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
