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

    var totalDenials: UInt64 { clients.reduce(0) { $0 + $1.stats.denials } }
    var anyConnected: Bool { clients.contains(where: \.connected) }
    /// Live Asks (paused operations) plus unhandled denials, across sessions.
    var pendingApprovals: Int {
        clients.reduce(0) { $0 + $1.pendingAsks.count + $1.recentDenials.count }
    }
}
