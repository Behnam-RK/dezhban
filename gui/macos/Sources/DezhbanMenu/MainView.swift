import SwiftUI

/// The main window's root: sidebar navigation over the window's sections. The
/// selection lives in AppState so actions elsewhere (e.g. a window-triggered
/// panic) can navigate to the Logs pane programmatically.
struct MainView: View {
    @EnvironmentObject var state: AppState

    var body: some View {
        NavigationSplitView {
            List(selection: $state.selectedSection) {
                ForEach(SidebarSection.allCases) { section in
                    Label(section.label, systemImage: section.systemImage)
                        .tag(section)
                }
            }
            .navigationSplitViewColumnWidth(min: 180, ideal: 200)
        } detail: {
            switch state.selectedSection ?? .overview {
            case .overview: OverviewView()
            case .diagnostics: DiagnosticsView()
            case .settings: SettingsView()
            case .help: HelpView()
            case .logs: LogsView(console: state.console)
            case .about: AboutView()
            }
        }
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
        .onAppear { ControlToken.warmCapability() }
    }
}
