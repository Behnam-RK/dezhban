import AppKit
import Combine
import SwiftUI

/// The main window: an AppKit NSWindow over an AppKit split view, whose two
/// columns host SwiftUI. Built once and reused (the singleton pattern of the
/// retired panels); closing hides it — the app lives on in the menubar. Never
/// opened automatically at launch unless the Settings "Open minimized" choice
/// says so (see LaunchVisibility): the app starts at login, and a window on
/// every boot would be noise for a background guard.
final class MainWindow: NSObject, NSWindowDelegate {
    static let shared = MainWindow()

    private var window: NSWindow!
    private var split: MainSplitViewController!
    private var cancellables = Set<AnyCancellable>()
    /// The title item, held only so its accessibility label can follow the text.
    private weak var sectionItem: NSToolbarItem?

    static let sectionTitleItemID = NSToolbarItem.Identifier("dezhban.sectionTitle")

    /// The section name, shown as a toolbar item in the DETAIL region rather
    /// than as the titlebar's own title. Two reasons, and the second is the
    /// load-bearing one:
    ///
    /// 1. It is where macOS sidebar apps put it (Mail, Notes, Finder).
    /// 2. A visible window title occupies the titlebar's leading area — the
    ///    very region the sidebar's own toolbar section needs. With the title
    ///    drawn there, the toggle and the tracking separator get pushed past
    ///    the split divider and the separator clamps to a position that does
    ///    not line up with it. Hiding the title frees that region, so the
    ///    separator can sit exactly on the divider, which is the whole point
    ///    of a TRACKING separator.
    ///
    /// `window.title` is still set — it feeds the Window menu, Mission Control
    /// and Exposé — it is only its titlebar drawing that is suppressed.
    /// The toolbar item's view is this NSTextField DIRECTLY, never wrapped in a
    /// container view. On macOS 26 a generic NSView as a toolbar item's view
    /// gets the Liquid Glass capsule behind it, so the title rendered as a
    /// pill and read as a tab or a button. A bare label is left plain, which is
    /// what a title should be — so the leading inset comes from a real
    /// `.space` toolbar item instead of from padding around a container.
    private let sectionLabel: NSTextField = {
        let label = NSTextField(labelWithString: "")
        // The system's own titlebar font face, a couple of points up: this
        // label stands in for the window title, so it should read as one
        // rather than as a control label.
        label.font = .titleBarFont(ofSize: NSFont.systemFontSize + 2)
        label.textColor = .labelColor
        label.lineBreakMode = .byTruncatingTail
        return label
    }()

