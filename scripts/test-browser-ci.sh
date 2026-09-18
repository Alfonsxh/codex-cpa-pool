#!/usr/bin/env sh
set -eu
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/cpap-browser-contract.XXXXXX")
trap 'rm -rf -- "$TEST_ROOT"' EXIT HUP INT TERM
mkdir -p "$TEST_ROOT/source/scripts" "$TEST_ROOT/source/frontend" "$TEST_ROOT/bin" "$TEST_ROOT/runtime"
cp "$ROOT_DIR/scripts/browser-ci.sh" "$TEST_ROOT/source/scripts/browser-ci.sh"
printf '%s\n' '{"packages":{"node_modules/@playwright/test":{"version":"1.62.1"}}}' >"$TEST_ROOT/source/frontend/package-lock.json"
cat >"$TEST_ROOT/bin/go" <<'SH'
#!/usr/bin/env sh
case "$*" in
  'env GOROOT') printf '%s/go\n' "$BROWSER_FIXTURE" ;;
  'env GOMODCACHE') printf '%s/modules\n' "$BROWSER_FIXTURE" ;;
  'env GOCACHE') printf '%s/build\n' "$BROWSER_FIXTURE" ;;
  *) exit 1 ;;
esac
SH
printf '%s\n' '#!/usr/bin/env sh' 'echo Linux' >"$TEST_ROOT/bin/uname"
cat >"$TEST_ROOT/bin/docker" <<'SH'
#!/usr/bin/env sh
set -eu
printf '%s\n' "$*" >>"$BROWSER_FIXTURE/commands"
if [ "$1" = run ]; then
  while [ "$#" -gt 0 ]; do
    if [ "$1" = --cidfile ]; then printf '%064d\n' 1 >"$2"; break; fi
    shift
  done
  exit "${BROWSER_FIXTURE_EXIT:-0}"
fi
SH
chmod 0755 "$TEST_ROOT/bin/"*
export BROWSER_FIXTURE="$TEST_ROOT" RUNNER_TEMP="$TEST_ROOT/runtime"
export PATH="$TEST_ROOT/bin:$PATH"
sh "$TEST_ROOT/source/scripts/browser-ci.sh" false
grep -Fq -- "--user $(id -u):$(id -g)" "$TEST_ROOT/commands"
grep -Fq -- '--shm-size=2g' "$TEST_ROOT/commands"
grep -Fq -- '--env CPAP_E2E_WORKERS=1' "$TEST_ROOT/commands"
grep -Fq -- "rm -f $(printf '%064d' 1)" "$TEST_ROOT/commands"
! grep -Eq 'update-snapshots|docker.sock|--privileged|--network|prune' "$TEST_ROOT/commands"
test -z "$(ls -A "$RUNNER_TEMP")"
: >"$TEST_ROOT/commands"
sh "$TEST_ROOT/source/scripts/browser-ci.sh" true
grep -Fq -- '--update-snapshots=all' "$TEST_ROOT/commands"
# Failed tests still clean up only the container ID produced by this invocation.
if BROWSER_FIXTURE_EXIT=17 sh "$TEST_ROOT/source/scripts/browser-ci.sh" false; then
  echo 'browser failure was ignored' >&2; exit 1
fi
test -z "$(ls -A "$RUNNER_TEMP")"
: >"$TEST_ROOT/commands"
if sh "$TEST_ROOT/source/scripts/browser-ci.sh" invalid >/dev/null 2>&1; then exit 1; fi
test ! -s "$TEST_ROOT/commands"
printf '%s\n' '{"packages":{"node_modules/@playwright/test":{"version":"0.0.0"}}}' >"$TEST_ROOT/source/frontend/package-lock.json"
if sh "$TEST_ROOT/source/scripts/browser-ci.sh" false >/dev/null 2>&1; then exit 1; fi
test ! -s "$TEST_ROOT/commands"
echo 'Browser container ownership and baseline-mode contract tests passed'
