import SwiftUI

/// The window's primary pane: a live status hero, the daily controls, and the
/// panic escape hatch. Degraded states (CLI missing / service not installed /
/// daemon stopped) each get a guided layout with the one relevant action inline,
/// instead of a wall of disabled buttons.
struct OverviewView: View {
    @EnvironmentObject var state: AppState
    @State private var busy = false

    var body: some View {
        Group {
            if !state.cliFound {
                cliMissing
            } else if state.isLive, let s = state.snapshot {
                live(s)
            } else if !state.serviceIsInstalled {
                notInstalled
            } else {
                stopped
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .navigationTitle("Overview")
    }

    // MARK: - live

    private func live(_ s: Snapshot) -> some View {
        let icon = PostureUI.iconFor(s)
        return ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                hero(state: icon.state, symbol: icon.symbol, title: icon.help.capitalized)

                if let e = s.enforcementErr, !e.isEmpty {
                    banner("Enforcement failed: \(e)", color: .orange)
                }
                if let sw = s.switch, sw.open {
                    // The window relaxes egress — the real IP may be exposed. Keep it
                    // loud, with the same rounded-down countdown as the menubar.
                    banner(sw.isAutoRedial
                           ? "VPN dropped — redial window open, redial now (closes in \(PostureUI.mmss(sw.until.timeIntervalSince(state.now))))"
                           : "Switch window OPEN — closes in \(PostureUI.mmss(sw.until.timeIntervalSince(state.now)))",
                           color: .orange)
                }

                detailsGrid(s)

                Divider()

                actionButtons(s)

                Spacer(minLength: 12)

                panicRow
            }
            .padding(20)
        }
    }

    /// One sentence saying what is true right now and what happens next. This
    /// replaces the old mode label ("VPN guard mode" / "Legacy country-blocklist
    /// mode"), which named a distinction that no longer exists — and which told a
    /// user nothing about whether they were actually protected.
    ///
    /// STANDBY is the one that must not be soft-pedalled: nothing is blocked, so
    /// the copy says so outright rather than implying the guard is watching.
    static func postureBlurb(_ s: Snapshot) -> String {
        switch s.posture {
        case "standby":
            return "Guard off — standby. Nothing is being blocked. Connect your VPN and dezhban arms itself."
        case "guard":
            if PostureUI.guardHoldsDownedTunnel(s) {
                return "Guard active, but no tunnel is up — all egress is cut until your VPN redials."
            }
            return "Guard active. Traffic leaves only through your VPN tunnel."
        case "full-block":
            let cc = s.countryCode.map { " (\($0))" } ?? ""
            return "Full block. Your VPN is exiting through a country you've blocked\(cc). Everything is cut until it moves."
        case "switch-window":
            if s.switch?.isPause == true {
                return "Paused at your request. You are using your real IP, and the guard re-arms itself when the pause ends."
            }
            let auto = s.switch?.isAutoRedial ?? false
            return auto
                ? "Your VPN dropped. The guard is relaxed while it redials — your real IP is exposed until it closes."
                : "Guard relaxed so a new VPN can connect — your real IP is exposed until it closes."
        case "stopped":
            return "dezhban is not running. Nothing is being blocked."
        default:
            return "Posture: \(s.posture)"
        }
    }

