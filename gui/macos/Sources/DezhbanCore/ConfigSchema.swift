import Foundation

/// Mirrors Go's schemaEntry (`dezhban config schema --json`), which is
/// `internal/config.Tunable` plus a `preset` flag.
///
/// This exists so the app stops restating what the daemon already knows. Before
/// it, every field's label, hint, and default were literal strings in
/// SettingsView — and they had drifted: the pane advertised a 30s endpoint
/// refresh and a 5s tunnel watch while the shipped defaults were 1m and 1s.
/// Anyone reading Settings was being told the wrong thing.
public struct ConfigTunable: Codable, Identifiable, Hashable {
    public var id: String { key }

    /// The dotted key `config set` accepts — the identity everything joins on.
    public let key: String
    /// Human name for the concept, in the vocabulary of docs/concepts/glossary.md.
    public let label: String
    /// Value shape: duration, bool, int, list, or string.
    public let kind: String
    /// What this key is when the config does not set it, rendered the way
    /// `config get` renders a live value.
    public let defaultValue: String
    /// The key that bounds this one, if any. A key rather than a number because
    /// the ceiling is itself settable — a slider's top must be read from the
    /// live config, never hardcoded.
    public let capKey: String?
    /// What an int counts. Empty for every other kind.
    public let unit: String?
    /// Whether "0" is an explicit, persisted off-switch for this key rather than
    /// "reset me to the default". Only these keys may be offered an **Off**
    /// choice: for the others Normalize restores the default, so an Off would
    /// silently do nothing.
    public let disablable: Bool
    /// The `vpn.advanced.*` knobs, shown behind the Advanced disclosure.
    public let advanced: Bool
    /// Whether a strictness preset writes this key, so editing it by hand drifts
    /// the config to Custom.
    public let preset: Bool
    public let help: String
    /// Where this key is documented, as "<page>#<anchor>".
    public let docAnchor: String
    /// Why a running daemon cannot adopt this key in place; nil when it can.
    public let restartReason: String?

    // `default` is a Swift keyword, so the stored property is renamed and mapped.
    private enum CodingKeys: String, CodingKey {
        case key, label, kind
        case defaultValue = "default"
        case capKey, unit, disablable, advanced, preset, help, docAnchor, restartReason
    }

    /// True when a change to this key takes effect without restarting the daemon.
    public var appliesLive: Bool { (restartReason ?? "").isEmpty }

    /// Placeholder text for a text field: the label, then the real default.
    ///
    /// It says "default", not "e.g.", because it now IS the default rather than
    /// an illustrative value someone typed once.
    public var placeholder: String {
        defaultValue.isEmpty ? label : "\(label) (default \(defaultValue))"
    }
}

/// The whole declared schema, indexed by key.
public struct ConfigSchema {
    public let tunables: [ConfigTunable]
    private let byKey: [String: ConfigTunable]

    public init(_ tunables: [ConfigTunable]) {
        self.tunables = tunables
        self.byKey = Dictionary(uniqueKeysWithValues: tunables.map { ($0.key, $0) })
    }

    public static func decode(_ data: Data) -> ConfigSchema? {
        guard let entries = try? JSONDecoder().decode([ConfigTunable].self, from: data) else { return nil }
        return ConfigSchema(entries)
    }

    public subscript(key: String) -> ConfigTunable? { byKey[key] }

    public var isEmpty: Bool { tunables.isEmpty }

    /// Placeholder for a key, falling back to a plain label when the schema is
    /// unavailable (an old CLI, or a failed read). A missing schema must degrade
    /// to a less helpful hint, never to a wrong one — which is why there is no
    /// hardcoded default to fall back to.
    public func placeholder(for key: String, fallback: String) -> String {
        self[key]?.placeholder ?? fallback
    }

    /// The help line for a key, or nil when the schema is unavailable.
    public func help(for key: String) -> String? { self[key]?.help }

    /// The live ceiling for a key: the current value of whatever `capKey` names,
    /// looked up in the values the pane was seeded with. Nil when the key is
    /// unbounded or the cap's value is not to hand.
    ///
    /// Resolved against seeded values rather than the cap's own default, because
    /// lowering a cap by hand must actually lower the control's top.
    public func cap(for key: String, in values: [String: String]) -> String? {
        guard let capKey = self[key]?.capKey else { return nil }
        return values[capKey] ?? self[capKey]?.defaultValue
    }

    /// Every key the daemon says needs a restart, among those given.
    public func restartRequired(among keys: [String]) -> [String] {
        keys.filter { self[$0].map { !$0.appliesLive } ?? false }
    }
}
