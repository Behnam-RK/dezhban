import Foundation

/// The math behind a duration slider: a log-scaled track from half the key's
/// default up to its cap (or a synthetic top when no cap exists), snapping to
/// a human ladder, with an "Off" detent at the left edge for disablable keys.
///
/// Pure and tested here so DurationField's view holds zero arithmetic — the
/// same division of labor as DurationChoices, whose parsing/formatting
/// helpers this reuses. Like `DurationChoices.build`, everything derives from
/// the schema's default and cap; no per-key values are hardcoded.
public struct DurationScale: Equatable {
    public let minSeconds: Double
    public let maxSeconds: Double
    public let defaultSeconds: Double
    /// Whether the leftmost stretch of track is the Off detent. Comes ONLY
    /// from the schema's `disablable` — offering Off on a key whose "0" the
    /// daemon's Normalize silently restores to the default would present a
    /// security choice that doesn't exist.
    public let hasOff: Bool

    /// The share of track reserved for the Off detent when it exists.
    /// Durations then occupy (offGap, 1]; Off is a discrete stop, not the
    /// asymptotic end of the continuum.
    public static let offGap = 0.08

    /// One snapped stop on the slider.
    public struct Snap: Equatable {
        /// The value as `config set` accepts it: "0" for Off, else a Go
        /// duration ("1m30s").
        public let value: String
        /// The value as a person reads it: "Off", "1 minute 30 seconds".
        public let label: String
        public let isOff: Bool
        public let isDefault: Bool
    }

    /// nil when the default doesn't parse or is non-positive — the control
    /// falls back to the Menu/TextField, the same rule as
    /// `DurationChoices.build` returning nothing. It also returns nil when a
    /// cap leaves no usable span, so a legitimately tiny ceiling degrades to
    /// the text control rather than to a track that lies.
    public init?(defaultValue: String, cap: String?, disablable: Bool) {
        guard let def = DurationChoices.seconds(defaultValue), def > 0 else { return nil }
        defaultSeconds = def
        if let cap, let capSecs = DurationChoices.seconds(cap), capSecs > 0 {
            // A cap binds WHEREVER it sits, including at or below the schema
            // default: caps have no floor by design (an operator may set
            // redialWindowMax below the default redialWindow), and a track
            // running past the ceiling stages values the daemon rejects at
            // Apply. Same rule `DurationChoices.build` follows by skipping
            // choices above the ceiling — a control is a menu of things that
            // will be accepted.
            maxSeconds = capSecs
        } else {
            // No cap: a synthetic 8× top. Arbitrary but bounded; the Custom
            // escape hatch covers the tail beyond it.
            maxSeconds = def * 8
        }
        // Locals, not the stored properties: referring to `self` inside the
        // filter closure below would capture it before initialization is done.
        let top = maxSeconds
        let floor = Swift.max(1, Swift.min(def / 2, top / 2))
        minSeconds = floor
        hasOff = disablable
        guard top > floor else { return nil }
        // Derived once, not per snap: `snapped(at:)` runs in the slider's
        // binding on every drag tick and again on every render of the row.
        var stops = Set(
            Self.ladder(upTo: top)
                .filter { Double($0) >= floor && Double($0) <= top }
        )
        // Always offer the exact bounds, whether or not the ladder passes
        // through them — the cap must be reachable.
        stops.insert(Int(floor.rounded()))
        stops.insert(Int(top.rounded()))
        // The default only when it fits: a default above a lowered cap is not
        // a value the daemon would take, so it must not be landable-on.
        if def >= floor && def <= top {
            stops.insert(Int(def.rounded()))
        }
        snapSteps = stops.sorted()
    }

    /// The snap stops within [minSeconds, maxSeconds].
    private let snapSteps: [Int]

    private var durationSpan: ClosedRange<Double> {
        (hasOff ? Self.offGap : 0.0)...1.0
    }

    /// Track position (0…1) for a config value. Off (and anything unparsable)
    /// sits at 0; durations map log-linearly across the duration span, clamped
    /// so an off-scale custom value still has a sensible thumb position.
    public func position(for value: String) -> Double {
        if hasOff && DurationChoices.isOff(value) { return 0 }
        guard let secs = DurationChoices.seconds(value), secs > 0 else { return 0 }
        let clamped = Swift.min(Swift.max(secs, minSeconds), maxSeconds)
        let f = log(clamped / minSeconds) / log(maxSeconds / minSeconds)
        let span = durationSpan
        return span.lowerBound + f * (span.upperBound - span.lowerBound)
    }

    /// The snapped stop at a track position: the Off detent when the thumb is
    /// inside the gap, else the nearest ladder step within [min, max].
    public func snapped(at position: Double) -> Snap {
        if hasOff && position < Self.offGap {
            return Snap(value: "0", label: "Off", isOff: true, isDefault: false)
        }
        let span = durationSpan
        let p = Swift.min(Swift.max(position, span.lowerBound), span.upperBound)
        let f = (p - span.lowerBound) / (span.upperBound - span.lowerBound)
        let raw = minSeconds * pow(maxSeconds / minSeconds, f)
        let secs = nearestLadderStep(to: raw)
        let isDefault = secs == Int(defaultSeconds.rounded())
        return Snap(
            value: DurationChoices.goDuration(secs),
            label: DurationChoices.humanize(secs),
            isOff: false,
            isDefault: isDefault
        )
    }

    /// The human ladder: 1/2/3/5/10/15/20/30/45 per decade unit (seconds,
    /// minutes, hours), generated arithmetically rather than listed per key.
    static func ladder(upTo maxSecs: Double) -> [Int] {
        let steps = [1, 2, 3, 5, 10, 15, 20, 30, 45]
        var out: [Int] = []
        for unit in [1, 60, 3600] {
            for s in steps {
                let v = s * unit
                if Double(v) <= maxSecs * 2 { out.append(v) }
            }
        }
        // Extend the hour ladder for long keys (learnedEndpointTTL is 720h).
        var day = 24 * 3600
        while Double(day) <= maxSecs * 2 {
            out.append(day)
            day *= 2
        }
        return out.sorted()
    }

    private func nearestLadderStep(to raw: Double) -> Int {
        // Log-nearest, not linear-nearest: on a log track, 90s is midway
        // between 1m and 2m, and linear distance would bias every snap upward.
        return snapSteps.min(by: { abs(log(Double($0)) - log(raw)) < abs(log(Double($1)) - log(raw)) }) ?? Int(raw.rounded())
    }
}
