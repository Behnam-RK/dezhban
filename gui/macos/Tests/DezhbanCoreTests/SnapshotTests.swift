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

    @Test func decodesARefusedRedialWindow() {
        let json = """
        { "time": "2026-07-25T10:00:00Z", "posture": "guard", "blocked": false,
          "drop": { "at": "2026-07-25T09:58:00Z" },
          "redial": { "reason": "exhausted", "nextEligible": "2026-07-25T10:13:00Z",
                      "remainingSeconds": 0 } }
        """.data(using: .utf8)!
        let s = try! #require(StateReader.decode(json))
        #expect(s.redial?.reason == "exhausted")
        #expect(s.redial?.remainingSeconds == 0)
        // Omitted by the daemon when the budget, not the backoff, refused —
        // `omitempty` on the Go side means absent, and absent must decode.
        #expect(s.redial?.fastDrops == nil)
    }

    /// Every date in the contract is `omitzero` on the Go side, so "the writer
    /// had no value" arrives as a MISSING KEY rather than as
    /// "0001-01-01T00:00:00Z" — which the ISO8601 decoder refuses, and one
    /// unreadable date fails the whole Snapshot, blanking the menubar over a
    /// detail nothing displays. This is the shape that must never throw.
    @Test func absentDatesDoNotFailTheDecode() {
        let json = """
        { "time": "2026-07-25T10:00:00Z", "posture": "switch-window", "blocked": false,
          "switch": { "open": true, "trigger": "auto" },
          "drop": {}, "hold": { "armed": true } }
        """.data(using: .utf8)!
        let s = try! #require(StateReader.decode(json))
        #expect(s.switch?.open == true)
        #expect(s.switch?.until == nil)
        #expect(s.drop?.at == nil)
        #expect(s.hold?.armed == true)
        #expect(s.hold?.at == nil)
    }

    /// A window with no deadline is still an open window: the countdown is
    /// dropped, never rendered as "0:00 left", which would count down to a
    /// moment nobody wrote down.
    @Test func aWindowWithoutADeadlineShowsNoCountdown() {
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        let dated = SwitchState(open: true, until: now.addingTimeInterval(90),
                                profile: nil, trigger: "auto")
        #expect(dated.timeLeft(asOf: now) == 90)
        #expect(dated.leftSuffix(asOf: now) == " (1:30 left)")

        let undated = SwitchState(open: true, until: nil, profile: nil, trigger: "auto")
        #expect(undated.timeLeft(asOf: now) == nil)
        #expect(undated.leftSuffix(asOf: now) == "")
    }

    /// A deadline already in the past clamps to zero rather than going negative,
    /// so a late poll cannot render "-0:03 left".
    @Test func aPassedDeadlineClampsToZero() {
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        let stale = SwitchState(open: true, until: now.addingTimeInterval(-30),
                                profile: nil, trigger: "manual")
        #expect(stale.timeLeft(asOf: now) == 0)
    }

    /// A refusal whose `nextEligible` the writer had no value for. Go omits it
    /// (`omitzero`) rather than publishing "0001-01-01T00:00:00Z", which the
    /// ISO8601 decoder refuses — and a throwing date inside `redial` would fail
    /// the WHOLE Snapshot decode, blanking the menubar over a missing detail on
    /// an optional sub-object. The rest of the refusal must still decode.
    @Test func aRefusalWithoutATimeStillDecodes() {
        let json = """
        { "time": "2026-07-25T10:00:00Z", "posture": "guard", "blocked": false,
          "redial": { "reason": "cooldown", "remainingSeconds": 12.5 } }
        """.data(using: .utf8)!
        let s = try! #require(StateReader.decode(json))
        #expect(s.redial?.reason == "cooldown")
        #expect(s.redial?.nextEligible == nil)
        #expect(s.redial?.remainingSeconds == 12.5)
    }

    /// The additive rule again, for the field this release adds: every snapshot
    /// an older daemon ever wrote lacks `redial`, and the app has to keep reading
    /// them. Absent means "nothing refused", never "no budget" and never a
    /// decode failure that would blank the menubar.
    @Test func absentRedialIsNotAFailure() {
        let json = """
        { "time": "2026-07-25T10:00:00Z", "posture": "guard", "blocked": false,
          "drop": { "at": "2026-07-25T09:58:00Z" } }
        """.data(using: .utf8)!
        let s = try! #require(StateReader.decode(json))
        #expect(s.redial == nil)
        #expect(s.drop?.at != nil)
    }
}
