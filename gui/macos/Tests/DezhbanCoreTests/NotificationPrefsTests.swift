import Foundation
import Testing
@testable import DezhbanCore

struct NotificationPrefsTests {
    @Test func defaultsToAllOn() {
        let prefs = NotificationPrefs.from(storage: nil, legacyEnabled: nil)
        for c in NotificationPrefs.EventClass.allCases {
            #expect(prefs.isEnabled(c))
        }
        #expect(prefs.anyEnabled)
    }

    @Test func legacyOffFansOutToAllOff() {
        let prefs = NotificationPrefs.from(storage: nil, legacyEnabled: false)
        for c in NotificationPrefs.EventClass.allCases {
            #expect(!prefs.isEnabled(c))
        }
        #expect(!prefs.anyEnabled)
    }

    @Test func legacyOnFansOutToAllOn() {
        let prefs = NotificationPrefs.from(storage: nil, legacyEnabled: true)
        #expect(prefs.anyEnabled)
    }

    @Test func perClassDictionaryWinsOverLegacy() {
        let prefs = NotificationPrefs.from(
            storage: ["blocked": true, "standby": false],
            legacyEnabled: false // must be ignored once the dictionary exists
        )
        #expect(prefs.isEnabled(.blocked))
        #expect(!prefs.isEnabled(.standby))
        // A class the stored dictionary doesn't name defaults on.
        #expect(prefs.isEnabled(.armed))
    }

    @Test func storageRoundTrips() {
        var prefs = NotificationPrefs.from(storage: nil, legacyEnabled: nil)
        prefs.set(.paused, enabled: false)
        prefs.set(.standby, enabled: false)
        let reloaded = NotificationPrefs.from(storage: prefs.storage, legacyEnabled: true)
        #expect(reloaded == prefs)
    }

    @Test func masterToggleIsAllOrNothing() {
        var prefs = NotificationPrefs.from(storage: nil, legacyEnabled: nil)
        prefs.setAll(false)
        #expect(!prefs.anyEnabled)
        prefs.setAll(true)
        #expect(prefs.anyEnabled)
        #expect(prefs.isEnabled(.stopped))
    }

    @Test func unknownClassFailsOpen() {
        var prefs = NotificationPrefs.from(storage: nil, legacyEnabled: nil)
        prefs.setAll(false)
        // A daemon state this build has no checkbox for must never be muted.
        #expect(prefs.shouldNotify(rawClass: "future-thing"))
        #expect(!prefs.shouldNotify(rawClass: "blocked"))
    }

    // The raw values are AppDelegate.essentialClass's outputs, and the labels
    // are the notification titles users have already seen — both pinned so a
    // rename shows up here, not in a silently-mismatched preference.
    @Test func rawValuesAndLabelsArePinned() {
        let want: [(NotificationPrefs.EventClass, String, String)] = [
            (.armed, "on", "Guard armed"),
            (.blocked, "blocked", "Traffic cut"),
            (.warning, "warning", "Warning"),
            (.paused, "paused", "Paused — using your real IP"),
            (.standby, "standby", "Standby — nothing is being blocked"),
            (.stopped, "stopped", "Guard stopped"),
        ]
        #expect(NotificationPrefs.EventClass.allCases.count == want.count)
        for (c, raw, label) in want {
            #expect(c.rawValue == raw)
            #expect(c.label == label)
        }
    }

    @Test func summaryCountsEnabledClasses() {
        var prefs = NotificationPrefs.from(storage: nil, legacyEnabled: nil)
        #expect(prefs.summary == "Notifications on for 6 of 6 events.")
        prefs.set(.standby, enabled: false)
        prefs.set(.stopped, enabled: false)
        #expect(prefs.summary == "Notifications on for 4 of 6 events.")
    }
}
