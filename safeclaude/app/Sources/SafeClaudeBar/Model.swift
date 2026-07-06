import Foundation

// Mirrors the engine's JSON types (engine/events.go, rules.go, control.go).

struct EngineEvent: Codable, Identifiable {
    let seq: UInt64
    let time: Date
    let op: String
    let path: String
    let category: String
    let action: String
    let severity: String
    let notify: Bool
    var id: UInt64 { seq }
}

struct SessionStats: Codable {
    var started: Date
    var repo: String
    var totalOps: UInt64
    var denials: UInt64
    var hidden: UInt64
    var byCategory: [String: UInt64]
    var deniedPaths: [String: UInt64]
    var byAction: [String: UInt64]

    enum CodingKeys: String, CodingKey {
        case started, repo
        case totalOps = "total_ops"
        case denials, hidden
        case byCategory = "by_category"
        case deniedPaths = "denied_paths"
        case byAction = "by_action"
    }

    static let empty = SessionStats(
        started: .now, repo: "", totalOps: 0, denials: 0, hidden: 0,
        byCategory: [:], deniedPaths: [:], byAction: [:])
}

struct PathOverride: Codable {
    var glob: String
    var action: String
}

/// Meta controls: the layer between explicit path overrides and the
/// category grid (overrides > meta > grid). Mirrors engine/meta.go.
struct MetaControls: Codable, Equatable {
    var allowClaudeWrites: Bool? = true
    var localSkills: String? = "self"
    var localHooks: String? = "self"
    var memory: String? = "self"
    var gitControls: String? = "self"

    enum CodingKeys: String, CodingKey {
        case allowClaudeWrites = "allow_claude_writes"
        case localSkills = "local_skills"
        case localHooks = "local_hooks"
        case memory
        case gitControls = "git_controls"
    }

    static let factory = MetaControls()
}

struct RuleSet: Codable {
    var read: [String: String]
    var write: [String: String]
    var overrides: [PathOverride]? = nil
    var meta: MetaControls? = nil

    /// Factory default: agent-steering and executable content blocked-with-
    /// notification; ordinary project files ("source"), inert git metadata,
    /// and routine git-config reads allowed. Mirrors DenyAllRules.
    static var denyAll: RuleSet {
        var all = Dictionary(uniqueKeysWithValues: allCategories.map { ($0.key, "notify") })
        all["vcs-metadata"] = "allow"
        all["source"] = "allow"
        var write = all
        all["vcs-config"] = "allow" // reads only; writes stay gated
        return RuleSet(read: all, write: write, meta: .factory)
    }
}

// Category display metadata, kept in the engine's order.
struct CategoryInfo {
    let key: String
    let label: String
    let isArtifact: Bool
    let icon: String   // SF Symbol
    let risk: String   // plain-language "why this is gated", shown in the Ask prompt
}

let allCategories: [CategoryInfo] = [
    .init(key: "claude-skill", label: "Claude skill", isArtifact: true,
          icon: "wand.and.stars",
          risk: "A skill can inject instructions into Claude and change how it behaves."),
    .init(key: "claude-hook", label: "Claude hook", isArtifact: true,
          icon: "bolt.horizontal.circle",
          risk: "A hook runs automatically on Claude's tool calls — arbitrary code execution."),
    .init(key: "claude-command", label: "Claude command", isArtifact: true,
          icon: "terminal",
          risk: "A slash command can steer Claude with custom instructions."),
    .init(key: "claude-agent", label: "Claude agent", isArtifact: true,
          icon: "person.2.badge.gearshape",
          risk: "An agent definition sets another Claude's system prompt and tools."),
    .init(key: "claude-settings", label: "Claude settings", isArtifact: true,
          icon: "gearshape.2",
          risk: "Settings control permissions, hooks, and env vars for this project."),
    .init(key: "claude-memory", label: "Claude memory", isArtifact: true,
          icon: "brain",
          risk: "Memory / CLAUDE.md is loaded into context as standing instructions."),
    .init(key: "mcp-config", label: "MCP config", isArtifact: true,
          icon: "server.rack",
          risk: "MCP config declares tool servers Claude will connect to and trust."),
    .init(key: "secret", label: "Secret", isArtifact: false,
          icon: "key.fill",
          risk: "Credentials, keys, or tokens — sensitive material that could be exfiltrated."),
    .init(key: "vcs-hooks", label: "Git hook", isArtifact: false,
          icon: "bolt.badge.clock",
          risk: "Git runs hooks on commit/checkout — arbitrary code execution."),
    .init(key: "vcs-config", label: "Git config", isArtifact: false,
          icon: "slider.horizontal.3",
          risk: "Git config can point fsmonitor/hooksPath at code that runs on `git status`."),
    .init(key: "vcs-metadata", label: "Git metadata", isArtifact: false,
          icon: "point.3.filled.connected.trianglepath.dotted",
          risk: "Internal git bookkeeping — HEAD, refs, index, objects."),
    .init(key: "source", label: "Project file", isArtifact: false,
          icon: "doc.text",
          risk: "An ordinary file in the repository."),
]

// Categories that carry real blast radius get the red tier; other gated
// categories get amber. Everything the agent can steer, plus secrets and the
// git execution vectors, is red.
private let dangerousKeys: Set<String> = [
    "claude-skill", "claude-hook", "claude-command", "claude-agent",
    "claude-settings", "claude-memory", "mcp-config",
    "secret", "vcs-hooks", "vcs-config",
]

// Older engine builds emit pre-split category keys; map them so stale
// sessions still render a real badge instead of a "?" fallback.
private let legacyKeyAlias: [String: String] = [
    "vcs-executable": "vcs-config",
    "vcs-internal": "vcs-metadata",
]

