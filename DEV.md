# 实施修订（C1–C12 核验后，以本节为准）

上游固定基线：`kenn-io/kata@b1667be5603a308f8c2d44a0d6cbac2463a7eeb2`。
K1 已完成：`17b335c refactor: classify issue lease authority`。
K2 已完成：`c10bd13 feat: enable standalone lease actions with project-scoped expiry`。
K2 验证：daemon/Service/SQLite 全量、真实 PostgreSQL conformance + Claim、构建通过；PostgreSQL 全包尝试超过 600 秒，未取得全包结果。
确认报告：`kata/docs/development/standalone-lease-baseline.md`。

K3 `31c2867`、K4 `f837be0`、K5 `f5a312d`、K6 `981d394`、K7 `304d2c4`、K8 `34fadad` 已独立提交。
后续正确性提交 `b67a696`：strict close 已通过完整 execution principal guard 时，不再拿展示 actor 与 opaque holder 比较而误记 federation violation；legacy audit 不变。SQLite 全量和真实 PostgreSQL claim/guard conformance 已通过（无 skip），HTTP daemon/Service 和生成 client/build 阶段检查通过。尚不据此声称 Forge/Pi 或 PostgreSQL 全包已通过。
补丁及基线保存在 `patches/kata/`；`scripts/bootstrap-kata.sh` 逐个应用并核对完整 Git tree，已有脏 checkout 不会被覆盖。

## 修订后的提交边界

| Commit | 边界 |
| --- | --- |
| K2 | standalone lease routes + target-project opportunistic expiry |
| K3 | standalone read/show/status projection |
| K4 | authoritative-project sweeper（standalone/hub；跳过 spoke） |
| K5 | close-time authoritative lease release，与 federation audit 解耦 |
| K6 | close-v2 + exact ClaimUID/principal transaction guard |
| K7 | close-v2 idempotency / ambiguous-commit hardening |

### K2

- Acquire/Renew/Release/Status/ForceRelease：先 resolve target issue，机会性清理只限 `issue.ProjectID`；SQLite/PostgreSQL 同义。
- standalone/hub 走本地 DB；spoke 保留转发，不可本地 force-release。
- 不降低 force-release 的现有 admin/host authorization；只取消必须 hub 的 authority 限制。
- 不改 show hydration、后台 sweeper、close release、strict gate、violation audit、CLI/MCP 名称。
- 回归至少覆盖 standalone A 和 hub A 的 acquire 不清理 spoke B cache；同一 authoritative project 内过期 claim 仍清理。其余 claim 操作也应覆盖项目隔离。
- 现有跨项目机会性 expiry 是被移除的 incidental behavior，相关测试改为隔离断言；回滚测试使用同项目 stale sibling，避免失去覆盖。
- `ExpireTimedClaimsForProject` 本身不判断 authority，daemon/sweeper caller 必须负责。

### K5–K7

- K5 抽窄事务 helper，例如 `releaseLiveAuthoritativeClaimOnCloseTx`；保留 federation audit，不引入 exact ClaimUID guard。
- K6 close principal 必须复用与 acquire/renew/release 相同的身份生成 helper，不以 actor 代替 execution principal，不重新拼 holder hash。
- K6 同时引入 `retry_protocol: close-v2`。有 `If-Lease-Match` 却无 close-v2 必须拒绝；新 strict 客户端同时发送二者。
- 核验旧服务会拒绝未知 close-v2 enum，而非忽略 lease header 后执行。Capabilities discovery 不是 correctness boundary。
- K7 fingerprint 加入 protocol 和 expected_claim_uid，同时保留既有 close-v1 重试兼容性。
- receipt replay 必须在 live lease validation 前；同 key/同 tenure 恢复 receipt，同 key/不同 tenure fingerprint mismatch。
- K6/K7 为独立提交，但完整重试保护落地前不可部署 strict close。K6 不做 Pi integration。

### F4/F5/F6 execution identity

