import Foundation

/// Collapse rules for the Overview's long values: list rows that would wrap
/// forever, and multi-line error text that belongs behind a disclosure. Pure
/// string math, tested — the views apply the results, never compute them.
public enum CollapsedText {
    /// A list row's inline form: the first `limit` items joined, plus how many
    /// were held back ("+N more"). `more == 0` means show the line alone with
    /// no disclosure.
    public static func listSummary(_ items: [String], limit: Int) -> (line: String, more: Int) {
        guard limit > 0, items.count > limit else {
            return (items.joined(separator: ", "), 0)
        }
        let shown = items.prefix(limit).joined(separator: ", ")
        return (shown, items.count - limit)
    }

    /// A long text's first line, cut at `max` characters on a word boundary
    /// where one exists, with an ellipsis when anything was held back.
    /// `truncated` says whether a "Show more" affordance is needed at all.
    public static func firstLine(_ text: String, max: Int) -> (line: String, truncated: Bool) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let first = trimmed.split(separator: "\n", maxSplits: 1, omittingEmptySubsequences: false)
        var line = String(first.first ?? "")
        var truncated = first.count > 1
        if max > 0, line.count > max {
            var cut = String(line.prefix(max))
            if let lastSpace = cut.lastIndex(of: " "), cut.distance(from: cut.startIndex, to: lastSpace) > max / 2 {
                cut = String(cut[..<lastSpace])
            }
            line = cut + "…"
            truncated = true
        }
        return (line, truncated)
    }
}
