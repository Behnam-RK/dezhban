import AppKit
import SwiftUI
import WebKit
import DezhbanCore

/// The Help pane: dezhban's own documentation, rendered at build time by
/// `tools/helpgen` and read from inside the app bundle.
///
/// It never touches the network. That is the whole point — the moment someone
/// most needs to know what the guard is doing to their traffic is often the
/// moment it has cut all of it, and a help pane that needs a working connection
/// would be blank exactly then.
struct HelpView: View {
    @EnvironmentObject var state: AppState

    @State private var bundle: HelpBundle? = HelpBundle.bundled()
    @State private var query = ""
    @State private var selection: HelpPage.ID?
    /// The heading to land on after the next load, from a contextual deep link.
    @State private var pendingAnchor: String?
    /// A link the page tried to follow that leaves the bundle. Shown rather
    /// than silently dropped, so a dead-feeling click has an explanation.
    @State private var blockedLink: URL?

    var body: some View {
        Group {
            if let bundle {
                browser(bundle)
            } else {
                unavailable
            }
        }
        .navigationTitle("Help")
        .onAppear { openPendingTarget() }
        .onChange(of: state.helpTarget) { _ in openPendingTarget() }
    }

    // MARK: - Browser

    private func browser(_ bundle: HelpBundle) -> some View {
        HSplitView {
            sidebar(bundle)
                .frame(minWidth: 200, idealWidth: 240, maxWidth: 340)
            VStack(spacing: 0) {
                if let page = current(bundle) {
                    HelpWebView(url: bundle.url(for: page),
                                readAccess: bundle.directory,
                                anchor: $pendingAnchor,
                                blockedLink: $blockedLink,
                                follow: { follow($0, in: bundle) })
                } else {
                    placeholder
                }
                if let blockedLink {
                    externalLinkNote(blockedLink)
                }
            }
            .frame(minWidth: 420)
        }
    }

    private func sidebar(_ bundle: HelpBundle) -> some View {
        VStack(spacing: 0) {
            TextField("Search the documentation", text: $query)
                .textFieldStyle(.roundedBorder)
                .padding(10)
            Divider()
            List(selection: $selection) {
                if query.isEmpty {
                    // The guided track first, in reading order: someone opening
                    // Help for the first time needs a starting point, not an
                    // alphabetical index.
                    Section("Start here") {
                        ForEach(bundle.tutorial) { row($0) }
                    }
                    Section("Reference") {
                        ForEach(bundle.reference) { row($0) }
                    }
                } else {
                    let hits = bundle.search(query)
                    if hits.isEmpty {
                        Text("Nothing matched “\(query)”.")
                            .foregroundStyle(.secondary)
                            .font(.callout)
                    } else {
                        ForEach(hits) { hit in
                            hitRow(hit)
                        }
                    }
                }
            }
            .listStyle(.sidebar)
        }
    }

