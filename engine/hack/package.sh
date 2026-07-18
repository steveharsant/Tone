#!/usr/bin/env bash
# Builds a Debian package for the Tone engine using plain dpkg-deb.
# Output: engine/dist/tone_<version>_amd64.deb
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION=$(grep -oP 'const version = "\K[^"]+' cmd/tone/main.go)
ARCH=amd64
STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT

echo "Building tone v${VERSION} (${ARCH})…"
CGO_ENABLED=0 GOARCH=$ARCH go build -trimpath -ldflags='-s -w' -o "$STAGE/usr/bin/tone" ./cmd/tone 2>/dev/null || {
  mkdir -p "$STAGE/usr/bin"
  CGO_ENABLED=0 GOARCH=$ARCH go build -trimpath -ldflags='-s -w' -o "$STAGE/usr/bin/tone" ./cmd/tone
}

mkdir -p "$STAGE/DEBIAN" "$STAGE/usr/share/applications" \
  "$STAGE/usr/share/icons/hicolor/32x32/apps" "$STAGE/usr/share/icons/hicolor/128x128/apps"
cp internal/tray/icon.png "$STAGE/usr/share/icons/hicolor/32x32/apps/tone.png"
cp internal/tray/appicon.png "$STAGE/usr/share/icons/hicolor/128x128/apps/tone.png"

cat > "$STAGE/usr/share/applications/tone.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Tone
Comment=Local AI writing assistant
Exec=/usr/bin/tone -open
Icon=tone
Terminal=false
Categories=Utility;Office;
EOF

cat > "$STAGE/DEBIAN/control" <<EOF
Package: tone
Version: ${VERSION}
Architecture: ${ARCH}
Maintainer: Steve Harsant <https://github.com/steveharsant/Tone>
Section: utils
Priority: optional
Homepage: https://github.com/steveharsant/Tone
Description: Local AI writing assistant engine
 Grammar, clarity and style suggestions powered by a local LLM.
 Pairs with the Tone browser extension; nothing leaves your machine.
EOF

mkdir -p dist
dpkg-deb --build --root-owner-group "$STAGE" "dist/tone_${VERSION}_${ARCH}.deb"
echo "Wrote dist/tone_${VERSION}_${ARCH}.deb"
