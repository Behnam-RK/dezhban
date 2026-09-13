import Foundation
import Testing
@testable import DezhbanCore

struct DoctorReportTests {
    @Test func decodesAllOKReport() throws {
        let json = """
        {
            "checks": [
                {"name": "config", "status": "ok", "summary": "OK (loaded and validated)"},
                {"name": "tunnels", "status": "ok", "summary": "",
                 "details": [{"iface": "utun4", "text": "— 10.0.0.0/24"}]}
            ],
            "ok": true
        }
        """.data(using: .utf8)!

        let report = try #require(DoctorReport.decode(json))
        #expect(report.ok)
        #expect(report.checks.count == 2)
        #expect(report.checks[1].details?.first?.line == "utun4 — 10.0.0.0/24")
    }

    @Test func decodesFailingCheckWithFixes() throws {
        let json = """
        {
            "checks": [
                {
                    "name": "endpoints",
                    "status": "fail",
                    "summary": "",
                    "details": [{"endpoint": "10.0.0.1", "iface": "utun4",
                                 "text": "— MISCONFIGURED: inside its subnet 10.0.0.0/24"}],
                    "fixes": ["10.0.0.1 is a tunnel-internal address..."]
                }
            ],
            "ok": false
        }
        """.data(using: .utf8)!

        let report = try #require(DoctorReport.decode(json))
        #expect(!report.ok)
        #expect(report.checks[0].status == "fail")
        #expect(report.checks[0].fixes?.count == 1)
        // The identifier is a field, and composing it back is this side's job —
        // the same sentence the CLI prints.
        #expect(report.checks[0].details?.first?.line
            == "10.0.0.1 — MISCONFIGURED: inside its subnet 10.0.0.0/24 (utun4)")
    }

    /// Additive keys must not fail the decode. Every carrier this report gained
    /// is optional for exactly this reason: JSONDecoder fails the WHOLE
    /// DoctorReport on a shape it cannot read, so `decode` returns nil, the
    /// Diagnostics pane falls back to its error state, and DoctorAttention stops
    /// badging the sidebar — an app that silently goes quiet about a warning.
    @Test func unknownAndNewCheckFieldsDoNotFailTheDecode() throws {
        let json = """
        {
            "checks": [{
                "name": "discover", "status": "ok", "summary": "",
                "profiles": ["work"],
                "connectedVPN": "Mullvad",
                "somethingAddedLater": 7,
                "details": [{"endpoint": "198.51.100.7", "text": ":51820"}]
            }],
            "ok": true
        }
        """.data(using: .utf8)!

        let report = try #require(DoctorReport.decode(json))
        #expect(report.checks[0].connectedVPN == "Mullvad")
        #expect(report.checks[0].profiles == ["work"])
        #expect(report.checks[0].details?.first?.line == "198.51.100.7:51820")
    }

    /// A detail with no identifier and no text is the paragraph break, and both
    /// renderers depend on it staying distinguishable from a finding.
    @Test func anEmptyDetailIsAParagraphBreak() throws {
        let json = """
        { "checks": [{"name": "config", "status": "ok", "summary": "",
                      "details": [{"text": ""}]}], "ok": true }
        """.data(using: .utf8)!

        let report = try #require(DoctorReport.decode(json))
        #expect(report.checks[0].details?.first?.line.isEmpty == true)
    }

    @Test func decodesCheckWithNoDetailsOrFixes() throws {
        let json = """
        { "checks": [{"name": "config", "status": "ok", "summary": "OK"}], "ok": true }
        """.data(using: .utf8)!

        let report = try #require(DoctorReport.decode(json))
        #expect(report.checks[0].details == nil)
        #expect(report.checks[0].fixes == nil)
    }

    @Test func corruptDataFailsToDecode() {
        #expect(DoctorReport.decode("not json".data(using: .utf8)!) == nil)
    }
}

extension DoctorReportTests {
    /// The same table as Go's TestTheComposedDetailLineIsTheSameOnBothSides in
    /// cmd/dezhban/doctor_test.go. Two renderers compose this sentence and they
    /// must never disagree about what a check found; changing one side without
    /// the other is the drift this pins.
    @Test func theComposedDetailLineIsTheSameOnBothSides() {
        let cases: [(DoctorDetail, String)] = [
            (DoctorDetail(text: ""), ""),
            (DoctorDetail(text: "plain prose"), "plain prose"),
            (DoctorDetail(iface: "nordlynx", text: "— no subnet"), "nordlynx — no subnet"),
            (DoctorDetail(iface: "nordlynx", text: ""), "nordlynx"),
            (DoctorDetail(profile: "work-nord", text: "— 2 stored"), "work-nord — 2 stored"),
            (DoctorDetail(endpoint: "1.2.3.4", text: ":51820"), "1.2.3.4:51820"),
            (DoctorDetail(iface: "utun4", endpoint: "1.2.3.4", text: "— MISCONFIGURED"),
             "1.2.3.4 — MISCONFIGURED (utun4)"),
            (DoctorDetail(iface: "utun4", profile: "p", endpoint: "1.2.3.4", text: "— x"),
             "1.2.3.4 — x (utun4)"),
            (DoctorDetail(iface: "utun4", profile: "p", text: "— x"), "p — x (utun4)"),
            (DoctorDetail(iface: "utun4", text: ":51820"), "utun4:51820"),
        ]
        for (detail, want) in cases {
            #expect(detail.line == want)
        }
    }
}
