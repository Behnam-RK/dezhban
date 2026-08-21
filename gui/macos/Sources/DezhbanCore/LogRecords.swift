import Foundation

/// One record from dezhban's own log — `dezhban logs --json`, mirroring Go's
/// `logread.Record`.
///
/// Parsed on the Go side, because that is where the format is written. A second
/// parser here would be a second thing to get wrong about slog's quoting, and
/// it could not be tested against the writer.
public struct LogRecord: Identifiable, Hashable {
    public let time: Date?
    public let level: String
    public let msg: String
    public let attrs: [(key: String, value: String)]
    /// The original line. A surface can always fall back to showing exactly
    /// what was written, which matters most for a line the parser did not fully
    /// understand — those are kept rather than dropped.
    public let raw: String

    /// Position within the fetched batch. The log has no id of its own, and two
    /// records can share a timestamp, a level and a message — a retry loop
    /// produces exactly that — so anything derived from the content would
    /// collapse them in a List.
    public let id: Int

    public init(id: Int, time: Date?, level: String, msg: String,
                attrs: [(key: String, value: String)] = [], raw: String = "") {
        self.id = id
        self.time = time
        self.level = level
        self.msg = msg
        self.attrs = attrs
        self.raw = raw
    }

    public static func == (a: LogRecord, b: LogRecord) -> Bool { a.id == b.id && a.raw == b.raw }
    public func hash(into hasher: inout Hasher) {
        hasher.combine(id)
        hasher.combine(raw)
    }

    public var isError: Bool { level.uppercased() == "ERROR" }
    public var isWarning: Bool { level.uppercased().hasPrefix("WARN") }

    /// The attrs as one trailing line, in the order the daemon wrote them —
    /// that order reads as a sentence, which is why Go keeps them as pairs
    /// rather than a map.
    public var detail: String {
        attrs.map { "\($0.key)=\($0.value)" }.joined(separator: "  ")
    }

    /// Decoded by hand rather than through Codable: `attrs` is an ordered array
    /// of pairs, and the timestamp arrives in Go's RFC 3339 form with
    /// fractional seconds, which Foundation's `.iso8601` strategy rejects.
    public static func decodeList(_ data: Data) -> [LogRecord]? {
        guard let raw = try? JSONSerialization.jsonObject(with: data) as? [[String: Any]] else {
            return nil
        }
        return raw.enumerated().map { index, obj in
            var attrs: [(key: String, value: String)] = []
            if let list = obj["attrs"] as? [[String: Any]] {
                attrs = list.compactMap { a in
                    guard let k = a["key"] as? String else { return nil }
                    return (k, a["value"] as? String ?? "")
                }
            }
            return LogRecord(
                id: index,
                time: (obj["time"] as? String).flatMap(parseTime),
                level: obj["level"] as? String ?? "INFO",
                msg: obj["msg"] as? String ?? "",
                attrs: attrs,
                raw: obj["raw"] as? String ?? "")
        }
    }

    /// A record whose timestamp did not parse is still a record. Returning nil
    /// for the date rather than dropping the line keeps the one rule this whole
    /// path lives by: never silently discard a log record.
    private static func parseTime(_ s: String) -> Date? {
        for f in [fractional, plain] {
            if let d = f.date(from: s) { return d }
        }
        return nil
    }

    private static let fractional = formatter("yyyy-MM-dd'T'HH:mm:ss.SSSSSSZZZZZ")
    private static let plain = formatter("yyyy-MM-dd'T'HH:mm:ssZZZZZ")

    private static func formatter(_ format: String) -> DateFormatter {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = format
        return f
    }
}
