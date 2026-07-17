import AppKit

/// The Settings hub: one window for the tweaks that used to be scattered across
/// menu items (or reachable only by editing the config file) — startup behavior,
/// protection settings, and entry points to the VPN guard panel and the raw
/// config. Follows VPNConfigPanel's design: fields are seeded from
/// `dezhban config get` on every open (the on-disk config is the only source of
/// truth), applied through one batched `dezhban config set` (one validation, one
/// write, one admin prompt), with `config.Validate` staying the single authority.
///
/// Two kinds of controls, two behaviors, stated in the UI:
///   - Startup toggles act IMMEDIATELY (they are service/login-item actions, not
///     config values — there is nothing to batch or restart).
///   - Protection fields are staged and written by Apply, like VPNConfigPanel.
final class SettingsPanel: NSObject, NSWindowDelegate {
    static let shared = SettingsPanel()

    // Dotted keys from configFields (cmd/dezhban/config_cmd.go).
    private static let keyBlockedCountries = "blockedCountries"
    private static let keySwitchWindow = "vpn.switchWindow"
    private static let keyEndpointGrace = "vpn.endpointGrace"

    private var window: NSWindow!
    private var bootCheckbox: NSButton!
    private var loginCheckbox: NSButton!
    private var blockedCountriesField: NSTextField!
    private var switchWindowField: NSTextField!
    private var endpointGraceField: NSTextField!
    private var configPathLabel: NSTextField!
    private var applyButton: NSButton!
    private var statusLabel: NSTextField!

    private override init() {
        super.init()
        buildWindow()
    }

    /// Shows the panel and re-seeds everything from the live system state.
    func open() {
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        seed()
    }

    // MARK: - window construction

