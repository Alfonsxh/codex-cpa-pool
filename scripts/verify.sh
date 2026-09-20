#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

printf '%s\n' '[1/7] 语法、格式与生成代码'
sh -n \
  "$ROOT_DIR/scripts/check-generated-api.sh" \
  "$ROOT_DIR/scripts/check-product-name.sh" \
  "$ROOT_DIR/scripts/test-product-name.sh" \
  "$ROOT_DIR/scripts/test-removed-runtime.sh" \
  "$ROOT_DIR/scripts/run.sh" \
  "$ROOT_DIR/scripts/generate-api.sh" \
  "$ROOT_DIR/scripts/local-release.sh" \
  "$ROOT_DIR/scripts/github-release.sh" \
  "$ROOT_DIR/scripts/browser-ci.sh" \
  "$ROOT_DIR/scripts/test-browser-ci.sh" \
  "$ROOT_DIR/scripts/package-release.sh" \
  "$ROOT_DIR/scripts/release-images.sh" \
  "$ROOT_DIR/scripts/test-release-images.sh" \
  "$ROOT_DIR/scripts/test-local-release.sh" \
  "$ROOT_DIR/scripts/test-run-runtime.sh" \
  "$ROOT_DIR/scripts/test-run-backup.sh" \
  "$ROOT_DIR/scripts/test-run.sh" \
  "$ROOT_DIR/scripts/test-run-compat.sh" \
  "$ROOT_DIR/scripts/test-faults.sh" \
  "$ROOT_DIR/scripts/test-smoke.sh" \
  "$ROOT_DIR/scripts/verify.sh"
sh "$ROOT_DIR/scripts/check-product-name.sh"
sh "$ROOT_DIR/scripts/test-product-name.sh"
(cd "$ROOT_DIR" && sh scripts/check-generated-api.sh)
sh "$ROOT_DIR/scripts/test-run-runtime.sh"
sh "$ROOT_DIR/scripts/test-run-backup.sh"
sh "$ROOT_DIR/scripts/test-run.sh"
sh "$ROOT_DIR/scripts/test-run-compat.sh"
sh "$ROOT_DIR/scripts/test-release-images.sh"
node --test "$ROOT_DIR/scripts/release-validation.test.mjs" "$ROOT_DIR/scripts/ci-release.test.mjs"
node --test "$ROOT_DIR/scripts/telegram-release.test.mjs"
sh "$ROOT_DIR/scripts/test-local-release.sh"
sh "$ROOT_DIR/scripts/test-browser-ci.sh"
UNFORMATTED_GO=$(find "$ROOT_DIR/cmd" "$ROOT_DIR/internal" -type f -name '*.go' -exec gofmt -l {} +)
if [ -n "$UNFORMATTED_GO" ]; then
  echo "Go 文件未格式化：" >&2
  printf '%s\n' "$UNFORMATTED_GO" >&2
  exit 1
fi

printf '%s\n' '[2/7] Go 静态检查与测试'
(cd "$ROOT_DIR" && go vet ./... && go test ./... && go test -race ./...)

printf '%s\n' '[3/7] React 类型、单元测试与构建'
npm --prefix "$ROOT_DIR/frontend" run typecheck
npm --prefix "$ROOT_DIR/frontend" test
npm --prefix "$ROOT_DIR/frontend" run build

printf '%s\n' '[4/7] 公开发布边界'
(cd "$ROOT_DIR" && go run ./cmd/releasectl privacy --root "$ROOT_DIR")

printf '%s\n' '[5/7] Compose 配置'
docker compose \
  --project-directory "$ROOT_DIR" \
  --env-file "$ROOT_DIR/.env.example" \
  -f "$ROOT_DIR/docker-compose.yml" \
  --profile writers \
  --profile external-effects \
  config --quiet
docker compose \
  -p codex-cpa-test \
  --project-directory "$ROOT_DIR" \
  -f "$ROOT_DIR/docker-compose.test.yml" \
  config --quiet

