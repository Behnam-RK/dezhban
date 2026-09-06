import Foundation

/// Mirrors Go's `setup.Option`.
public struct SetupOption: Codable, Identifiable, Hashable {
    public var id: String { value }
    public let label: String
    public let value: String

    public init(label: String, value: String) {
        self.label = label
        self.value = value
    }
}

/// Mirrors Go's `setup.Question` (`dezhban setup --questions --json`).
///
/// The app asks what the CLI wizard asks, in the same order, gated the same
/// way — because it asks the daemon rather than keeping its own list. A
/// question added in Go appears here without a Swift change.
public struct SetupQuestion: Codable, Identifiable, Hashable {
    public var id: String { questionID }
    /// `id` in the JSON; renamed because `id` is taken by Identifiable.
    public let questionID: String
    /// The dotted config key this answer writes, empty when the question only
    /// steers the flow or is folded into another key's value.
    public let key: String
    public let kind: String
    public let title: String
    public let description: String
    public let options: [SetupOption]
    /// Seeded answer for every kind but multiselect.
    public let defaultValue: String
    /// Seeded answer for multiselect.
    public let selected: [String]
    public let group: Int
    /// Gate: this question is asked only when `requiresID`'s answer equals
    /// `requiresValue`.
    public let requiresID: String
    public let requiresValue: String

    private enum CodingKeys: String, CodingKey {
        case questionID = "id"
        case key, kind, title, description, options
        case defaultValue = "default"
        case selected, group
        case requiresID = "requiresId"
        case requiresValue
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        questionID = try c.decode(String.self, forKey: .questionID)
        kind = try c.decode(String.self, forKey: .kind)
        title = try c.decode(String.self, forKey: .title)
        // Every remaining field is `omitempty` on the Go side.
        key = try c.decodeIfPresent(String.self, forKey: .key) ?? ""
        description = try c.decodeIfPresent(String.self, forKey: .description) ?? ""
        options = try c.decodeIfPresent([SetupOption].self, forKey: .options) ?? []
        defaultValue = try c.decodeIfPresent(String.self, forKey: .defaultValue) ?? ""
        selected = try c.decodeIfPresent([String].self, forKey: .selected) ?? []
        group = try c.decodeIfPresent(Int.self, forKey: .group) ?? 0
        requiresID = try c.decodeIfPresent(String.self, forKey: .requiresID) ?? ""
        requiresValue = try c.decodeIfPresent(String.self, forKey: .requiresValue) ?? ""
    }

    public init(questionID: String, key: String = "", kind: String, title: String,
                description: String = "", options: [SetupOption] = [],
                defaultValue: String = "", selected: [String] = [], group: Int = 1,
                requiresID: String = "", requiresValue: String = "") {
        self.questionID = questionID
        self.key = key
        self.kind = kind
        self.title = title
        self.description = description
        self.options = options
        self.defaultValue = defaultValue
        self.selected = selected
        self.group = group
        self.requiresID = requiresID
        self.requiresValue = requiresValue
    }

    public var isGated: Bool { !requiresID.isEmpty }

    public static func decodeList(_ data: Data) -> [SetupQuestion]? {
        try? JSONDecoder().decode([SetupQuestion].self, from: data)
    }
}

/// Question kinds, mirroring Go's `setup.Kind*` constants.
public enum SetupKind {
    public static let duration = "duration"
    public static let text = "text"
    public static let list = "list"
    public static let select = "select"
    public static let multiSelect = "multiselect"
    public static let bool = "bool"
}

/// The answers collected so far, keyed by question id, plus the rules for
/// turning them into `config set` pairs.
///
/// Values are strings for every kind — "true"/"false" for a bool,
/// comma-separated for a list — which is what `config set` takes and what Go's
/// `Answers.Set` accepts, so the two wizards agree on the wire as well as on
/// the questions.
public struct SetupAnswers {
    public private(set) var values: [String: String]

