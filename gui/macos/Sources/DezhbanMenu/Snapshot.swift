import Foundation

/// One VPN tunnel's observed state — mirrors Go's `state.Tunnel`.
struct Tunnel: Codable {
    let name: String?
    let up: Bool
    let detail: String?
}

/// An open switch window — mirrors Go's `state.SwitchState`.
struct SwitchState: Codable {
    let open: Bool
    let until: Date
    let profile: String?
    /// "manual" (operator command) or "auto" (automatic reconnect window opened
    /// by a tunnel drop). Absent from older daemons — treat nil as "manual".
    let trigger: String?

    var isAutoReconnect: Bool { trigger == "auto" }
}

/// The daemon's posture at a point in time — mirrors Go's `state.Snapshot`.
/// JSON keys match the lowerCamelCase struct tags in internal/state/state.go.
struct Snapshot: Codable {
    let time: Date
    // No `mode`. dezhban has a single enforcement model now, so the field was
    // removed from the daemon's snapshot rather than frozen at one value. It was
    // non-optional here, which means leaving it would have failed decoding of
    // every snapshot the new daemon writes. `posture` carries the real state.
    let posture: String         // "standby" | "guard" | "full-block" | "switch-window" | "stopped"
    let blocked: Bool
    let ip: String?
    let countryCode: String?
    let provider: String?
    let lookupErr: String?
    let enforcementErr: String?     // last firewall-action failure, nil when clear
    let tunnels: [Tunnel]?
    let endpoints: [String]?
    let pollIntervalSeconds: Int?   // daemon poll cadence, for sizing staleness
    let blockedCountries: [String]?
    let pid: Int?
    let activeProfile: String?      // matched VPN profile name, nil if unknown
    let `switch`: SwitchState?      // present only while a switch window is open

    /// Wall-clock age of this snapshot.
    var age: TimeInterval { Date().timeIntervalSince(time) }
}

/// Reads and decodes the daemon's state file. The daemon (Go) marshals `time` as
/// RFC3339, sometimes with fractional seconds; `.iso8601` alone rejects the
/// fractional form, so we try both formatters.
enum StateReader {
    /// Default path the daemon publishes to (see cmd/dezhban defaultStatePath).
    static let defaultPath = "/var/db/dezhban/state.json"

    private static let rfc3339: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        return f
    }()
    private static let rfc3339Frac: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()

    private static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { dec in
            let container = try dec.singleValueContainer()
            let s = try container.decode(String.self)
            if let date = rfc3339Frac.date(from: s) { return date }
            if let date = rfc3339.date(from: s) { return date }
            throw DecodingError.dataCorruptedError(
                in: container, debugDescription: "unrecognized RFC3339 date: \(s)")
        }
        return d
    }()

    /// Reads the snapshot, or nil if the file is missing/unreadable/unparsable
    /// (all of which the caller renders as "stopped/unknown").
    static func read(path: String = defaultPath) -> Snapshot? {
        guard let data = FileManager.default.contents(atPath: path) else { return nil }
        return try? decoder.decode(Snapshot.self, from: data)
    }

    /// The state file's last-modified time, or nil if absent. Lets a poller skip
    /// re-decoding an unchanged file (the daemon rewrites it only ~every poll).
    static func modificationTime(path: String = defaultPath) -> Date? {
        (try? FileManager.default.attributesOfItem(atPath: path))?[.modificationDate] as? Date
    }
}
