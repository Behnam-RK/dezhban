import SwiftUI
import DezhbanCore

/// The Diagnostics pane: `dezhban doctor --json`'s structured findings,
/// rendered natively instead of dumped as a text transcript (which the Logs
/// pane used to do, and still does for panic/install/config-apply output —
/// this is the one pane that renders `doctor` specifically). Read-only, no
/// root, no firewall effects — identical guarantee to running `dezhban doctor`
/// in a terminal.
struct DiagnosticsView: View {
    @EnvironmentObject var state: AppState
    @State private var discover = false
    @State private var exporting = false

    var body: some View {
        VStack(spacing: 0) {
            toolbar
            Divider()
            content
        }
        .onAppear {
            // The report and the inventory live on AppState (they feed the
            // sidebar badge and survive navigation); this pane only asks for a
            // refresh when what is there has gone stale.
            state.runDoctorIfStale(maxAge: 15 * 60)
            state.refreshVPNInventoryIfStale()
            state.refreshAppliedRules()
            state.refreshProblems()
        }
    }

    private var toolbar: some View {
        HStack(spacing: 10) {
            Button("Run diagnostics") { run() }
                .disabled(state.doctorRunning || !state.cliFound)
            Toggle("Find my VPN's server", isOn: $discover)
                .toggleStyle(.checkbox)
                .help("macOS-only best-effort hunt for the connected VPN's real server IP (`--discover`).")
            Button("Export…") { exportReport() }
                .disabled(exporting || !state.cliFound)
                .help("Save everything on this pane — plus your config, dezhban's state and its recent log — "
                    + "to one zip you can attach to a bug report. Nothing is sent anywhere.")
            Spacer()
            if state.doctorRunning {
                ProgressView().controlSize(.small)
            }
        }
        .padding(PaneMetrics.footerPadding)
    }

    /// One refresh in a person's head: the button re-runs doctor AND re-reads
    /// the VPN inventory (cheap next to doctor itself).
    private func run() {
        state.runDoctor(discover: discover)
        state.refreshVPNInventoryIfStale(maxAge: 0)
        state.refreshAppliedRules()
        state.refreshProblems()
    }

    @ViewBuilder
    private var content: some View {
        // The VPN inventory and the doctor report are fetched independently and
        // arrive independently, so the List renders as soon as EITHER of them
        // has something. Gating the whole List on the report kept a
        // successfully-fetched inventory invisible: on first open, until the
        // async doctor run returned; after a doctor failure with nothing
        // retained; and permanently on a host where `doctor --json` cannot run
        // at all. refreshVPNInventoryIfStale fetched it and nothing showed it.
        if state.doctorReport != nil || state.vpnInventory != nil {
            List {
                if let error = state.doctorError {
                    Section {
                        // A retained report OUTRANKS a failed run: AppState
                        // deliberately keeps the last one so navigating away
                        // doesn't discard something someone just read. Which
                        // sentence is true therefore depends on whether there
                        // is one to show — the banner must not promise a "last
                        // result" that is not on screen.
                        Label(state.doctorReport == nil
                                ? "Couldn't run diagnostics. \(error)"
                                : "Couldn't re-run diagnostics — showing the last result. \(error)",
                              systemImage: "exclamationmark.triangle.fill")
                            .font(.callout)
                            .foregroundStyle(.orange)
                            .textSelection(.enabled)
                    }
                }
                problemsSection
                vpnInventorySection
                firewallRulesSection
                if let report = state.doctorReport {
                    Section {
                        Label(report.ok ? "No lockout risk found" : "Found something to fix",
                              systemImage: report.ok ? "checkmark.circle.fill" : "exclamationmark.triangle.fill")
                            .foregroundStyle(report.ok ? .green : .orange)
                            .font(.headline)
                    }
                    ForEach(report.checks) { check in
                        checkRow(check)
                    }
                }
            }
            .listStyle(.inset)
        } else if let error = state.doctorError {
            guided(symbol: "exclamationmark.triangle", title: "Couldn't run diagnostics", message: error)
        } else if !state.cliFound {
            guided(symbol: "questionmark.circle", title: "dezhban CLI not found",
                   message: "Install the dezhban command-line tool, then run diagnostics again.")
        } else {
            guided(symbol: "stethoscope", title: "No results yet", message: "Run diagnostics to see tunnels, endpoints, and lockout risks.")
        }
    }

