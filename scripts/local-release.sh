#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ACTION=${1:-publish}
VERSION=${VERSION:-}
IMAGE_PREFIX=${IMAGE_PREFIX:-}
PLATFORM=${PLATFORM:-linux/amd64}
GH_REPO=${GH_REPO:-Alfonsxh/codex-cpa-pool}
GIT_REMOTE=${GIT_REMOTE:-origin}
RELEASE_BRANCH=${RELEASE_BRANCH:-main}
DIST_DIR=${DIST_DIR:-$ROOT_DIR/dist}

# Shared implementation for the Runner. Workstations dispatch release.yml.
if [ "$ACTION" = publish ] && [ "${GITHUB_ACTIONS:-}" != true ]; then
  echo '正式发布已迁移到 GitHub Actions；请运行 make -f scripts/build.mk release' >&2
  exit 1
fi

run_stage() {
  STAGE_LABEL=$1
  shift
  STAGE_START=$(date +%s)
  if "$@"; then
    printf '[发布] %s完成，耗时 %s 秒\n' "$STAGE_LABEL" "$(( $(date +%s) - STAGE_START ))"
  else
    STAGE_EXIT=$?
    printf '[发布] %s失败，耗时 %s 秒\n' "$STAGE_LABEL" "$(( $(date +%s) - STAGE_START ))" >&2
    return "$STAGE_EXIT"
  fi
}

