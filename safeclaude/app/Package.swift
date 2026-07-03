// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "SafeClaudeBar",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(
            name: "SafeClaudeBar",
            path: "Sources/SafeClaudeBar"
        )
    ]
)