    // MARK: - problems

    /// Recent warn-and-worse records from dezhban's own log.
    ///
    /// The three states are deliberately distinct. Nothing found is the GOOD
    /// answer and says so; not-yet-asked shows nothing; could-not-ask explains
    /// itself. Collapsing "no problems" into "no data" would make a healthy host
    /// look like a broken reader, and the reverse would be worse.
    @ViewBuilder
    private var problemsSection: some View {
        if let problems = state.problems {
            Section("Recent problems") {
                if problems.isEmpty {
                    Label("Nothing logged as a warning or an error.", systemImage: "checkmark.circle.fill")
                        .foregroundStyle(.green)
                        .font(.callout)
                } else {
                    ForEach(problems.reversed()) { problemRow($0) }
                }
            }
        } else if state.cliFound {
            Section("Recent problems") {
                Label("Couldn't read dezhban's log. A CLI older than `dezhban logs` can't be asked.",
                      systemImage: "questionmark.circle")
                    .font(.callout)
                    .foregroundStyle(.secondary)
            }
        }
    }

    /// One record. The message is what a person reads; the attrs are the
    /// evidence, in the order dezhban wrote them — that order reads as a
    /// sentence, which is why they are carried as ordered pairs rather than a
    /// dictionary all the way from Go.
    private func problemRow(_ r: LogRecord) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Image(systemName: r.isError ? "xmark.octagon.fill" : "exclamationmark.triangle.fill")
                .foregroundStyle(r.isError ? .red : .orange)
            VStack(alignment: .leading, spacing: 2) {
                Text(r.msg)
                    .font(.callout)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
                if !r.detail.isEmpty {
                    Text(r.detail)
                        .font(.caption.monospaced())
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            Spacer(minLength: 8)
            if let t = r.time {
                Text(Self.stamp.string(from: t))
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .monospacedDigit()
            }
        }
    }

    // MARK: - export

    /// Writes the bundle where the user chooses, then reveals it in Finder.
    ///
    /// Redacted by default. The checkbox is the deliberate opt-out, and its
    /// label says what the unredacted bundle contains rather than describing the
    /// mechanism — someone about to paste this into a public issue needs to read
    /// the consequence, not the feature.
    private func exportReport() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.canCreateDirectories = true
        panel.prompt = "Save Here"
        panel.message = "Where should the diagnostic bundle go?"

        let includeNetwork = NSButton(checkboxWithTitle: "Include my real VPN server addresses and exit IP",
                                      target: nil, action: nil)
        includeNetwork.state = .off
        includeNetwork.toolTip = "Leave this off to get a bundle that is safe to attach to a public issue: "
            + "addresses and hostnames are replaced with stable placeholders, so it is still diagnosable."
        panel.accessoryView = includeNetwork
        panel.isAccessoryViewDisclosed = true

