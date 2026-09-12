# Harness adapter contract

Forge supports multiple coding-agent harnesses through thin adapters. This
document is the normative contract for every adapter. Model-facing guides remain
separate: [Codex](codex.md), [Claude](claude.md), and the Pi extension README.

## 1. Scope

The supported launch surface is:

```sh
forge pi [Pi arguments...]
forge codex [Codex arguments...]
forge claude [Claude Code arguments...]
```

Harness adapters MUST NOT define a second work/execution protocol.

The canonical work protocol is the existing Forge Worker API and
execution-credential implementation in this repository:

```text
docs/worker-api.md
internal/forge/execution.go

pi-extension/
client/pi.ts

codex/
internal/forge/codex_*.go
docs/codex.md

client/main.ts
scripts/forge.mjs
```

Kata remains authoritative for:

```text
Issue
Comment
Link
Lease / Claim
Event
typed completion evidence
```

`forged` remains authoritative for:

```text
worker capability enforcement
execution proof issuance/verification
harness ephemeral coordination
strict-close facade
retry/receipt handling
```

Harness adapters are responsible only for:

```text
launch
harness identity attribution
lifecycle translation
model-facing CLI/tool UX
liveness signals
graceful cleanup
```

## 2. Protocol invariants

All adapters MUST preserve the existing Worker API semantics. They MUST NOT
introduce harness-specific versions of:

```text
Issue
execution ID
claim protocol
close protocol
evidence protocol
retry protocol
```

The existing execution model remains:

```text
logical acquire
    ↓
private attempt_id
    ↓
forged derives execution subject
    ↓
Kata grants exact ClaimUID
    ↓
forged returns private execution proof
    ↓
renew / release / close use that proof
```

The execution proof remains model-invisible. It binds the existing fields
implemented by `internal/forge/execution.go`:

```text
Subject
Session
IssueUID
ClaimUID
```

A valid execution credential is not itself lease authority. Kata's current live
exact ClaimUID remains the final authority.

## 3. Worker API contract

Adapters consume the existing operations:

```text
issue_list
issue_get
issue_timeline
issue_graph

issue_create
issue_comment
issue_link

issue_claim
issue_renew
issue_release
issue_close
```

The following remain collaborative and do not require an execution lease:

```text
read
create Issue
comment
add relationship
```

The following remain execution-scoped:

```text
claim
renew
release
close
```

Workers MUST NOT gain access through adapter-specific shortcuts to:

```text
assignment
priority
arbitrary Issue edit
metadata administration
reopen
delete
force-release
project administration
```

These remain outside the ordinary worker capability surface.

## 4. Common execution rules

### 4.1 New logical claim

Every genuinely new logical acquire uses a fresh private `attempt_id`. An
ambiguous acquire retry MUST reuse the same attempt. Therefore:

```text
HTTP retry
≠
new execution attempt
```

but:

```text
lease definitely lost/released
+
new acquire
=
new attempt
```

Adapters MUST NOT reconstruct old attempts from model/session history.

### 4.2 Execution credentials

The model MUST never receive:

```text
attempt_id
execution_token
execution proof
private Idempotency-Key
```

These values MUST NOT be emitted into:

```text
model tool results
session transcript
prompt context
normal logs
Issue comments
```

Sanitization follows the existing Pi/Codex behavior.

### 4.3 Automatic renewal

The LLM is never responsible for heartbeat. The normal renewal interval is the
existing policy:

```text
min(TTL / 3, 30 seconds)
```

A local wall clock is not lease authority. Renew responses use `server_now` and
lease expiry to derive a conservative local deadline. A late response cannot
revive a locally expired tenure.

### 4.4 Strict close

`issue_close` always goes through the existing Forge facade. The adapter does
NOT directly send its own:

```text
If-Lease-Match
retry_protocol
actor
execution principal
```

`forged` derives close-v2 authority from the private execution proof. A logical
close owns a stable private Idempotency-Key. On ambiguous outcome:

```text
same body
same execution proof
same Idempotency-Key
```

