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

    var body: some View {
        VStack(spacing: 0) {
            toolbar
            Divider()
            content
        }
        .onDisappear { state.clearInstalledRules() }
        .onAppear {
            // The report and the inventory live on AppState (they feed the
            // sidebar badge and survive navigation); this pane only asks for a
            // refresh when what is there has gone stale.
            state.runDoctorIfStale(maxAge: 15 * 60)
            state.refreshVPNInventoryIfStale()
            state.refreshAppliedRules()
        }
    }

    private var toolbar: some View {
        HStack(spacing: 10) {
            Button("Run diagnostics") { run() }
                .disabled(state.doctorRunning || !state.cliFound)
            Toggle("Find my VPN's server", isOn: $discover)
                .toggleStyle(.checkbox)
                .help("macOS-only best-effort hunt for the connected VPN's real server IP (`--discover`).")
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
        // The kernel readback is a snapshot and re-reading it costs a password,
        // so a refresh drops it rather than silently renewing it. Keeping it
        // would leave the previous posture's rules under a heading that says
        // "now", beside freshly-read rows.
        state.clearInstalledRules()
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
        //
        // The firewall-rules section needs NEITHER of them: the applied record is
        // its own unprivileged read, the kernel button is a button, and the
        // previews render from config. Leaving it inside the gate hid it exactly
        // where it is most wanted — on a host where `doctor --json` cannot run,
        // which is the state someone is most likely diagnosing — and on every
        // first open until the async doctor returned. Same bug the paragraph
        // above describes for the inventory, one section over, so the gate now
        // opens for anything the List can show.
        if state.doctorReport != nil || state.vpnInventory != nil || state.cliFound {
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
            // The posture is in the title and the time is only in the caption,
            // so a pane held open across GUARD → FULL BLOCK kept reading
            // "Applied by dezhban — Guard". This read is unprivileged and cheap,
            // unlike the kernel one, so the honest fix is to say WHEN, in the
            // title, where the claim is made.
            rulesDisclosure(
                title: "Applied by dezhban — \(postureLabel(a.mode)), at \(Self.stamp.string(from: a.at))",
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
                    // Scoped to dezhban's OWN rules. On Windows the blocking
                    // lives in each profile's default outbound action, so their
                    // absence does not mean egress is open — and the readback
                    // below says which.
                    Label("dezhban has no rules of its own loaded — expected in standby, or with dezhban stopped.",
                          systemImage: "info.circle")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                    if !i.installed.isEmpty {
                        rulesDisclosure(
                            title: state.installedRulesAt.map { "What the firewall reported, read at \(Self.stamp.string(from: $0))" }
                                ?? "What the firewall reported",
                            caption: "dezhban's own rules are absent, but this is what the firewall said when asked. "
                                + "On Windows the outbound default is where the blocking lives.",
                            rules: i.installed)
                    }
                } else {
                    // Titled by WHEN it was read, never "now": this is a
                    // snapshot nothing refreshes, and the posture can change
                    // underneath it.
                    rulesDisclosure(
                        title: state.installedRulesAt.map { "In the kernel, read at \(Self.stamp.string(from: $0))" }
                            ?? "In the kernel, as read",
                        caption: "Read back from the firewall, in \(i.backend) syntax. It is a snapshot from when "
                            + "you pressed the button, not a live view — read it again after the posture changes. "
                            + "It will not match the applied text byte for byte: the firewall renders its own "
                            + "normalised form.",
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

    /// Times shown beside a ruleset carry their DATE as well. A record survives
    /// restarts, so one applied three days ago rendered as a bare "14:02:11"
    /// reads as today — precisely the wrong impression for the row that tells
    /// someone what is enforcing.
    private static let stamp: DateFormatter = {
        let f = DateFormatter()
        f.dateStyle = .short
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
