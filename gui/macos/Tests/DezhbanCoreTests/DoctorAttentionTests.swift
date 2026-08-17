import Foundation
import Testing
@testable import DezhbanCore

struct DoctorAttentionTests {
    private func report(statuses: [String], ok: Bool) -> DoctorReport {
        let checks = statuses.enumerated().map {
            """
            {"name": "c\($0.offset)", "status": "\($0.element)", "summary": ""}
            """
        }.joined(separator: ",")
        let json = "{\"checks\": [\(checks)], \"ok\": \(ok)}".data(using: .utf8)!
        return DoctorReport.decode(json)!
    }

    @Test func nilReportIsNotAttention() {
        #expect(!DoctorAttention.needsAttention(nil))
    }

    @Test func allOKIsNotAttention() {
        #expect(!DoctorAttention.needsAttention(report(statuses: ["ok", "ok"], ok: true)))
    }

    @Test func aWarnIsAttentionEvenWhenOKIsTrue() {
        // report.ok tracks lockout-grade failures only; a warn must still badge.
        #expect(DoctorAttention.needsAttention(report(statuses: ["ok", "warn"], ok: true)))
    }

    @Test func aFailIsAttention() {
        #expect(DoctorAttention.needsAttention(report(statuses: ["fail"], ok: false)))
    }

    @Test func unknownStatusIsNotAttention() {
        #expect(!DoctorAttention.needsAttention(report(statuses: ["someday-new"], ok: true)))
    }
}
