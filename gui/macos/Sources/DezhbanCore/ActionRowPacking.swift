import CoreGraphics

/// Pure row-packing arithmetic for the window's action rows: given each
/// control's natural width and how much room there is, decide which controls
/// share a line.
///
/// Lives here rather than beside the SwiftUI `Layout` that consumes it for the
/// same reason `PostureUI` does: it is the part with a right and a wrong
/// answer, and an `.executableTarget` cannot be `@testable import`ed.
///
/// The defect this exists to remove: an `HStack` given less width than its
/// children's ideal sum distributes the shortfall across every flexible child,
/// so a row of bordered buttons truncated *all* its labels at once ("Block n…",
/// "Switchin…", "Guard…") rather than admitting it did not fit.
public enum ActionRowPacking {
    /// One laid-out line of a packed row.
    public struct Line: Equatable {
        /// Indices into the original width array, in order.
        public let indices: [Int]
        /// Natural width of this line's items plus the gaps between them.
        ///
        /// Not the DRAWN width — a line carrying pinned items is stretched to
        /// the container's width so the pinned run can meet its trailing edge.
        public let width: CGFloat

        public init(indices: [Int], width: CGFloat) {
            self.indices = indices
            self.width = width
        }
    }

    /// Packs `widths` into lines no wider than `available`.
    ///
    /// Items at index >= `pinnedFrom` are the trailing group (the lifecycle
    /// action — "Guard down", "Apply…"). The gap in front of the FIRST of them
    /// is `gutter` rather than `spacing`, so that group can never sit flush
    /// against the daily controls and read as one of them.
    ///
    /// An item wider than `available` gets a line to itself and overflows it
    /// rather than being dropped: an overflowing control is a visible bug, a
    /// missing one is a silent bug.
    ///
    /// Pass `pinnedFrom: widths.count` for no pinning.
    public static func pack(widths: [CGFloat],
                            available: CGFloat,
                            spacing: CGFloat,
                            gutter: CGFloat,
                            pinnedFrom: Int) -> [Line] {
        guard !widths.isEmpty else { return [] }
        var lines: [Line] = []
        var current: [Int] = []
        var used: CGFloat = 0

        for i in widths.indices {
            let w = widths[i]
            let gap: CGFloat = current.isEmpty ? 0 : (i == pinnedFrom ? gutter : spacing)
            if !current.isEmpty && used + gap + w > available {
                lines.append(Line(indices: current, width: used))
                current = [i]
                used = w
            } else {
                current.append(i)
                used += gap + w
            }
        }
        if !current.isEmpty { lines.append(Line(indices: current, width: used)) }
        return lines
    }

    /// The width the row wants when nothing constrains it: everything on one
    /// line. Used to answer an unspecified or infinite size proposal, so the
    /// row reports a real number instead of claiming infinite width.
    public static func naturalWidth(widths: [CGFloat],
                                    spacing: CGFloat,
                                    gutter: CGFloat,
                                    pinnedFrom: Int) -> CGFloat {
        pack(widths: widths, available: .infinity,
             spacing: spacing, gutter: gutter, pinnedFrom: pinnedFrom).first?.width ?? 0
    }
}
