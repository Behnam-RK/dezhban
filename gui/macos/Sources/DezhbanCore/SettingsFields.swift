import Foundation

/// The staged values for the Settings pane's batched `config set`, and the
/// dotted keys they correspond to. Pulled out of SettingsView so the
/// seed→pairs round trip — the exact place a silent index mismatch would write
/// one field's value under another field's key — can be unit-tested without
/// standing up SwiftUI state.
public struct SettingsFields {
    // Dotted keys from configFields (cmd/dezhban/config_cmd.go), in the same
    // order `seed()`/`pairs()` destructure and rebuild them. The last twelve are
    // the vpn.advanced.* block, staged into the same batch and shown behind the
    // Settings pane's Advanced disclosure.
    public static let keys = [
        "vpn.tunnelInterfaces", "vpn.endpoints",
        "vpn.autoDetect", "vpn.autoDiscoverEndpoints", "vpn.autoArm",
        "vpn.allowLocalNetwork",
        "blockedCountries", "pollInterval",
        "vpn.switchWindow", "vpn.redialWindow", "vpn.endpointGrace",
        "vpn.endpointRefresh", "vpn.tunnelWatch",
        "vpn.advanced.switchWindowMax", "vpn.advanced.redialWindowMax", "vpn.advanced.redialMinUptime",
        "vpn.advanced.commandFreshness", "vpn.advanced.windowDiscoveryInterval", "vpn.advanced.tunnelPruneAfter",
        "vpn.advanced.learnedEndpointTTL", "vpn.advanced.learnedMaxPerProfile", "vpn.advanced.promoteAfterRefreshes",
        "vpn.advanced.endpointWarnThreshold", "vpn.advanced.windowProtocols", "vpn.advanced.windowPorts",
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

    public var advSwitchWindowMax = ""
    public var advRedialWindowMax = ""
    public var advRedialMinUptime = ""
    public var advCommandFreshness = ""
    public var advWindowDiscoveryInterval = ""
    public var advTunnelPruneAfter = ""
    public var advLearnedEndpointTTL = ""
    public var advLearnedMaxPerProfile = ""
    public var advPromoteAfterRefreshes = ""
    public var advEndpointWarnThreshold = ""
    public var advWindowProtocols = ""
    public var advWindowPorts = ""

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

        advSwitchWindowMax = values[13]
        advRedialWindowMax = values[14]
        advRedialMinUptime = values[15]
        advCommandFreshness = values[16]
        advWindowDiscoveryInterval = values[17]
        advTunnelPruneAfter = values[18]
        advLearnedEndpointTTL = values[19]
        advLearnedMaxPerProfile = values[20]
        advPromoteAfterRefreshes = values[21]
        advEndpointWarnThreshold = values[22]
        advWindowProtocols = values[23]
        advWindowPorts = values[24]
    }

    /// The current values in `keys` order — the dirtiness check compares this
    /// against what the pane was last seeded with.
    public var currentValues: [String] {
        [tunnelInterfaces, endpoints,
         String(autoDetect), String(autoDiscover), String(autoArm),
         String(allowLocalNetwork),
         blockedCountries, pollInterval,
         switchWindow, redialWindow, endpointGrace,
         endpointRefresh, tunnelWatch,
         advSwitchWindowMax, advRedialWindowMax, advRedialMinUptime,
         advCommandFreshness, advWindowDiscoveryInterval, advTunnelPruneAfter,
         advLearnedEndpointTTL, advLearnedMaxPerProfile, advPromoteAfterRefreshes,
         advEndpointWarnThreshold, advWindowProtocols, advWindowPorts]
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
            "vpn.advanced.switchWindowMax=\(advSwitchWindowMax.trimmingCharacters(in: .whitespaces))",
            "vpn.advanced.redialWindowMax=\(advRedialWindowMax.trimmingCharacters(in: .whitespaces))",
            "vpn.advanced.redialMinUptime=\(advRedialMinUptime.trimmingCharacters(in: .whitespaces))",
            "vpn.advanced.commandFreshness=\(advCommandFreshness.trimmingCharacters(in: .whitespaces))",
            "vpn.advanced.windowDiscoveryInterval=\(advWindowDiscoveryInterval.trimmingCharacters(in: .whitespaces))",
            "vpn.advanced.tunnelPruneAfter=\(advTunnelPruneAfter.trimmingCharacters(in: .whitespaces))",
            "vpn.advanced.learnedEndpointTTL=\(advLearnedEndpointTTL.trimmingCharacters(in: .whitespaces))",
            "vpn.advanced.learnedMaxPerProfile=\(advLearnedMaxPerProfile.trimmingCharacters(in: .whitespaces))",
            "vpn.advanced.promoteAfterRefreshes=\(advPromoteAfterRefreshes.trimmingCharacters(in: .whitespaces))",
            "vpn.advanced.endpointWarnThreshold=\(advEndpointWarnThreshold.trimmingCharacters(in: .whitespaces))",
            "vpn.advanced.windowProtocols=\(advWindowProtocols.trimmingCharacters(in: .whitespaces))",
            "vpn.advanced.windowPorts=\(advWindowPorts.trimmingCharacters(in: .whitespaces))",
        ]
    }

    /// The duration-valued fields that must pass `looksLikeGoDuration` before
    /// Apply spends a privileged round trip — label, then the trimmed value.
    /// The three int-valued and two list-valued advanced fields aren't duration
    /// strings, so (matching the existing precedent for `hysteresis`, which was
    /// never validated this way either) they're left to the daemon's own
    /// rejection if malformed.
    public var durationFieldsForValidation: [(label: String, value: String)] {
        [
            ("Geo IP lookup interval", pollInterval.trimmingCharacters(in: .whitespaces)),
            ("Switch window", switchWindow.trimmingCharacters(in: .whitespaces)),
            ("Redial window", redialWindow.trimmingCharacters(in: .whitespaces)),
            ("Endpoint grace", endpointGrace.trimmingCharacters(in: .whitespaces)),
            ("Endpoint refresh", endpointRefresh.trimmingCharacters(in: .whitespaces)),
            ("Tunnel watch", tunnelWatch.trimmingCharacters(in: .whitespaces)),
            ("Manual switch window cap", advSwitchWindowMax.trimmingCharacters(in: .whitespaces)),
            ("Redial window cap", advRedialWindowMax.trimmingCharacters(in: .whitespaces)),
            ("Redial anti-flap uptime", advRedialMinUptime.trimmingCharacters(in: .whitespaces)),
            ("Command freshness", advCommandFreshness.trimmingCharacters(in: .whitespaces)),
            ("Window discovery interval", advWindowDiscoveryInterval.trimmingCharacters(in: .whitespaces)),
            ("Tunnel prune delay", advTunnelPruneAfter.trimmingCharacters(in: .whitespaces)),
            ("Learned endpoint TTL", advLearnedEndpointTTL.trimmingCharacters(in: .whitespaces)),
        ]
    }
}
