import Foundation

/// Merged fields from `status --json` that the app needs but the daemon's
/// Snapshot itself doesn't carry (those come from Snapshot/Display instead).
///
/// Lives in DezhbanCore (rather than nested in DezhbanCLI, where it started)
/// because it now carries display data — the strictness preset — and display
/// derivations belong where they can be tested.
public struct StatusInfo: Decodable {
    public let service: String
    public let controlReachable: Bool
    public let pauseEnabled: Bool
    /// The strictness preset this config matches, or the NEAREST one for a
    /// drifted config (then `presetExact` is false). nil from a CLI older than
    /// the field — callers must show nothing, not "Custom".
    public let preset: String?
    public let presetExact: Bool?

    public init(service: String, controlReachable: Bool, pauseEnabled: Bool,
                preset: String? = nil, presetExact: Bool? = nil) {
        self.service = service
        self.controlReachable = controlReachable
        self.pauseEnabled = pauseEnabled
        self.preset = preset
        self.presetExact = presetExact
    }

    /// `installed`, derived from `service`'s merged field (itself
    /// `internal/svc.Status()`) — the single source of truth, so the GUI
    /// never invents its own notion of "installed" that could drift from
    /// the CLI's.
    public var serviceInstalled: Bool { service.hasPrefix("installed") }

    /// The strictness line the Overview shows: "Balanced" for an exact match,
    /// "Custom (closest: Balanced)" for a drifted config, nil when the CLI is
    /// too old to say — a row that would otherwise have to guess is hidden
    /// instead.
    public var presetLabel: String? {
        guard let preset, !preset.isEmpty else { return nil }
        if presetExact == true { return preset.capitalized }
        return "Custom (closest: \(preset.capitalized))"
    }

    public static func decode(_ data: Data) -> StatusInfo? {
        try? JSONDecoder().decode(StatusInfo.self, from: data)
    }
}
