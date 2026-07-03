import Foundation
import Network
import UserNotifications
import AppKit

/// Talks JSON-lines to one engine's unix control socket — one instance per
/// live session. Created and destroyed by SessionsManager as sessions
/// come and go.
@MainActor
final class EngineClient: ObservableObject, Identifiable {
    let name: String          // session name == mount folder name
    let socketPath: String
    nonisolated var id: String { name }

    @Published var connected = false
    @Published var stats: SessionStats = .empty
    @Published var rules: RuleSet = RuleSet(read: [:], write: [:])
    @Published var recentDenials: [EngineEvent] = []
    @Published var pendingAsks: [PendingAsk] = []

    private var conn: NWConnection?
    private var buffer = Data()
    private var statsTimer: Timer?
    private var stopped = false
    private var reportCallbacks: [(String) -> Void] = []

    var repoName: String {
        stats.repo.split(separator: "/").last.map(String.init) ?? name
    }

    init(name: String, socketPath: String) {
        self.name = name
        self.socketPath = socketPath
        connect()
        statsTimer = Timer.scheduledTimer(withTimeInterval: 2.0, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.tick() }
        }
    }

    func shutdown() {
        stopped = true
        statsTimer?.invalidate()
        conn?.cancel()
        connected = false
    }

    private func tick() {
        guard !stopped else { return }
        if connected {
            send(["cmd": "stats"])
            send(["cmd": "pending"]) // re-sync asks (also drops expired ones)
        } else {
            connect()
        }
    }

    private func connect() {
        guard !stopped else { return }
        conn?.cancel()
        buffer.removeAll()
        let c = NWConnection(to: .unix(path: socketPath), using: .tcp)
        conn = c
        c.stateUpdateHandler = { [weak self] state in
            Task { @MainActor in
                guard let self else { return }
                switch state {
                case .ready:
                    self.connected = true
                    self.send(["cmd": "subscribe"])
                    self.send(["cmd": "get_rules"])
                    self.send(["cmd": "stats"])
                case .failed, .cancelled:
                    self.connected = false
                default: break
                }
            }
        }
        receiveLoop(c)
        c.start(queue: .main)
    }

    private func receiveLoop(_ c: NWConnection) {
        c.receive(minimumIncompleteLength: 1, maximumLength: 1 << 16) { [weak self] data, _, done, err in
            Task { @MainActor in
                guard let self else { return }
                if let data { self.buffer.append(data); self.drainLines() }
                if done || err != nil {
                    self.connected = false
                    return
                }
                self.receiveLoop(c)
            }
        }
    }

    private func drainLines() {
        while let nl = buffer.firstIndex(of: 0x0A) {
            let line = buffer[buffer.startIndex..<nl]
            buffer.removeSubrange(buffer.startIndex...nl)
            if !line.isEmpty { handle(Data(line)) }
        }
    }

    private static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { dec in
            let s = try dec.singleValueContainer().decode(String.self)
            let f = ISO8601DateFormatter()
            f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let d = f.date(from: s) { return d }
            f.formatOptions = [.withInternetDateTime]
            return f.date(from: s) ?? .now
        }
        return d
    }()

    private struct Reply: Codable {
        let type: String
        let stats: SessionStats?
        let rules: RuleSet?
        let event: EngineEvent?
        let report: String?
        let ask: PendingAsk?
        let asks: [PendingAsk]?
    }

    private func handle(_ line: Data) {
        guard let r = try? Self.decoder.decode(Reply.self, from: line) else { return }
        switch r.type {
        case "stats": if let s = r.stats { stats = s }
        case "rules": if let ru = r.rules { rules = ru }
        case "report":
            if let p = r.report {
                if reportCallbacks.isEmpty {
                    NSWorkspace.shared.open(URL(fileURLWithPath: p))
                } else {
                    reportCallbacks.removeFirst()(p)
                }
            }
        case "ask":
            if let a = r.ask {
                pendingAsks.removeAll { $0.id == a.id }
                pendingAsks.insert(a, at: 0)
                NotificationCenter.default.post(name: .safeclaudeDenial, object: name)
                postAskNotification(a)
            }
        case "pending":
            pendingAsks = r.asks ?? []
        case "event":
            if let e = r.event {
                if e.action == "deny" {
                    // collapse repeats of the same op+path into the newest
                    recentDenials.removeAll { $0.path == e.path && $0.op == e.op }
                    recentDenials.insert(e, at: 0)
                    if recentDenials.count > 10 { recentDenials.removeLast() }
                    NotificationCenter.default.post(name: .safeclaudeDenial, object: name)
                }
                if e.notify || e.severity == "critical" {
                    postNotification(e)
                }
            }
        default: break
        }
    }

    private func postAskNotification(_ a: PendingAsk) {
        let content = UNMutableNotificationContent()
        content.title = "SafeClaude: approval needed"
        content.subtitle = "Session: \(name) — agent is waiting"
        content.body = "\(a.op) \(a.path)"
        content.sound = .default
        UNUserNotificationCenter.current().add(
            UNNotificationRequest(identifier: "ask-\(name)-\(a.id)", content: content, trigger: nil))
    }

    private func postNotification(_ e: EngineEvent) {
        let content = UNMutableNotificationContent()
        content.title = e.severity == "critical"
            ? "Critical: agent-file write blocked"
            : "SafeClaude blocked an access"
        content.subtitle = "Session: \(name)"
        content.body = "\(e.op) \(e.path)"
        content.sound = .default
        UNUserNotificationCenter.current().add(
            UNNotificationRequest(identifier: "sc-\(name)-\(e.seq)", content: content, trigger: nil))
    }

    // MARK: commands

    private func send(_ obj: [String: Any]) {
        guard let conn, let data = try? JSONSerialization.data(withJSONObject: obj) else { return }
        conn.send(content: data + Data([0x0A]), completion: .contentProcessed { _ in })
    }

    /// Answer a pending Ask. The parked file operation completes (or fails)
    /// within the client's next retry (~5s). remember=true persists an
    /// exact-path override for the rest of the session.
    func resolve(ask: PendingAsk, allow: Bool, remember: Bool) {
        send(["cmd": "resolve", "id": ask.id, "allow": allow, "remember": remember])
        pendingAsks.removeAll { $0.id == ask.id }
    }

    func setRule(axis: String, category: String, action: String) {
        send(["cmd": "set_rule", "axis": axis, "category": category, "action": action])
    }

    func approve(path: String) {
        send(["cmd": "approve", "path": path])
        recentDenials.removeAll { $0.path == path }
    }

    /// Keep it blocked, but stop re-alerting for this exact path.
    func deny(path: String) {
        send(["cmd": "deny", "path": path])
        recentDenials.removeAll { $0.path == path }
    }

    func openReport() {
        send(["cmd": "report"])
    }

    /// Render the session report and deliver its path to the UI (instead of
    /// launching the browser).
    func requestReport(_ completion: @escaping (String) -> Void) {
        reportCallbacks.append(completion)
        send(["cmd": "report"])
    }
}
