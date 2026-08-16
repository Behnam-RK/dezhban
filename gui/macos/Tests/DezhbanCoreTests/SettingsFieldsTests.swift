import Foundation
import Testing
@testable import DezhbanCore

/// Builds a schema good enough to exercise the pane's use of it, without
/// shelling out to the CLI. Kinds and labels match what `config schema --json`
/// reports for these keys.
private func testSchema() -> ConfigSchema {
    func tunable(_ key: String, _ label: String, _ kind: String,
                 defaultValue: String = "", capKey: String? = nil,
                 disablable: Bool = false) -> ConfigTunable {
        let json = """
        {"key":"\(key)","label":"\(label)","kind":"\(kind)","default":"\(defaultValue)",
         \(capKey.map { "\"capKey\":\"\($0)\"," } ?? "")
         "disablable":\(disablable),"advanced":false,"preset":false,
         "help":"help for \(key)","docAnchor":"usage/config.md#fields"}
        """
        return try! JSONDecoder().decode(ConfigTunable.self, from: Data(json.utf8))
    }

    return ConfigSchema([
        tunable("vpn.tunnelInterfaces", "Your VPN tunnel", "list"),
        tunable("vpn.endpoints", "VPN server addresses", "list"),
        tunable("vpn.autoDetect", "Find my VPN tunnel automatically", "bool", defaultValue: "true"),
        tunable("vpn.autoDiscoverEndpoints", "Find the VPN server address automatically", "bool",
                defaultValue: "true"),
        tunable("vpn.autoArm", "Arm the guard when a VPN connects", "bool", defaultValue: "true"),
        tunable("vpn.allowLocalNetwork", "Keep local devices reachable", "bool", defaultValue: "true"),
        tunable("blockedCountries", "Blocked countries", "list", defaultValue: "IR,RU,KP"),
        tunable("pollInterval", "Exit country check interval", "duration", defaultValue: "15s"),
        tunable("vpn.switchWindow", "Switch window", "duration", defaultValue: "5s",
                capKey: "vpn.advanced.switchWindowMax", disablable: true),
        tunable("vpn.redialWindow", "Redial window", "duration", defaultValue: "30s",
                capKey: "vpn.advanced.redialWindowMax", disablable: true),
        tunable("vpn.pauseMax", "Longest pause", "duration", defaultValue: "30m0s", disablable: true),
        tunable("vpn.endpointGrace", "VPN server address grace", "duration", defaultValue: "15m0s"),
        tunable("vpn.endpointRefresh", "VPN server address refresh", "duration", defaultValue: "1m0s"),
        tunable("vpn.tunnelWatch", "Tunnel check interval", "duration", defaultValue: "1s"),
        tunable("vpn.advanced.switchWindowMax", "Switch window cap", "duration", defaultValue: "3m0s"),
        tunable("vpn.advanced.redialWindowMax", "Redial window cap", "duration", defaultValue: "10m0s"),
        tunable("vpn.advanced.redialMinUptime", "Redial anti-flap uptime", "duration",
                defaultValue: "15s", disablable: true),
        tunable("vpn.advanced.commandFreshness", "Command freshness", "duration", defaultValue: "30s"),
        tunable("vpn.advanced.windowDiscoveryInterval", "Window discovery interval", "duration",
                defaultValue: "1s"),
        tunable("vpn.advanced.tunnelPruneAfter", "Tunnel prune delay", "duration", defaultValue: "1m0s"),
        tunable("vpn.advanced.learnedEndpointTTL", "Learned address lifetime", "duration",
                defaultValue: "720h0m0s"),
        tunable("vpn.advanced.learnedMaxPerProfile", "Learned addresses per profile", "int",
                defaultValue: "16"),
        tunable("vpn.advanced.promoteAfterRefreshes", "Sightings before an address is learned", "int",
                defaultValue: "3"),
        tunable("vpn.advanced.endpointWarnThreshold", "Address-bloat warning threshold", "int",
                defaultValue: "256"),
        tunable("vpn.advanced.windowProtocols", "Window protocols", "list"),
        tunable("vpn.advanced.windowPorts", "Window ports", "list"),
        tunable("vpn.advanced.verifyInterval", "Enforcement verification interval", "duration",
                defaultValue: "1m0s", disablable: true),
        tunable("vpn.advanced.livenessRedial", "Redial on a hung tunnel", "bool", defaultValue: "false"),
    ])
}

struct SettingsFieldsTests {
    @Test func pairsCoverEveryStagedKey() {
        let pairs = SettingsFields().pairs()
        #expect(pairs.count == SettingsFields.keys.count)
        for (key, pair) in zip(SettingsFields.keys, pairs) {
            #expect(pair.hasPrefix("\(key)="), "expected pair \"\(pair)\" to start with \"\(key)=\"")
        }
    }