printf '%s\n' '[6/7] 已移除运行时残留'
sh "$ROOT_DIR/scripts/test-removed-runtime.sh"
REMOVED_SOURCE_SUFFIX=$(printf '.%s%s' p y)
REMOVED_BYTECODE_SUFFIX=$(printf '.%s%sc' p y)
REMOVED_DATA_PLANE_SUFFIX=$(printf '.%s%s%s' l u a)
REMOVED_REQUIREMENT_GLOB=$(printf 'require%s*.txt' ments)
if find "$ROOT_DIR" \
  -path "$ROOT_DIR/.git" -prune -o \
  -path "$ROOT_DIR/.harness" -prune -o \
  -path "$ROOT_DIR/dist" -prune -o \
  -path "$ROOT_DIR/frontend/node_modules" -prune -o \
  -path "$ROOT_DIR/tools/openapi/node_modules" -prune -o \
  -type f \( -name "*$REMOVED_SOURCE_SUFFIX" -o -name "*$REMOVED_BYTECODE_SUFFIX" -o \
    -name "*$REMOVED_DATA_PLANE_SUFFIX" -o -name "$REMOVED_REQUIREMENT_GLOB" \) -print -quit \
  | grep -q .; then
  echo "发现已移除运行时的源码或依赖文件" >&2
  exit 1
fi
# Match interpreter command tokens, while allowing historical document filenames.
FORBIDDEN_RUNTIME=$(printf '(^|[^[:alnum:]_.-])\160\171\164\150\157\156\63\77([^[:alnum:]_.-]|$)\174\163\145\164\165\160\55\160\171\164\150\157\156\174\160\151\160\40\151\156\163\164\141\154\154\174\160\171\164\145\163\164\174\165\156\151\164\164\145\163\164')
if rg --hidden -n "$FORBIDDEN_RUNTIME" \
  --glob '!frontend/node_modules/**' \
  --glob '!tools/openapi/node_modules/**' \
  --glob '!.git/**' \
  --glob '!.harness/**' \
  --glob '!dist/**' \
  "$ROOT_DIR"; then
  echo "发现已移除运行时的命令引用" >&2
  exit 1
fi
FORBIDDEN_DEPLOYMENT_NAMESPACE=$(printf '%s' 'V''2_|go-''v2|Go[[:space:]]+v''2|cliproxy-''v2|v2-''target|docker-compose\.v2-''test|scripts/v2-''test|testdata/v''2|v''2/Dockerfile|docker-read-''proxy|dockerread''proxy')
if rg --hidden -n "$FORBIDDEN_DEPLOYMENT_NAMESPACE" \
  --glob '!frontend/node_modules/**' \
  --glob '!tools/openapi/node_modules/**' \
  --glob '!.git/**' \
  --glob '!.harness/**' \
  --glob '!dist/**' \
  "$ROOT_DIR" \
  | CPA_FORBIDDEN_NAMESPACE="$FORBIDDEN_DEPLOYMENT_NAMESPACE" CPA_SCAN_ROOT="$ROOT_DIR" awk '
    BEGIN {
      forbidden=ENVIRON["CPA_FORBIDDEN_NAMESPACE"]
      root=ENVIRON["CPA_SCAN_ROOT"] "/"
      history="Go v" 2
      title="# Python v1 到 " history " 的保留数据迁移方案"
      intro="Python v1 首次切换到 " history " 必须先完成 [保留数据迁移方案](python-to-go-migration.md) 中的兼容转换、演练和受控接管。"
    }
    {
      path=$0; sub(/:[0-9]+:.*/, "", path)
      if (index(path, root) == 1) path=substr(path, length(root) + 1)
      line=$0; sub(/^[^:]+:[0-9]+:/, "", line)
      # Only the historical label in these two established migration summaries
      # is allowed. Every other namespace on the same line is still checked.
      if ((path == "docs/python-to-go-migration.md" && index(line, title) == 1) ||
          (path == "docs/upgrade.md" && index(line, intro) == 1)) {
        sub(history, "Go", line)
      }
      if (line ~ forbidden) { print $0; found=1 }
    }
    END { exit (found ? 0 : 1) }
  '; then
  echo "发现迁移期部署命名或已移除的 Docker 只读代理" >&2
  exit 1
fi
DOCKERFILE_COUNT=$(find "$ROOT_DIR" \
  -path "$ROOT_DIR/.git" -prune -o \
  -path "$ROOT_DIR/.harness" -prune -o \
  -path "$ROOT_DIR/dist" -prune -o \
  -path "$ROOT_DIR/frontend/node_modules" -prune -o \
  -path "$ROOT_DIR/tools/openapi/node_modules" -prune -o \
  -type f -name Dockerfile -print | wc -l | tr -d ' ')
if [ "$DOCKERFILE_COUNT" != 1 ]; then
  echo "正式构建必须只保留仓库根目录 Dockerfile" >&2
  exit 1
fi

printf '%s\n' '[7/7] Git diff 格式'
git -C "$ROOT_DIR" diff --check

printf '%s\n' '验证通过'
