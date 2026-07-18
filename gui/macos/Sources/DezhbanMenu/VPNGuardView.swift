import SwiftUI

/// The VPN guard configuration pane — SwiftUI port of the retired VPNConfigPanel.
/// Seeds every field from `dezhban config get <key>` on appear (the raw on-disk
/// config is the source of truth, never a cached second-schema Swift model) and
/// applies through one batched `dezhban config set` — the same dotted-key
/// accessor layer (`configFields` in cmd/dezhban/config_cmd.go) the CLI uses.
/// No new Go command: validation stays single-sourced in `config.Validate`.
struct VPNGuardView: View {
    @EnvironmentObject var state: AppState

    // The exact vpn.* keys already in configFields — the scope of this pane,
    // not a general config editor.
    private static let keys = [
        "vpn.enabled", "vpn.tunnelInterfaces", "vpn.endpoints",
        "vpn.autodetect", "vpn.autoDiscoverEndpoints", "vpn.autoArm",
        "vpn.endpointRefresh", "vpn.tunnelWatch",
    ]

    @State private var enabled = false
    @State private var tunnelInterfaces = ""
    @State private var endpoints = ""
    @State private var autodetect = false
    @State private var autoDiscover = false
    @State private var autoArm = false
    @State private var endpointRefresh = ""
    @State private var tunnelWatch = ""
    @State private var status = ""
    @State private var canApply = false

    var body: some View {
        VStack(spacing: 0) {
            Form {
                Section("VPN guard") {
                    Toggle("Enable VPN guard (vpn.enabled)", isOn: $enabled)
                    TextField("Tunnel interfaces (comma-sep)", text: $tunnelInterfaces)
                    TextField("Endpoints (comma-sep)", text: $endpoints)
                }
                Section("Autodetection") {
                    Toggle("Autodetect tunnel interface (vpn.autodetect)", isOn: $autodetect)
                    Toggle("Auto-discover endpoints (vpn.autoDiscoverEndpoints)", isOn: $autoDiscover)
                    Toggle("Auto-arm when a VPN connects (vpn.autoArm)", isOn: $autoArm)
                        .help("With no VPN connected the daemon idles in standby (nothing blocked) and arms "
                            + "the guard the moment a tunnel appears. It never disarms on a drop — that's the "
                            + "kill switch — only an explicit Unblock with the VPN off returns to standby.")
                }
                Section("Timing") {
                    TextField("Endpoint refresh (e.g. 30s)", text: $endpointRefresh)
                    TextField("Tunnel watch (e.g. 5s)", text: $tunnelWatch)
                }
            }
            .formStyle(.grouped)
            .disabled(!canApply)

            Divider()
            footer
        }
        .navigationTitle("VPN Guard")
        .onAppear(perform: seed)
    }

    private var footer: some View {
        HStack {
            Text(status)
                .font(.callout)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.tail)
            Spacer()
            Button("Reload", action: seed)
            Button("Apply…", action: apply)
                .keyboardShortcut(.defaultAction)
                .disabled(!canApply)
        }
        .padding(12)
    }

    // MARK: - seeding

    /// One `config get <key>` per rendered field, every time the pane appears —
    /// never reuses a stale in-memory copy from a prior visit. A failed seed
    /// clears the fields and leaves Apply disabled (an error string must never
    /// be written back as a value).
    private func seed() {
        status = "Loading current config…"
        canApply = false
        enabled = false; autodetect = false; autoDiscover = false; autoArm = false
        tunnelInterfaces = ""; endpoints = ""; endpointRefresh = ""; tunnelWatch = ""
        ConfigApply.seed(keys: Self.keys) { values, error in
            if let error = error {
                status = error
                return
            }
            guard let v = values else { return }
            enabled = (v[0] == "true")
            tunnelInterfaces = v[1]
            endpoints = v[2]
            autodetect = (v[3] == "true")
            autoDiscover = (v[4] == "true")
            autoArm = (v[5] == "true")
            endpointRefresh = v[6]
            tunnelWatch = v[7]
            status = "Seeded from \(DezhbanCLI.resolvedConfigPath())"
            canApply = true
        }
    }

    // MARK: - apply

    private func apply() {
        let refresh = endpointRefresh.trimmingCharacters(in: .whitespaces)
        let watch = tunnelWatch.trimmingCharacters(in: .whitespaces)
        for (label, value) in [("Endpoint refresh", refresh), ("Tunnel watch", watch)] {
            guard ConfigApply.looksLikeGoDuration(value) else {
                ConfigApply.invalidDurationAlert(label, value)
                return
            }
        }

        let pairs = [
            "vpn.enabled=\(enabled)",
            "vpn.tunnelInterfaces=\(tunnelInterfaces)",
            "vpn.endpoints=\(endpoints)",
            "vpn.autodetect=\(autodetect)",
            "vpn.autoDiscoverEndpoints=\(autoDiscover)",
            "vpn.autoArm=\(autoArm)",
            "vpn.endpointRefresh=\(refresh)",
            "vpn.tunnelWatch=\(watch)",
        ]

        let restart = ConfigApply.confirmRestart()
        canApply = false
        status = restart ? "Applying and restarting…" : "Applying…"
        ConfigApply.apply(pairs: pairs, restart: restart, awaitPosture: true,
                          title: "VPN config") { outcome in
            canApply = true
            status = outcome.status
            if let title = outcome.transcriptTitle, let text = outcome.transcript {
                state.showInLogs(title: title, text: text)
            }
        }
    }
}
