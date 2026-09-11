# Forge development

- Follow DEV.md's amended K/F boundaries. Keep changes buildable, tested, and
  committed in independent logical steps; never amend or squash without approval.
- Use test-first development and keep evidence for failures and fixes.
- Do not invent an Issue database: embedded Kata owns issues, leases, events,
  links, comments, and typed evidence. Never query Kata private SQL here.
- Worker authority must be enforced by the server, not by hidden tools.
- No schema migrations, PR/Change domain, orchestration scheduler, or unrelated UI.
- Never restore an old execution into a new Pi runtime. Network retries of the
  same logical acquire must reuse their original attempt identity.
- Do not log credentials or secrets. The service is loopback-only by default;
  do not silently expose unauthenticated listeners.
- kata/ is a fixed-commit patched upstream fork submodule. The versioned patch
  export remains available for reconstruction; never make the submodule dirty
  or add another nested worktree to this repo.
