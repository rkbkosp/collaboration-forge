---
name: collab-forge-client
description: 使用独立 forge CLI 或 Forge Pi extension 管理本机 Issue、评论、依赖、时间线及带自动续租的排他执行；适用于认领任务、跨 Agent 协作、提交真实 evidence、安全重试和明确授权的 Human 管理。不替代 OS 沙箱，不把 Owner 当作执行权。
---

# Collab Forge client

Forge 有两个实际入口，底层 Issue/lease 语义相同但 execution runtime 不同：

- **独立 CLI**：不要求 Pi；普通执行命令通过一个前台 session broker 持有续租、精确重试状态和 lease。完整参数、Human 命令及安装说明见 [references/cli.md](references/cli.md)。
- **Pi extension**：由 Pi 0.85.1 加载 `pi-extension/forge.ts`，直接使用 Pi session UUID 与 Forge HTTP API；不启动、读取或复用 CLI broker/socket。

先运行 `forge --help`。若 PATH 未安装，从可信仓库运行
`node /绝对路径/collab-forge/scripts/forge.mjs`；不要安装未经确认的同名第三方工具。Pi 的安装、版本和生命周期说明见下方“Pi extension”。

## 不可违反的规则

1. Issue 是工作权威；Owner/Assignee 是长期责任，**不是 lease**。只有当前 exact live lease 才允许执行任务。
2. Worker 只能使用 worker 凭据。凭据由操作者配置 `FORGE_WORKER_TOKEN_FILE`，不得读取、打印、复制其内容，不使用 `cat`、`env`、shell trace 或日志收集暴露凭据。不索取 supervisor token 来绕过拒绝。
3. `FORGE_URL` 只能为 loopback IP literal。不能改为远程地址、开启未认证 listener 或直接改 Kata DB。
4. **CLI worker** 为每个独立 worker 建立自己的新 session broker。不要复用其他 Agent 的 socket、nonce、proof 或历史 execution；broker 是 execution runtime，短命 CLI 调用只是其客户端。Pi 不使用 broker/socket，而是在每个 Pi runtime 内创建全新的 Controller。
5. CLI 写文件、edit/apply_patch、运行会修改文件的 shell 前执行 `forge session guard`，必须 exit 0 且 `allowed:true`。Pi 则由 `tool_call` hook 自动对 `edit`、`write`、`bash`、`apply_patch` 做同等 exact-lease preflight。两者都是协作 preflight，不会取消已经运行的 shell，也不是文件系统沙箱。
6. 在**已成功初始化的 runtime** 中，不持 lease 仍可 list/get/graph/timeline/create/comment/link；冲突时贡献发现，不能强行执行或谎称取得 lease。Pi 配置失败时没有 Controller，十个 Forge model tools 都不可用；不要把“无 lease 可贡献”误解为“配置不可用时仍能调用”。
7. 只有真实执行过的测试、存在的 commit/PR、实际审查路径可作 evidence。提交 evidence 不等于服务端替你验证内容。
8. CLI 遇到不确定 acquire/close 后保持 broker 活着，使用 `forge session retry` 重放原请求。Pi 没有 `forge session retry` 命令或 retry model tool；再次调用 `issue_claim`/`issue_close` 时必须使用原始完整参数，由插件重用原 attempt、execution proof 和 close key。不要改 body、生成新 key、先 acquire 新 lease 或绕过 receipt replay 做 live preflight。
9. CLI broker 崩溃/重启后不恢复旧执行；新 broker 使用新目录/socket，等待旧 TTL 回收。Pi reload/new/resume/fork/clone 会结束旧 runtime 并创建 fresh runtime；crash 同样不依赖 cleanup，等待 TTL。请 Human 检查 issue/timeline 确认不确定的 close。
10. CLI 任务结束显式停止自己创建的 session；Pi 可用 `/forge-stop` 或 `/forge-logout` 停止 runtime。正常 stop 都是 bounded best-effort release；意外退出靠 TTL，不把 stale socket 当作活 lease，也不删除共享锁来接管。

## Pi extension

### 安装与配置

Pi 路径要求 Node >=22.19、Pi **0.85.1**：

在已配置项目绑定的宿主 wrapper 中，优先使用仓库 CLI 的路由入口：

```sh
forge pi
```

它在启动 Pi 前按当前 canonical Git root 选择项目、loopback URL 和 worker
token 文件，并自动加载 `pi-extension/forge.ts`；用户不需要指定端口或 token。
未知仓库、冲突的连接覆盖和旧 socket 会 fail closed。Pi extension 本身不扫描
端口、不从模型参数选择项目，也不复用 CLI broker。

