import Foundation

/// Per-event notification preferences for the six essential posture classes.
///
/// The classing itself stays in AppDelegate (it needs the live icon state);
/// this type owns which classes a user wants to hear about, the migration from
/// the original single "notifyEssentials" toggle, and the notification titles
/// — pure data, so all of it is testable.
public struct NotificationPrefs: Equatable {
    /// The essential classes, raw values matching AppDelegate.essentialClass's
    /// outputs exactly. `allCases` order is the order Settings lists them in.
    public enum EventClass: String, CaseIterable {
        case armed = "on"
        case blocked
        case warning
        case paused
        case standby
        case stopped

        /// The notification title, and the Settings checkbox label. Relocated
        /// from AppDelegate.essentialTitles so the string lives once.
        public var label: String {
            switch self {
            case .armed: return "Guard armed"
            case .blocked: return "Traffic cut"
            case .warning: return "Warning"
            case .paused: return "Paused — using your real IP"
            case .standby: return "Standby — nothing is being blocked"
            case .stopped: return "Guard stopped"
            }
        }
    }

    private var enabled: [EventClass: Bool]

    public init(enabled: [EventClass: Bool] = [:]) {
        self.enabled = enabled
    }

    /// Equality is what the user would observe — the effective per-class
    /// answer — not the storage dictionary's key set, which differs between
    /// "default on" (absent) and "explicitly on" (present).
    public static func == (a: NotificationPrefs, b: NotificationPrefs) -> Bool {
        EventClass.allCases.allSatisfy { a.isEnabled($0) == b.isEnabled($0) }
    }

    public func isEnabled(_ c: EventClass) -> Bool { enabled[c] ?? true }

    public mutating func set(_ c: EventClass, enabled on: Bool) { enabled[c] = on }

    /// Whether anything at all can notify — drives the master toggle and the
    /// authorization request (no point asking the OS for permission to say
    /// nothing).
    public var anyEnabled: Bool { EventClass.allCases.contains { isEnabled($0) } }

    /// The master toggle's write: off zeroes every class, on restores every
    /// class. Deliberately all-or-nothing — the disclosure's checkboxes are
    /// where partial selections are made.
    public mutating func setAll(_ on: Bool) {
        for c in EventClass.allCases { enabled[c] = on }
    }

    /// What UserDefaults persists: raw class → enabled. Only this dictionary
    /// is stored; the legacy single bool is left untouched so a downgrade
    /// still behaves.
    public var storage: [String: Bool] {
        var out: [String: Bool] = [:]
        for c in EventClass.allCases { out[c.rawValue] = isEnabled(c) }
        return out
    }

    /// Loads preferences with migration: the per-class dictionary wins when
    /// present; else the legacy single "notifyEssentials" bool fans out to all
    /// six; else everything defaults on.
    public static func from(storage: [String: Bool]?, legacyEnabled: Bool?) -> NotificationPrefs {
        if let storage, !storage.isEmpty {
            var prefs = NotificationPrefs()
            for c in EventClass.allCases {
                prefs.set(c, enabled: storage[c.rawValue] ?? true)
            }
            return prefs
        }
        var prefs = NotificationPrefs()
        if let legacyEnabled {
            prefs.setAll(legacyEnabled)
        }
        return prefs
    }

    /// Maps AppDelegate.essentialClass's string to a class. nil for a class
    /// string this build doesn't know — the CALLER must treat nil as "notify
    /// anyway" (fail-open): a new daemon state must never be silently muted by
    /// an old preference pane that has no checkbox for it.
    public static func eventClass(for raw: String) -> EventClass? {
        EventClass(rawValue: raw)
    }

    /// Whether a post for this raw class should go out: known classes follow
    /// their toggle, unknown classes fail open — but only while something is
    /// still switched on. Failing open exists so a NEW daemon state isn't
    /// silently muted by a pane with no checkbox for it; it must never
    /// override an explicit "notify me about nothing", which is what the
    /// master toggle writes (every class false).
    public func shouldNotify(rawClass: String) -> Bool {
        guard let c = Self.eventClass(for: rawClass) else { return anyEnabled }
        return isEnabled(c)
    }

    /// The Settings status line: "Notifications on for N of 6 events."
    public var summary: String {
        let on = EventClass.allCases.filter { isEnabled($0) }.count
        return "Notifications on for \(on) of \(EventClass.allCases.count) events."
    }
}