- execution_id 是 logical claim attempt / tenure identity，不是每个 HTTP request 的 ID。
- 一次新 attempt 先生成并保存 execution_id，再发送 acquire；不确定响应时重试同一 execution_id。
- 明确 denied/released/lost 后丢弃；下一次 logical attempt 使用新 ID。
- 保存必须隔离到当前 runtime；不得由新 Pi runtime 从 session 历史恢复旧 execution_id 并续租。
- 新 runtime 即便 session_id 相同也使用新 execution；旧 lease 尚未 TTL 时暂时 conflict 是安全取舍。
- shutdown：先停止 renew/失效 runtime 状态，再 bounded best-effort release；crash 依赖 TTL。
- 实现上保留真实 Pi Session UUID；私有 acquire attempt nonce 每个新 logical tenure 生成，绝不回写 session 历史。服务端派生 opaque execution subject，并返回绑定 session/issue/ClaimUID 的签名凭据，而不信任调用者自行拼 subject。公开 execution_id 不是可用来冒领的 nonce。
- renew/release/close 凭据只在插件内存中使用；不进入模型参数、输出或 session entry。服务端持久签名 key 支持同一运行中客户端跨 daemon restart 恢复未决 receipt；新 Pi runtime 仍不得恢复旧执行。

---

# 目标

构建一个面向 Human + Agent 的 local-first collaboration forge。

第一阶段不实现本地 Pull Request / Change，而只解决最核心的问题：

* Issue 是跨 session / workspace 的唯一工作权威；
* Owner/Assignee 表示长期责任归属；
* Lease/Claim 表示当前 Agent 的排他执行权；
* 多 Agent 可以继续贡献 comment、发现依赖、创建子 Issue；
* 同一 Issue 不允许两个 Agent 同时执行；
* Agent 崩溃后 timed lease 自动失效；
* 旧 Agent 恢复后不能覆盖新的执行者；
* 完成任务必须通过服务端校验 live lease 后才能 close；
* Git commit / PR / test / external evidence 继续使用 Kata 已有 evidence 模型。

Kata 当前底层 Storage 已经在普通 `CreateProject` 上支持 Acquire/Renew/Release/Expiry/ForceRelease，并有 SQLite/PostgreSQL 共同行为测试，因此本次不要重新设计 claim schema。

---

# 一、Codex 开工前必须确认的事项

要求 Codex **先输出确认报告，再开始修改**。

## C1. 固定上游基线

确认当前 Kata `main` SHA，并记录：

```text
upstream:
repository: kenn-io/kata
commit: <sha>
```

重新核对下列实现是否仍与本设计一致：

```text
internal/daemon/handlers_claims.go
internal/daemon/claim_gate.go
internal/daemon/claims_sweeper.go
internal/daemon/handlers_issues.go
internal/daemon/handlers_actions.go

internal/db/storage.go
internal/db/types.go
internal/db/sqlitestore/claims.go
internal/db/pgstore/claims_core.go
internal/db/dbtest/conformance_claims.go

service.go
access.go
```

### 影响

这是最高优先级检查。

Kata 最近 claim / embedding / federation 代码演化较快；如果 upstream 已经实现 standalone lease，不应重复实现。

---

## C2. 确认 Lease 与 Owner 必须继续是两个概念

必须确认：

```text
Issue.Owner
```

仍然属于长期 assignment/responsibility，

而：

```text
IssueClaim / Lease
```

属于短期 execution ownership。

不得把现有：

```text
claimIssue / ClaimOwner
```

改造成 Agent execution claim。

Forge 用户侧语义：

```text
Owner / Assignee
= 谁长期负责这个 Issue

Active Lease
= 哪一个 Agent execution 当前有执行权
```

### 影响

涉及：

```text
CLI 命名
Web UI
Pi tool schema
Access policy
Agent prompt
```

但**不需要数据库迁移**。

---

## C3. 确认 standalone / hub / spoke 三种 lease authority

当前 daemon lease route 会要求 FederationBinding，没有 binding 会得到 `federation_not_found`，但底层 DB claim 本身没有这个限制。

实现后必须形成：

```text
Standalone Project
    ↓
local DB authoritative

Federation Hub
    ↓
local DB authoritative

Federation Spoke
    ↓
remote Hub authoritative
    ↓
local cache only
```

不得把 spoke cached lease 当作本地 authority。

### 影响

主要：

```text
handlers_claims.go
handlers_issues.go
claims_sweeper.go
claim_gate.go
```

这是 standalone lease patch 的核心。

---

## C4. 不要把现有 federation claim gate 全局化

这是非常重要的设计约束。

不要简单：

```text
requireFederatedIssueClaim
        ↓
requireIssueClaim
```

然后套到所有 standalone mutation。

因为 Forge 要允许：

