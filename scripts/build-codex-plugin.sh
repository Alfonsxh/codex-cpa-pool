#!/usr/bin/env sh
set -eu
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUTPUT=${1:?output directory required}
mkdir -p "$OUTPUT"
OUTPUT=$(CDPATH= cd -- "$OUTPUT" && pwd)
BUILD_DIR=$(mktemp -d)
trap 'rm -rf "$BUILD_DIR"' EXIT HUP INT TERM
curl --fail --location --retry 2 --max-time 120 \
  'https://codeload.github.com/Su-cyber-art/cpa-plugin-codex-ticket/tar.gz/refs/tags/v0.2.0' \
  --output "$BUILD_DIR/source.tar.gz"
printf '%s  %s\n' '1b2aa11077ad3c086391e5c8229417141ea7e7b810c4936942fdd3bea6d0fd46' "$BUILD_DIR/source.tar.gz" | sha256sum --check --status
mkdir "$BUILD_DIR/source"
tar -xzf "$BUILD_DIR/source.tar.gz" --strip-components=1 -C "$BUILD_DIR/source"
cd "$BUILD_DIR/source"
git apply --unidiff-zero --check "$ROOT_DIR/plugins/codex-ticket/direct.patch"
git apply --unidiff-zero "$ROOT_DIR/plugins/codex-ticket/direct.patch"
CGO_ENABLED=1 go test -race ./... -count=1 -timeout=120s
CGO_ENABLED=1 go vet ./...
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 CC="${PLUGIN_CC:-gcc}" go build -trimpath -buildvcs=false -ldflags='-s -w' -buildmode=c-shared -o "$OUTPUT/codex-ticket.so" ./cmd/codex-ticket
cp LICENSE NOTICE THIRD_PARTY_NOTICES.txt "$OUTPUT/"
DIGEST=$(sha256sum "$OUTPUT/codex-ticket.so" | cut -d ' ' -f 1)
printf '{"version":"v0.2.0-ccpa.1","sha256":"%s","upstream":"482ade49bd169f0ef05fec67f297ce636c6fa7e9"}\n' "$DIGEST" > "$OUTPUT/release.json"
