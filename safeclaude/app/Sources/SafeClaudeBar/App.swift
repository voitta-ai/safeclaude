import SwiftUI
import Combine
import UserNotifications

@main
struct SafeClaudeBarApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var delegate

    var body: some Scene {
        // No SwiftUI windows — the status item + dashboard window are
        // managed by AppDelegate so we control drag/resize/persistence.
        Settings { EmptyView() }
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private var manager: SessionsManager!
    private var statusItem: NSStatusItem!
    private var window: NSWindow?
    private var iconSub: AnyCancellable?

    func applicationDidFinishLaunching(_ notification: Notification) {
        manager = SessionsManager()

        UNUserNotificationCenter.current()
            .requestAuthorization(options: [.alert, .sound]) { _, _ in }

        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        statusItem.button?.target = self
        statusItem.button?.action = #selector(toggleWindow)
        updateIcon()

        // SessionsManager republishes its clients' changes often enough that
        // piggybacking on objectWillChange keeps the icon current.
        iconSub = manager.objectWillChange
            .receive(on: DispatchQueue.main)
            .sink { [weak self] _ in self?.updateIcon() }

        // A denial means the agent is blocked and possibly waiting on a
        // permission decision — surface the dashboard immediately.
        NotificationCenter.default.addObserver(
            forName: .safeclaudeDenial, object: nil, queue: .main
        ) { [weak self] _ in
            Task { @MainActor in self?.raiseForAttention() }
        }
    }

    private var lastRaise = Date.distantPast

    private func raiseForAttention() {
        updateIcon() // orange alert icon immediately, even between poll ticks
        // Denial bursts (e.g. git polling a blocked .git/ every 2s) must not
        // re-yank the window on every event.
        guard Date().timeIntervalSince(lastRaise) > 10 else { return }
        lastRaise = Date()
        showWindow()
        // Float above everything (incl. fullscreen apps) until the user
        // interacts — a pending permission decision shouldn't hide.
        window?.level = .floating
    }

    private func updateIcon() {
        // Pending approvals get a colored alert icon — the one attention
        // channel that can't be lost (notifications are unreliable for
        // ad-hoc-signed apps; there is no Dock icon to bounce).
        if manager.pendingApprovals > 0 {
            let cfg = NSImage.SymbolConfiguration(paletteColors: [.white, .systemOrange])
            let img = NSImage(systemSymbolName: "exclamationmark.shield.fill",
                              accessibilityDescription: "SafeClaude — approval needed")?
                .withSymbolConfiguration(cfg)
            img?.isTemplate = false // keep the orange
            statusItem.button?.image = img
            return
        }
        let symbol = !manager.anyConnected ? "shield.slash"
            : manager.totalDenials > 0 ? "shield.lefthalf.filled.badge.checkmark"
            : "shield.lefthalf.filled"
        let img = NSImage(systemSymbolName: symbol, accessibilityDescription: "SafeClaude")
        img?.isTemplate = true // adapt to menu bar light/dark
        statusItem.button?.image = img
    }

    @objc private func toggleWindow() {
        if let w = window, w.isVisible {
            // Hide only when it's truly frontmost; if it's buried under
            // other apps' windows, the click should surface it instead.
            if NSApp.isActive && w.isKeyWindow {
                w.orderOut(nil)
            } else {
                w.makeKeyAndOrderFront(nil)
                NSApp.activate(ignoringOtherApps: true)
            }
            return
        }
        showWindow()
    }

    private func showWindow() {
        if window == nil {
            let w = NSWindow(
                contentRect: NSRect(x: 0, y: 0, width: 820, height: 620),
                styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView],
                backing: .buffered, defer: false)
            w.title = "SafeClaude"
            w.titleVisibility = .hidden
            w.titlebarAppearsTransparent = true
            w.isMovableByWindowBackground = true
            w.isReleasedWhenClosed = false
            w.minSize = NSSize(width: 640, height: 440)
            // Follow the user to whatever Space they're on — including another
            // app's fullscreen Space (the common "I'm in fullscreen VS Code /
            // terminal" case). Without this the window opens on the desktop
            // Space and looks lost.
            w.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary]
            w.contentView = NSHostingView(rootView: DashboardView(manager: manager))

            // Remember size & position across opens and app restarts.
            w.setFrameAutosaveName("SafeClaudeDashboard")
            if !w.setFrameUsingName("SafeClaudeDashboard") {
                positionNearStatusItem(w) // first launch: drop below the icon
            }
            // Once the user engages with the window, stop floating.
            NotificationCenter.default.addObserver(
                forName: NSWindow.didResignKeyNotification, object: w, queue: .main
            ) { _ in
                Task { @MainActor in w.level = .normal }
            }
            window = w
        }
        window!.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    private func positionNearStatusItem(_ w: NSWindow) {
        guard let btn = statusItem.button, let screen = btn.window?.screen else {
            w.center()
            return
        }
        let btnFrame = btn.window?.convertToScreen(btn.frame) ?? .zero
        var origin = NSPoint(x: btnFrame.midX - w.frame.width / 2,
                             y: btnFrame.minY - w.frame.height - 8)
        origin.x = min(max(origin.x, screen.visibleFrame.minX + 8),
                       screen.visibleFrame.maxX - w.frame.width - 8)
        w.setFrameOrigin(origin)
    }
}