```text
Agent B
在 Agent A claim #123 时

✓ comment #123
✓ 创建发现出来的子 Issue
✓ 添加 dependency / related relation
```

我们的原则是：

> execution authority 单写手，knowledge contribution 多写手。

Kata 当前很多 edit/owner/priority/link/lifecycle mutation 都会调用 federation claim gate。

Standalone Lease 第一版**不要改变 Kata 默认 mutation semantics**。

真正的 worker 权限由 forged AccessController 控制。

---

## C5. Comment 必须永远不要求 execution lease

确认 `createComment` 当前没有进入 Issue lease gate。

保持：

```text
任何有项目写权限的 Agent
        ↓
comment allowed
```

原因：

Issue timeline 是 Agent 间共享知识层。

例如：

```text
Agent A claims #123

Agent B discovers:
"#123 actually depends on #141"

Agent B must still be able to record this.
```

### 影响

如果任何 proposed patch 让 comment 需要 holder lease，应立即停止并重新设计。

---

## C6. 确认 timed lease sweeper 当前只处理 federation hub

当前 `TimedClaimSweeper` 枚举 FederationBindings，只对 enabled hub 调 `ExpireTimedClaimsForProject`。

Storage 已经存在：

```text
ExpireTimedClaims
ExpireTimedClaimsForProject
```

要求 Codex确认：

* standalone timed lease 当前不会被后台 sweep；
* 不能简单调用全局 `ExpireTimedClaims()`，因为可能误处理 spoke cached claim；
* 新 sweeper 必须明确区分 authoritative project。

---

## C7. 确认 close 后 lease release 仍带 federation 假设

现有 close/claim audit 路径能够产生：

```text
issue.closed
claim.released(reason=issue_closed)
```

但相关逻辑带有 hub/federation authority 判断。

要求把：

```text
release lease on close
```

从：

```text
federation violation auditing
```

中尽可能解耦。

### 推荐结果

```text
Standalone
close → release live lease

Hub
close → release authoritative lease

Spoke
不要本地释放 authoritative remote lease
```

同时：

**不要在这个 commit 顺手把 federation `claim.violated` audit 泛化到 standalone。**

---

## C8. Strict Close 的事务边界

普通 Kata 的 claim gate 是：

```text
没有 lease
→ allow

lease 是自己
→ allow

lease 是别人
→ deny
```

Forge worker 需要：

```text
没有 lease
→ deny

lease 过期
→ deny

lease 是别人
→ deny

lease 是当前 execution
→ allow
```

这个判断必须和：

```text
Issue close
+
lease release
```

处于**同一数据库事务**。

禁止实现：

```text
GET lease
↓
HTTP 层判断
↓
时间窗口
↓
CloseIssue()
```

Kata 的 SQLite / PostgreSQL writable transaction 已经有明确的 transaction-fence boundary。

但 forged 不应直接查询 Kata 私有 `issue_claims` SQL。

---

## C9. Strict Close 建议采用显式 Lease Precondition

请 Codex核对后优先采用：

```text
If-Lease-Match: <claim_uid>
```

作为可选 API precondition。

语义类似：

```text
If-Match: issue revision
```

但两个东西独立：

```text
If-Match
= 我看到的 Issue version 还是不是那个 version

If-Lease-Match
= 我拿到的 execution tenure 还是不是那个 tenure
```

Kata AcquireClaim 每次新的 ownership tenure 都会产生新的 `ClaimUID`；同 principal 的幂等重复 acquire 返回原 ClaimUID。

推荐 Close DB 参数增加类似：

```go
type ClaimGuard struct {
    Required         bool
    ExpectedClaimUID string
    Principal        ClaimPrincipal
}
```

默认：

```text
nil
→ 完全保持现有 Kata 行为
```

Forge worker close：

```text
ClaimGuard{
    Required: true,
    ExpectedClaimUID: C123,
    Principal: currentExecution,
}
```

### 为什么优先这种设计

不要通过 forged 的 TransactionFence 偷查：

```text
issue_claims
```

否则 forged 会绑定 Kata private schema、SQLite/Postgres SQL 和 migration lifecycle。

也不推荐第一版扩张整个 `AccessDecision` public API 来描述 lease policy。

显式 precondition 更小、更容易 upstream。

---

## C10. Close idempotency 顺序绝不能被破坏

当前 close handler 会先处理 idempotency receipt，再进入真正 mutation/gate。

必须保证：

