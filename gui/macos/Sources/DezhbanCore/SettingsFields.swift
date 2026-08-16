import Foundation

/// The staged values for the Settings pane's batched `config set`, and the
/// dotted keys they correspond to.
///
/// Values are stored **keyed by dotted key**, not positionally. They used to be
/// twenty-five stored properties destructured out of an array by literal index,
/// with a `precondition` on the count alone — so inserting a key anywhere but the
/// end, or reordering two of them, silently wrote one field's value under another
/// field's key and passed every check. Keying the storage makes that
/// unrepresentable: a value can only ever be read back under the key it was
/// stored against.
///
/// The named properties survive as computed accessors so the SwiftUI bindings
/// (`$fields.switchWindow`) keep working; they are now views onto the dictionary
/// rather than a second copy of it.
public struct SettingsFields {
    // Dotted keys from configFields (cmd/dezhban/config_cmd.go). This is the set
    // the pane stages — a deliberate subset of every settable key, since identity
    // data (profiles) and the control socket are managed elsewhere. Order is
    // presentation order only; nothing correctness-bearing depends on it now.
    public static let keys = [
        "vpn.tunnelInterfaces", "vpn.endpoints",
        "vpn.autoDetect", "vpn.autoDiscoverEndpoints", "vpn.autoArm",
        "vpn.allowLocalNetwork", "vpn.allowPhysicalDNS",
        "blockedCountries", "pollInterval",
        "vpn.switchWindow", "vpn.redialWindow", "vpn.pauseMax", "vpn.endpointGrace",
        "vpn.endpointRefresh", "vpn.tunnelWatch",
        "hysteresis", "providerQuorum", "logLevel",
        "vpn.advanced.switchWindowMax", "vpn.advanced.redialWindowMax", "vpn.advanced.redialMinUptime",
        "vpn.advanced.redialBudget", "vpn.advanced.redialBudgetWindow",
        "vpn.advanced.commandFreshness", "vpn.advanced.windowDiscoveryInterval", "vpn.advanced.tunnelPruneAfter",
        "vpn.advanced.learnedEndpointTTL", "vpn.advanced.learnedMaxPerProfile", "vpn.advanced.promoteAfterRefreshes",
        "vpn.advanced.endpointWarnThreshold", "vpn.advanced.windowProtocols", "vpn.advanced.windowPorts",
        "vpn.advanced.verifyInterval", "vpn.advanced.livenessRedial",
    ]

    /// Keys the pane stages ONLY when the CLI's schema knows them. A key an old
    /// CLI has never heard of must not be in the seeded set at all —
    /// `ConfigApply.seed` short-circuits on its first failed `config get`, so
    /// one unconditionally-staged new key would brick the whole pane against an
    /// older install. The pane computes which of these to register from the
    /// schema it just read, then passes them to `init(seeded:extraKeys:)`.
    public static let optionalKeys = ["vpn.allowGeoProviders"]

    /// Resting values for optional keys once registered — same rule as
    /// boolKeys' resting values below.
    private static let optionalRestingValues: [String: String] = [
        "vpn.allowGeoProviders": "true",
    ]

    /// Raw staged values, exactly as `config get` returned them and exactly as
    /// `config set` will receive them. Bools travel as "true"/"false" because
    /// that is what both ends of the round trip use.
    private var values: [String: String]

    /// Optional keys that were registered, in registration order — appended
    /// after `keys` wherever an ordered walk is needed.
    private var registeredKeys: [String] = []

    public init() {
        values = Dictionary(uniqueKeysWithValues: Self.keys.map { ($0, "") })
        // Only bools need a non-empty resting value; an empty string would be
        // written back as an invalid bool if the pane were applied unseeded.
        for key in Self.boolKeys { values[key] = "false" }
        values["vpn.allowLocalNetwork"] = "true"
        values["vpn.allowPhysicalDNS"] = "true"
    }

    /// Rebuilds a SettingsFields from what `config get` returned, keyed by the
    /// key each value was read for. A key the caller did not read keeps its
    /// resting value rather than silently taking a neighbour's. `extraKeys`
    /// registers storage for the schema-known optional keys before seeding, so
    /// their values land instead of being ignored.
    public init(seeded: [String: String], extraKeys: [String] = []) {
        self.init()
        for key in extraKeys {
            register(key: key)
        }
        for (key, value) in seeded where values[key] != nil {
            values[key] = value
        }
    }

