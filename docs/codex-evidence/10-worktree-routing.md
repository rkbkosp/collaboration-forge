# Registered project routing from ordinary Git worktrees

Base: `codex/claimed-checkout@827d0e7`. The host wrapper used absolute cwd
prefixes; even `forge --help` in the branch's linked worktree exited 2 with
`forge_project_binding`. The managed-checkout shim delegated this path back to
that wrapper, reproducing the same failure.

The new project-root helper compares canonical Git common directories against
host-supplied registrations. It preserves cwd and leaves endpoint, credential,
socket and execution checks in the existing wrapper/server. Unknown repositories
fail closed. The managed-checkout shim is unchanged.

Test-first evidence: the new routing regression failed with MODULE_NOT_FOUND
before the helper was implemented. It then passed for main/linked/subdirectory/
symlink paths and distinct registered projects, rejecting unrelated and nested
repositories and inherited Git directory overrides. All fixtures are temporary;
no worktree was added to the product repository and Kata was untouched.

Validation:

- Node unit suite: 84 passed, zero failures/skips. TypeScript typecheck passed.
- `npm run test:checkout-e2e` passed against a freshly built real daemon and
  embedded Kata. Its host wrapper now enforces registered Git roots instead of
  unconditionally forwarding. The worker creates an ordinary Git worktree and
  reads its current runtime and Issue from both the root and a subdirectory,
  then creates managed checkouts and closes/archives using the same claim.
- A candidate local host wrapper successfully queried the installed Forge project
  from the existing development worktree; a conflicting Fornax endpoint was
  rejected with exit 2. No credential contents were printed.

After backing up the local wrapper, the helper was installed and the wrapper's
cwd selector was replaced. Live readback confirmed the same Forge project UID
from main, the development worktree and its `codex/` subdirectory; Fornax and
fornax-pi still resolved to their separate shared Fornax project. Conflicting URL,
project root, worker credential path and socket overrides all exited 2, as did an
unregistered directory. Both product worktrees' Kata submodules remained clean.

The local host wrapper integration is machine configuration, separate from these
source commits. Updating it does not merge this branch, change the installed CLI
entrypoint, restart either service, or activate the new managed-checkout API.
The real-model opt-in test was not rerun for this routing-only change.
