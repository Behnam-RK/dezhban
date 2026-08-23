import CoreGraphics
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

    // MARK: - hoverIsAim

    private let p1 = CGPoint(x: 100, y: 200)
    private let p2 = CGPoint(x: 140, y: 200)

    /// The pointer crossing from one control to another, having moved to do it.
    @Test func movingOntoAnotherControlIsAim() {
        #expect(ActionCaption.hoverIsAim(previousHoverID: "block", id: "pause",
                                         pointer: p2, lastPointer: p1))
    }

    /// The first hover of a session has nothing to compare against and must not be
    /// swallowed.
    @Test func theFirstHoverIsAim() {
        #expect(ActionCaption.hoverIsAim(previousHoverID: nil, id: "block",
                                         pointer: p1, lastPointer: nil))
    }

    /// The same control re-firing. These retitle every second while a window
    /// counts down, and each retitle re-establishes the tracking area.
    @Test func theSameControlReFiringIsNotAim() {
        #expect(!ActionCaption.hoverIsAim(previousHoverID: "cancel-window",
                                          id: "cancel-window",
                                          pointer: p2, lastPointer: p1))
    }

    /// The half the id check cannot see: Pause becomes Cancel when a window opens,
    /// so the enter arrives with a *new* id while the hand has not moved. Read as
    /// aiming, it handed the caption back to the pointer a moment after the keyboard
    /// took it — the failure the id check was added for, through the other door.
    @Test func aControlArrivingUnderAStationaryPointerIsNotAim() {
        #expect(!ActionCaption.hoverIsAim(previousHoverID: nil, id: "cancel-window",
                                          pointer: p1, lastPointer: p1))
        #expect(!ActionCaption.hoverIsAim(previousHoverID: "pause", id: "cancel-window",
                                          pointer: p1, lastPointer: p1))
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
