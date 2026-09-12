# CLI reference

## 安装

在可信 Collab Forge checkout：

```sh
npm ci
npm link                         # 可选：向当前 Node prefix 安装 forge 命令
forge --help
forge skill install --target "$HOME/.agents/skills/collab-forge-client"
forge pi                         # host-routed Pi with the Forge extension
```

不使用 `npm link` 时执行 `node /绝对路径/collab-forge/scripts/forge.mjs ...`。Skill 安装不覆盖已有目录；升级时由操作者先审核/备份旧 Skill。可用 `forge skill path` 查看随包分发的原文。支持 macOS/Linux、Node >=22.19；CLI 不要求安装 Pi 或调用模型。Pi extension 是另一条入口，直接加载 `pi-extension/forge.ts`，不使用本参考中的 session broker。

## 配置

- `FORGE_URL` / `--url`：默认 `http://127.0.0.1:7347`，仅 loopback literal。私有 direct dispatcher 绕过 Node 环境代理，防止 `HTTP_PROXY` / `NODE_USE_ENV_PROXY` 将凭据转出本机。
- `FORGE_WORKER_TOKEN_FILE` / `--worker-token-file`：owned regular 0600 文件，拒绝 symlink。不是 token 值。
- `FORGE_ADMIN_TOKEN_FILE` / `--admin-token-file`：独立 supervisor 文件，仅 `admin` 命令使用。配置了它也不会让普通 worker 命令升级权限。
- `FORGE_SOCKET` / `--socket`：该 worker 的前台 session broker socket，父目录 owned 0700、socket 0600，绝对路径不超过 100 bytes。已有路径不会被自动接管。
- `--ttl`：session TTL，60–3600 秒，默认 300；heartbeat TTL/3、最长 30 秒。
- `--session-id`：可选标准 UUID，仅作公开归属关联，不是 execution proof。通常让 CLI 自动生成；Pi 使用 Pi 自己的真实 session UUID，不能用 CLI 参数覆盖。

不传 `--socket` 的 `forge session start` 会自动生成
`/tmp/forge-client-<uid>/<uuid>.sock`，并在 stdout 输出 socket、sessionID 和
protocol；`start` 仍保持前台运行。`forge health` 不带 socket 时只验证 loopback URL
并访问公开 `/health`，不需要 worker token；`project`、issue/tool 操作和带 socket
的 health 则使用 broker 配置。

不要将凭据放 argv、shell history、模型上下文或证据文件。0600 不能防同 UID 的工具读取文件；不可信 worker 需要额外账户/沙箱/凭据代理，且不能访问 supervisor 文件、服务 DB 或 signing key。

## 命令矩阵

所有普通结果为 JSON stdout；错误为 sanitized JSON stderr。`--help` 为文本。

需要给 Human 直接阅读时，使用相同的 worker/admin/read 参数加 `human` 前缀：

```sh
forge human issue list --status open
forge human issue get REF
forge human issue timeline REF
forge human admin list --status open
```

Human 模式只改变成功结果的渲染，不改变参数校验、权限、session、网络或 mutation 语义；默认输出稳定的标题、字段和表格，并过滤 execution/attempt/token 字段。需要脚本处理时使用 `--format json` 或 `--json` 恢复 JSON stdout。错误仍为相同的 JSON stderr envelope：`{"error":{"code","message","ambiguous", "hint?", "data?"},"status?}`。

`forge codex ...` 使用 forged daemon-owned runtime，不能在其中再启动 session broker。
`forge checkout ...` 在 Codex 内使用该 runtime，在普通 CLI 中使用当前私有 socket；
Pi 使用自己的 checkout tool。创建工作区要求已有 claim，不会增加另一个续租器。
普通 CLI 的 checkout list/status/archive 可只用项目 worker 配置；archive 要求 Issue 已关闭。

| 命令 | 参数 |
| --- | --- |
| `health`、`project` | 健康与配置项目发现 |
| `pi` | 通过宿主 cwd 项目绑定启动 Pi，并自动加载 Forge extension |
| `checkout ISSUE` | `--dirty` 或 `--ref COMMIT`；`--source REPO` 或 `--recover ID`；`--socket PATH`、`--no-wait` |
| `checkout list/status/archive` | `list [ISSUE]`、`status ID`、`archive ID`；返回工作区元数据 |
| `issue list` | `--status open/closed`、`--limit 1..1000` |
| `issue get REF` | 当前 issue、owner、lease、comments、links |
| `issue graph REF` | `--depth 1..10` |
| `issue create` | `--title`、`--body` / `--body-file` |
| `issue comment REF` | `--body` / `--body-file` |
| `issue link REF` | `--type parent/blocks/related --to-ref REF` |
| `issue timeline REF` | `--after-id N --limit N`；`--all --max-pages N` |
| `issue claim REF` | live broker 必需；`--purpose` |
| `issue renew REF` | live broker；自动 heartbeat 外的显式续租 |
| `issue release REF` | live broker；`--reason` |
| `issue close REF` | live broker；`--reason --message --evidence-file`、可选 `--if-match` |
| `tool issue_NAME` | `--data JSON` / `--data-file FILE`；相同严格 worker schema，不是 raw API escape |
| `session start` | 前台运行、生成新 runtime；输出 socket/sessionID，不输出私有状态 |
| `session wait/status` | 等待 ready / 查看安全状态 |
| `session guard/context` | exact live preflight / 工作上下文 |
| `session retry` | 原 pending acquire/close 精确重试，或恢复最近已确认的 CLI 丢响应结果；无新参数 |
| `session stop` | 终止 runtime、停止 heartbeat、best-effort release |

