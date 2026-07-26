import Foundation
import Testing
@testable import DezhbanCore

struct PresetInfoTests {
    @Test func decodesPresetList() throws {
        let json = """
        [
            {"name": "strict", "summary": "Zero relaxation.", "cost": "No windows.", "matched": false},
            {"name": "balanced", "summary": "Defaults.", "cost": "Brief exposure.", "matched": true},
            {"name": "relaxed", "summary": "Longer windows.", "cost": "More exposure.", "matched": false}
        ]
        """.data(using: .utf8)!

        let list = try #require(PresetSummary.decodeList(json))
        #expect(list.count == 3)
        #expect(list[1].matched == true)
        #expect(list[1].values == nil)
    }

    @Test func decodesPresetShowWithValues() throws {
        let json = """
        {"name": "strict", "summary": "Zero relaxation.", "cost": "No windows.",
         "values": {"vpn.switchWindow": "0", "hysteresis": "1"}}
        """.data(using: .utf8)!

        let p = try JSONDecoder().decode(PresetSummary.self, from: json)
        #expect(p.values?["vpn.switchWindow"] == "0")
        #expect(p.matched == nil)
    }

    @Test func decodesPresetDiff() throws {
        let json = """
        {"preset": "balanced", "changes": [
            {"key": "hysteresis", "from": "3", "to": "2"},
            {"key": "vpn.advanced.commandFreshness", "from": "30s", "to": "45s", "restartReason": "wired up at startup"}
        ]}
        """.data(using: .utf8)!

        let diff = try #require(PresetDiff.decode(json))
        #expect(diff.preset == "balanced")
        #expect(diff.changes.count == 2)
        #expect(diff.changes[1].restartReason == "wired up at startup")
        #expect(diff.changes[0].restartReason == nil)
    }

    /// A preset whose window exceeds a cap the user lowered in Advanced cannot
    /// be written, and the picker must offer it as unavailable rather than let
    /// it be chosen and fail. An absent `conflicts` key means appliable — the
    /// Go side omits the field entirely when there is nothing to report.
    @Test func conflictsMarkAPresetUnappliable() throws {
        let json = """
        [
            {"name": "strict", "summary": "Zero relaxation.", "cost": "No windows."},
            {"name": "relaxed", "summary": "Longer windows.", "cost": "More exposure.",
             "conflicts": ["relaxed sets vpn.switchWindow 30s, above your vpn.advanced.switchWindowMax 10s — raise the cap (dezhban config set vpn.advanced.switchWindowMax=30s) or pick another preset"]}
        ]
        """.data(using: .utf8)!

        let list = try #require(PresetSummary.decodeList(json))
        #expect(list[0].conflicts == nil)
        #expect(list[0].isAppliable)
        #expect(list[1].conflicts?.count == 1)
        #expect(!list[1].isAppliable)
        #expect(list[1].conflicts?.first?.contains("vpn.advanced.switchWindowMax") == true)
    }

    @Test func corruptDataFailsToDecode() {
        #expect(PresetDiff.decode("not json".data(using: .utf8)!) == nil)
    }
}
