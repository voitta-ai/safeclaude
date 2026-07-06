import Foundation

/// Watches ~/.safeclaude/sessions/ and maintains one EngineClient per live
/// session (a session is live while its control.sock exists — the wrapper
/// removes the whole session dir when the last client exits).
@MainActor
final class SessionsManager: ObservableObject {
    @Published private(set) var clients: [EngineClient] = []

    private let sessionsDir: String
    private var timer: Timer?

    init() {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        sessionsDir = "\(home)/.safeclaude/sessions"
        scan()
        timer = Timer.scheduledTimer(withTimeInterval: 2.0, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.scan() }
        }
    }

    private func scan() {
        let fm = FileManager.default
        let names = (try? fm.contentsOfDirectory(atPath: sessionsDir)) ?? []
        let live = Set(names.filter { fm.fileExists(atPath: "\(sessionsDir)/\($0)/control.sock") })

        for client in clients where !live.contains(client.name) {
            client.shutdown()
        }
        clients.removeAll { !live.contains($0.name) }

        let known = Set(clients.map(\.name))
        for name in live.subtracting(known).sorted() {
            clients.append(EngineClient(name: name, socketPath: "\(sessionsDir)/\(name)/control.sock"))
        }
        clients.sort { $0.name < $1.name }
    }

    /// Force-end a session and wipe every trace of it: engine, mount,
    /// session dir (incl. rules.json), audit logs and reports — so the next
    /// `safeclaude <repo>` starts as if the repo was never guarded.
    /// wipeClaudeState additionally removes Claude Code's own per-project
    /// state for the mount path (conversation history, project config).
    func delete(_ client: EngineClient, wipeClaudeState: Bool) {
        let name = client.name
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        let sess = "\(sessionsDir)/\(name)"
        let mount = "\(home)/.safeclaude/mounts/\(name)"
        let enginePid = (try? String(contentsOfFile: "\(sess)/engine.pid", encoding: .utf8))?
            .trimmingCharacters(in: .whitespacesAndNewlines)

        client.requestShutdown() // graceful engine exit
        client.shutdown()
        clients.removeAll { $0.name == name }

        Task.detached(priority: .userInitiated) {
            try? await Task.sleep(for: .milliseconds(400))
            if let s = enginePid, let pid = Int32(s) {
                kill(pid, SIGTERM) // in case the socket shutdown didn't land
            }
            Self.run("/sbin/umount", "-f", mount)
            let fm = FileManager.default
            try? fm.removeItem(atPath: sess)
            try? fm.removeItem(atPath: mount)
            // logs: <name>-YYYYMMDD-HHMMSS.log · reports: session-<name>-….html
            // (anchored timestamps so "myrepo" never matches "myrepo-2" files)
            let esc = NSRegularExpression.escapedPattern(for: name)
            Self.wipe(dir: "\(home)/.safeclaude/logs",
                      matching: "^\(esc)-\\d{8}-\\d{6}\\.log$")
            Self.wipe(dir: "\(home)/.safeclaude/reports",
                      matching: "^session-\(esc)-\\d{8}-\\d{6}\\.html$")
            if wipeClaudeState {
                // Claude Code keys project state by path, non-alphanumerics → "-"
                let slug = String(mount.map { $0.isLetter || $0.isNumber ? $0 : "-" })
                try? fm.removeItem(atPath: "\(home)/.claude/projects/\(slug)")
            }
        }
    }

    private nonisolated static func run(_ path: String, _ args: String...) {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: path)
        p.arguments = args
        try? p.run()
        p.waitUntilExit()
    }

    private nonisolated static func wipe(dir: String, matching pattern: String) {
        let fm = FileManager.default
        guard let names = try? fm.contentsOfDirectory(atPath: dir),
              let re = try? NSRegularExpression(pattern: pattern) else { return }
        for n in names where re.firstMatch(in: n, range: NSRange(n.startIndex..., in: n)) != nil {
            try? fm.removeItem(atPath: "\(dir)/\(n)")
        }
    }

    var totalDenials: UInt64 { clients.reduce(0) { $0 + $1.stats.denials } }
    var anyConnected: Bool { clients.contains(where: \.connected) }
    /// Live Asks (paused operations) plus unhandled denials, across sessions.
    var pendingApprovals: Int {
        clients.reduce(0) { $0 + $1.pendingAsks.count + $1.recentDenials.count }
    }
}
