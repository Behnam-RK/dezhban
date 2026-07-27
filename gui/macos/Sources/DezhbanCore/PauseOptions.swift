import Foundation

/// One offered pause length — mirrors Go's `config.PauseOption`
/// (`dezhban pause --list --json`).
///
/// Defined by the daemon rather than here so the CLI and the app offer the same
/// choices. A picker that invented its own list would drift from `pause --list`
/// the first time either changed.
public struct PauseOption: Codable, Identifiable, Hashable {
    public var id: String { value }

    /// How the length reads to a person: "15 minutes".
    public let label: String
    /// The duration as `dezhban pause` accepts it, passed straight through so
    /// the app never formats a duration itself.
    public let value: String
    /// What this length is for, so the choice is made on the need rather than
    /// on the number.
    public let why: String
    /// Non-empty when `vpn.pauseMax` forbids this length, and says so in words.
    ///
    /// Such an option is still SHOWN, disabled, with this as its tooltip.
    /// Hiding it would leave the user with a wrong idea of their own cap, and
    /// silently shortening it — the behaviour this replaced — is worse still.
    public let unavailable: String?

    public var isAvailable: Bool { (unavailable ?? "").isEmpty }

    public static func decodeList(_ data: Data) -> [PauseOption]? {
        try? JSONDecoder().decode([PauseOption].self, from: data)
    }
}
