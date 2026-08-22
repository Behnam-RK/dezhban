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

    /// The window title for this section. The window is an AppKit NSWindow with
    /// an AppKit split view (MainWindow), so there is no NavigationSplitView to
    /// bridge a `.navigationTitle` into the titlebar — MainWindow binds the
    /// title to the selection through this instead.
    ///
    /// Five cases are just `label`; About is the one that differs, because the
    /// pane it names has always titled itself "About Dezhban" while the sidebar
    /// row says "About".
    var windowTitle: String {
        self == .about ? "About Dezhban" : label
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
    /// Whether the first-run wizard is on screen. Presented as a sheet over the
    /// main window, so there is one window and the wizard cannot be lost behind
    /// it.
    @Published var showFirstRun = false
    /// Last update check result (nil: none run yet, or the last one found
    /// nothing worth reporting — see UpdateChecker.check's doc comment on why
    /// a failure never surfaces as an error here).
    @Published var updateCheck: UpgradeCheckResult?

    // MARK: Diagnostics state
    //
    // The doctor report lives HERE, not in DiagnosticsView's @State, for two
    // reasons: navigating away must not discard a report someone just read,
    // and the sidebar badge needs the report without the pane being on screen.
    @Published var doctorReport: DoctorReport?
    @Published var doctorError: String?
    @Published var doctorRunning = false

    /// The rules dezhban recorded applying, and the rules the kernel actually
    /// holds. Two separate reads: the first is unprivileged and refreshed with
    /// the rest of the pane, the second costs a password and only happens when
    /// asked for.
    @Published var appliedRules: AppliedRuleset?
    @Published var installedRules: InstalledRuleset?
    @Published var installedRulesError: String?
    @Published var installedRulesRunning = false

    /// Recent warn-and-worse records from dezhban's own log. nil is "not asked
    /// yet, or could not ask"; an EMPTY array is "asked, and there were none" —
    /// which is the good answer, and the pane says so. Collapsing the two would
    /// make a healthy host look like a broken reader.
    @Published var problems: [LogRecord]?
    /// The sidebar's yellow dot: the last doctor report has something a person
    /// should look at. A dedicated Bool (not derived in the cell) so the
    /// sidebar can subscribe with removeDuplicates() and never reload at 1 Hz.
    @Published var diagnosticsAttention = false
    private var doctorRanAt: Date?

    /// The VPN inventory (`detect-vpn --json`) for the Diagnostics pane's
    /// "Your VPNs" section and the Overview's "VPN app" row. nil until read,
    /// and nil against a CLI too old for the subcommand — both surfaces hide
    /// rather than guess.
    @Published var vpnInventory: VPNInventory?
    private var vpnInventoryReadAt: Date?
    /// In-flight guard, the same role `doctorRunning` plays for doctor. The
    /// staleness gate alone cannot hold the line for a FORCED refresh
    /// (`maxAge: 0`, what the Run-diagnostics button asks for), so without this
    /// a person clicking that button repeatedly forks one process-scanning
    /// `detect-vpn` subprocess per click and the last one to finish — not the
    /// last one started — decides what the pane shows.
    private var vpnInventoryRunning = false

    /// The strictness line for the Overview ("Balanced", "Custom (closest:
    /// Balanced)"), from `status --json`'s preset fields, falling back to
    /// `config preset list` against an older CLI. nil hides the row.
    @Published var presetLabel: String?

    let console = Console()

    var isLive: Bool { PostureUI.isLive(snapshot) }

    /// Offers the first-run wizard when this account has never completed it AND
    /// nothing is configured yet.
    ///
    /// The check is "does dezhban know a VPN server yet", not "is there a config
    /// file": someone who set dezhban up from the CLI has already answered these
    /// questions, and asking again the first time they open the app would look
    /// like it had forgotten. An endpoint or a profile is the signal, because
    /// tunnel interfaces are legitimately empty on an autodetect config — the
    /// recommended one. Opens the window too: a fresh install with nothing
    /// configured is the one launch where a window is the point.
    func offerFirstRunIfNeeded() {
        guard !FirstRun.isComplete, cliFound else { return }
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let info = DezhbanCLI.readProfiles()
            let configured = !(info?.defaultEndpoints.isEmpty ?? true) || !(info?.profiles.isEmpty ?? true)
            DispatchQueue.main.async {
                guard FirstRun.shouldOffer(vpnKnown: configured) else { return }
                self?.showFirstRun = true
                MainWindow.shared.open()
            }
        }
    }

    /// Decoded lazily on the first contextual help click and kept thereafter — see
    /// `openHelp(preferring:)`.
    private var cachedHelpBundle: HelpBundle?

    /// Opens the first of `targets` whose anchor actually exists in the bundle.
    ///
    /// The only entry point, deliberately. A second `openHelp(docAnchor:)` taking a
    /// bare string survived this change with no callers, and it skipped the
    /// resolution step this one exists for — so picking the shorter-looking overload
    /// would have silently restored landing at the top of the page.
    ///
    /// Preference order, not alternatives: a key's own row first, then its
    /// section. Resolving here rather than in the Help pane is what makes the
    /// fallback a *section* rather than the top of the page — the pane's own
    /// resolve() drops an unknown fragment and keeps the page, which for a
    /// forty-key reference is not a useful place to land.
    func openHelp(preferring targets: [HelpTarget]) {
        guard !targets.isEmpty else { return }
        // Decoded once and kept, rather than on every click of a `?`: `bundled()`
        // reads the payload off disk and JSON-decodes every page's full text plus its
        // per-row key list.
        //
        // It does not make that the app's only decode — `HelpView` evaluates
        // `bundled()` in its own `@State` initialiser, so a second copy exists and is
        // rebuilt more often than this one. Sharing them is worth doing and is not
        // this change's business; the point here is only that resolving a target must
        // not add a decode of its own.
        //
        // Nil (a bare SwiftPM binary with no help payload) leaves the preferred
        // target, which is the right guess when nothing can be checked.
        if cachedHelpBundle == nil { cachedHelpBundle = HelpBundle.bundled() }
        let index = cachedHelpBundle
        helpTarget = targets.first { index?.resolve($0)?.anchor != nil } ?? targets[0]
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
            // One `status --json` subprocess for all its fields, instead of
            // one subprocess per field (each of which used to probe the
            // control socket independently on top of that).
            let status = DezhbanCLI.readStatusJSON()
            let profiles = DezhbanCLI.readProfiles()
            // Read here, with the rest, so the menu can build from a cache
            // instead of shelling out while it is opening.
            let pauseOptions = DezhbanCLI.readPauseOptions()
            // Strictness rides the same status call. Against a CLI older than
            // the preset fields, one extra read of `config preset list` keeps
            // the Overview row alive; when even that is missing, the row hides.
            var presetLabel = status?.presetLabel
            if status != nil, presetLabel == nil, let presets = DezhbanCLI.readPresets() {
                if let matched = presets.first(where: { $0.matched == true }) {
                    presetLabel = matched.name.capitalized
                } else if !presets.isEmpty {
                    presetLabel = "Custom"
                }
            }
            let label = presetLabel
            DispatchQueue.main.async {
                self?.serviceIsInstalled = status?.serviceInstalled ?? false
                self?.controlIsReachable = status?.controlReachable ?? false
                self?.pauseIsEnabled = status?.pauseEnabled ?? false
                self?.profiles = profiles
                self?.pauseOptions = pauseOptions
                self?.presetLabel = label
            }
        }
    }

    /// Runs `doctor --json` off the main thread and publishes the report (and
    /// the sidebar-badge flag) here. Relocated from DiagnosticsView so the
    /// result survives navigation and can be triggered without the pane.
    ///
    /// Uses DezhbanCLI.exec directly (not `.run`) so stdout and stderr stay
    /// separate: `doctor` logs informational lines (autodetect, DNS warnings)
    /// to stderr, and `.run`'s CommandResult joins both into one string —
    /// which would corrupt the JSON parse the moment anything logged.
    func runDoctor(discover: Bool = false) {
        guard let bin = DezhbanCLI.binaryPath() else {
            doctorError = "dezhban CLI not found in a trusted install location"
            return
        }
        guard !doctorRunning else { return }
        doctorRunning = true
        doctorError = nil
        let args = discover
            ? ["doctor", "--json", "--discover", "--config", DezhbanCLI.resolvedConfigPath()]
            : ["doctor", "--json", "--config", DezhbanCLI.resolvedConfigPath()]
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let r = DezhbanCLI.exec(bin, args)
            let decoded = r.out.data(using: .utf8).flatMap(DoctorReport.decode)
            DispatchQueue.main.async {
                guard let self else { return }
                self.doctorRunning = false
                self.doctorRanAt = Date()
                if let decoded {
                    self.doctorReport = decoded
                    self.diagnosticsAttention = DoctorAttention.needsAttention(decoded)
                } else {
                    let text = [r.out, r.err].filter { !$0.isEmpty }.joined(separator: "\n")
                    self.doctorError = text.isEmpty ? "No output from `dezhban doctor --json`." : text
                    // The retained report is kept (the pane still shows it, under
                    // the failure) but it is no longer VERIFIED, so the badge must
                    // not go on asserting the pre-failure verdict. A run that
                    // can't complete is itself something to look at.
                    self.diagnosticsAttention = true
                }
            }
        }
    }

    /// Reads what dezhban recorded applying. Unprivileged and cheap — the record
    /// is a small file beside state.json — so it refreshes with the rest of the
    /// Diagnostics pane rather than on demand.
    func refreshAppliedRules() {
        guard cliFound else { return }
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let rules = DezhbanCLI.readAppliedRules()
            DispatchQueue.main.async { self?.appliedRules = rules }
        }
    }

    /// Reads recent problem records from dezhban's log. Unprivileged and cheap,
    /// so it refreshes with the rest of the Diagnostics pane.
    func refreshProblems() {
        guard cliFound else { return }
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let recs = DezhbanCLI.readProblems()
            DispatchQueue.main.async { self?.problems = recs }
        }
    }

    /// Reads dezhban's rules back out of the kernel. Costs an admin prompt, so
    /// it is never automatic.
    ///
    /// A READ — it installs nothing, changes nothing, and does not go through
    /// `Backend.Apply`, so it leaves the run loop's single-writer rule alone.
    /// There is deliberately no repair here either: the run loop's verification
    /// tick already re-applies rules that go missing, and a second repairer
    /// would be a second writer.
    func readInstalledRules() {
        guard !installedRulesRunning, cliFound else { return }
        installedRulesRunning = true
        installedRulesError = nil
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let r = DezhbanCLI.runPrivileged(["print-rules", "--installed", "--json"])
            let decoded = r.ok ? r.output.data(using: .utf8).flatMap(InstalledRuleset.decode) : nil
            DispatchQueue.main.async {
                guard let self else { return }
                self.installedRulesRunning = false
                if let decoded {
                    self.installedRules = decoded
                } else {
                    self.installedRules = nil
                    self.installedRulesError = r.output.isEmpty
                        ? "No output from `dezhban print-rules --installed`."
                        : r.output
                }
            }
        }
    }

    /// The background-trigger form: runs doctor only when the last report is
    /// older than maxAge (or absent). The staleness gate is load-bearing —
    /// callers include the essential-class edge into warning/blocked, and a
    /// flapping tunnel must not fork doctor subprocesses on every flap. Never
    /// auto-passes --discover; that scan is a person's explicit ask.
    func runDoctorIfStale(maxAge: TimeInterval) {
        guard cliFound else { return }
        // Gated on the timestamp ALONE: a run that produced no report still
        // ran, and re-testing `doctorReport != nil` would leave the gate
        // permanently unlatched on every host where doctor fails — forking a
        // subprocess per trigger forever, which is what the gate exists to
        // prevent.
        if let at = doctorRanAt, Date().timeIntervalSince(at) < maxAge { return }
        runDoctor()
    }

    /// Refreshes the VPN inventory when it is older than maxAge. `.onAppear`-
    /// and event-triggered only — detect-vpn is a process-scanning subprocess
    /// and has no business anywhere near the 1-second timer.
    func refreshVPNInventoryIfStale(maxAge: TimeInterval = 60) {
        guard cliFound else { return }
        // Timestamp alone, for the same reason as runDoctorIfStale: a nil
        // inventory (a CLI too old for --json, or a scan that failed) is a
        // RESULT, and re-testing it here would re-fork the process-scanning
        // subprocess on every `.onAppear` — i.e. every sidebar pane switch.
        if let at = vpnInventoryReadAt, Date().timeIntervalSince(at) < maxAge { return }
        // A forced refresh (maxAge: 0) walks straight past the staleness gate,
        // so the in-flight guard is the only thing between the Run-diagnostics
        // button and one process-scanning subprocess per click.
        guard !vpnInventoryRunning else { return }
        vpnInventoryRunning = true
        vpnInventoryReadAt = Date()
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let inv = DezhbanCLI.readVPNInventory()
            DispatchQueue.main.async {
                self?.vpnInventoryRunning = false
                self?.vpnInventory = inv
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
