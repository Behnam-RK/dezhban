import SwiftUI
import DezhbanCore

/// The window's primary pane: a live status hero, the daily controls, and the
/// panic escape hatch. Degraded states (CLI missing / service not installed /
/// daemon stopped) each get a guided layout with the one relevant action inline,
/// instead of a wall of disabled buttons.
struct OverviewView: View {
    @EnvironmentObject var state: AppState
    @State private var busy = false
    /// The action control the pointer is over, and the sentence it explains.
    /// Carried together so a control can only clear the caption it actually put
    /// there — see `captioned`.
    @State private var hoveredAction: (id: String, hint: String)?
    /// Last hint handed over by keyboard focus. Read only while
    /// `focusedAction != nil`, so blurring the row falls back to the posture
    /// headline instead of stranding the caption on a control nobody is on.
    @State private var focusedHint = ""
    /// The last pointer position reported for each control, so `.active` phases
    /// caused by a tracking area being rebuilt can be told from real movement.
    @State private var lastHoverPoint: [String: CGPoint?] = [:]
    @FocusState private var focusedAction: String?

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

                VStack(alignment: .leading, spacing: 6) {
                    actionButtons(s)
                    actionCaption(s)
                }

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
        let observed = (s.ipv6?.isEmpty == false) ? s.ipv6 : nil
        // The geo reading's IP is family-AGNOSTIC — the country lookup goes out
        // over whichever family the OS picks — so on a v6-preferring tunnel
        // `s.ip` is itself a v6 address. Calling it "Public IPv4" would be a
        // wrong label on a correct value, and when it is the very address the
        // v6 probe observed, a second row would repeat it.
        let geoIsV6 = s.ip?.contains(":") == true
        // Suppressed by FAMILY, not by string equality. The two are independent
        // observers — the geo provider reports the address it saw the request
        // arrive from, the probe reports the one the host sends from — and with
        // privacy extensions those legitimately differ. Deduping on equality
        // alone therefore left the grid showing two IPv6 addresses, the first
        // labelled "Public IP" and the second "Public IPv6", which reads as a
        // contradiction rather than as two readings. One family, one row, and
        // the geo row is the one the country verdict is tied to.
        let ipv6 = geoIsV6 ? nil : observed
        return Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 6) {
            if let ip = s.ip, !ip.isEmpty {
                // An em dash, not a parenthetical: `countryLabel` already ends
                // in "(IR)", so "1.2.3.4 (Iran (IR) via ipinfo)" would nest one
                // set of parentheses inside another. Same call render.go's
                // fullBlockDisplay makes.
                let cc = s.countryLabel ?? "unknown country"
                let prov = s.provider.map { " via \($0)" } ?? ""
                // "Public IPv4" only when this address really is v4 AND a v6 row
                // will sit under it — with one address, or an address whose
                // family we didn't choose, the qualifier is noise at best and
                // wrong at worst.
                row(!geoIsV6 && ipv6 != nil ? "Public IPv4" : "Public IP", "\(ip) — \(cc)\(prov)")
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
        // Titles are one or two words; the sentence each one used to carry
        // inline ("Pause — use my real IP") is now the caption line below, fed
        // by hover and keyboard focus. Same strings still go to `.help`, so the
        // tooltip and the caption can never say different things.
        //
        // ActionRow, not HStack: an HStack given less width than its children's
        // ideal sum compresses every one of them, so at a narrow window all
        // five labels truncated at once ("Block n…", "Switchin…", "Guard…").
        // ActionRow wraps instead, and pins "Guard down" to the trailing edge
        // of whatever line it lands on — which is also why the Spacer that used
        // to sit before it is gone.
        return ActionRow(trailingCount: 1) {
            captioned("block",
                      state.routineHint("Cuts all traffic and holds it until you unblock.")) {
                Button("Block") { AppActions.routine(["block"], "block") }
                    .disabled(blocked)
            }
            captioned("unblock",
                      state.routineHint("Releases a manual block and resumes monitoring.")) {
                Button("Unblock") { AppActions.routine(["unblock"], "unblock") }
                    .disabled(!(blocked || guardHolds))
            }
            if let sw = s.switch, sw.open, sw.isPause {
                // `switch --cancel` deliberately refuses to touch a pause (see the
                // glossary's Pause entry) — `resume` is the only way to end one early.
                captioned("resume",
                          state.routineHint("Ends the pause early and re-arms the guard.")) {
                    Button("Resume" + sw.leftSuffix(asOf: state.now)) {
                        AppActions.routine(["resume"], "resume the guard")
                    }
                }
            } else if let sw = s.switch, sw.open {
                // Which window, not just "the window". Hovering this button replaces
                // the posture headline — the only other place the distinction
                // appears — so a caption that says neither leaves the user cancelling
                // something unnamed, and the three triggers being distinct is most of
                // what the guard's rules rest on.
                captioned("cancel-window",
                          state.routineHint(sw.isAutoRedial
                              ? "Closes the automatic redial window and restores the guard."
                              : "Closes the switch window you opened and restores the guard.")) {
                    Button("Cancel" + sw.leftSuffix(asOf: state.now)) {
                        AppActions.routine(["switch", "--cancel"], "cancel the switch window")
                    }
                }
            } else {
                switchMenu
                captioned("pause", state.pauseIsEnabled
                            ? state.routineHint("Uses your real ISP IP instead of the VPN, then re-arms the guard automatically.")
                            : "Disabled — vpn.pauseMax is \"0\" in your config.") {
                    Button("Pause") { AppActions.routine(["pause"], "pause the guard") }
                        .disabled(!state.pauseIsEnabled)
                }
            }
            captioned("stop",
                      "Stops dezhban. Asks for your password — it can’t stop itself while running.") {
                Button("Guard down") { AppActions.privileged(["stop"], "take the guard down") }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    /// Wraps one action control so pointing at it, or focusing it with the
    /// keyboard, puts `hint` in the caption line — and puts the same string in
    /// the tooltip, so the two surfaces cannot drift apart.
    ///
    /// `id` is what distinguishes controls in the hover/focus state; it is never
    /// shown. Hover is cleared only by the control that owns the current hint,
    /// so the pointer leaving A after it has already entered B does not blank
    /// out B's caption.
    @ViewBuilder
    private func captioned<Content: View>(_ id: String, _ hint: String,
                                          @ViewBuilder content: () -> Content) -> some View {
        content()
            .help(hint)
            .focused($focusedAction, equals: id)
            .onHover { inside in
                if inside {
                    hoveredAction = (id, hint)
                } else if hoveredAction?.id == id {
                    hoveredAction = nil
                }
            }
            // Re-arms hover on actual mouse *movement*, not merely on being hovered.
            // `onHover` fires on enter and exit alone, so once focus cleared the hover
            // state (see below) a pointer already sitting on this control could not
            // take the caption back until it left and re-entered — which is not what
            // "move the pointer and it takes over again" means, and is the step the
            // checklist asks a tester to perform.
            //
            // The location is compared, because `.active` is also reported when a
            // tracking area is (re)established — and these controls re-measure
            // constantly: a window's countdown retitles "Cancel (m:ss left)" every
            // second, which re-places the row. Acting on any `.active` therefore let
            // a parked pointer silently reclaim the caption a second after the
            // keyboard took it, defeating "most recent interaction wins" in exactly
            // the scenario the checklist step describes.
            .onContinuousHover { phase in
                guard case .active(let point) = phase else {
                    lastHoverPoint[id] = nil
                    return
                }
                defer { lastHoverPoint[id] = point }
                guard let previous = lastHoverPoint[id] ?? nil else { return }
                guard previous != point else { return }
                if hoveredAction?.id != id { hoveredAction = (id, hint) }
            }
            .onChange(of: focusedAction) { _ in
                guard focusedAction == id else { return }
                focusedHint = hint
                // Focus supersedes a parked pointer. `.help` is recomputed every
                // body pass while the captured hint is not, and hover used to win
                // unconditionally — so tabbing with the pointer resting on another
                // button moved the focus ring and the Space key while the caption
                // went on describing whatever the mouse happened to be over. Most
                // recent interaction wins; moving the pointer takes it straight back.
                hoveredAction = nil
            }
            .onChange(of: hint) { newHint in
                // The captured string has to track the live one. `state.routineHint`
                // flips on `controlIsReachable`, which a poll updates — so a hint
                // captured at hover-enter went stale under a stationary pointer and
                // the caption said "No password needed" while the tooltip, recomputed
                // each pass, said the opposite. That disagreement is the one thing
                // this wrapper exists to prevent.
                if hoveredAction?.id == id { hoveredAction = (id, newHint) }
                if focusedAction == id { focusedHint = newHint }
            }
            .onDisappear {
                // A control can be replaced under a stationary pointer — Pause
                // becomes Cancel the moment a window opens — and the removed view
                // never receives a hover-exit, so its caption described a button that
                // no longer exists until the mouse next moved.
                if hoveredAction?.id == id { hoveredAction = nil }
                if focusedAction == id { focusedHint = "" }
            }
    }

    /// The one line that explains whatever the user is pointing at. Always
    /// present and always one line high — a caption that vanished between two
    /// buttons would reflow the row under the pointer.
    private func actionCaption(_ s: Snapshot) -> some View {
        Text(ActionCaption.text(hovered: hoveredAction?.hint,
                                focused: focusedAction == nil ? nil : focusedHint,
                                fallback: PostureUI.humanPosture(s)))
            .font(.callout)
            .foregroundStyle(.secondary)
            // Two lines, always reserved. One line truncated the tail of every long
            // caption, and the tail is where `routineHint` appends the password
            // expectation — so Pause read "…Will ask for your pass…" and dropped the
            // clause warning that a prompt is coming. Reserving the space keeps the
            // row from reflowing as the pointer crosses it, which is why this was
            // one line to begin with; the panic caption twelve lines below already
            // made the same trade for the same reason.
            .lineLimit(2, reservesSpace: true)
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityHidden(true)
    }

    /// Opens a switch window, optionally targeted at a known VPN profile so the
    /// learned endpoint is attributed to it (`switch --no-wait --name <profile>`).
    /// Plain button when there are no profiles to pick from — a menu with a
    /// single "Any known VPN" entry would just be a worse button.
    @ViewBuilder
    private var switchMenu: some View {
        let hint = state.routineHint("Briefly relaxes the guard so a new VPN can connect.")
        if let profiles = state.profiles, !profiles.profiles.isEmpty {
            captioned("switch", hint) {
                Menu("Switch VPN…") {
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
            }
        } else {
            captioned("switch", hint) {
                Button("Switch VPN…") { AppActions.routine(["switch", "--no-wait"], "open a switch window") }
            }
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
                Label("Panic…", systemImage: "exclamationmark.octagon.fill")
            }
            .tint(.red)
            .fixedSize()
            Text("Force unblock: removes every dezhban firewall rule, even with dezhban not running.")
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
            //
            // This one keeps its full title while the Overview's own panic
            // button is just "Panic…": the rule is that a title may shed its
            // explanation only where the explanation has somewhere else to
            // live. Here there is no caption line and no action row — a lone
            // "Panic…" in a guided empty state would explain itself to nobody.
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
