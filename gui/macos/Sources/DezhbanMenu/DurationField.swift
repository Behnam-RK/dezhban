import SwiftUI
import DezhbanCore

/// A duration setting as a menu of real choices, with **Off** where that is a
/// real choice and a Custom entry for anything else.
///
/// It replaces a bare text field that required knowing Go's duration syntax, and
/// whose only feedback was a modal after Apply. Every value it offers comes from
/// the daemon's schema — the key's own default, its live cap, and whether `"0"`
/// is a persisted opt-out — so nothing here holds an opinion about any
/// particular setting.
struct DurationField: View {
    let key: String
    /// Shown when the schema is unavailable: names the concept, states no value.
    let fallbackLabel: String
    let schema: ConfigSchema?
    /// The pane's staged values, used to resolve this key's cap to whatever the
    /// operator has actually set — not to the cap's own default.
    let values: [String: String]
    @Binding var text: String
    let enabled: Bool

    /// True while the user is typing a value that is not one of the choices.
    @State private var isCustom = false

    private var tunable: ConfigTunable? { schema?[key] }
    private var label: String { tunable?.label ?? fallbackLabel }

    private var choices: [DurationChoices.Choice] {
        guard let tunable else { return [] }
        return DurationChoices.build(defaultValue: tunable.defaultValue,
                                     cap: schema?.cap(for: key, in: values),
                                     disablable: tunable.disablable)
    }

    /// The choice the current value corresponds to, or nil when it is custom.
    private var selected: DurationChoices.Choice? {
        if DurationChoices.isOff(text) {
            return choices.first(where: \.isOff)
        }
        guard let secs = DurationChoices.seconds(text) else { return nil }
        return choices.first { DurationChoices.seconds($0.value) == secs }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text(label)
                Spacer()
                if choices.isEmpty {
                    // No schema, or nothing sensible to derive: fall back to the
                    // plain field rather than an empty menu.
                    TextField("", text: $text).frame(width: 160)
                } else {
                    menu
                }
            }
            if isCustom || (selected == nil && !text.isEmpty) {
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
            }
        }
        .disabled(!enabled)
    }

    private var valid: Bool {
        DurationChoices.isOff(text) || DurationChoices.seconds(text) != nil
    }

    @ViewBuilder
    private var menu: some View {
        Menu(selected?.label ?? (text.isEmpty ? "Choose…" : "Custom: \(text)")) {
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