带 socket 时，health/project/worker 操作均使用 broker 固定的服务与身份；不能在这些调用上另传 URL、token file、TTL、session ID 覆盖它们（可在 session start 时设置）。Admin 始终直连其配置 URL，并使用独立 supervisor 文件，不经过 worker socket。

普通贡献可以不启动 broker，此时每个 CLI 调用是短暂客户端；若需要统一 session 归属或执行生命周期，所有调用带同一私有 socket。不带 socket 的 claim/renew/release/close 会拒绝，而不是创建瞬间释放的虚假执行。

JSON 输入必须是对象。命名 flag 不能覆盖 `--data` 中同名字段。`--data-file`、body/message/evidence 文件支持 `-` 从 stdin 读取，最大 1 MiB；同一命令不要多次消费 stdin。

限制也适用于恢复判断：session IPC 请求/响应上限分别为 1 MiB/8 MiB，读写 I/O
超时 5 秒，单个 broker operation 30 秒；直接 Controller 请求默认 10 秒，普通
ClientHTTP read/project/health 请求 15 秒。超时或已发出的 mutation 必须按“不确定”
处理并按本参考重试，不能盲目重发。

Timeline 根据**扫描过的所有项目事件**推进 cursor，即使该页过滤后没有事件也要继续。分页上限会显式 `truncated:true`；单次聚合超过 8 MiB 报错，改为逐页读取。缺失历史不能由客户端恢复。

## Evidence

Close reasons：`done`、`wontfix`、`duplicate`、`superseded`、`audit-no-change`，具体要求由 Kata gate 校验。证据只接受对应 type 的一个字段：

| type | field |
| --- | --- |
| commit | sha |
| pr | url |
| test | command |
| reviewed-paths | paths（非空字符串数组） |
| external | account |
| no-change-audit | rationale |
| duplicate-of / superseded-by | issue_ref |

**仅在实际成功执行了 `npm test` 后**，才能使用类似：

```json
{
  "reason": "done",
  "message": "已完成该 Issue 的验收范围，并实际运行测试确认结果；详细实现与限制见关联提交和评论。",
  "evidence": [{"type": "test", "command": "npm test"}]
}
```

保存为 close.json 后：`forge issue close ISSUE_REF --data-file close.json`。如果响应丢失，保持 broker 活着并运行 `forge session retry`。原 body/key/proof 在 broker 内存中，既不打印也不持久化。不要先做 guard 阻止这个 receipt retry。

若 HTTP 已成功但 CLI socket 响应丢失，broker 可返回最近 acquire/close 的已脱敏确认结果，标记 `client_replayed:true,current_state_not_refreshed:true`。这是历史结果，不代表当前状态；用 get 复查，用 guard 确认当前执行权。新 claim/renew/release/close 会清除旧缓存。broker 重启不会恢复这些结果。

## Human 管理

只能在已明确授权的 supervisor 上下文使用：

```sh
forge admin create --title 'Human plan' --body-file plan.md
forge admin list --status open
forge admin get REF
forge admin update REF --owner alice --priority 1
forge admin update REF --clear-owner --priority none
forge admin comment REF --body-file review.md
forge admin link REF --type related --to-ref OTHER
forge admin label add REF urgent
forge admin label remove REF urgent
forge admin unlink REF LINK_ID
forge admin reopen REF
forge admin force-release REF --reason 'Human 已确认原执行者离线'
forge admin delete REF --confirm 'DELETE actual-project#actual-short-id'
```

`admin close REF` 是明确的 Human 管理 close，不冒充 worker exact-tenure close；优先用普通 `issue close` 完成 Agent 任务。`admin request METHOD /api/v1/PATH --data-file FILE` 可使用服务已开放的其他原生接口（如扩展 metadata），仍受 restricted embedding profile 限制；不能用来创建 token/federation 管理权限。不允许远程 URL、跳转或用客户端指定 Authorization。原生 CAS 接口可用 `--if-match`；原生重试协议可用 `--idempotency-key` 配合完整原始请求，`admin close` 提供 key 时默认使用 close-v1。`admin request` 不替用户补协议字段，也不自动重放。

Human/raw mutation 不自动重试；网络/5xx 后先检查状态，不要盲目重复非幂等操作。只有 broker 管理的 worker acquire/close 有私有 exact retry 快照。

## 退出码与恢复

- 0：成功；注意 inspect `changed/reused/reset_required/truncated`。
- 1：本地配置、连接或 HTTP definite error。
- 2：参数错误，或缺少 live session / supervisor 配置。
- 3：mutation 结果不确定。worker pending acquire/close 使用 `session retry`；其他操作观察服务端状态。
- 4：guard 不允许编辑。

SIGINT/SIGTERM 尝试释放并退出；SIGKILL 不执行 cleanup，lease 等待 TTL。不要通过删除他人的 socket 或恢复历史 execution 接管。为新 broker 创建新目录；Human 可核验状态后清理确认已退出进程遗留的旧目录。