```text
第一次 close
    ↓
成功
    ↓
lease 自动 release
    ↓
HTTP response lost

客户端 retry same Idempotency-Key
    ↓
必须恢复第一次 close receipt
```

不能因为 lease 已经释放而：

```text
409 claim_required
```

所以：

> idempotency replay detection 必须发生在 strict lease validation 之前。

建议 close fingerprint 包含：

```text
expected_claim_uid
```

避免相同 Idempotency-Key 被另一个 execution 重用。

---

## C11. Pi identity 不等于 execution identity

Pi 原生：

```text
ctx.sessionManager.getSessionId()
```

返回持久 session UUID。Pi session 内部本身支持 tree navigation；fork 则创建新 session。

因此不能：

```text
Principal.Subject = pi_session_id
```

否则两个同时 attach 到同一 Pi session 的进程可能被视为同一 holder。

建议：

```text
session_id
= Pi session UUID
= lineage / context identity

execution_id
= 每次 issue_claim 新生成的 UUIDv7
= execution tenure identity

Principal.Subject
= pi:<session_id>:<execution_id>
```

一个 execution ID 一旦 lease 丢失：

```text
禁止重新使用
```

重新 claim：

```text
必须生成新的 execution_id
```

---

## C12. Pi shutdown / crash 语义

Pi 有：

```text
session_shutdown:
quit
reload
new
resume
fork
```

而普通 tree navigation 不 teardown session runtime。

所以：

```text
session_shutdown
→ best-effort release

正常运行
→ automatic renew

kill -9 / crash / network loss
→ 不依赖 release
→ timed lease expiry 回收
```

确认插件不得使用永久 hard claim 作为默认 Agent claim。

---

# 二、建议最终 Worker 权限面

第一版 Pi Agent 暴露：

```text
READ
issue_list
issue_get
issue_graph

WRITE / COLLABORATIVE
issue_create
issue_comment
issue_link

EXECUTION
issue_claim
issue_renew
issue_release
issue_close
```

暂时**不向普通 worker 暴露**：

```text
edit title/body
owner assign/unassign
priority
label
arbitrary metadata
reopen
delete/purge
move
import
recurrence administration
force-release
```

Human / supervisor 可以拥有这些权限。

这样即使 standalone Kata 默认不 gate 所有 mutation，Agent 也不能绕过 forged policy 修改 execution-owned state。

---

# 三、Kata 改动按 Commit 拆分

所有 commit 必须独立：

```text
buildable
testable
reviewable
revertable
```

禁止一口气做完再拆历史。

---

## K1 — refactor: classify issue lease authority

目标：

只重构，不改变现有行为。

新增内部 authority abstraction，例如：

```go
type issueLeaseAuthority int

const (
    leaseAuthorityStandalone issueLeaseAuthority = iota
    leaseAuthorityHub
    leaseAuthoritySpoke
    leaseAuthorityDisabled
)
```

或等价实现。

集中处理：

```text
no FederationBinding → standalone
enabled hub          → hub
enabled spoke        → spoke
disabled/invalid     → existing refusal semantics
```

涉及：

```text
internal/daemon/handlers_claims.go
相关 helper tests
```

不要：

```text
改 DB schema
改 wire format
开放 standalone route
```

### 验收

现有全部 federation claim tests 不变并通过。

---

## K2 — feat: enable standalone issue lease actions

基于 K1 authority abstraction：

```text
acquire
renew
release
status
force-release
```

支持 standalone。

路由：

```text
standalone
→ DB.AcquireClaim / RenewClaim / ReleaseClaim / ClaimStatus

hub
→ existing local authoritative path

spoke
→ existing forward-to-hub path
```

涉及：

```text
internal/daemon/handlers_claims.go
internal/daemon/claims_auth.go（如需要）
internal/daemon/handlers_claims_test.go
API/e2e tests
```

不要新增表。

### 验收

至少覆盖：

```text
standalone hard acquire
same-principal idempotent acquire
different-principal conflict
status
release
force-release

hub unchanged
spoke unchanged
```

---

## K3 — feat: surface standalone leases in issue reads

当前 show hydration 只有 lease 被认为 federation-relevant 时才继续。

修改：

```text
showIssue
showIssueByUID
status/read projection
```

使 standalone live lease 能出现在现有：

```text
lease
lease_hub_now / equivalent local timestamp
```

字段。

不要发明第二套：

