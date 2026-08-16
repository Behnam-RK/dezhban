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
    /// `deliberateLaunch` is AppKit's `launchIsDefaultUserInfoKey`: false when
    /// the launch was performed on the user's behalf (a login item, state
    /// restoration, opening a file) rather than at their request. A missing
    /// flag reads as an ordinary launch, so the window still opens if AppKit
    /// ever stops reporting it — the caller supplies that default.
    ///
    /// Note this governs the LAUNCH only. The Dock icon and the menubar's
    /// "Open Dezhban…" open the window regardless, in every mode — a
    /// preference about startup noise must never become a way to lose access
    /// to the window.
    public func opensWindow(deliberateLaunch: Bool) -> Bool {
        switch self {
        case .never: return true
        case .always: return false
        case .bootOnly: return deliberateLaunch
        }
    }
}
