import AppKit

/// One shared output window, built once and reused by every diagnostic/action
/// in the app (Run diagnostics, Panic, Install/Uninstall service, About,
/// View logs, VPN config apply) instead of a bespoke window per action.
///
/// Two modes:
///  - `show(title:text:)` — a finished result (most actions: run to
///    completion, then show what happened).
///  - `showStreaming(title:stop:)` + `append(_:)` — a live, appendable feed
///    with a Stop button, used only by "Stream live…" logs, the one action
///    that needs a running rather than run-to-completion child process.
final class OutputPanel: NSObject, NSWindowDelegate {
    static let shared = OutputPanel()

    private var window: NSWindow!
    private var textView: NSTextView!
    private var stopButton: NSButton!
    private var onStop: (() -> Void)?

    private override init() {
        super.init()
        buildWindow()
    }

    private func buildWindow() {
        let win = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 640, height: 420),
            styleMask: [.titled, .closable, .resizable, .miniaturizable],
            backing: .buffered, defer: false)
        win.isReleasedWhenClosed = false
        win.delegate = self
        win.center()

        let container = NSView(frame: NSRect(x: 0, y: 0, width: 640, height: 420))
        container.autoresizingMask = [.width, .height]

        let scroll = NSScrollView(frame: NSRect(x: 0, y: 40, width: 640, height: 380))
        scroll.autoresizingMask = [.width, .height]
        scroll.hasVerticalScroller = true
        scroll.borderType = .noBorder

        let tv = NSTextView(frame: scroll.bounds)
        tv.isEditable = false
        tv.isSelectable = true
        tv.isVerticallyResizable = true
        tv.isHorizontallyResizable = false
        tv.autoresizingMask = [.width]
        tv.font = NSFont.monospacedSystemFont(ofSize: 11, weight: .regular)
        tv.textContainerInset = NSSize(width: 8, height: 8)
        scroll.documentView = tv

        let stop = NSButton(title: "Stop", target: self, action: #selector(stopTapped))
        stop.frame = NSRect(x: 640 - 96, y: 8, width: 80, height: 24)
        stop.autoresizingMask = [.minXMargin]
        stop.isHidden = true

        container.addSubview(scroll)
        container.addSubview(stop)
        win.contentView = container

        self.window = win
        self.textView = tv
        self.stopButton = stop
    }

    /// Shows a run-to-completion result. Ends any previously-running stream
    /// first (a new action reusing this shared window supersedes it).
    func show(title: String, text: String) {
        stopPreviousStream()
        window.title = title
        textView.string = text
        stopButton.isHidden = true
        present()
    }

    /// Shows an appendable, live stream panel with a visible Stop button.
    /// `stop` is invoked when the user clicks Stop, closes the window, or
    /// starts a different action in this shared panel — never left running
    /// unattended.
    func showStreaming(title: String, stop: @escaping () -> Void) {
        stopPreviousStream()
        onStop = stop
        window.title = title
        textView.string = ""
        stopButton.isHidden = false
        present()
    }

    /// Appends text to whatever panel is currently open — the streaming
    /// process's output callback.
    func append(_ text: String) {
        textView.string += text
        textView.scrollToEndOfDocument(nil)
    }

    private func present() {
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    private func stopPreviousStream() {
        onStop?()
        onStop = nil
    }

    @objc private func stopTapped() {
        stopPreviousStream()
        stopButton.isHidden = true
    }

    func windowWillClose(_ notification: Notification) {
        stopPreviousStream()
    }
}
