#!/usr/bin/env bash
set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "This package script must run on macOS because relay-app uses Wails/WKWebView." >&2
  exit 1
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

APP_NAME="${APP_NAME:-Relay}"
BUNDLE_ID="${BUNDLE_ID:-com.muhaymien96.relay}"
VERSION="${VERSION:-$(tr -d '[:space:]' < "$ROOT/VERSION")}"
ARCHES="${ARCHES:-universal}" # universal, arm64, or amd64
MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-10.13}"

BUILD_DIR="$ROOT/build/macos"
DIST_DIR="$ROOT/dist"
APP_DIR="$DIST_DIR/$APP_NAME.app"
ZIP_PATH="$DIST_DIR/$APP_NAME-macos-$ARCHES.zip"

rm -rf "$BUILD_DIR" "$APP_DIR" "$ZIP_PATH"
mkdir -p "$BUILD_DIR" "$DIST_DIR" "$APP_DIR/Contents/MacOS" "$APP_DIR/Contents/Resources"

build_arch() {
  local arch="$1"
  local out="$BUILD_DIR/$arch/$APP_NAME"

  mkdir -p "$(dirname "$out")"
  GOOS=darwin GOARCH="$arch" CGO_ENABLED=1 \
    CGO_CFLAGS="${CGO_CFLAGS:-} -mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET" \
    CGO_CXXFLAGS="${CGO_CXXFLAGS:-} -mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET" \
    CGO_LDFLAGS="${CGO_LDFLAGS:-} -framework UniformTypeIdentifiers -mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET" \
    go build -trimpath -tags desktop,production \
      -ldflags "-s -w" \
      -o "$out" ./cmd/relay-app
}

case "$ARCHES" in
  arm64|amd64)
    build_arch "$ARCHES"
    cp "$BUILD_DIR/$ARCHES/$APP_NAME" "$APP_DIR/Contents/MacOS/$APP_NAME"
    ;;
  universal)
    build_arch arm64
    build_arch amd64
    lipo -create \
      "$BUILD_DIR/arm64/$APP_NAME" \
      "$BUILD_DIR/amd64/$APP_NAME" \
      -output "$APP_DIR/Contents/MacOS/$APP_NAME"
    ;;
  *)
    echo "ARCHES must be universal, arm64, or amd64" >&2
    exit 2
    ;;
esac

chmod 0755 "$APP_DIR/Contents/MacOS/$APP_NAME"

cat > "$APP_DIR/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key>
  <string>$APP_NAME</string>
  <key>CFBundleDisplayName</key>
  <string>$APP_NAME</string>
  <key>CFBundleIdentifier</key>
  <string>$BUNDLE_ID</string>
  <key>CFBundleExecutable</key>
  <string>$APP_NAME</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleShortVersionString</key>
  <string>$VERSION</string>
  <key>CFBundleVersion</key>
  <string>$VERSION</string>
  <key>LSMinimumSystemVersion</key>
  <string>$MACOSX_DEPLOYMENT_TARGET</string>
  <key>NSHighResolutionCapable</key>
  <true/>
</dict>
</plist>
PLIST

printf 'APPL????' > "$APP_DIR/Contents/PkgInfo"

codesign --force --deep --sign - "$APP_DIR"
ditto -c -k --sequesterRsrc --keepParent "$APP_DIR" "$ZIP_PATH"

echo "Created $ZIP_PATH"
shasum -a 256 "$ZIP_PATH"
