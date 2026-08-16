import SwiftUI

/// The detail column the window's AppKit split view hosts (see MainWindow).
///
/// This file was one `MainView` wrapping a SwiftUI `NavigationSplitView`. The
/// split is AppKit's now, because the window itself is a hand-built NSWindow
/// with no SwiftUI scene anywhere — so every chrome behaviour was riding an
/// undocumented NSHostingController→NSWindow bridge that had already broken
/// once. AppKit owns the chrome and the sidebar (SidebarViewController);
/// SwiftUI owns the panes.

/// The detail column: the selected pane, plus the first-run sheet.
struct DetailHostView: View {
    @EnvironmentObject var state: AppState

    var body: some View {
        Group {
            switch state.selectedSection ?? .overview {
            case .overview: OverviewView()
            case .diagnostics: DiagnosticsView()
            case .settings: SettingsView()
            case .help: HelpView()
            case .logs: LogsView(console: state.console)
            case .about: AboutView()
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        // A sheet rather than a second window: the wizard is a step you are in,
        // not a place you can leave open behind the thing it configures.
        .sheet(isPresented: $state.showFirstRun) {
            FirstRunView { saved in
                state.showFirstRun = false
                if saved {
                    state.refreshServiceState()
                    state.selectedSection = .overview
                }
            }
            .environmentObject(state)
        }
        // The token capability probe is a keychain WRITE, and a locked login
        // keychain answers one with a system dialog. Warming it here rather than
        // at launch is the whole point: a menubar-only session — which is most of
        // them — then never touches the keychain for a feature nobody asked
        // about, while anyone who opens this window has the answer settled long
        // before they can navigate to Settings or About. Both panes still fall
        // back to `capabilityIfKnown` + `resolveCapability`, so this is an
        // optimisation and never the thing that keeps them off the main thread.
        .onAppear {
            ControlToken.warmCapability()
            // Feed the Diagnostics sidebar badge: a report older than 15 minutes
            // (or absent) is refreshed when the window opens. Staleness-gated,
            // never the 1s timer — doctor is a subprocess.
            state.runDoctorIfStale(maxAge: 15 * 60)
        }
    }
}
