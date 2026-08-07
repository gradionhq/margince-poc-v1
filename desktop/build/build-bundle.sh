#!/usr/bin/env bash
# Assemble Margince.app from the pieces the other build scripts produced.
#
# Run build-postgres.sh, build-valkey.sh and build-app.sh first; this script
# only stages and signs what already exists, so a missing input is an error
# rather than a silently incomplete bundle.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
OUT="$HERE/out"
APP="$OUT/Margince.app"

VERSION="0.1.0"
BUNDLE_ID="com.gradion.margince"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

require() {
  local path="$1" hint="$2"
  if [ ! -e "$path" ]; then
    echo "missing $path — run $hint first" >&2
    exit 1
  fi
}

build_ui() {
  log "building the window app"
  mkdir -p "$APP/Contents/MacOS"
  # macos13 is the floor for the WKWebView APIs used here; naming it keeps a
  # newer SDK from silently raising the minimum an installed Mac must meet.
  swiftc -O -target arm64-apple-macos13.0 \
    -framework AppKit -framework WebKit \
    -o "$APP/Contents/MacOS/Margince" \
    "$ROOT/desktop/ui/main.swift"
}

write_plist() {
  cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key><string>Margince</string>
  <key>CFBundleIdentifier</key><string>$BUNDLE_ID</string>
  <key>CFBundleName</key><string>Margince</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>CFBundleVersion</key><string>$VERSION</string>
  <key>LSMinimumSystemVersion</key><string>13.0</string>
  <key>NSHighResolutionCapable</key><true/>
  <!-- The stack is reached only over loopback, which App Transport Security
       blocks by default because it is plain HTTP. Terminating TLS between
       two processes on the same machine would buy nothing, so the narrow
       local-networking exemption is used instead of disabling ATS. -->
  <key>NSAppTransportSecurity</key>
  <dict><key>NSAllowsLocalNetworking</key><true/></dict>
</dict>
</plist>
PLIST
}

stage_resources() {
  log "staging resources"
  local res="$APP/Contents/Resources"
  mkdir -p "$res"
  cp -R "$OUT/pgsql" "$res/pgsql"
  cp "$OUT/valkey/valkey-server" "$res/valkey-server"
  cp "$OUT/bin/api" "$OUT/bin/worker" "$OUT/bin/migrate" "$res/"
  cp "$OUT/bin/Margince" "$res/margince-launcher"
  cp -R "$OUT/web" "$res/web"
}

sign() {
  # Ad-hoc signing only. A shipped bundle needs a Developer ID plus
  # notarization, without which a downloaded copy is quarantined and the
  # first double-click reports that the developer cannot be verified.
  log "signing (ad-hoc — NOT release signing)"
  codesign --force --deep --sign - "$APP" >/dev/null 2>&1
  codesign --verify --deep "$APP"
}

main() {
  require "$OUT/pgsql/bin/postgres" "build-postgres.sh"
  require "$OUT/valkey/valkey-server" "build-valkey.sh"
  require "$OUT/bin/api" "build-app.sh"
  require "$OUT/web/index.html" "build-app.sh"

  rm -rf "$APP"
  build_ui
  write_plist
  stage_resources
  sign

  log "built $APP ($(du -sh "$APP" | awk '{print $1}'))"
}

main "$@"