    /// The optional-key contract: unregistered, the key contributes no pair
    /// (an old CLI must never be sent a key it can't parse — and its `config
    /// get` for it must never be attempted, which is the caller's half);
    /// registered, it seeds, edits, and emits like any other key.
    @Test func optionalKeyParticipatesOnlyWhenRegistered() {
        let bare = SettingsFields(seeded: ["vpn.allowGeoProviders": "false"])
        #expect(!bare.pairs().contains { $0.hasPrefix("vpn.allowGeoProviders=") })
        #expect(bare.allowGeoProviders) // resting read is the daemon default
        #expect(!bare.stagedKeys.contains("vpn.allowGeoProviders"))

        let with = SettingsFields(seeded: ["vpn.allowGeoProviders": "false"],
                                  extraKeys: ["vpn.allowGeoProviders"])
        #expect(with.pairs().contains("vpn.allowGeoProviders=false"))
        #expect(!with.allowGeoProviders)
    }

    @Test func writingAnUnregisteredOptionalKeyIsDropped() {
        var f = SettingsFields()
        f.allowGeoProviders = false
        #expect(!f.pairs().contains { $0.hasPrefix("vpn.allowGeoProviders=") })
    }

    @Test func registerRefusesKeysOutsideTheOptionalList() {
        var f = SettingsFields()
        f.register(key: "vpn.enabled") // retired; must never gain storage
        #expect(!f.stagedKeys.contains("vpn.enabled"))
    }

    @Test func allowPhysicalDNSIsStagedAndRestsOn() {
        var f = SettingsFields()
        #expect(f.allowPhysicalDNS) // daemon default is true; resting must match
        f.allowPhysicalDNS = false
        #expect(f.pairs().contains("vpn.allowPhysicalDNS=false"))
    }

    /// The bug this storage change exists to make unrepresentable: a value must
    /// come back out under the key it went in under, whatever the key order is.
    @Test func everyValueIsStagedUnderItsOwnKey() {
        var f = SettingsFields()
        f.tunnelInterfaces = "utun9"
        f.endpoints = "203.0.113.5"
        f.autoDetect = true
        f.autoDiscover = false
        f.autoArm = true
        f.allowLocalNetwork = false
        f.blockedCountries = "IR,RU"
        f.pollInterval = "20s"
        f.switchWindow = "10s"
        f.redialWindow = "40s"
        f.pauseMax = "45m"
        f.endpointGrace = "5m"
        f.endpointRefresh = "1m"
        f.tunnelWatch = "2s"
        f.advSwitchWindowMax = "4m"
        f.advRedialWindowMax = "11m"
        f.advRedialMinUptime = "20s"
        f.advCommandFreshness = "45s"
        f.advWindowDiscoveryInterval = "2s"
        f.advTunnelPruneAfter = "90s"
        f.advLearnedEndpointTTL = "48h"
        f.advLearnedMaxPerProfile = "32"
        f.advPromoteAfterRefreshes = "5"
        f.advEndpointWarnThreshold = "512"
        f.advWindowProtocols = "udp,tcp"
        f.advWindowPorts = "51820,443"
        f.advVerifyInterval = "90s"
        f.advLivenessRedial = true

        // Named accessor and keyed lookup are the same storage, not two copies.
        #expect(f.value(for: "vpn.tunnelInterfaces") == "utun9")
        #expect(f.value(for: "vpn.switchWindow") == "10s")
        #expect(f.value(for: "vpn.pauseMax") == "45m")
        #expect(f.value(for: "vpn.advanced.windowPorts") == "51820,443")
        #expect(f.value(for: "vpn.autoDetect") == "true")
        #expect(f.value(for: "vpn.allowLocalNetwork") == "false")
        #expect(f.value(for: "vpn.advanced.verifyInterval") == "90s")
        #expect(f.value(for: "vpn.advanced.livenessRedial") == "true")

        let pairs = f.pairs()
        #expect(pairs.contains("vpn.switchWindow=10s"))
        #expect(pairs.contains("vpn.pauseMax=45m"))
        #expect(pairs.contains("blockedCountries=IR,RU"))
    }

    @Test func seedThenCurrentValuesRoundTrips() {
        let seeded = [
            "vpn.tunnelInterfaces": "utun4",
            "vpn.endpoints": "203.0.113.9",
            "vpn.autoDetect": "true",
            "vpn.switchWindow": "5s",
            "vpn.pauseMax": "30m0s",
            "vpn.advanced.windowPorts": "51820",
        ]
        let f = SettingsFields(seeded: seeded)
        for (key, value) in seeded {
            #expect(f.value(for: key) == value)
        }
        #expect(f.tunnelInterfaces == "utun4")
        #expect(f.autoDetect)
    }

    /// A key the seed did not include must keep its resting value rather than
    /// taking a neighbour's — the failure the positional version could not rule out.
    @Test func seedingASubsetLeavesOtherKeysAlone() {
        let f = SettingsFields(seeded: ["vpn.switchWindow": "12s"])
        #expect(f.switchWindow == "12s")
        #expect(f.redialWindow == "")
        #expect(f.pollInterval == "")
    }

    /// A key this pane does not stage must not create storage by being written.
    @Test func unknownKeysAreIgnored() {
        var f = SettingsFields(seeded: ["control.group": "wheel"])
        #expect(f.value(for: "control.group") == "")
        f.setValue("wheel", for: "control.group")
        #expect(f.value(for: "control.group") == "")
        #expect(!f.pairs().contains { $0.hasPrefix("control.group=") })
    }

