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
}