没有宿主 wrapper 时才使用显式 direct-mode 配置：

```sh
npm ci
export FORGE_URL=http://127.0.0.1:7347
export FORGE_WORKER_TOKEN_FILE=/absolute/path/to/worker-token
export FORGE_TTL_SECONDS=300
pi -e ./pi-extension/forge.ts
```

生产适配器只读取上述三个环境变量；默认 URL 是
`http://127.0.0.1:7347`，TTL 默认 300 秒，范围 60–3600。worker token
必须来自当前用户拥有的 regular `0600` 文件，不接受 symlink、空 token、含空白
或超过 16 KiB 的文件。URL 必须是 loopback IP literal（`127.0.0.0/8` 或
`::1`），拒绝 hostname、路径、userinfo、query/fragment、重定向和代理转发。
不要把 token 放进 prompt、shell 参数、session 配置或日志。根 `package.json` 的
`pi.extensions` 只选择 `pi-extension/forge.ts`；CLI `-e` 适合试用，正常安装按
Pi 的 extension discovery 规则进行。

### 工具、续租与关闭

Pi 注册十个严格 schema 的 model tools：
`issue_list`、`issue_get`、`issue_graph`、`issue_create`、`issue_comment`、
`issue_link`、`issue_claim`、`issue_renew`、`issue_release`、`issue_close`。
工具不能提交 authority、execution token/attempt、force/replace 或 retry protocol
字段；`issue_close` 的 evidence 是 typed Kata evidence，不是任意字符串。读取、
创建、评论和添加 link 不要求 execution lease；有 Controller 时先 claim 一个
issue，才能编辑工作树。

`issue_claim`、`issue_renew`、`issue_release`、`issue_close` 按 sequential 模式
执行，其余工具可 parallel。heartbeat 自动按 `min(TTL/3, 30s)` 续租。每次
`edit`、`write`、`bash`、`apply_patch` 前，Pi hook 都通过 `issue_get` 检查当前
exact ClaimUID、issue UID 和 principal tuple；同 holder 的新 ClaimUID 也不是旧
tenure。过期、替换、明确丢失或网络无法确认都会阻止后续编辑。close 由插件内部
生成 Idempotency-Key，并附 exact lease proof；模型不能自行伪造 header。

不确定的 claim/close 结果保留在**当前 Pi runtime 内存**中：重复调用原始完整
`issue_claim` 或 `issue_close` 才能重放，不能改 ref、purpose、evidence 或其他
body，即使随后 get 已显示 released/closed 也一样。新 Pi runtime 不从 session
history 恢复旧 execution；同一个真实 Pi session UUID 下的两个 fresh runtime
也会生成不同 attempt，旧 lease 未过 TTL 前冲突是预期的。`before_agent_start`
只注入脱敏的当前状态/lease context，不写 appendEntry 或恢复历史执行。

每个 tool result 会递归脱敏；输出超过 2000 行或 50 KiB 时，完整的**脱敏** JSON
写入拥有的 `0600` 临时文件，并把路径放入结果。Pi 不是 OS sandbox：hook 不覆盖
custom tools、其他 extensions、用户 `!` 命令或已经开始运行的 shell，也不能消除
parallel-tool 的 preflight-to-write race；服务端事务 close fencing 才是正确性边界。

### Pi 生命周期

- `session_start` 读取当前 `ctx.sessionManager.getSessionId()`，创建 fresh Controller。
- `session_shutdown` 先本地失效、停 heartbeat，再在约 1.5 秒内 best-effort release；release 失败时 lease 仍可能存活到 TTL。
- reload/new/resume/fork/clone 是旧 shutdown 后的新 runtime；`session_tree` 不 teardown，也不 release。
- `/forge-stop` 和 `/forge-logout` 停止当前 Forge runtime；reload 后才能 claim 新 execution。Pi 内置 provider `/logout` 是 OAuth logout，不是 Forge 命令，也不会被拦截。
- kill -9/crash 不保证 cleanup；依赖服务端 timed lease expiry。

普通 agent turn 完成不是 `session_shutdown`，不会自动 release。Pi 默认请求超时 10 秒，shutdown release 上限约 1.5 秒。

## CLI broker 工作流

### 1. 确认服务与任务

```sh
forge health
forge project
forge issue list --status open
forge issue get ISSUE_REF
forge issue graph ISSUE_REF --depth 2
```

