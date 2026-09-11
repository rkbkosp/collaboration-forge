# Checkout attached to a Codex claim

Implemented on `codex/claimed-checkout` in an isolated worktree based on
`7a98245317d58a4703db2de39dea0f7a88a44fc7`. The installed CLI executes the
original checkout, so that checkout, its global Hooks/Skill and both running
project services were deliberately left unchanged. These commits are source
validation, not activation or deployment.

## Test-first evidence

The initial Go tests failed because workspace storage and the claimed-checkout
adapter did not exist. The CLI parser/polling tests initially failed on the
missing module; launcher routing coverage failed before the temporary shim was
installed. The concurrent-root regression then reproduced a second daemon
opening an already owned artifact root. An exclusive lock now prevents this;
shutdown joins workers and rejects late writes before handing over the root.
All of these regressions pass in the final tree.

Go tests use real embedded Kata and disposable Git repositories. They cover:

- Unclaimed rejection, exact current tenure, repeated request ID returning the
  same workspace, and no additional acquire or renewer.
- Multiple repositories attached to one claim, commit versus staged/unstaged/
  untracked snapshot content, and rejection of a different thread.
- Renewal while capture is blocked, close refused during preparation, and lease
  loss preventing restore/readiness.
- Automatic retained archive on close, failed archive persistence followed by
  manual retry, and a lost committed-close response recovered after expiry.
- Release/restart orphaning, rejection of old instance capabilities, and explicit
  recovery under a fresh claim into a separate path retaining the previous tree.
- Exclusive workspace-root ownership and rejection of writes after store close.

## Validation

- `go test -race ./...`, `go vet ./...`, and `go build ./cmd/forged`: passed.
- Workspace lock/shutdown regression rerun after adding the late-write assertion:
  passed under the race detector.
- `npm test`: 78 passed; `npm run typecheck`: passed.
- `npm run test:e2e`: 12 passed, two opt-in real-model tests skipped. This includes
  real daemon/CLI checkout, existing Pi integration and real SIGKILL/TTL tests.
- `FORGE_REAL_CODEX=1 npm run test:checkout-live`: real model fixture separately
  passed (one test, 44.7 seconds). It verifies claim, dirty checkout, shell `pwd` in the returned worktree, content
  inspection, close, archived metadata and retained files. Hooks include
  SessionStart, PreToolUse, PostToolUse, Stop and SessionEnd.
- Package smoke: `npm pack` into a temporary directory, extraction, packaged
  `forge checkout --help`, and shipped checkout documentation: passed. The first
  extraction helper used an unsupported Python `tarfile` option; rerunning with
  the system tar succeeded without changing product code.
- `git diff --check`: passed.

All integration daemons, ledgers and repositories are disposable fixtures. The
real-model fixture uses a temporary Codex home/config and existing authentication;
it does not edit the user's global configuration or active project ledgers.

## Scope and limits

Checkout preserves the existing execution and Kata claim. It does not switch the
parent Codex cwd: tools must use the returned worktree explicitly. Archive is
metadata only and retains all files, branches and repositories; it does not merge
or certify uncommitted changes. Filesystem writes are cooperative, not lease
fenced. Ignored files and unsupported snapshot entries are not silently copied.

Artifact roots must be outside source repositories. Changing production startup
configuration, installed CLI/Hook/Skill versions, or restarting either project
service remains a later activation step. See `../codex-checkout.md` for commands,
layout, reconciliation and multi-repository behavior.
