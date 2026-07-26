import Testing
@testable import DezhbanCore

struct DurationChoicesTests {
    // MARK: - parsing

    @Test(arguments: [
        ("30s", 30.0), ("5m", 300.0), ("1h", 3600.0), ("1h30m", 5400.0),
        ("720h", 2_592_000.0), ("1m0s", 60.0), ("30m0s", 1800.0), ("0s", 0.0),
    ])
    func parsesGoDurations(_ c: (String, Double)) {
        #expect(DurationChoices.seconds(c.0) == c.1)
    }

    @Test(arguments: ["", "off", "15x", "abc", "m30", "15", "1h30"])
    func rejectsNonDurations(_ text: String) {
        #expect(DurationChoices.seconds(text) == nil)
    }

    /// A value the app writes must read back identically from `config get`,
    /// which reports Go's own Duration.String rendering.
    @Test(arguments: [(0, "0s"), (45, "45s"), (60, "1m0s"), (90, "1m30s"),
                      (1800, "30m0s"), (3600, "1h0m0s"), (5400, "1h30m0s")])
    func rendersLikeGo(_ c: (Int, String)) {
        #expect(DurationChoices.goDuration(c.0) == c.1)
    }

    @Test func goDurationRoundTripsThroughTheParser() {
        for secs in [1, 45, 60, 90, 300, 1800, 3600, 5400, 2_592_000] {
            let rendered = DurationChoices.goDuration(secs)
            #expect(DurationChoices.seconds(rendered) == Double(secs),
                    "\(secs)s rendered as \(rendered) did not parse back")
        }
    }

    // MARK: - the off sentinel

    /// The round trip spells "off" three ways: the app writes "0",
    /// `config get` reports "0s", and KeyValues renders "off". A control that
    /// recognised only one would show a disabled setting as enabled.
    @Test(arguments: ["0", "0s", "off", " 0s "])
    func recognisesEverySpellingOfOff(_ value: String) {
        #expect(DurationChoices.isOff(value))
    }

    @Test(arguments: ["30s", "1m0s", ""])
    func doesNotMistakeADurationForOff(_ value: String) {
        #expect(!DurationChoices.isOff(value))
    }

    // MARK: - choices

    /// Off is offered only where the schema says the sentinel survives. Offering
    /// it elsewhere would present a security choice that silently does nothing.
    @Test func offIsOfferedOnlyForDisablableKeys() {
        let disablable = DurationChoices.build(defaultValue: "30s", cap: "10m0s", disablable: true)
        #expect(disablable.contains { $0.isOff })

        let notDisablable = DurationChoices.build(defaultValue: "1m0s", cap: nil, disablable: false)
        #expect(!notDisablable.contains { $0.isOff })
    }

    /// Nothing above the cap is offered: this menu lists what will be accepted,
    /// so an option the daemon would reject is noise.
    @Test func nothingAboveTheCapIsOffered() {
        let choices = DurationChoices.build(defaultValue: "30s", cap: "1m0s", disablable: true)
        for c in choices where !c.isOff {
            let secs = try! #require(DurationChoices.seconds(c.value))
            #expect(secs <= 60, "\(c.label) is above the 1m cap")
        }
    }

    /// The cap itself is always reachable — it is the most relaxed setting the
    /// operator has allowed, and hitting it by doubling upward is luck.
    @Test func theCapItselfIsAlwaysOffered() {
        let choices = DurationChoices.build(defaultValue: "30s", cap: "10m0s", disablable: true)
        #expect(choices.contains { $0.value == "10m0s" })
    }

    /// Lowering the cap must move the choices, which is the point of deriving
    /// them from the schema rather than listing them.
    @Test func loweringTheCapNarrowsTheChoices() {
        let wide = DurationChoices.build(defaultValue: "5s", cap: "3m0s", disablable: true)
        let narrow = DurationChoices.build(defaultValue: "5s", cap: "10s", disablable: true)
        #expect(narrow.count < wide.count)
        #expect(!narrow.contains { $0.value == "3m0s" })
    }

    @Test func theShippedDefaultIsMarked() {
        let choices = DurationChoices.build(defaultValue: "30s", cap: "10m0s", disablable: false)
        let defaults = choices.filter(\.isDefault)
        #expect(defaults.count == 1)
        #expect(defaults.first?.value == "30s")
    }

    @Test func choicesAreUnique() {
        let choices = DurationChoices.build(defaultValue: "1s", cap: "8s", disablable: true)
        #expect(Set(choices.map(\.value)).count == choices.count)
    }

    /// An unusable default must not produce a bogus menu — Off (where real) and
    /// nothing else, leaving Custom as the way in.
    @Test func anUnparsableDefaultYieldsNoInventedChoices() {
        let choices = DurationChoices.build(defaultValue: "", cap: "1m0s", disablable: true)
        #expect(choices.count == 1)
        #expect(choices.first?.isOff == true)
        #expect(DurationChoices.build(defaultValue: "nonsense", cap: nil, disablable: false).isEmpty)
    }

    @Test func humanizesForReading() {
        #expect(DurationChoices.humanize(0) == "Off")
        #expect(DurationChoices.humanize(1) == "1 second")
        #expect(DurationChoices.humanize(30) == "30 seconds")
        #expect(DurationChoices.humanize(60) == "1 minute")
        #expect(DurationChoices.humanize(1800) == "30 minutes")
        #expect(DurationChoices.humanize(3600) == "1 hour")
        #expect(DurationChoices.humanize(2_592_000) == "720 hours")
    }
}
