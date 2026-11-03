#!/usr/bin/env bash
# 全 OS 向けの配布用 zip を dist/ に作る
set -euo pipefail
cd "$(dirname "$0")"
VERSION="${1:-$(git describe --tags --always 2>/dev/null || echo dev)}"
rm -rf dist && mkdir -p dist

for target in linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64; do
  os="${target%/*}"; arch="${target#*/}"
  name="phone-deck-${VERSION}-${os}-${arch}"
  ext=""; [ "$os" = windows ] && ext=".exe"
  mkdir -p "dist/$name"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "dist/$name/phone-deck$ext" .
  cp README.md LICENSE "dist/$name/"
  (cd dist && zip -qr "$name.zip" "$name" && rm -rf "$name")
  echo "dist/$name.zip"
done
