import AppKit
import SwiftUI
import DezhbanCore

/// The first-run wizard: the same questions `dezhban setup` asks, rendered
/// natively and answered without a terminal.
///
/// It asks the daemon what to ask (`setup --questions --json`) rather than
/// keeping its own list, so the two wizards cannot drift; it writes through the
/// same batched, validated `config set` every other pane uses, so there is one
/// write path and one validation authority; and it never shells out to the huh
/// wizard, which needs a TTY the app does not have.
struct FirstRunView: View {
    @EnvironmentObject var state: AppState
    /// Called when the sheet should close, with whether anything was written.
    let done: (Bool) -> Void

    @State private var questions: [SetupQuestion] = []
    @State private var answers = SetupAnswers(questions: [])
    @State private var groupIndex = 0
    @State private var busy = false
    @State private var error: String?
    @State private var loaded = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            Divider()
            content
            Divider()
            footer
        }
        .frame(width: 560, height: 520)
        .onAppear(perform: load)
    }

    // MARK: - Chrome

    private var header: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("Set up dezhban").font(.title2.weight(.semibold))
            Text("dezhban blocks everything that isn’t your VPN. These are the same questions `dezhban setup` asks — you can change any of them later in Settings.")
                .font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        }
        .padding(16)
    }

    @ViewBuilder
    private var content: some View {
        if let error {
            guided(symbol: "exclamationmark.triangle", title: "Couldn’t start setup", message: error)
        } else if !loaded {
            ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if questions.isEmpty {
            guided(symbol: "questionmark.circle", title: "Nothing to ask",
                   message: "This copy of the dezhban CLI is too old to describe its setup questions. Run `sudo dezhban setup` in a terminal instead.")
        } else {
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    ForEach(currentQuestions) { q in
                        question(q)
                    }
                }
                .padding(16)
            }
        }
    }

    private var footer: some View {
        HStack {
            if busy { ProgressView().controlSize(.small) }
            Text(stepLabel).font(.callout).foregroundStyle(.secondary)
            Spacer()
            Button("Not now") { done(false) }
                .disabled(busy)
            Button(isLastStep ? "Save and finish" : "Continue") { advance() }
                .keyboardShortcut(.defaultAction)
                .disabled(busy || questions.isEmpty || error != nil || !stepIsValid)
        }
        .padding(16)
    }

    private var stepLabel: String {
        // "Step 1 of 1" is noise on a flow with nothing to step through —
        // which the wizard now often is, after the question shrink.
        guard visibleGroups.count > 1 else { return "" }
        return "Step \(min(groupIndex + 1, visibleGroups.count)) of \(visibleGroups.count)"
    }

    private func guided(symbol: String, title: String, message: String) -> some View {
        VStack(spacing: 12) {
            Image(systemName: symbol).font(.system(size: 36)).foregroundStyle(.secondary)
            Text(title).font(.title3.weight(.semibold))
            Text(message).multilineTextAlignment(.center).foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding(24)
    }

    // MARK: - Questions

    /// The groups that still have something to ask, recomputed as answers
    /// change — a gate closing removes a whole step rather than showing an
    /// empty one.
    private var visibleGroups: [Int] {
        var seen = Set<Int>()
        var out: [Int] = []
        for q in questions where answers.shouldAsk(q) {
            if seen.insert(q.group).inserted { out.append(q.group) }
        }
        return out.sorted()
    }

    private var currentQuestions: [SetupQuestion] {
        guard groupIndex < visibleGroups.count else { return [] }
        let group = visibleGroups[groupIndex]
        return questions.filter { $0.group == group && answers.shouldAsk($0) }
    }

    private var isLastStep: Bool { groupIndex >= visibleGroups.count - 1 }

    @ViewBuilder
    private func question(_ q: SetupQuestion) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            switch q.kind {
            case SetupKind.bool:
                Toggle(q.title, isOn: boolBinding(q.id))
            case SetupKind.select:
                Picker(q.title, selection: stringBinding(q.id)) {
                    ForEach(q.options) { Text($0.label).tag($0.value) }
                }
            case SetupKind.multiSelect:
                Text(q.title).font(.body.weight(.medium))
                ForEach(q.options) { opt in
                    Toggle(opt.label, isOn: memberBinding(q.id, opt.value))
                        .toggleStyle(.checkbox)
                }
            default:
                Text(q.title).font(.body.weight(.medium))
                TextField(q.id == "profileFiles" ? "Optional" : "", text: stringBinding(q.id))
                if q.id == "profileFiles" {
                    Button("Choose files…") { chooseProfileFiles() }
                        .controlSize(.small)
                }
            }
            if !q.description.isEmpty {
                Text(q.description).font(.callout).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if q.kind == SetupKind.duration, !DurationText.looksLikeGoDuration(answers[q.id]) {
                Text("Enter a duration like 30s or 5m.").font(.callout).foregroundStyle(.red)
            }
        }
    }

    private func stringBinding(_ id: String) -> Binding<String> {
        Binding(get: { answers[id] }, set: { answers[id] = $0 })
    }

    private func boolBinding(_ id: String) -> Binding<Bool> {
        Binding(get: { answers.bool(id) }, set: { answers[id] = $0 ? "true" : "false" })
    }

    /// A checkbox over one member of a comma-separated list answer.
    private func memberBinding(_ id: String, _ value: String) -> Binding<Bool> {
        Binding(
            get: { answers.list(id).contains(value) },
            set: { on in
                var list = answers.list(id)
                if on {
                    if !list.contains(value) { list.append(value) }
                } else {
                    list.removeAll { $0 == value }
                }
                answers[id] = list.joined(separator: ",")
            })
    }

    private func chooseProfileFiles() {
        let panel = NSOpenPanel()
        panel.allowsMultipleSelection = true
        panel.canChooseDirectories = false
        panel.message = "Choose WireGuard (.conf), OpenVPN (.ovpn), or V2Ray (.json) files to import as profiles."
        guard panel.runModal() == .OK else { return }
        let paths = panel.urls.map(\.path)
        answers["profileFiles"] = paths.joined(separator: ",")
    }

    // MARK: - Flow

    private func load() {
        guard !loaded else { return }
        DispatchQueue.global(qos: .userInitiated).async {
            let qs = DezhbanCLI.readSetupQuestions()
            DispatchQueue.main.async {
                loaded = true
                guard let qs else {
                    error = DezhbanCLI.binaryPath() == nil
                        ? "The dezhban command-line tool isn’t installed, so there is nothing to configure yet."
                        : "`dezhban setup --questions` failed. Run `sudo dezhban setup` in a terminal instead."
                    return
                }
                questions = qs
                answers = SetupAnswers(questions: qs)
            }
        }
    }

    /// True when every duration on this step parses. A bad value blocks the
    /// step it was typed on, while the field saying why is still on screen —
    /// rather than surfacing as a modal after the last question.
    private var stepIsValid: Bool {
        !currentQuestions.contains {
            $0.kind == SetupKind.duration && !DurationText.looksLikeGoDuration(answers[$0.id])
        }
    }

    private func advance() {
        guard stepIsValid else { return }
        guard isLastStep else {
            groupIndex += 1
            return
        }
        save()
    }

    /// Writes every answer through ONE batched `config set` — the same
    /// validated, token-gated path the Settings pane uses, so the wizard has no
    /// write logic of its own and cannot bypass `config.Validate`.
    ///
    /// Named VPN config files are imported afterwards, because a profile is not
    /// a config key: `vpn import` parses the file. That step is privileged and
    /// asks separately; the config is already saved by then, so cancelling the
    /// prompt loses the import, not the setup.
    private func save() {
        let pairs = answers.configPairs(for: questions)
        guard !pairs.isEmpty else {
            done(false)
            return
        }
        busy = true
        ConfigApply.apply(pairs: pairs, awaitPosture: false, title: "Setup") { outcome in
            busy = false
            guard outcome.ok else {
                error = outcome.status
                if let transcript = outcome.transcript, let title = outcome.transcriptTitle {
                    state.showInLogs(title: title, text: transcript)
                }
                return
            }
            let files = answers.profileFiles(for: questions)
            FirstRun.markComplete()
            guard !files.isEmpty else {
                done(true)
                return
            }
            AppActions.capturedSequence(files.map { ["vpn", "import", $0] }) { result in
                if !result.ok {
                    state.showInLogs(title: "Setup — profile import failed", text: result.output)
                }
                done(true)
            }
        }
    }
}

/// Whether the first-run wizard has been offered on this Mac.
///
/// A flag in UserDefaults, not in the config: the config belongs to the daemon
/// and is shared by every surface, while "has this user seen the wizard" is a
/// fact about this app on this account. Marked complete only after a successful
/// write, so a cancelled or failed setup is offered again.
enum FirstRun {
    private static let key = "dezhban.firstRunCompleted"

    static var isComplete: Bool { UserDefaults.standard.bool(forKey: key) }

    static func markComplete() { UserDefaults.standard.set(true, forKey: key) }

    /// Offer the wizard when it has never been completed AND dezhban does not
    /// know a VPN server yet. The rule lives in DezhbanCore so it is testable;
    /// this only supplies the flag.
    static func shouldOffer(vpnKnown: Bool) -> Bool {
        FirstRunDecision.offer(isComplete: isComplete, vpnKnown: vpnKnown)
    }
}
