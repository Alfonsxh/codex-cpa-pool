#!/usr/bin/env sh
set -eu
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/cpap-local-publish-test.XXXXXX")
trap 'rm -rf -- "$TEST_ROOT"' EXIT HUP INT TERM
mkdir -p "$TEST_ROOT/bin" "$TEST_ROOT/source/scripts" "$TEST_ROOT/source/frontend" "$TEST_ROOT/source/tools/openapi"
cp "$ROOT_DIR/scripts/local-release.sh" "$TEST_ROOT/source/scripts/local-release.sh"
cp "$ROOT_DIR/scripts/telegram-release.mjs" "$TEST_ROOT/source/scripts/telegram-release.mjs"
printf '%s\n' '{}' >"$TEST_ROOT/source/frontend/package.json"
printf '%s\n' '{}' >"$TEST_ROOT/source/tools/openapi/package.json"
printf '%s\n' '# validation fixture' >"$TEST_ROOT/source/scripts/release-validation.mjs"
cat >"$TEST_ROOT/source/scripts/release-images.sh" <<'EOF'
#!/usr/bin/env sh
set -eu
printf 'images %s\n' "$VERSION" >>"$FIXTURE_LOG"
test -z "${RELEASE_VALIDATION_CACHE:-}"
EOF
cat >"$TEST_ROOT/source/scripts/package-release.sh" <<'EOF'
#!/usr/bin/env sh
set -eu
printf 'archive\n' >"$1"
EOF
printf '%s\n' '#!/usr/bin/env sh' >"$TEST_ROOT/source/scripts/run.sh"
cat >"$TEST_ROOT/bin/gh" <<'EOF'
#!/usr/bin/env sh
set -eu
printf 'gh %s\n' "$*" >>"$FIXTURE_LOG"
case "$*" in
  'auth status '*) exit 0 ;;
  'release view '*) printf 'true\n' ;;
  'release upload '*) exit 0 ;;
  'workflow run '*) exit 0 ;;
  'release edit '*)
    if [ -f "$FIXTURE_FAIL_ONCE" ]; then
      rm "$FIXTURE_FAIL_ONCE"
      echo 'fixture public step failed' >&2
      exit 1
    fi
    ;;
  *) echo "unexpected gh invocation: $*" >&2; exit 90 ;;
esac
EOF
cat >"$TEST_ROOT/bin/node" <<'EOF'
#!/usr/bin/env sh
set -eu
case "$1" in
  */ci-release-gate.mjs) printf "ci-gate %s\n" "$2" >>"$FIXTURE_LOG"; exit 0 ;;
  */telegram-release.mjs)
    printf 'notify %s\n' "$*" >>"$FIXTURE_LOG"
    if [ "$2" = send ] && [ -f "$FIXTURE_NOTIFY_FAIL" ]; then exit 1; fi
    exit 0
    ;;
esac
printf 'validate %s\n' "$*" >>"$FIXTURE_LOG"
test -f "$2/frontend/package.json"
EOF
cat >"$TEST_ROOT/bin/go" <<'EOF'
#!/usr/bin/env sh
set -eu
while [ "$#" -gt 0 ]; do
  if [ "$1" = --output ]; then printf 'fixture\n' >"$2"; exit 0; fi
  shift
done
exit 90
EOF
printf '%s\n' '#!/usr/bin/env sh' 'exit 0' >"$TEST_ROOT/bin/docker"
cp "$TEST_ROOT/bin/docker" "$TEST_ROOT/bin/npm"
chmod 0755 "$TEST_ROOT/bin/"*
git init -q --bare --initial-branch=main "$TEST_ROOT/remote.git"
git -C "$TEST_ROOT/source" init -q --initial-branch=main
git -C "$TEST_ROOT/source" config user.name 'Release test'
git -C "$TEST_ROOT/source" config user.email 'release@example.com'
git -C "$TEST_ROOT/source" add -- scripts frontend/package.json tools/openapi/package.json
git -C "$TEST_ROOT/source" commit -qm 'release fixture'
git -C "$TEST_ROOT/source" remote add origin "$TEST_ROOT/remote.git"
git -C "$TEST_ROOT/source" push -q -u origin main
export FIXTURE_LOG="$TEST_ROOT/commands.log" FIXTURE_FAIL_ONCE="$TEST_ROOT/fail-once"
export FIXTURE_NOTIFY_FAIL="$TEST_ROOT/notify-fail"
export PATH="$TEST_ROOT/bin:$PATH"
export IMAGE_PREFIX=ghcr.io/fixture-a GH_REPO=fixture-a/pool DIST_DIR="$TEST_ROOT/dist"