    /// Registers storage for one optional key. Anything not in `optionalKeys`
    /// is refused — the static list is the contract of what this pane may ever
    /// write, and a caller cannot widen it at runtime.
    public mutating func register(key: String) {
        guard Self.optionalKeys.contains(key), values[key] == nil else { return }
        values[key] = Self.optionalRestingValues[key] ?? ""
        registeredKeys.append(key)
    }

    /// The keys whose values are booleans, so accessors and seeding agree on the
    /// representation.
    static let boolKeys: Set<String> = [
        "vpn.autoDetect", "vpn.autoDiscoverEndpoints", "vpn.autoArm", "vpn.allowLocalNetwork",
        "vpn.allowPhysicalDNS", "providerQuorum",
        "vpn.advanced.livenessRedial",
    ]

    /// Reads one staged value by key. Returns "" for a key this pane does not
    /// stage, which is the same thing an unset field looks like.
    public func value(for key: String) -> String { values[key] ?? "" }

    /// Stages one value by key. Ignores keys the pane does not carry, so a
    /// schema-driven control cannot invent storage by writing to it.
    public mutating func setValue(_ value: String, for key: String) {
        guard values[key] != nil else { return }
        values[key] = value
    }

    /// Every staged value, keyed. The dirtiness check compares this against what
    /// the pane was last seeded with.
    public var currentValues: [String: String] { values }

    /// Every key with storage, in presentation order: the static set, then the
    /// registered optionals. This — not `Self.keys` — is what seed and apply
    /// walk, so an optional key participates exactly when it was registered.
    public var stagedKeys: [String] { Self.keys + registeredKeys }

    /// Renders `key=value` pairs for one batched `config set`, in `stagedKeys`
    /// order so the resulting command line is stable and diffable — and only
    /// for keys that HAVE storage, so an optional key an old CLI doesn't know
    /// is never sent to it. Durations are trimmed (whitespace typed into a
    /// text field is not part of the value); everything else travels as-is —
    /// `config set`/Normalize (Go) does the canonicalisation (upper-casing
    /// country codes, etc.), not this layer.
    public func pairs() -> [String] {
        stagedKeys.map { key in
            "\(key)=\(value(for: key).trimmingCharacters(in: .whitespaces))"
        }
    }

    /// The duration-valued fields that must parse before Apply spends a
    /// privileged round trip, as (label, trimmed value) pairs.
    ///
    /// Both which fields these are and what they are called now come from the
    /// daemon's schema instead of a hand-kept list that had to be updated in
    /// lockstep with the pane. Without a schema this returns nothing: refusing to
    /// pre-validate is safe (the daemon still rejects a malformed duration and
    /// says so), whereas guessing the field set would either miss one or invent
    /// labels that disagree with the ones on screen.
    public func durationFieldsForValidation(schema: ConfigSchema) -> [(label: String, value: String)] {
        stagedKeys.compactMap { key in
            guard let tunable = schema[key], tunable.kind == "duration" else { return nil }
            return (tunable.label, value(for: key).trimmingCharacters(in: .whitespaces))
        }
    }

    // MARK: - Named accessors
    //
    // Views onto `values`, so SwiftUI bindings keep their existing spelling.

    private func string(_ key: String) -> String { values[key] ?? "" }
    private mutating func setString(_ key: String, _ newValue: String) { values[key] = newValue }
    private func bool(_ key: String) -> Bool { values[key] == "true" }
    private mutating func setBool(_ key: String, _ newValue: Bool) { values[key] = String(newValue) }

