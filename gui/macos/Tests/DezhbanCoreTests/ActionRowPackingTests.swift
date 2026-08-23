import CoreGraphics
import Testing
@testable import DezhbanCore

struct ActionRowPackingTests {
    /// The Overview's standard-state row: Block / Unblock / Switch VPN… (a Menu,
    /// so + chevron) / Pause / Guard down.
    ///
    /// The widths are the ones measured at macOS 13pt bordered-button metrics
    /// when those controls still carried their long titles ("Block now",
    /// "Pause — use my real IP"). They are deliberately kept: what is under test
    /// is the packing arithmetic and the break points these numbers exercise,
    /// not the current text metrics. Shrinking them to match today's shorter
    /// titles would make every row fit on one line and quietly stop testing the
    /// wrapping this type exists for. Treat them as a fixture, not a measurement.
    private let overview: [CGFloat] = [94, 82, 141, 168, 110]
    private let spacing: CGFloat = 10
    private let gutter: CGFloat = 24

    private func pack(_ widths: [CGFloat], _ available: CGFloat,
                      pinnedFrom: Int) -> [ActionRowPacking.Line] {
        ActionRowPacking.pack(widths: widths, available: available,
                              spacing: spacing, gutter: gutter, pinnedFrom: pinnedFrom)
    }

    /// 94+82+141+168+110 = 595 of controls, three 10pt gaps, one 24pt gutter.
    /// Bound as a CGFloat rather than written inline: `#expect` captures the
    /// expression, and inline literal arithmetic binds as `Int` there.
    private let pinnedNatural: CGFloat = 649
    /// The same controls with no pinning: four 10pt gaps, no gutter.
    private let unpinnedNatural: CGFloat = 635

    @Test func everythingOnOneLineWhenItFits() {
        let lines = pack(overview, 1000, pinnedFrom: 4)
        #expect(lines.count == 1)
        #expect(lines[0].indices == [0, 1, 2, 3, 4])
        #expect(lines[0].width == pinnedNatural)
    }

    /// The gap before the pinned item is the gutter, not the spacing — a
    /// lifecycle action must never read as one of the daily controls.
    @Test func theGutterPrecedesThePinnedItemOnly() {
        let unpinned = pack(overview, 1000, pinnedFrom: overview.count)
        #expect(unpinned.count == 1)
        #expect(unpinned[0].width == unpinnedNatural)
        // Swapping one 10pt spacing for the 24pt gutter is the whole difference.
        #expect(pack(overview, 1000, pinnedFrom: 4)[0].width == unpinnedNatural + 14)
    }

    /// The reported bug's exact geometry: at the old 640pt window minimum the
    /// detail pane offered ~419pt, and the LEADING group alone measures ~515pt,
    /// so it has to wrap too. This is the case a one-row/two-row `ViewThatFits`
    /// could not have expressed.
    @Test func theLeadingGroupItselfWrapsAtTheNarrowestPane() {
        let lines = pack(overview, 419, pinnedFrom: 4)
        #expect(lines.count > 1)
        // No line may exceed the available width...
        for line in lines { #expect(line.width <= 419) }
        // ...and every control must still be placed, exactly once, in order.
        #expect(lines.flatMap(\.indices) == [0, 1, 2, 3, 4])
    }

    @Test func thePinnedItemDropsToItsOwnLineWhenTheLeadingGroupFillsOne() {
        // 94+10+82 = 186 fits; adding the gutter + 110 would need 320.
        let lines = pack([94, 82, 110], 200, pinnedFrom: 2)
        #expect(lines.count == 2)
        #expect(lines[0].indices == [0, 1])
        #expect(lines[1].indices == [2])
    }

    /// An overflowing control is a visible bug; a missing one is a silent bug.
    @Test func anItemWiderThanTheLineGetsItsOwnLineRatherThanVanishing() {
        let lines = pack([94, 400, 82], 200, pinnedFrom: 3)
        #expect(lines.flatMap(\.indices) == [0, 1, 2])
        let oversized = lines.first { $0.indices == [1] }
        #expect(oversized != nil)
        #expect(oversized?.width == 400)
    }

    @Test func emptyInputPacksToNothing() {
        #expect(pack([], 500, pinnedFrom: 0).isEmpty)
        #expect(ActionRowPacking.naturalWidth(widths: [], spacing: spacing,
                                              gutter: gutter, pinnedFrom: 0) == 0)
    }

    @Test func naturalWidthIsTheSumPlusItsGaps() {
        let w = ActionRowPacking.naturalWidth(widths: overview, spacing: spacing,
                                              gutter: gutter, pinnedFrom: 4)
        #expect(w == pinnedNatural)
        // It is by definition the single-line packing, so it must agree.
        #expect(pack(overview, .infinity, pinnedFrom: 4)[0].width == w)
    }

    /// The middle slot is state-dependent and can grow a live countdown
    /// ("Cancel (12:34 left)"), so the break point moves at runtime. Packing
    /// must follow it rather than assume a fixed arity. Same fixture caveat as
    /// `overview` above: the widths exercise the break, they do not measure it.
    @Test func aLongerMiddleSlotMovesTheBreakPoint() {
        let short: [CGFloat] = [94, 82, 141, 110]   // no countdown              → 471 natural
        let long: [CGFloat] = [94, 82, 215, 110]    // with a live countdown     → 545
        // One pane width, two different answers — which is the point: nothing
        // here may be decided from the control COUNT, only from the measurements.
        #expect(pack(short, 500, pinnedFrom: 3).count == 1)
        #expect(pack(long, 500, pinnedFrom: 3).count == 2)
    }
}