        guard panel.runModal() == .OK, let dir = panel.url else { return }
        exporting = true
        let full = includeNetwork.state == .on
        DispatchQueue.global(qos: .userInitiated).async {
            let result = DezhbanCLI.writeReport(to: dir, includeNetwork: full)
            DispatchQueue.main.async {
                exporting = false
                switch result {
                case .wrote(let url):
                    NSWorkspace.shared.activateFileViewerSelecting([url])
                case .failed(let message):
                    state.showInLogs(title: "dezhban — export diagnostics", text: message)
                }
            }
        }
    }

    // MARK: - firewall rules

    /// What the guard is doing to your traffic, in three parts, because they
    /// answer three different questions and are not interchangeable:
    ///
    ///  - **Applied** — what dezhban recorded installing, and when. Its own
    ///    account: cheap, unprivileged, and identical on every platform.
    ///  - **In the kernel** — what is actually loaded, read back on demand.
    ///    Costs a password, so it is never automatic. This is the half that can
    ///    see something outside dezhban having flushed the firewall.
    ///  - **Would apply** — the ruleset of each posture, rendered without
    ///    applying anything (`print-rules --mode`). The safe way to find out
    ///    what FULL BLOCK does before you are in it.
    ///
    /// The labels say which is which. "The current rules" would be a claim only
    /// the middle one can make.
    @ViewBuilder
    private var firewallRulesSection: some View {
        Section("Firewall rules") {
            appliedRow
            installedRow
            previewRows
        }
    }

    @ViewBuilder
    private var appliedRow: some View {
        if let a = state.appliedRules {
            rulesDisclosure(
                title: "Applied by dezhban — \(postureLabel(a.mode))",
                caption: "What dezhban installed at \(Self.stamp.string(from: a.at)), in \(a.backend) syntax. "
                    + "This is dezhban's own record, not a reading of the firewall.",
                rules: a.rules)
        } else {
            Label("No ruleset recorded yet — dezhban writes one every time it applies rules. "
                    + "In standby it has applied none.",
                  systemImage: "doc.text")
                .font(.callout)
                .foregroundStyle(.secondary)
        }
    }

    @ViewBuilder
    private var installedRow: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 10) {
                Button("Read from the kernel…") { state.readInstalledRules() }
                    .disabled(state.installedRulesRunning || !state.cliFound)
                    .help("Asks the firewall itself what dezhban rules it holds. Needs your password. "
                        + "It only reads — nothing is installed, changed, or repaired.")
                if state.installedRulesRunning { ProgressView().controlSize(.small) }
            }
            if let error = state.installedRulesError {
                Label(error, systemImage: "exclamationmark.triangle.fill")
                    .font(.callout)
                    .foregroundStyle(.orange)
                    .textSelection(.enabled)
            }
            if let i = state.installedRules {
                if i.drift {
                    // The finding, stated plainly. No repair button: the run
                    // loop's verification tick already re-applies rules that go
                    // missing, and a second repairer would be a second writer of
                    // the firewall.
                    Label("dezhban applied rules, but the firewall holds none. Something removed them. "
                            + "dezhban's own verification re-applies on its next check — this pane only reports.",
                          systemImage: "exclamationmark.triangle.fill")
                        .font(.callout)
                        .foregroundStyle(.orange)
                } else if !i.loaded {
                    Label("No dezhban rules are loaded. That is expected in standby, or with dezhban stopped.",
                          systemImage: "info.circle")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                } else {
                    rulesDisclosure(
                        title: "In the kernel now",
                        caption: "Read back from the firewall, in \(i.backend) syntax. It will not match the "
                            + "applied text byte for byte — the firewall renders its own normalised form.",
                        rules: i.installed)
                }
            }
        }
    }

    @ViewBuilder
    private var previewRows: some View {
        ForEach(RulesetPreview.allCases) { mode in
            rulesDisclosure(
                title: "Would apply — \(mode.label)",
                caption: mode.detail,
                rules: nil,
                load: { DezhbanCLI.renderRules(mode: mode) })
        }
    }

    /// One collapsed ruleset. `rules` is text already in hand; `load` fetches it
    /// the first time it is opened instead — the three previews each cost a
    /// subprocess, and rendering all of them on every visit to this pane would
    /// be three processes nobody asked for.
    @ViewBuilder
    private func rulesDisclosure(title: String, caption: String,
                                 rules: String?,
                                 load: (() -> String?)? = nil) -> some View {
        DisclosureGroup {
            RulesetBody(rules: rules, load: load)
        } label: {
            VStack(alignment: .leading, spacing: 2) {
                Text(title).font(.callout.weight(.medium))
                Text(caption)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    /// The posture strings are stable CLI identifiers, not display text, so they
    /// are mapped rather than shown raw. An unknown one is shown as-is: a
    /// daemon newer than this app is not a reason to hide what it said.
    private func postureLabel(_ mode: String) -> String {
        RulesetPreview(rawValue: mode)?.label ?? mode
    }

    private static let stamp: DateFormatter = {
        let f = DateFormatter()
        f.dateStyle = .none
        f.timeStyle = .medium
        return f
    }()

    /// The VPN inventory (`detect-vpn --json`): which tunnels and VPN apps
    /// detection can see, and which one is connected now. Hidden entirely when
    /// the CLI is too old for the subcommand — degrade by omission, never a
    /// wrong claim. Same List as the doctor checks: one scroll surface.
    @ViewBuilder
    private var vpnInventorySection: some View {
        if let inv = state.vpnInventory {
            Section("Your VPNs") {
                // Only when the scan can actually be quoted: an errored or
                // unsupported scan prints its own row below, and asserting
                // "found none" above it would answer a question nobody
                // managed to ask.
                if !inv.hasAnything && inv.scanConclusive {
                    Text("No VPN apps or tunnels found.")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                }
                ForEach(inv.tunnels ?? [], id: \.self) { tunnel in
                    Label(tunnel, systemImage: "point.3.connected.trianglepath.dotted")
                        .font(.callout)
                }
                if let connected = inv.connectedVPN, !connected.isEmpty {
                    Label("\(connected) — connected now", systemImage: "checkmark.circle.fill")
                        .font(.callout)
                        .foregroundStyle(Color.accentColor)
                }
                ForEach(inv.candidates ?? []) { cand in
                    candidateRow(cand)
                }
                if let derr = inv.discoveryErr, !derr.isEmpty {
                    Label(derr, systemImage: "exclamationmark.triangle")
                        .font(.callout)
                        .foregroundStyle(.orange)
                }
                if inv.discoverySupported == false {
                    Text("VPN app discovery works on macOS only; tunnels above are still detected.")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                }
                // Shown whether or not anything was found: the app always runs
                // unprivileged, and the scan it drives sees only this user's
                // sockets. That makes an empty list no evidence of absence, and
                // a non-empty one still possibly short — so the caveat belongs
                // on both, not just under the empty state it also suppresses.
                if inv.scanPrivileged == false {
                    Text("Scanned as your user — a VPN whose connection runs as root won't appear here. "
                        + "Run `sudo dezhban doctor --discover` for the full picture.")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                }
            }
        }
    }

    /// One discovered VPN transport, named and with the server address it
    /// connects to.
    ///
    /// A candidate that is also the connected service is deliberately NOT
    /// suppressed here even though the "— connected now" row already names it:
    /// the two rows carry different facts, and this is the only one that says
    /// *where* the tunnel goes. It used to take a `connected` parameter for a
    /// suppression rule that was never written — the parameter went unread and
    /// the comment described logic that wasn't there.
    private func candidateRow(_ cand: VPNInventory.Candidate) -> some View {
        var detail: String?
        if let server = cand.server, !server.isEmpty {
            detail = cand.port.map { "\(server):\($0)" } ?? server
        }
        return VStack(alignment: .leading, spacing: 2) {
            Label(cand.displayName, systemImage: "app.badge.checkmark")
                .font(.callout)
            if let detail {
                Text("server \(detail)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
            }
        }
    }

    /// Indexed, not `id: \.self`: detail lines are free-form text from the
    /// daemon and two identical ones in the same check (or two paragraph
    /// breaks) would collide on a value-based id, which SwiftUI resolves by
    /// dropping rows.
    private func checkRow(_ check: DoctorCheck) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Label(rowTitle(check), systemImage: symbol(for: check.status))
                .foregroundStyle(color(for: check.status))
                .font(.body.weight(.semibold))
            ForEach(Array((check.details ?? []).enumerated()), id: \.offset) { _, line in
                // An empty detail is a paragraph break, not a finding (see
                // doctorCheck.Details in cmd/dezhban/main.go) — rendering it as
                // a Text would leave a stray blank row where the CLI puts a
                // blank line.
                if line.isEmpty {
                    Spacer().frame(height: 4)
                } else {
                    Text(line).font(.callout).foregroundStyle(.secondary).textSelection(.enabled)
                }
            }
            ForEach(Array((check.fixes ?? []).enumerated()), id: \.offset) { _, fix in
                Label(fix, systemImage: "wrench.and.screwdriver")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
            }
        }
        .padding(.vertical, 4)
    }

    private func rowTitle(_ check: DoctorCheck) -> String {
        let name = checkNames[check.name] ?? check.name.capitalized
        return check.summary.isEmpty ? name : "\(name) — \(check.summary)"
    }

    /// Display names for the checks `dezhban doctor --json` ships. The fallback
    /// is `.capitalized`, which is fine for a one-word name and wrong for a
    /// camelCased one ("armAtBoot" reads as "Armatboot"), so every shipped check
    /// belongs here. The wording matches the CLI's own section headings — one
    /// voice, per docs/concepts/glossary.md.
    private let checkNames: [String: String] = [
        "config": "Config", "tunnels": "Tunnels", "endpoints": "Endpoints",
        "lockout": "Lockout risk", "service": "Boot service",
        "armAtBoot": "Arm at boot", "endpointRetention": "Learned endpoints",
        "touchID": "Touch ID", "discover": "Discovered servers",
    ]

    private func symbol(for status: String) -> String {
        switch status {
        case "ok": return "checkmark.circle.fill"
        case "warn": return "exclamationmark.triangle.fill"
        case "fail": return "xmark.octagon.fill"
        default: return "questionmark.circle"
        }
    }

    private func color(for status: String) -> Color {
        switch status {
        case "ok": return .green
        case "warn": return .orange
        case "fail": return .red
        default: return .secondary
        }
    }

    private func guided(symbol: String, title: String, message: String) -> some View {
        VStack(spacing: 12) {
            Image(systemName: symbol).font(.system(size: 40)).foregroundStyle(.secondary)
            Text(title).font(.title3.weight(.semibold))
            Text(message).multilineTextAlignment(.center).foregroundStyle(.secondary).frame(maxWidth: PaneMetrics.proseColumn)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding(24)
    }

}

/// The body of one ruleset disclosure: monospaced, selectable, and scrollable in
/// its own right so a long ruleset cannot stretch the pane.
///
/// It exists as a view rather than a `@ViewBuilder` function so `load` can run
/// once, on first appearance, and hold its result. The three posture previews
/// each cost a `print-rules` subprocess; rendering them eagerly would spawn
/// three processes on every visit to Diagnostics for text nobody may open.
private struct RulesetBody: View {
    let rules: String?
    let load: (() -> String?)?

    @State private var loaded: String?
    @State private var failed = false

    var body: some View {
        Group {
            if let text = rules ?? loaded {
                ScrollView([.horizontal, .vertical]) {
                    Text(text)
                        .font(.system(.caption, design: .monospaced))
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .frame(maxHeight: 260)
            } else if failed {
                Text("Couldn't render this ruleset. `dezhban print-rules` needs a config it can read.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else {
                ProgressView().controlSize(.small)
            }
        }
        .onAppear {
            guard rules == nil, loaded == nil, let load else { return }
            DispatchQueue.global(qos: .userInitiated).async {
                let text = load()
                DispatchQueue.main.async {
                    if let text { loaded = text } else { failed = true }
                }
            }
        }
    }
}
