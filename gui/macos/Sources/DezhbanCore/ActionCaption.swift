import CoreGraphics
import Foundation

/// What the single description line under the action row says.
///
/// The row's buttons carry one- or two-word titles ("Pause", "Panic", "Guard
/// down"), so the sentence that used to live inside the title — "Pause — use my
/// real IP" — has to live somewhere the reader sees it *before* they click, not
/// only after a tooltip delay. That somewhere is one caption line beneath the
/// row, showing whatever the pointer or keyboard focus is on.
///
/// Split out of the view for the same reason `PostureUI` and
/// `ActionRowPacking` are: the fallback rule has a right and a wrong answer, and
/// getting it wrong means a kill switch whose most dangerous button explains
/// itself to nobody.
public enum ActionCaption {
    /// Which input the user reached for most recently.
    public enum Aim: Equatable {
        case pointer
        case keyboard
    }

    /// The caption to show.
    ///
    /// `hovered` is the hint of the control the pointer is over, `focused` the hint
    /// of the keyboard-focused control, and `aim` says which of the two the user
    /// touched last. Most-recent-interaction wins, which is the only rule that
    /// satisfies both directions: a pointer parked on one button must not outrank the
    /// keyboard indefinitely (tabbing moved the focus ring and the Space key while
    /// the caption described something else), and tabbing must not erase the fact
    /// that the pointer is still resting somewhere.
    ///
    /// Ranking rather than clearing, deliberately. The caller used to answer this by
    /// setting `hovered` to nil when focus moved, which threw the pointer's position
    /// away instead of deprioritising it: tab into the row and back out to anything
    /// outside it and *both* were empty, so the caption fell to the prompt while the
    /// pointer sat on Block, until it moved off and back on.
    ///
    /// With neither, `fallback` stands in — a prompt, in the app's case, not the
    /// posture headline: the hero already renders that string in title2 just above,
    /// so echoing it here said the same thing twice. Never empty and never a
    /// placeholder like "—": the line must not collapse and reflow the row every
    /// time the pointer crosses a gap between two buttons.
    public static func text(hovered: String?, focused: String?,
                            aim: Aim, fallback: String) -> String {
        let order = aim == .keyboard ? [focused, hovered] : [hovered, focused]
        for candidate in order {
            if let c = candidate, !c.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                return c
            }
        }
        return fallback
    }

    /// Whether a hover-enter is the user aiming at something, or a control arriving
    /// under a pointer that never moved.
    ///
    /// `onHover(true)` cannot tell the two apart on its own: it fires both when the
    /// pointer crosses into a control and when a tracking area is established
    /// beneath a stationary one. Two guards, and they cover different halves:
    ///
    ///   - `previousHoverID != id` rejects the *same* control re-firing, which these
    ///     controls do constantly — a window's countdown retitles "Cancel (m:ss
    ///     left)" every second.
    ///   - the pointer comparison rejects a *different* control arriving underneath,
    ///     which the first guard cannot see. Pause becomes Cancel when a window
    ///     opens, and the replacement's enter arrives with a new id while the user's
    ///     hand has not moved — handing the caption back to the pointer a moment
    ///     after the keyboard took it, the very failure the first guard was added
    ///     for, through the other door.
    ///
    /// `pointer` must be in SCREEN coordinates (`NSEvent.mouseLocation`), which is
    /// what makes this different from the local-space comparison that was tried and
    /// removed: a banner appearing above the row moves a control under a stationary
    /// mouse, so the pointer's position *within* that control changes while the
    /// mouse itself has not. On screen it has not changed, which is the question
    /// being asked.
    ///
    /// `keyboardAimedAt` is where the pointer was when the keyboard last took the
    /// caption, and nil when it has not. That is the reference point, NOT the
    /// pointer's position at the previous hover event, which was the first shape of
    /// this and was wrong in both directions. `onHover` fires on enter and exit
    /// only, so a reading taken there is the position at the last boundary crossing:
    /// a hand that drifted a few points while reading (no hover event) then made a
    /// swap look like movement, and a fast flick across the gap between two buttons
    /// — whose exit and enter are dispatched from a single mouse-moved event, so
    /// both sample the same location — looked like stillness and silently refused to
    /// take the caption back. Anchored to the moment the keyboard took over, the
    /// question is the one actually being asked: has the hand gone anywhere since?
    public static func hoverIsAim(previousHoverID: String?, id: String,
                                  pointer: CGPoint, keyboardAimedAt: CGPoint?) -> Bool {
        guard previousHoverID != id else { return false }
        guard let keyboardAimedAt else { return true }
        return pointer != keyboardAimedAt
    }
}
