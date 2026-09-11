---
name: forge-codex
description: Use Forge's CLI work ledger inside a Codex runtime launched by forge codex; claim issues, contribute findings, close with truthful evidence, and safely pause or recover lifecycle operations.
---

# Forge in Codex

Use `forge issue` as the active interface. `forged` owns execution identity and
renewal; Codex hooks carry lifecycle/context. Never start `forge session start`
or inherit another worker's socket. Do not read, print or copy token files.

1. Inspect `forge issue list`, `forge issue get REF`, and dependencies with
   `forge issue graph REF`. Owner is long-term responsibility, not execution.
2. Before substantial work, `forge issue claim REF`. Treat conflicts as a stop
   for execution. Reads, new issues, comments and links remain collaborative.
3. Record findings with `forge issue comment REF --body TEXT`, create follow-ups,
   and link dependencies. Use `forge --help` for typed parameters.
4. Close with `forge issue close REF --data-file FILE`; the JSON contains a
   reason, message and truthful typed evidence. Never invent tests or commits.
   Server close-v2 checks the exact live tenure and releases it atomically.
5. On exit code 3 or a pending request, use `forge codex retry`. Do not alter the
   original operation. A `client_replayed` result with
   `current_state_not_refreshed` is historical: inspect the issue before claiming
   it remains closed. New request IDs must not be used to guess past outcomes.
6. Before asking the user for input, `forge codex pause`. Renewal stops; the
   existing lease may expire. `forge codex unpause` resumes liveness only; check
   `forge codex status`, then claim afresh if the tenure is gone. Release when
   abandoning work. Do not force-release another worker.
7. Normal session end queues best-effort release. Interrupt/advisory Stop pauses
   renewal. Crashes and missed child Stop hooks recover through process fencing
   or the 30-minute activity ceiling plus Kata TTL (300 seconds by default).
8. Never restore execution from transcript/history. After daemon restart or
   launcher death, start a new `forge codex` instance and inspect the ledger.
   Resume/fork through a fresh launcher gets new execution authority.

This is trusted same-UID local coordination, not an OS sandbox or proof of work.
The server restricts worker ledger mutations independently of hook availability.

## Optional isolated worktrees

Claim first, then either work in the original directory or run
`forge checkout ISSUE --dirty --source REPO` / `forge checkout ISSUE --ref COMMIT --source REPO`.
Choose the source explicitly; stop source writers for dirty capture. Repeat for
each repository in a multi-repository Issue. Checkout reuses the current tenure.
Wait for `state: ready`, then explicitly set each shell/edit/test tool's working
directory or absolute paths to the returned worktree. A CLI cannot change the
Agent's cwd. If login PATH overrides the runtime shim, use `"$FORGE_CODEX_CLI"`.

Use `forge checkout list ISSUE` and `forge checkout status ID` to inspect jobs.
After a confirmed Issue close, workspaces are archived in place without deleting
files or branches. Archive is not a merge or proof that every change was committed.
If `workspace_archive.state` is pending, close already succeeded: use
`forge checkout archive ID`, not a new close. A preparing checkout blocks close.

Pause, crash, release and restart preserve artifacts for explicit recovery.
After a fresh claim, use `forge checkout ISSUE --recover OLD_ID --dirty` to copy
retained progress into a new tree. Never restore an old execution or silently
reuse another tenure's working directory. This requires the new server/CLI and
an artifact root outside source repositories; older services cannot supply it.
