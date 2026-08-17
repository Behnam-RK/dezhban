import Foundation
import UserNotifications
import DezhbanCore

/// Posts macOS notifications for ESSENTIAL posture transitions only — armed,
/// blocked, warnings (enforcement error / switch window open), standby,
/// stopped. Finer changes (country updates, tooltip rewording) never notify;
/// the transition classing lives in AppDelegate.essentialClass, and which
/// classes a user wants to hear about lives in DezhbanCore.NotificationPrefs.
///
/// Silently unavailable outside a proper .app bundle — UNUserNotificationCenter
/// aborts without a bundle identifier, and a bare `swift run` binary has none.
enum NotificationManager {
    /// The per-class dictionary, plus the legacy single bool kept in sync as
    /// the master state, so a downgrade to a build that only knows
    /// "notifyEssentials" honours the last choice the user actually made.
    /// Mirroring rather than leaving it untouched: a user who upgrades, mutes
    /// everything, then downgrades would otherwise find notifications back on,
    /// because the stale legacy bool still said "on" from before the upgrade.
    /// Read direction is unchanged — the dictionary wins whenever it exists.
    private static let prefsKey = "notifyEvents"
    private static let legacyKey = "notifyEssentials"

    /// Whether a bundle exists to notify from. Checked before every center
    /// access so the bare-binary dev loop can't crash on it.
    private static var available: Bool { Bundle.main.bundleIdentifier != nil }

    static var prefs: NotificationPrefs {
        get {
            NotificationPrefs.from(
                storage: UserDefaults.standard.dictionary(forKey: prefsKey) as? [String: Bool],
                legacyEnabled: UserDefaults.standard.object(forKey: legacyKey) as? Bool
            )
        }
        set {
            UserDefaults.standard.set(newValue.storage, forKey: prefsKey)
            UserDefaults.standard.set(newValue.anyEnabled, forKey: legacyKey)
            if newValue.anyEnabled { requestAuthorizationIfNeeded() }
        }
    }

    /// The master state: anything at all can notify. Kept as the name the rest
    /// of the app already used, so callers (the authorization request at
    /// launch) read the same way they did with the single toggle.
    static var isEnabled: Bool { prefs.anyEnabled }

    /// Asks the system once for permission (no-op when already decided). Called
    /// at launch and when anything turns on in Settings, not before every post —
    /// the OS remembers the answer.
    static func requestAuthorizationIfNeeded() {
        guard available, isEnabled else { return }
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { _, _ in }
    }

    /// Posts one essential-transition notification, gated per class. `rawClass`
    /// is AppDelegate.essentialClass's output; a class this build has no
    /// checkbox for FAILS OPEN (NotificationPrefs.shouldNotify) — a new daemon
    /// state must never be silently muted. The title comes from the class's own
    /// label so Settings' checkbox and the banner can never disagree.
    static func post(rawClass: String, body: String) {
        guard available, prefs.shouldNotify(rawClass: rawClass) else { return }
        let content = UNMutableNotificationContent()
        content.title = NotificationPrefs.eventClass(for: rawClass)?.label ?? "Dezhban"
        content.body = body
        let req = UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil)
        UNUserNotificationCenter.current().add(req)
    }
}
