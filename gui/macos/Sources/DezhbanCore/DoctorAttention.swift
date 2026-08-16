import Foundation

/// The sidebar's "check me" rule, kept as a pure function so it is testable —
/// the sidebar cell itself (AppKit) cannot be.
public enum DoctorAttention {
    /// True when the last doctor report contains anything a person should look
    /// at: any check whose status is "warn" or "fail". Deliberately not
    /// `!report.ok` — the report's own `ok` tracks only lockout-grade
    /// failures, and a warn-only report would clear a flag it should raise.
    /// nil (no report yet) is false: not having looked is not a warning.
    /// An unrecognised future status is false too — unknown is not attention.
    public static func needsAttention(_ report: DoctorReport?) -> Bool {
        guard let report else { return false }
        return report.checks.contains { $0.status == "warn" || $0.status == "fail" }
    }
}
