import AppKit
import SwiftUI

/// The main window's sidebar sections.
enum SidebarSection: String, CaseIterable, Identifiable {
    case overview, settings, logs, about

    var id: String { rawValue }

    var label: String {
        switch self {
        case .overview: return "Overview"
        case .settings: return "Settings"
        case .logs: return "Logs & Diagnostics"
        case .about: return "About"
        }
    }

    var systemImage: String {
        switch self {
        case .overview: return "shield.lefthalf.filled"
        case .settings: return "gearshape"
        case .logs: return "text.alignleft"
        case .about: return "info.circle"
        }
    }
}

/// Pure snapshot → presentation derivations, shared by the menubar (AppDelegate)
/// and the main window so the two surfaces can never disagree about what a
/// posture looks like. Moved verbatim from AppDelegate when the window was added.
enum PostureUI {
    /// Floor for the staleness threshold. The daemon's actual poll cadence — carried
    /// in the snapshot as `pollIntervalSeconds` — scales this up (see staleThreshold),
    /// so a deliberately long pollInterval doesn't make an enforcing daemon read as
    /// "stopped" between polls. 90s tolerates a couple of missed default-cadence cycles.
    static let staleFloor: TimeInterval = 90

    /// The age past which a snapshot reads as stopped, derived from the daemon's own
    /// poll cadence (3× the interval) so it scales with a custom pollInterval, floored
    /// at staleFloor. Falls back to the floor when the field is absent (older daemon).
    static func staleThreshold(_ s: Snapshot) -> TimeInterval {
        guard let p = s.pollIntervalSeconds, p > 0 else { return staleFloor }
        return max(staleFloor, TimeInterval(p) * 3)
    }

    /// Whether a snapshot represents a live, enforcing daemon (fresh and not
    /// stopped). Single source of truth for the icon, the menu's gating, and the
    /// window's Overview.
    static func isLive(_ s: Snapshot?) -> Bool {
        guard let s = s else { return false }
        return s.age <= staleThreshold(s) && s.posture != "stopped"
    }

    /// Maps a snapshot (or its absence/staleness) to one of the four brand states
    /// (on / off / blocked / warning — the full-color icons from gui/assets/), plus an
    /// SF Symbol fallback for running outside the assembled bundle, plus a label.
    static func iconFor(_ s: Snapshot?) -> (state: String, symbol: String, help: String) {
        guard let s = s, isLive(s) else {
            return ("off", "shield", "stopped") // gray / hollow shield: not enforcing
        }
        // A failed firewall action means the intended posture was NOT achieved (e.g. a
        // failed block leaves posture "allow" during a live leak). Surface it as a
        // warning regardless of posture so a "safe" icon never masks a failed enforce.
        if let e = s.enforcementErr, !e.isEmpty {
            return ("warning", "exclamationmark.triangle.fill", "enforcement error")
        }
        switch s.posture {
        case "standby":
            // vpn.autoArm parked: daemon alive, nothing enforced, arms on VPN
            // connect. Gray like "off" — the truthful "not enforcing" look.
            return ("off", "shield", humanPosture(s))
        case "block", "full-block":
            return ("blocked", "shield.slash.fill", humanPosture(s))
        case "switch-window":
            // The switch window relaxes egress (all outbound, or a proto/port subset
            // if restricted) — the real IP may be exposed. Never show the plain "safe"
            // icon here; warn so the user notices it's open.
            return ("warning", "exclamationmark.shield.fill", humanPosture(s))
        default: // allow, guard — enforcing normally
            // Guard with the tunnel DOWN is the guard actively doing its job:
            // physical egress is cut until the VPN comes back. The posture string
            // stays "guard" (the standing rule didn't change), but visually this
            // is a blocked state, not a calm "on" — show it as blocked so a
            // dropped VPN is impossible to miss.
            if guardHoldsDownedTunnel(s) {
                return ("blocked", "shield.slash.fill", "VPN down — egress blocked (guard)")
            }
            return ("on", "checkmark.shield.fill", humanPosture(s))
        }
    }

