import Foundation

/// Mirrors Go's `help.Heading` — one heading in a rendered page.
public struct HelpHeading: Codable, Hashable {
    public let level: Int
    public let text: String
    public let anchor: String

    public init(level: Int, text: String, anchor: String) {
        self.level = level
        self.text = text
        self.anchor = anchor
    }
}

/// Mirrors Go's `help.IndexEntry` — one bundled page, as written to
/// `help-index.json` by `tools/helpgen` at build time.
public struct HelpPage: Codable, Identifiable, Hashable {
    /// The docs-relative source path is the identity, because that is what a
    /// `Tunable.DocAnchor` names ("usage/config.md#fields").
    public var id: String { source }
    public let file: String
    public let source: String
    public let title: String
    public let summary: String
    /// Position in the first-time reading order, 1-based; 0 means reference
    /// material — reachable and searchable, but not part of the guided track.
    public let tutorial: Int
    public let headings: [HelpHeading]
    /// The page stripped to words, so search runs in the app with no second
    /// pass over the HTML.
    public let text: String

    private enum CodingKeys: String, CodingKey {
        case file, source, title, summary, tutorial, headings, text
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        file = try c.decode(String.self, forKey: .file)
        source = try c.decode(String.self, forKey: .source)
        title = try c.decode(String.self, forKey: .title)
        summary = try c.decodeIfPresent(String.self, forKey: .summary) ?? ""
        // Go omits `tutorial` when it is zero (`omitempty`), which is the
        // common case: most pages are reference, not tutorial.
        tutorial = try c.decodeIfPresent(Int.self, forKey: .tutorial) ?? 0
        headings = try c.decodeIfPresent([HelpHeading].self, forKey: .headings) ?? []
        text = try c.decodeIfPresent(String.self, forKey: .text) ?? ""
    }

    public init(file: String, source: String, title: String, summary: String,
                tutorial: Int = 0, headings: [HelpHeading] = [], text: String = "") {
        self.file = file
        self.source = source
        self.title = title
        self.summary = summary
        self.tutorial = tutorial
        self.headings = headings
        self.text = text
    }

    /// True when this page has a heading with that fragment id — what a
    /// contextual deep link depends on.
    public func hasAnchor(_ anchor: String) -> Bool {
        headings.contains { $0.anchor == anchor }
    }
}

/// One search result: the page, optionally a heading to land on, and a line
/// saying why it matched.
public struct HelpHit: Identifiable, Hashable {
    public var id: String { page.source + "#" + (anchor ?? "") }
    public let page: HelpPage
    public let anchor: String?
    public let context: String

    public init(page: HelpPage, anchor: String?, context: String) {
        self.page = page
        self.anchor = anchor
        self.context = context
    }
}

/// Where a deep link points: a page, and optionally a heading within it.
/// Written as `"usage/config.md#fields"` in a `Tunable`'s `docAnchor`.
public struct HelpTarget: Hashable, Identifiable {
    public var id: String { source + "#" + (anchor ?? "") }
    public let source: String
    public let anchor: String?

    public init(source: String, anchor: String? = nil) {
        self.source = source
        self.anchor = anchor
    }

    /// Parses the `page#fragment` form a `Tunable.docAnchor` is written in.
    public init?(docAnchor: String) {
        let parts = docAnchor.split(separator: "#", maxSplits: 1, omittingEmptySubsequences: false)
        guard let first = parts.first, !first.isEmpty else { return nil }
        source = String(first)
        anchor = parts.count > 1 && !parts[1].isEmpty ? String(parts[1]) : nil
    }
}

/// The documentation shipped inside the app: the rendered pages on disk plus
/// the index the sidebar and search are built from.
///
/// It is deliberately read from the bundle rather than the network. The pane is
/// opened when someone needs to know what the guard is doing to their traffic,
/// which is often while it has cut all of it.
public struct HelpBundle {
    /// The directory holding the HTML, help.css, and help-index.json.
    public let directory: URL
    public let pages: [HelpPage]

    public init(directory: URL, pages: [HelpPage]) {
        self.directory = directory
        self.pages = pages
    }