extension CategoryInfo {
    var isDangerous: Bool { dangerousKeys.contains(key) }
}

func categoryInfo(_ key: String) -> CategoryInfo {
    if let c = allCategories.first(where: { $0.key == key }) { return c }
    if let a = legacyKeyAlias[key], let c = allCategories.first(where: { $0.key == a }) { return c }
    return CategoryInfo(key: key, label: key, isArtifact: false,
                        icon: "questionmark.circle", risk: "Uncategorized path.")
}

/// Human-readable verb for an operation code.
func opVerb(_ op: String) -> String {
    switch op {
    case "READ": return "read"
    case "WRITE": return "write to"
    case "CREATE": return "create"
    case "DELETE": return "delete"
    case "LIST": return "list"
    case "RENAME": return "rename"
    case "MKDIR": return "create directory"
    case "SYMLNK": return "symlink"
    case "CHMOD": return "change permissions of"
    case "CHOWN": return "change owner of"
    default: return op.lowercased()
    }
}

extension Notification.Name {
    /// Posted when a deny event arrives from any engine — the dashboard
    /// raises itself and jumps to the Sessions tab.
    static let safeclaudeDenial = Notification.Name("SafeClaudeDenial")
}

let actionChoices: [(key: String, label: String)] = [
    ("allow", "Allow"),
    ("ask", "Ask"),
    ("block", "Block"),
    ("hide", "Hide"),
    ("notify", "Block & Notify"),
]

// MARK: - Meta controls (third policy column)

/// The three positions of a meta switch, in display order.
let metaModeChoices: [(key: String, label: String)] = [
    ("off", "Block"),
    ("self", "Self"),
    ("ask", "Ask"),
]

struct MetaSwitchInfo {
    let key: String    // wire name for set_meta
    let label: String
    let help: String
}

let metaSwitches: [MetaSwitchInfo] = [
    .init(key: "local_skills", label: "Local skills",
          help: "Reading skills in this repo. Block: none readable. Self: only skills not yet in git, or whose last commit is yours. Ask: every read pauses for approval."),
    .init(key: "local_hooks", label: "Local hooks",
          help: "Reading Claude hooks in this repo. Block: none readable. Self: only hooks not yet in git, or whose last commit is yours. Ask: every read pauses for approval."),
    .init(key: "memory", label: "Memory",
          help: "Reading CLAUDE.md / memory files. Block: none readable. Self: only memory not yet in git, or whose last commit is yours. Ask: every read pauses for approval."),
    .init(key: "git_controls", label: "Git hooks & config",
          help: "Git hooks and .git/config, reads and writes. Block: nothing. Self: reads allowed for files that predate the session; writes fall to the grid. Ask: pauses for approval."),
]

extension MetaControls {
    func mode(for key: String) -> String {
        switch key {
        case "local_skills": return localSkills ?? "self"
        case "local_hooks": return localHooks ?? "self"
        case "memory": return memory ?? "self"
        case "git_controls": return gitControls ?? "self"
        default: return "self"
        }
    }

    mutating func set(key: String, value: String) {
        switch key {
        case "allow_claude_writes": allowClaudeWrites = (value == "on")
        case "local_skills": localSkills = value
        case "local_hooks": localHooks = value
        case "memory": memory = value
        case "git_controls": gitControls = value
        default: break
        }
    }

    /// Fill fields absent from an older rules file with factory values.
    func normalized() -> MetaControls {
        var m = self
        m.allowClaudeWrites = m.allowClaudeWrites ?? true
        m.localSkills = m.localSkills ?? "self"
        m.localHooks = m.localHooks ?? "self"
        m.memory = m.memory ?? "self"
        m.gitControls = m.gitControls ?? "self"
        return m
    }
}

/// A pending Ask pushed by an engine: the file operation is parked (the
/// agent's call is blocked mid-syscall) until resolved or expired.
struct PendingAsk: Codable, Identifiable {
    let id: String
    let op: String
    let path: String
    let category: String
    let created: Date
    let expires: Date
}

/// The default-policy template new sessions inherit
/// (~/.safeclaude/default-rules.json). Factory state: deny everything.
@MainActor
final class DefaultPolicyStore: ObservableObject {
    @Published private(set) var rules: RuleSet = .denyAll

    private let url = FileManager.default.homeDirectoryForCurrentUser
        .appendingPathComponent(".safeclaude/default-rules.json")

    init() { load() }

    func load() {
        if let data = try? Data(contentsOf: url),
           let r = try? JSONDecoder().decode(RuleSet.self, from: data) {
            // overlay onto denyAll so categories added later get an entry
            var base = RuleSet.denyAll
            base.read.merge(r.read) { _, new in new }
            base.write.merge(r.write) { _, new in new }
            base.overrides = r.overrides
            if let m = r.meta { base.meta = m.normalized() }
            rules = base
        } else {
            save() // first run: materialize deny-all so the engine sees it too
        }
    }

    func set(axis: String, category: String, action: String) {
        if axis == "write" {
            rules.write[category] = action
        } else {
            rules.read[category] = action
        }
        save()
    }

    func setMeta(name: String, value: String) {
        var m = rules.meta ?? .factory
        m.set(key: name, value: value)
        rules.meta = m
        save()
    }

    private func save() {
        try? FileManager.default.createDirectory(
            at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        let enc = JSONEncoder()
        enc.outputFormatting = [.prettyPrinted, .sortedKeys]
        if let data = try? enc.encode(rules) {
            try? data.write(to: url, options: .atomic)
        }
    }
}
