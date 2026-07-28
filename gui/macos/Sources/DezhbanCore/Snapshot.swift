import Foundation

/// One VPN tunnel's observed state — mirrors Go's `state.Tunnel`.
public struct Tunnel: Codable {
    public let name: String?
    public let up: Bool
    public let detail: String?
}

/// An open switch window — mirrors Go's `state.SwitchState`.
public struct SwitchState: Codable {
    public let open: Bool
    /// The window's deadline, absent when the writer had none. Optional to match
    /// Go's `omitzero`: publishing a zero `time.Time` would emit a year-1
    /// timestamp the ISO8601 decoder refuses, and one unreadable date fails the
    /// WHOLE Snapshot decode — the menubar would read "stopped" while dezhban is
    /// enforcing. `render.windowDisplay` already omits the deadline clause in
    /// this case; a countdown with no deadline is simply not shown.
    public let until: Date?
    public let profile: String?
    /// "manual" (operator command), "auto" (automatic redial window opened by a
    /// tunnel drop), or "pause" (deliberate operator pause). Absent from older
    /// daemons — treat nil as "manual".
    public let trigger: String?

    public var isAutoRedial: Bool { trigger == "auto" }

    /// Seconds left on the window as of `now`, or nil when the writer published
    /// no deadline. One helper rather than an `if let` at each of the five
    /// display sites, so they cannot drift on what a missing deadline means:
    /// every one of them drops the countdown and keeps the rest of the label. A
    /// window with no stated end is still an open window, and saying "0:00 left"
    /// would be a countdown to a moment nobody wrote down.
    public func timeLeft(asOf now: Date) -> TimeInterval? {
        guard let until else { return nil }
        return max(0, until.timeIntervalSince(now))
    }

    /// The " (2:31 left)" a button title carries, or "" when there is no
    /// deadline to count down to. Separate from timeLeft because the three
    /// button sites all want exactly this and would otherwise each spell the
    /// same optional-to-string dance.
    public func leftSuffix(asOf now: Date) -> String {
        guard let left = timeLeft(asOf: now) else { return "" }
        return " (\(PostureUI.mmss(left)) left)"
    }

    /// A pause is a deliberate drop to the real ISP IP, not a VPN problem, so it
    /// reads and looks different from the other two even though all three share
    /// the same bounded-window machinery underneath.
    public var isPause: Bool { trigger == "pause" }
}

/// The moment a healthy tunnel went down — mirrors Go's `state.DropRecord`.
/// Carried from the drop until a tunnel is up again,
/// including across the redial window that follows, which is the only reason any
/// surface can report the drop at all.
///
/// It carries the moment and nothing else — see Go's DropRecord for why a "was
/// traffic cut" companion flag could not be rendered truthfully.
public struct DropRecord: Codable {
    /// Optional to match Go's `omitzero` — see `SwitchState.until` for why a
    /// zero date must arrive as an absent key rather than as year 1.
    public let at: Date?
}

/// "Hold the line" is armed — mirrors Go's `state.HoldState`. The next tunnel
/// drop stays cut instead of opening an automatic redial window, so a deliberate
/// disconnect does not get a relaxation the user never asked for.
///
/// It only ever suppresses, so it is not a fourth relaxation trigger and the
/// icon rules are unaffected: it makes the RED state reachable rather than
/// introducing a new colour.
public struct HoldState: Codable {
    public let armed: Bool
    /// Optional to match Go's `omitzero` — see `SwitchState.until`. Nothing
    /// displays it; `armed` is the whole signal.
    public let at: Date?
}

/// The automatic redial window was REFUSED for the drop being carried — mirrors
/// Go's `state.RedialState`. Present only while such a refusal stands; a granted
/// window is reported by `switch` instead.
///
/// The app does not compose a sentence from these fields. `display.detail`
/// already says it, rendered by the same Go code the CLI uses, so both surfaces
/// say the same thing. This is here for anything that needs the facts rather
/// than the prose — and because decoding what the daemon writes is how the
/// contract stays honest.
public struct RedialState: Codable {
    /// Stable identifier: "cooldown" (backing off after fast drops) or
    /// "exhausted" (the rolling budget is spent). Match on it, don't display it.
    public let reason: String
    /// Earliest instant a window could open — a bound, not a promise. dezhban
    /// re-takes the decision when this instant arrives, so it is a time the
    /// guard acts on rather than one it merely reports; but the re-decision
    /// consults the budget afresh and may refuse again. This is what makes a
    /// refusal actionable — "the guard is holding" without it is a wall, not a
    /// wait.
    ///
    /// Optional because the Go field is `omitzero`: a writer with no instant
    /// omits the key rather than publishing "0001-01-01T00:00:00Z", and an
    /// absent key is the one shape this decoder handles for free. The `?` is
    /// what buys that — note it would NOT rescue a year-1 value that was
    /// actually written, because `decode`'s custom date strategy throws on any
    /// string `ISO8601DateFormatter` refuses whether the property is optional
    /// or not, and one throwing `Date` fails the WHOLE `Snapshot` decode: then
    /// `StateReader.decode` returns nil and the menubar reads "stopped" while
    /// dezhban is enforcing. `omitzero` on the Go side is the guard; this is
    /// the half that makes its output decode cleanly.
    public let nextEligible: Date?
    /// What is left of the rolling budget, in seconds.
    public let remainingSeconds: Double
    /// Consecutive fast drops behind the current backoff; absent when the budget
    /// rather than the backoff is what refused.
    public let fastDrops: Int?
}

