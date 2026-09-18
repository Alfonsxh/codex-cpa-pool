# Telegram 发布通知

GitHub Release 工作流在正式 Release 公开并校验附件、Tag 和镜像后发送公告。无需常驻进程，与业务环境的企业微信通知独立。

仅发送规范 `vX.Y.Z` 正式版。代码 push、独立 Tag、Draft、RC/beta、构建后缀及启用前的历史版本不发送。

## 配置

在 GitHub 仓库 Actions Secrets 中配置：

- `CPAP_TELEGRAM_CONFIG_JSON`：下面的完整 JSON 配置。
- `CPAP_TELEGRAM_BOT_TOKEN`：Bot Token。
- `CPAP_TELEGRAM_RECEIPTS_JSON`：首次迁移时使用的历史回执 JSON 数组；导入确认后删除此 Secret。

工作流将配置写入自托管 Runner 的 `~/.config/codex-cpa-pool/ci-telegram-release/`，后续任务继续使用同一目录和回执：

- `bot-token`：BotFather 提供的 Token。
- `config.json`：下列配置，示例值须替换。
- `deliveries/`：程序生成的回执，保留用于去重。

目录权限 `0700`，文件 `0600`，由当前用户持有，不使用软链接、不放入任何 Git 工作区或发布附件。

```json
{
  "enabled": true,
  "repository": "owner/repository",
  "repository_id": 123456,
  "chat_id": "-1001234567890",
  "bot_username": "YourReleaseBot",
  "min_version": "v2.0.2"
}
```

仓库 ID 用 `gh api repos/owner/repository --jq .id` 获取；群 ID 用 Telegram `getChat` 返回的数字 ID。核对 Bot 和目标，授予发送权限；频道需要发布权限。

`min_version` 阻止补发旧版本。可选 `proxy_url` 为不含账号密码的 HTTP/HTTPS 代理；也支持标准代理环境变量。`XDG_CONFIG_HOME` 可替换配置根目录，`CPAP_TELEGRAM_CONFIG` 可直接指定配置文件。

## 公告模板

Agent 审核变更后编写 Release Draft，包含以下两个唯一、非空段落：

```markdown
## 社群摘要
- **账号管理**：一句话介绍功能。
- **通知服务**：一句话介绍修复。
- **安装升级**：一句话介绍变化。

## 升级提示
在原运维目录运行：`./run.sh`
```

公告只保留 3–5 个短条目和升级命令；常规数据保留要求、行为细节放入对应文档。Release 本身也保持简短，以实际变化、必要验证和文档链接为主。

标题、功能名加粗，小标题与正文间空一行，更新条目之间不空行，升级命令与提示文字同行并使用等宽文字。程序仅转换粗体和行内代码，转义原始 HTML；通知上限为 3800 个 UTF-16 单元（含标记）。底部“发布详情”指向本版本，“安装升级”指向维护中的升级文档。

发布命令见[开发指南](development.md#发布版本)。Release 工作流要求有效且启用的通知 Secrets，并在发布前检查目标权限。工作站的代理地址不能直接复制到 Runner；按 Runner 的实际网络配置。迁移导入仅补充不存在的历史回执，已有回执始终保留。

## 预览与回执

```sh
make -f scripts/build.mk release-notify VERSION=v2.0.2 NOTIFY_ACTION=preview
make -f scripts/build.mk release-notify VERSION=v2.0.2 NOTIFY_ACTION=status
make -f scripts/build.mk release-notify VERSION=v2.0.2 NOTIFY_ACTION=send
make -f scripts/build.mk release-notify VERSION=v2.0.2 NOTIFY_ACTION=edit
```

| 操作或结果 | 处理 |
| --- | --- |
| `preview` | 校验发布产物并预览，不发送 |
| `sent` | 已成功；再次 `send` 复用回执 |
| `edit` | 先更新并审核 Release 正文，再修改原消息 |
| `failed` | Telegram 明确拒绝，修复原因后可重试 |
| `pending` / `unknown` | 先核对实际群消息，禁止盲目重发或删除回执 |

上述命令触发 CI 的通知专用操作，结果在 Actions 日志中查看；不会重建镜像或重新发布 Release。请备份 Runner 的 `deliveries/`，迁移或更换 Runner 时一起迁移，避免丢失去重记录。

回执绑定仓库、版本、群、Release 与 Bot；目录锁防止并发。遗留锁须确认进程已退出后处理。明确限流最多重试三次，每次等待不超过 60 秒。

通知失败不回滚已发布 Release。离线回归：`node --test scripts/telegram-release.test.mjs`；完整检查见[开发指南](development.md#验证)。
