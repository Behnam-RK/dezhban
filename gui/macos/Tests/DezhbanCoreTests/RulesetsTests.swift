import Foundation
import Testing
@testable import DezhbanCore

/// The producer side — what gets recorded, and when — is pinned by Go's
/// internal/applied and internal/runner tests. This is the consumer side: that
/// the app decodes what `print-rules --applied/--installed --json` emits.
struct RulesetsTests {
    /// Go's encoding/json writes time.Time as RFC 3339 with fractional seconds.
    /// Foundation's `.iso8601` strategy rejects those outright, which would turn
    /// a perfectly good record into "no rules recorded" — a pane claiming the
    /// guard had installed nothing while it was enforcing.
    @Test func decodesGosFractionalTimestamps() throws {
        let json = """
        {"version":1,"mode":"guard","at":"2026-08-21T14:02:11.123456+02:00",
         "rules":"block drop out all\\n","backend":"pf"}
        """
        let a = try #require(AppliedRuleset.decode(Data(json.utf8)))
        #expect(a.mode == "guard")
        #expect(a.backend == "pf")
        #expect(a.rules == "block drop out all\n")
    }

    /// Whole seconds, no fraction — what Go emits when the instant happens to
    /// land on one. Both forms have to decode or the pane works only sometimes.
    @Test func decodesWholeSecondTimestamps() throws {
        let json = """
        {"version":1,"mode":"fullblock","at":"2026-08-21T14:02:11Z","rules":"x\\n","backend":"nft"}
        """
        let a = try #require(AppliedRuleset.decode(Data(json.utf8)))
        #expect(a.mode == "fullblock")
        #expect(a.backend == "nft")
    }

    /// `null` is what the CLI prints when nothing has been recorded — an
    /// ordinary state, not a parse failure, and the caller shows "nothing
    /// recorded yet" for both.
    @Test func nullIsNotARecord() {
        #expect(AppliedRuleset.decode(Data("null".utf8)) == nil)
    }

    @Test func decodesAnInstalledReadbackWithItsNestedRecord() throws {
        let json = """
        {"installed":"block drop out all\\n","loaded":true,
         "applied":{"version":1,"mode":"guard","at":"2026-08-21T14:02:11.5Z",
                    "rules":"block drop out all\\n","backend":"pf"},
         "drift":false,"backend":"pf"}
        """
        let i = try #require(InstalledRuleset.decode(Data(json.utf8)))
        #expect(i.loaded)
        #expect(!i.drift)
        #expect(i.applied?.mode == "guard")
    }

    /// Rules recorded, none in the kernel: the finding this readback exists to
    /// surface. It must survive decoding intact — a drift flag lost in transit
    /// is a tampering report nobody sees.
    @Test func driftSurvivesDecoding() throws {
        let json = """
        {"installed":"","loaded":false,"drift":true,"backend":"pf"}
        """
        let i = try #require(InstalledRuleset.decode(Data(json.utf8)))
        #expect(i.drift)
        #expect(!i.loaded)
        #expect(i.applied == nil)
    }

    /// The preview modes are the stable `print-rules --mode` identifiers named
    /// in CLAUDE.md. Renaming one to read better breaks the CLI contract.
    @Test func previewModesAreTheStableCLIIdentifiers() {
        #expect(RulesetPreview.allCases.map(\.rawValue) == ["guard", "fullblock", "switch"])
        for mode in RulesetPreview.allCases {
            #expect(!mode.label.isEmpty)
            #expect(!mode.detail.isEmpty)
        }
    }
}
