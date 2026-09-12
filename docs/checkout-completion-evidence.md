# Checkout completion

Work is isolated in `fix/checkout-runtime`, based on `bad95be`.
No active daemon, launcher, global skill or source worktree is updated.

## Pinned submodules

Test-first failures: `TestPinnedSubmoduleOfflineSnapshot` failed in commit mode
with `submodules unsupported`, and dirty mode with `git cat-file failed`.
The regression now exercises both source modes, offline restoration after source
removal, clean parent status, recovery capture, and dirty-child rejection.

`go test ./internal/checkout -count=1` passed after implementation.
Snapshots include local bundles and validated bytes at direct gitlink commits.
Dirty mode requires an initialized, clean child at the indexed commit. Commit
mode reads the pinned local objects. Nested submodules remain explicitly rejected.
No remote submodule URL is followed and no source index or child is changed.

The opt-in `TestRepositoryWithPinnedKataBuildsAfterRestore` passed against the
actual isolated Forge worktree, including its pinned `kata/`: dirty snapshot,
restoration, source-state comparison and `go build ./cmd/forged` in the restored
directory (131.22 seconds). This did not launch or replace an installed service.
Restore validation also rejects child paths that would traverse a working symlink.

## Runtime adapter

The first server test failed because the worker checkout adapter was missing.
The first Controller retry test failed because no pending checkout state existed.
The persistence fault reproduced `preparing` on a second job after a record rename
followed by failed directory fsync; the request now retains one failed record.

Server coverage includes signed-session rejection, exact existing claim, request
fingerprint conflicts, close/archive replay after release, renewal during blocked
preparation, release before restore, and retry after daemon restart without a new
job or lease. Node coverage checks private proof/key reuse, no second acquire,
socket routing and refusal of mixed runtimes.

The real Pi 0.85.1 SDK test exercises all fourteen registered tools, including
checkout, status, list and archive. No model inference or TUI interaction is claimed.

## Existing test correction

`tests/client-e2e.test.ts` expected exit 1 for an unknown worker tool, although the
documented usage exit code and implementation are 2. The same failure was reproduced
in the untouched `bad95be` checkout using its existing binary and disposable service.
The expectation is corrected independently; worker authority is not broadened.

## Multi-repository project and packaging

`tests/worker-checkout-e2e.test.ts` uses a disposable project and three independent
repositories named `fornax`, `fornax-agent-runtime` and `fornax-console`. Both CLI
and Pi create three worktrees with one original claim. Assertions cover distinct
repository IDs/paths, one project/issue/tenure, dirty versus commit content, source
preservation, registered-root lookup, CLI reads from managed directories with the
same socket, inventory without a broker, and one close archiving all three trees.
No real Fornax ledger or source is mutated by this test. The first routing assertion
was corrected to compare canonical paths (`/tmp` resolves to `/private/tmp` on macOS).
The existing real Codex facade multi-repository/shim integration also passes.

The package regression initially failed with `ERR_MODULE_NOT_FOUND` for
`pi-extension/errors.ts`. Adding it to the package allowlist makes extracted
CLI help, checkout help and Pi extension import succeed using the installed
dependency tree. No global package install or service activation is performed.

## Final validation

- `go test -race ./...`: passed; `go vet ./...`: passed.
- `npm test`: 90 passed; `npm run typecheck`: passed (Node 24.19.0).
- `npm run test:e2e`: 13 passed, 2 opt-in model tests skipped. Includes real
  daemon/CLI, Pi SDK, Codex facade, three-repository projects, SIGKILL and TTL.
- Actual Forge+Kata snapshot/restore/build opt-in acceptance: passed, as above.
- Extracted package CLI/checkout/Pi import smoke: passed.
- `git diff --check`: passed. Both original and new Kata submodules remain clean.

Activation is excluded. The original worktree and global host wrapper/hooks/skills
were not modified. Existing service PIDs remained 91435 (Forge) and 6595 (Fornax).
For later deployment, build from this branch and update CLI/extension/daemon
together after current sessions finish; use a workspace root outside source
repositories (the Forge service's repository-local `.forge/workspaces` default
would be rejected). Start fresh Codex/Pi runtimes; do not copy old execution state.
The host wrapper's socket restriction remains in effect: standalone CLI work in
managed directories uses the absolute installed entrypoint plus its own socket.