```text
standalone_claim
agent_claim
```

wire shape。

涉及：

```text
internal/daemon/handlers_issues.go
internal/daemon/handlers_claims.go
相关 show/status tests
```

### 验收

```text
Acquire standalone lease
↓
showIssue
↓
same ClaimUID / holder / expiry visible
```

---

## K4 — feat: sweep standalone timed leases

重构 `TimedClaimSweeper`：

```text
authoritative:
✓ standalone
✓ enabled hub

non-authoritative:
✗ spoke
```

不要直接全局 expire cached spoke lease。

可以：

```text
ListProjects
+
ListFederationBindings
↓
classify authoritative project IDs
↓
ExpireTimedClaimsForProject
```

或者实现同等安全的 storage abstraction。

涉及：

```text
internal/daemon/claims_sweeper.go
internal/daemon/claims_sweeper_test.go
claims_sweeper_idle_test.go
service worker tests
```

### 验收

```text
standalone timed claim expires
claim.expired emitted

hub timed claim expires
existing behavior preserved

spoke cached timed claim
not locally swept
```

---

## K5 — refactor: decouple close-time lease release from federation violation audit

目标：

建立一个明确的：

```text
release authoritative live lease on issue close
```

transaction helper。

要求：

```text
standalone → release
hub        → release
spoke      → no local authoritative release
```

事件顺序推荐：

```text
issue.closed
claim.released(reason=issue_closed)
```

不要在本 commit 泛化：

```text
claim.violated
federation ingest audit
```

涉及 SQLite + PostgreSQL close transaction：

```text
internal/db/sqlitestore/...
internal/db/pgstore/issue_lifecycle.go
相关 claim helper
dbtest conformance
```

### 验收

close 完成以后：

```text
ClaimStatus → no live claim
```

并且两个 backend 行为一致。

---

## K6 — feat: add exact lease precondition for close

新增可选：

```text
If-Lease-Match
```

或者经过 Codex核对后使用等价、更加符合 Kata 命名规范的 header。

修改 API request：

```text
CloseActionRequest
```

增加 lease precondition。

DB `CloseIssueParams` 增加 typed guard，而不是让 handler 自己查 SQL。

事务内部：

```text
lock issue
↓
validate revision
↓
validate exact live lease
↓
validate children
↓
close
↓
release lease
↓
events
↓
commit
```

需要区分错误：

```text
claim_required
claim_expired
claim_denied / claim_lost
```

如果 ClaimUID 已被另一个 tenure 取代，推荐明确返回：

```text
409 claim_lost
```

而不是模糊的 500/validation。

涉及：

```text
internal/api/types.go
internal/db/types.go
internal/db/storage.go

internal/db/sqlitestore/claims.go
internal/db/pgstore/claims_core.go
close lifecycle files

internal/daemon/handlers_actions.go

api/openapi.yaml
pkg/client/openapi.yaml
generated clients
tests
```

### 必测

```text
no lease + strict close → reject

other holder → reject

expired lease → reject

wrong ClaimUID → reject

correct holder + ClaimUID → success

success → lease released

stale old ClaimUID after new claim → reject
```

---

## K7 — fix: preserve close retry semantics with lease precondition

这一 commit 专门处理 idempotency，不和 K6 混在一起。

测试：

```text
Acquire C1
↓
Close with:
Idempotency-Key = X
If-Lease-Match = C1
↓
commit succeeds
lease released
↓
simulate lost response
↓
retry:
Idempotency-Key = X
If-Lease-Match = C1
↓
returns original receipt
```

另外：

```text
same key
different ClaimUID
→ fingerprint mismatch
```

涉及：

```text
handlers_actions.go
close idempotency fingerprint/helper
handlers_actions_retry_test.go
SQLite/Postgres close tests if necessary
```

---

## K8 — docs: generalize lease terminology

只在核心 correctness 稳定之后做。

当前很多地方仍称：

```text
federation write lease
```

整理成：

```text
issue execution/write lease

Standalone:
local authority

Federation:
hub authority
```

CLI 第一版可以暂时继续保留：

```text
kata federation lease
```

只要 forged 不依赖 CLI。

如果准备 upstream，再单独讨论增加：

```text
kata lease
```

或：

```text
kata issue lease
```

不要在 K2 顺手大改 CLI hierarchy。

涉及：

```text
docs/
CLI help
agent-output docs
OpenAPI descriptions
```

