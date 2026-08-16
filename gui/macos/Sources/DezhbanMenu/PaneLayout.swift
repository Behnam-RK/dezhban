import SwiftUI
import DezhbanCore

/// The window's shared layout numbers. Every pane used to spell its own out;
/// the ones that appeared more than once are named here so two panes cannot
/// quietly drift apart. (420 was already hardcoded identically in OverviewView,
/// DiagnosticsView and HelpView; 12 in every footer.)
enum PaneMetrics {
    /// Centered prose column for guided / empty states.
    static let proseColumn: CGFloat = 420

    /// Ceiling on a pane's leading-aligned content column.
    ///
    /// Derived, not picked: the Overview's fullest action row is ~515pt of
    /// daily controls + a 24pt gutter + ~110pt of "Guard down" = ~649pt, plus
    /// 20pt of padding either side = ~689pt. 720 is the next round number that
    /// clears it, so the row collapses to one line exactly when the window is
    /// wide enough to deserve one, and never grows past it. 680pt of usable
    /// text is also about 90 characters at 13pt — inside the readable measure,
    /// which is what stops a Divider or a paragraph running the width of a 5K
    /// display.
    static let contentColumn: CGFloat = 720

    /// Ceiling on a status line that shares a row with controls. `ActionRow`
    /// places subviews at their IDEAL size, and a status line's ideal width is
    /// however long the message happens to be; unbounded, one long message
    /// would shove every button onto the next line.
    static let statusColumn: CGFloat = 220

    /// Gap between neighbouring controls in an action row.
    static let controlSpacing: CGFloat = 10

    /// Minimum clear space between a row's daily controls and its trailing
    /// lifecycle action, so the latter never reads as one of them.
    static let actionGutter: CGFloat = 24

    static let panePadding: CGFloat = 20
    static let footerPadding: CGFloat = 12
}

/// A control row that wraps onto as many lines as it needs, keeping the last
/// `trailingCount` controls flush against the trailing edge of whichever line
/// they land on.
///
/// Replaces `HStack { …controls; Spacer(); lifecycleButton }`, which failed at
/// both ends of the width range at once. Narrow: an HStack given less than its
/// children's ideal sum distributes the shortfall across all of them, so every
/// label truncated simultaneously ("Block n…", "Switchin…", "Guard…") rather
/// than the row admitting it did not fit. Wide: the Spacer claimed all surplus,
/// opening a window-wide void beside the trailing button.
///
/// A `Layout` rather than `ViewThatFits` with hand-authored candidates: at the
/// old 640pt window minimum the Overview's daily controls alone were ~515pt
/// against ~419pt of usable pane, so the LEADING group has to wrap too — that
/// is a third candidate, and the arity changes with the switch-state slot. A
/// packer that measures is right at every width; a fixed list of arrangements
/// is only right at the widths someone thought of.
///
/// It also removes any need for `.fixedSize()` at the call sites: a Layout asks
/// each subview for its ideal size and places it at exactly that size, so
/// nothing is ever handed a proposal it can only meet by truncating. That
/// matters most for the `Menu`, whose `.fixedSize()` behaviour on macOS 13 is
/// the least dependable of the controls involved.
struct ActionRow: Layout {
    var spacing: CGFloat = PaneMetrics.controlSpacing
    var gutter: CGFloat = PaneMetrics.actionGutter
    /// How many trailing subviews form the pinned group. 0 = no pinning.
    var trailingCount: Int = 1

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let sizes = subviews.map { $0.sizeThatFits(.unspecified) }
        let widths = sizes.map(\.width)
        let from = pinnedFrom(subviews.count)
        let natural = ActionRowPacking.naturalWidth(
            widths: widths, spacing: spacing, gutter: gutter, pinnedFrom: from)
        // nil (an ideal-size query) and .infinity both mean "unconstrained":
        // lay out on one line and report that, rather than claiming infinity.
        let available = proposal.width.flatMap { $0.isFinite ? $0 : nil } ?? natural
        let lines = ActionRowPacking.pack(
            widths: widths, available: available,
            spacing: spacing, gutter: gutter, pinnedFrom: from)

        let widest = lines.map(\.width).max() ?? 0
        // With a pinned group the row must occupy the full offered width —
        // that trailing edge is what the group is pinned to.
        let width = trailingCount > 0 ? max(available, widest) : widest
        let height = lines.enumerated().reduce(CGFloat.zero) { acc, e in
            acc + (e.offset > 0 ? spacing : 0)
                + (e.element.indices.map { sizes[$0].height }.max() ?? 0)
        }
        return CGSize(width: width, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize,
                       subviews: Subviews, cache: inout ()) {
        let sizes = subviews.map { $0.sizeThatFits(.unspecified) }
        let widths = sizes.map(\.width)
        let from = pinnedFrom(subviews.count)
        let lines = ActionRowPacking.pack(
            widths: widths, available: bounds.width,
            spacing: spacing, gutter: gutter, pinnedFrom: from)

        var y = bounds.minY
        for line in lines {
            let rowHeight = line.indices.map { sizes[$0].height }.max() ?? 0
            let midY = y + rowHeight / 2

            var x = bounds.minX
            for i in line.indices where i < from {
                subviews[i].place(at: CGPoint(x: x, y: midY),
                                  anchor: .leading,
                                  proposal: ProposedViewSize(sizes[i]))
                x += widths[i] + spacing
            }
            // Pinned run, laid out backwards from the trailing edge — so a
            // wrapped lifecycle action still reads as trailing, not as the
            // start of a new group of daily controls.
            var right = bounds.maxX
            for i in line.indices.filter({ $0 >= from }).reversed() {
                subviews[i].place(at: CGPoint(x: right, y: midY),
                                  anchor: .trailing,
                                  proposal: ProposedViewSize(sizes[i]))
                right -= widths[i] + spacing
            }
            y += rowHeight + spacing
        }
    }

    private func pinnedFrom(_ count: Int) -> Int {
        trailingCount <= 0 ? count : max(0, count - trailingCount)
    }
}
