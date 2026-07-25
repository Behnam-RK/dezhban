import Testing
@testable import DezhbanCore

struct DurationTextTests {
    @Test(arguments: ["0", "30s", "5m", "1h30m", "500ms", "-1.5h", "+2m", "1h", "0.5s"])
    func acceptsValidGoDurations(_ s: String) {
        #expect(DurationText.looksLikeGoDuration(s))
    }

    @Test(arguments: ["", "s", "abc", "5", "15x", " ", "5 m"])
    func rejectsInvalidGoDurations(_ s: String) {
        #expect(!DurationText.looksLikeGoDuration(s))
    }

    @Test func pendingRestartKeysExtractsCommaSeparatedList() {
        let output = """
            set pollInterval = 20s  (/etc/dezhban/config.json)
            Saved and applied: pollInterval
            Restart dezhban to apply: logLevel, providers
            """
        #expect(DurationText.pendingRestartKeys(in: output) == ["logLevel", "providers"])
    }

    @Test func pendingRestartKeysEmptyWhenMarkerAbsent() {
        let output = "Saved and applied: pollInterval\n"
        #expect(DurationText.pendingRestartKeys(in: output) == [])
    }

    @Test func pendingRestartKeysTrimsWhitespace() {
        let output = "Restart dezhban to apply:  logLevel ,  providers  \n"
        #expect(DurationText.pendingRestartKeys(in: output) == ["logLevel", "providers"])
    }
}
