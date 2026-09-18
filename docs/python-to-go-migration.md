# Python v1 到 Go v2 的保留数据迁移方案

更新于 2026-09-07。本手册说明旧版升级到 Go 实现时的停机迁移要求：保留历史 Token 用量、账号、API Key 和 OAuth，完成离线转换、核对后再启动服务。普通安装升级命令不会代替这些迁移步骤。

## 版本和范围

源版本为 Python `v1.1.5`，控制库 Schema 6、用量库 Schema 9。目标为 [v2.0.0 正式版](https://github.com/Alfonsxh/codex-cpa-pool/releases/tag/v2.0.0)。未知 Schema 必须停止，不能仅凭版本名称推断兼容性。

从明确的 `v2.0.0` Release 下载脚本、归档、发布环境和校验文件，并核对镜像摘要；迁移过程中不要使用会变化的 `latest` 选择目标。更换目标版本前重新核对兼容性。

保留原业务根目录、现有公网入口和账号上游镜像。产品更名为 Pool、默认安装目录变化，均不要求搬迁旧业务数据。服务器地址和实际路径由操作者本地配置及当次容器挂载确定，不写入公开手册。

本流程采用停机离线转换。转换工具、最终备份与启动顺序须先在副本上演练。

## 必须保留的数据

| 内容 | 保留要求 |
| --- | --- |
| 历史 Token 用量 | usage_events 全部原记录，包括 ID、event_key、时间、用户/账号/团队、输入/输出/缓存/推理/总 Token、加权 Token 和原权重版本 |
| 账号及关联 | accounts、user_routes、teams、user_team_memberships，以及账号端口、代理模式、启用状态 |
| API Key | key_records 全部记录和状态、internal_keys 原字节；撤销和轮换记录也保留，不重新创建用户或发 Key |
| 登录与 OAuth | 原密码哈希、登录凭据、OAuth 文件、管理密钥和匹配的 control-plane.key；会话过期可重新登录，不能重置账号密码 |
| 额度和配置 | 已用量、个人额度、奖励/重置记录、业务时区及原推理倍率；原聚合、元数据和自增序列同时保留 |
| 恢复材料 | 两份数据库、配对主密钥、auth/configs/management、Gateway/Edge 状态、旧 Compose/环境文件、Caddy 配置与证书数据、逐容器镜像及挂载清单 |

历史数据即使关联了已删除用户或账号，也照原样保留。迁移不执行用量清理、账号清理、Key 轮换或历史重算。

## v2.0.0 的最小适配

| 变化 | 本次处理 |
| --- | --- |
| 控制库 6 → 7 | 在工作副本上使用该 Release 的控制库迁移逻辑，补充团队 tag_style 等受支持结构；原业务字段逐行核对。未知结构停止 |
| 用量库 9 → 10 | 在事务内给 user_quota_policies 增加 reset_at INTEGER，原值为 NULL；验证全部列、索引和约束后再写 PRAGMA user_version=10。原用量事件不改写 |
| 个人额度默认每周恢复默认值 | 显式保存 user_quota.reset_personal_weekly_on_new_week=false，延续旧版个人额度规则 |
| 新增模型倍率，Astra 默认 4 倍 | 历史 weighted_tokens 原样保留。为延续旧版新请求扣量规则，本次把目标版本的模型倍率及 unknown 回退明确设为 1，保留原推理倍率；日后调整倍率作为独立变更 |
| 统一业务时区 | 将旧额度/通知时区等价映射为 system.timezone，保持原自然日/自然周边界；宿主机时区不变，容器 UTC 不代表历史时间戳要转换 |
| API Key 默认前缀变化 | 保留原 identity.key_prefix 和全部现存 Key，不按新前缀改写旧 Key |
| 账号配置挂载 | configs/<account>.yaml 转为 configs/<account>/config.yaml；按原 Image ID、端口、网络和 OAuth 挂载重建，逐个鉴权探测 |
| 安装器保留既有 target.env | 安装器只替换既有文件的四个镜像字段。Python 目标没有该文件及 release-manifest.json 时，仍需首次接管准备，不能直接运行普通升级 |

依据：[控制库迁移](../internal/controlplane/store.go)、[控制库 Schema](../internal/controlplane/schema.go)、[用量 Schema](../internal/usage/schema.go)、[严格 Schema 校验及写入](../internal/usage/writer.go)、[模型倍率](../internal/usage/multiplier_policy.go)、[Collector 默认值](../internal/collector/runtime.go)、[业务时区](../internal/sitetime/timezone.go)、[账号挂载迁移](../internal/runtimeops/account_lifecycle.go)、[安装器](../scripts/run.sh)。实施时以目标 Tag 的实现为准，不混用其他版本代码。

不能仅修改用量库版本号；也不能启动完整 Go Admin 来试探生产库是否兼容。Admin 启动包含数据库、配置及账号容器变更。

## 执行步骤

### 1. 停机前准备

- 下载并校验固定 Release 和镜像；目标只拉取，构建/发布通过 GitHub Release 工作流完成。
- 准备一次性离线转换/核对工具：只接受已识别的 Schema 6/9，源备份只读，输出到独立工作副本；重复执行须验证后无操作，未知列、错误密钥或不完整输入立即停止。
- 先用备份副本验证转换、原字段核对和恢复流程，测出耗时。在线演练副本不代替最终停机备份；演练不启动生产 OAuth 副本、不消费生产队列、不连接生产 Docker Socket。
- 准备 target.env 和发布元数据，明确填写原部署路径、账号 Compose 项目/实例/网络及 Edge 端口；Go 控制服务使用不冲突的项目身份。
- 已有 Caddy 等反向代理继续承担域名和 TLS，使用 external 模式。容器代理需准备到 Go Edge 的持久网络和唯一后端名。

### 2. 暂停业务并取得最终备份

1. CPA 站点进入维护模式，阻止新 API 请求及 Admin/Portal 写入，覆盖直达 Gateway/账号的旁路入口。
2. 等待已有请求和 SSE 结束。暂停旧管理后台及调度中的配置/生命周期写入，保留唯一旧 Collector；账号容器暂不停止，让最后请求的用量完整入库。
3. 确认每个账号队列最后批次消费及数据库提交成功，再平滑停止 Collector、旧 Admin/Web/Gateway/Edge、management 辅助容器和账号容器。若 Collector 依赖 Admin，先冻结 Admin 写接口，最后批次提交后再停 Admin。禁止一开始就整项目 down。
4. 检查无遗留 Writer 或自动拉起任务。数据冻结后，通过 SQLite Backup API 取得两份数据库，并同批保存密钥、OAuth、配置和运行清单。在无写入时顺序备份两库属于同一业务停写点。
5. 基线备份放在原业务目录之外，保持不可修改，另保存受保护的异机副本。两库 quick_check=ok，秘密解密/摘要正确，恢复副本可读。WAL 中的数据必须进入备份，不能只复制在线主文件。

旧队列使用 AUTH/LPOP，取出与入库不是同一事务，最后排空不可省略；不额外运行消费者“查看队列”。无请求产生后，逐账号完成空批次确认，并确认此前批次提交成功。失败时停在此阶段，不继续停账号后丢弃队列。[队列实现](../internal/collector/queue.go)

### 3. 离线转换并核对

从最终基线再复制工作副本，执行上述 Schema 和配置适配；原备份不修改。放行条件：

- 所有历史事件按原 ID 分块、规范化编码计算行摘要，前后一致；同时比较 COUNT、MAX(id)、event_key 集合及各类 Token 总和。
- 账号、路由、团队、Key 原字段逐行一致；密码哈希及内部/外部 Key 字节一致，个人额度及历史动作完整保留。
- sqlite_sequence、usage_meta 和原周聚合保留；只有新增字段/配置允许出现明确差异。
- 原主密钥能解密所有秘密，OAuth 文件清单、摘要和权限一致；两库 quick_check 正常，迁移未新增孤立关系。

有任何不符就停止并修正工作副本。此时尚未开放 Go，可以恢复原 Python 服务。

### 4. 启动 Go，保持维护并验收

1. 所有相关服务保持停止，将已验证副本发布到原业务根目录；保留原密钥及 auth 数据。归档旧 WAL/SHM，不让旧旁路文件配上新数据库，两库替换完成前禁止启动服务。
2. 安装对应发布元数据和 target.env，补齐所需目录。首次接管走受控现有目标路径，不调用 cpa-bootstrap 创建空库。
3. 激活 Go 唯一所有权，准备 Go 可读的非空账号/Key 快照及新鲜心跳。必要时通过该版本 Collector 的 --once 在停流状态发布快照，正常退出后再启动常驻 Collector。首次启动顺序须在演练中验证，不能依赖过期 Python 快照。
4. 启动 Go 核心，按演练步骤转换账号挂载并恢复每个原运行账号。停止容器的自动挂载迁移可能保持停止状态，必须显式启动并逐个探测；本次不同时升级上游账号镜像。
5. 启动唯一 Collector 和必要 Worker。验收前保持账号编辑/删除、Key 轮换、自动账号切换及清理等业务变更暂停；恢复生产通知时核对原发送水位，不在演练中发送。
6. 让既有反向代理连接 Go Edge，保留域名、TLS 和原端口。容器代理重载后须核对实际配置，避免单文件挂载仍指向旧 inode。
7. 先核对历史数据、账号和页面，再用原 Key 验证 models、非流式 Responses、SSE 与新增用量入库。原撤销 Key 继续拒绝。

### 5. 恢复业务

数据核对、账号探测、API 和页面验收全部通过才解除维护。原用户应能查询历史 Token，旧 Key 可继续使用，新请求产生新记录，历史记录保持原样。

建议预留 30–60 分钟维护窗口，实际由演练耗时及在途流式请求时长确定；失败时允许继续停机处理。备份保留至业务确认，不随部署成功自动删除。

## 失败处理

**Go 尚未产生业务写入：** 停止 Go，恢复同批旧数据库、密钥、配置、账号挂载和旧镜像，恢复 Caddy 后端后启动 Python。账号已正常刷新 OAuth 时，应核对并保留最新有效文件，不盲目覆盖回旧 refresh token。

**Go 已产生验收用量或开放后的业务写入：** 继续维护，保存当前完整状态并优先前向修复。不能直接用旧备份覆盖当前库。确需回 Python 时，先核对并合并新增事件及其他业务变化，再针对性恢复；本方案不承诺放流后的一键降级。

真实 Responses/SSE 验收也会产生新用量，即使公网仍维护，也属于后一种情况。允许停机使失败可留在维护窗口处理，无需为此次任务预建完整双向迁移平台。

## 验证与交付边界

转换工具需验证原数据保持、重复执行、缺列/未知版本拒绝、事务失败回滚和真实备份恢复。实现发布前执行 `make -f scripts/build.mk verify`，前端使用 `npm --prefix frontend run test:e2e`；根 Makefile 目前仅提供前端开发入口。

迁移验收必须包含：备份恢复演练、最终停机备份、转换前后原字段逐行摘要与历史 Token 总和核对，以及原 Key 的 models、普通 Responses、SSE 和新增用量入库。账号配置目录转换应保留原上游 Image ID，Edge 状态目录权限须满足服务读取要求。

本仓库的源码、浏览器和发布包检查不代替实际目标的数据迁移验收。一次性转换工具、密钥、备份位置和目标地址由操作者私下管理。完成上述验收后，后续 Go 版本更新使用 [日常升级](upgrade.md)。
