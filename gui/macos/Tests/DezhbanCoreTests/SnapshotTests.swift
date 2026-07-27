import Foundation
import Testing
@testable import DezhbanCore

struct SnapshotTests {
    @Test func decodesFullSnapshotWithDisplay() throws {
        let json = """
        {
            "time": "2026-07-25T10:00:00Z",
            "posture": "guard",
            "blocked": false,
            "ip": "203.0.113.9",
            "countryCode": "US",
            "provider": "geojs",
            "tunnels": [{"name": "utun4", "up": true, "detail": null}],
            "endpoints": ["203.0.113.1"],
            "pollIntervalSeconds": 15,
            "blockedCountries": ["IR", "RU"],
            "activeProfile": "home",
            "display": {"key": "on", "headline": "Guarding", "detail": "Traffic leaves only through your VPN tunnel."}
        }
        """.data(using: .utf8)!

        let snap = try #require(StateReader.decode(json))
        #expect(snap.posture == "guard")
        #expect(snap.display?.key == "on")
        #expect(snap.display?.headline == "Guarding")
        #expect(snap.tunnels?.first?.name == "utun4")
        #expect(snap.activeProfile == "home")
    }

    @Test func decodesOldDaemonSnapshotWithNoDisplayField() throws {
        // An older daemon predating internal/render: no `display` key at all.
        let json = """
        { "time": "2026-01-01T00:00:00Z", "posture": "standby", "blocked": false }
        """.data(using: .utf8)!

        let snap = try #require(StateReader.decode(json))
        #expect(snap.display == nil)
        #expect(snap.posture == "standby")
    }

    @Test func decodesBothRFC3339DateForms() {
        let withFraction = """
        { "time": "2026-07-25T10:00:00.123456Z", "posture": "guard", "blocked": false }
        """.data(using: .utf8)!
        let withoutFraction = """
        { "time": "2026-07-25T10:00:00Z", "posture": "guard", "blocked": false }
        """.data(using: .utf8)!

        #expect(StateReader.decode(withFraction) != nil)
        #expect(StateReader.decode(withoutFraction) != nil)
    }

    @Test func corruptDataFailsToDecode() {
        let garbage = "not json at all".data(using: .utf8)!
        #expect(StateReader.decode(garbage) == nil)
    }

    @Test func missingRequiredFieldFailsToDecode() {
        // `posture` and `blocked` are non-optional; a snapshot missing them must
        // not decode into some partially-valid struct.
        let json = "{ \"time\": \"2026-07-25T10:00:00Z\" }".data(using: .utf8)!
        #expect(StateReader.decode(json) == nil)
    }

    /// The drop record is what lets the app report a VPN drop at all, so it has
    /// to survive decoding — including across a window, which is precisely when
    /// the posture alone says nothing about the drop.
    @Test func decodesTheDropRecordCarriedThroughAWindow() {
        let json = """
        {
          "time": "2026-07-25T10:00:05Z", "posture": "switch-window", "blocked": false,
          "switch": { "open": true, "until": "2026-07-25T10:00:35Z", "trigger": "auto" },
          "drop": { "at": "2026-07-25T10:00:00Z" }
        }
        """.data(using: .utf8)!
        let s = try! #require(StateReader.decode(json))
        #expect(s.drop != nil)
        #expect(s.switch?.isAutoRedial == true)
        // The drop happened before the window it opened, and the two timestamps
        // must not be confused for one another.
        let dropAt = try! #require(s.drop?.at)
        let until = try! #require(s.switch?.until)
        #expect(dropAt < until)
        #expect(dropAt < s.time)
    }

    /// A key the app no longer knows about must not fail the decode. The daemon
    /// and the app ship in one package but are not guaranteed to be restarted
    /// together, so a running daemon can still be emitting a field this build
    /// has dropped — `drop.cut` is the first one that happened to. Refusing the
    /// whole snapshot over it would blank the menubar until the next restart.
    @Test func anUnknownFieldOnTheDropRecordIsIgnored() {
        let json = """
        { "time": "2026-07-25T10:00:05Z", "posture": "guard", "blocked": true,
          "drop": { "at": "2026-07-25T10:00:00Z", "cut": true } }
        """.data(using: .utf8)!
        let s = try! #require(StateReader.decode(json))
        #expect(s.drop?.at != nil)
    }

    @Test func decodesHoldTheLineArmed() {
        let json = """
        { "time": "2026-07-25T10:00:00Z", "posture": "guard", "blocked": false,
          "hold": { "armed": true, "at": "2026-07-25T09:59:00Z" } }
        """.data(using: .utf8)!
        let s = try! #require(StateReader.decode(json))
        #expect(s.holdArmed)
    }

    /// Both fields are additive: a daemon that predates them, or one where
    /// nothing is armed and nothing has dropped, must still decode. Absent must
    /// read as "not armed", never as a decode failure.
    @Test func absentDropAndHoldAreNotAFailure() {
        let json = """
        { "time": "2026-07-25T10:00:00Z", "posture": "guard", "blocked": false }
        """.data(using: .utf8)!
        let s = try! #require(StateReader.decode(json))
        #expect(s.drop == nil)
        #expect(!s.holdArmed)
    }
}
