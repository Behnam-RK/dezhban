import AppKit
import Combine

/// The sidebar column, as an AppKit source list.
///
/// This was a SwiftUI `List(selection:).listStyle(.sidebar)`. It is not any
/// more, and the reason is worth recording so nobody "simplifies" it back:
/// SwiftUI only renders the real full-bleed source list when it can see that it
/// IS the sidebar of a NavigationSplitView. Hosted in a bare
/// NSHostingController inside an AppKit split — which is what this window needs
/// for its chrome to be deterministic — it falls back to an inset, rounded,
/// bordered list that floats on the window background. That floating card was
/// the reported bug. Compare Finder on the same OS: its sidebar runs to the
/// window edges with the traffic lights sitting on top of it.
///
/// `NSTableView.Style.sourceList` gives exactly that, with no availability
/// guards (macOS 11+) and no dependence on what SwiftUI infers.
///
/// AppState.selectedSection stays the single source of truth in both
/// directions, so the programmatic jumps (AppState.openHelp → .help,
/// AppState.showInLogs → .logs) still move the selection here.
final class SidebarViewController: NSViewController {
    private let tableView = NSTableView()
    private let scrollView = NSScrollView()
    private var cancellables = Set<AnyCancellable>()

    /// Guards the two-way binding: without it, writing the selection back into
    /// AppState re-enters this controller and fights the user's click.
    private var isSyncingSelection = false

    private static let cellID = NSUserInterfaceItemIdentifier("dezhban.sidebarCell")

    override func loadView() {
        let column = NSTableColumn(identifier: NSUserInterfaceItemIdentifier("main"))
        column.resizingMask = .autoresizingMask
        tableView.addTableColumn(column)
        tableView.style = .sourceList
        tableView.headerView = nil
        tableView.backgroundColor = .clear
        tableView.selectionHighlightStyle = .regular
        tableView.allowsEmptySelection = false
        tableView.allowsMultipleSelection = false
        tableView.rowSizeStyle = .default
        tableView.dataSource = self
        tableView.delegate = self

        scrollView.documentView = tableView
        scrollView.drawsBackground = false
        scrollView.hasVerticalScroller = true
        scrollView.autohidesScrollers = true
        // The sidebar item supplies the vibrancy; anything opaque here would
        // sit on top of it and undo the effect.
        scrollView.backgroundColor = .clear

        view = scrollView

        // Seeds the split item's initial thickness. An NSScrollView has no
        // intrinsic width, so without a seed the sidebar starts at zero and the
        // toggle has no width to restore it to — it looks permanently
        // collapsed. (The SwiftUI hosting controller this replaced supplied a
        // width from its content; an AppKit view has to say so.)
        //
        // A low-priority WIDTH constraint, not `preferredContentSize`: an
        // NSSplitViewController derives its own preferred size from its
        // children, so a preferred size here propagated to the window and
        // locked its height at whatever number this said. Low priority so the
        // user can still drag the divider — minimumThickness/maximumThickness
        // on the split item are what actually bound it.
        let seed = scrollView.widthAnchor.constraint(equalToConstant: 200)
        seed.priority = .defaultLow
        seed.isActive = true
    }

    override func viewDidLoad() {
        super.viewDidLoad()
        select(AppState.shared.selectedSection)
        AppState.shared.$selectedSection
            // @Published emits in willSet, so hop a turn to read the new value.
            .receive(on: RunLoop.main)
            .sink { [weak self] section in self?.select(section) }
            .store(in: &cancellables)
    }

    private func select(_ section: SidebarSection?) {
        guard !isSyncingSelection,
              let index = SidebarSection.allCases.firstIndex(of: section ?? .overview),
              tableView.selectedRow != index else { return }
        isSyncingSelection = true
        tableView.selectRowIndexes(IndexSet(integer: index), byExtendingSelection: false)
        isSyncingSelection = false
    }
}

extension SidebarViewController: NSTableViewDataSource {
    func numberOfRows(in tableView: NSTableView) -> Int {
        SidebarSection.allCases.count
    }
}

extension SidebarViewController: NSTableViewDelegate {
    func tableView(_ tableView: NSTableView,
                   viewFor tableColumn: NSTableColumn?, row: Int) -> NSView? {
        let section = SidebarSection.allCases[row]
        let cell = (tableView.makeView(withIdentifier: Self.cellID, owner: self) as? NSTableCellView)
            ?? Self.makeCell()
        cell.imageView?.image = NSImage(systemSymbolName: section.systemImage,
                                        accessibilityDescription: nil)
        cell.textField?.stringValue = section.label
        return cell
    }

    func tableViewSelectionDidChange(_ notification: Notification) {
        guard !isSyncingSelection else { return }
        let row = tableView.selectedRow
        guard SidebarSection.allCases.indices.contains(row) else { return }
        isSyncingSelection = true
        AppState.shared.selectedSection = SidebarSection.allCases[row]
        isSyncingSelection = false
    }

    private static func makeCell() -> NSTableCellView {
        let cell = NSTableCellView()
        cell.identifier = cellID

        let icon = NSImageView()
        icon.translatesAutoresizingMaskIntoConstraints = false
        icon.imageScaling = .scaleProportionallyDown
        // Match the sidebar row tint the system uses for a selected source-list
        // row, rather than the icon's own color.
        icon.contentTintColor = nil
        cell.addSubview(icon)
        cell.imageView = icon

        let label = NSTextField(labelWithString: "")
        label.translatesAutoresizingMaskIntoConstraints = false
        label.lineBreakMode = .byTruncatingTail
        label.font = .systemFont(ofSize: NSFont.systemFontSize)
        cell.addSubview(label)
        cell.textField = label

        NSLayoutConstraint.activate([
            icon.leadingAnchor.constraint(equalTo: cell.leadingAnchor),
            icon.centerYAnchor.constraint(equalTo: cell.centerYAnchor),
            icon.widthAnchor.constraint(equalToConstant: 18),
            label.leadingAnchor.constraint(equalTo: icon.trailingAnchor, constant: 6),
            label.trailingAnchor.constraint(lessThanOrEqualTo: cell.trailingAnchor),
            label.centerYAnchor.constraint(equalTo: cell.centerYAnchor),
        ])
        return cell
    }
}
