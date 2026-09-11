# Package 4: lifecycle hook adapter

Test-first: missing codexEvent and hooks.ts caused Go compilation and Node
module-resolution failures. Added context/start, prompt/touch, interrupt/pause,
stop-check and session-scoped end. Compaction does not reset pending authority.
SessionEnd queues cleanup without waiting for execution; unrelated sessions stay
live. Retired session tombstones reject delayed tool calls and hook starts.

Validation: Node Codex tests 7/7, dedicated typecheck and Go Codex race tests pass.
Reviewed official rust-v0.154.0 source using authenticated read-only GitHub API:
- core/src/session/session.rs: session_id equals root thread ID, shared by descendants.
- core/src/hook_runtime.rs: subagent agent_id is sess.thread_id().
- hooks/src/schema.rs: child tool/prompt/compact payloads also have agent_id.
- hooks/src/engine/command_runner.rs: hook commands replay session environment snapshot.

Consequently hook attribution uses payload agent_id for every child event, not
an inherited root CODEX_THREAD_ID. Launcher strips parent CODEX_* identity before
spawning a new harness. Ordinary CLI requests still require both harness-injected
CODEX_SESSION_ID and CODEX_THREAD_ID. Stop output behavior is package 5;
installation of the generated bounded command bundle is package 6.
