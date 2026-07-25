import Foundation

/// Mirrors Go's `doctorCheck` (cmd/dezhban's `dezhban doctor --json`). `status`
/// is one of "ok"/"warn"/"fail" — kept as the raw string (not an enum) so an
/// unrecognised future value decodes rather than failing the whole report.
public struct DoctorCheck: Codable, Identifiable {
    public var id: String { name }
    public let name: String
    public let status: String
    public let summary: String
    public let details: [String]?
    public let fixes: [String]?
}

/// Mirrors Go's `doctorReport`. Decoded from `dezhban doctor --json`'s stdout.
public struct DoctorReport: Codable {
    public let checks: [DoctorCheck]
    public let ok: Bool

    public static func decode(_ data: Data) -> DoctorReport? {
        try? JSONDecoder().decode(DoctorReport.self, from: data)
    }
}