    /// Loads a bundle from a directory containing help-index.json.
    public static func load(directory: URL) -> HelpBundle? {
        guard let data = try? Data(contentsOf: directory.appendingPathComponent("help-index.json")),
              let pages = try? JSONDecoder().decode([HelpPage].self, from: data),
              !pages.isEmpty
        else { return nil }
        return HelpBundle(directory: directory, pages: pages)
    }

    /// Loads the copy inside Dezhban.app (`Contents/Resources/help`). Returns
    /// nil when running from `swift run` outside a bundle, or against an app
    /// built before the docs were bundled — callers show a short explanation
    /// rather than an empty pane.
    public static func bundled() -> HelpBundle? {
        guard let resources = Bundle.main.resourceURL else { return nil }
        return load(directory: resources.appendingPathComponent("help", isDirectory: true))
    }

    /// The guided reading order, first.
    public var tutorial: [HelpPage] {
        pages.filter { $0.tutorial > 0 }.sorted { $0.tutorial < $1.tutorial }
    }

    /// Everything else, in manifest order — reference material a reader looks
    /// something up in rather than reads through.
    public var reference: [HelpPage] {
        pages.filter { $0.tutorial == 0 }
    }

    public func page(source: String) -> HelpPage? {
        pages.first { $0.source == source }
    }

    /// The on-disk file for a page. `WKWebView.loadFileURL` needs a real URL,
    /// and this is the only place one is constructed.
    public func url(for page: HelpPage) -> URL {
        directory.appendingPathComponent(page.file)
    }

    /// Resolves a deep link. Reports the anchor only when a heading actually
    /// carries it — a stale anchor lands the reader at the top of the right
    /// page instead of nowhere.
    public func resolve(_ target: HelpTarget) -> (page: HelpPage, anchor: String?)? {
        guard let page = page(source: target.source) else { return nil }
        if let anchor = target.anchor, page.hasAnchor(anchor) {
            return (page, anchor)
        }
        return (page, nil)
    }

    /// Substring search over titles, summaries, headings, and body text, in
    /// that order of confidence. Case-insensitive, no stemming: the corpus is
    /// nine pages, and a reader typing "pause" wants the pause section, not a
    /// ranked relevance model.
    public func search(_ query: String) -> [HelpHit] {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard !q.isEmpty else { return [] }

        var hits: [HelpHit] = []
        var ranks: [String: Int] = [:]

        for page in pages {
            if page.title.lowercased().contains(q) {
                hits.append(HelpHit(page: page, anchor: nil, context: page.summary))
                ranks[page.source + "#"] = 0
                continue
            }
            if page.summary.lowercased().contains(q) {
                hits.append(HelpHit(page: page, anchor: nil, context: page.summary))
                ranks[page.source + "#"] = 1
                continue
            }
            if let heading = page.headings.first(where: { $0.text.lowercased().contains(q) }) {
                let hit = HelpHit(page: page, anchor: heading.anchor, context: heading.text)
                hits.append(hit)
                ranks[hit.id] = 2
                continue
            }
            if let snippet = Self.snippet(in: page.text, around: q) {
                hits.append(HelpHit(page: page, anchor: nil, context: snippet))
                ranks[page.source + "#"] = 3
            }
        }

        return hits.sorted { a, b in
            let ra = ranks[a.id] ?? 9, rb = ranks[b.id] ?? 9
            if ra != rb { return ra < rb }
            // Within a rank, the guided track comes first: a reader who does
            // not know the vocabulary yet is better served by "How it works"
            // than by the configuration reference.
            let ta = a.page.tutorial == 0 ? Int.max : a.page.tutorial
            let tb = b.page.tutorial == 0 ? Int.max : b.page.tutorial
            if ta != tb { return ta < tb }
            return a.page.title < b.page.title
        }
    }

    /// One line of body text around the match, so a result says what it found
    /// rather than just where.
    static func snippet(in text: String, around query: String) -> String? {
        for line in text.split(separator: "\n") {
            if line.lowercased().contains(query) {
                let s = line.trimmingCharacters(in: .whitespaces)
                return s.count > 140 ? String(s.prefix(140)) + "…" : s
            }
        }
        return nil
    }
}
