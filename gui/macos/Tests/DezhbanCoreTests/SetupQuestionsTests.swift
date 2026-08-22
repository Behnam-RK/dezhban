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
      {"id":"autoMode","kind":"bool","title":"Use automatic VPN detection? (recommended)",
       "default":"true","group":2},
      {"id":"tunnels","key":"vpn.tunnelInterfaces","kind":"multiselect","title":"Tunnel interface(s)",
       "options":[{"label":"utun4","value":"utun4"},{"label":"utun7","value":"utun7"}],
       "selected":["utun4"],"group":2,"requiresId":"autoMode","requiresValue":"false"},
      {"id":"profileFiles","kind":"list","title":"Self-hosted VPN config files","group":2,
       "requiresId":"autoMode","requiresValue":"false"},
      {"id":"endpoints","key":"vpn.endpoints","kind":"list","title":"VPN endpoint(s)",
       "default":"203.0.113.7","group":2,"requiresId":"autoMode","requiresValue":"false"}
    ]
    """

    static func questions() throws -> [SetupQuestion] {
        try #require(SetupQuestion.decodeList(Data(json.utf8)))
    }

    @Test func decodesTheDaemonsQuestions() throws {
        let qs = try Self.questions()
        #expect(qs.count == 7)
        let countries = try #require(qs.first { $0.id == "blockedCountries" })
        #expect(countries.selected == ["IR"])
        #expect(countries.options.map(\.value) == ["IR", "RU"])
        // Absent `key` decodes as "no config key", not as a decode failure.
        #expect(try #require(qs.first { $0.id == "otherCountries" }).key.isEmpty)
        // autoMode is the gate now, not a gated question — everything manual
        // hangs off it, and it hangs off nothing.
        #expect(!(try #require(qs.first { $0.id == "autoMode" }).isGated))
        #expect(try #require(qs.first { $0.id == "endpoints" }).isGated)
        #expect(!(try #require(qs.first { $0.id == "pollInterval" }).isGated))
    }

    @Test func answersSeedFromTheQuestions() throws {
        let a = SetupAnswers(questions: try Self.questions())
        #expect(a["pollInterval"] == "15s")
        #expect(a.list("blockedCountries") == ["IR"])
        #expect(a.bool("autoMode"))
    }

    /// Two steps, matching Go's TestTheWizardIsTwoGroups. The app renders one
    /// group per step, so a third group is a third screen.
    @Test func theWizardIsTwoSteps() throws {
        #expect(Set(try Self.questions().map(\.group)) == [1, 2])
    }

    /// Step 2 is one automatic-detection tickbox with every manual field hanging
    /// off it — the reveal-in-place the app renders, and the same gate the CLI
    /// evaluates.
    @Test func gatingMatchesTheCLI() throws {
        let qs = try Self.questions()
        var a = SetupAnswers(questions: qs)

        a["autoMode"] = "true"
        for q in qs where q.requiresID == "autoMode" {
            #expect(!a.shouldAsk(q), "\(q.id) should be hidden under automatic detection")
        }

        a["autoMode"] = "false"
        for id in ["tunnels", "profileFiles", "endpoints"] {
            #expect(a.shouldAsk(try #require(qs.first { $0.id == id })),
                    "\(id) should be asked once automatic detection is unticked")
        }
    }

    /// The rule that replaced "configure your VPN now?": a question that was
    /// never shown writes no key. Under automatic detection on macOS the
    /// endpoint question is hidden, so a re-run must produce no
    /// `vpn.endpoints=` pair — writing an empty one would delete a configured
    /// server. Mirrors Go's TestAnUnaskedEndpointListTouchesNoEndpoint.
    @Test func anUnaskedQuestionWritesNoKey() throws {
        let qs = try Self.questions()
        var a = SetupAnswers(questions: qs)
        a["autoMode"] = "true"

        let pairs = a.configPairs(for: qs)
        #expect(!pairs.contains { $0.hasPrefix("vpn.endpoints=") })
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
        a["autoMode"] = "true"

        let pairs = a.configPairs(for: qs)
        #expect(pairs.contains("vpn.tunnelInterfaces="))
    }

    @Test func pinningWritesTheChosenInterfaces() throws {
        let qs = try Self.questions()
        var a = SetupAnswers(questions: qs)
        a["autoMode"] = "false"
        a["tunnels"] = "utun4,utun7"

        let pairs = a.configPairs(for: qs)
        #expect(pairs.contains("vpn.tunnelInterfaces=utun4,utun7"))
        #expect(pairs.filter { $0.hasPrefix("vpn.tunnelInterfaces=") }.count == 1,
                "a pinned config must not also be cleared")
    }

    /// A wizard seeded with `autoMode: false` — which the daemon does whenever
    /// vpn.tunnelInterfaces is pinned — and clicked straight through must write
    /// those pins back, not clear them. This is the consumer side of Go's
    /// TestAutoModeSeedsFalseWhenInterfacesArePinned: the app renders whatever
    /// default arrives, so the guard only holds if seeding drives it.
    @Test func aSeededManualModeReWritesItsPins() throws {
        let qs = try Self.questions().map { q -> SetupQuestion in
            guard q.id == "autoMode" else { return q }
            return SetupQuestion(questionID: q.questionID, key: q.key, kind: q.kind,
                                 title: q.title, description: q.description,
                                 options: q.options, defaultValue: "false",
                                 selected: q.selected, group: q.group,
                                 requiresID: q.requiresID, requiresValue: q.requiresValue)
        }
        let a = SetupAnswers(questions: qs)
        #expect(!a.bool("autoMode"), "the seeded default must drive the answer")

        let pairs = a.configPairs(for: qs)
        #expect(pairs.contains("vpn.tunnelInterfaces=utun4"))
        #expect(!pairs.contains("vpn.tunnelInterfaces="))
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

    // MARK: - the shrunk wizard

    /// The daemon's question list after the 2026-08 shrink: blocked countries,
    /// auto-vs-manual and the gated VPN details — nothing else. Everything above must keep working with this list, because the
    /// view renders whatever arrives, and the id-keyed special cases
    /// (blockedCountries+otherCountries fold, autoMode's tunnel clearing) must
    /// hold with the surrounding questions gone.
    static let shrunkJSON = """
    [
      {"id":"blockedCountries","key":"blockedCountries","kind":"multiselect","title":"Blocked countries",
       "options":[{"label":"Iran (IR)","value":"IR"},{"label":"Russia (RU)","value":"RU"}],
       "selected":["IR"],"group":1},
      {"id":"otherCountries","kind":"list","title":"Other country codes","default":"AQ","group":1},
      {"id":"autoMode","kind":"bool","title":"Use automatic VPN detection? (recommended)",
       "default":"true","group":2},
      {"id":"tunnels","key":"vpn.tunnelInterfaces","kind":"multiselect","title":"Tunnel interface(s)",
       "options":[{"label":"utun4","value":"utun4"}],"selected":[],"group":2,
       "requiresId":"autoMode","requiresValue":"false"},
      {"id":"profileFiles","kind":"list","title":"Self-hosted VPN config files","group":2,
       "requiresId":"autoMode","requiresValue":"false"},
      {"id":"endpoints","key":"vpn.endpoints","kind":"list","title":"VPN endpoint(s)","group":2,
       "requiresId":"autoMode","requiresValue":"false"}
    ]
    """

    static func shrunkQuestions() throws -> [SetupQuestion] {
        try #require(SetupQuestion.decodeList(Data(shrunkJSON.utf8)))
    }

    @Test func shrunkListDecodesAndGates() throws {
        let qs = try Self.shrunkQuestions()
        #expect(qs.count == 6)
        var a = SetupAnswers(questions: qs)
        // Default flow: automatic detection on — the tunnel question is never
        // asked.
        let tunnels = try #require(qs.first { $0.id == "tunnels" })
        #expect(!a.shouldAsk(tunnels))
        a["autoMode"] = "false"
        #expect(a.shouldAsk(tunnels))
    }

    @Test func shrunkListFoldsOtherCountries() throws {
        let qs = try Self.shrunkQuestions()
        var a = SetupAnswers(questions: qs)
        a["otherCountries"] = "AQ, SY"
        let pairs = a.configPairs(for: qs)
        #expect(pairs.contains("blockedCountries=IR,AQ,SY"))
        // No stray dropped-question keys can appear: the list carries none.
        #expect(!pairs.contains { $0.hasPrefix("pollInterval=") })
        #expect(!pairs.contains { $0.hasPrefix("logLevel=") })
        #expect(!pairs.contains { $0.hasPrefix("providerQuorum=") })
        #expect(!pairs.contains { $0.hasPrefix("vpn.allowPhysicalDNS=") })
        #expect(!pairs.contains { $0.hasPrefix("vpn.autoDiscoverEndpoints=") })
    }

    @Test func shrunkListStillClearsPinsForAutoMode() throws {
        let qs = try Self.shrunkQuestions()
        var a = SetupAnswers(questions: qs)
        a["endpoints"] = "203.0.113.7"
        var pairs = a.configPairs(for: qs)
        // autoMode defaults true → pinned interfaces cleared, and the endpoint
        // question is not asked, so its answer is not written even though one
        // was set.
        #expect(pairs.contains("vpn.tunnelInterfaces="))
        #expect(!pairs.contains { $0.hasPrefix("vpn.endpoints=") })

        a["autoMode"] = "false"
        pairs = a.configPairs(for: qs)
        #expect(pairs.contains("vpn.endpoints=203.0.113.7"))
    }
}
