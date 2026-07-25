import Testing
@testable import DezhbanCore

struct SettingsFieldsTests {
    @Test func keysAndPairsHaveTheSameCount() {
        #expect(SettingsFields.keys.count == SettingsFields().pairs().count)
    }

    @Test func everyPairsEntryIsKeyedByTheCorrespondingKey() {
        // Ordering matters: init(seeded:) and pairs() must agree on which index
        // is which key, or a value gets staged under the WRONG key silently.
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
        f.endpointGrace = "5m"
        f.endpointRefresh = "1m"
        f.tunnelWatch = "2s"

        let pairs = f.pairs()
        for (key, pair) in zip(SettingsFields.keys, pairs) {
            #expect(pair.hasPrefix("\(key)="), "expected pair \"\(pair)\" to start with \"\(key)=\"")
        }
    }

    @Test func seedThenPairsRoundTrips() {
        // Values in SettingsFields.keys order.
        let seeded = [
            "utun4", "203.0.113.9",
            "true", "true", "false",
            "true",
            "IR,RU", "15s",
            "5s", "30s", "15m",
            "1m", "1s",
        ]
        let f = SettingsFields(seeded: seeded)
        #expect(f.currentValues == seeded)
    }

    @Test func durationFieldsForValidationCoversEveryDurationKey() {
        let f = SettingsFields()
        let labels = Set(f.durationFieldsForValidation.map(\.label))
        #expect(labels == [
            "Geo IP lookup interval", "Switch window", "Redial window",
            "Endpoint grace", "Endpoint refresh", "Tunnel watch",
        ])
    }

    @Test func durationFieldsForValidationTrimsWhitespace() {
        var f = SettingsFields()
        f.pollInterval = "  15s  "
        let entry = f.durationFieldsForValidation.first { $0.label == "Geo IP lookup interval" }
        #expect(entry?.value == "15s")
    }
}