must be retried. Receipt recovery happens before current-lease validation, so a
successful close whose HTTP response was lost remains recoverable after its
lease was automatically released.

### 4.5 Abnormal termination

Correctness MUST NOT depend on a shutdown hook running. Normal termination:

```text
best-effort release
```

Abnormal termination:

```text
stop renewal
↓
Kata timed lease expires
```

Adapters MUST NOT force-release a lease merely because a process appears dead.

## 5. Harness identity model

Harness identity is attribution and runtime routing, not the execution
credential itself. Each adapter maps its native concepts into:

```text
instance
session
actor/thread
```

The concrete mapping differs by harness. Execution identity remains
server-derived from:

```text
runtime attribution
+
fresh logical acquire attempt
+
canonical Issue identity
```

Adapters MUST NOT parse or manufacture execution subjects.

## 6. Pi adapter

### 6.1 Status

Pi support is the reference implementation for a first-class extension
integration. Primary implementation:

```text
client/pi.ts
pi-extension/forge.ts
pi-extension/controller.ts
pi-extension/schemas.ts
pi-extension/README.md
```

Launch:

```sh
forge pi [Pi arguments...]
```

`forge pi` MUST continue to:

1. launch the real Pi binary;
2. inject the repository Forge extension unless already supplied;
3. preserve host-selected `FORGE_URL` and worker credential;
4. remove Codex/legacy execution environment;
5. avoid creating a second CLI session broker.

### 6.2 Identity

The canonical Pi session identity is `ctx.sessionManager.getSessionId()`. It is
sent as `X-Forge-Session`. The extension controller's random runtime ID is
diagnostic only. Concurrent extension runtimes using the same Pi session UUID
remain isolated because every new logical acquire uses its own private attempt.

### 6.3 Model surface

Pi exposes the existing Worker API as typed first-class tools. The current tool
set remains canonical:

```text
issue_list
issue_get
issue_graph
issue_create
issue_comment
issue_link
issue_claim
issue_renew
issue_release
issue_close

plus checkout tools
```

Tool schemas must continue rejecting authority/protocol fields supplied by the
model.

### 6.4 Lifecycle

```text
session_start
→ create fresh Controller for the real Pi session

session_shutdown
→ invalidate controller immediately
→ stop heartbeat
→ bounded best-effort release

session_tree
→ no execution lifecycle change

before_agent_start
→ refresh and inject current Forge context

tool_call(edit/write/bash/apply_patch)
→ cooperative exact-tenure guard
```

New/resume/fork/reload MUST NOT restore old execution state from Pi history.
Crash recovery remains TTL-based.

## 7. Codex adapter

### 7.1 Status

Codex support is the reference implementation for a supervised CLI harness.
Primary implementation:

```text
codex/launcher.ts
codex/facade.ts
codex/hooks.ts
codex/cli.ts

internal/forge/codex_identity.go
internal/forge/codex_runtime.go
internal/forge/codex_events.go

docs/codex.md
```

Launch:

```sh
forge codex [Codex arguments...]
```

### 7.2 Supervised launcher

`forge codex` creates a fresh `instance_id` and private 0600 instance
capability, and registers instance ID, launcher PID, process creation time and
TTL with forged. The launcher remains alive while Codex runs. It forwards
SIGINT/SIGTERM, reports normal child termination, reports abnormal termination,
and owns no renew loop. The daemon owns renewal.

### 7.3 Identity

```text
instance_id = FORGE_CODEX_INSTANCE_ID
session_id  = CODEX_SESSION_ID
thread_id   = CODEX_THREAD_ID
```

represented by the existing `codexIdentity{Instance, Session, Thread}`. The
tuple identifies one harness runtime/thread but is not an execution credential.

### 7.4 CLI routing

Inside `forge codex`, normal commands `forge issue ...` and `forge checkout ...`
auto-route through `codexTool()` when `FORGE_CODEX_INSTANCE_ID` is present.
Harness-owned identity cannot be overridden with `--socket` or `--session-id`.
The model must never need to call `forge session start`.

