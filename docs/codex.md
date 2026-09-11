# Codex adapter

`forge` is the active CLI interface, Codex command hooks carry lifecycle and
context, `forged` owns ephemeral execution state/renewal, and embedded Kata owns
issues, leases, comments, links, events and typed evidence. No MCP or Codex fork.
The hook contract is verified against Codex CLI **0.154.0**.

## Setup

Build/start forged as in README and install/link the local CLI package. Configure
`FORGE_URL` and `FORGE_WORKER_TOKEN_FILE` with the worker credential path. Never
supply a supervisor credential to an Agent.

```sh
forge codex install --target "$PWD/.codex"
forge codex skill --target "$PWD/.agents/skills/forge-codex"
forge codex
```

The installer never overwrites existing files. If hooks already exist, use
`forge codex hooks` to inspect the generated configuration and merge it with the
existing bundle. Trust the project configuration layer, then review and trust
the installed hooks in Codex; installing them grants neither kind of trust. Hook commands use absolute Node/script paths, so
regenerate after moving the package or Node installation. The launcher also works
without hooks, but context, Stop checks and thread activity observation require
them. There is no claim that an untrusted/disabled bundle is active.

Use `forge codex --help-forge` for adapter help. Other arguments go to Codex,
including `forge codex resume THREAD` and `forge codex exec ...`.
`FORGE_CODEX_STOP_POLICY` accepts `strict` (default) or `advisory`.
`FORGE_TTL_SECONDS` is fixed for each launched instance, default 300, range
60–3600. Renewal runs at TTL/3 capped at 30 seconds. No CLI renewal subprocess.

For optional per-Issue, multi-repository worktrees, see [Codex checkout](codex-checkout.md).
Claim alone still works directly in the original directory.

## Agent workflow

```sh
forge issue list --status open
forge issue get REF
forge issue claim REF
forge issue comment REF --body 'Verified finding...'
forge issue close REF --data-file evidence.json
# Or relinquish work:
forge issue release REF --reason 'handoff'
```

Inside a launched Codex, the standard issue commands route to the daemon facade,
ignoring any inherited legacy `FORGE_SOCKET`. Explicit socket/session overrides
and `forge session ...` are refused. Each request uses the harness's
`CODEX_SESSION_ID` and `CODEX_THREAD_ID`, plus a fresh launcher instance. The
instance capability is held in a private 0600 temporary file; execution proofs
and claim attempt identities remain in forged, never model arguments/output.
The service checks credentials, registered process identity, thread ownership
and exact Kata tenure; hiding tools is not the authority boundary.

A launcher supervises the child instead of exec-replacing itself. Its same-UID
PID **and creation time** fence the instance, it forwards SIGINT/SIGTERM, and it
owns no renewal loop. This supports Node >=22.19 as well as Node 24. The launcher
removes parent Codex identities, legacy socket and supervisor-token path from
the child environment. Fork/resume through a fresh launcher never restores an
old execution. Each thread has its own current execution and pending request.

## Lifecycle and failure behavior

| Event | Behavior |
| --- | --- |
| SessionStart / SubagentStart | Register thread and inject short protocol |
| SessionStart compact / PostCompact | Refresh context; keep current pending request and tenure |
| Pre/PostToolUse | Liveness only, no shell-command business classifier |
| UserPromptSubmit | Touch and resume paused renewal if the lease is still live |
| Interrupt | Pause renewal; no force release |
| Stop / SubagentStop | Check exact lease and pending outcome; bounded continuation |
| SessionEnd | Retire just that session immediately; queued bounded best-effort release |
| Normal launcher exit | Retire entire instance and queue best-effort release |
| Launcher/child crash | Stop renewal; Kata TTL recovers, no force release |
| forged restart | Reject all old instance capabilities; new launcher and new tenure required |

Subagent hook attribution uses `agent_id` even on tool/prompt/compact events.
`SubagentStop` is not treated as an unconditional process shutdown: other hooks
may continue the child. An allowed stop with unresolved work pauses renewal;
TTL recovers its lease. A missed child stop is bounded by a 30-minute per-thread
inactivity ceiling plus the remaining lease TTL. Very long silent tool calls
must tolerate loss of execution authority; PID liveness alone cannot prove a
particular subagent remains active.

Strict Stop requests at most one continuation, asking the Agent to close,
release or resolve its pending request. Already-continued turns, advisory mode,
explicit pauses and recognizable requests for user input pause renewal and emit
a warning instead of looping. Use `forge codex pause` before awaiting input;
`forge codex unpause` resumes observation, not an expired execution. Check
`forge codex status` and claim again if needed. Message classification is only
UX and never grants server authority.

## Retry and restart

Every CLI logical command snapshots its parameters and request ID before its
first RPC. Automatic transport retries reuse that ID; pending acquire uses the
same private attempt, and pending close keeps its key and proof even after lease
expiry. A failed comment/read cannot erase execution retry state.

`forge codex retry` resolves a pending execution request or returns the last
confirmed execution result. Replayed results explicitly carry
`client_replayed` / `current_state_not_refreshed`; inspect the current issue
before claiming it is still closed or still held. Historical replay never
restores execution. Same ID with changed parameters is rejected. Unknown
non-idempotent collaborative writes are not automatically reexecuted.

Runtime responses are ephemeral: 32 recent bodies / 16 MiB per thread, with up to
4096 request-ID tombstones. Evicted results fail explicitly without reexecution.
The daemon currently bounds registrations to 256 instances and 4096 threads;
restart after exhausting these limits. Restart deliberately invalidates all
runtime authority, leaving Kata timed leases/receipts intact.

## Verification and boundaries

```sh
npm test
npm run typecheck
go test -race ./internal/forge ./cmd/forged
npm run test:codex-e2e
# Opt-in: consumes a real Codex model/tool turn using an ephemeral fixture.
FORGE_REAL_CODEX=1 npm run test:codex-live
```

The real-model smoke test uses an empty temporary workspace and a separate
temporary CODEX_HOME, symlinking only the existing auth.json for login. Its
project trust and reviewed hook-trust bypass are confined to that fixture; it
does not change user hook trust or global configuration. It records event names/success flags, not transcripts
or credentials. Process tests cover real daemon renewal, normal close, crash,
TTL reclaim and daemon restart. Subagent/compaction/Stop edge cases also have
protocol fixtures and server race tests; they are not all separate real LLM
multi-agent acceptance runs.

This remains supervised trusted-local use, not a same-UID sandbox or a proof
that submitted evidence is true. Lease correctness does not cancel already
running filesystem tools. There is no PR domain, scheduler or new Issue store.
See the six package records in `docs/codex-evidence/`.
