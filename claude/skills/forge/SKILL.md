---
name: forge
description: Use Forge's CLI work ledger inside a Claude Code session launched by forge claude; inspect and claim Issues, record durable findings, use isolated checkouts, and close with truthful typed evidence.
---

# Forge in Claude

Forge is the authoritative work ledger. Use the `forge` CLI; `forged` owns
execution identity and renewal, and this plugin's hooks carry lifecycle and
context. Never run `forge session start`, and do not read, print or copy token
files.

Before substantial work:

1. Inspect the relevant Issue with `forge issue list`, `forge issue get REF`, and
   its dependencies with `forge issue graph REF`. Owner is long-term
   responsibility, not execution.
2. Claim it before executing: `forge issue claim REF`. Treat a conflict as a stop
   for execution. Reading, creating Issues, commenting and linking stay
   collaborative and need no claim.
3. Record durable discoveries where they will outlive this session: comments with
   `forge issue comment REF --body TEXT`, new Issues for separate work, and links
   for dependencies. Run `forge --help` for exact syntax and typed parameters.
4. Use `forge checkout ISSUE --dirty` or `--ref COMMIT` when isolation is needed,
   one per repository. Wait for `state: ready`, then set the returned worktree as
   the explicit working directory for shell, edit and test tools. A CLI cannot
   change your working directory. Completed checkouts are archived without
   deletion.
5. Close only with truthful typed evidence:
   `forge issue close REF --data-file FILE`. Never invent tests, commits or
   results. If a close reports a pending `workspace_archive`, the close already
   succeeded; use `forge checkout archive ID` instead of closing again.
6. Release when handing off: `forge issue release REF --reason TEXT`.

If a command exits 3 or reports an unresolved request, run `forge claude retry`.
Do not alter the original operation, and do not use a new request to guess a past
outcome. Before waiting for user input, run `forge claude pause`; `forge claude
unpause` resumes observation only, so check `forge claude status` and claim again
if the tenure is gone.

Never restore execution from transcript or history. After a daemon restart or a
launcher exit, start a new `forge claude` session and inspect the ledger. Resuming
or clearing a Claude session does not restore an old execution.

This is trusted same-UID local coordination, not an OS sandbox or proof of work.
The server restricts ledger mutations independently of whether hooks are active.
