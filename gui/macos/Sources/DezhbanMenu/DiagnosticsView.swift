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
        if let error = state.doctorError {
            guided(symbol: "exclamationmark.triangle", title: "Couldn't run diagnostics", message: error)
        } else if let report = state.doctorReport {
            List {
                vpnInventorySection
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
            .listStyle(.inset)
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
                if !inv.hasAnything {
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
                    candidateRow(cand, connected: inv.connectedVPN)
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
            }
        }
    }

    private func candidateRow(_ cand: VPNInventory.Candidate, connected: String?) -> some View {
        var parts: [String] = []
        if let server = cand.server, !server.isEmpty {
            parts.append(cand.port.map { "\(server):\($0)" } ?? server)
        }
        let detail = parts.joined(separator: " ")
        // Already named by the connected row above? Then this row only adds
        // the server address; keep the name so the pairing is readable.
        return VStack(alignment: .leading, spacing: 2) {
            Label(cand.displayName, systemImage: "app.badge.checkmark")
                .font(.callout)
            if !detail.isEmpty {
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