`forge health` 直连公开 `/health` 时只需 loopback URL、无需 worker token；
`project` 和其他 worker 操作仍需 worker 配置。只用 bare short ID 或 ULID，不用
`project#id`。先理解 acceptance criteria、owner、状态、依赖、现有 lease；不把
聊天承诺当完成。

### 2. 建立独立 execution runtime

由当前 worker 创建一个新的私有目录，不将其他人的 `FORGE_SOCKET` 继承为自己的执行会话：

```sh
SESSION_DIR=$(mktemp -d /tmp/forge-worker.XXXXXX)
export FORGE_SOCKET="$SESSION_DIR/s.sock"
nohup forge session start --socket "$FORGE_SOCKET" --ttl 300 \
  >"$SESSION_DIR/broker.log" 2>&1 </dev/null &
forge session wait
forge issue claim ISSUE_REF --purpose '实现该 Issue 的已确认范围'
forge session guard
```

`start` 本身是前台服务；上例显式由 shell 放到后台。若不传 `--socket`，CLI 会在
`/tmp/forge-client-<uid>/<uuid>.sock` 生成新路径；显式路径的父目录必须是当前用户
拥有的 `0700` 目录，socket 是 `0600`、绝对路径最长 100 bytes，既有/过期路径也
不会被自动接管。记录自己的 socket 路径供之后调用，**只记录路径，不记录凭据**。
长任务自动 heartbeat，无需轮询 renew。默认 TTL 300 秒，允许 60–3600。

若 claim denied：停止执行；可 comment 发现、create 子 Issue、link 依赖；等待、选择其他任务或请 Human 协调，不自行 force-release。

### 3. 贡献与执行

```sh
forge issue comment ISSUE_REF --body-file finding.md
forge issue create --title '发现的后续任务' --body-file followup.md
forge issue link CHILD_REF --type parent --to-ref PARENT_REF
# A blocks B 表示 B 依赖 A：
forge issue link A_REF --type blocks --to-ref B_REF
forge session guard
```

guard 通过后再使用环境提供的编辑/执行工具。每个新批次前复查 guard；状态不确定立即停手。不要把本 Skill 当作 OS 权限隔离。

### 4. 完成与重试

准备 close JSON，包含真实 reason/message/evidence，使用 `forge issue close ISSUE_REF --data-file close.json`。参考中的示例只有替换为真实结果后才能使用。

- exit 0：检查返回 `issue.status`、`changed/reused`；旧 receipt 的 `reused:true` **可能对应已 reopened 的当前 issue**，不等于再次关闭成功。
- 若 `client_replayed:true,current_state_not_refreshed:true`：这是 broker 为处理 CLI 连接丢响应而缓存的历史确认结果，不是新 HTTP close。立即 `issue get` 查看当前状态；不可拿缓存的 lease 代替 guard。
- exit 3 或 session status 显示 `pendingClose/pendingClaim`：`forge session retry`。不重新拼参数。必要时先用 get/timeline 观察，但不要因此丢弃原请求。
- definite rejection：按错误修正工作/证据；不要假定失败无副作用，检查当前状态。若 broker 没有 pending 或历史确认，则 retry 明确拒绝，不会凭空重建执行。
- release：`forge issue release ISSUE_REF --reason '交接'`；最终 `forge session stop`。

### 5. 汇报

用 `forge issue timeline ISSUE_REF --all` 汇报真实贡献、执行、close reason/evidence 和释放。`reset_required:true` 表示旧历史已清理；`truncated:true` 表示可能未读完，使用 `next_after_id` 继续，不能声称完整覆盖。说明哪些验证未执行。

CLI 成功结果默认是 JSON。给 Human 阅读时可在同一命令前加 `human`，例如
`forge human issue get REF`；它只改变成功结果的确定性渲染，不改变权限、session、
网络或 mutation 语义，并过滤 execution/attempt/token 字段。脚本需要 JSON 时用
`forge human --format json ...` 或 `--json`；错误始终是脱敏 JSON stderr envelope，
退出码仍为 0 成功、1 普通错误、2 usage/缺少 session、3 不确定 mutation、4 guard
阻止。

## Human 权限

只有用户明确要求管理操作且已有独立 supervisor 凭据时才使用 `forge admin ...`。它从不自动回退到 worker 凭据。赋 owner/priority、reopen/delete、移除关系、force-release 都不是普通 worker 的执行能力。删除需要完整确认串；发生冲突应让 Human 决策，而不是借 admin 绕过 lease。
