import Foundation
import UserNotifications

/// Posts macOS notifications for ESSENTIAL posture transitions only — armed,
/// blocked, warnings (enforcement error / switch window open), standby,
/// stopped. Finer changes (country updates, tooltip rewording) never notify;
/// the transition classing lives in AppDelegate.essentialClass.
///
/// Gated by the Settings toggle (persisted in UserDefaults, default on) and
/// silently unavailable outside a proper .app bundle — UNUserNotificationCenter
/// aborts without a bundle identifier, and a bare `swift run` binary has none.
enum NotificationManager {
    private static let enabledKey = "notifyEssentials"

    /// Whether a bundle exists to notify from. Checked before every center
    /// access so the bare-binary dev loop can't crash on it.
    private static var available: Bool { Bundle.main.bundleIdentifier != nil }

    static var isEnabled: Bool {
        get { UserDefaults.standard.object(forKey: enabledKey) as? Bool ?? true }
        set {
            UserDefaults.standard.set(newValue, forKey: enabledKey)
            if newValue { requestAuthorizationIfNeeded() }
        }
    }

    /// Asks the system once for permission (no-op when already decided). Called
    /// at launch and when the Settings toggle turns on, not before every post —
    /// the OS remembers the answer.
    static func requestAuthorizationIfNeeded() {
        guard available, isEnabled else { return }
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { _, _ in }
    }

    static func post(title: String, body: String) {
        guard available, isEnabled else { return }
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = body
        let req = UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil)
        UNUserNotificationCenter.current().add(req)
    }
}
