import AppKit

/// The in-app VPN guard config panel (Phase 11). Seeds every field from
/// `dezhban config get <key>` on every open — the raw on-disk config is the
/// source of truth, never cached in a second full-schema Swift model (see the
/// phase's "Raw-file seeding" design decision) — and applies changes through
/// `dezhban config set <key> <value>` per field, the same dotted-key accessor
/// layer (`configFields` in cmd/dezhban/config_cmd.go) the CLI itself uses.
/// No new Go command: validation stays single-sourced in `config.Validate`.
final class VPNConfigPanel: NSObject, NSWindowDelegate {
    static let shared = VPNConfigPanel()

    // The exact vpn.* keys already in configFields (cmd/dezhban/config_cmd.go)
    // — the scope this phase closes, not a general config editor.
    private static let keyEnabled = "vpn.enabled"
    private static let keyTunnelInterfaces = "vpn.tunnelInterfaces"
    private static let keyEndpoints = "vpn.endpoints"
    private static let keyAutodetect = "vpn.autodetect"
    private static let keyAutoDiscoverEndpoints = "vpn.autoDiscoverEndpoints"
    private static let keyEndpointRefresh = "vpn.endpointRefresh"
    private static let keyTunnelWatch = "vpn.tunnelWatch"

    private var window: NSWindow!
    private var enabledCheckbox: NSButton!
    private var tunnelInterfacesField: NSTextField!
    private var endpointsField: NSTextField!
    private var autodetectCheckbox: NSButton!
    private var autoDiscoverCheckbox: NSButton!
    private var endpointRefreshField: NSTextField!
    private var tunnelWatchField: NSTextField!
    private var applyButton: NSButton!
    private var statusLabel: NSTextField!

    private override init() {
        super.init()
        buildWindow()
    }

    /// Shows the panel and re-seeds every field from the live config. Called
    /// every time the menu's "VPN guard mode" item is chosen — never reuses a
    /// stale in-memory copy from a prior open.
    func open() {
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        seedFields()
    }

    // MARK: - window construction

