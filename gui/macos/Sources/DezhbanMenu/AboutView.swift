import SwiftUI

/// Real-data About pane: version, resolved config path, binary path, the
/// enforcement posture (from the shared snapshot), whether the OS service is
/// installed, and which elevation path privileged actions will take — surfaced
/// so "why did I get a password dialog?" is diagnosable from the app itself.
struct AboutView: View {
    @EnvironmentObject var state: AppState

    @State private var version = ""
    @State private var configPath = ""
    @State private var binaryPath = ""

    var body: some View {
        Form {
            Section {
                LabeledContent("Version", value: version.isEmpty ? "dezhban (version unknown)" : version)
                LabeledContent("Config path") { pathText(configPath) }
                LabeledContent("Binary path") { pathText(binaryPath) }
            }
            Section {
                LabeledContent("Posture",
                               value: state.isLive ? PostureUI.humanPosture(state.snapshot!) : "stopped")
                LabeledContent("Service",
                               value: state.serviceIsInstalled ? "installed" : "not installed")
                LabeledContent("Elevation",
                               value: Elevation.isAvailable
                                   ? "Authorization Services (Touch ID capable)"
                                   : "AppleScript fallback (password only)")
            }
        }
        .formStyle(.grouped)
        .navigationTitle("About Dezhban")
        .onAppear(perform: load)
    }

    private func pathText(_ s: String) -> some View {
        Text(s)
            .textSelection(.enabled)
            .foregroundStyle(.secondary)
            .truncationMode(.middle)
            .lineLimit(1)
    }

    /// Only `version` needs a fresh CLI call — posture and service state are
    /// already live in AppState for the rest of the window.
    private func load() {
        configPath = DezhbanCLI.resolvedConfigPath()
        binaryPath = DezhbanCLI.binaryPath() ?? "(not found — install it first)"
        DispatchQueue.global(qos: .userInitiated).async {
            let v = DezhbanCLI.run(["version"]).output.trimmingCharacters(in: .whitespacesAndNewlines)
            DispatchQueue.main.async { version = v }
        }
    }
}