    /// Seeds every question with its default, so a wizard the user clicks
    /// straight through writes back what the config already says.
    public init(questions: [SetupQuestion]) {
        var seeded: [String: String] = [:]
        for q in questions {
            seeded[q.id] = q.kind == SetupKind.multiSelect
                ? q.selected.joined(separator: ",")
                : q.defaultValue
        }
        values = seeded
    }

    public subscript(id: String) -> String {
        get { values[id] ?? "" }
        set { values[id] = newValue }
    }

    public func bool(_ id: String) -> Bool { values[id] == "true" }

    public func list(_ id: String) -> [String] {
        (values[id] ?? "").split(separator: ",")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
    }

    /// Whether a question's gate is satisfied by the answers so far. Mirrors
    /// Go's `Answers.ShouldAsk`.
    public func shouldAsk(_ q: SetupQuestion) -> Bool {
        guard q.isGated else { return true }
        return self[q.requiresID] == q.requiresValue
    }

    /// The `key=value` pairs one batched `config set` should write.
    ///
    /// Two rules are not derivable from a question's `key` alone, and they
    /// mirror Go's `setup.Apply` exactly:
    ///  - the free-text country codes fold into `blockedCountries`;
    ///  - choosing automatic detection CLEARS pinned interfaces rather than
    ///    skipping the key, because a leftover pin is what makes autodetect not
    ///    happen.
    ///
    /// The third rule is `shouldAsk` itself, and it is load-bearing now that
    /// there is no "configure your VPN now?" question to skip the branch: a key
    /// whose question was never shown must not be written. On macOS the
    /// endpoint question is gated behind "not automatic", so a re-run that
    /// leaves automatic detection on produces no `vpn.endpoints=` pair at all —
    /// which is what keeps it from blanking a configured server. Go's `Apply`
    /// achieves the same with a nil `Input.Endpoints`.
    public func configPairs(for questions: [SetupQuestion]) -> [String] {
        var pairs: [String] = []
        for q in questions where shouldAsk(q) && !q.key.isEmpty {
            switch q.id {
            case "blockedCountries":
                let all = list("blockedCountries") + list("otherCountries")
                pairs.append("blockedCountries=\(all.joined(separator: ","))")
            case "tunnels":
                pairs.append("vpn.tunnelInterfaces=\(list("tunnels").joined(separator: ","))")
            default:
                pairs.append("\(q.key)=\(self[q.id])")
            }
        }
        if bool("autoMode") {
            pairs.append("vpn.tunnelInterfaces=")
        }
        return pairs
    }

    /// The VPN config files to import, which are not a config key at all — they
    /// become profiles through `dezhban vpn import`.
    ///
    /// Gated, for the same reason `configPairs` skips an unasked key: this step
    /// reveals in place and re-evaluates as answers change, so someone can untick
    /// automatic detection, choose files, then tick it again — the field goes
    /// away but the answer it collected does not. Importing those would enact a
    /// choice the user visibly withdrew, and Go's wizard does not (it reads this
    /// answer only when `ShouldAsk` holds). Taking the question set rather than
    /// reading the stored answer blind is what keeps the two in step.
    public func profileFiles(for questions: [SetupQuestion]) -> [String] {
        guard let q = questions.first(where: { $0.id == "profileFiles" }), shouldAsk(q) else {
            return []
        }
        return list("profileFiles")
    }
}

/// When the first-run wizard should be offered.
///
/// Split from the UserDefaults flag that feeds it so the rule itself is
/// testable: whether the flag is set is bookkeeping, but whether an unasked
/// user should be asked is a decision — and getting it wrong means an app that
/// looks like it forgot the setup someone already did.
public enum FirstRunDecision {
    /// - Parameters:
    ///   - isComplete: whether this account has completed the wizard before.
    ///   - vpnKnown: whether dezhban already knows a VPN server, from an
    ///     endpoint or a profile. Not "are tunnel interfaces set": those are
    ///     legitimately empty on an autodetect config, which is the recommended
    ///     one.
    public static func offer(isComplete: Bool, vpnKnown: Bool) -> Bool {
        !isComplete && !vpnKnown
    }
}
