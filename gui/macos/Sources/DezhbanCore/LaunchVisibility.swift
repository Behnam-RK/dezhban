import Foundation

/// Whether the main window opens when the app starts.
///
/// The rule is split from the UserDefaults flag that feeds it (see
/// `LaunchPreference` in DezhbanMenu) for the same reason `SetupQuestions`
/// splits its own: the decision has a right and a wrong answer and is worth a
/// test, while the storage is a one-line `UserDefaults` read.
///
/// This is an app preference, never a daemon config key. The config belongs to
/// the daemon, lives root-owned under /etc/dezhban, and `config set` is in the
/// privileged set — charging a password prompt for a window-appearance choice
/// would be absurd, and the daemon has no business knowing. Same call as the
/// first-run flag (FirstRunView) and the notify/update toggles.
public enum LaunchVisibility: String, CaseIterable, Identifiable, Sendable {
    /// Never start minimized — the window opens on every launch.
    case never
    /// Always start minimized — the window never opens by itself.
    case always
    /// Start minimized only when macOS launched us at login/boot. The default,
    /// and what the app did unconditionally before this was configurable.
    case bootOnly

    public var id: String { rawValue }

    public var label: String {
        switch self {
        case .never: return "Never"
        case .always: return "Always"
        case .bootOnly: return "Only at login"
        }
    }

    /// The one-line consequence, for a Settings help string.
    public var detail: String {
        switch self {
        case .never: return "The window opens every time Dezhban starts."
        case .always: return "Dezhban stays in the menu bar; open the window yourself."
        case .bootOnly: return "Hidden when macOS starts Dezhban at login, shown when you start it."
        }
    }

    /// Whether to open the main window for this launch.
    ///
    /// `backgroundLaunch` is true when macOS started the app at login rather
    /// than the user starting it. It is read from `--background` in
    /// `CommandLine.arguments`, which only the login LaunchAgent passes (see
    /// `LoginItem` and docs/adr/0014-login-item-launch-marker.md) — an explicit
    /// marker, not a heuristic. The predecessor asked AppKit's
    /// `launchIsDefaultUserInfoKey` and got the answer wrong in both
    /// directions: the window appeared at login and failed to appear on a
    /// manual launch.
    ///
    /// The absent argument therefore reads as a user launch, which is the safe
    /// default: the worst case is a window the user did not ask for, never a
    /// window they cannot reach.
    ///
    /// Note this governs the LAUNCH only. The Dock icon and the menubar's
    /// "Open Dezhban…" open the window regardless, in every mode — a
    /// preference about startup noise must never become a way to lose access
    /// to the window.
    public func opensWindow(backgroundLaunch: Bool) -> Bool {
        switch self {
        case .never: return true
        case .always: return false
        case .bootOnly: return !backgroundLaunch
        }
    }

    /// The launch marker the login LaunchAgent passes. Public so the app and
    /// its tests name the same string, and so a rename cannot drift away from
    /// `LoginAgent.plist`.
    public static let backgroundArgument = "--background"

    /// Reads the marker out of a process argument list.
    public static func isBackgroundLaunch(arguments: [String]) -> Bool {
        arguments.contains(backgroundArgument)
    }
}
