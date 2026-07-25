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
    @State private var report: DoctorReport?
    @State private var discover = false
    @State private var running = false
    @State private var error: String?

    var body: some View {
        VStack(spacing: 0) {
            toolbar
            Divider()
            content
        }
        .navigationTitle("Diagnostics")
        .onAppear {
            if report == nil { run() }
        }
    }

    private var toolbar: some View {
        HStack(spacing: 10) {
            Button("Run diagnostics") { run() }
                .disabled(running || !state.cliFound)
            Toggle("Find my VPN's server", isOn: $discover)
                .toggleStyle(.checkbox)
                .help("macOS-only best-effort hunt for the connected VPN's real server IP (`--discover`).")
            Spacer()
            if running {
                ProgressView().controlSize(.small)
            }
        }
        .padding(12)
    }

    @ViewBuilder
    private var content: some View {
        if let error {
            guided(symbol: "exclamationmark.triangle", title: "Couldn't run diagnostics", message: error)
        } else if let report {
            List {
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

    private func checkRow(_ check: DoctorCheck) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Label(rowTitle(check), systemImage: symbol(for: check.status))
                .foregroundStyle(color(for: check.status))
                .font(.body.weight(.semibold))
            ForEach(check.details ?? [], id: \.self) { line in
                Text(line).font(.callout).foregroundStyle(.secondary).textSelection(.enabled)
            }
            ForEach(check.fixes ?? [], id: \.self) { fix in
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

    private let checkNames: [String: String] = [
        "config": "Config", "tunnels": "Tunnels", "endpoints": "Endpoints",
        "lockout": "Lockout risk", "touchID": "Touch ID", "discover": "Discovered servers",
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
            Text(message).multilineTextAlignment(.center).foregroundStyle(.secondary).frame(maxWidth: 420)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding(24)
    }

    /// Uses DezhbanCLI.exec directly (not `.run`) so stdout and stderr stay
    /// separate: `doctor` logs informational lines (autodetect, DNS warnings)
    /// to stderr, and `.run`'s CommandResult joins both into one string —
    /// which would corrupt the JSON parse the moment anything logged.
    private func run() {
        guard let bin = DezhbanCLI.binaryPath() else {
            error = "dezhban CLI not found in a trusted install location"
            return
        }
        running = true
        error = nil
        let args = discover
            ? ["doctor", "--json", "--discover", "--config", DezhbanCLI.resolvedConfigPath()]
            : ["doctor", "--json", "--config", DezhbanCLI.resolvedConfigPath()]
        DispatchQueue.global(qos: .userInitiated).async {
            let r = DezhbanCLI.exec(bin, args)
            let decoded = r.out.data(using: .utf8).flatMap(DoctorReport.decode)
            DispatchQueue.main.async {
                running = false
                if let decoded {
                    report = decoded
                } else {
                    let text = [r.out, r.err].filter { !$0.isEmpty }.joined(separator: "\n")
                    error = text.isEmpty ? "No output from `dezhban doctor --json`." : text
                }
            }
        }
    }
}