---

# 四、forged 按 Commit 拆分

Kata patch 跑通以后再开始。

## F1 — bootstrap embedded Kata service

实现：

```text
forged
└── embedded kata.Service
```

配置：

```text
EmbeddingProfileRestricted
SQLite default
host AccessController
host-owned HTTP server
```

Kata 负责：

```text
Issue
Comment
Link
Event
Lease
```

Forge 暂时不建自己的 Issue 表。

### 验收

```text
forged start
↓
Kata health
↓
EnsureProject
↓
create/show issue
```

---

## F2 — add principal and execution identity

定义：

```text
Actor
SessionID
ExecutionID
```

其中：

```text
Actor        = codex / claude / human
SessionID    = Pi session UUID
ExecutionID  = per claim UUIDv7
```

构造：

```text
Principal.Subject =
pi:<session_id>:<execution_id>
```

普通非 execution request 可以使用 session-scoped subject。

### 验收

同一 Pi session：

```text
execution E1
execution E2
```

必须被 Kata 看作不同 lease principal。

---

## F3 — enforce Agent operation policy

AccessController 实现 worker capability matrix。

允许：

```text
read/list/show/graph
create issue
create comment
create link
lease acquire/renew/release
```

禁止：

```text
owner
priority
arbitrary edit
delete
reopen
force-release
administration
```

Human/supervisor 另行授权。

### 验收

Agent 直接调用受限 Kata endpoint 得到拒绝，而不是只靠 Pi tool 隐藏。

---

## F4 — implement execution lifecycle facade

Forge API：

```text
issue_claim
issue_renew
issue_release
issue_close
```

`issue_claim`：

```text
generate execution_id
↓
call Kata Acquire
↓
return:
execution_id
claim_uid
expires_at
```

`issue_close` 必须自动附：

```text
If-Lease-Match: claim_uid
```

Agent 不自行拼 header。

---

## F5 — implement Pi extension

工具：

```text
issue_list
issue_get
issue_create
issue_comment
issue_link

issue_claim
issue_release
issue_close
```

插件内部而非 LLM负责：

```text
execution_id
claim_uid
heartbeat/renew
```

建议默认：

```text
timed lease
TTL: configurable

renew interval:
明显小于 TTL
```

例如初始默认可以：

```text
TTL 15m
renew every 3m
```

不要让 Agent 自己记得 heartbeat。

---

## F6 — handle Pi lifecycle

`session_start`：

```text
read Pi session UUID
initialize Forge client
```

`session_shutdown`：

```text
best-effort release current claim
stop renew loop
```

`session_tree`：

```text
do nothing
```

crash：

```text
no cleanup assumption
wait for lease TTL
```

不要在 resume 后偷偷复用旧 execution ID。

---

## F7 — add Issue timeline projection

第一版 timeline 合并：

```text
Issue created
Comment
Lease acquired
Comment
Link/dependency
Close result
Lease released
```

目标 UI：

```text
#123 Deploy staging

Human
工作计划……

Agent codex
claimed this issue

Agent codex
Deployment completed.
commit: abc123
health: pass

Issue closed
```

底层 authority 仍然：

```text
structured rows/events
```

timeline 只是 projection。

---

# 五、当前明确不要做的东西

第一阶段禁止顺手实现：

```text
Pull Request domain
Review
Change / SecretRef
Git hosting
CI server
release registry
chat
wiki
complex RBAC
agent orchestration scheduler
deep task tree
```

它们都建立在：

```text
Issue + Lease + Event + Evidence
```

稳定之后。

尤其不要现在把 Beads / Radicle / git-appraise 的模型一次性搬进来。

---

# 六、第一阶段 Done Definition

只有同时满足以下条件才进入 PR/Review 开发：

```text
Human creates Issue #123

Pi A claims #123
→ success

Pi B claims #123
→ conflict

Pi B comments #123
→ success

Pi B creates discovered child/dependency
→ success

Pi A works
→ automatic renew

Pi A closes with correct ClaimUID
→ success
→ issue.closed
→ claim.released

old Pi A request retries with stale claim
→ cannot mutate new tenure

Pi A crashes
→ TTL eventually releases authority

Pi B can then acquire new lease

Human can inspect Issue timeline
and understand:
who worked,
what happened,
what evidence exists,
why the Issue closed.
```

如果这一 happy path 还没有完整通过，不开始 PullRequest / Change。
