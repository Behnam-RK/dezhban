import AppKit
import SwiftUI
import DezhbanCore

/// The main window's sidebar sections.
enum SidebarSection: String, CaseIterable, Identifiable {
    case overview, diagnostics, settings, help, logs, about

    var id: String { rawValue }

    var label: String {
        switch self {
        case .overview: return "Overview"
        case .diagnostics: return "Diagnostics"
        case .settings: return "Settings"
        // The repo's own docs, rendered into the app at build time — see
        // internal/help. Sits next to Settings because that is where a reader
        // asking "what does this setting cost me?" is standing.
        case .help: return "Help"
        // Structured findings moved to the Diagnostics pane above; this pane
        // is transcripts only now (panic, install, config apply, `log` output).
        case .logs: return "Logs"
        case .about: return "About"
        }
    }

    var systemImage: String {
        switch self {
        case .overview: return "shield.lefthalf.filled"
        case .diagnostics: return "stethoscope"
        case .settings: return "gearshape"
        case .help: return "book"
        case .logs: return "text.alignleft"
        case .about: return "info.circle"
        }
    }
}

/// The Logs pane's backing store: one shared monospaced transcript
/// (NSTextStorage, appended in place — O(n) over a long `log stream`, unlike
/// re-setting a String each chunk) plus the live-stream lifecycle. Successor to
/// the retired OutputPanel; every long-running window action writes here.
final class Console: ObservableObject {
    @Published var title = "No output yet — run an action above."
    @Published var isStreaming = false

    let storage = NSTextStorage()
    /// Set by ConsoleTextView when the pane is on screen; used only to autoscroll.
    weak var textView: NSTextView?

    private var stream: StreamingProcess?
    private static var attrs: [NSAttributedString.Key: Any] {
        [.font: NSFont.monospacedSystemFont(ofSize: 11, weight: .regular),
         .foregroundColor: NSColor.labelColor]
    }

    /// Shows a run-to-completion result, superseding (and stopping) any live stream.
    func set(title: String, text: String) {
        stopStream()
        self.title = title
        storage.setAttributedString(NSAttributedString(string: text, attributes: Self.attrs))
        scrollToEnd()
    }

    func append(_ text: String) {
        storage.append(NSAttributedString(string: text, attributes: Self.attrs))
        scrollToEnd()
    }

    /// Starts the live `log stream` feed. The one action needing a running rather
    /// than run-to-completion child process; stopped by the Stop button, by any
    /// new `set`, or by the main window closing (MainWindow.windowWillClose).
    func startLogStream() {
        stopStream()
        title = "Live logs — streaming"
        storage.setAttributedString(NSAttributedString(string: ""))
        let proc = StreamingProcess(DezhbanCLI.logBinary, DezhbanCLI.streamLogsArgs)
        if proc.start(onOutput: { [weak self] text in self?.append(text) }) {
            stream = proc
            isStreaming = true
        } else {
            set(title: "Live logs", text: "failed to start log stream\n")
        }
    }

    /// Safe to call when no stream is running (e.g. every window close).
    func stopStream() {
        stream?.stop()
        stream = nil
        if isStreaming {
            isStreaming = false
            title = "Live logs — stopped"
        }
    }

    private func scrollToEnd() { textView?.scrollToEndOfDocument(nil) }
}

/// Observable state shared between the AppKit shell and the SwiftUI window.
/// Fed exclusively from AppDelegate's 1-second refresh (single timer, single
/// state-file reader — the window never polls on its own).
final class AppState: ObservableObject {
    static let shared = AppState()

