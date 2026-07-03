import SwiftUI
import WebKit

/// Renders a session report HTML file inside the dashboard window.
struct ReportWebView: NSViewRepresentable {
    let url: URL
    let readAccess: URL

    func makeNSView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        let view = WKWebView(frame: .zero, configuration: config)
        view.setValue(false, forKey: "drawsBackground") // let report's own bg rule
        view.loadFileURL(url, allowingReadAccessTo: readAccess)
        return view
    }

    func updateNSView(_ view: WKWebView, context: Context) {
        if view.url != url {
            view.loadFileURL(url, allowingReadAccessTo: readAccess)
        }
    }
}
