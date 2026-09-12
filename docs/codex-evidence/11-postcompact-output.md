# PostCompact output contract repair

PostCompact previously returned `hookSpecificOutput` with `additionalContext`,
reusing the SessionStart/SubagentStart format. Codex rejects that event-specific
shape. The [official hook contract](https://learn.chatgpt.com/docs/hooks#postcompact)
allows common output fields for PostCompact.

The hook now retains the awaited server `context` event and returns `{}`. No
additional context injection mechanism was added. Existing SessionStart compact
behavior is unchanged; server renewal and pending-operation handling are unchanged.

Test-first evidence (Node 24.19.0, 2026-09-12):

- Before the implementation change, `node --import tsx --test codex/hooks.test.ts`
  exited 1: 4 new PostCompact cases failed, 3 existing cases passed. Each failure
  showed actual `hookSpecificOutput` versus expected `{}`; the preceding RPC
  assertion passed.
- Cases cover manual/auto triggers and root/child attribution, asserting one
  `context` RPC with the correct identity and an empty output object.
- After the fix, `npm run test:codex` passed 22/22 with no skips.
- `npm run typecheck` and `git diff --check` passed.

These are adapter regression checks, not a real Codex compaction E2E run.
The repair was made in the requested checkout; global hook configuration was
not changed or activated against this checkout.
