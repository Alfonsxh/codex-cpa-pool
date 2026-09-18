# 故障排查

先确认实际部署目录和私有 `target.env`。在匹配 Release 的目录执行：

```sh
make -f scripts/build.mk target-ps TARGET_ENV=/absolute/path/to/target.env
make -f scripts/build.mk target-ownership-status TARGET_ENV=/absolute/path/to/target.env
make -f scripts/build.mk target-smoke TARGET_ENV=/absolute/path/to/target.env
```

日志使用同一目标的 Compose 项目与配置；分享前删除密钥、OAuth、Webhook、邮箱和私有地址。

## 通知没有发送

企业微信先看配置中心的“后台调度”和“最近心跳”：

- 无心跳或超过 3 分钟未更新：检查 `notifications` 容器、所有权和数据库错误。
- 心跳正常：检查开关、Webhook、发送错误、计划时间和系统时区。
- 手动发送成功仅证明通道可用；超过补发窗口的漏发不会自动补发。

Telegram 正式版公告由 GitHub Release 工作流发送，按[通知文档](telegram-release.md#预览与回执)检查回执。

## Gateway 请求失败

| 状态 | 检查 |
| --- | --- |
| `401` | Key 是否有效、路由是否可用、鉴权快照是否刷新 |
| `503 authentication_snapshot_unavailable` | `state/gateway/auth-snapshot.json` 的完整性、新鲜度与权限 |
| `502 upstream_unavailable` | 账号容器、内部网络、内部 Key 与上游错误 |

账号管理的“模型通信测试”可直接验证指定账号生成，最长等待 25 秒，输出上限 64 Token。它产生少量用量，需要可用内部凭据，但无需将用户绑定到该账号。

模型列表或账号测试成功不能代替外部 Gateway 的实际 Responses/SSE 验证。

## Edge 健康但访问 502

核对 Edge 的 Control/Ingress 网络、Gateway 与 Admin 的上游网络，以及 Web 到 Admin 的连接。`state/edge/active-gateway.conf` 只能选择 `blue` 或 `green`。

用匹配目标的 `target-up-core` 恢复声明拓扑，不将手工连接未知容器作为长期修复。

## 页面白屏或资源 404

检查 `web` 镜像与 Release 描述是否一致。下例端口须替换为实际 `CPA_PUBLIC_PORT`：

```sh
curl --noproxy '*' -I http://127.0.0.1:18317/admin/
curl --noproxy '*' -I http://127.0.0.1:18317/portal/assets/codex-cpa-pool-logo.svg
```

HTML 与稳定品牌资源使用 `no-cache`，内容指纹 JS/CSS 使用长期缓存。

## 用量不更新

检查 Collector 健康、Writer Lease、用量库权限与磁盘空间，以及 Gateway 日志是否产生新事件。旧 Generation 不能继续写入；不要直接修改 SQLite 绕过所有权。

## 管理中心与使用中心的账号状态

账号管理、总览筛选目录和使用中心共用一套状态、说明和可切换条件，以同一份账号状态及运行状态覆盖结果为准。管理中心的官方额度详情和诊断信息不再单独决定状态标签。周额度剩余不高于 10% 显示“注意额度”；达到配置的预留线显示“额度预留”。手动可切换与自动迁入资格分别判断，额度预留、临时冷却等状态仍可手动选择；已停用、服务停止、未授权、凭据失效、额度耗尽或完全缺少状态时不可选择。

两个账号列表在前台每 15 秒取数，回到页面时重新取数。点击“刷新”会合并到同一后台官方额度查询请求，待更新期间每 3 秒取数；已有请求或刚完成的查询会被复用，不消耗重置次数。运行状态缓存正常有效期为 15 秒，后台更新时最多复用至 30 秒；停用、容器运行和 OAuth 存在性仍实时检查。官方额度由后台 Worker 发布，两个页面的请求也可能先后到达，因此刷新不是瞬时同步保证。实际切换会重新校验账号状态，过期的展示缓存不能授权切换。

## 周额度已恢复，但使用中心不能切换

切换按钮要求后端的账号和状态 `selectable` 均为 `true`。官方周额度百分比与 CPA 凭据运行状态来自不同接口；CPA 保留旧的 `usage_limit_reached` 冷却时，页面可能同时显示剩余额度和“额度耗尽”。修改控制数据库不能清除 CPA 内部的冷却状态。

额度 Worker 在完成官方查询后，会核对同一上游身份、常规周周期和 CPA 错误时间。只有新周期开始晚于旧错误、相关周额度均未耗尽、账号及凭据未停用时，才在所有权保护下调用该 CPA 的 `/v0/management/reset-quota`，并重新读取凭据确认恢复。此接口只清除 CPA 冷却，不消耗官方重置次数，不更改用户路由或重启容器。

查询过期、身份不一致、多个凭据、当前周期的新错误或授权失败时不会自动恢复。旧版 CPA 不支持该接口、调用失败或恢复未确认时，Worker 记录 `CPA quota cooldown recovery incomplete`，继续保留官方查询结果和现有切换限制；检查额度 Worker 日志与 CPA 版本后处理。

## 周额度已耗尽，但重置按钮不可用

账号管理的“重置”使用官方重置余额恢复已耗尽的周额度。周窗口上报已用 100% 且重置余额大于零时，应允许打开重置对话框；官方明确标记为可重置的周窗口也保留支持。短时额度耗尽、保留额度阈值或 CPA 本地冷却本身不构成周额度重置依据。

部分官方响应会同时返回周用量 100%、`limit_reached=false`、`applicable_available_count=0` 和空的 `rate_limit_reached_type`。旧实现将后三项作为必要条件，造成账号已被判定耗尽，却无法发起重置。当前实现以周窗口耗尽为请求依据，提交前在所有权保护下重新查询官方用量和所选重置凭证。周额度已恢复、凭证已不可用或查询失败时停止操作；官方执行接口拒绝时如实报告，不自动重试或修改本地用量来伪造恢复。

列表的“剩余 N 次”表示重置余额。点击入口只查询详情，确认提交才会消耗一次余额。是否成功以官方执行响应和刷新后的周额度为准，不能仅凭按钮可点击认定恢复。

## 账号代理投影损坏

普通修复先迁移路由并等待请求排空。仅当源账号已不可用、仍有路由且全池没有安全迁移目标时，才能使用受限接口：

```http
POST /admin/api/accounts/repair-proxy
X-Management-Key: <管理密钥>
Content-Type: application/json

{"id":"<account-id>","proxy_url":"http://<existing-proxy>:<port>","confirm":"repair-proxy:<account-id>"}
```

操作前核对既有代理网络并保留可恢复备份。该接口只修改独立代理并重建同一账号，不修改外部代理服务、账号标识或路由。首个账号恢复后，其余账号回到普通迁移流程。

## 部署检查失败

| 问题 | 处理 |
| --- | --- |
| 镜像不匹配 | 使用同一 Release 的四组件摘要引用，核对组件名与两个摘要标签 |
| 所有权冲突 | 先查明并停止旧 Writer，再进行受控交接，不删除所有权记录 |
| Gateway 排空超时 | 旧槽与 SSE 保持运行，处理长请求后重试 |
| Edge 维护未确认 | 明确维护窗口后填写两个 Edge 确认字段，见[部署](deployment.md#底层应用与验收) |
| 配置来源冲突 | 正式控制面使用 `target.env`，账号投影 `state/compose.env` 不可替代它 |
