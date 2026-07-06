import SwiftUI

/// The Drive-style panel shown when the menu bar icon is clicked.
/// Header + tab strip + content; sessions never pile up as menu items —
/// they're cards in a scrollable list.
struct DashboardView: View {
    @ObservedObject var manager: SessionsManager

    enum Tab: String, CaseIterable {
        case sessions = "Sessions"
        case policy = "Session Policy"
        case defaults = "Default Policy"
        case reports = "Reports"
    }

    @State private var tab: Tab = .sessions
    @State private var selectedReport: URL?

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            Picker("", selection: $tab) {
                ForEach(Tab.allCases, id: \.self) { Text($0.rawValue).tag($0) }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .padding(.horizontal, 16)
            .padding(.vertical, 10)

            Group {
                switch tab {
                case .sessions:
                    SessionsTab(manager: manager) { path in
                        selectedReport = URL(fileURLWithPath: path)
                        tab = .reports
                    }
                case .policy:
                    PolicyTab(manager: manager)
                case .defaults:
                    DefaultsTab()
                case .reports:
                    ReportsTab(selected: $selectedReport)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .frame(minWidth: 860, minHeight: 440)
        .onReceive(NotificationCenter.default.publisher(for: .safeclaudeDenial)) { _ in
            tab = .sessions // a decision is pending — show it
        }
    }

    private var header: some View {
        HStack(spacing: 10) {
            Image(systemName: manager.anyConnected
                  ? "shield.lefthalf.filled" : "shield.slash")
                .font(.title2)
                .foregroundStyle(manager.anyConnected ? Color.accentColor : .secondary)
            VStack(alignment: .leading, spacing: 1) {
                Text("SafeClaude").font(.headline)
                Text(statusLine).font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            Button {
                NSApp.terminate(nil)
            } label: {
                Image(systemName: "power")
            }
            .buttonStyle(.borderless)
            .help("Quit SafeClaude")
        }
        .padding(.leading, 78) // clear the traffic lights (transparent titlebar)
        .padding(.trailing, 16)
        .padding(.vertical, 12)
    }

    private var statusLine: String {
        let n = manager.clients.count
        if n == 0 { return "No active sessions" }
        let d = manager.totalDenials
        return "\(n) session\(n == 1 ? "" : "s") · \(d) denial\(d == 1 ? "" : "s")"
    }
}

// MARK: - Sessions

private struct SessionsTab: View {
    @ObservedObject var manager: SessionsManager
    let showReport: (String) -> Void

    var body: some View {
        if manager.clients.isEmpty {
            VStack(spacing: 10) {
                Image(systemName: "checkmark.shield")
                    .font(.system(size: 42)).foregroundStyle(.secondary)
                Text("You're all caught up!").font(.title3.bold())
                Text("Start a guarded session with:")
                    .foregroundStyle(.secondary)
                Text("safeclaude <repo-dir>")
                    .font(.system(.callout, design: .monospaced))
                    .padding(6).background(.quaternary, in: RoundedRectangle(cornerRadius: 6))
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else {
            ScrollView {
                VStack(spacing: 12) {
                    ForEach(manager.clients) { client in
                        SessionCard(client: client, showReport: showReport) { wipeClaude in
                            manager.delete(client, wipeClaudeState: wipeClaude)
                        }
                    }
                }
                .padding(16)
            }
        }
    }
}

private struct SessionCard: View {
    @ObservedObject var client: EngineClient
    let showReport: (String) -> Void
    let onDelete: (_ wipeClaudeState: Bool) -> Void
    @State private var confirmDelete = false

    private var artifactReads: UInt64 {
        allCategories.filter(\.isArtifact)
            .reduce(0) { $0 + (client.stats.byCategory[$1.key] ?? 0) }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Circle()
                    .fill(client.connected ? .green : .orange)
                    .frame(width: 9, height: 9)
                Text(client.name).font(.headline)
                Text(client.stats.repo)
                    .font(.caption).foregroundStyle(.secondary)
                    .lineLimit(1).truncationMode(.middle)
                Spacer()
                Button("View report") {
                    client.requestReport(showReport)
                }
                .disabled(!client.connected)
                Button {
                    confirmDelete = true
                } label: {
                    Image(systemName: "trash")
                }
                .help("End this session and erase all its data")
                .confirmationDialog(
                    "Delete session “\(client.name)”?",
                    isPresented: $confirmDelete, titleVisibility: .visible
                ) {
                    Button("Delete session data", role: .destructive) { onDelete(false) }
                    Button("Delete + Claude Code history", role: .destructive) { onDelete(true) }
                    Button("Cancel", role: .cancel) {}
                } message: {
                    Text("Force-ends the session (any attached shell loses the mount) and erases its policy, audit logs and reports — the next run starts fresh from the default policy. The second option also wipes Claude Code's conversation history for this mount. Repo files are not touched.")
                }
            }

            HStack(spacing: 18) {
                stat("\(client.stats.totalOps)", "operations")
                stat("\(client.stats.denials)", "denied")
                stat("\(client.stats.hidden)", "hidden")
                stat("\(artifactReads)", "agent-file reads",
                     highlight: artifactReads > 0)
                Spacer()
            }

            if !client.pendingAsks.isEmpty {
                Divider()
                Label("WAITING FOR YOUR DECISION — the agent is paused right now",
                      systemImage: "hourglass")
                    .font(.caption.bold()).foregroundStyle(.red)
                    .symbolEffect(.pulse)
                ForEach(client.pendingAsks) { a in
                    DecisionCard(mode: .paused(a), client: client)
                }
            }

            if !client.recentDenials.isEmpty {
                Divider()
                Label("ALREADY DENIED — set a permanent rule for these paths",
                      systemImage: "exclamationmark.triangle.fill")
                    .font(.caption.bold()).foregroundStyle(.orange)
                ForEach(client.recentDenials) { e in
                    DecisionCard(mode: .denied(e), client: client)
                }
            }
        }
        .padding(14)
        .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 10))
        .overlay {
            if !client.pendingAsks.isEmpty {
                RoundedRectangle(cornerRadius: 10)
                    .strokeBorder(.red.opacity(0.8), lineWidth: 2)
            } else if !client.recentDenials.isEmpty {
                RoundedRectangle(cornerRadius: 10)
                    .strokeBorder(.orange.opacity(0.7), lineWidth: 2)
            }
        }
    }

    private func stat(_ value: String, _ label: String, highlight: Bool = false) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            Text(value).font(.title3.bold())
                .foregroundStyle(highlight ? Color.orange : Color.primary)
            Text(label).font(.caption2).foregroundStyle(.secondary)
        }
    }
}

