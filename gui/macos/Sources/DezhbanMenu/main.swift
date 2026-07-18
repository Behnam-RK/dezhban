import AppKit

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

    let windowItem = NSMenuItem()
    main.addItem(windowItem)
    let windowMenu = NSMenu(title: "Window")
    windowMenu.addItem(withTitle: "Close", action: #selector(NSWindow.performClose(_:)), keyEquivalent: "w")
    windowMenu.addItem(withTitle: "Minimize", action: #selector(NSWindow.performMiniaturize(_:)), keyEquivalent: "m")
    windowItem.submenu = windowMenu
    NSApplication.shared.windowsMenu = windowMenu

    return main
}

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