### 7.5 Lifecycle hooks

```text
SessionStart / SubagentStart
→ start
→ register/touch thread
→ inject short Forge protocol + state

PreCompact
→ touch

PostCompact
→ context refresh

PreToolUse / PostToolUse
→ liveness only

UserPromptSubmit
→ prompt
→ resume paused renewal if tenure is still live

Interrupt
→ pause renewal

Stop / SubagentStop
→ exact state refresh
→ strict/advisory Stop policy

SessionEnd
→ retire that session

normal launcher end
→ retire instance
→ best-effort release

crash
→ stop renew
→ TTL recovery
```

Shell-command business classification is not a correctness boundary.

## 8. Claude Code adapter

Claude Code uses the Codex-style supervised architecture. It does not use a
second in-process Pi-style Controller as execution authority, because the main
user-facing interface is the shared `forge` CLI and execution state must
therefore be accessible across Bash invocations.

Launch:

```sh
forge claude [Claude Code arguments...]
```

## 9. Claude plugin layout

The repository owns a Claude plugin:

```text
claude/
├── .claude-plugin/
│   └── plugin.json
├── hooks/
│   └── hooks.json
├── skills/
│   └── forge/
│       └── SKILL.md
├── hook.ts
├── facade.ts
├── launcher.ts
└── cli.ts
```

Do not add MCP.

Claude Code officially supports local plugins through
`claude --plugin-dir /path/to/plugin`, so `forge claude` automatically injects
the bundled plugin directory when launching Claude Code. No separate plugin
install step is needed for the normal `forge claude` development/local workflow.
If the exact bundled Forge plugin is already supplied in the arguments, it is not
added twice. Other user `--plugin-dir` arguments remain untouched.

## 10. `forge claude` launcher

On startup:

```text
generate fresh instance_id
generate private instance capability
create owned 0600 temporary capability file
register instance with forged
launch Claude Code with bundled Forge plugin
```

Environment names are Claude-specific:

```text
FORGE_CLAUDE_INSTANCE_ID
FORGE_CLAUDE_TOKEN_FILE
```

Optional binary override:

```text
FORGE_CLAUDE_BINARY
```

The launcher removes inherited conflicting authority/runtime state:

```text
FORGE_SOCKET
FORGE_EXECUTION_SOCKET
FORGE_ADMIN_TOKEN_FILE

FORGE_CODEX_*
stale FORGE_CLAUDE_*
```

except the fresh values it owns. It must not expose supervisor credentials. The
launcher stays alive, forwards termination signals, and reports whether the
Claude child ended normally.

```text
launcher owns process supervision
forged owns execution + renewal
Kata owns the lease
```

## 11. Claude identity

Claude does not need a new execution identity model. Claude-native identity maps
into a harness attribution tuple:

```text
instance_id
session_id
agent_id
```

Server-side conceptual shape:

```go
claudeIdentity {
    Instance string
    Session  string
    Agent    string
}
```

Main agent `agent_id = "main"`. Subagent `agent_id = hook input.agent_id`.

`agent_type` is diagnostic metadata only and MUST NOT be execution authority.

## 12. Claude session ID

The Claude hook payload's `session_id` is the canonical session identity. Do not
rely on an undocumented shell environment variable for Claude session identity.

On `SessionStart`, write the canonical value into `CLAUDE_ENV_FILE`:

```sh
export FORGE_CLAUDE_SESSION_ID='...'
export FORGE_CLAUDE_AGENT_ID='main'
```

`FORGE_CLAUDE_INSTANCE_ID` already comes from the launcher. This makes ordinary
main-agent Bash calls see instance, session and main agent identity without
model-visible protocol state.

## 13. Claude subagent identity

Subagents share the Claude session but receive a distinct hook `agent_id`. A
subagent `agent_id` is NOT written globally to `CLAUDE_ENV_FILE`, because
several subagents can coexist.

Instead, `PreToolUse` for shell execution binds the current hook `agent_id` to
that individual shell invocation:

```text
PreToolUse(Bash)
    ↓
agent_id from hook
    ↓
execute command with:
FORGE_CLAUDE_AGENT_ID=<agent_id>
```

For main-agent calls with no subagent ID:

```text
FORGE_CLAUDE_AGENT_ID=main
```

The adapter injects/overwrites this value rather than trusting a model-provided
value. This identity is still cooperative attribution, not a security boundary.
The private execution proof remains the actual execution credential.

## 14. Claude facade

The Claude facade is analogous to `codex/facade.ts`. It constructs requests
containing:

```text
instance_id
session_id
agent_id
operation
params
request_id
```

Transport ambiguity rules remain identical to Codex:

```text
same logical request
→ same request ID
→ bounded transport retry
```

The facade must not implement acquire/retry semantics itself. Private attempts,
execution proof and close Idempotency-Key remain managed inside forged's
supervised runtime.

## 15. Claude CLI routing

`client/main.ts` routes:

```text
FORGE_CODEX_INSTANCE_ID present
→ issue/checkout operations use codexTool()

FORGE_CLAUDE_INSTANCE_ID present
→ issue/checkout operations use claudeTool()
```

Inside a Forge-launched Claude session:

```sh
forge issue list
forge issue get abc4
forge issue claim abc4
forge issue comment abc4 --body '...'
forge checkout abc4 --dirty
forge issue close abc4 ...
```

work directly. The Agent must NOT run `forge session start`. Harness-owned
identity cannot be replaced with `--session-id` or `--socket`.

## 16. Claude execution runtime

`codexRuntime` is not copied wholesale. Reusable implementation is extracted
from the existing Codex runtime while preserving all existing Codex wire
behavior. Shared supervised runtime responsibilities:

```text
instance registration
instance capability validation
PID + creation-time liveness fence
actor runtime registry
active tenure
pending acquire/close
receipt cache/tombstones
automatic renewal
pause/resume
normal-end release
abnormal-end TTL recovery
worker API dispatch
response redaction
```

Harness-specific layers provide only:

```text
identity parsing
event translation
wire namespace
diagnostic labels
```

Codex continues to expose its existing private endpoint contract unchanged.
Claude exposes a sibling private namespace:

```text
/forge/v1/claude/register
/forge/v1/claude/end
/forge/v1/claude/event
/forge/v1/claude/tool
```

This is an adapter-control transport into the existing Worker API, not a new
Issue/execution protocol. The following are not modified as part of Claude
adaptation:

```text
/forge/v1/tools/*
execution proof format
Worker API input schemas
strict-close semantics
Kata lease semantics
```

## 17. Claude lifecycle translation

### SessionStart

Input `session_id`, `source`. Actions:

```text
bind canonical session
write FORGE_CLAUDE_SESSION_ID to CLAUDE_ENV_FILE
register/touch main agent runtime
refresh current Forge state
inject short Forge protocol as additionalContext
```

For compact/reload-like context refresh, do not restore or replace the existing
execution merely because context changed.

### SubagentStart

Input `session_id`, `agent_id`, `agent_type`. Actions:

```text
register/touch subagent runtime
inject same short Forge protocol
inject current state relevant to that agent
```

No automatic claim.

### UserPromptSubmit

```text
touch runtime
resume paused observation/renewal
refresh current exact state
```

It must not revive an expired/lost lease.

### PreToolUse

Always touch the current agent runtime. For Bash, bind the current `agent_id` to
this shell invocation. For direct filesystem-mutating Claude tools such as
Edit/Write, an optional cooperative guard refreshes exact current tenure and
blocks when adapter policy requires a lease.

Do not build a complicated shell-command classifier. Bash remains cooperative
because Forge CLI itself must be callable through Bash and filesystem fencing is
not the server correctness boundary.

### PostToolUse / PostToolUseFailure

Liveness observation only. They must not grant authority or infer successful work
completion.

### PreCompact / PostCompact

Used for `touch`, state refresh and context refresh where supported. Compaction
MUST NOT:

