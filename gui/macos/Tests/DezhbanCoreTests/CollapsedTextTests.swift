import Foundation
import Testing
@testable import DezhbanCore

struct CollapsedTextTests {
    @Test func shortListStaysInline() {
        let (line, more) = CollapsedText.listSummary(["a", "b"], limit: 3)
        #expect(line == "a, b")
        #expect(more == 0)
    }

    @Test func longListCollapses() {
        let (line, more) = CollapsedText.listSummary(["a", "b", "c", "d", "e"], limit: 3)
        #expect(line == "a, b, c")
        #expect(more == 2)
    }

    @Test func exactLimitIsNotCollapsed() {
        let (_, more) = CollapsedText.listSummary(["a", "b", "c"], limit: 3)
        #expect(more == 0)
    }

    @Test func singleShortLineIsNotTruncated() {
        let (line, truncated) = CollapsedText.firstLine("all fine", max: 120)
        #expect(line == "all fine")
        #expect(!truncated)
    }

    @Test func multiLineTextTruncatesToItsFirstLine() {
        let (line, truncated) = CollapsedText.firstLine("apply failed\npfctl: whole traceback\nmore", max: 120)
        #expect(line == "apply failed")
        #expect(truncated)
    }

    @Test func longSingleLineCutsOnAWordBoundary() {
        let text = "one very long single line of failure prose that keeps going and going"
        let (line, truncated) = CollapsedText.firstLine(text, max: 30)
        #expect(truncated)
        #expect(line.hasSuffix("…"))
        #expect(line.count <= 31)
        // Word boundary, not mid-word.
        #expect(!line.dropLast().hasSuffix(" "))
    }
}