// MARK: - Decision card (one consistent approval UI for both states)

/// Two moments, one visual language:
///  • .paused — the operation is blocked right now, waiting on a decision.
///  • .denied — it already failed; only a persistent rule can change that.
private enum DecisionMode {
    case paused(PendingAsk)
    case denied(EngineEvent)
}

private struct DecisionCard: View {
    let mode: DecisionMode
    let client: EngineClient

    private var category: String {
        switch mode {
        case .paused(let a): return a.category
        case .denied(let e): return e.category
        }
    }
    private var op: String {
        switch mode {
        case .paused(let a): return a.op
        case .denied(let e): return e.op
        }
    }
    private var path: String {
        switch mode {
        case .paused(let a): return a.path
        case .denied(let e): return e.path
        }
    }

    var body: some View {
        let cat = categoryInfo(category)
        let accent: Color = cat.isDangerous ? .red : .orange

        VStack(alignment: .leading, spacing: 9) {
            HStack(alignment: .top, spacing: 11) {
                // color-coded category badge
                Image(systemName: cat.icon)
                    .font(.system(size: 15, weight: .semibold))
                    .foregroundStyle(.white)
                    .frame(width: 30, height: 30)
                    .background(accent.gradient, in: RoundedRectangle(cornerRadius: 7))

                VStack(alignment: .leading, spacing: 3) {
                    HStack(spacing: 6) {
                        Text(cat.label.uppercased())
                            .font(.caption2.bold()).foregroundStyle(accent)
                        if cat.isDangerous {
                            Text("SENSITIVE")
                                .font(.system(size: 8.5, weight: .heavy))
                                .foregroundStyle(.white)
                                .padding(.horizontal, 4).padding(.vertical, 1)
                                .background(accent, in: Capsule())
                        }
                    }
                    (Text("The agent wants to ") + Text(opVerb(op)).bold() + Text(" this file:"))
                        .font(.callout)
                    Text(path)
                        .font(.system(.caption, design: .monospaced))
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                        .lineLimit(2).truncationMode(.middle)
                    Text(cat.risk)
                        .font(.caption2).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                Spacer(minLength: 0)
            }

            HStack(spacing: 8) {
                statusLabel(accent: accent)
                Spacer()
                actions
            }
        }
        .padding(11)
        .background(accent.opacity(0.07), in: RoundedRectangle(cornerRadius: 9))
        .overlay {
            RoundedRectangle(cornerRadius: 9).strokeBorder(accent.opacity(0.4), lineWidth: 1)
        }
    }

    @ViewBuilder private func statusLabel(accent: Color) -> some View {
        switch mode {
        case .paused(let a):
            Label { Text("auto-deny \(a.expires, style: .relative)") }
                icon: { Image(systemName: "clock") }
                .font(.caption2).foregroundStyle(accent).monospacedDigit()
        case .denied(let e):
            Label { Text("denied \(e.time, style: .relative) ago") }
                icon: { Image(systemName: "xmark.circle") }
                .font(.caption2).foregroundStyle(.secondary).monospacedDigit()
        }
    }

    @ViewBuilder private var actions: some View {
        switch mode {
        case .paused(let a):
            // The op is live: full ladder from one-shot to persistent.
            Button("Deny") { client.resolve(ask: a, allow: false, remember: false) }
                .tint(.red)
                .help("Fail this operation now")
            Button("Allow once") { client.resolve(ask: a, allow: true, remember: false) }
                .help("Permit just this operation")
            Button("Always allow") { client.resolve(ask: a, allow: true, remember: true) }
                .buttonStyle(.borderedProminent).tint(.green)
                .help("Permit and stop asking for this exact path this session")
        case .denied(let e):
            // The op already failed: only persistent rules are meaningful.
            Button("Always deny") { client.deny(path: e.path) }
                .tint(.red)
                .help("Pin this path to blocked and stop alerting")
            Button("Always allow") { client.approve(path: e.path) }
                .buttonStyle(.borderedProminent).tint(.green)
                .help("Allow this exact path for the rest of the session")
        }
    }
}

// MARK: - Policy grids (shared by Session Policy and Default Policy tabs)

private struct PolicyGrids: View {
    let rules: RuleSet
    let onChange: (_ axis: String, _ category: String, _ action: String) -> Void
    let onMeta: (_ name: String, _ value: String) -> Void

