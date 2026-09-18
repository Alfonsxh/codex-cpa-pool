#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ACTION=${1:-publish}
GH_REPO=${GH_REPO:-Alfonsxh/codex-cpa-pool}
RELEASE_BRANCH=${RELEASE_BRANCH:-main}
GIT_REMOTE=${GIT_REMOTE:-origin}
VERSION=${VERSION:-}
IMAGE_PREFIX=${IMAGE_PREFIX:-ghcr.io/alfonsxh}
NOTIFY_ACTION=${NOTIFY_ACTION:-send}
case "$NOTIFY_ACTION" in send|status|preview|edit) ;; *) echo '通知动作无效' >&2; exit 1 ;; esac
case "$ACTION" in check|publish|notify) ;; *) echo '动作必须为 check、publish 或 notify' >&2; exit 1 ;; esac
if [ "$ACTION" != check ] && [ -z "$VERSION" ]; then
  echo 'VERSION 不能为空' >&2; exit 1
fi
if [ -n "$VERSION" ] && ! printf '%s' "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$'; then
  echo 'VERSION 必须为语义化 Tag' >&2; exit 1
fi
if [ "$IMAGE_PREFIX" != ghcr.io/alfonsxh ] || [ "$GH_REPO" != Alfonsxh/codex-cpa-pool ] || [ "$RELEASE_BRANCH" != main ]; then
  echo '正式发布使用 main、Alfonsxh/codex-cpa-pool 和 ghcr.io/alfonsxh' >&2; exit 1
fi
gh auth status --hostname github.com >/dev/null
if [ -n "$(git -C "$ROOT_DIR" status --porcelain --untracked-files=normal)" ]; then
  echo '工作区存在未提交修改，拒绝触发发布' >&2; exit 1
fi
git -C "$ROOT_DIR" fetch "$GIT_REMOTE" "$RELEASE_BRANCH"
REVISION=$(git -C "$ROOT_DIR" rev-parse HEAD)
if [ "$REVISION" != "$(git -C "$ROOT_DIR" rev-parse "$GIT_REMOTE/$RELEASE_BRANCH")" ]; then
  echo '当前提交必须与远端 main 一致' >&2; exit 1
fi
gh workflow run release.yml --repo "$GH_REPO" --ref "$RELEASE_BRANCH" \
  -f "operation=$ACTION" -f "version=$VERSION" -f "revision=$REVISION" -f "notification_action=$NOTIFY_ACTION"
printf '已触发 GitHub Release 工作流（%s，%s）：https://github.com/%s/actions/workflows/release.yml\n' "$ACTION" "$REVISION" "$GH_REPO"
