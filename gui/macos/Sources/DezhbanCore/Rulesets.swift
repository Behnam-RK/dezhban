import Foundation

/// The firewall rules dezhban recorded applying — `print-rules --applied --json`,
/// mirroring Go's `applied.Record`.
///
/// This is dezhban's own account of what it installed, not a reading of the
/// kernel, and every surface showing it must say so. The distinction is not
/// pedantry: something outside dezhban can flush a firewall, and a pane that
/// called this "the current rules" would go on claiming the guard was enforcing
/// over a wide-open network.
public struct AppliedRuleset: Codable, Hashable {
    public let mode: String
    public let at: Date
    public let rules: String
    /// The mechanism the text is written for — "pf", "nft", "wfp" — so a reader
    /// does not have to infer a syntax from the platform.
    public let backend: String

    public init(mode: String, at: Date, rules: String, backend: String) {
        self.mode = mode
        self.at = at
        self.rules = rules
        self.backend = backend
    }

    /// Go writes RFC 3339 with fractional seconds; `.iso8601` alone rejects
    /// those, which would turn a perfectly good record into "no rules recorded".
    public static func decode(_ data: Data) -> AppliedRuleset? {
        for strategy in [rfc3339Fractional, rfc3339] {
            let d = JSONDecoder()
            d.dateDecodingStrategy = .formatted(strategy)
            if let v = try? d.decode(AppliedRuleset.self, from: data) { return v }
        }
        return nil
    }

    private static let rfc3339Fractional: DateFormatter = formatter("yyyy-MM-dd'T'HH:mm:ss.SSSSSSZZZZZ")
    private static let rfc3339: DateFormatter = formatter("yyyy-MM-dd'T'HH:mm:ssZZZZZ")

    private static func formatter(_ format: String) -> DateFormatter {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = format
        f.timeZone = TimeZone(secondsFromGMT: 0)
        return f
    }
}

/// What the kernel actually holds — `print-rules --installed --json`.
///
/// The privileged half of the picture, taken on demand rather than on a tick.
/// It is a READ: it installs nothing, so it does not touch the rule that only
/// the run loop may apply.
public struct InstalledRuleset: Hashable {
    public let installed: String
    /// False when dezhban has no rules in the kernel at all. An ordinary
    /// answer — standby, or nothing running — never an error.
    public let loaded: Bool
    public let applied: AppliedRuleset?
    /// True when dezhban has a record of applying rules and the kernel holds
    /// none. Deliberately NOT a text diff: the kernel renders a normalised form
    /// of what was loaded, so comparing bytes would report drift on every
    /// healthy host. The texts are shown side by side for a person to read.
    public let drift: Bool
    public let backend: String
    /// Why the loaded rules are not filtering — pf switched off, an anchor the
    /// main ruleset no longer references, an nft chain whose policy drifted off
    /// drop. Empty on a healthy host.
    public let warnings: [String]
    /// The question a reader actually has, and it is NOT `loaded`: a firewall
    /// can hold every rule dezhban installed and filter none of them. Left
    /// inside the ruleset text, that state rendered as a collapsed disclosure
    /// with nothing visibly wrong.
    public let enforcing: Bool

    public init(installed: String, loaded: Bool, applied: AppliedRuleset?,
                drift: Bool, backend: String,
                warnings: [String] = [], enforcing: Bool = true) {
        self.installed = installed
        self.loaded = loaded
        self.applied = applied
        self.drift = drift
        self.backend = backend
        self.warnings = warnings
        self.enforcing = enforcing
    }

    /// Decoded by hand rather than through Codable so the nested `applied`
    /// record can reuse AppliedRuleset's two-format date handling.
    public static func decode(_ data: Data) -> InstalledRuleset? {
        guard let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return nil
        }
        var nested: AppliedRuleset?
        if let sub = obj["applied"],
           let subData = try? JSONSerialization.data(withJSONObject: sub) {
            nested = AppliedRuleset.decode(subData)
        }
        // The fields the CLI ALWAYS emits are required, not defaulted. Defaulting
        // them made every JSON object decode — `{}` included — as a confident
        // "no rules loaded, no drift", so a malformed or version-skewed response
        // reached the pane as a benign standby message instead of taking the
        // error path. False reassurance about whether a kill switch is enforcing
        // is the one thing this surface must never produce; failing to decode is
        // recoverable, and the pane already says so.
        //
        // `installed` stays optional-with-default on purpose: it is legitimately
        // absent-or-empty when nothing is loaded.
        guard let loaded = obj["loaded"] as? Bool,
              let drift = obj["drift"] as? Bool,
              let backend = obj["backend"] as? String
        else { return nil }
        // Both default rather than being required: a CLI predating them emits
        // neither, and an older CLI must degrade to the previous behaviour
        // rather than fail to decode. `enforcing` defaults to `loaded` there,
        // which is exactly what the pane assumed before this existed.
        let warnings = obj["warnings"] as? [String] ?? []
        return InstalledRuleset(
            installed: obj["installed"] as? String ?? "",
            loaded: loaded,
            applied: nested,
            drift: drift,
            backend: backend,
            warnings: warnings,
            enforcing: obj["enforcing"] as? Bool ?? loaded)
    }
}

/// The postures whose rulesets can be previewed without applying anything.
///
/// These are the stable `print-rules --mode` identifiers, which CLAUDE.md pins
/// as part of the CLI contract — they are not display strings and must not be
/// renamed to read better.
public enum RulesetPreview: String, CaseIterable, Identifiable, Sendable {
    case guardMode = "guard"
    case fullBlock = "fullblock"
    case switchWindow = "switch"

    public var id: String { rawValue }

    public var label: String {
        switch self {
        case .guardMode: return "Guard"
        case .fullBlock: return "Full block"
        case .switchWindow: return "Switch window"
        }
    }

    /// What this posture does to traffic, in one line — the caption beside the
    /// rules, because a ruleset is not self-explanatory to the person most
    /// likely to be reading it.
    public var detail: String {
        switch self {
        case .guardMode:
            return "The standing posture: only the VPN tunnel and the handshake to its server may leave. "
                + "Everything else is dropped, so a tunnel drop cuts traffic with no leak window."
        case .fullBlock:
            return "What happens when the VPN's exit lands in a blocked country: the tunnel's own pass is "
                + "removed too, so no traffic reaches that exit — but the handshake to the server stays "
                + "open, so the VPN can still move."
        case .switchWindow:
            return "The bounded window you open deliberately to connect a new VPN. It closes early on a "
                + "confirmed good exit, and always at its deadline."
        }
    }
}
