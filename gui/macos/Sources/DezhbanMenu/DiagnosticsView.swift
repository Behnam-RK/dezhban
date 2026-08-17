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
        .onAppear {
            // The report and the inventory live on AppState (they feed the
            // sidebar badge and survive navigation); this pane only asks for a
            // refresh when what is there has gone stale.
            state.runDoctorIfStale(maxAge: 15 * 60)
            state.refreshVPNInventoryIfStale()
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
                vpnInventorySection
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
