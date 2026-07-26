import Foundation

/// Turns a duration setting into a short list of choices a person can pick from,
/// instead of a text field that demands they know Go's duration syntax.
///
/// The choices are DERIVED from the key's own default and cap rather than listed
/// per key, so a new duration setting gets a usable control by existing, and a
/// changed default or a lowered cap moves the choices with it. Nothing here
/// hardcodes a value for any particular setting — that was the drift Phase M
/// removed, and re-adding it one layer up would be the same mistake.
public enum DurationChoices {
    /// One offered value.
    public struct Choice: Identifiable, Hashable {
        public var id: String { value }
        /// How it reads: "30 seconds", "Off".
        public let label: String
        /// The value as `config set` accepts it. "0" for Off.
        public let value: String
        /// Set for the shipped default, so a control can mark it.
        public let isDefault: Bool
        /// Set for Off, which is not a duration but a persisted opt-out.
        public let isOff: Bool
    }

    /// The multiples of the default to offer, either side of it. Chosen to span
    /// a useful range in few enough steps to stay a menu rather than a form:
    /// noticeably tighter, the default, and progressively looser.
    private static let multipliers: [Double] = [0.5, 1, 2, 4, 8]

    /// Builds the choice list for one setting.
    ///
    /// - `defaultValue`/`cap` are Go duration strings from the daemon's schema;
    ///   an unparsable or absent cap simply means unbounded.
    /// - `disablable` decides whether **Off** is offered. It must come from the
    ///   schema, never a guess: offering Off for a key whose `0` is coerced back
    ///   to the default would present a security choice that silently does
    ///   nothing.
    public static func build(defaultValue: String, cap: String?, disablable: Bool) -> [Choice] {
        guard let base = seconds(defaultValue), base > 0 else {
            // No usable default to derive from. Off may still be a real choice,
            // and Custom always is — better a short menu than a wrong one.
            return disablable ? [offChoice] : []
        }
        let ceiling = cap.flatMap(seconds).map { Int($0.rounded()) }

        var out: [Choice] = []
        if disablable {
            out.append(offChoice)
        }

        var seen = Set<Int>()
        for m in multipliers {
            let secs = Int((base * m).rounded())
            guard secs > 0 else { continue }
            // A choice above the cap is not offered at all — unlike a pause
            // length, this is not a menu of things you might want, it is a menu
            // of things that will be accepted. An option the daemon would reject
            // is noise.
            if let ceiling, secs > ceiling { continue }
            guard seen.insert(secs).inserted else { continue }
            out.append(Choice(label: humanize(secs), value: goDuration(secs),
                              isDefault: secs == Int(base.rounded()), isOff: false))
        }

        // The cap itself is always worth offering: it is the most relaxed
        // setting the operator has allowed, and reaching it by multiplying
        // upward is luck.
        if let ceiling, ceiling > 0, seen.insert(ceiling).inserted {
            out.append(Choice(label: humanize(ceiling), value: goDuration(ceiling),
                              isDefault: false, isOff: false))
        }
        return out
    }

    private static let offChoice = Choice(label: "Off", value: "0", isDefault: false, isOff: true)

    /// Parses a Go duration into whole seconds. Returns nil for anything that is
    /// not a duration — including "off", which callers handle separately.
    public static func seconds(_ text: String) -> Double? {
        let s = text.trimmingCharacters(in: .whitespaces)
        guard !s.isEmpty, s != "off" else { return nil }

        var total = 0.0
        var number = ""
        var unit = ""
        var sawAny = false

        func flush() -> Bool {
            guard let n = Double(number) else { return false }
            let scale: Double
            switch unit {
            case "h": scale = 3600
            case "m": scale = 60
            case "s": scale = 1
            case "ms": scale = 0.001
            case "us", "µs", "μs": scale = 0.000_001
            case "ns": scale = 0.000_000_001
            default: return false
            }
            total += n * scale
            sawAny = true
            number = ""
            unit = ""
            return true
        }

        for ch in s {
            if ch.isNumber || ch == "." {
                if !unit.isEmpty, !flush() { return nil }
                number.append(ch)
            } else {
                guard !number.isEmpty else { return nil }
                unit.append(ch)
            }
        }
        guard unit.isEmpty || flush(), sawAny, number.isEmpty else { return nil }
        return total
    }

    /// Renders whole seconds the way Go's Duration.String would, so a value the
    /// app writes reads back identically from `config get`.
    public static func goDuration(_ secs: Int) -> String {
        if secs == 0 { return "0s" }
        var rest = secs
        var out = ""
        let h = rest / 3600
        rest %= 3600
        let m = rest / 60
        rest %= 60
        if h > 0 { out += "\(h)h" }
        if h > 0 || m > 0 { out += "\(m)m" }
        out += "\(rest)s"
        return out
    }

    /// Renders a duration in plain words, for a label a person reads rather than
    /// parses.
    public static func humanize(_ secs: Int) -> String {
        func plural(_ n: Int, _ word: String) -> String { "\(n) \(word)\(n == 1 ? "" : "s")" }
        switch secs {
        case 0: return "Off"
        case ..<60: return plural(secs, "second")
        case ..<3600 where secs % 60 == 0: return plural(secs / 60, "minute")
        case ..<86_400 where secs % 3600 == 0: return plural(secs / 3600, "hour")
        default:
            if secs % 3600 == 0 { return plural(secs / 3600, "hour") }
            if secs % 60 == 0 { return plural(secs / 60, "minute") }
            return goDuration(secs)
        }
    }

    /// True when `value` is the persisted "off" sentinel in any of the spellings
    /// the round trip produces: the app writes "0", `config get` reports "0s",
    /// and KeyValues renders "off".
    public static func isOff(_ value: String) -> Bool {
        let v = value.trimmingCharacters(in: .whitespaces)
        return v == "0" || v == "0s" || v == "off"
    }
}
