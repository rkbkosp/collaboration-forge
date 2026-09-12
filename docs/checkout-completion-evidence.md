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