```text
create a new execution
restore an old execution
release a live execution
change ClaimUID
```

### Stop

Before the main Agent terminates, refresh current exact lease/pending state.

Strict policy:

```text
active execution or unresolved pending mutation
→ block once
→ tell Agent to close, release, or resolve retry
```

Advisory policy:

```text
warn
→ allow stop
→ pause renewal as appropriate
```

The existing Codex stop semantics are reused rather than creating
Claude-specific work-state rules.

### SubagentStop

The same execution check, scoped to `session_id + agent_id`. A subagent that
still holds work must not accidentally be treated as the main agent. Not every
`SubagentStop` is a global Claude session shutdown.

### SessionEnd

Immediately mark that Claude session ended for the current instance. Queue
bounded best-effort release for its active executions. The hook performs only a
fast local RPC. If SessionEnd is missed:

```text
launcher/process liveness
→ stop renew
→ TTL
```

remains the recovery path.

## 18. Claude process exit

Normal launcher child exit:

```text
retire instance
stop renew
best-effort release all active tenures
```

Abnormal exit/crash:

```text
retire instance
stop renew
DO NOT force-release
Kata TTL recovers
```

A restarted `forge claude` always gets a fresh `instance_id`. It MUST NOT restore
old executions merely because Claude resumes an old session.

## 19. Claude automatic renewal

Claude plugin background monitors are not lease authority. Renewal belongs in
forged, same as Codex. The Claude plugin/hook layer only supplies liveness
observations. Therefore:

```text
Claude hooks unavailable temporarily
≠ immediate lease loss

forged instance alive + runtime active
→ ordinary daemon renewal policy

normal pause/stop signal
→ daemon adjusts renewal state

process death
→ daemon stops renewal

TTL
→ final recovery
```

Lease correctness stays independent of Claude's plugin monitor subsystem.

## 20. Claude Stop policy

The same policy surface as Codex:

```text
FORGE_CLAUDE_STOP_POLICY=strict
FORGE_CLAUDE_STOP_POLICY=advisory
```

Default `strict`. Commands:

```sh
forge claude status
forge claude retry
forge claude pause
forge claude unpause
forge claude --help-forge
```

Semantics match the existing Codex commands as closely as possible. `retry`
resolves the current private ambiguous acquire/close. It does not create a new
logical execution.

## 21. Claude plugin Skill

Ship a short Skill explaining only the model-facing workflow:

```text
Forge is the authoritative work ledger.

Before substantial work:
1. inspect the relevant Issue;
2. claim it before executing;
3. record durable discoveries as comments/issues/links;
4. use checkout when isolation is needed;
5. close only with truthful typed evidence;
6. release when handing off.
```

Do not expose:

```text
attempt IDs
execution tokens
ClaimUID plumbing
close-v2 headers
daemon adapter endpoints
```

The Skill points the Agent to `forge --help` for exact CLI syntax.

## 22. Checkout

The existing Forge checkout domain remains harness-independent. All three
harnesses use:

```sh
forge checkout ISSUE --dirty
forge checkout ISSUE --ref COMMIT
forge checkout status WORKSPACE_ID
forge checkout list
forge checkout archive WORKSPACE_ID
```

Claude WorktreeCreate/WorktreeRemove events are not a second checkout authority.
`Forge checkout record` remains the canonical Forge workspace artifact.

## 23. Claude Tasks / Agent Teams

Claude Code Task/Agent-Team state is not Forge work authority:

```text
Claude task
= ephemeral harness-local orchestration

Forge Issue
= durable cross-session work authority
```

Every Claude Task is not automatically mirrored into an Issue. A model explicitly
promotes durable discovered work using:

```sh
forge issue create ...
forge issue link ...
```

TaskCreated/TaskCompleted/TeammateIdle hooks may later feed observability only.

## 24. Top-level CLI dispatch

`scripts/forge.mjs` dispatches:

```text
checkout
pi
codex
claude
default client
```

so that `forge pi`, `forge codex` and `forge claude` are equal top-level harness
entrypoints.

