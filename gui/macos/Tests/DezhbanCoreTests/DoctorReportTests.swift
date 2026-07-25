import Foundation
import Testing
@testable import DezhbanCore

struct DoctorReportTests {
    @Test func decodesAllOKReport() throws {
        let json = """
        {
            "checks": [
                {"name": "config", "status": "ok", "summary": "OK (loaded and validated)"},
                {"name": "tunnels", "status": "ok", "summary": "", "details": ["utun4 — 10.0.0.0/24"]}
            ],
            "ok": true
        }
        """.data(using: .utf8)!

        let report = try #require(DoctorReport.decode(json))
        #expect(report.ok)
        #expect(report.checks.count == 2)
        #expect(report.checks[1].details == ["utun4 — 10.0.0.0/24"])
    }

    @Test func decodesFailingCheckWithFixes() throws {
        let json = """
        {
            "checks": [
                {
                    "name": "endpoints",
                    "status": "fail",
                    "summary": "",
                    "details": ["10.0.0.1 — MISCONFIGURED: inside utun4's subnet 10.0.0.0/24"],
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
