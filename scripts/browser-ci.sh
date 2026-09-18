#!/usr/bin/env sh
set -eu
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
UPDATE_SNAPSHOTS=${1:-false}
SNAPSHOT_TEST_FILTER=${2:-}
case "$UPDATE_SNAPSHOTS" in true|false) ;; *) echo 'snapshot update must be true or false' >&2; exit 1 ;; esac
[ -z "$SNAPSHOT_TEST_FILTER" ] || [ "$UPDATE_SNAPSHOTS" = true ] || { echo 'Test filtering is allowed only for baseline generation' >&2; exit 1; }
[ "$(uname -s)" = Linux ] || { echo 'Run this browser container on the Linux CI Runner' >&2; exit 1; }
# Match the lockfile and pin the multi-architecture image used for Linux baselines.
PLAYWRIGHT_VERSION=1.62.1
PLAYWRIGHT_IMAGE=mcr.microsoft.com/playwright:v1.62.1-noble@sha256:dcc5531e97840b9b5e794f2814476b21571c5124a3fca2267d73041f56e7580e
LOCK_VERSION=$(node -p "require(process.argv[1]).packages['node_modules/@playwright/test'].version" "$ROOT_DIR/frontend/package-lock.json")
[ "$LOCK_VERSION" = "$PLAYWRIGHT_VERSION" ] || { echo 'Update the pinned browser image with the Playwright lockfile' >&2; exit 1; }
NODE_ROOT=$(node -p 'require("node:path").dirname(require("node:path").dirname(process.execPath))')
GO_ROOT=$(go env GOROOT)
MODULE_CACHE=$(go env GOMODCACHE)
BUILD_CACHE=$(go env GOCACHE)
BROWSER_TASK_ROOT=$(mktemp -d "${RUNNER_TEMP:-/tmp}/cpap-browser.XXXXXX")
cleanup() {
  if [ -s "$BROWSER_TASK_ROOT/container-id" ]; then
    CONTAINER_ID=$(cat "$BROWSER_TASK_ROOT/container-id")
    case "$CONTAINER_ID" in
      *[!a-f0-9]*) ;;
      *) if [ "${#CONTAINER_ID}" -eq 64 ]; then docker rm -f "$CONTAINER_ID" >/dev/null 2>&1 || true; fi ;;
    esac
  fi
  rm -rf -- "$BROWSER_TASK_ROOT"
}
trap cleanup EXIT HUP INT TERM
mkdir -p "$BROWSER_TASK_ROOT/home" "$MODULE_CACHE" "$BUILD_CACHE"
docker pull "$PLAYWRIGHT_IMAGE"
set -- npm --prefix frontend run test:e2e --
if [ "$UPDATE_SNAPSHOTS" = true ]; then
  set -- "$@" --update-snapshots=all
  if [ -n "$SNAPSHOT_TEST_FILTER" ]; then set -- "$@" --grep "$SNAPSHOT_TEST_FILTER"; fi
else
  set -- "$@" --update-snapshots=none
fi
# Do not mount the Docker socket or join business Compose networks. Match the host
# UID so generated evidence and snapshots remain writable by subsequent jobs.
docker run --rm --init --shm-size=2g \
  --cidfile "$BROWSER_TASK_ROOT/container-id" \
  --user "$(id -u):$(id -g)" --workdir /work \
  --mount "type=bind,src=$ROOT_DIR,dst=/work" \
  --mount "type=bind,src=$NODE_ROOT,dst=/opt/node,readonly" \
  --mount "type=bind,src=$GO_ROOT,dst=/opt/go,readonly" \
  --mount "type=bind,src=$MODULE_CACHE,dst=/go-mod" \
  --mount "type=bind,src=$BUILD_CACHE,dst=/go-build" \
  --mount "type=bind,src=$BROWSER_TASK_ROOT/home,dst=/home/browser" \
  --env HOME=/home/browser --env GOMODCACHE=/go-mod --env GOCACHE=/go-build \
  --env PATH=/opt/node/bin:/opt/go/bin:/usr/local/bin:/usr/bin:/bin \
  --env CI=true --env CPAP_E2E_WORKERS=1 --env CGO_ENABLED=0 \
  "$PLAYWRIGHT_IMAGE" sh -c 'set -eu; go build -o /home/browser/test-preview ./cmd/test-preview; exec "$@"' sh "$@"