# verify has no release version, main-branch or GitHub requirement and no writes remotely.
: >"$FIXTURE_LOG"
git -C "$TEST_ROOT/source" switch -qc codex/verify-fixture
sh "$TEST_ROOT/source/scripts/local-release.sh" verify >/dev/null
! grep -Eq '^(gh|images) ' "$FIXTURE_LOG" || { echo 'verify published externally' >&2; exit 1; }
git -C "$TEST_ROOT/source" switch -q main

# Direct workstation publication is rejected before any external mutation.
: >"$FIXTURE_LOG"
if GITHUB_ACTIONS=false VERSION=v9.9.9 sh "$TEST_ROOT/source/scripts/local-release.sh" publish >"$TEST_ROOT/local-error.log" 2>&1; then
  echo 'workstation publication was allowed' >&2; exit 1
fi
test ! -s "$FIXTURE_LOG"
export GITHUB_ACTIONS=true

# A failed public step can be retried against its already-created tag and Draft.
VERSION=v9.9.9-rc.1
export VERSION
touch "$FIXTURE_FAIL_ONCE"
if sh "$TEST_ROOT/source/scripts/local-release.sh" publish >"$TEST_ROOT/failed.log" 2>&1; then
  echo 'public failure was ignored' >&2; exit 1
fi
! grep -Eq '^notify .* send ' "$FIXTURE_LOG" || { echo 'failed publication triggered notification' >&2; exit 1; }
sh "$TEST_ROOT/source/scripts/local-release.sh" publish >/dev/null
grep -Fq 'release edit v9.9.9-rc.1 --repo fixture-a/pool --draft=false --prerelease --latest=false' "$FIXTURE_LOG"
! grep -Fq 'release create ' "$FIXTURE_LOG" || { echo 'existing Draft was replaced' >&2; exit 1; }
test "$(git -C "$TEST_ROOT/source" rev-parse 'v9.9.9-rc.1^{commit}')" = "$(git -C "$TEST_ROOT/source" rev-parse HEAD)"

# Only canonical stable versions may move Latest.
VERSION=v9.9.9 sh "$TEST_ROOT/source/scripts/local-release.sh" publish >/dev/null
grep -Fq 'release edit v9.9.9 --repo fixture-a/pool --draft=false --prerelease=false --latest' "$FIXTURE_LOG"
awk '/release edit v9.9.9 .*--latest$/ { published=1 } /^notify .* send .*--version v9.9.9 / { if (!published) exit 1; sent=1 } END { if (!sent) exit 1 }' "$FIXTURE_LOG"
# Notification failure is separate from an already completed GitHub publication.
touch "$FIXTURE_NOTIFY_FAIL"
if VERSION=v9.9.10 sh "$TEST_ROOT/source/scripts/local-release.sh" publish >"$TEST_ROOT/notify-error.log" 2>&1; then
  echo 'notification failure was hidden' >&2; exit 1
fi
grep -Fq 'GitHub Release 已发布，但 Telegram 通知未完成' "$TEST_ROOT/notify-error.log"
grep -q '^ci-gate ' "$FIXTURE_LOG"
printf '%s\n' 'CI release orchestration contract tests passed'

# The workstation entry dispatches the exact pushed revision without publishing.
cp "$ROOT_DIR/scripts/github-release.sh" "$TEST_ROOT/source/scripts/github-release.sh"
git -C "$TEST_ROOT/source" add scripts/github-release.sh
git -C "$TEST_ROOT/source" commit -qm 'dispatch fixture'
git -C "$TEST_ROOT/source" push -q origin main
export GH_REPO=Alfonsxh/codex-cpa-pool IMAGE_PREFIX=ghcr.io/alfonsxh
: >"$FIXTURE_LOG"
sh "$TEST_ROOT/source/scripts/github-release.sh" publish >/dev/null
grep -Fq "workflow run release.yml --repo $GH_REPO --ref main -f operation=publish -f version=$VERSION -f revision=$(git -C "$TEST_ROOT/source" rev-parse HEAD)" "$FIXTURE_LOG"
! grep -Eq '^(images|notify) |^gh release ' "$FIXTURE_LOG"
NOTIFY_ACTION=status sh "$TEST_ROOT/source/scripts/github-release.sh" notify >/dev/null
grep -Fq 'notification_action=status' "$FIXTURE_LOG"
touch "$TEST_ROOT/source/dirty"
if sh "$TEST_ROOT/source/scripts/github-release.sh" publish >"$TEST_ROOT/dirty-error.log" 2>&1; then
  echo 'dirty dispatch accepted' >&2; exit 1
fi
rm "$TEST_ROOT/source/dirty"
git -C "$TEST_ROOT/source" commit --allow-empty -qm 'unpushed fixture'
if sh "$TEST_ROOT/source/scripts/github-release.sh" publish >"$TEST_ROOT/unpushed-error.log" 2>&1; then
  echo 'unpushed dispatch accepted' >&2; exit 1
fi
printf '%s\n' 'GitHub release dispatch contract tests passed'
