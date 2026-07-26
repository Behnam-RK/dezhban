import Foundation

/// The VPN profiles configured on disk — decoded from `dezhban config show`'s
/// `vpn.profiles` (plus the flat `vpn.endpoints`, shown as the profile-less
/// default). The Snapshot only carries `activeProfile` (which one matched last),
/// never the full list — that lives in config, not daemon state.
public struct ProfilesInfo: Codable {
    public struct Profile: Codable, Identifiable, Hashable {
        public var id: String { name }
        public let name: String
        public let endpoints: [String]
        public let tunnelHint: String?
    }

    public let defaultEndpoints: [String]
    public let profiles: [Profile]

    private enum RootKeys: String, CodingKey { case vpn }
    private enum VPNKeys: String, CodingKey { case endpoints, profiles }

    public init(defaultEndpoints: [String], profiles: [Profile]) {
        self.defaultEndpoints = defaultEndpoints
        self.profiles = profiles
    }

    public init(from decoder: Decoder) throws {
        let root = try decoder.container(keyedBy: RootKeys.self)
        let vpn = try root.nestedContainer(keyedBy: VPNKeys.self, forKey: .vpn)
        defaultEndpoints = try vpn.decodeIfPresent([String].self, forKey: .endpoints) ?? []
        profiles = try vpn.decodeIfPresent([Profile].self, forKey: .profiles) ?? []
    }

    public func encode(to encoder: Encoder) throws {
        var root = encoder.container(keyedBy: RootKeys.self)
        var vpn = root.nestedContainer(keyedBy: VPNKeys.self, forKey: .vpn)
        try vpn.encode(defaultEndpoints, forKey: .endpoints)
        try vpn.encode(profiles, forKey: .profiles)
    }

    public static func decode(_ data: Data) -> ProfilesInfo? {
        try? JSONDecoder().decode(ProfilesInfo.self, from: data)
    }
}
