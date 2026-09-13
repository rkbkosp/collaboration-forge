# Claude Code adapter

`forge claude` launches Claude Code with the bundled Forge plugin. Inside that
session, ordinary `forge issue` and `forge checkout` commands use daemon-owned
execution and automatic renewal: no background session broker, no plugin install
step, and no extra arguments.

Forge remains the work ledger. `forged` owns execution proofs and renewal,
embedded Kata owns issues, leases, comments, links, events and typed evidence,
and the Claude plugin carries only lifecycle, context and attribution. See the
[harness adapter contract](harness-adapters.md) for the shared protocol
invariants.

## Setup

Build/start forged as in the README, then configure `FORGE_URL` and
`FORGE_WORKER_TOKEN_FILE`. Never supply a supervisor credential to an agent.

```sh
forge claude --help-forge
forge claude
```

The launcher injects the plugin with `--plugin-dir` automatically. Any other
`--plugin-dir` argument you pass is left untouched, and supplying the bundled
directory yourself does not add it twice. `forge claude plugin-dir` prints the
directory if you want to load it another way.

Other arguments go to Claude Code, so `forge claude --resume`, `forge claude -p`
and `forge claude --model <model>` work as usual. `FORGE_CLAUDE_BINARY`
overrides the binary, `FORGE_TTL_SECONDS` sets the lease TTL for the instance
(default 300, range 60–3600), and renewal runs at `min(TTL/3, 30s)`.

## Agent workflow

```sh
forge issue list --status open
forge issue get REF
forge issue claim REF
forge issue comment REF --body 'Verified finding...'
forge issue close REF --data-file evidence.json
forge issue release REF --reason 'handoff'
```

Reading, creating issues, commenting and linking stay collaborative and need no
claim. Claiming is what makes you the current execution for that issue; treat a
conflict as a stop. Close only with truthful typed evidence, and release when
handing off.

Inside a launched session these commands route through the daemon facade, so an
inherited legacy `FORGE_SOCKET` is ignored. Explicit `--socket`/`--session-id`
overrides and `forge session start` are refused, because the harness owns its
identity. The session identity comes from Claude's own hook payload, not from a
shell variable.

## Subagents

A subagent shares the Claude session but is a distinct execution identity. It
can read, comment, create issues and link work, and it can claim a *different*
issue. It cannot take or close work the main agent holds, and `SubagentStop`
checks only that subagent's own execution.

## Lifecycle

| Event | Behavior |
| --- | --- |
| SessionStart | Bind the session, publish it for Bash, inject the protocol and current state |
| SubagentStart | Register the subagent and inject the same protocol; never claims work |
| PreToolUse | Bind the current agent to the shell/edit about to run; never decides permissions |
| PostToolUse / PostToolUseFailure | Liveness observation only |
| PreCompact / PostCompact | Refresh state and context; never create, restore, release or re-key an execution |
| UserPromptSubmit | Resume observation if the lease is still live |
| Stop / SubagentStop | Check that agent's exact lease and pending request |
| SessionEnd | Retire that session immediately, then best-effort release |
| Normal exit | Retire the instance and release |
| Crash | Stop renewal; the timed lease expires and another execution may claim |
| forged restart | Reject all old instance capabilities; a new launch and new tenure are required |

The model is never responsible for renewal, and a missing hook never costs the
lease: forging stops renewal when the instance dies, and Kata's timed lease is
the final recovery. Claude's own permission flow is untouched — this adapter
never approves, denies or rewrites a tool call.

## Stop policy

```sh
forge claude status     # current lease, pending request and workspaces
forge claude retry      # resolve an unresolved acquire/close
forge claude pause      # before waiting for the user; renewal stops
forge claude unpause    # resume observation; an expired lease stays expired
```

`FORGE_CLAUDE_STOP_POLICY` accepts `strict` (default) or `advisory`. Strict asks
the agent once to close, release or resolve its pending request before stopping;
advisory only warns. Use `forge claude pause` before asking for user input, and
check `forge claude status` before assuming a pause is recoverable.

If a command exits 3, or a result reports `client_replayed` with
`current_state_not_refreshed`, inspect the issue before assuming anything: a
replayed result is history, not current state. Use `forge claude retry` rather
than reissuing the operation with new arguments.

## Isolated worktrees

After claiming, work in the original directory or use:

```sh
forge checkout ISSUE --dirty
forge checkout ISSUE --ref COMMIT
forge checkout status WORKSPACE_ID
forge checkout list
forge checkout archive WORKSPACE_ID
```

Choose the source explicitly and stop other writers before a dirty capture.
Checkout reuses the current tenure and creates one worktree per repository. Wait
for `state: ready`, then set the returned worktree as the explicit working
directory for your shell, edit and test tools — a CLI cannot change the agent's
working directory. If a shell profile overrides `forge`, use `"$FORGE_CLAUDE_CLI"`
inside isolated worktrees.

After a confirmed close, workspaces are archived in place without deleting files
or branches. If a close reports a pending `workspace_archive`, the close already
succeeded: run `forge checkout archive ID` instead of closing again. A preparing
checkout blocks close.

## Skill

The bundled plugin ships a short Skill describing the model-facing workflow, and
`forge --help` is the authority for exact syntax. Private details — attempt
identities, execution proofs, close headers and daemon endpoints — are never
exposed to the model.