    private var meta: MetaControls { rules.meta ?? .factory }

    var body: some View {
        HStack(alignment: .top, spacing: 32) {
            grid(axis: "read", title: "Read policy", table: rules.read)
            grid(axis: "write", title: "Write policy", table: rules.write)
            metaColumn
        }
    }

    /// Third column: switches that sit between the per-path Approve/Deny
    /// overrides and the category grids (overrides > meta > grid).
    private var metaColumn: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Meta controls").font(.headline)

            Toggle("Allow Claude writes", isOn: Binding(
                get: { meta.allowClaudeWrites ?? true },
                set: { onMeta("allow_claude_writes", $0 ? "on" : "off") }
            ))
            .toggleStyle(.switch)
            .controlSize(.small)
            .help("Allow every write to Claude files — skills, hooks, commands, agents, settings, memory, MCP config. When off, the write grid decides.")

            ForEach(metaSwitches, id: \.key) { s in
                VStack(alignment: .leading, spacing: 3) {
                    Text(s.label).font(.caption)
                    Picker("", selection: Binding(
                        get: { meta.mode(for: s.key) },
                        set: { onMeta(s.key, $0) }
                    )) {
                        ForEach(metaModeChoices, id: \.key) { c in
                            Text(c.label).tag(c.key)
                        }
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                    .frame(width: 180)
                }
                .help(s.help)
            }

            Text("Self = not yet in git, or last commit is yours.\nMeta beats the grids; per-path Approve/Deny beats both.")
                .font(.caption2).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .frame(width: 180, alignment: .leading)
        }
    }

    private func grid(axis: String, title: String, table: [String: String]) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title).font(.headline)
            Grid(alignment: .leading, horizontalSpacing: 14, verticalSpacing: 6) {
                ForEach(allCategories, id: \.key) { cat in
                    GridRow {
                        HStack(spacing: 5) {
                            if cat.isArtifact {
                                Image(systemName: "diamond.fill")
                                    .font(.system(size: 7))
                                    .foregroundStyle(Color.accentColor)
                            }
                            Text(cat.label)
                        }
                        .gridColumnAlignment(.leading)

                        Picker("", selection: Binding(
                            get: { table[cat.key] ?? "notify" },
                            set: { onChange(axis, cat.key, $0) }
                        )) {
                            ForEach(actionChoices, id: \.key) { c in
                                Text(c.label).tag(c.key)
                            }
                        }
                        .labelsHidden()
                        .frame(width: 160)
                    }
                }
            }
        }
    }
}