    private func row(_ page: HelpPage) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(page.title).font(.body)
            Text(page.summary).font(.caption).foregroundStyle(.secondary)
        }
        .padding(.vertical, 2)
        .tag(page.id)
    }

    /// A search result is a button rather than a selectable row: choosing one
    /// carries an anchor as well as a page, and List selection only carries the
    /// page.
    private func hitRow(_ hit: HelpHit) -> some View {
        Button {
            pendingAnchor = hit.anchor
            selection = hit.page.id
        } label: {
            VStack(alignment: .leading, spacing: 2) {
                Text(hit.page.title).font(.body)
                if !hit.context.isEmpty {
                    Text(hit.context).font(.caption).foregroundStyle(.secondary)
                        .lineLimit(2)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .padding(.vertical, 2)
    }

    private var placeholder: some View {
        VStack(spacing: 12) {
            Image(systemName: "book").font(.system(size: 40)).foregroundStyle(.secondary)
            Text("Pick a page").font(.title3.weight(.semibold))
            Text("Everything here ships inside the app, so it works even while dezhban has cut your traffic.")
                .multilineTextAlignment(.center).foregroundStyle(.secondary).frame(maxWidth: 420)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding(24)
    }

    /// Shown when the app was built without its documentation — a `swift run`
    /// outside a bundle, or an app from before the docs were bundled. Says so
    /// plainly instead of showing an empty pane.
    private var unavailable: some View {
        VStack(spacing: 12) {
            Image(systemName: "book.closed").font(.system(size: 40)).foregroundStyle(.secondary)
            Text("No documentation in this build").font(.title3.weight(.semibold))
            Text("This copy of Dezhban was built without its bundled help. The same pages are in the repository under docs/.")
                .multilineTextAlignment(.center).foregroundStyle(.secondary).frame(maxWidth: 420)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding(24)
    }

    private func externalLinkNote(_ url: URL) -> some View {
        HStack(spacing: 8) {
            Image(systemName: "arrow.up.forward.square").foregroundStyle(.secondary)
            Text("That link points outside the app: \(url.absoluteString)")
                .font(.callout).foregroundStyle(.secondary).lineLimit(2).textSelection(.enabled)
            Spacer()
            Button("Copy link") {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(url.absoluteString, forType: .string)
            }
            Button("Dismiss") { blockedLink = nil }
        }
        .padding(10)
        .background(.quaternary.opacity(0.4))
    }

    // MARK: - Navigation

    private func current(_ bundle: HelpBundle) -> HelpPage? {
        guard let selection else { return nil }
        return bundle.page(source: selection)
    }

    /// A cross-page link inside the pane moves the sidebar selection rather
    /// than letting the web view navigate on its own — otherwise the sidebar
    /// would keep highlighting a page the reader had already left.
    private func follow(_ url: URL, in bundle: HelpBundle) {
        let file = url.lastPathComponent
        guard let page = bundle.pages.first(where: { $0.file == file }) else { return }
        pendingAnchor = url.fragment
        selection = page.id
    }

    /// Consumes a deep link left by another pane (see `AppState.openHelp`).
    /// Cleared once acted on, so re-selecting the Help pane later does not jump
    /// back to a link the reader has moved on from.
    private func openPendingTarget() {
        guard let bundle else { return }
        guard let target = state.helpTarget, let (page, anchor) = bundle.resolve(target) else {
            if selection == nil { selection = bundle.tutorial.first?.id ?? bundle.pages.first?.id }
            return
        }
        pendingAnchor = anchor
        selection = page.id
        state.helpTarget = nil
    }
}

private extension URL {
    /// The same URL without its fragment — what tells "scroll within this page"
    /// apart from "go to another page".
    var deletingFragment: URL {
        var c = URLComponents(url: self, resolvingAgainstBaseURL: false)
        c?.fragment = nil
        return c?.url ?? self
    }

    /// The same URL pointing at a heading within the page. WebKit scrolls to it
    /// on load, which is why the pane needs no script of its own.
    func appendingFragment(_ fragment: String) -> URL {
        var c = URLComponents(url: self, resolvingAgainstBaseURL: false)
        c?.fragment = fragment
        return c?.url ?? self
    }
}

/// A `WKWebView` restricted to the bundled documentation.
///
/// Two rules make this safe to point at rendered HTML: read access is granted
/// only to the help directory, and the navigation delegate allows nothing but
/// `file:` URLs inside it. A page therefore cannot reach the network even if a
/// doc gains an external link — which matters because this pane exists to work
/// while the guard has cut traffic, and a silently hanging request would read
/// as the app being broken.
struct HelpWebView: NSViewRepresentable {
    let url: URL
    let readAccess: URL
    @Binding var anchor: String?
    @Binding var blockedLink: URL?
    /// Called for a link to another bundled page, so the owning view can move
    /// its selection instead of the web view navigating behind the sidebar.
    let follow: (URL) -> Void

    func makeNSView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        // Nothing bundled runs script; the pane only ever scrolls to an anchor,
        // which the coordinator does with a URL fragment and WebKit's own
        // scrolling — no script is injected either (see `load`).
        config.defaultWebpagePreferences.allowsContentJavaScript = false
        let web = WKWebView(frame: .zero, configuration: config)
        web.navigationDelegate = context.coordinator
        // The bundled stylesheet follows the system appearance; matching the
        // area behind it stops a white flash in dark mode between loads.
        web.underPageBackgroundColor = .textBackgroundColor
        context.coordinator.load(web, url: url, readAccess: readAccess, anchor: anchor)
        return web
    }

    func updateNSView(_ web: WKWebView, context: Context) {
        context.coordinator.parent = self
        context.coordinator.load(web, url: url, readAccess: readAccess, anchor: anchor)
    }

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    final class Coordinator: NSObject, WKNavigationDelegate {
        var parent: HelpWebView
        /// What was last handed to the web view, fragment included.
        private var loaded: URL?

        init(_ parent: HelpWebView) {
            self.parent = parent
        }

        /// Loads a page, landing on a heading when one was asked for.
        ///
        /// The anchor rides in the URL's fragment and WebKit does the scrolling,
        /// rather than the app injecting a `scrollIntoView` — which would not
        /// run anyway with content JavaScript off, and which would put script
        /// into a pane that has none.
        func load(_ web: WKWebView, url: URL, readAccess: URL, anchor: String?) {
            let target = anchor.map { url.appendingFragment($0) } ?? url
            defer { clearAnchor() }
            guard target != loaded else { return }
            loaded = target
            web.loadFileURL(target, allowingReadAccessTo: readAccess)
        }

        /// The anchor is one-shot: it is spent by the navigation it triggered.
        /// Written back on the next runloop turn because clearing a `@Binding`
        /// from inside `updateNSView` would mutate state during a view update.
        private func clearAnchor() {
            guard parent.anchor != nil else { return }
            DispatchQueue.main.async { [parent] in parent.anchor = nil }
        }

        /// Whether `url` is `root` itself or something beneath it.
        ///
        /// Compared by path COMPONENT, not by string prefix: a sibling whose
        /// name merely starts with the same characters — "help-old" next to
        /// "help" — satisfies a prefix test while being inside nothing. The
        /// bundle is generated and has no such sibling today, which is exactly
        /// why the check should not depend on that staying true.
        static func isInside(_ url: URL, _ root: URL) -> Bool {
            let here = url.standardizedFileURL.pathComponents
            let base = root.standardizedFileURL.pathComponents
            guard here.count >= base.count else { return false }
            return Array(here.prefix(base.count)) == base
        }

        func webView(_ web: WKWebView,
                     decidePolicyFor action: WKNavigationAction,
                     decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
            guard let target = action.request.url else {
                decisionHandler(.cancel)
                return
            }
            // Anything that is not a local file in the help directory is
            // refused — no http(s), no custom scheme, no path outside the
            // bundle. A refused link is reported so the pane can say what
            // happened rather than appearing to ignore the click.
            guard target.isFileURL, Coordinator.isInside(target, parent.readAccess) else {
                let blocked = target
                DispatchQueue.main.async { [parent] in parent.blockedLink = blocked }
                decisionHandler(.cancel)
                return
            }
            // A link to a DIFFERENT bundled page is handed back to the owning
            // view, which moves its selection — the web view following it on
            // its own would leave the sidebar highlighting a page the reader
            // has left. A link within the page (a bare fragment) is allowed
            // straight through: it is a scroll, not a navigation.
            if action.navigationType == .linkActivated,
               target.deletingFragment != loaded?.deletingFragment {
                let followed = target
                DispatchQueue.main.async { [parent] in parent.follow(followed) }
                decisionHandler(.cancel)
                return
            }
            decisionHandler(.allow)
        }
    }
}
