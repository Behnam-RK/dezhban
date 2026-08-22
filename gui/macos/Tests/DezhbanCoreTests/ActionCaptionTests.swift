import Testing
@testable import DezhbanCore

struct ActionCaptionTests {
    @Test func thePointerWinsWhileItIsWhatTheUserLastUsed() {
        #expect(ActionCaption.text(hovered: "cut everything",
                                   focused: "release the line",
                                   aim: .pointer,
                                   fallback: "point at a button") == "cut everything")
    }

    /// Tabbing has to take the caption from a parked pointer, or the focus ring and
    /// the Space key end up on one button while the caption describes another.
    @Test func theKeyboardWinsAfterTabbing() {
        #expect(ActionCaption.text(hovered: "cut everything",
                                   focused: "release the line",
                                   aim: .keyboard,
                                   fallback: "point at a button") == "release the line")
    }

    /// Ranking, not erasing. Tab into the row and back out of it — focus is gone, the
    /// pointer has not moved — and the caption must still describe what it is on.
    /// The caller used to clear the hover state instead, which left this case showing
    /// the resting prompt over a control the pointer was physically resting on.
    @Test func aParkedPointerSurvivesFocusLeavingTheRow() {
        #expect(ActionCaption.text(hovered: "cut everything",
                                   focused: nil,
                                   aim: .keyboard,
                                   fallback: "point at a button") == "cut everything")
    }

    @Test func focusIsUsedWhenNothingIsHovered() {
        #expect(ActionCaption.text(hovered: nil,
                                   focused: "release the line",
                                   aim: .pointer,
                                   fallback: "point at a button") == "release the line")
    }

    /// The line must never go blank between two buttons: an empty caption
    /// collapses its own height and reflows the row under the pointer.
    @Test func fallbackFillsEveryGap() {
        for aim: ActionCaption.Aim in [.pointer, .keyboard] {
            #expect(ActionCaption.text(hovered: nil, focused: nil,
                                       aim: aim, fallback: "point at a button")
                == "point at a button")
            #expect(ActionCaption.text(hovered: "", focused: "  ",
                                       aim: aim, fallback: "point at a button")
                == "point at a button")
        }
    }
}