case "$DIST_DIR" in
  /*) ;;
  *) DIST_DIR="$ROOT_DIR/$DIST_DIR" ;;
esac

case "$ACTION" in
  check|verify|publish) ;;
  *) echo "动作必须是 check、verify 或 publish：$ACTION" >&2; exit 1 ;;
esac
if [ "$ACTION" != verify ]; then
  : "${VERSION:?VERSION 不能为空，例如 v1.1.0}"
  : "${IMAGE_PREFIX:?IMAGE_PREFIX 不能为空，例如 ghcr.io/owner}"
  if ! printf '%s' "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$'; then
    echo "VERSION 必须是带 v 前缀的语义化 Tag：$VERSION" >&2
    exit 1
  fi
  if ! printf '%s' "$IMAGE_PREFIX" | grep -Eq '^[A-Za-z0-9.-]+(:[0-9]+)?/[A-Za-z0-9._/-]+$'; then
    echo "IMAGE_PREFIX 无效：$IMAGE_PREFIX" >&2
    exit 1
  fi
  if ! printf '%s' "$GH_REPO" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$'; then
    echo "GH_REPO 必须是 owner/repository：$GH_REPO" >&2
    exit 1
  fi

fi

for command in git docker go make npm node; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "缺少发布依赖：$command" >&2
    exit 1
  fi
done
docker buildx version >/dev/null
if [ "$ACTION" != verify ]; then
  gh auth status --hostname github.com >/dev/null
fi

CURRENT_BRANCH=$(git -C "$ROOT_DIR" symbolic-ref --quiet --short HEAD || true)
if [ "$ACTION" != verify ] && [ "$CURRENT_BRANCH" != "$RELEASE_BRANCH" ] && [ "${GITHUB_ACTIONS:-}" != true ]; then
  echo "只能从 $RELEASE_BRANCH 分支发布，当前分支：${CURRENT_BRANCH:-detached HEAD}" >&2
  exit 1
fi
if [ -n "$(git -C "$ROOT_DIR" status --porcelain --untracked-files=normal)" ]; then
  echo "工作区存在未提交修改，拒绝发布" >&2
  exit 1
fi

REVISION=$(git -C "$ROOT_DIR" rev-parse HEAD)
if [ "$ACTION" = publish ]; then
  node "$ROOT_DIR/scripts/ci-release-gate.mjs" "$REVISION"
fi
if [ "$ACTION" != verify ]; then
  # 发布只接受已经推送到主分支的提交，避免 GitHub Release 指向本地独有 revision。
  git -C "$ROOT_DIR" fetch "$GIT_REMOTE" "$RELEASE_BRANCH" --tags
  REMOTE_BRANCH_REVISION=$(git -C "$ROOT_DIR" rev-parse "$GIT_REMOTE/$RELEASE_BRANCH")
  if [ "$REVISION" != "$REMOTE_BRANCH_REVISION" ]; then
    echo "当前提交尚未与 $GIT_REMOTE/$RELEASE_BRANCH 同步，拒绝发布" >&2
    exit 1
  fi

  LOCAL_TAG_REVISION=$(
    git -C "$ROOT_DIR" rev-parse --verify "$VERSION^{commit}" 2>/dev/null || true
  )
  if [ -n "$LOCAL_TAG_REVISION" ] && [ "$LOCAL_TAG_REVISION" != "$REVISION" ]; then
    echo "本地 Tag 已指向其他提交：$VERSION" >&2
    exit 1
  fi

  REMOTE_TAG_REVISION=$(
    git -C "$ROOT_DIR" ls-remote "$GIT_REMOTE" \
      "refs/tags/$VERSION" "refs/tags/$VERSION^{}" \
      | awk -v direct="refs/tags/$VERSION" -v peeled="refs/tags/$VERSION^{}" '
          $2 == direct { direct_revision = $1 }
          $2 == peeled { peeled_revision = $1 }
          END { print peeled_revision ? peeled_revision : direct_revision }
        '
  )
  if [ -n "$REMOTE_TAG_REVISION" ] && [ "$REMOTE_TAG_REVISION" != "$REVISION" ]; then
    echo "远端 Tag 已指向其他提交：$VERSION" >&2
    exit 1
  fi

  RELEASE_STATE=missing
  if RELEASE_DRAFT=$(gh release view "$VERSION" --repo "$GH_REPO" --json isDraft --jq .isDraft 2>/dev/null); then
    if [ "$RELEASE_DRAFT" = true ]; then
      RELEASE_STATE=draft
    else
      echo "GitHub Release 已发布，拒绝覆盖：$VERSION" >&2
      exit 1
    fi
  fi

  if [ "$RELEASE_STATE" != draft ]; then
    echo '请先为该版本准备经过审核的 GitHub Release Draft' >&2
    exit 1
  fi
  printf 'version=%s\nrevision=%s\nimage_prefix=%s\ngithub_repo=%s\nrelease_state=%s\n' \
    "$VERSION" "$REVISION" "$IMAGE_PREFIX" "$GH_REPO" "$RELEASE_STATE"
  # With local notifications enabled, reject missing notes or destination rights
  # before publishing artifacts. This only reads Telegram/GitHub state.
  node "$ROOT_DIR/scripts/telegram-release.mjs" check --repo "$GH_REPO" --version "$VERSION"
  if [ "$ACTION" = check ]; then
    printf '%s\n' '发布预检通过；未创建 Tag、镜像或 GitHub Release'
    exit 0
  fi

fi

CACHE_ROOT=$(git -C "$ROOT_DIR" rev-parse --path-format=absolute --git-common-dir)/release-validation

# Verification, image publication and release packaging must read one immutable
# checkout. The operator's working tree can otherwise change between those
# phases and produce a Release whose image references do not match its archive.
SNAPSHOT_PARENT=$(mktemp -d "${TMPDIR:-/tmp}/cpap-local-release.XXXXXX")
SNAPSHOT_ROOT="$SNAPSHOT_PARENT/source"
cleanup_snapshot() {
  if [ -d "$SNAPSHOT_ROOT" ]; then
    git -C "$ROOT_DIR" worktree remove --force "$SNAPSHOT_ROOT" >/dev/null 2>&1 || true
  fi
  rm -rf -- "$SNAPSHOT_PARENT"
}
trap cleanup_snapshot EXIT HUP INT TERM
git -C "$ROOT_DIR" worktree add --detach "$SNAPSHOT_ROOT" "$REVISION" >/dev/null
# Local verification owns its dependencies. CI publication has already passed
# its source/browser/package jobs and builds images from this exact source.
if [ "$ACTION" = verify ]; then
  for npm_workspace in frontend tools/openapi; do
    npm --prefix "$SNAPSHOT_ROOT/$npm_workspace" ci --prefer-offline --no-audit --no-fund
  done
  unset npm_workspace
  node "$SNAPSHOT_ROOT/scripts/release-validation.mjs" "$SNAPSHOT_ROOT" "$CACHE_ROOT" "$PLATFORM"
fi
if [ "$ACTION" = verify ]; then
  echo "发布验收完成；未推送代码、Tag、镜像或 Release"
  exit 0
fi

if [ -z "$LOCAL_TAG_REVISION" ]; then
  # Tag 先保留在本地；镜像和发布包全部完成后才推送到 GitHub。
  git -C "$ROOT_DIR" tag -a "$VERSION" "$REVISION" -m "Release $VERSION"
fi

(
  cd "$SNAPSHOT_ROOT"
  VERSION="$VERSION" \
  PLATFORM="$PLATFORM" \
  IMAGE_PREFIXES="$IMAGE_PREFIX" \
    run_stage "镜像准备与推送" sh scripts/release-images.sh publish
)

mkdir -p "$DIST_DIR"
ARCHIVE="$DIST_DIR/codex-cpa-pool-$VERSION.tar.gz"
RELEASE_DESCRIPTOR="$DIST_DIR/release-$VERSION.json"
RELEASE_ENV="$DIST_DIR/release-$VERSION.env"
RUN_ASSET="$DIST_DIR/run.sh"
CHECKSUMS="$DIST_DIR/SHA256SUMS"
(cd "$SNAPSHOT_ROOT" && run_stage "部署包生成与校验" sh scripts/package-release.sh "$ARCHIVE")
(cd "$SNAPSHOT_ROOT" && go run ./cmd/releasectl manifest descriptor \
  --root "$SNAPSHOT_ROOT" \
  --output "$RELEASE_DESCRIPTOR" \
  --release-version "$VERSION" \
  --revision "$REVISION" \
  --image-prefix "$IMAGE_PREFIX" \
  --archive-name "$(basename -- "$ARCHIVE")")
(cd "$SNAPSHOT_ROOT" && go run ./cmd/releasectl manifest deploy-env \
  --root "$SNAPSHOT_ROOT" \
  --output "$RELEASE_ENV" \
  --release-version "$VERSION" \
  --revision "$REVISION" \
  --image-prefix "$IMAGE_PREFIX" \
  --archive-name "$(basename -- "$ARCHIVE")")
cp "$SNAPSHOT_ROOT/scripts/run.sh" "$RUN_ASSET"
chmod 0755 "$RUN_ASSET"

(cd "$SNAPSHOT_ROOT" && go run ./cmd/releasectl checksum \
  --output "$CHECKSUMS" \
  "$ARCHIVE" "$RELEASE_DESCRIPTOR" "$RELEASE_ENV" "$RUN_ASSET")

if [ -z "$REMOTE_TAG_REVISION" ]; then
  git -C "$ROOT_DIR" push "$GIT_REMOTE" "refs/tags/$VERSION"
fi

run_stage "附件上传" gh release upload "$VERSION" \
  "$ARCHIVE#Deployment archive" \
  "$RELEASE_DESCRIPTOR#Release descriptor" \
  "$RELEASE_ENV#deployment environment" \
  "$RUN_ASSET#single installation and upgrade script" \
  "$CHECKSUMS#SHA-256 checksums" \
  --repo "$GH_REPO" \
  --clobber

# GitHub Release 最后公开，确保用户看见版本时全部附件已经可用。
# SemVer prereleases cannot be GitHub Latest. Apply the policy to Drafts too.
if printf '%s' "$VERSION" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; then
  run_stage "公开正式版" gh release edit "$VERSION" --repo "$GH_REPO" --draft=false --prerelease=false --latest
else
  run_stage "公开预发布" gh release edit "$VERSION" --repo "$GH_REPO" --draft=false --prerelease --latest=false
fi
printf '发布完成：https://github.com/%s/releases/tag/%s\n' "$GH_REPO" "$VERSION"
if ! node "$SNAPSHOT_ROOT/scripts/telegram-release.mjs" send \
  --repo "$GH_REPO" --version "$VERSION" --revision "$REVISION" --image-prefix "$IMAGE_PREFIX"; then
  printf '%s\n' 'GitHub Release 已发布，但 Telegram 通知未完成。请先查看回执，再通过 release-notify 重试；不要重新发布或删除未知结果的回执。' >&2
  exit 2
fi
