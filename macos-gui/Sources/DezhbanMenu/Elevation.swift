import Foundation
import Security

/// Runs privileged commands through **Authorization Services**, which is what gets us
/// Touch ID.
///
/// The old path — `NSAppleScript`'s `do shell script … with administrator privileges` —
/// puts up the legacy "Type your password to allow this" dialog. That dialog has never
/// supported biometrics, and no amount of coaxing will make it. Authorization Services
/// is the API behind the System Settings padlock and the pkg installer, and its
/// SecurityAgent prompt offers **Touch ID or password** on any Mac that has it.
///
/// It also *caches*. `AuthorizationCopyRights` extends the right onto a long-lived
/// `AuthorizationRef` we keep for the life of the app, and the system's admin right has
/// a grace period — so a second privileged action a moment later usually needs no
/// authentication at all. Authenticate once, act several times.
///
/// Falls back to the AppleScript path (see DezhbanCLI.runPrivileged) whenever any of
/// this is unavailable, so the app never loses the ability to elevate.
enum Elevation {
    /// Marker the shell appends so we can recover the command's exit status.
    /// `AuthorizationExecuteWithPrivileges` hands back a pipe but NOT the child's exit
    /// code (and no pid to wait on), so the script has to report it in-band. Without
    /// this every failure would look like a success — including a daemon refusal, which
    /// must never be mistaken for one.
    static let rcMarker = "__DEZHBAN_RC__"

    private static let lock = NSLock()
    private static var authRef: AuthorizationRef?

    /// True once the user has authenticated at least once in this app session, i.e. the
    /// next privileged action will very likely be silent. Used only for menu hints.
    static var isPreAuthorized: Bool {
        lock.lock()
        defer { lock.unlock() }
        return authRef != nil
    }

    /// `AuthorizationExecuteWithPrivileges`, resolved at runtime.
    ///
    /// It is deprecated (since 10.7) and therefore not exposed to Swift, but it is still
    /// present and still the only way to run a command as root from an `AuthorizationRef`
    /// without shipping an `SMAppService` helper — which would mean a permanently
    /// installed root XPC service, a great deal more attack surface than this tool wants
    /// for what amounts to "run `dezhban start` occasionally". Resolving it by symbol
    /// rather than linking it means that if a future macOS finally removes it, we get nil
    /// and fall back cleanly instead of failing to launch.
    private typealias ExecWithPrivileges = @convention(c) (
        AuthorizationRef,
        UnsafePointer<CChar>,
        AuthorizationFlags,
        UnsafePointer<UnsafeMutablePointer<CChar>?>,
        UnsafeMutablePointer<UnsafeMutablePointer<FILE>?>?
    ) -> OSStatus

    private static let execWithPrivileges: ExecWithPrivileges? = {
        guard let handle = dlopen("/System/Library/Frameworks/Security.framework/Security", RTLD_LAZY),
              let sym = dlsym(handle, "AuthorizationExecuteWithPrivileges")
        else { return nil }
        return unsafeBitCast(sym, to: ExecWithPrivileges.self)
    }()

    /// Whether this elevation path is usable at all. When false, callers use AppleScript.
    static var isAvailable: Bool { execWithPrivileges != nil }

    /// Acquires (once) an AuthorizationRef carrying the admin right, prompting with
    /// Touch ID or password as needed. Subsequent calls reuse the same ref, which is what
    /// makes repeat actions silent within the system's grace period.
    private static func authorizedRef() -> AuthorizationRef? {
        lock.lock()
        defer { lock.unlock() }

        if authRef == nil {
            var ref: AuthorizationRef?
            guard AuthorizationCreate(nil, nil, [], &ref) == errAuthorizationSuccess, ref != nil else {
                return nil
            }
            authRef = ref
        }
        guard let ref = authRef else { return nil }

        // kAuthorizationRightExecute is "system.privilege.admin" — the same right the
        // padlock asks for, and the one whose prompt offers Touch ID. The name must
        // outlive the call, hence the strdup rather than a scoped withCString.
        let name = strdup(kAuthorizationRightExecute)
        defer { free(name) }
        var item = AuthorizationItem(name: name!, valueLength: 0, value: nil, flags: 0)

        return withUnsafeMutablePointer(to: &item) { itemPtr -> AuthorizationRef? in
            var rights = AuthorizationRights(count: 1, items: itemPtr)
            // preAuthorize + extendRights: authenticate NOW and keep the right on the ref,
            // so the execute below doesn't put up a second prompt of its own.
            let flags: AuthorizationFlags = [.interactionAllowed, .extendRights, .preAuthorize]
            let status = AuthorizationCopyRights(ref, &rights, nil, flags, nil)
            if status != errAuthorizationSuccess {
                // Cancelled or denied. Drop the ref so the next attempt prompts again
                // rather than silently reusing a ref that carries no rights.
                if status == errAuthorizationCanceled {
                    invalidate()
                }
                return nil
            }
            return ref
        }
    }

    /// Forgets the cached authorization, so the next privileged action re-authenticates.
    static func invalidate() {
        lock.lock()
        defer { lock.unlock() }
        if let ref = authRef {
            AuthorizationFree(ref, [])
        }
        authRef = nil
    }

    /// Runs `capture` as root. `capture` must leave the command's combined output in
    /// `$out` and its status in `$?` — the same contract DezhbanCLI's AppleScript path
    /// uses, so the two elevation paths are interchangeable.
    ///
    /// Returns nil when this path is unusable at all (no symbol, no authorization), which
    /// tells the caller to fall back rather than treating it as a command failure — a
    /// cancelled prompt and a broken API must not look the same.
    static func run(shell capture: String) -> CommandResult? {
        guard let exec = execWithPrivileges, let ref = authorizedRef() else { return nil }

        // Emit the captured output, then the status in-band (see rcMarker).
        let script = "\(capture); rc=$?; printf '%s' \"$out\"; printf '\\n\(rcMarker)%d' \"$rc\""

        var argv: [UnsafeMutablePointer<CChar>?] = [strdup("-c"), strdup(script), nil]
        defer { for a in argv { free(a) } }

        var pipe: UnsafeMutablePointer<FILE>?
        let status: OSStatus = "/bin/sh".withCString { tool in
            argv.withUnsafeMutableBufferPointer { buf in
                exec(ref, tool, [], buf.baseAddress!, &pipe)
            }
        }
        guard status == errAuthorizationSuccess else { return nil }

        var data = Data()
        if let p = pipe {
            let handle = FileHandle(fileDescriptor: fileno(p), closeOnDealloc: false)
            data = handle.readDataToEndOfFile()
            fclose(p)
        }
        let raw = String(decoding: data, as: UTF8.self)

        // Split the in-band status off the tail of the output.
        guard let markerRange = raw.range(of: rcMarker, options: .backwards) else {
            // No marker: the shell died before it could report. Treat as a failure with
            // whatever it managed to print, rather than silently passing.
            return CommandResult(ok: false, output: raw, status: 1)
        }
        let output = String(raw[raw.startIndex..<markerRange.lowerBound])
        let rc = Int32(raw[markerRange.upperBound...].trimmingCharacters(in: .whitespacesAndNewlines)) ?? 1
        return CommandResult(
            ok: rc == 0,
            output: output.trimmingCharacters(in: .newlines),
            status: rc)
    }
}
