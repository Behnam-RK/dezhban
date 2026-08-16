import SwiftUI
import DezhbanCore

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
    }

    // MARK: - live

    private func live(_ s: Snapshot) -> some View {
        let icon = PostureUI.iconFor(s)
        return ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                hero(state: icon.state, symbol: icon.symbol, title: icon.help)

                if let sw = s.switch, sw.open {
                    // The rendered detail (in the hero) already says which kind of
                    // window this is and gives its absolute deadline; this banner's
                    // only job is the live, second-by-second countdown, which has to
                    // stay client-side — a string persisted to state.json goes stale
                    // the instant it's written. Pause gets its own wording and color
                    // (blue, not amber — it's deliberate, not a warning) so it never
                    // reads as an accidental exposure.
                    let left = sw.timeLeft(asOf: state.now).map { PostureUI.mmss($0) }
                    if sw.isPause {
                        // pause.circle.fill, not the warning triangle: a pause
                        // is deliberate, and a warning glyph on it teaches
                        // people to ignore warning glyphs.
                        banner(left.map { "Guard re-arms in \($0)" } ?? "Guard re-arms when the pause ends",
                               color: .blue, symbol: "pause.circle.fill")
                    } else {
                        banner(left.map { "Closes in \($0)" } ?? "Closes when the window ends", color: .orange)
                    }
                }

                errorBanners(s)

                detailsGrid(s)

                Divider()

                actionButtons(s)

                Spacer(minLength: 12)

                panicRow
            }
            .padding(PaneMetrics.panePadding)
            // Cap the column and keep it anchored to the leading edge. Without
            // the cap the Divider ran edge-to-edge on a wide display and the
            // action row's trailing button was flung to the far right with a
            // window-wide void beside it. Anchored rather than centered: this
            // is a left-aligned data pane — hero at the left margin, grid
            // labels right-aligned into a left column, Divider starting at the
            // margin — and centering it would unmoor it from the sidebar it
            // reads out of. (The centered treatment stays in `guided`, where
            // it belongs.) Two stacked frames is the required idiom: the inner
            // one caps, the outer claims the ScrollView's width to pin it.
            .frame(maxWidth: PaneMetrics.contentColumn, alignment: .leading)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        // The "VPN app" row's source. `.onAppear` (fires per pane selection),
        // staleness-gated inside — never the 1s timer; detect-vpn is a
        // process-scanning subprocess.
        .onAppear { state.refreshVPNInventoryIfStale() }
    }

    /// Faults get banners above the grid, not rows inside it: colored, glyphed,
    /// first line only, with the full text behind a disclosure and anything
    /// transcript-sized routed to Logs. `exitUnknown` is deliberately NOT here —
    /// it is a state, not a fault (see the grid row below).
    @ViewBuilder
    private func errorBanners(_ s: Snapshot) -> some View {
        if let err = s.enforcementErr, !err.isEmpty {
            // Previously invisible in this pane: the daemon TRIED to enforce
            // and the firewall backend refused, so posture describes an intent
            // the data plane may not be honoring. The most important line here.
            errorBanner(err, title: "Enforcement problem", color: .red)
        }
        if let err = s.lookupErr, !err.isEmpty {
            // Genuine failures only (tunnel up, measuring failed) — the daemon
            // already filters the expected cases into exitUnknown.
            errorBanner(err, title: "Exit check failing", color: .orange)
        }
    }

    private func errorBanner(_ text: String, title: String, color: Color) -> some View {
        let cut = CollapsedText.firstLine(text, max: 120)
        return VStack(alignment: .leading, spacing: 4) {
            banner("\(title): \(cut.line)", color: color)
            if cut.truncated {
                DisclosureGroup("Show more") {
                    VStack(alignment: .leading, spacing: 6) {
                        Text(text)
                            .font(.callout)
                            .foregroundStyle(.secondary)
                            .textSelection(.enabled)
                            .fixedSize(horizontal: false, vertical: true)
                        Button("Show in Logs") {
                            state.showInLogs(title: "dezhban — \(title)", text: text)
                        }
                        .buttonStyle(.borderless)
                        .font(.callout)
                    }
                }
                .font(.callout)
            }
        }
    }

    private func hero(state iconState: String, symbol: String, title: String) -> some View {
        HStack(spacing: 16) {
            // The bundled brand state tile when available (color IS the state),
            // SF Symbol fallback for a bare `swift run` binary. A state TILE,
            // not the Dock icon: the Dock's family holds only the two coarsened
            // keys, so off / warning / paused used to find no file here and
            // drop to a generic SF Symbol shield.
            if let img = PostureUI.stateTile(iconState) {
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
                if let detail = state.snapshot?.display?.detail {
                    Text(detail)
                        .foregroundStyle(.secondary)
                }
            }
        }
    }

    private func banner(_ text: String, color: Color,
                        symbol: String = "exclamationmark.triangle.fill") -> some View {
        Label(text, systemImage: symbol)
            .padding(10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(color.opacity(0.15), in: RoundedRectangle(cornerRadius: 8))
            .foregroundStyle(color)
    }

    private func detailsGrid(_ s: Snapshot) -> some View {
        let ipv6 = (s.ipv6?.isEmpty == false) ? s.ipv6 : nil
        return Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 6) {
            if let ip = s.ip, !ip.isEmpty {
                // An em dash, not a parenthetical: `countryLabel` already ends
                // in "(IR)", so "1.2.3.4 (Iran (IR) via ipinfo)" would nest one
                // set of parentheses inside another. Same call render.go's
                // fullBlockDisplay makes.
                let cc = s.countryLabel ?? "unknown country"
                let prov = s.provider.map { " via \($0)" } ?? ""
                // "Public IPv4" only when a v6 row will sit under it — with one
                // address the family qualifier is noise.
                row(ipv6 != nil ? "Public IPv4" : "Public IP", "\(ip) — \(cc)\(prov)")
            } else if let why = s.exitUnknown, !why.isEmpty {
                // Expected, not a fault — phrased as a state rather than an
                // error, because reporting it as one is what made the geo
                // providers look broken during every switch window. Genuine
                // lookup failures render as a banner above the grid instead.
                row("Exit country", "unknown — \(why)")
            }
            if let ipv6 {
                // Best-effort observation; hidden on v4-only hosts and from
                // older daemons rather than shown empty.
                row("Public IPv6", ipv6)
            }
            if let preset = state.presetLabel {
                row("Strictness", preset)
            }
            if let t = s.tunnels?.first {
                row("Tunnel", "\(t.up ? "up" : "down")\(t.detail.map { " (\($0))" } ?? "")")
            }
            if let app = state.vpnInventory?.connectedName {
                row("VPN app", app)
            }
            if let eps = s.endpoints, !eps.isEmpty {
                endpointsRow(eps)
            }
            if let profiles = state.profiles, !profiles.profiles.isEmpty {
                let names = profiles.profiles.map { p in
                    p.name == s.activeProfile ? "\(p.name) (active)" : p.name
                }
                row("VPN profiles", names.joined(separator: ", "))
            } else if let p = s.activeProfile, !p.isEmpty {
                row("VPN profile", p)
            }
            let blocking = s.blockedCountryLabels
            if !blocking.isEmpty {
                row("Blocking", blocking.joined(separator: ", "))
            }
            row("Updated", PostureUI.agoString(state.now.timeIntervalSince(s.time)))
        }
    }

    /// The endpoints row collapses past three: a learned set can run to dozens
    /// of addresses, and a wall of IPs buries every other row. The full list
    /// stays one click away, in place.
    private func endpointsRow(_ eps: [String]) -> some View {
        let summary = CollapsedText.listSummary(eps, limit: 3)
        return GridRow {
            Text("Endpoints").foregroundStyle(.secondary).gridColumnAlignment(.trailing)
            if summary.more == 0 {
                Text(summary.line).textSelection(.enabled)
            } else {
                VStack(alignment: .leading, spacing: 2) {
                    Text(summary.line).textSelection(.enabled)
                    DisclosureGroup("+\(summary.more) more") {
                        Text(eps.joined(separator: ", "))
                            .font(.callout)
                            .foregroundStyle(.secondary)
                            .textSelection(.enabled)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    .font(.callout)
                }
            }
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
        // ActionRow, not HStack: an HStack given less width than its children's
        // ideal sum compresses every one of them, so at a narrow window all
        // five labels truncated at once ("Block n…", "Switchin…", "Guard…").
        // ActionRow wraps instead, and pins "Guard down" to the trailing edge
        // of whatever line it lands on — which is also why the Spacer that used
        // to sit before it is gone.
        return ActionRow(trailingCount: 1) {
            Button("Block now") { AppActions.routine(["block"], "block") }
                .disabled(blocked)
                .help(state.routineHint("Cuts all traffic and holds it until you unblock."))
            Button("Unblock") { AppActions.routine(["unblock"], "unblock") }
                .disabled(!(blocked || guardHolds))
                .help(state.routineHint("Releases a manual block and resumes monitoring."))
            if let sw = s.switch, sw.open, sw.isPause {
                // `switch --cancel` deliberately refuses to touch a pause (see the
                // glossary's Pause entry) — `resume` is the only way to end one early.
                Button("Resume now" + sw.leftSuffix(asOf: state.now)) {
                    AppActions.routine(["resume"], "resume the guard")
                }
                .help(state.routineHint("Ends the pause early and re-arms the guard."))
            } else if let sw = s.switch, sw.open {
                Button("\(sw.isAutoRedial ? "Cancel redial window" : "Cancel VPN switch")"
                       + sw.leftSuffix(asOf: state.now)) {
                    AppActions.routine(["switch", "--cancel"], "cancel the switch window")
                }
                .help(state.routineHint("Closes the window and restores the guard."))
            } else {
                switchMenu
                Button("Pause — use my real IP") { AppActions.routine(["pause"], "pause the guard") }
                    .disabled(!state.pauseIsEnabled)
                    .help(state.pauseIsEnabled
                        ? state.routineHint("Deliberately drops to your real ISP IP, then re-arms the guard automatically.")
                        : "Disabled — vpn.pauseMax is \"0\" in your config.")
            }
            Button("Guard down") { AppActions.privileged(["stop"], "take the guard down") }
                .help("Stops dezhban. Asks for your password — it can’t stop itself while running.")
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    /// Opens a switch window, optionally targeted at a known VPN profile so the
    /// learned endpoint is attributed to it (`switch --no-wait --name <profile>`).
    /// Plain button when there are no profiles to pick from — a menu with a
    /// single "Any known VPN" entry would just be a worse button.
    @ViewBuilder
    private var switchMenu: some View {
        if let profiles = state.profiles, !profiles.profiles.isEmpty {
            Menu("Switching VPN…") {
                Button("Any known VPN") {
                    AppActions.routine(["switch", "--no-wait"], "open a switch window")
                }
                Divider()
                ForEach(profiles.profiles) { p in
                    Button(p.name) {
                        AppActions.routine(["switch", "--no-wait", "--name", p.name],
                                           "open a switch window for \(p.name)")
                    }
                }
            }
            .help(state.routineHint("Briefly relaxes the guard so a new VPN can connect."))
        } else {
            Button("Switching VPN…") { AppActions.routine(["switch", "--no-wait"], "open a switch window") }
                .help(state.routineHint("Briefly relaxes the guard so a new VPN can connect."))
        }
    }

    private var panicRow: some View {
        HStack(alignment: .firstTextBaseline, spacing: PaneMetrics.controlSpacing) {
            Button(role: .destructive) {
                guard AppActions.confirmPanic() else { return }
                AppActions.capturedPrivileged(["panic"]) { result in
                    state.showInLogs(title: "dezhban — panic", text: result.output)
                }
            } label: {
                Label("Panic — force unblock…", systemImage: "exclamationmark.octagon.fill")
            }
            .tint(.red)
            .fixedSize()
            Text("Removes every dezhban firewall rule, even with dezhban not running.")
                .font(.callout)
                .foregroundStyle(.secondary)
                // Wrap to a second line rather than truncate: this caption is
                // the sentence that stops someone pressing panic by accident.
                .fixedSize(horizontal: false, vertical: true)
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
            title: "Guard not installed",
            message: "The dezhban system service is not installed, so nothing is enforced — "
                + "at boot or now. Installing it starts the guard immediately and at every boot."
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
            title: "Stopped",
            message: "The dezhban service is installed but isn’t running "
                + "(or hasn’t reported recently). Nothing is being watched."
        ) {
            Button("Guard up") {
                AppActions.privileged(["start"], "bring the guard up")
            }
            .buttonStyle(.borderedProminent)
        }
    }

    private func guided<Content: View>(symbol: String, title: String, message: String,
                                       @ViewBuilder action: () -> Content) -> some View {
        VStack(spacing: 12) {
            // "off" is right for all three degraded pages — CLI missing, service
            // not installed, and stopped are all "not enforcing". It just
            // resolves to a real file now.
            if let img = PostureUI.stateTile("off") {
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
                .frame(maxWidth: PaneMetrics.proseColumn)
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