    /// Last decoded daemon snapshot (nil: no state file / unparsable).
    @Published var snapshot: Snapshot?
    /// Ticks once a second from AppDelegate's timer so countdowns and "updated
    /// Xs ago" stay current even when the snapshot itself hasn't changed.
    @Published var now = Date()
    @Published var cliFound = DezhbanCLI.binaryPath() != nil
    @Published var serviceIsInstalled = false
    /// Whether the daemon's control socket is answering — i.e. whether Block /
    /// Unblock / Switch will complete without an admin prompt. Advisory only
    /// (tooltips/hints): the actions themselves probe for real, so a stale value
    /// can never cause a wrong action, just a wrong hint.
    @Published var controlIsReachable = false
    /// Whether `vpn.pauseMax` is nonzero, i.e. whether Pause would do anything.
    /// Advisory only, same convention as `controlIsReachable` — `dezhban pause`
    /// still refuses for real if this cache is stale.
    @Published var pauseIsEnabled = true
    /// The pause lengths the daemon offers, and which of them this host's
    /// vpn.pauseMax allows (nil: not yet read, or the CLI is too old to know
    /// `pause --list`). Cached so building the menu never shells out — a
    /// shell-out while the menu is opening would stall it.
    ///
    /// Read from the daemon rather than listed here so the menu and
    /// `dezhban pause --list` cannot offer different choices.
    @Published var pauseOptions: [PauseOption]?
    /// The configured VPN profiles (nil: not yet read, or config unreadable).
    /// Lives in config, not the daemon's Snapshot — see DezhbanCLI.readProfiles.
    @Published var profiles: ProfilesInfo?
    @Published var selectedSection: SidebarSection? = .overview
    /// A page (and optionally a heading) the Help pane should open next —
    /// how a contextual link from Settings names where it wants to land. The
    /// pane consumes it and sets it back to nil, so selecting Help again later
    /// does not jump back to a link the reader has moved on from.
    @Published var helpTarget: HelpTarget?
    /// Last update check result (nil: none run yet, or the last one found
    /// nothing worth reporting — see UpdateChecker.check's doc comment on why
    /// a failure never surfaces as an error here).
    @Published var updateCheck: UpgradeCheckResult?

    let console = Console()

    var isLive: Bool { PostureUI.isLive(snapshot) }

    /// Opens the Help pane at a specific place in the documentation, named the
    /// way a `Tunable`'s docAnchor writes it ("usage/config.md#fields").
    /// A docAnchor that names nothing bundled still opens the pane — better a
    /// reader lands in the documentation than on a dead control.
    func openHelp(docAnchor: String) {
        helpTarget = HelpTarget(docAnchor: docAnchor)
        selectedSection = .help
    }

    /// Routes a finished transcript into the Logs pane and
    /// navigates there — the window-side output surface for long actions.
    func showInLogs(title: String, text: String) {
        console.set(title: title, text: text.isEmpty ? "(no output)" : text)
        selectedSection = .logs
    }

    /// Appends the password expectation to a routine action's hint, so a button
    /// or menu item tells the truth about what the click will cost before it
    /// costs it. Shared by the window (OverviewView) and the menubar
    /// (AppDelegate), which used to compose this byte-identically on their own.
    func routineHint(_ what: String) -> String {
        controlIsReachable
            ? "\(what) No password needed — dezhban handles it while it's running."
            : "\(what) Will ask for your password (dezhban isn’t reachable)."
    }

    /// Recomputes the installed/reachable caches off the main thread. Skips the
    /// subprocesses entirely when the CLI is absent (nothing to ask). Called at
    /// launch, when the menu opens, when the window opens, and after
    /// install/uninstall — reads stay off every hot path.
    func refreshServiceState() {
        cliFound = DezhbanCLI.binaryPath() != nil
        guard cliFound else {
            serviceIsInstalled = false
            controlIsReachable = false
            return
        }
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            // One `status --json` subprocess for all three fields, instead of
            // one subprocess per field (each of which used to probe the
            // control socket independently on top of that).
            let status = DezhbanCLI.readStatusJSON()
            let profiles = DezhbanCLI.readProfiles()
            // Read here, with the rest, so the menu can build from a cache
            // instead of shelling out while it is opening.
            let pauseOptions = DezhbanCLI.readPauseOptions()
            DispatchQueue.main.async {
                self?.serviceIsInstalled = status?.serviceInstalled ?? false
                self?.controlIsReachable = status?.controlReachable ?? false
                self?.pauseIsEnabled = status?.pauseEnabled ?? false
                self?.profiles = profiles
                self?.pauseOptions = pauseOptions
            }
        }
    }

    /// Runs an update check off the main thread. Called at launch and from a
    /// ~24h timer (AppDelegate) — never more often than that, and never from
    /// anywhere but here: see UpdateChecker's doc comment on why this is
    /// user-context-only, on a schedule, not the root daemon on a fixed poll.
    func checkForUpdates() {
        DispatchQueue.global(qos: .utility).async { [weak self] in
            let result = UpdateChecker.check()
            DispatchQueue.main.async { self?.updateCheck = result }
        }
    }
}