// MARK: - Session Policy (per-session, inherited from defaults at start)

private struct PolicyTab: View {
    @ObservedObject var manager: SessionsManager
    @State private var selection: String = ""

    private var client: EngineClient? {
        manager.clients.first { $0.name == selection } ?? manager.clients.first
    }

    var body: some View {
        if manager.clients.isEmpty {
            VStack(spacing: 8) {
                Text("No active session.").foregroundStyle(.secondary)
                Text("Session policy is edited live; new sessions start from the Default Policy tab's template.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if let client {
            SessionPolicyPane(manager: manager, client: client, selection: $selection)
        }
    }
}

private struct SessionPolicyPane: View {
    @ObservedObject var manager: SessionsManager
    @ObservedObject var client: EngineClient
    @Binding var selection: String

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                HStack {
                    Text("Session").font(.headline)
                    Picker("", selection: $selection) {
                        ForEach(manager.clients) { c in
                            Text(c.name).tag(c.name)
                        }
                    }
                    .labelsHidden()
                    .frame(width: 220)
                }

                Text("These rules apply to this session only, took their initial values from the default policy when the session started, and take effect immediately — no remount.")
                    .font(.caption).foregroundStyle(.secondary)

                PolicyGrids(rules: client.rules, onChange: { axis, category, action in
                    client.setRule(axis: axis, category: category, action: action)
                }, onMeta: { name, value in
                    client.setMeta(name: name, value: value)
                })
            }
            .padding(16)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .onAppear { if selection.isEmpty { selection = client.name } }
    }
}

// MARK: - Default Policy (template for new sessions)

private struct DefaultsTab: View {
    @StateObject private var store = DefaultPolicyStore()

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                Text("The template every new session inherits at start. Factory state blocks & notifies on everything — open up what you trust. Changing it does not affect sessions already running (use Session Policy for those).")
                    .font(.caption).foregroundStyle(.secondary)

                PolicyGrids(rules: store.rules, onChange: { axis, category, action in
                    store.set(axis: axis, category: category, action: action)
                }, onMeta: { name, value in
                    store.setMeta(name: name, value: value)
                })
            }
            .padding(16)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .onAppear { store.load() }
    }
}

// MARK: - Reports

private struct ReportsTab: View {
    @Binding var selected: URL?
    @State private var reports: [URL] = []

    private var reportsDir: URL {
        FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".safeclaude/reports")
    }

    var body: some View {
        HSplitView {
            List(reports, id: \.self, selection: $selected) { url in
                VStack(alignment: .leading, spacing: 2) {
                    Text(sessionName(url)).font(.callout.bold())
                    Text(dateLabel(url)).font(.caption).foregroundStyle(.secondary)
                }
                .tag(url)
            }
            .frame(minWidth: 190, idealWidth: 210, maxWidth: 280)

            Group {
                if let url = selected {
                    ReportWebView(url: url, readAccess: reportsDir)
                } else {
                    Text(reports.isEmpty
                         ? "No reports yet — they render when a session ends,\nor via a session card's “View report”."
                         : "Select a report")
                        .multilineTextAlignment(.center)
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            }
        }
        .onAppear(perform: refresh)
    }

    private func refresh() {
        let fm = FileManager.default
        let urls = (try? fm.contentsOfDirectory(
            at: reportsDir, includingPropertiesForKeys: [.contentModificationDateKey])) ?? []
        reports = urls
            .filter { $0.pathExtension == "html" }
            .sorted { (modDate($0) ?? .distantPast) > (modDate($1) ?? .distantPast) }
        if selected == nil { selected = reports.first }
    }

    private func modDate(_ u: URL) -> Date? {
        try? u.resourceValues(forKeys: [.contentModificationDateKey]).contentModificationDate
    }

    // filenames: session-<name>-<yyyymmdd-hhmmss>.html
    private func sessionName(_ u: URL) -> String {
        let parts = u.deletingPathExtension().lastPathComponent.split(separator: "-")
        guard parts.count >= 3 else { return u.lastPathComponent }
        return parts.dropFirst().dropLast(2).joined(separator: "-")
    }

    private func dateLabel(_ u: URL) -> String {
        guard let d = modDate(u) else { return "" }
        return d.formatted(date: .abbreviated, time: .shortened)
    }
}
