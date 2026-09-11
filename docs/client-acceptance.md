# 独立 client CLI 与 client Skill 验收

## 交付范围

`forge` 是独立 Node CLI，不要求运行 Pi；`forged` 仍是服务端。客户端覆盖 F1–F7 的全部 Issue/Lease worker 操作：list/get/graph/create/comment/link/claim/renew/release/close，以及 timeline、project discovery、health。

- `session start/status/guard/context/retry/stop/wait`：独立 execution runtime，自动 heartbeat，私有 proof/nonce/pending close 只在内存中。
- Human：create/list/get/comment/link/update（owner、priority、标题、正文）、label add/remove、unlink、reopen、close、delete、force-release；另有受服务端 restricted profile 限制的原生 `admin request`，支持确认串、If-Match、Idempotency-Key。
- 输入：命名 flags、JSON object、文件或单一 stdin，typed evidence；JSON 输出及明确退出码。
- 分发：npm `forge` bin、`npm run forge -- ...`、直接 `node scripts/forge.mjs ...`。
- Skill：`skills/collab-forge-client/SKILL.md` 与自包含 reference；`forge skill install --target ...` 不覆盖已有安装。

这是 Issue/Lease 协作客户端；不是 OS shell 沙箱、Git/PR 管理器或另一套 Issue 数据库。并行开发的 workspace/Codex 原生生命周期接口不属于这份 F1–F7 客户端契约，不能据此声称它们都已由此 Skill 覆盖。

## 关键保证

1. Worker/admin 凭据分离；admin 不回退到 worker token。只有 socket 的客户端不必读取凭据文件；包括 health 在内都使用 broker 固定的目标，不能用命令 flags 偷换目标。
2. Socket owned 0600，父目录 owned 0700，拒绝 symlink、已有路径和过长路径。私有 bind + 原子 hard-link 发布避免 Node 关闭监听器时删除被替换的公共 socket。没有匿名 TCP broker。
3. 服务端保持权限和 exact lease 的最终校验；guard 不取消已经运行的 shell，也不约束绕过 CLI 的文件写入。同 UID 仍是可信协作边界，不是安全沙箱。
4. HTTP 不跟随 redirect，且使用私有 direct dispatcher 绕过 Node 环境代理，防止 loopback 凭据被 `HTTP_PROXY` / `NODE_USE_ENV_PROXY` 转发。该修复同时适用于现有 Pi Controller。
5. 不确定 HTTP acquire/close 复用完整原始请求。并发过时 retry 不能作用于新 tenure。broker 已收到确认但 CLI socket 响应丢失时，仅恢复有界、脱敏的历史确认，标记 `client_replayed` / `current_state_not_refreshed`；之后必须 get/guard 确认当前状态。
6. 不从磁盘/历史恢复 execution。退出 best-effort release，崩溃靠 TTL。Human/raw mutation 不自动重试；明确 4xx 即使响应体损坏/过大也不误报为未知 mutation。

## 实测证据（2026-09-11，Darwin arm64 / Node 24.19 / Go 1.27.1）

- `npm test`：**62/62**，无 skip。
- `npm run typecheck`：通过。
- `npm run test:e2e`：**9/9**，无 skip，包括原有真实 Pi SDK、自动 heartbeat、SIGKILL/实际 TTL 验收。
- 新增真实 CLI 子进程验收：双 broker 冲突、非 holder 贡献、Human 全组操作、guard/force-release、typed close、timeline 小页完整扫描、worker token 不可调用 admin、socket-only health。
- 新增真实 CLI 重试验收：服务端提交后丢 HTTP 响应、真正重启 forged、reopen/new tenure，再用 `session retry` 恢复旧 receipt；body/key/proof 不变，只有一次 close event，新 lease 不受影响。
- 独立安全复核发现并修复：Node 环境代理外发风险、损坏/过大的 4xx body 错误分类。均有回归测试。
- `npm pack` 后安装到临时 prefix，`--omit=dev --ignore-scripts`，实际运行安装后的 `forge --version` 和 Skill 安装，均通过；tsx 与兼容 Node 内置 fetch 的 undici 7.29.1 是显式 runtime dependencies。
- root `go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...` 通过。

最终全套验收使用隔离的已提交基线 + 本次 CLI 文件快照，并从固定 tree 重建 Kata，避免共享 checkout 中并行 Codex TDD 改动影响。共享目录测试曾被其他线程未完成的 Go 测试阻断；不将其伪装成共享目录全绿。本次不重复宣称新的 Kata 全包验收，已有基线证据见 [production-readiness.md](production-readiness.md)。

本机日志：`/tmp/forge-client-isolated-unit.log`、`/tmp/forge-client-isolated-e2e.log`、`/tmp/forge-client-isolated-bootstrap.log`、`/tmp/forge-client-install.log`；red 包括 `/tmp/forge-cli-red.log`、`/tmp/forge-client-proxy-red.log`。临时日志可能清理，仓库测试可重跑。

安装与实际工作流程见 [Skill](../skills/collab-forge-client/SKILL.md)；结论仍限受监督、可信本机使用。