/// The daemon's posture at a point in time — mirrors Go's `state.Snapshot`.
/// JSON keys match the lowerCamelCase struct tags in internal/state/state.go.
public struct Snapshot: Codable {
    public let time: Date
    // No `mode`. dezhban has a single enforcement model now, so the field was
    // removed from the daemon's snapshot rather than frozen at one value. It was
    // non-optional here, which means leaving it would have failed decoding of
    // every snapshot the new daemon writes. `posture` carries the real state.
    public let posture: String         // "standby" | "guard" | "full-block" | "switch-window" | "stopped"
    public let blocked: Bool
    public let ip: String?
    public let countryCode: String?
    public let provider: String?
    public let lookupErr: String?          // a GENUINE failure: a tunnel was up and measuring it failed
    public let exitUnknown: String?        // EXPECTED: no tunnel up, so there is no exit to measure
    public let enforcementErr: String?     // last firewall-action failure, nil when clear
    public let tunnels: [Tunnel]?
    public let endpoints: [String]?
    public let pollIntervalSeconds: Int?   // daemon poll cadence, for sizing staleness
    public let blockedCountries: [String]?
    public let pid: Int?
    public let activeProfile: String?      // matched VPN profile name, nil if unknown
    public let `switch`: SwitchState?      // present only while a switch window is open
    public let pending: PendingFlip?       // present only while a posture change is being counted toward
    public let display: Display?          // the rendered sentence — nil from an older daemon
    public let drop: DropRecord?          // present from a tunnel drop until a tunnel is up again
    public let hold: HoldState?           // present only while "hold the line" is armed
    public let redial: RedialState?       // present only while a redial window stands refused

    /// Wall-clock age of this snapshot.
    public var age: TimeInterval { Date().timeIntervalSince(time) }

    /// The next VPN drop will stay cut rather than opening a redial window.
    public var holdArmed: Bool { hold?.armed ?? false }
}

/// The rendered form of a Snapshot — mirrors Go's `render.Display` (carried via
/// `state.Display`). `key` is the stable machine classification
/// ("on"/"off"/"blocked"/"warning"/"paused") and is also a PNG filename
/// component (menubar-state-<key>.png, dock-state-<key>.png); `headline` and
/// `detail` are the sentences every surface displays instead of composing its
/// own. Optional because an older daemon's snapshot won't have one.
public struct Display: Codable {
    public let key: String
    public let headline: String
    public let detail: String
}

/// A posture change the daemon is counting toward — mirrors Go's
/// `state.PendingFlip`. Hysteresis requires `need` consecutive agreeing readings
/// and `have` have arrived.
///
/// It exists because the posture alone cannot tell "recovering, one check to go"
/// from "stuck", and after a VPN redials out of a full block those look identical
/// for as long as the streak takes.
public struct PendingFlip: Codable {
    public let to: String
    public let have: Int
    public let need: Int
}

/// Reads and decodes the daemon's state file. The daemon (Go) marshals `time` as
/// RFC3339, sometimes with fractional seconds; `.iso8601` alone rejects the
/// fractional form, so we try both formatters.
public enum StateReader {
    /// Default path the daemon publishes to (see cmd/dezhban defaultStatePath).
    public static let defaultPath = "/var/db/dezhban/state.json"

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

    /// Decodes a snapshot from raw JSON data, or nil if it doesn't parse. Shared
    /// by `read` (file-backed) and tests (in-memory fixtures).
    public static func decode(_ data: Data) -> Snapshot? {
        try? decoder.decode(Snapshot.self, from: data)
    }

    /// Reads the snapshot, or nil if the file is missing/unreadable/unparsable
    /// (all of which the caller renders as "stopped/unknown").
    public static func read(path: String = defaultPath) -> Snapshot? {
        guard let data = FileManager.default.contents(atPath: path) else { return nil }
        return decode(data)
    }

    /// The state file's last-modified time, or nil if absent. Lets a poller skip
    /// re-decoding an unchanged file (the daemon rewrites it only ~every poll).
    public static func modificationTime(path: String = defaultPath) -> Date? {
        (try? FileManager.default.attributesOfItem(atPath: path))?[.modificationDate] as? Date
    }
}
