import SwiftUI

/// The main window's root: sidebar navigation over the five sections. The
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
            case .vpnGuard: VPNGuardView()
            case .settings: SettingsView()
            case .logs: LogsView(console: state.console)
            case .about: AboutView()
            }
        }
    }
}
