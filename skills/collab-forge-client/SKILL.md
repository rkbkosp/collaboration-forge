---
name: collab-forge-client
description: 使用独立 forge CLI 管理本机 Forge Issue、评论、依赖、时间线及带自动续租的排他执行；适用于认领任务、跨 Agent 协作、提交真实 evidence、安全重试和明确授权的 Human 管理。不替代 OS 沙箱，不把 Owner 当作执行权。
---

# Collab Forge client

使用独立 `forge` 命令，不要求 Pi。先运行 `forge --help`；完整参数、Human 命令及安装说明见 [references/cli.md](references/cli.md)。若 PATH 未安装，从仓库运行 `node /绝对路径/collab-forge/scripts/forge.mjs`；不要安装未经确认的同名第三方工具。

## 不可违反的规则

1. Issue 是工作权威；Owner/Assignee 是长期责任，**不是 lease**。只有当前 exact live lease 才允许执行任务。
2. Worker 只能使用 worker 凭据。凭据由操作者配置 `FORGE_WORKER_TOKEN_FILE`，不得读取、打印、复制其内容，不使用 `cat`、`env`、shell trace 或日志收集暴露凭据。不索取 supervisor token 来绕过拒绝。
3. `FORGE_URL` 只能为 loopback IP literal。不能改为远程地址、开启未认证 listener 或直接改 Kata DB。
4. 每个独立 worker 建立自己的新 session broker。**不要复用其他 Agent 的 socket、nonce、proof 或历史 execution**。broker 是 execution runtime，短命 CLI 调用只是其客户端。
5. 写文件、edit/apply_patch、运行会修改文件的 shell 前执行 `forge session guard`，必须 exit 0 且 `allowed:true`。未知、网络异常、过期、被强制释放都停止编辑。它是协作 preflight，不会取消已经运行的 shell，也不是文件系统沙箱。
6. 不持 lease 时仍可 list/get/graph/timeline/create/comment/link；冲突时贡献发现，不能强行执行或谎称取得 lease。
7. 只有真实执行过的测试、存在的 commit/PR、实际审查路径可作 evidence。提交 evidence 不等于服务端替你验证内容。
8. 不确定 acquire/close 后保持 broker 活着，使用 `forge session retry` 重放原请求。**不要改 body、生成新 key、先 acquire 新 lease 或绕过 receipt replay 做 live preflight**。
9. broker 崩溃/重启后不恢复旧执行。新 broker 使用新目录/socket，等待旧 TTL 回收；请 Human 检查 issue/timeline 确认不确定的 close。
10. 任务结束显式停止自己创建的 session。正常 stop bounded best-effort release；意外退出靠 TTL，不把 stale socket 当作活 lease，也不删除共享锁来接管。

## 工作流

### 1. 确认服务与任务

```sh
forge health
forge project
forge issue list --status open
forge issue get ISSUE_REF
forge issue graph ISSUE_REF --depth 2
```

只用 bare short ID 或 ULID，不用 `project#id`。先理解 acceptance criteria、owner、状态、依赖、现有 lease；不把聊天承诺当完成。

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

`start` 本身是前台服务；上例显式由 shell 放到后台。记录自己的 socket 路径供之后调用，**只记录路径，不记录凭据**。长任务自动 heartbeat，无需轮询 renew。默认 TTL 300 秒，允许 60–3600。

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

## Human 权限

只有用户明确要求管理操作且已有独立 supervisor 凭据时才使用 `forge admin ...`。它从不自动回退到 worker 凭据。赋 owner/priority、reopen/delete、移除关系、force-release 都不是普通 worker 的执行能力。删除需要完整确认串；发生冲突应让 Human 决策，而不是借 admin 绕过 lease。
