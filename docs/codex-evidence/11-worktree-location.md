# Short worktree locations and source-volume placement

Base: `main@e54dfbb`. Existing checkout paths nested Issue UID, tenure hash,
repository hash and workspace UUID beneath a project root. Creation and Codex
routing both depended on that hierarchy.

New worktrees use `SELECTED_ROOT/WORKSPACE_UUID/worktree`. Relationship fields
remain in existing records and Kata observations. Host `project.worktree_root`
(or `--worktree-root`) takes priority, followed by a per-user root on the source
filesystem, then the platform application-support directory. Explicit unusable
roots fail visibly. Snapshots live alongside worktrees outside source repositories.
Existing records and old paths remain readable; no filesystem migration or
execution restoration is performed. The Codex shim matches recorded locations.

Test-first evidence:

- The new root-selection suite initially failed to build because
  `chooseWorktreeRoot` did not exist.
- Configuration tests initially failed because `loadProjectConfig` did not exist.
- The existing launcher regression, changed to place a managed checkout outside
  the record root, failed behaviorally: the old hierarchy-based shim returned
  `host-project-routing` instead of invoking the Forge client. It passes with
  record-based lookup, including nested cwd and an unregistered lookalike path.

Validation:

- Root Go suite passed (`go test ./...`). Root build and vet passed.
- Race checks passed for `internal/forge` and `cmd/forged`.
- Node unit suite: 94 passed, zero failures/skips. TypeScript typecheck passed.
- Real daemon/embedded Kata checkout E2E: 2 passed, zero skips, covering Codex
  plus CLI/Pi multi-repository checkout, shared tenure, routing and retained archive.
- Root selection tests cover explicit preference, same-volume preference,
  unavailable volume/directory fallback, unusable explicit root, total failure,
  relative path rejection and actual source filesystem device identity.
- Legacy long-path record loading, short-path restart orphaning, and explicit
  recovery for trees outside the record root passed.

Fallback failure cases use isolated filesystem fixtures; they are not a claim
of testing a physically disconnected or read-only external disk. E2E uses an
explicit temporary worktree root and does not create host volume-root directories.
No installed daemon was restarted or replaced. Kata's submodule is unchanged.
