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
    }
}