    public var tunnelInterfaces: String {
        get { string("vpn.tunnelInterfaces") } set { setString("vpn.tunnelInterfaces", newValue) }
    }
    public var endpoints: String {
        get { string("vpn.endpoints") } set { setString("vpn.endpoints", newValue) }
    }
    public var autoDetect: Bool {
        get { bool("vpn.autoDetect") } set { setBool("vpn.autoDetect", newValue) }
    }
    public var autoDiscover: Bool {
        get { bool("vpn.autoDiscoverEndpoints") } set { setBool("vpn.autoDiscoverEndpoints", newValue) }
    }
    public var autoArm: Bool {
        get { bool("vpn.autoArm") } set { setBool("vpn.autoArm", newValue) }
    }
    public var allowLocalNetwork: Bool {
        get { bool("vpn.allowLocalNetwork") } set { setBool("vpn.allowLocalNetwork", newValue) }
    }
    public var allowPhysicalDNS: Bool {
        get { bool("vpn.allowPhysicalDNS") } set { setBool("vpn.allowPhysicalDNS", newValue) }
    }
    /// The one optional-key accessor: reads true (the daemon default) until the
    /// key is registered and seeded, and writes through `setValue`, which drops
    /// the write when no storage exists — a control bound here while the CLI is
    /// too old cannot invent a pair for `config set`. The pane hides the toggle
    /// in that case anyway; this is the belt to that suspender.
    public var allowGeoProviders: Bool {
        get { values["vpn.allowGeoProviders"].map { $0 == "true" } ?? true }
        set { setValue(String(newValue), for: "vpn.allowGeoProviders") }
    }
    public var blockedCountries: String {
        get { string("blockedCountries") } set { setString("blockedCountries", newValue) }
    }
    public var pollInterval: String {
        get { string("pollInterval") } set { setString("pollInterval", newValue) }
    }
    public var switchWindow: String {
        get { string("vpn.switchWindow") } set { setString("vpn.switchWindow", newValue) }
    }
    public var redialWindow: String {
        get { string("vpn.redialWindow") } set { setString("vpn.redialWindow", newValue) }
    }
    public var pauseMax: String {
        get { string("vpn.pauseMax") } set { setString("vpn.pauseMax", newValue) }
    }
    public var endpointGrace: String {
        get { string("vpn.endpointGrace") } set { setString("vpn.endpointGrace", newValue) }
    }
    public var endpointRefresh: String {
        get { string("vpn.endpointRefresh") } set { setString("vpn.endpointRefresh", newValue) }
    }
    public var tunnelWatch: String {
        get { string("vpn.tunnelWatch") } set { setString("vpn.tunnelWatch", newValue) }
    }

    public var advSwitchWindowMax: String {
        get { string("vpn.advanced.switchWindowMax") } set { setString("vpn.advanced.switchWindowMax", newValue) }
    }
    public var advRedialWindowMax: String {
        get { string("vpn.advanced.redialWindowMax") } set { setString("vpn.advanced.redialWindowMax", newValue) }
    }
    public var advRedialMinUptime: String {
        get { string("vpn.advanced.redialMinUptime") } set { setString("vpn.advanced.redialMinUptime", newValue) }
    }
    public var advRedialBudget: String {
        get { string("vpn.advanced.redialBudget") } set { setString("vpn.advanced.redialBudget", newValue) }
    }
    public var advRedialBudgetWindow: String {
        get { string("vpn.advanced.redialBudgetWindow") }
        set { setString("vpn.advanced.redialBudgetWindow", newValue) }
    }
    public var advCommandFreshness: String {
        get { string("vpn.advanced.commandFreshness") } set { setString("vpn.advanced.commandFreshness", newValue) }
    }
    public var advWindowDiscoveryInterval: String {
        get { string("vpn.advanced.windowDiscoveryInterval") }
        set { setString("vpn.advanced.windowDiscoveryInterval", newValue) }
    }
    public var advTunnelPruneAfter: String {
        get { string("vpn.advanced.tunnelPruneAfter") } set { setString("vpn.advanced.tunnelPruneAfter", newValue) }
    }
    public var advLearnedEndpointTTL: String {
        get { string("vpn.advanced.learnedEndpointTTL") } set { setString("vpn.advanced.learnedEndpointTTL", newValue) }
    }
    public var advLearnedMaxPerProfile: String {
        get { string("vpn.advanced.learnedMaxPerProfile") }
        set { setString("vpn.advanced.learnedMaxPerProfile", newValue) }
    }
    public var advPromoteAfterRefreshes: String {
        get { string("vpn.advanced.promoteAfterRefreshes") }
        set { setString("vpn.advanced.promoteAfterRefreshes", newValue) }
    }
    public var advEndpointWarnThreshold: String {
        get { string("vpn.advanced.endpointWarnThreshold") }
        set { setString("vpn.advanced.endpointWarnThreshold", newValue) }
    }
    public var advWindowProtocols: String {
        get { string("vpn.advanced.windowProtocols") } set { setString("vpn.advanced.windowProtocols", newValue) }
    }
    public var advWindowPorts: String {
        get { string("vpn.advanced.windowPorts") } set { setString("vpn.advanced.windowPorts", newValue) }
    }
    public var advVerifyInterval: String {
        get { string("vpn.advanced.verifyInterval") } set { setString("vpn.advanced.verifyInterval", newValue) }
    }
    public var advLivenessRedial: Bool {
        get { bool("vpn.advanced.livenessRedial") } set { setBool("vpn.advanced.livenessRedial", newValue) }
    }
}
