import Foundation

/// What "Remove Dezhban…" should do about the half of the install only root can
/// remove, and what to say when it cannot do it.
///
/// Split from the filesystem probes and the alerts that feed it for the same
/// reason `FirstRunDecision` is: whether a file exists is bookkeeping, but which
/// of these situations a machine is in is a decision — and getting it wrong
/// means telling somebody their kill switch is gone while it is still cutting
/// their traffic. Both bugs this rule has had were branch selection, not
/// plumbing, so the branch selection is what is tested.
public enum UninstallDecision {
    /// Where the installer leaves the uninstaller matching the installed
    /// version. Both `scripts/install.sh` and the `.pkg` put it here, and
    /// `build-app.sh` fails the build if they stop agreeing.
    public static let uninstallerPath = "/usr/local/share/dezhban/uninstall.sh"

    /// The daemon's launchd job, as `internal/svc` installs it. Read only to
    /// answer "is dezhban still installed here?" — never written, never loaded
    /// from the app; service lifecycle is root's.
    public static let daemonPlistPath = "/Library/LaunchDaemons/dezhban.plist"

    /// The situation the app is in once it knows what is on disk.
    public enum Situation: Equatable {
        /// The uninstaller is there: hand off to it and quit.
        case handOff
        /// No uninstaller, but a CLI binary or the launchd job is still here, so
        /// dezhban is still installed and may well still be enforcing.
        case uninstallerMissingButInstalled(cliFound: Bool)
        /// No uninstaller and nothing root-owned either. This account's residue
        /// was the whole job.
        case nothingRootOwnedLeft
    }

    /// - Parameters:
    ///   - uninstallerExists: whether `uninstallerPath` is on disk.
    ///   - cliFound: whether the `dezhban` binary was found.
    ///   - daemonPlistExists: whether `daemonPlistPath` is on disk.
    ///
    /// `cliFound` and `daemonPlistExists` are OR-ed, deliberately: either
    /// artefact alone means the guard may still be enforcing, and the one answer
    /// this must never give wrongly is "it is all gone".
    ///
    /// A missing uninstaller does NOT imply a missing install. `scripts/install.sh`
    /// fetches it over the network and treats a failure as non-fatal — "install
    /// itself succeeded" — and `install-local.sh` never fetches it at all. So a
    /// fully armed, currently-enforcing Mac reaches the middle case, and reading
    /// the missing file as "already uninstalled" would wipe the user's keychain
    /// and preferences, retract the login item that gives them a menubar, and
    /// invite them to delete the app: a kill switch still enforcing with nothing
    /// left to see or control it by.
    public static func situation(uninstallerExists: Bool,
                                 cliFound: Bool,
                                 daemonPlistExists: Bool) -> Situation {
        if uninstallerExists { return .handOff }
        if cliFound || daemonPlistExists {
            return .uninstallerMissingButInstalled(cliFound: cliFound)
        }
        return .nothingRootOwnedLeft
    }

    /// The exact command that removes everything root owns. Shown to the user
    /// verbatim when Terminal cannot be opened, so it has to be copy-pasteable
    /// as printed — and it is executed as root, so it is assembled from
    /// constants only. Keep it that way: nothing here interpolates user input,
    /// which is why there is nothing to quote-escape.
    public static func command(keepConfig: Bool) -> String {
        (keepConfig ? "sudo KEEP_CONFIG=1 sh " : "sudo sh ") + uninstallerPath
    }

    /// How to take the guard down when the uninstaller is missing on a machine
    /// that still has one.
    ///
    /// Split on `cliFound` because the "Start the guard at boot" toggle shells
    /// out to the CLI and is disabled without it — and the OR above exists
    /// precisely so a machine with the launchd job but no binary reaches here,
    /// which `scripts/install-local.sh` produces routinely. Naming a greyed-out
    /// control as the remedy there would be the same failure as naming a file
    /// that is not on disk. The token line rides along for the same reason: it
    /// is a CLI command, so it may only be offered when there is a CLI.
    public static func remedy(cliFound: Bool) -> String {
        cliFound
            ? "To take the guard down without it, turn off \"Start the guard at boot\" in "
                + "Settings — that runs panic, stop and uninstall, in that order. Reinstalling "
                + "dezhban also restores the uninstaller.\n\n"
                + "Run `sudo dezhban token forget` too — dezhban still holds an enrollment for "
                + "the Touch ID key just removed."
            : "The dezhban command-line tool is gone from this Mac too, so Settings cannot take "
                + "the guard down either. Reinstall dezhban — that restores both the tool and "
                + "the uninstaller — then remove it again from here."
    }
}
