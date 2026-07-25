import Foundation

/// The staged values for the Settings pane's batched `config set`, and the
/// dotted keys they correspond to. Pulled out of SettingsView so the
/// seed→pairs round trip — the exact place a silent index mismatch would write
/// one field's value under another field's key — can be unit-tested without
/// standing up SwiftUI state.
public struct SettingsFields {
    // Dotted keys from configFields (cmd/dezhban/config_cmd.go), in the same
    // order `seed()`/`pairs()` destructure and rebuild them.
    public static let keys = [
        "vpn.tunnelInterfaces", "vpn.endpoints",
        "vpn.autoDetect", "vpn.autoDiscoverEndpoints", "vpn.autoArm",
        "vpn.allowLocalNetwork",
        "blockedCountries", "pollInterval",
        "vpn.switchWindow", "vpn.redialWindow", "vpn.endpointGrace",
        "vpn.endpointRefresh", "vpn.tunnelWatch",
    ]

    public var tunnelInterfaces = ""
    public var endpoints = ""
    public var autoDetect = false
    public var autoDiscover = false
    public var autoArm = false
    public var allowLocalNetwork = true
    public var blockedCountries = ""
    public var pollInterval = ""
    public var switchWindow = ""
    public var redialWindow = ""
    public var endpointGrace = ""
    public var endpointRefresh = ""
    public var tunnelWatch = ""

    public init() {}

    /// Rebuilds a SettingsFields from `keys`-ordered values, as returned by a
    /// batch of `config get` calls (ConfigApply.seed). Out-of-range access is a
    /// programmer error (a caller passing the wrong key list), so this traps
    /// rather than silently seeding partial/wrong fields.
    public init(seeded values: [String]) {
        precondition(values.count == Self.keys.count,
                     "SettingsFields.init(seeded:) got \(values.count) values, want \(Self.keys.count)")
        tunnelInterfaces = values[0]
        endpoints = values[1]
        autoDetect = (values[2] == "true")
        autoDiscover = (values[3] == "true")
        autoArm = (values[4] == "true")
        allowLocalNetwork = (values[5] == "true")
        blockedCountries = values[6]
        pollInterval = values[7]
        switchWindow = values[8]
        redialWindow = values[9]
        endpointGrace = values[10]
        endpointRefresh = values[11]
        tunnelWatch = values[12]
    }

    /// The current values in `keys` order — the dirtiness check compares this
    /// against what the pane was last seeded with.
    public var currentValues: [String] {
        [tunnelInterfaces, endpoints,
         String(autoDetect), String(autoDiscover), String(autoArm),
         String(allowLocalNetwork),
         blockedCountries, pollInterval,
         switchWindow, redialWindow, endpointGrace,
         endpointRefresh, tunnelWatch]
    }

    /// Renders `key=value` pairs for one batched `config set`. Durations are
    /// trimmed (whitespace typed into a text field is not part of the value);
    /// everything else travels as-is — `config set`/Normalize (Go) does the
    /// canonicalisation (upper-casing country codes, etc.), not this layer.
    public func pairs() -> [String] {
        [
            "vpn.tunnelInterfaces=\(tunnelInterfaces)",
            "vpn.endpoints=\(endpoints)",
            "vpn.autoDetect=\(autoDetect)",
            "vpn.autoDiscoverEndpoints=\(autoDiscover)",
            "vpn.autoArm=\(autoArm)",
            "vpn.allowLocalNetwork=\(allowLocalNetwork)",
            "blockedCountries=\(blockedCountries.trimmingCharacters(in: .whitespaces))",
            "pollInterval=\(pollInterval.trimmingCharacters(in: .whitespaces))",
            "vpn.switchWindow=\(switchWindow.trimmingCharacters(in: .whitespaces))",
            "vpn.redialWindow=\(redialWindow.trimmingCharacters(in: .whitespaces))",
            "vpn.endpointGrace=\(endpointGrace.trimmingCharacters(in: .whitespaces))",
            "vpn.endpointRefresh=\(endpointRefresh.trimmingCharacters(in: .whitespaces))",
            "vpn.tunnelWatch=\(tunnelWatch.trimmingCharacters(in: .whitespaces))",
        ]
    }

    /// The duration-valued fields that must pass `looksLikeGoDuration` before
    /// Apply spends a privileged round trip — label, then the trimmed value.
    public var durationFieldsForValidation: [(label: String, value: String)] {
        [
            ("Geo IP lookup interval", pollInterval.trimmingCharacters(in: .whitespaces)),
            ("Switch window", switchWindow.trimmingCharacters(in: .whitespaces)),
            ("Redial window", redialWindow.trimmingCharacters(in: .whitespaces)),
            ("Endpoint grace", endpointGrace.trimmingCharacters(in: .whitespaces)),
            ("Endpoint refresh", endpointRefresh.trimmingCharacters(in: .whitespaces)),
            ("Tunnel watch", tunnelWatch.trimmingCharacters(in: .whitespaces)),
        ]
    }
}
