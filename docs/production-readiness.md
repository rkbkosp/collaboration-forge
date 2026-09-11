# 第一阶段验收与部署边界

## 结论

2026-09-11：K1–K8、F1–F7 已落地并通过下列验收，可用于**受监督、受信任的本机试运行**。不宣称任意生产环境就绪；特别是不构成恶意 Agent 的 OS/文件系统沙箱。

Issue、Comment、Link、Event、Lease 和 close receipt 均属于 Kata。没有新增 claim schema、Issue 数据库或数据库迁移。Owner/Assignee 与临时 execution lease 仍为不同概念。

## 可复现基线

- 补丁来源上游：`kenn-io/kata@b1667be5603a308f8c2d44a0d6cbac2463a7eeb2`。
- 工作区 submodule：`rkbkosp/kata@b67a69678c69b00594dde45c2ffd4c2539a35b6b`，保持干净且承载已验证补丁树。
- 补丁后 tree：`7f4c5cc52f32e29dee68cb702330910d99ef7c1c`；由 `patches/kata/` 的九个独立补丁重建。
- 验收环境：Darwin arm64、Go 1.27.1、Node 24.19.0、npm 11.17.0、Bun 1.4.0、Pi SDK 0.85.1、Docker 29.4.0。
- Forge 提交层次：F1 `dc9fc24`；F2 `04b617d`；F3 `91148a9`；F4 `6cce372`；F5 `f87af89`；F6 `0470580`；F7 `0452183`。
- 实测修正：`e3f5501` 接受真实 Pi 的 UUIDv7 session；`bbd8698` 在 claim purpose/event 中保留 session 归属，关联 opaque execution 与 comment/close actor。

## 验收矩阵

| 命令 / 场景 | 结果与范围 |
| --- | --- |
| `go test ./... -count=1` | root CLI、服务、鉴权、exact execution、并发 close、持久 key/receipt、timeline 全部通过 |
| `go test -race ./... -count=1` | root 全包通过，无 race 报告 |
| `go build ./...`、`go vet ./...` | root 通过 |
| `npm test` | 24 tests 通过，无 skip；fake-clock 边界、真实 fetch、schema、adapter lifecycle |
| `npm run typecheck` | production extension 与 E2E tests 均通过 |
| `npm run test:e2e` | 7 tests 通过，无 skip；见下面精确范围 |
| `scripts/test-bootstrap-kata.sh` | fresh 重建 tree、重复执行、拒绝覆盖脏 checkout 均通过 |
| `scripts/test-kata.sh` | Kata **完整 `go test -v ./... -p 4 -timeout 15m` 通过**；真实 PostgreSQL 全包约 152 秒，PG package 无 skip；SQLite、daemon、Service、CLI、client、TUI、e2e 均通过 |
| `npm audit --omit=dev` | 当次 registry audit 为 0 vulnerabilities；不是独立安全审计 |

Kata full suite 含上游有意的 optional-capability transcript skips，以及未提供独立 release binary 时的 `TestReleaseBinaryContainsValidatedWebUI` skip。不能把“全命令通过”说成所有可选测试都执行。PostgreSQL 两个显式服务（pgvector 和 plain PG17）故障会 fail，而非静默 skip。

先前的全包失败不是被隐藏：长 macOS `TMPDIR` 引发 AF_UNIX 路径失败；未安装 `kata/web` 依赖影响包含 release/git-describe 构建的 e2e；逐测试创建 PG 容器超过旧超时。新脚本使用 `/tmp`、frozen Bun install 和独立临时 PostgreSQL 服务，重新运行完整命令取得通过，未为此更改 Kata 源码。脚本只清理自己创建的容器。

### 真实 forged + execution controller

`tests/forge-e2e.test.ts` 启动真实 CLI 子进程、真实 HTTP 和 SQLite：