    /// Labels now come from the daemon, so the pane and the validation alert
    /// cannot call the same field two different things.
    @Test func durationValidationUsesSchemaLabelsAndCoversEveryDurationKey() {
        let f = SettingsFields()
        let labels = Set(f.durationFieldsForValidation(schema: testSchema()).map(\.label))
        #expect(labels == [
            "Exit country check interval", "Switch window", "Redial window", "Longest pause",
            "VPN server address grace", "VPN server address refresh", "Tunnel check interval",
            "Switch window cap", "Redial window cap", "Redial anti-flap uptime",
            "Command freshness", "Window discovery interval", "Tunnel prune delay",
            "Learned address lifetime", "Enforcement verification interval",
        ])
    }

    @Test func durationValidationTrimsWhitespace() {
        var f = SettingsFields()
        f.pollInterval = "  15s  "
        let entry = f.durationFieldsForValidation(schema: testSchema())
            .first { $0.label == "Exit country check interval" }
        #expect(entry?.value == "15s")
    }

    /// Without a schema there is nothing to pre-validate. Returning nothing is
    /// deliberate: the daemon still rejects a malformed duration, whereas
    /// guessing the field set would miss one or invent a label.
    @Test func durationValidationIsEmptyWithoutASchema() {
        let f = SettingsFields()
        #expect(f.durationFieldsForValidation(schema: ConfigSchema([])).isEmpty)
    }
}

struct ConfigSchemaTests {
    @Test func decodesTheDaemonsShape() {
        let json = """
        [{"key":"vpn.redialWindow","label":"Redial window","kind":"duration","default":"30s",
          "capKey":"vpn.advanced.redialWindowMax","disablable":true,"advanced":false,"preset":true,
          "help":"How long the guard relaxes by itself.","docAnchor":"usage/config.md#vpn-block",
          "restartReason":""}]
        """
        let schema = ConfigSchema.decode(Data(json.utf8))
        let tunable = try! #require(schema?["vpn.redialWindow"])
        #expect(tunable.label == "Redial window")
        #expect(tunable.defaultValue == "30s")
        #expect(tunable.capKey == "vpn.advanced.redialWindowMax")
        #expect(tunable.disablable)
        #expect(tunable.preset)
        #expect(tunable.appliesLive)
    }

    @Test func aRestartReasonMeansItDoesNotApplyLive() {
        let json = """
        [{"key":"logLevel","label":"Log level","kind":"string","default":"info",
          "disablable":false,"advanced":false,"preset":false,"help":"h",
          "docAnchor":"usage/config.md#fields",
          "restartReason":"the logger is wired up before the run loop starts"}]
        """
        let schema = try! #require(ConfigSchema.decode(Data(json.utf8)))
        #expect(!(schema["logLevel"]?.appliesLive ?? true))
        #expect(schema.restartRequired(among: ["logLevel", "unknown"]) == ["logLevel"])
    }

    /// Every control's help link is only as good as the anchor it carries. Go's
    /// TestEveryTunableDocAnchorResolves proves the anchors exist in the
    /// bundled pages; this proves the app turns them into a page and a heading
    /// rather than dropping the fragment and landing at the top.
    @Test func docAnchorBecomesADeepLink() {
        let schema = testSchema()
        let target = try! #require(schema["vpn.endpointRefresh"]?.docTarget)
        #expect(target.source == "usage/config.md")
        #expect(target.anchor == "fields")
    }

    /// The placeholder states the real default, which is the whole point: the
    /// pane used to say "e.g. 30s" for a key whose default was 1m.
    @Test func placeholderStatesTheRealDefault() {
        let schema = testSchema()
        #expect(schema["vpn.endpointRefresh"]?.placeholder == "VPN server address refresh (default 1m0s)")
        // No default to state (an empty list): just the concept's name.
        #expect(schema["vpn.advanced.windowPorts"]?.placeholder == "Window ports")
    }

    /// A missing schema must degrade to a plainer label, never to a wrong value.
    @Test func placeholderFallsBackWithoutASchema() {
        let empty = ConfigSchema([])
        #expect(empty.placeholder(for: "vpn.redialWindow", fallback: "Redial window") == "Redial window")
        #expect(empty.help(for: "vpn.redialWindow") == nil)
    }

    /// The ceiling is read from the live config, not from the cap's default —
    /// lowering a cap by hand must actually lower the control's top.
    @Test func capResolvesThroughSeededValues() {
        let schema = testSchema()
        #expect(schema.cap(for: "vpn.switchWindow", in: ["vpn.advanced.switchWindowMax": "90s"]) == "90s")
        // Falls back to the cap's own default when the value is not to hand.
        #expect(schema.cap(for: "vpn.switchWindow", in: [:]) == "3m0s")
        // An unbounded key has no ceiling.
        #expect(schema.cap(for: "vpn.pauseMax", in: [:]) == nil)
    }
}
