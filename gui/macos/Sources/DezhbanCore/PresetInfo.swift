import Foundation

/// Mirrors Go's presetJSON (`dezhban config preset list`/`show --json`).
/// `values` is populated only by `show`, never `list`.
public struct PresetSummary: Codable, Identifiable, Hashable {
    public var id: String { name }
    public let name: String
    public let summary: String
    public let cost: String
    public let matched: Bool?
    /// Why this preset cannot be applied to the current config — empty for an
    /// appliable one. A preset never raises `vpn.advanced.switchWindowMax` /
    /// `redialWindowMax` (a strictness macro must not widen a ceiling the
    /// operator set by hand), so lowering one in Advanced can put a preset out
    /// of reach. Populated by `preset list` only; `show` describes a preset in
    /// the abstract, with no config to compare against.
    public let conflicts: [String]?
    public let values: [String: String]?

    /// True when this preset can actually be written to the current config.
    /// A picker must offer it as unavailable rather than let it be chosen and
    /// fail at apply time.
    public var isAppliable: Bool { (conflicts ?? []).isEmpty }

    public static func decodeList(_ data: Data) -> [PresetSummary]? {
        try? JSONDecoder().decode([PresetSummary].self, from: data)
    }
}

/// Mirrors Go's changeJSON (`dezhban config preset diff --json`'s `changes`).
public struct PresetChange: Codable, Identifiable, Hashable {
    public var id: String { key }
    public let key: String
    public let from: String
    public let to: String
    public let restartReason: String?
}

/// Mirrors Go's `preset diff --json` top-level shape: `{preset, changes}`.
public struct PresetDiff: Codable {
    public let preset: String
    public let changes: [PresetChange]

    public static func decode(_ data: Data) -> PresetDiff? {
        try? JSONDecoder().decode(PresetDiff.self, from: data)
    }
}
