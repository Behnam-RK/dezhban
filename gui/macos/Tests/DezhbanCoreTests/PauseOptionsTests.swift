import Foundation
import Testing
@testable import DezhbanCore

struct PauseOptionsTests {
    /// Decodes what `dezhban pause --list --json` actually emits.
    @Test func decodesTheDaemonsList() {
        let json = """
        [
          {"label":"15 minutes","value":"15m0s","why":"a bank transfer or a delivery tracker"},
          {"label":"2 hours","value":"2h0m0s","why":"an evening of something the VPN can't reach",
           "unavailable":"longer than your 30m0s cap (vpn.pauseMax)"}
        ]
        """.data(using: .utf8)!
        let opts = try! #require(PauseOption.decodeList(json))
        #expect(opts.count == 2)
        #expect(opts[0].isAvailable)
        #expect(opts[0].value == "15m0s")
        #expect(!opts[1].isAvailable)
    }

    /// An over-cap length is still an option — shown, disabled, explained. The
    /// alternative teaches the user their cap is something other than it is.
    @Test func overCapOptionsAreKeptWithTheirReason() {
        let json = """
        [{"label":"2 hours","value":"2h0m0s","why":"a long evening",
          "unavailable":"longer than your 30m0s cap (vpn.pauseMax)"}]
        """.data(using: .utf8)!
        let opts = try! #require(PauseOption.decodeList(json))
        #expect(opts.count == 1, "an unavailable option must not be dropped at decode time")
        #expect(opts[0].unavailable?.contains("vpn.pauseMax") == true,
                "the reason must name the setting to change")
    }

    /// Absent `unavailable` means available — the JSON omits it rather than
    /// sending an empty string.
    @Test func absentUnavailableMeansAvailable() {
        let json = #"[{"label":"5 minutes","value":"5m0s","why":"one stubborn site"}]"#.data(using: .utf8)!
        let opts = try! #require(PauseOption.decodeList(json))
        #expect(opts[0].isAvailable)
        #expect(opts[0].unavailable == nil)
    }

    @Test func corruptDataFailsToDecode() {
        #expect(PauseOption.decodeList(Data("not json".utf8)) == nil)
    }
}
