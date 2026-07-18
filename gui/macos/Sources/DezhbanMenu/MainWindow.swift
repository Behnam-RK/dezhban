import AppKit
import SwiftUI

/// The main window: an AppKit NSWindow hosting the SwiftUI MainView. Built once
/// and reused (the singleton pattern of the retired panels); closing hides it —
/// the app lives on in the menubar. Never opened automatically at launch: the
/// app starts at login, and a window on every boot would be noise for a
/// background guard.
final class MainWindow: NSObject, NSWindowDelegate {
    static let shared = MainWindow()

    private var window: NSWindow!

    private override init() {
        super.init()
        let win = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 780, height: 540),
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered, defer: false)
        win.title = "Dezhban"
        win.isReleasedWhenClosed = false
        win.delegate = self
        win.minSize = NSSize(width: 640, height: 440)
        win.center()
        win.setFrameAutosaveName("DezhbanMainWindow")
        win.contentView = NSHostingView(rootView: MainView().environmentObject(AppState.shared))
        window = win
    }

    func open() {
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        // Keep the installed/reachable caches honest while the window is in view.
        AppState.shared.refreshServiceState()
    }

    func windowWillClose(_ notification: Notification) {
        // Never leave a `log stream` child running unattended behind a closed
        // window; in-flight one-shot work still completes and lands in the
        // console for the next open.
        AppState.shared.console.stopStream()
    }
}
