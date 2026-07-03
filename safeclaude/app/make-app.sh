#!/usr/bin/env bash
# Build SafeClaudeBar with SPM and wrap it into a proper .app bundle so that
# LSUIElement (no Dock icon) and UserNotifications work.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE"

swift build -c release

APP="$HERE/SafeClaude.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

cp ".build/release/SafeClaudeBar" "$APP/Contents/MacOS/SafeClaude"

cat > "$APP/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key><string>SafeClaude</string>
    <key>CFBundleIdentifier</key><string>ai.voitta.safeclaude</string>
    <key>CFBundleName</key><string>SafeClaude</string>
    <key>CFBundleDisplayName</key><string>SafeClaude</string>
    <key>CFBundlePackageType</key><string>APPL</string>
    <key>CFBundleShortVersionString</key><string>0.1.0</string>
    <key>CFBundleVersion</key><string>1</string>
    <key>LSMinimumSystemVersion</key><string>14.0</string>
    <key>LSUIElement</key><true/>
    <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST

codesign --force --sign - "$APP"
echo "Built: $APP"