    private func buildWindow() {
        let win = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 560, height: 480),
            styleMask: [.titled, .closable, .miniaturizable],
            backing: .buffered, defer: false)
        win.title = "Dezhban Settings"
        win.isReleasedWhenClosed = false
        win.delegate = self
        win.center()

        // Startup — immediate actions, not staged config.
        bootCheckbox = NSButton(checkboxWithTitle: "Start protection at boot (install the system service)",
                                target: self, action: #selector(bootToggled))
        bootCheckbox.toolTip = "Installs dezhban as a launchd system daemon: enforcement starts at boot — "
            + "before any login — and survives restarts and crashes. Unchecking uninstalls the service "
            + "(rules are torn down first so nothing is left blocking)."
        loginCheckbox = NSButton(checkboxWithTitle: "Open this menubar app at login",
                                 target: self, action: #selector(loginToggled))
        loginCheckbox.toolTip = "Registers the app as a login item (System Settings → General → Login Items). "
            + "This is only the status display — protection itself is the system service above."

        // Protection — staged fields, written by Apply.
        blockedCountriesField = NSTextField()
        switchWindowField = NSTextField()
        endpointGraceField = NSTextField()
        for f in [blockedCountriesField, switchWindowField, endpointGraceField] {
            f?.translatesAutoresizingMaskIntoConstraints = false
            f?.widthAnchor.constraint(equalToConstant: 240).isActive = true
        }
        blockedCountriesField.toolTip = "Country codes that trigger a block, comma-separated (e.g. IR,CN)."
        switchWindowField.toolTip = "Default duration of a VPN switch window (e.g. 2m). Clamped to the configured maximum."
        endpointGraceField.toolTip = "How long a discovered VPN server stays reachable after its connection "
            + "disappears, so a dropped VPN can redial the same server (e.g. 15m)."

        let vpnButton = NSButton(title: "VPN Guard Configuration…", target: self, action: #selector(openVPNPanel))
        vpnButton.toolTip = "Tunnel interfaces, endpoints, autodetection — the full VPN guard panel."

        configPathLabel = NSTextField(labelWithString: "")
        configPathLabel.textColor = .secondaryLabelColor
        configPathLabel.font = NSFont.systemFont(ofSize: 11)
        configPathLabel.lineBreakMode = .byTruncatingMiddle
        let openConfigButton = NSButton(title: "Open Config File…", target: self, action: #selector(openConfigFile))

        statusLabel = NSTextField(labelWithString: "")
        statusLabel.textColor = .secondaryLabelColor
        statusLabel.font = NSFont.systemFont(ofSize: 11)
        statusLabel.lineBreakMode = .byTruncatingTail

        applyButton = NSButton(title: "Apply…", target: self, action: #selector(applyTapped))
        applyButton.keyEquivalent = "\r"
        applyButton.isEnabled = false
        let closeButton = NSButton(title: "Close", target: self, action: #selector(closeTapped))

        let form = NSStackView(views: [
            sectionLabel("Startup"),
            bootCheckbox,
            loginCheckbox,
            spacer(),
            sectionLabel("Protection"),
            labeledRow("Blocked countries (comma-sep):", blockedCountriesField),
            labeledRow("Switch window (e.g. 2m):", switchWindowField),
            labeledRow("Endpoint grace (e.g. 15m):", endpointGraceField),
            vpnButton,
            spacer(),
            sectionLabel("Config file"),
            configPathLabel,
            openConfigButton,
        ])
        form.orientation = .vertical
        form.alignment = .leading
        form.spacing = 10

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

        let content = NSView(frame: NSRect(x: 0, y: 0, width: 560, height: 480))
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

    private func sectionLabel(_ text: String) -> NSView {
        let l = NSTextField(labelWithString: text)
        l.font = NSFont.boldSystemFont(ofSize: 12)
        return l
    }

    private func spacer() -> NSView {
        let v = NSView()
        v.translatesAutoresizingMaskIntoConstraints = false
        v.heightAnchor.constraint(equalToConstant: 4).isActive = true
        return v
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

    /// Re-reads everything the panel shows: config fields via `config get`
    /// (short-circuiting on the first failure so an error string can never be
    /// written back as a value — same rule as VPNConfigPanel), service state via
    /// `status --json`, login-item state from SMAppService.
    private func seed() {
        statusLabel.stringValue = "Loading…"
        applyButton.isEnabled = false
        bootCheckbox.isEnabled = false
        loginCheckbox.state = LoginItem.isEnabled ? .on : .off
        blockedCountriesField.stringValue = ""
        switchWindowField.stringValue = ""
        endpointGraceField.stringValue = ""
        let cfgPath = DezhbanCLI.resolvedConfigPath()
        configPathLabel.stringValue = cfgPath
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let installed = DezhbanCLI.serviceInstalled()
            let keys = [Self.keyBlockedCountries, Self.keySwitchWindow, Self.keyEndpointGrace]
            let results = keys.map { (key: $0, result: DezhbanCLI.run(["config", "get", $0, "--config", cfgPath])) }
            DispatchQueue.main.async {
                guard let self = self else { return }
                self.bootCheckbox.state = installed ? .on : .off
                self.bootCheckbox.isEnabled = DezhbanCLI.binaryPath() != nil
                if let failed = results.first(where: { !$0.result.ok }) {
                    let detail = failed.result.output.trimmingCharacters(in: .whitespacesAndNewlines)
                    self.statusLabel.stringValue = "Failed to read \(failed.key): \(detail)"
                    return
                }
                let values = results.map { $0.result.output.trimmingCharacters(in: .whitespacesAndNewlines) }
                self.blockedCountriesField.stringValue = values[0]
                self.switchWindowField.stringValue = values[1]
                self.endpointGraceField.stringValue = values[2]
                self.statusLabel.stringValue = "Seeded from \(cfgPath)"
                self.applyButton.isEnabled = true
            }
        }
    }

    // MARK: - startup toggles (immediate)

    /// Install-and-start or tear-down-and-uninstall, mirroring the menu's
    /// install/uninstall sequences (one admin prompt each). The checkbox is
    /// reverted until the action's outcome is known — it reflects reality, not
    /// intent.
    @objc private func bootToggled() {
        let wantInstalled = bootCheckbox.state == .on
        bootCheckbox.state = wantInstalled ? .off : .on // revert; seed() sets the truth
        if !wantInstalled {
            let alert = NSAlert()
            alert.alertStyle = .warning
            alert.messageText = "Uninstall the dezhban service?"
            alert.informativeText = "Protection will stop and will no longer start at boot. "
                + "All dezhban firewall rules are removed first, so nothing is left blocking."
            alert.addButton(withTitle: "Uninstall")
            alert.addButton(withTitle: "Cancel")
            guard alert.runModal() == .alertFirstButtonReturn else { return }
        }
        bootCheckbox.isEnabled = false
        statusLabel.stringValue = wantInstalled ? "Installing service…" : "Uninstalling service…"
        let cfgPath = DezhbanCLI.resolvedConfigPath()
        // Same sequences as the menu items: install+start, or panic+stop+uninstall
        // (rules torn down BEFORE the unload so none can be left behind).
        let commands: [[String]] = wantInstalled
            ? [["install", "--config", cfgPath], ["start"]]
            : [["panic"], ["stop"], ["uninstall"]]
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = DezhbanCLI.runPrivileged(batch: commands)
            DispatchQueue.main.async {
                guard let self = self else { return }
                if !result.ok {
                    OutputPanel.shared.show(
                        title: wantInstalled ? "Install service — failed" : "Uninstall service — failed",
                        text: result.output.isEmpty ? "(no output)" : result.output)
                }
                self.seed()
            }
        }
    }

    @objc private func loginToggled() {
        let enabled = LoginItem.toggle()
        loginCheckbox.state = enabled ? .on : .off
        statusLabel.stringValue = enabled
            ? "Menubar app will open at login."
            : "Menubar app will not open at login."
    }

    // MARK: - apply (staged protection fields)

    @objc private func applyTapped() {
        let blocked = blockedCountriesField.stringValue.trimmingCharacters(in: .whitespaces)
        let switchWindow = switchWindowField.stringValue.trimmingCharacters(in: .whitespaces)
        let endpointGrace = endpointGraceField.stringValue.trimmingCharacters(in: .whitespaces)

        for (label, value) in [("Switch window", switchWindow), ("Endpoint grace", endpointGrace)] {
            guard VPNConfigPanel.looksLikeGoDuration(value) else {
                let alert = NSAlert()
                alert.alertStyle = .warning
                alert.messageText = "Invalid duration"
                alert.informativeText = "\(label) doesn't look like a Go duration string (e.g. \"30s\", \"5m\", \"1h30m\"): \"\(value)\""
                alert.runModal()
                return
            }
        }

        // One batched `config set` (single validation, write, and prompt), with
        // the restart decision made BEFORE elevating — VPNConfigPanel's flow.
        let restart = confirmRestart()
        let cfgPath = DezhbanCLI.resolvedConfigPath()
        let pairs = [
            "\(Self.keyBlockedCountries)=\(blocked)",
            "\(Self.keySwitchWindow)=\(switchWindow)",
            "\(Self.keyEndpointGrace)=\(endpointGrace)",
        ]
        var commands: [[String]] = [["config", "set"] + pairs + ["--config", cfgPath]]
        if restart {
            commands.append(["restart"])
        }

        applyButton.isEnabled = false
        statusLabel.stringValue = restart ? "Applying and restarting…" : "Applying…"
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = DezhbanCLI.runPrivileged(batch: commands)
            DispatchQueue.main.async {
                guard let self = self else { return }
                self.applyButton.isEnabled = true
                if !result.ok {
                    self.statusLabel.stringValue = "Rejected — see output."
                    OutputPanel.shared.show(title: "Settings — not applied", text: result.output)
                    return
                }
                self.statusLabel.stringValue = restart
                    ? "Applied and restarted."
                    : "Config saved; restart later to apply."
            }
        }
    }

    private func confirmRestart() -> Bool {
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = "Restart dezhban to apply this change?"
        alert.informativeText = "A config change only takes effect when dezhban restarts. Network filtering is briefly disabled while it does (usually under a few seconds). Choosing “Save only” writes the config now and leaves the running daemon on its old settings."
        alert.addButton(withTitle: "Save and Restart")
        alert.addButton(withTitle: "Save Only")
        return alert.runModal() == .alertFirstButtonReturn
    }

    // MARK: - entry points

    @objc private func openVPNPanel() {
        VPNConfigPanel.shared.open()
    }

    @objc private func openConfigFile() {
        NSWorkspace.shared.open(URL(fileURLWithPath: DezhbanCLI.resolvedConfigPath()))
    }

    @objc private func closeTapped() {
        window.performClose(nil)
    }

    func windowWillClose(_ notification: Notification) {
        // Nothing to tear down; in-flight background work reports via OutputPanel.
    }
}
