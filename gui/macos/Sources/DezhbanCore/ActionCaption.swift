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
    /// The caption to show.
    ///
    /// `hovered` is the hint of the control the pointer is over, `focused` the
    /// hint of the keyboard-focused control. Hover wins when both are present:
    /// the pointer is the thing the user is currently aiming, and focus often
    /// lingers on whatever was last clicked.
    ///
    /// With neither, the posture headline stands in. Never empty and never a
    /// placeholder like "—": the line must not collapse and reflow the row every
    /// time the pointer crosses a gap between two buttons.
    public static func text(hovered: String?, focused: String?, fallback: String) -> String {
        for candidate in [hovered, focused] {
            if let c = candidate, !c.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                return c
            }
        }
        return fallback
    }
}
