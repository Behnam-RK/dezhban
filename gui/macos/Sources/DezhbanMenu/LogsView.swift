import AppKit
import SwiftUI

/// The Logs & Diagnostics pane: the single output surface for the window —
/// run-doctor, last-hour logs, the live `log stream` (with Stop), plus the
/// transcripts other panes route here (panic, install/uninstall, config apply).
struct LogsView: View {
    @EnvironmentObject var state: AppState
    @ObservedObject var console: Console

    var body: some View {
        VStack(spacing: 0) {
            toolbar
            Divider()
            ConsoleTextView(console: console)
        }
        .navigationTitle("Logs & Diagnostics")
    }

    private var toolbar: some View {
        HStack(spacing: 10) {
            Button("Run diagnostics") { runDiagnostics() }
                .disabled(!state.cliFound)
                .help("Read-only `dezhban doctor` — no root, no firewall effects.")
            // The log actions run `/usr/bin/log` directly and don't need the
            // dezhban binary, so they stay available even when the CLI is
            // uninstalled/mislocated — reading the unified log is exactly the
            // diagnostic you want then.
            Button("Show last hour") { showRecent() }
            if console.isStreaming {
                Button {
                    console.stopStream()
                } label: {
                    Label("Stop", systemImage: "stop.fill")
                }
            } else {
                Button {
                    console.startLogStream()
                } label: {
                    Label("Stream live", systemImage: "play.fill")
                }
            }
            Button("Open in Console.app") {
                NSWorkspace.shared.open(URL(fileURLWithPath: "/System/Applications/Utilities/Console.app"))
            }
            Spacer()
            Text(console.title)
                .font(.callout)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.tail)
        }
        .padding(12)
    }

    /// Unprivileged, read-only `doctor` run → console.
    private func runDiagnostics() {
        console.set(title: "Diagnostics — running…", text: "")
        DispatchQueue.global(qos: .userInitiated).async {
            let result = DezhbanCLI.run(["doctor", "--config", DezhbanCLI.resolvedConfigPath()])
            DispatchQueue.main.async {
                console.set(title: "Diagnostics", text: result.output.isEmpty ? "(no output)" : result.output)
            }
        }
    }

    private func showRecent() {
        console.set(title: "Logs — reading last hour…", text: "")
        DispatchQueue.global(qos: .userInitiated).async {
            let result = DezhbanCLI.showRecentLogs()
            DispatchQueue.main.async {
                console.set(title: "Logs (last hour)",
                            text: result.output.isEmpty ? "(no matching log lines)" : result.output)
            }
        }
    }
}

/// AppKit-backed monospaced transcript view. The NSTextView renders the shared
/// Console.storage directly (appends grow the text storage in place — O(n) over
/// a long-running `log stream`, no String re-copy per chunk).
struct ConsoleTextView: NSViewRepresentable {
    let console: Console

    func makeNSView(context: Context) -> NSScrollView {
        let scroll = NSTextView.scrollableTextView()
        let tv = scroll.documentView as! NSTextView
        tv.isEditable = false
        tv.isSelectable = true
        tv.textContainerInset = NSSize(width: 8, height: 8)
        tv.font = NSFont.monospacedSystemFont(ofSize: 11, weight: .regular)
        tv.layoutManager?.replaceTextStorage(console.storage)
        console.textView = tv
        return scroll
    }

    func updateNSView(_ nsView: NSScrollView, context: Context) {
        // Content flows through Console.storage; nothing to sync here.
    }
}
