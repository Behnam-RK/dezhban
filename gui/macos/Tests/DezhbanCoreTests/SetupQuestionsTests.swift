import Foundation
import Testing
@testable import DezhbanCore

/// The producer side — what the questions ARE, how they are gated, and what
/// they apply to — is pinned by Go's internal/setup tests. This is the consumer
/// side: that the app decodes that JSON, gates the same way, and turns answers
/// into the same config writes. A difference here is two wizards asking the
/// same question and doing different things with the answer.
struct SetupQuestionsTests {
    /// Exactly what `dezhban setup --questions --json` emits, `omitempty` and
    /// all: `key`, `options`, `selected`, and the gate fields are absent on the
    /// questions that do not have them.
    static let json = """
    [
      {"id":"pollInterval","key":"pollInterval","kind":"duration","title":"Poll interval",
       "description":"How often the exit country is checked, e.g. 30s.","default":"15s","group":1},
      {"id":"blockedCountries","key":"blockedCountries","kind":"multiselect","title":"Blocked countries",
       "options":[{"label":"Iran (IR)","value":"IR"},{"label":"Russia (RU)","value":"RU"}],
       "selected":["IR"],"group":1},
      {"id":"otherCountries","kind":"list","title":"Other country codes","default":"AQ","group":1},
      {"id":"configureVPN","kind":"bool","title":"Configure your VPN now?","default":"true","group":1},
      {"id":"autoMode","kind":"bool","title":"Use automatic VPN detection? (recommended)",
       "default":"true","group":2,"requiresId":"configureVPN","requiresValue":"true"},
      {"id":"tunnels","key":"vpn.tunnelInterfaces","kind":"multiselect","title":"Tunnel interface(s)",
       "options":[{"label":"utun4","value":"utun4"},{"label":"utun7","value":"utun7"}],
       "selected":["utun4"],"group":3,"requiresId":"autoMode","requiresValue":"false"},
      {"id":"profileFiles","kind":"list","title":"Self-hosted VPN config files","group":4,
       "requiresId":"configureVPN","requiresValue":"true"},
      {"id":"endpoints","key":"vpn.endpoints","kind":"list","title":"VPN endpoint(s)",
       "default":"203.0.113.7","group":4,"requiresId":"configureVPN","requiresValue":"true"}
    ]
    """

    static func questions() throws -> [SetupQuestion] {
        try #require(SetupQuestion.decodeList(Data(json.utf8)))
    }

    @Test func decodesTheDaemonsQuestions() throws {
        let qs = try Self.questions()
        #expect(qs.count == 8)
        let countries = try #require(qs.first { $0.id == "blockedCountries" })
        #expect(countries.selected == ["IR"])
        #expect(countries.options.map(\.value) == ["IR", "RU"])
        // Absent `key` decodes as "no config key", not as a decode failure.
        #expect(try #require(qs.first { $0.id == "otherCountries" }).key.isEmpty)
        #expect(try #require(qs.first { $0.id == "autoMode" }).isGated)
        #expect(!(try #require(qs.first { $0.id == "pollInterval" }).isGated))
    }

    @Test func answersSeedFromTheQuestions() throws {
        let a = SetupAnswers(questions: try Self.questions())
        #expect(a["pollInterval"] == "15s")
        #expect(a.list("blockedCountries") == ["IR"])
        #expect(a.bool("configureVPN"))
    }

    @Test func gatingMatchesTheCLI() throws {
        let qs = try Self.questions()
        var a = SetupAnswers(questions: qs)

        a["configureVPN"] = "false"
        for q in qs where q.requiresID == "configureVPN" {
            #expect(!a.shouldAsk(q), "\(q.id) should be hidden when the VPN branch is declined")
        }

        a["configureVPN"] = "true"
        a["autoMode"] = "true"
        #expect(!a.shouldAsk(try #require(qs.first { $0.id == "tunnels" })))
        a["autoMode"] = "false"
        #expect(a.shouldAsk(try #require(qs.first { $0.id == "tunnels" })))
    }

    /// The free-text codes fold into the same key as the checkboxes — they are
    /// one setting asked as two questions, and writing them separately would
    /// mean the second overwrote the first.
    @Test func otherCountriesFoldIntoBlockedCountries() throws {
        let qs = try Self.questions()
        var a = SetupAnswers(questions: qs)
        a["blockedCountries"] = "IR,RU"
        a["otherCountries"] = "AQ, KP"

        let pairs = a.configPairs(for: qs)
        #expect(pairs.contains("blockedCountries=IR,RU,AQ,KP"))
        // And never as a key of its own.
        #expect(!pairs.contains { $0.hasPrefix("otherCountries=") })
    }

    /// Choosing automatic detection must CLEAR pinned interfaces, not skip the
    /// key: a leftover pin is exactly what stops autodetect from happening.
    @Test func automaticDetectionClearsPinnedInterfaces() throws {
        let qs = try Self.questions()
        var a = SetupAnswers(questions: qs)
        a["configureVPN"] = "true"
        a["autoMode"] = "true"

        let pairs = a.configPairs(for: qs)
        #expect(pairs.contains("vpn.tunnelInterfaces="))
    }

    @Test func pinningWritesTheChosenInterfaces() throws {
        let qs = try Self.questions()
        var a = SetupAnswers(questions: qs)
        a["configureVPN"] = "true"
        a["autoMode"] = "false"
        a["tunnels"] = "utun4,utun7"

        let pairs = a.configPairs(for: qs)
        #expect(pairs.contains("vpn.tunnelInterfaces=utun4,utun7"))
        #expect(pairs.filter { $0.hasPrefix("vpn.tunnelInterfaces=") }.count == 1,
                "a pinned config must not also be cleared")
    }

    /// Declining the VPN branch writes none of its keys, so a VPN somebody
    /// already configured is left alone — the same rule as Go's setup.Apply.
    @Test func decliningTheVPNBranchWritesNoVPNKey() throws {
        let qs = try Self.questions()
        var a = SetupAnswers(questions: qs)
        a["configureVPN"] = "false"

        let pairs = a.configPairs(for: qs)
        #expect(!pairs.contains { $0.hasPrefix("vpn.") })
        // The answers that were given still apply.
        #expect(pairs.contains { $0.hasPrefix("pollInterval=") })
    }

    /// Profile files are not a config key: they become profiles through
    /// `vpn import`, so they must never reach `config set`.
    @Test func profileFilesAreNotAConfigKey() throws {
        let qs = try Self.questions()
        var a = SetupAnswers(questions: qs)
        a["profileFiles"] = "/tmp/home.conf, /tmp/work.ovpn"

        #expect(a.profileFiles == ["/tmp/home.conf", "/tmp/work.ovpn"])
        #expect(!a.configPairs(for: qs).contains { $0.contains("profileFiles") })
    }

    @Test func firstRunIsOfferedOnlyWhenNothingIsKnownYet() {
        // Whether the flag is set is UserDefaults' business; whether the wizard
        // should be offered given that flag is a rule, and lives in the core.
        #expect(FirstRunDecision.offer(isComplete: false, vpnKnown: false))
        #expect(!FirstRunDecision.offer(isComplete: false, vpnKnown: true),
                "a VPN configured from the CLI means these questions were already answered")
        #expect(!FirstRunDecision.offer(isComplete: true, vpnKnown: false))
    }
}
