# 开发与验证

## 环境

依赖 Go（版本见 `go.mod`）、Node.js 22、Docker Engine 和 Compose v2。在仓库根目录执行：

```sh
npm ci --prefix frontend
npm ci --prefix tools/openapi
```

根 `Makefile` 管理前端开发；构建、验证和发布使用 `make -f scripts/build.mk`。

## 前端热更新

复制 `frontend/.env.example` 为 `frontend/.env`，将 `CPA_DEV_PROXY_TARGET` 设为已授权的 Test Admin，然后运行：

```sh
make frontend-dev-all
```

Admin、Usage 和 Portal 开发服务支持热更新，修改页面通常无需重启。命令行 `FRONTEND_DEV_UPSTREAM=<URL>` 可临时覆盖配置；不要将开发写接口连接生产。

只读 Admin 演示可在两个终端分别运行：

```sh
go run ./cmd/test-preview --address 127.0.0.1:8896 --root .
```

```sh
make frontend-dev FRONTEND_DEV_UPSTREAM=http://127.0.0.1:8896
```

Preview 使用固定演示数据，不能执行真实写操作；Usage 和 Portal 需要完整 Test Admin。

## 验证

```sh
make -f scripts/build.mk verify
npm --prefix frontend run test:e2e
```

`verify` 包含生成契约、Shell/Go 检查、单元与竞态测试、前端类型/测试/构建、部署脚本、隐私和 Compose 校验。修改 OpenAPI 后先运行 `make -f scripts/build.mk generate-api`。

Playwright 默认两个 worker，资源紧张时设 `CPAP_E2E_WORKERS=1`。隔离数据面演练：

```sh
make -f scripts/build.mk test-build
make -f scripts/build.mk test-up
make -f scripts/build.mk test-smoke
make -f scripts/build.mk test-faults
make -f scripts/build.mk test-down
```

覆盖 Key 拒绝、上游故障、快照损坏、蓝绿切换及 SSE 排空，不替代真实目标验收。Writer 必须持有运行时与自身 Lease，旧 Generation 写入应返回 `ErrLeaseLost`。

## 发布版本

正式发布由 [GitHub Actions 的 Release 工作流](https://github.com/Alfonsxh/codex-cpa-pool/actions/workflows/release.yml)执行。将最终提交推送到 `main`，为该版本和提交准备审核过的 Release Draft；正文模板见[Telegram 发布通知](telegram-release.md)。

```sh
make -f scripts/build.mk release VERSION=v2.1.5
```

将示例版本替换为待发布版本。本机只需 Git 和已登录的 `gh`，命令锁定当前提交并触发流水线，不在本机构建或推送镜像。也可在 Actions 页面选择 `main`、`operation=publish` 和版本号。

流水线先运行完整源码检查、Linux 容器中的浏览器验收和部署包校验，所有任务均在已有 OPC 自托管 Runner 执行，随后发布 Linux AMD64 镜像、Tag、五个附件和 GitHub Release，最后校验产物并发送正式版公告。发布使用当前运行及当前重试次数的验收结果；源码改变、检查失败或已公开的版本都会阻止发布。镜像固定使用 `ghcr.io/alfonsxh`，组件源码摘要相同则复用已有镜像。

所有版本的发布串行执行，不会因新任务而取消正在发布的版本。发布阶段失败后选择 Actions 的 `Re-run all jobs`，确保本次重试也有完整验收结果。`vX.Y.Z` 正式版更新 Latest，RC 等使用 Pre-release。通知失败不回滚已公开版本，检查回执后单独重试通知。

```sh
# 完整 CI 验收和 Runner/通知预检；不创建 Tag、镜像或 Release
make -f scripts/build.mk release-check
# 加 VERSION 时还检查对应的 Draft 和版本冲突
make -f scripts/build.mk release-check VERSION=v2.1.5
# 单独查询通知回执或重试，均由同一 Runner 执行
make -f scripts/build.mk release-notify VERSION=v2.1.5 NOTIFY_ACTION=status
make -f scripts/build.mk release-notify VERSION=v2.1.5
```

`release-verify` 保留为可选本地验收；本地记录不会替代 CI 验收。浏览器使用与 Playwright 锁文件匹配的固定镜像和 Linux 视觉基准；Mac 本地基准独立保留。CI 的 `update_browser_snapshots` 仅生成待审查基准，可用 `snapshot_test_filter` 定位单个用例；普通验收禁止过滤测试或更新基准。原有 CI 工作流仍可手动执行检查和打包，正式发布统一走 Release 工作流。工作流使用临时 `GITHUB_TOKEN` 发布，Docker 登录目录按任务隔离并在结束时清理，不覆盖 Runner 原有登录。

通知 Secrets 和持久化回执见[配置说明](telegram-release.md#配置)。CI 不持有部署凭据、不连接业务环境；测试和生产升级仍分别在目标机运行 `run.sh`。

## 前端与统计约定

运行总览 → Token 使用 → **导出** 可下载 XLSX，包含用量总览、团队统计、账号明细、用户使用明细和每日趋势五张表。默认上个完整自然周，按系统时区划分；选择本周时与上周同一时段对比。周报不受页面筛选及 Top10 限制。

周报按原始 Token 降序，保留历史加权值。总览展示核心指标、日趋势、团队消耗和本周观察；明细包含输入、缓存、输出及最后请求时间。缓存包含在输入中，不重复相加；带单位显示仍保留完整数值。表头与内容按列对齐，滚动时固定表头。

两期均按当前团队归属汇总，账号用量按请求实际所属账号统计；当前绑定关系不改变历史用量归属。历史身份仍计入，人数与账号数去重。未采集到记录不代表已确认无用量，上期为零或无记录时环比显示 `—`。导出不包含 Key 等凭据。

前端数据和凭据规则见 [frontend/README](../frontend/README.md)。使用中心缓存按用户和时间范围隔离：列表/摘要 15 秒、模型明细 30 秒，闲置数据最多保留 5 分钟；刷新和账号切换使相关缓存失效。

账号列表可短暂使用运行状态缓存并后台刷新；路由、账号切换和自动分配仍使用独立有效性检查。不能用展示缓存作为授权依据。

## 文档与截图

每页围绕一个任务，先写步骤，再写必要前提或失败处理。共享细节放在所属页面并链接，避免重复接口定义、像素参数和历史实现过程；命令、默认值和限制以当前代码为准。

```sh
npm --prefix frontend run docs:screenshots
```

截图为 1920×1080 深色模式，使用隔离演示数据，不缩放或拼接。发布前检查邮箱、Key、Webhook 和私有地址；日常浏览器测试仍覆盖深浅两种主题。