    private func buildWindow() {
        let win = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 520, height: 400),
            styleMask: [.titled, .closable, .miniaturizable],
            backing: .buffered, defer: false)
        win.title = "VPN Guard Configuration"
        win.isReleasedWhenClosed = false
        win.delegate = self
        win.center()

        enabledCheckbox = NSButton(checkboxWithTitle: "Enable VPN guard (vpn.enabled)", target: nil, action: nil)
        autodetectCheckbox = NSButton(checkboxWithTitle: "Autodetect tunnel interface (vpn.autodetect)", target: nil, action: nil)
        autoDiscoverCheckbox = NSButton(checkboxWithTitle: "Auto-discover endpoints (vpn.autoDiscoverEndpoints)", target: nil, action: nil)

        tunnelInterfacesField = NSTextField()
        endpointsField = NSTextField()
        endpointRefreshField = NSTextField()
        tunnelWatchField = NSTextField()
        for f in [tunnelInterfacesField, endpointsField, endpointRefreshField, tunnelWatchField] {
            f?.translatesAutoresizingMaskIntoConstraints = false
            f?.widthAnchor.constraint(equalToConstant: 240).isActive = true
        }

        statusLabel = NSTextField(labelWithString: "")
        statusLabel.textColor = .secondaryLabelColor
        statusLabel.font = NSFont.systemFont(ofSize: 11)
        statusLabel.lineBreakMode = .byTruncatingTail

        applyButton = NSButton(title: "Apply…", target: self, action: #selector(applyTapped))
        applyButton.keyEquivalent = "\r"
        applyButton.isEnabled = false
        let closeButton = NSButton(title: "Close", target: self, action: #selector(closeTapped))

        let form = NSStackView(views: [
            enabledCheckbox,
            labeledRow("Tunnel interfaces (comma-sep):", tunnelInterfacesField),
            labeledRow("Endpoints (comma-sep):", endpointsField),
            autodetectCheckbox,
            autoDiscoverCheckbox,
            labeledRow("Endpoint refresh (e.g. 30s):", endpointRefreshField),
            labeledRow("Tunnel watch (e.g. 5s):", tunnelWatchField),
        ])
        form.orientation = .vertical
        form.alignment = .leading
        form.spacing = 12

        let buttonRow = NSStackView(views: [closeButton, applyButton])
        buttonRow.orientation = .horizontal
        buttonRow.spacing = 8

        let bottomRow = NSStackView(views: [statusLabel, NSView(), buttonRow])
        bottomRow.orientation = .horizontal
        bottomRow.spacing = 8
        statusLabel.setContentHuggingPriority(.defaultLow, for: .horizontal)

        let root = NSStackView(views: [form, bottomRow])
        root.orientation = .vertical
        root.spacing = 20
        root.edgeInsets = NSEdgeInsets(top: 20, left: 20, bottom: 16, right: 20)
        root.translatesAutoresizingMaskIntoConstraints = false

        let content = NSView(frame: NSRect(x: 0, y: 0, width: 520, height: 400))
        content.addSubview(root)
        NSLayoutConstraint.activate([
            root.topAnchor.constraint(equalTo: content.topAnchor),
            root.leadingAnchor.constraint(equalTo: content.leadingAnchor),
            root.trailingAnchor.constraint(equalTo: content.trailingAnchor),
            root.bottomAnchor.constraint(lessThanOrEqualTo: content.bottomAnchor),
        ])
        win.contentView = content
        self.window = win
    }

    private func labeledRow(_ label: String, _ control: NSView) -> NSView {
        let l = NSTextField(labelWithString: label)
        l.alignment = .left
        let row = NSStackView(views: [l, control])
        row.orientation = .horizontal
        row.spacing = 8
        return row
    }

    // MARK: - seeding

    /// One `config get <key>` per rendered field, every time the panel opens
    /// — the raw file is truth, never a cached/second full-schema mirror.
    private func seedFields() {
        statusLabel.stringValue = "Loading current config…"
        applyButton.isEnabled = false
        // Clear any values left from a previous open so a failed seed can't leave
        // stale (and misleading) data on screen while Apply is disabled.
        enabledCheckbox.state = .off
        autodetectCheckbox.state = .off
        autoDiscoverCheckbox.state = .off
        tunnelInterfacesField.stringValue = ""
        endpointsField.stringValue = ""
        endpointRefreshField.stringValue = ""
        tunnelWatchField.stringValue = ""
        DispatchQueue.global(qos: .userInitiated).async {
            let keys = [
                Self.keyEnabled, Self.keyTunnelInterfaces, Self.keyEndpoints,
                Self.keyAutodetect, Self.keyAutoDiscoverEndpoints,
                Self.keyEndpointRefresh, Self.keyTunnelWatch,
            ]
            // Capture each read's success. If any `config get` fails, the raw
            // output is an error message, not a value — seeding it into the
            // fields would let Apply write that error string back via
            // `config set`. Short-circuit on the first failure, leave Apply
            // disabled, and surface the error instead.
            // Read through the same resolved --config path Apply writes/validates
            // with, so a nonstandard path ($DEZHBAN_CONFIG, etc.) can't seed from
            // one file and then apply to another.
            let cfgPath = DezhbanCLI.resolvedConfigPath()
            let results = keys.map { (key: $0, result: DezhbanCLI.run(["config", "get", $0, "--config", cfgPath])) }
            if let failed = results.first(where: { !$0.result.ok }) {
                DispatchQueue.main.async { [weak self] in
                    guard let self = self else { return }
                    let detail = failed.result.output.trimmingCharacters(in: .whitespacesAndNewlines)
                    self.statusLabel.stringValue = "Failed to read \(failed.key): \(detail)"
                    self.applyButton.isEnabled = false
                }
                return
            }
            let values = results.map { $0.result.output.trimmingCharacters(in: .whitespacesAndNewlines) }
            DispatchQueue.main.async { [weak self] in
                guard let self = self else { return }
                self.enabledCheckbox.state = (values[0] == "true") ? .on : .off
                self.tunnelInterfacesField.stringValue = values[1]
                self.endpointsField.stringValue = values[2]
                self.autodetectCheckbox.state = (values[3] == "true") ? .on : .off
                self.autoDiscoverCheckbox.state = (values[4] == "true") ? .on : .off
                self.endpointRefreshField.stringValue = values[5]
                self.tunnelWatchField.stringValue = values[6]
                self.statusLabel.stringValue = "Seeded from \(cfgPath)"
                self.applyButton.isEnabled = true
            }
        }
    }

    // MARK: - apply

    @objc private func closeTapped() {
        window.performClose(nil)
    }

    @objc private func applyTapped() {
        let enabled = enabledCheckbox.state == .on
        let tunnelInterfaces = tunnelInterfacesField.stringValue
        let endpoints = endpointsField.stringValue
        let autodetect = autodetectCheckbox.state == .on
        let autoDiscover = autoDiscoverCheckbox.state == .on
        let endpointRefresh = endpointRefreshField.stringValue.trimmingCharacters(in: .whitespaces)
        let tunnelWatch = tunnelWatchField.stringValue.trimmingCharacters(in: .whitespaces)

        // Superficial client-side check only ("looks like a Go duration
        // string") — `config set`'s setDuration (time.ParseDuration) remains
        // the authority. This just avoids burning a privileged round trip on
        // obviously-wrong input.
        for (label, value) in [("Endpoint refresh", endpointRefresh), ("Tunnel watch", tunnelWatch)] {
            guard Self.looksLikeGoDuration(value) else {
                showBlockingAlert(
                    "Invalid duration",
                    "\(label) doesn't look like a Go duration string (e.g. \"30s\", \"5m\", \"1h30m\"): \"\(value)\"")
                return
            }
        }

        // Write order matters: `config set` validates the WHOLE config on every
        // write (internal/config.Save → Marshal → Validate), not just the field
        // being set. So when turning the guard ON, every other field is written
        // first and vpn.enabled last — the on-disk config is never briefly
        // "enabled=true" with stale/empty tunnels or endpoints in between. When
        // turning it OFF, vpn.enabled is written first so the cross-field
        // invariant (which only applies while enabled) is never checked against
        // fields not yet updated.
        var sets: [(key: String, value: String)] = [
            (Self.keyTunnelInterfaces, tunnelInterfaces),
            (Self.keyEndpoints, endpoints),
            (Self.keyAutodetect, autodetect ? "true" : "false"),
            (Self.keyAutoDiscoverEndpoints, autoDiscover ? "true" : "false"),
            (Self.keyEndpointRefresh, endpointRefresh),
            (Self.keyTunnelWatch, tunnelWatch),
        ]
        if enabled {
            sets.append((Self.keyEnabled, "true"))
        } else {
            sets.insert((Self.keyEnabled, "false"), at: 0)
        }

        applyButton.isEnabled = false
        statusLabel.stringValue = "Applying…"
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self = self else { return }
            // Resolve the target path once and pass --config to every write and
            // the final validate, so all three provably act on the same file
            // (rather than trusting each subcommand to re-resolve identically).
            let cfgPath = DezhbanCLI.resolvedConfigPath()
            var log = ""
            for (key, value) in sets {
                let result = DezhbanCLI.runPrivileged(["config", "set", key, value, "--config", cfgPath])
                // Quote value/cfgPath (via String(reflecting:)) so the transcript
                // is unambiguous and copy/paste-runnable when they contain spaces.
                log += "$ dezhban config set \(key) \(String(reflecting: value)) --config \(String(reflecting: cfgPath))\n\(result.output)\n\n"
                if !result.ok {
                    DispatchQueue.main.async {
                        self.finishWithoutRestart(log: log, message: "Rejected: \(key) failed to set — no restart attempted.")
                    }
                    return
                }
            }

            // Belt-and-suspenders: each `config set` above already validates the
            // full config on write, but re-validate the file itself once more
            // before ever offering a restart.
            let validate = DezhbanCLI.run(["validate", "--config", cfgPath])
            log += "$ dezhban validate --config \(String(reflecting: cfgPath))\n\(validate.output)\n\n"
            guard validate.ok else {
                DispatchQueue.main.async {
                    self.finishWithoutRestart(log: log, message: "Config written but failed final validation — daemon not restarted.")
                }
                return
            }

            DispatchQueue.main.async {
                self.confirmRestart(log: log)
            }
        }
    }

    private func finishWithoutRestart(log: String, message: String) {
        applyButton.isEnabled = true
        statusLabel.stringValue = message
        OutputPanel.shared.show(title: "VPN config — apply", text: log + "\n" + message)
    }

    /// The restart-window decision made explicit: no atomic reload exists
    /// (kardianos/service has no SIGHUP-style reconfigure, and Cleanup/panic
    /// deliberately shares the same rules-come-down path as `stop`), so this
    /// is disclosed plainly rather than papered over as seamless.
    private func confirmRestart(log: String) {
        // Apply stays disabled (set in applyTapped) through the restart so a
        // second config-write/restart can't be kicked off concurrently. It is
        // re-enabled only when we stop here (cancel) or when the restart finishes.
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = "Restart dezhban to apply this change?"
        alert.informativeText = "Applying this change restarts dezhban. Network filtering is briefly disabled while it restarts (usually under a few seconds). Continue?"
        alert.addButton(withTitle: "Restart")
        alert.addButton(withTitle: "Cancel")
        guard alert.runModal() == .alertFirstButtonReturn else {
            applyButton.isEnabled = true
            statusLabel.stringValue = "Config saved; restart later to apply."
            OutputPanel.shared.show(title: "VPN config — saved (not restarted)", text: log)
            return
        }
        performRestart(log: log)
    }

    private func performRestart(log: String) {
        statusLabel.stringValue = "Restarting…"
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self = self else { return }
            var fullLog = log
            // No --config on stop/start: they act on the already-installed
            // service unit, whose config path was baked in at install time
            // (cmdService only reads --config for `install`). The service
            // manager ignores a --config passed to start/stop, so the restarted
            // daemon reloads its install-time path — which the GUI installs with
            // resolvedConfigPath(), the same path this panel just wrote to.
            let stopResult = DezhbanCLI.runPrivileged(["stop"])
            fullLog += "$ dezhban stop\n\(stopResult.output)\n\n"
            let startResult = DezhbanCLI.runPrivileged(["start"])
            fullLog += "$ dezhban start\n\(startResult.output)\n\n"

            guard stopResult.ok, startResult.ok else {
                DispatchQueue.main.async {
                    self.applyButton.isEnabled = true
                    self.statusLabel.stringValue = "Restart failed — see output."
                    OutputPanel.shared.show(title: "VPN config — restart failed", text: fullLog)
                }
                return
            }

            // Poll status --json (bounded) until a posture is published rather
            // than assuming success — a config that passed validate should
            // start, but a service-manager-level failure (e.g. launchd
            // rejecting the plist) is still possible and must not be swallowed.
            var reportedPosture: String?
            for _ in 0..<10 {
                Thread.sleep(forTimeInterval: 0.5)
                if let posture = DezhbanCLI.reportedPosture() {
                    reportedPosture = posture
                    break
                }
            }
            DispatchQueue.main.async {
                self.applyButton.isEnabled = true
                if let posture = reportedPosture {
                    self.statusLabel.stringValue = "Restarted — posture: \(posture)."
                    OutputPanel.shared.show(title: "VPN config — restarted", text: fullLog + "resolved posture: \(posture)\n")
                } else {
                    self.statusLabel.stringValue = "Restart did not report a posture — check status."
                    OutputPanel.shared.show(title: "VPN config — restart incomplete", text: fullLog + "no posture reported within 5s of polling `status --json`\n")
                }
            }
        }
    }

    private func showBlockingAlert(_ title: String, _ message: String) {
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = title
        alert.informativeText = message
        alert.runModal()
    }

    /// Superficial "looks like a Go duration" check: an optional sign, then
    /// either a bare "0" or one or more number(.number)?unit chunks, e.g. "30s",
    /// "5m", "1h30m", "500ms", "-1.5h", "0". Not a full parser — time.ParseDuration
    /// (via `config set`) remains the authority, so this errs permissive (it
    /// accepts everything ParseDuration does) and only exists to catch obviously
    /// wrong input before spending a privileged round trip.
    private static func looksLikeGoDuration(_ s: String) -> Bool {
        guard !s.isEmpty else { return false }
        // Mirror ParseDuration: optional [-+], the special bare "0", or repeated
        // chunks of (number + unit). Each number needs at least one digit (before
        // or after the dot) so a bare unit like "s"/"ms" is rejected. Units: ns,
        // us/µs/μs, ms, s, m, h.
        let pattern = #"^[-+]?(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$"#
        return s.range(of: pattern, options: .regularExpression) != nil
    }

    func windowWillClose(_ notification: Notification) {
        // No running process to tear down here (unlike OutputPanel's log
        // stream) — closing mid-apply just abandons the in-flight sequence's
        // UI feedback; the background work still runs to completion and
        // reports through the shared OutputPanel.
    }
}