1. Human 创建 issue；A acquire；同一 Pi session ID 的新 runtime B 获得 409，不能编辑。
2. B 不持 lease 仍可 comment、create、link dependency；A 的 guard 确认执行权后实际运行 `go test ./...`。
3. 真实等待 TTL/3 自动 heartbeat，确认服务端 expires_at 延长，不用假时钟替代。
4. close 在服务端提交后注入响应丢失；get 已显示 closed/released，客户端仍保留原 snapshot。
5. 重启真正的 forged 进程，使用原数据目录/签名 key/receipt；Human reopen、B 获得新 tenure 后，A 用原 body、Idempotency-Key 和 proof 重试，只恢复旧 receipt，不关闭 reopened issue，也不释放 B。
6. 给旧 proof 换一个新 key 会被拒绝；只有一条 close event。CLI 用小页读取 timeline，显示贡献、执行、关系和实际 test evidence，不输出 token。
7. Supervisor force-release 后，B 下一次 editing preflight 停止编辑。
8. 另一个 controller 子进程 acquire 后被 SIGKILL，没有 shutdown cleanup；新 runtime 在真实约 60 秒 TTL 前 conflict，之后获得新 ClaimUID，并存在对应 expiry event。

SIGKILL 对象是运行生产 execution controller 的 Node 子进程，不冒充完整 Pi TUI 进程。

### 真实 Pi SDK

`tests/pi-runtime.test.ts` 不伪造 ExtensionAPI：真正的 loader、SessionManager、AgentSession、ExtensionRunner 加载生产 extension，全部十个 registered AgentTools 走真实 forged/Kata。验证：

- Pi 0.85.1 实际产生 UUIDv7；v4 旧 session 也兼容。
- schema 与服务端 graph depth/status/ref 契约一致。
- 无 lease 的 runner preflight 阻止编辑；持有 exact lease 后通过。
- `navigateTree` 不释放；真实 `reload()` 释放并创建新 factory，旧 history 存在也不能恢复 execution。
- 新 acquire 使用不同 ClaimUID/holder；显式 shutdown event 释放。
- 工具输出和附加 system prompt 不含 worker token / private execution 字段。

**未测试** LLM/provider 回合、TUI/RPC 自动调度或 Pi OS 信号链。测试直接调用真实 AgentTool；preflight 与 quit 通过真实 runner 显式派发。禁止将此描述为完整模型自主执行验收。

## 运行与安全边界

- forged 仅接受 loopback IP literal；除最小 health 外均认证。Worker token 访问 raw Kata/supervisor mutation 即使伪造 actor/role headers 也被服务端拒绝。
- 同 UID 的任意 shell/read 工具仍可能读取 0600 文件。插件不输出凭据，不等于 OS 沙箱：不可信 worker 必须无法读取 supervisor token、签名 key、DB；需要额外的账户/沙箱/凭据代理隔离。默认同账户示例只适合可信本机协作。
- Pi editing preflight 是协作保护，不会撤销已在运行的 shell，也不限制文件路径；自定义 mutation 工具、扩展、自定义 shell 或绕过 Pi 的写入不在此保护内。检查到失去 lease 之前可能存在 check/use 时间窗口。
- Worker session UUID 是客户端提供的关联信息，actor 是 session 伪名，不是经过独立认证的人名或进程证明。共享 worker token 不提供每个 Agent 的独立身份认证；nonce/proof 负责隔离 execution tenure。
- heartbeat 在错误/未知状态下停止编辑；crash 或 release 失败等待 TTL。不会从 session history 恢复 pending close/attempt。丢失客户端内存后由 Human 检查 issue/timeline，再开新 tenure。
- close 的结构和 exact live lease/事务检查由服务端负责；evidence 是提交者陈述，服务端不重新运行测试，也不证明某个 commit/PR 的内容正确。
- 数据放本机可信文件系统，依赖 `flock`；不支持共享 NFS、多进程写同一目录、HA 或远程暴露。备份前停 forged，整个目录一起保存（含 execution-key），不要删除锁文件绕过排他。
- Forge 当前部署后端是 SQLite。Kata PostgreSQL storage 全包已通过，不等于 Forge 已实现 PostgreSQL 部署选项。Federation authority 回归通过，不等于已进行真实跨主机故障演练。
- 尚未完成长时间 soak/load、故障磁盘/断电恢复、独立安全审计、跨平台发行测试及无人值守生产运维验收。

## 本次原始日志

本机临时证据（不保证 `/tmp` 永久保留；以上测试代码和命令在仓库内可重跑）：

- `/tmp/forge-pi-uuid-red.log`、`/tmp/forge-attribution-red.log`：两个实测缺口的 red。
- `/tmp/forge-final-e2e.log`：真实 E2E 最终结果。
- `/tmp/forge-kata-full-explicit.log`：Kata 完整 suite。
- `/tmp/forge-final-bootstrap.log`：补丁重建。
- `/tmp/forge-npm-audit.json`：当次依赖审计。
