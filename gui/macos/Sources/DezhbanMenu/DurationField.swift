import SwiftUI
import DezhbanCore

/// A duration setting as a slider over the key's real range, with an **Off**
/// detent where that is a real choice and a Custom escape hatch for anything
/// off the ladder.
///
/// It replaces the earlier menu of derived choices (itself a replacement for a
/// bare text field that required knowing Go's duration syntax). Every bound and
/// stop comes from the daemon's schema — the key's own default, its live cap,
/// and whether `"0"` is a persisted opt-out — so nothing here holds an opinion
/// about any particular setting. All the arithmetic lives in
/// DezhbanCore.DurationScale (tested); this view holds none.
///
/// Degrade ladder: schema + usable default → slider; schema but no usable
/// scale → the menu of derived choices; no schema at all → a plain text field.
struct DurationField: View {
    let key: String
    /// Shown when the schema is unavailable: names the concept, states no value.
    let fallbackLabel: String
    let schema: ConfigSchema?
    /// The pane's staged values, used to resolve this key's cap to whatever the
    /// operator has actually set — not to the cap's own default. Lowering a cap
    /// in Developer immediately lowers this slider's top, because the scale is
    /// rebuilt from these on every render (pure math, free at 1 Hz).
    let values: [String: String]
    @Binding var text: String
    let enabled: Bool

    /// True while the user is typing a value by hand.
    @State private var isCustom = false

    private var tunable: ConfigTunable? { schema?[key] }
    private var label: String { tunable?.label ?? fallbackLabel }

    private var scale: DurationScale? {
        guard let tunable else { return nil }
        return DurationScale(defaultValue: tunable.defaultValue,
                             cap: schema?.cap(for: key, in: values),
                             disablable: tunable.disablable)
    }

    private var choices: [DurationChoices.Choice] {
        guard let tunable else { return [] }
        return DurationChoices.build(defaultValue: tunable.defaultValue,
                                     cap: schema?.cap(for: key, in: values),
                                     disablable: tunable.disablable)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text(label)
                Spacer()
                if let scale {
                    slider(scale)
                } else if !choices.isEmpty {
                    menu
                } else {
                    // No schema, or nothing sensible to derive: fall back to the
                    // plain field rather than an empty control.
                    TextField("", text: $text).frame(width: 160)
                }
            }
            if scale != nil {
                HStack {
                    Spacer()
                    valueCaption
                    Button("Custom…") { isCustom = true }
                        .buttonStyle(.borderless)
                        .font(.callout)
                }
            }
            if isCustom || (scale == nil && !choices.isEmpty && offLadder) {
                HStack {
                    Spacer()
                    TextField("e.g. 45s, 2m30s", text: $text)
                        .frame(width: 160)
                    // Immediate, quiet feedback — the old design let a typo
                    // through to a modal alert after Apply had already been
                    // clicked.
                    Image(systemName: valid ? "checkmark.circle" : "exclamationmark.triangle.fill")
                        .foregroundStyle(valid ? Color.secondary : Color.orange)
                        .help(valid ? "A valid duration" : "Not a duration — try 45s, 2m30s, 1h")
                }
            }
            if let consequence {
                Text(consequence)
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .disabled(!enabled)
    }

    /// The slider: position 0…1, snapped through DurationScale. The setter
    /// writes the SNAP's value, so the staged value is always something a
    /// person could have picked; the getter maps whatever the value is (a
    /// custom one included) to its nearest thumb position without rewriting it.
    private func slider(_ scale: DurationScale) -> some View {
        Slider(
            value: Binding(
                get: { scale.position(for: text) },
                set: { pos in
                    text = scale.snapped(at: pos).value
                    isCustom = false
                }
            ),
            in: 0...1
        )
        .frame(width: 200)
        .help(tunable?.help ?? "")
    }

    /// What the slider currently says, beside it in words: "Off", "30 seconds
    /// (recommended)", or the literal text for a hand-typed value off the
    /// ladder — never rewritten under the typist.
    @ViewBuilder
    private var valueCaption: some View {
        if let scale {
            if DurationChoices.isOff(text), scale.hasOff {
                Text("Off").font(.callout).foregroundStyle(.secondary)
            } else if let secs = DurationChoices.seconds(text) {
                let snap = scale.snapped(at: scale.position(for: text))
                if DurationChoices.seconds(snap.value) == secs {
                    Text(snap.isDefault ? "\(snap.label)  (recommended)" : snap.label)
                        .font(.callout).foregroundStyle(.secondary)
                } else {
                    Text("Custom: \(text)").font(.callout).foregroundStyle(.secondary)
                }
            } else if !text.isEmpty {
                Text("Custom: \(text)").font(.callout).foregroundStyle(.orange)
            }
        }
    }

    /// Whether the current value is one the menu would offer (menu fallback only).
    private var offLadder: Bool {
        if DurationChoices.isOff(text) { return false }
        guard let secs = DurationChoices.seconds(text) else { return !text.isEmpty }
        return !choices.contains { DurationChoices.seconds($0.value) == secs }
    }

    private var valid: Bool {
        DurationChoices.isOff(text) || DurationChoices.seconds(text) != nil
    }

    @ViewBuilder
    private var menu: some View {
        Menu(menuTitle) {
            ForEach(choices) { choice in
                Button {
                    text = choice.value
                    isCustom = false
                } label: {
                    Text(choice.isDefault ? "\(choice.label)  (recommended)" : choice.label)
                }
            }
            Divider()
            Button("Custom…") { isCustom = true }
        }
        .frame(width: 200)
        .help(tunable?.help ?? "")
    }

    private var menuTitle: String {
        if DurationChoices.isOff(text), let off = choices.first(where: \.isOff) { return off.label }
        if let secs = DurationChoices.seconds(text),
           let match = choices.first(where: { DurationChoices.seconds($0.value) == secs }) {
            return match.label
        }
        return text.isEmpty ? "Choose…" : "Custom: \(text)"
    }

    /// What this setting costs, stated where the choice is made rather than in a
    /// tooltip nobody hovers. Off is the case worth spelling out: for these keys
    /// it is a real, persisted decision with a real consequence.
    private var consequence: String? {
        guard let tunable else { return nil }
        if DurationChoices.isOff(text) {
            switch key {
            case "vpn.switchWindow":
                return "Off — nothing you type can relax the guard. Adding a new VPN means "
                    + "entering its server address by hand first."
            case "vpn.redialWindow":
                return "Off — a dropped VPN stays cut until it redials on its own. "
                    + "No exposure, but no automatic help either."
            case "vpn.pauseMax":
                return "Off — pausing is unavailable, so there is no way to use your real IP on purpose."
            case "vpn.advanced.redialMinUptime":
                return "Off — no backing off, so every drop gets a full-length window "
                    + "until the redial budget runs out."
            default:
                return "Off."
            }
        }
        return tunable.help
    }
}
