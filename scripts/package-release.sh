#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUTPUT=${1:-"$ROOT_DIR/dist/codex-cpa-pool.tar.gz"}
MANIFEST_FILE="$ROOT_DIR/release-manifest.json"
MANIFEST_CREATED=false

cleanup_manifest_file() {
  if [ "$MANIFEST_CREATED" = true ] && [ -f "$MANIFEST_FILE" ]; then
    [ "$MANIFEST_FILE" = "$ROOT_DIR/release-manifest.json" ] || {
      echo "refusing to remove unexpected release manifest: $MANIFEST_FILE" >&2
      return 1
    }
    rm -f -- "$MANIFEST_FILE"
  fi
}
trap cleanup_manifest_file EXIT HUP INT TERM

case "$OUTPUT" in
  /*) ;;
  *) OUTPUT="$ROOT_DIR/$OUTPUT" ;;
esac

mkdir -p "$(dirname -- "$OUTPUT")"
if [ -e "$MANIFEST_FILE" ]; then
  echo "拒绝覆盖已有发布清单：$MANIFEST_FILE" >&2
  exit 1
fi
go run "$ROOT_DIR/cmd/releasectl" manifest create \
  --root "$ROOT_DIR" \
  --output "$MANIFEST_FILE"
MANIFEST_CREATED=true

COPYFILE_DISABLE=1 tar --no-xattrs \
  --exclude='*/node_modules' \
  --exclude='frontend/dist' \
  --exclude='frontend/coverage' \
  --exclude='frontend/playwright-report' \
  --exclude='frontend/test-results' \
  --exclude='frontend/.env' \
  --exclude='frontend/.env.local' \
  --exclude='frontend/.env.*.local' \
  -czf "$OUTPUT" -C "$ROOT_DIR" \
  .dockerignore \
  .env.example \
  Dockerfile \
  Makefile \
  CHANGELOG.md \
  CODE_OF_CONDUCT.md \
  CONTRIBUTING.md \
  LICENSE \
  README.md \
  README.en.md \
  SECURITY.md \
  api \
  cmd \
  docker-compose.yml \
  docker-compose.test.yml \
  docs \
  frontend \
  go.mod \
  go.sum \
  internal \
  plugins/codex-ticket \
  scripts \
  testdata/preview \
  testdata/runtime \
  tools/openapi \
  release-manifest.json

go run "$ROOT_DIR/cmd/releasectl" archive verify "$OUTPUT"
printf 'release=%s\n' "$OUTPUT"
