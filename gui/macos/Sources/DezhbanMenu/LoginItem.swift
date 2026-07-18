import Foundation
import ServiceManagement

/// Wraps the menubar app's own login-item registration via SMAppService
/// (macOS 13+). No LaunchAgent plist to ship — the system framework registers
/// the currently-running .app to relaunch at login. Requires a proper bundle
/// with a bundle identifier (assembled by build-app.sh), so it is a no-op /
/// failure when run as a bare SwiftPM binary.
enum LoginItem {
    static var isEnabled: Bool {
        SMAppService.mainApp.status == .enabled
    }

    /// Toggles login-at-launch. Returns the resulting enabled state; on error it
    /// logs and returns the unchanged prior state.
    @discardableResult
    static func toggle() -> Bool {
        do {
            if isEnabled {
                try SMAppService.mainApp.unregister()
            } else {
                try SMAppService.mainApp.register()
            }
        } catch {
            NSLog("DezhbanMenu: login item toggle failed: \(error)")
        }
        return isEnabled
    }
}