## 25. Package contents

`package.json` distribution files include:

```text
claude/
docs/claude.md
docs/harness-adapters.md
```

plus any Claude hook launcher script the plugin requires. Credentials and
generated runtime state are never bundled.

## 26. Adapter comparison

The three adapters intentionally differ internally:

```text
Pi
→ native extension
→ typed first-class model tools
→ Controller manages execution heartbeat client-side

Codex
→ CLI + hooks
→ supervised launcher
→ forged owns ephemeral execution state and renewal

Claude
→ CLI + plugin hooks
→ supervised launcher
→ forged owns ephemeral execution state and renewal
```

What must be identical is the backend execution contract.

## 27. Required acceptance scenarios

### Common

For every harness:

```text
A reads Issue #1
A claims #1
→ success

B claims #1
→ conflict

B comments on #1
→ success

B creates/links discovered work
→ success

A continues working
→ lease renews automatically

A closes with valid evidence
→ issue closed
→ lease released

old execution later attempts mutation
→ cannot recover authority

crash
→ renewal stops
→ timed lease expires
→ another execution may claim
```

Private authority values never appear in model-visible output.

### Pi

```text
forge pi starts with Forge extension
real Pi session UUID is used
two runtimes with same Pi session remain isolated
session_tree does not release
new/resume/fork/reload creates fresh execution runtime
shutdown best-effort releases
crash recovers by TTL
```

### Codex

```text
root and subagent use distinct thread attribution
daemon auto-renew works
Stop guard works
normal launcher exit releases
crash relies on TTL
resume/fork never restores execution
ambiguous close exact replay works
forged restart invalidates runtime capability
```

No Codex behavior may regress during supervised-runtime refactoring.

### Claude

```text
forge claude injects bundled plugin automatically

SessionStart obtains canonical hook session_id

main agent:
agent_id = main

subagent:
agent_id = real Claude hook agent_id

main claims Issue A
subagent claiming A conflicts

subagent can comment on A

subagent can independently claim Issue B

forge issue commands in subagent Bash route to that subagent identity

automatic daemon renewal works independently for A and B

Edit/Write cooperative guard uses the correct agent identity

Stop checks main execution

SubagentStop checks only that subagent execution

SessionEnd retires the session

normal process exit best-effort releases

Claude crash stops renew and recovers through TTL

resume/clear/start never silently restores an old execution

ambiguous acquire retains its logical attempt

ambiguous close retains original body/proof/key and resolves receipt
```

## 28. Implementation commits

```text
H1  docs: define harness adapter contract
H2  refactor: extract supervised runtime core
H3  feat: add supervised Claude launcher
H4  feat: add Claude plugin lifecycle adapter
H5  feat: route Forge CLI through Claude runtime
H6  test: add Claude adapter acceptance coverage
H7  docs: publish Claude adapter guide
```

H2 extracts reusable machinery from `internal/forge/codex_runtime.go`,
`codex_identity.go` and `codex_events.go` without changing Codex URLs, Codex
request/response shapes, Codex behavior, the Worker API or the execution proof.

## 29. Non-goals

This adapter work MUST NOT introduce:

```text
new Issue storage
new lease schema
new execution proof
new scheduler
new PR domain
Claude Task ↔ Issue automatic synchronization
Claude worktree ↔ Forge checkout authority replacement
MCP
harness-specific close semantics
persistent execution restoration
force-release on process death
```

## 30. Final architecture

```text
                       Forge work authority

                         embedded Kata
                              ▲
                              │
                            forged
                    Worker API / execution proof
                              ▲
              ┌───────────────┼───────────────┐
              │               │               │
             Pi             Codex           Claude
              │               │               │
      native extension    CLI + hooks     CLI + plugin
      local Controller    supervised      supervised
                          runtime         runtime
              │               │               │
              └──── forge pi/codex/claude ───┘
```

User-facing invariant:

```text
different harness,
same Issue,
same execution rules,
same lease authority,
same evidence semantics.
```