    private override init() {
        super.init()

        let split = MainSplitViewController()
        self.split = split

        let win = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 960, height: 600),
            // .fullSizeContentView is what lets the sidebar's vibrancy run up
            // behind the titlebar, the way Finder's does — it is the other half
            // of NSSplitViewItem.allowsFullHeightLayout, which is inert without
            // it. Safe here specifically because the window has a real unified
            // toolbar: AppKit then owns the titlebar area and gives the detail
            // column a safe-area inset, so nothing draws under an opaque bar.
            styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView],
            backing: .buffered, defer: false)
        win.isReleasedWhenClosed = false
        // AppKit state restoration would otherwise reopen this window at launch
        // on its own, entirely outside the "Open minimized" check in
        // AppDelegate — which is half of why that setting appeared not to work.
        // Frame and sidebar position still persist; those go through
        // setFrameAutosaveName and the split view's autosave, not restoration.
        win.isRestorable = false
        win.delegate = self
        // 820 wide, not 640: the Help pane's inner HSplitView needs 620pt of
        // detail (200 sidebar + 420 page) and could not fit at the old minimum
        // at all. The 960 default clears the Overview's 720pt content column
        // plus the sidebar, so a new window shows the action row on one line.
        win.minSize = NSSize(width: 820, height: 460)
        win.center()
        win.setFrameAutosaveName("DezhbanMainWindow")

        // NSHostingController inside a view-controller hierarchy — NOT
        // `contentView = NSHostingView(...)`. The prohibition is older than
        // this code and still holds, though its original reason has changed:
        // it used to be that SwiftUI could only install a titlebar toolbar
        // through the contentViewController hookup, and a bare NSHostingView
        // made it draw its own toggle inline in the content instead. We install
        // the toolbar ourselves now, so that specific mechanism is gone — but a
        // hosting *controller* is still what participates in the responder
        // chain, sheet presentation, and safe-area propagation, and those are
        // what the columns need.
        win.contentViewController = split

        // Seed the section label BEFORE the toolbar is built. The toolbar
        // delegate hands AppKit `sectionLabel` itself as an item view, and an
        // item view is inserted at the intrinsic size it has at that moment —
        // an empty NSTextField is 0pt wide, so the title could come up clipped
        // and stay that way until the first section change moved it.
        sectionLabel.stringValue = (AppState.shared.selectedSection ?? .overview).windowTitle

        // A real NSToolbar, assigned BEFORE toolbarStyle: `toolbarStyle` is
        // inert while `toolbar` is nil, and it is the presence of a toolbar
        // that makes AppKit give the sidebar split item its full-height,
        // under-the-titlebar layout. Previously the style was set and no
        // toolbar ever assigned, so SwiftUI supplied one containing nothing but
        // [toggleSidebar, sidebarTrackingSeparator] — a tracking separator with
        // no trailing items, which is why the toggle and an orphan hairline
        // drifted to the far right whenever the sidebar collapsed.
        let toolbar = NSToolbar(identifier: "DezhbanMainToolbar")
        toolbar.delegate = self
        toolbar.allowsUserCustomization = false
        toolbar.displayMode = .iconOnly
        win.toolbar = toolbar
        win.toolbarStyle = .unified
        // See `sectionLabel`: the title lives in the toolbar, not the titlebar,
        // so the sidebar's toolbar section keeps the room it needs.
        win.titleVisibility = .hidden

        window = win
        bindWindowTitle()
    }

    /// The window title follows the selected section. It used to come from each
    /// pane's `.navigationTitle`, which only worked because SwiftUI bridged it
    /// through the hosting controller; with the split in AppKit those modifiers
    /// would be inert, so they are gone and this is the source instead.
    private func bindWindowTitle() {
        apply(section: AppState.shared.selectedSection)
        AppState.shared.$selectedSection
            // @Published emits in willSet, so hop a turn to read the new value.
            // DispatchQueue.main, not RunLoop.main: RunLoop.main only delivers in
            // the default run-loop mode, so a hop scheduled during event tracking
            // (a click-drag in the sidebar) waits for mouse-up — while the SwiftUI
            // detail pane, which observes AppState directly, has already switched.
            // The title would visibly lag the pane it names.
            .receive(on: DispatchQueue.main)
            .sink { [weak self] section in
                self?.apply(section: section)
            }
            .store(in: &cancellables)
    }

    private func apply(section: SidebarSection?) {
        let title = (section ?? .overview).windowTitle
        // Both: the toolbar label is what the user reads, `window.title` is what
        // the Window menu, Mission Control and Exposé read.
        window.title = title
        sectionLabel.stringValue = title
        sectionItem?.label = title
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

extension MainWindow: NSToolbarDelegate {
    func toolbarDefaultItemIdentifiers(_ toolbar: NSToolbar) -> [NSToolbarItem.Identifier] {
        // The tracking separator is what anchors the toggle to the sidebar's
        // own titlebar region; without it AppKit lays the toggle out as an
        // ordinary trailing item. This one is bound to the real NSSplitView
        // (see itemForItemIdentifier), which is the difference from the one
        // SwiftUI used to install — that one tracked nothing and drifted.
        // The section label after the separator is also what earns the
        // separator its place: it now has real trailing content to separate
        // the sidebar from, which the drifting one never did.
        [.toggleSidebar, .sidebarTrackingSeparator, .space, Self.sectionTitleItemID, .flexibleSpace]
    }

    func toolbarAllowedItemIdentifiers(_ toolbar: NSToolbar) -> [NSToolbarItem.Identifier] {
        [.toggleSidebar, .sidebarTrackingSeparator, Self.sectionTitleItemID, .flexibleSpace, .space]
    }

    func toolbar(_ toolbar: NSToolbar,
                 itemForItemIdentifier id: NSToolbarItem.Identifier,
                 willBeInsertedIntoToolbar flag: Bool) -> NSToolbarItem? {
        if id == Self.sectionTitleItemID {
            let item = NSToolbarItem(itemIdentifier: id)
            item.view = sectionLabel
            // Never collapsed into the overflow menu: it is the window's title,
            // not a command. (`allowsUserCustomization = false` on the toolbar
            // is what keeps it non-removable; `isNavigational` would only move
            // it into the navigational region, which is not what this is.)
            item.visibilityPriority = .high
            // A view-based item still needs a label for the overflow menu and
            // for accessibility — without one it reads as a nameless control.
            // Kept in step with the text by `apply(section:)`.
            item.label = sectionLabel.stringValue
            sectionItem = item
            return item
        }
        if id == .sidebarTrackingSeparator {
            // Bound to the real NSSplitView, so it tracks the actual divider
            // and hides itself when the sidebar collapses — which is the whole
            // difference from the one that used to drift.
            return NSTrackingSeparatorToolbarItem(
                identifier: .sidebarTrackingSeparator,
                splitView: split.splitView,
                dividerIndex: 0)
        }
        return nil // AppKit supplies .toggleSidebar and the spaces itself.
    }
}

/// The window's split: an AppKit sidebar item — so the vibrant source-list
/// material runs full-height under the titlebar, which is AppKit's own layout
/// for a sidebar split item in a window that has a toolbar — hosting a SwiftUI
/// list, over a SwiftUI detail host.
final class MainSplitViewController: NSSplitViewController {
    // The sidebar is AppKit (see SidebarViewController for why); the detail is
    // SwiftUI. AnyView so the detail is a stored property of a concrete type:
    // `.environmentObject` returns an opaque `some View`, which cannot be the
    // inferred type of a stored property.
    private let sidebarVC = SidebarViewController()
    private let detailVC = NSHostingController(
        rootView: AnyView(DetailHostView().environmentObject(AppState.shared)))

    override func viewDidLoad() {
        super.viewDidLoad()

        // Let the split view, not SwiftUI's intrinsic content size, decide the
        // detail column's width.
        detailVC.sizingOptions = []

        let sidebarItem = NSSplitViewItem(sidebarWithViewController: sidebarVC)
        sidebarItem.minimumThickness = 180
        sidebarItem.maximumThickness = 280
        sidebarItem.canCollapse = true
        sidebarItem.allowsFullHeightLayout = true // default for sidebar items; explicit for intent
        sidebarItem.titlebarSeparatorStyle = .none
        addSplitViewItem(sidebarItem)

        let detailItem = NSSplitViewItem(viewController: detailVC)
        detailItem.minimumThickness = 620 // HelpView's inner HSplitView floor
        detailItem.canCollapse = false
        detailItem.titlebarSeparatorStyle = .automatic
        addSplitViewItem(detailItem)

        splitView.autosaveName = "DezhbanMainSplit"
        splitView.dividerStyle = .thin
    }
}