    static func humanPosture(_ s: Snapshot) -> String {
        switch s.posture {
        case "allow": return "allowing"
        case "block": return "blocking"
        case "standby": return "standby — waiting for VPN (not enforcing)"
        case "guard": return "guarding (VPN)"
        case "full-block": return "full block (VPN)"
        case "switch-window":
            if s.switch?.isAutoReconnect == true {
                return "VPN dropped — reconnect window open (redial now; real IP may be exposed)"
            }
            return "switch window — egress relaxed (real IP may be exposed)"
        case "stopped": return "stopped"
        default: return s.posture
        }
    }

    /// Guard mode holding a downed tunnel: Unblock doubles as the "my VPN is off
    /// on purpose — release the line" action (a vpn.autoArm daemon returns to
    /// standby; without autoArm it's a harmless no-op the daemon acknowledges).
    /// An EMPTY tunnel list counts as down: the zero-tunnel standing posture is a
    /// total egress cut (ModeFullBlock shape under the "guard" posture string),
    /// and the icon must never show a calm green shield while the network is cut.
    static func guardHoldsDownedTunnel(_ s: Snapshot?) -> Bool {
        guard let s = s, s.posture == "guard" else { return false }
        guard let tuns = s.tunnels, !tuns.isEmpty else { return true }
        return !tuns.contains(where: { $0.up })
    }

    /// SwiftUI accent for a brand state — used where the bundled bitmap isn't
    /// (SF Symbol fallback, text highlights).
    static func color(for state: String) -> Color {
        switch state {
        case "on": return .green
        case "blocked": return .red
        case "warning": return .orange
        default: return .secondary
        }
    }

    /// Coarsens a menu-bar state down to what the Dock tile is allowed to show.
    /// The Dock icon answers one question — "is dezhban actively cutting my
    /// traffic right now?" — so only "blocked" may stand out; "off" (stopped /
    /// standby) and "warning" (switch-window / enforcement error) collapse to the
    /// calm default "on" (guard) look instead of their own Dock badge. This is a
    /// Dock-only narrowing: the menu bar icon keeps showing all four states via
    /// `iconFor` untouched, since that's where the real-time nuance belongs.
    static func dockState(for state: String) -> String {
        state == "blocked" ? "blocked" : "on"
    }

    /// Dock-size brand state images from the app bundle's Resources (put there by
    /// build-app.sh from gui/assets/png), cached per state. Empty outside the bundle,
    /// where callers fall back to SF Symbols. Shared by the Dock tile and the
    /// Overview hero.
    private static var dockIcons: [String: NSImage] = [:]

    static func dockIcon(_ state: String) -> NSImage? {
        if let img = dockIcons[state] { return img }
        guard let url = Bundle.main.url(forResource: "dock-state-\(state)", withExtension: "png"),
              let img = NSImage(contentsOf: url) else { return nil }
        dockIcons[state] = img
        return img
    }

    static func agoString(_ seconds: TimeInterval) -> String {
        let s = Int(seconds.rounded())
        if s < 60 { return "\(s)s ago" }
        return "\(s / 60)m ago"
    }

    /// Round DOWN so the countdown never shows more time than is actually left
    /// (e.g. 59.6s reads "0:59", not "1:00"). For the switch-window exposure
    /// timer, under-stating the remaining time is the safe direction.
    static func mmss(_ seconds: TimeInterval) -> String {
        let s = max(0, Int(seconds.rounded(.down)))
        return String(format: "%d:%02d", s / 60, s % 60)
    }
}

/// The Logs & Diagnostics pane's backing store: one shared monospaced transcript
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
    @Published var selectedSection: SidebarSection? = .overview
    /// Last update check result (nil: none run yet, or the last one found
    /// nothing worth reporting — see UpdateChecker.check's doc comment on why
    /// a failure never surfaces as an error here).
    @Published var updateCheck: UpgradeCheckResult?

    let console = Console()

    var isLive: Bool { PostureUI.isLive(snapshot) }

    /// Routes a finished transcript into the Logs & Diagnostics pane and
    /// navigates there — the window-side output surface for long actions.
    func showInLogs(title: String, text: String) {
        console.set(title: title, text: text.isEmpty ? "(no output)" : text)
        selectedSection = .logs
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
            let installed = DezhbanCLI.serviceInstalled()
            let control = DezhbanCLI.daemonControlReachable()
            DispatchQueue.main.async {
                self?.serviceIsInstalled = installed
                self?.controlIsReachable = control
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
