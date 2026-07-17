import AppKit

// Regular app (not an LSUIElement agent): the Dock tile doubles as a state
// display — AppDelegate swaps NSApp.applicationIconImage to match the
// enforcement posture, and that needs a Dock icon to exist. The bundled
// Info.plist sets LSUIElement=false for the same reason.
let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.regular)
app.run()
