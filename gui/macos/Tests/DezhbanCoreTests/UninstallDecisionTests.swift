import Foundation
import Testing
@testable import DezhbanCore

struct UninstallDecisionTests {
    /// The uninstaller settles it on its own. Whatever else is or is not on
    /// disk, if the script is there the script is what runs.
    @Test func uninstallerPresentAlwaysHandsOff() {
        for cli in [true, false] {
            for plist in [true, false] {
                #expect(UninstallDecision.situation(uninstallerExists: true,
                                                    cliFound: cli,
                                                    daemonPlistExists: plist) == .handOff)
            }
        }
    }

    /// The bug this rule exists to prevent. `scripts/install.sh` fetches the
    /// uninstaller over the network and treats a failure as non-fatal, and
    /// `install-local.sh` never fetches it — so a fully installed, enforcing Mac
    /// can have no uninstall.sh. Reading that as "already removed" told the user
    /// their kill switch was gone while it was still cutting their traffic.
    @Test func missingUninstallerIsNotAMissingInstall() {
        #expect(UninstallDecision.situation(uninstallerExists: false,
                                            cliFound: true,
                                            daemonPlistExists: false)
            == .uninstallerMissingButInstalled(cliFound: true))
        // The plist alone has to be enough: a machine whose binary was removed
        // by hand while the launchd job stayed loaded is still enforcing, and it
        // is the case a CLI-only check would get wrong.
        #expect(UninstallDecision.situation(uninstallerExists: false,
                                            cliFound: false,
                                            daemonPlistExists: true)
            == .uninstallerMissingButInstalled(cliFound: false))
        #expect(UninstallDecision.situation(uninstallerExists: false,
                                            cliFound: true,
                                            daemonPlistExists: true)
            == .uninstallerMissingButInstalled(cliFound: true))
    }

    /// Only when nothing root-owned is left at all. This is the one situation in
    /// which the app may say the service is gone, retract the login item, and
    /// invite the user to delete the bundle.
    @Test func nothingLeftIsTheOnlyAllGoneAnswer() {
        #expect(UninstallDecision.situation(uninstallerExists: false,
                                            cliFound: false,
                                            daemonPlistExists: false)
            == .nothingRootOwnedLeft)
    }

    /// Executed as root, so it is pinned exactly. `KEEP_CONFIG=1` is the
    /// uninstaller's own opt-out and has to reach it as an environment
    /// assignment, before `sh`.
    @Test func commandIsExactAndCarriesKeepConfig() {
        #expect(UninstallDecision.command(keepConfig: false)
            == "sudo sh /usr/local/share/dezhban/uninstall.sh")
        #expect(UninstallDecision.command(keepConfig: true)
            == "sudo KEEP_CONFIG=1 sh /usr/local/share/dezhban/uninstall.sh")
    }

    /// Nothing in the command comes from user input, so there is nothing to
    /// quote-escape — and this test is what notices if that ever stops being
    /// true, since the string is handed to Terminal and run as root.
    @Test func commandIsBuiltFromConstantsOnly() {
        for keep in [true, false] {
            let c = UninstallDecision.command(keepConfig: keep)
            #expect(c.hasSuffix(UninstallDecision.uninstallerPath))
            #expect(!c.contains("\""))
            #expect(!c.contains("'"))
            #expect(!c.contains("$"))
            #expect(!c.contains(";"))
        }
    }

    /// The remedy may only name a control the user actually has. Without the
    /// CLI, "Start the guard at boot" is disabled and every `dezhban` command is
    /// unrunnable, so neither may appear.
    @Test func remedyWithoutTheCLINamesNeitherToggleNorCommand() {
        let r = UninstallDecision.remedy(cliFound: false)
        #expect(!r.contains("Start the guard at boot"))
        #expect(!r.contains("dezhban token forget"))
        #expect(r.contains("Reinstall dezhban"))
    }

    @Test func remedyWithTheCLIOffersBoth() {
        let r = UninstallDecision.remedy(cliFound: true)
        #expect(r.contains("Start the guard at boot"))
        #expect(r.contains("sudo dezhban token forget"))
    }

    /// The plist path is the app's only evidence of a loaded daemon, and Go
    /// derives the same string from `svc.Name`. build-app.sh pins the two
    /// together; this pins the shape so a rename cannot quietly pass that pin.
    @Test func daemonPlistPathIsTheSystemDomainLocation() {
        #expect(UninstallDecision.daemonPlistPath == "/Library/LaunchDaemons/dezhban.plist")
    }
}