    private func hero(state iconState: String, symbol: String, title: String) -> some View {
        HStack(spacing: 16) {
            // The bundled dock-size brand bitmap when available (color IS the
            // state), SF Symbol fallback for a bare `swift run` binary.
            if let img = PostureUI.dockIcon(iconState) {
                Image(nsImage: img)
                    .resizable()
                    .aspectRatio(contentMode: .fit)
                    .frame(width: 64, height: 64)
            } else {
                Image(systemName: symbol)
                    .font(.system(size: 44))
                    .foregroundStyle(PostureUI.color(for: iconState))
                    .frame(width: 64, height: 64)
            }
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.title2.weight(.semibold))
                if let s = state.snapshot {
                    Text(Self.postureBlurb(s))
                        .foregroundStyle(.secondary)
                }
            }
        }
    }

    private func banner(_ text: String, color: Color) -> some View {
        Label(text, systemImage: "exclamationmark.triangle.fill")
            .padding(10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(color.opacity(0.15), in: RoundedRectangle(cornerRadius: 8))
            .foregroundStyle(color)
    }

    private func detailsGrid(_ s: Snapshot) -> some View {
        Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 6) {
            if let ip = s.ip, !ip.isEmpty {
                let cc = s.countryCode ?? "??"
                let prov = s.provider.map { " via \($0)" } ?? ""
                row("Public IP", "\(ip) (\(cc)\(prov))")
            } else if let err = s.lookupErr, !err.isEmpty {
                // Only genuine failures reach lookupErr: a tunnel was up, so
                // there was an exit to measure, and measuring it did not work.
                row("Last lookup", "failed: \(err)")
            } else if let why = s.exitUnknown, !why.isEmpty {
                // Expected, not a fault — phrased as a state rather than an
                // error, because reporting it as one is what made the geo
                // providers look broken during every switch window.
                row("Exit country", "unknown — \(why)")
            }
            if let t = s.tunnels?.first {
                row("Tunnel", "\(t.up ? "up" : "down")\(t.detail.map { " (\($0))" } ?? "")")
            }
            if let eps = s.endpoints, !eps.isEmpty {
                row("Endpoints", eps.joined(separator: ", "))
            }
            if let p = s.activeProfile, !p.isEmpty {
                row("VPN profile", p)
            }
            if let bc = s.blockedCountries, !bc.isEmpty {
                row("Blocking", bc.joined(separator: ", "))
            }
            row("Updated", PostureUI.agoString(state.now.timeIntervalSince(s.time)))
        }
    }

    private func row(_ label: String, _ value: String) -> some View {
        GridRow {
            Text(label).foregroundStyle(.secondary).gridColumnAlignment(.trailing)
            Text(value).textSelection(.enabled)
        }
    }

    // MARK: - actions

    private func actionButtons(_ s: Snapshot) -> some View {
        let blocked = s.blocked
        let guardHolds = PostureUI.guardHoldsDownedTunnel(s)
        return HStack(spacing: 10) {
            Button("Block now") { AppActions.routine(["block"], "block") }
                .disabled(blocked)
                .help(routineHint("Cuts all egress and holds it until you unblock."))
            Button("Unblock") { AppActions.routine(["unblock"], "unblock") }
                .disabled(!(blocked || guardHolds))
                .help(routineHint("Releases a manual block and resumes monitoring."))
            if let sw = s.switch, sw.open {
                Button("\(sw.isAutoRedial ? "Cancel redial window" : "Cancel VPN switch") (\(PostureUI.mmss(sw.until.timeIntervalSince(state.now))) left)") {
                    AppActions.routine(["switch", "--cancel"], "cancel the switch window")
                }
                .help(routineHint("Closes the window and restores the guard."))
            } else {
                Button("Switching VPN…") { AppActions.routine(["switch", "--no-wait"], "open a switch window") }
                    .help(routineHint("Briefly relaxes the guard so a new VPN can connect."))
            }
            Spacer()
            Button("Stop kill switch") { AppActions.privileged(["stop"], "stop the kill switch") }
                .help("Stops the daemon. Asks for your password — a daemon can’t stop itself.")
        }
    }

    /// Appends the password expectation to a routine action's hint, so the button
    /// tells the truth about what the click will cost before it costs it.
    private func routineHint(_ what: String) -> String {
        state.controlIsReachable
            ? "\(what) No password needed — the running daemon handles it."
            : "\(what) Will ask for your password (the daemon isn’t reachable)."
    }

    private var panicRow: some View {
        HStack {
            Button(role: .destructive) {
                guard AppActions.confirmPanic() else { return }
                AppActions.capturedPrivileged(["panic"]) { result in
                    state.showInLogs(title: "dezhban — panic", text: result.output)
                }
            } label: {
                Label("Panic — force unblock…", systemImage: "exclamationmark.octagon.fill")
            }
            .tint(.red)
            Text("Removes every dezhban firewall rule, even with no daemon running.")
                .font(.callout)
                .foregroundStyle(.secondary)
        }
    }

    // MARK: - degraded states

    private var cliMissing: some View {
        guided(
            symbol: "questionmark.circle",
            title: "dezhban CLI not found",
            message: "The dezhban command-line tool isn’t installed in a trusted location "
                + "(/usr/local/bin or /opt/homebrew/bin). Install it — e.g. via the .pkg "
                + "installer or `task build:all` — then come back here."
        ) { EmptyView() }
    }

    private var notInstalled: some View {
        guided(
            symbol: "shield",
            title: "Not protecting",
            message: "The dezhban system service is not installed, so nothing is enforced — "
                + "at boot or now. Installing it starts protection immediately and at every boot."
        ) {
            Button("Install service…") {
                busy = true
                AppActions.capturedSequence(AppActions.installCommands) { result in
                    busy = false
                    state.showInLogs(title: "dezhban — install service", text: result.output)
                }
            }
            .buttonStyle(.borderedProminent)
            .disabled(busy)
        }
    }

    private var stopped: some View {
        guided(
            symbol: "shield",
            title: "Protection stopped",
            message: "The dezhban service is installed but the daemon isn’t running "
                + "(or hasn’t reported recently). Egress is not being watched."
        ) {
            Button("Start kill switch") {
                AppActions.privileged(["start"], "start the kill switch")
            }
            .buttonStyle(.borderedProminent)
        }
    }

    private func guided<Content: View>(symbol: String, title: String, message: String,
                                       @ViewBuilder action: () -> Content) -> some View {
        VStack(spacing: 12) {
            if let img = PostureUI.dockIcon("off") {
                Image(nsImage: img)
                    .resizable()
                    .aspectRatio(contentMode: .fit)
                    .frame(width: 72, height: 72)
            } else {
                Image(systemName: symbol)
                    .font(.system(size: 48))
                    .foregroundStyle(.secondary)
            }
            Text(title).font(.title2.weight(.semibold))
            Text(message)
                .multilineTextAlignment(.center)
                .foregroundStyle(.secondary)
                .frame(maxWidth: 420)
            action()
                .padding(.top, 4)
            // Panic stays reachable even from a degraded state — stale rules with
            // no daemon are exactly when the escape hatch matters.
            Button("Panic — force unblock…") {
                guard AppActions.confirmPanic() else { return }
                AppActions.capturedPrivileged(["panic"]) { result in
                    state.showInLogs(title: "dezhban — panic", text: result.output)
                }
            }
            .disabled(!state.cliFound)
            .padding(.top, 12)
        }
        .padding(24)
    }
}
