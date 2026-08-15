import Foundation
import DezhbanCore

/// UserDefaults storage for `LaunchVisibility` — the "open minimized?" setting.
///
/// A flag in UserDefaults, not in the config, for the same reason the first-run
/// flag and the notify/update toggles are: the config belongs to the daemon and
/// writing it needs root. This one is purely about this app's own window on
/// this Mac.
///
/// Unrecognised or absent values fall back to `.bootOnly`, which is what the
/// app did unconditionally before this was configurable — so an upgrade is a
/// no-op for anyone who never touches the setting.
enum LaunchPreference {
    private static let key = "launchVisibility"

    static var current: LaunchVisibility {
        get {
            (UserDefaults.standard.string(forKey: key).flatMap(LaunchVisibility.init(rawValue:)))
                ?? .bootOnly
        }
        set { UserDefaults.standard.set(newValue.rawValue, forKey: key) }
    }
}
