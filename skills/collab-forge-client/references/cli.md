# CLI reference

## 安装

在可信 Collab Forge checkout：

```sh
npm ci
npm link                         # 可选：向当前 Node prefix 安装 forge 命令
forge --help
forge skill install --target "$HOME/.agents/skills/collab-forge-client"
```

不使用 `npm link` 时执行 `node /绝对路径/collab-forge/scripts/forge.mjs ...`。Skill 安装不覆盖已有目录；升级时由操作者先审核/备份旧 Skill。可用 `forge skill path` 查看随包分发的原文。支持 macOS/Linux、Node >=22.19；不要求安装 Pi 或调用模型。

## 配置

- `FORGE_URL` / `--url`：默认 `http://127.0.0.1:7347`，仅 loopback literal。私有 direct dispatcher 绕过 Node 环境代理，防止 `HTTP_PROXY` / `NODE_USE_ENV_PROXY` 将凭据转出本机。
- `FORGE_WORKER_TOKEN_FILE` / `--worker-token-file`：owned regular 0600 文件，拒绝 symlink。不是 token 值。
- `FORGE_ADMIN_TOKEN_FILE` / `--admin-token-file`：独立 supervisor 文件，仅 `admin` 命令使用。配置了它也不会让普通 worker 命令升级权限。
- `FORGE_SOCKET` / `--socket`：该 worker 的前台 session broker socket，父目录 owned 0700、socket 0600，绝对路径不超过 100 bytes。已有路径不会被自动接管。
- `--ttl`：session TTL，60–3600 秒，默认 300；heartbeat TTL/3、最长 30 秒。
- `--session-id`：可选 v4/v7 session UUID，仅作公开归属关联，不是 execution proof。通常让 CLI 自动生成。

不要将凭据放 argv、shell history、模型上下文或证据文件。0600 不能防同 UID 的工具读取文件；不可信 worker 需要额外账户/沙箱/凭据代理，且不能访问 supervisor 文件、服务 DB 或 signing key。

## 命令矩阵

所有普通结果为 JSON stdout；错误为 sanitized JSON stderr。`--help` 为文本。

| 命令 | 参数 |
| --- | --- |
| `health`、`project` | 健康与配置项目发现 |
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
