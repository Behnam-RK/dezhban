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
}
