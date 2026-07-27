import Foundation
import Testing
@testable import DezhbanCore

/// The producer side — that help-index.json is written with these keys, and
/// that every page in it has headings and text — is pinned by Go's
/// internal/help TestIndexDecodes. This is the consumer side: that the app
/// decodes that exact shape, and that the sidebar and search built on it behave.
struct HelpIndexTests {
    /// The shape `help.Build` writes, including `tutorial` absent on a
    /// reference page (Go's `omitempty`) — the field most likely to be missing,
    /// and the one whose absence would break decoding hardest.
    static let indexJSON = """
    [
      {
        "file": "usage-getting-started.html",
        "source": "usage/getting-started.md",
        "title": "Quick start",
        "summary": "Install it, set it up, and arm it.",
        "tutorial": 1,
        "headings": [
          {"level": 1, "text": "Getting started", "anchor": "getting-started"},
          {"level": 2, "text": "Install", "anchor": "install"}
        ],
        "text": "Getting started\\nInstall dezhban and check it will not cut your traffic by surprise.\\n"
      },
      {
        "file": "usage-config.html",
        "source": "usage/config.md",
        "title": "Configuration reference",
        "summary": "Every setting: what it does and what it costs.",
        "headings": [
          {"level": 2, "text": "Fields", "anchor": "fields"},
          {"level": 2, "text": "vpn block", "anchor": "vpn-block"}
        ],
        "text": "Fields\\nvpn.pauseMax bounds a pause, and how long traffic stays uncut.\\n"
      },
      {
        "file": "concepts-modes.html",
        "source": "concepts/modes.md",
        "title": "Postures",
        "summary": "Every posture the guard can be in.",
        "tutorial": 2,
        "headings": [{"level": 2, "text": "FULL BLOCK", "anchor": "full-block"}],
        "text": "FULL BLOCK cuts everything but the geo lookup.\\n"
      }
    ]
    """

    /// Writes the fixture where `HelpBundle.load` expects it, the same way the
    /// app reads it out of its own bundle.
    static func fixture() throws -> HelpBundle {
        let dir = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("help-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        try Data(indexJSON.utf8).write(to: dir.appendingPathComponent("help-index.json"))
        return try #require(HelpBundle.load(directory: dir))
    }

    @Test func decodesTheShapeHelpgenWrites() throws {
        let bundle = try Self.fixture()
        #expect(bundle.pages.count == 3)
        let config = try #require(bundle.page(source: "usage/config.md"))
        // The omitted `tutorial` reads as reference material, not as a decode
        // failure that would drop the page from the sidebar entirely.
        #expect(config.tutorial == 0)
        #expect(config.file == "usage-config.html")
        #expect(config.hasAnchor("fields"))
        #expect(!config.hasAnchor("no-such-heading"))
    }

    /// A missing index has to be nil rather than an empty bundle, so the pane
    /// can say this build shipped without its docs instead of showing an empty
    /// sidebar and looking broken.
    @Test func aMissingIndexIsNotAnEmptyBundle() {
        let dir = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("help-missing-\(UUID().uuidString)")
        #expect(HelpBundle.load(directory: dir) == nil)
    }

    @Test func tutorialComesFirstAndInOrder() throws {
        let bundle = try Self.fixture()
        #expect(bundle.tutorial.map(\.title) == ["Quick start", "Postures"])
        #expect(bundle.reference.map(\.title) == ["Configuration reference"])
    }

    // MARK: - Deep links

    @Test func docAnchorParses() {
        #expect(HelpTarget(docAnchor: "usage/config.md#fields")?.source == "usage/config.md")
        #expect(HelpTarget(docAnchor: "usage/config.md#fields")?.anchor == "fields")
        // A page with no fragment is a whole-page link, not a broken one.
        #expect(HelpTarget(docAnchor: "usage/cli.md")?.anchor == nil)
        #expect(HelpTarget(docAnchor: "usage/cli.md#")?.anchor == nil)
        #expect(HelpTarget(docAnchor: "#fields") == nil)
    }

    /// A stale anchor must still open the right page. Landing at the top of the
    /// configuration reference is a bad link; landing nowhere is a dead control.
    @Test func aStaleAnchorStillOpensThePage() throws {
        let bundle = try Self.fixture()
        let good = try #require(bundle.resolve(HelpTarget(source: "usage/config.md", anchor: "fields")))
        #expect(good.anchor == "fields")

        let stale = try #require(bundle.resolve(HelpTarget(source: "usage/config.md", anchor: "gone")))
        #expect(stale.page.title == "Configuration reference")
        #expect(stale.anchor == nil)

        #expect(bundle.resolve(HelpTarget(source: "docs/nope.md", anchor: nil)) == nil)
    }

    // MARK: - Search

    @Test func searchPrefersTitleThenHeadingThenBody() throws {
        let bundle = try Self.fixture()

        // A title match wins outright.
        #expect(bundle.search("postures").first?.page.title == "Postures")

        // A heading match carries the anchor, so the result lands on the
        // section rather than the top of a long reference page.
        let heading = try #require(bundle.search("full block").first)
        #expect(heading.page.title == "Postures")
        #expect(heading.anchor == "full-block")

        // A body-only match still finds the page, and reports the line it
        // matched so the result says what it found, not just where.
        let body = try #require(bundle.search("pauseMax").first)
        #expect(body.page.title == "Configuration reference")
        #expect(body.anchor == nil)
        #expect(body.context.contains("pauseMax"))
    }

    @Test func searchIsCaseInsensitiveAndTrimmed() throws {
        let bundle = try Self.fixture()
        #expect(bundle.search("  QUICK  ").first?.page.title == "Quick start")
        #expect(bundle.search("   ").isEmpty)
        #expect(bundle.search("wireguard").isEmpty)
    }

    /// Within one rank the guided track comes first: a reader who does not know
    /// the vocabulary yet is better served by the tutorial than the reference.
    /// "traffic" is in the body of one of each and in no title, summary, or
    /// heading — so only the tutorial ordering separates them.
    @Test func tiedResultsPutTheTutorialFirst() throws {
        let bundle = try Self.fixture()
        #expect(bundle.search("traffic").map(\.page.title) == ["Quick start", "Configuration reference"])
    }

    @Test func snippetIsOneLineAndBounded() throws {
        let long = String(repeating: "pause ", count: 60)
        let snippet = try #require(HelpBundle.snippet(in: "unrelated\n\(long)\nmore", around: "pause"))
        #expect(snippet.count <= 141)
        #expect(!snippet.contains("\n"))
        #expect(HelpBundle.snippet(in: "nothing here", around: "pause") == nil)
    }
}
