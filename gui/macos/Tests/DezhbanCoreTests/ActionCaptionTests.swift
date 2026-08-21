import Testing
@testable import DezhbanCore

struct ActionCaptionTests {
    @Test func hoverWins() {
        #expect(ActionCaption.text(hovered: "cut everything",
                                   focused: "release the line",
                                   fallback: "guard is up") == "cut everything")
    }

    @Test func focusIsUsedWhenNothingIsHovered() {
        #expect(ActionCaption.text(hovered: nil,
                                   focused: "release the line",
                                   fallback: "guard is up") == "release the line")
    }

    /// The line must never go blank between two buttons: an empty caption
    /// collapses its own height and reflows the row under the pointer.
    @Test func fallbackFillsEveryGap() {
        #expect(ActionCaption.text(hovered: nil, focused: nil,
                                   fallback: "guard is up") == "guard is up")
        #expect(ActionCaption.text(hovered: "", focused: "  ",
                                   fallback: "guard is up") == "guard is up")
    }
}
