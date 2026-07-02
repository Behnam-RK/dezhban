import AppKit

// Menubar-only agent: no Dock icon, no main window. LSUIElement in the bundled
// Info.plist does the same for the packaged app; setting the activation policy
// here keeps a bare `swift run` binary Dock-less too.
let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.accessory)
app.run()
