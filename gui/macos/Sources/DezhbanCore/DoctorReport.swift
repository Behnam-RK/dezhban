import Foundation

/// Mirrors Go's `doctorDetail` (cmd/dezhban). One of a check's findings: the
/// identifier it is ABOUT, carried as data, plus the prose that qualifies it.
///
/// The identifier is a field rather than a word inside `text` because the
/// diagnostic bundle's redactor works by KEY — a value under `iface`, `profile`
/// or `endpoint` is recognised for what it is and replaced with a stable token,
/// while the same word inside prose is reachable only by a literal
/// word-replacement pass over free text. Composing the sentence is this view's
/// job, which is why `line` lives here and mirrors Go's `doctorDetail.line`.
///
/// An empty `text` with no identifier is a PARAGRAPH BREAK, not a finding.
public struct DoctorDetail: Codable, Hashable {
    public let iface: String?
    public let profile: String?
    public let endpoint: String?
    public let text: String

    public init(iface: String? = nil, profile: String? = nil, endpoint: String? = nil, text: String) {
        self.iface = iface
        self.profile = profile
        self.endpoint = endpoint
        self.text = text
    }

    /// The composed human form, identical to what `dezhban doctor` prints: the
    /// finding's subject, its prose, and — when a finding is about an endpoint
    /// AND the interface it is misrouted onto — the interface in parentheses.
    public var line: String {
        let subject = endpoint ?? profile ?? iface ?? ""
        var out = subject
        if !text.isEmpty {
            if subject.isEmpty || text.hasPrefix(":") {
                // A port joins its address with no separator: `1.2.3.4:51820`.
                out += text
            } else {
                out += " " + text
            }
        }
        if let iface, !iface.isEmpty, iface != subject {
            out += " (\(iface))"
        }
        return out
    }
}

/// Mirrors Go's `doctorCheck` (cmd/dezhban's `dezhban doctor --json`). `status`
/// is one of "ok"/"warn"/"fail" — kept as the raw string (not an enum) so an
/// unrecognised future value decodes rather than failing the whole report.
public struct DoctorCheck: Codable, Identifiable {
    public var id: String { name }
    public let name: String
    public let status: String
    public let summary: String
    public let profiles: [String]?
    public let connectedVPN: String?
    public let details: [DoctorDetail]?
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
