#!/bin/bash

# Build script for macOS app bundle

set -e

APP_NAME="LocalChat"
APP_BUNDLE="${APP_NAME}.app"
BINARY_NAME="LocalChat-bin"
BUILD_DIR="build"
APP_DIR="${BUILD_DIR}/${APP_BUNDLE}"
CONTENTS_DIR="${APP_DIR}/Contents"
MACOS_DIR="${CONTENTS_DIR}/MacOS"

echo "Building ${APP_NAME}..."

# Clean previous build
rm -rf "${BUILD_DIR}"

# Create app bundle structure
mkdir -p "${MACOS_DIR}"

# Build the Go binary
echo "Compiling Go binary..."
go build -o "${MACOS_DIR}/${BINARY_NAME}" ./cmd/main.go

# Make binary executable
chmod +x "${MACOS_DIR}/${BINARY_NAME}"

# Create a launcher script that opens Terminal and runs the app
cat > "${MACOS_DIR}/${APP_NAME}" <<'SCRIPT'
#!/bin/bash
# Get the directory where this script is located
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BINARY="${SCRIPT_DIR}/LocalChat-bin"

# Open Terminal and run the binary
osascript <<EOF
tell application "Terminal"
    activate
    do script "\"${BINARY}\""
end tell
EOF
SCRIPT

chmod +x "${MACOS_DIR}/${APP_NAME}"

# Create Info.plist
cat > "${CONTENTS_DIR}/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>${APP_NAME}</string>
    <key>CFBundleIdentifier</key>
    <string>com.LocalChat.app</string>
    <key>CFBundleName</key>
    <string>${APP_NAME}</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>1.0</string>
    <key>CFBundleVersion</key>
    <string>1</string>
    <key>LSMinimumSystemVersion</key>
    <string>10.13</string>
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>LSUIElement</key>
    <false/>
</dict>
</plist>
EOF

echo ""
echo "✓ Build complete!"
echo "  App bundle: ${APP_DIR}"
echo ""
echo "To run the app:"
echo "  open \"${APP_DIR}\""
echo ""
echo "Or drag ${APP_BUNDLE} to your Applications folder."

