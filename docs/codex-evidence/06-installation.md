# Package 6: CLI integration, installation and real acceptance

Integrated only after the concurrent CLI baseline `77db0d8` was committed.
The standard issue commands now use the Codex facade inside the launcher;
`forge codex` exposes launch, status/retry/pause, hook configuration/install,
and the separate Codex skill. Package exports include the adapter and hook entry.
The existing direct HTTP transport also prevents proxy-environment credential
routing. Installation is explicit and refuses to overwrite hooks or skills.

Test-first failures and corrections:

- Installer tests initially lacked their CLI module; the implementation now
  creates the bundle and rejects a second install.
- Real process fixtures exposed optional absent lease fields and a worker stdin
  handle that prevented exit; assertions and owned-process cleanup were fixed.
- A renewal cadence regression failed because successful renewal was overwritten
  by the 5-second retry delay. Success now keeps TTL/3 capped at 30 seconds.
- Pending close receipt recovery initially identified a subsequent historical
  retry as the previous claim. A failing regression now verifies `issue_close`.
- A real model could claim/close successfully while no hook event record existed.
  This was not accepted as lifecycle success. The final smoke fixture uses an
  isolated temporary CODEX_HOME with an explicitly trusted temporary project and
  hooks enabled, symlinking only existing auth.json. Hook definition trust is
  bypassed only for this reviewed invocation. No global config or trust is changed.

Validation on Node 24.19.0 / Codex CLI 0.154.0:

- `npm test`: 72 passed; `npm run typecheck`: passed.
- Root `go test ./...`, build and vet: passed.
- `go test -race ./internal/forge ./cmd/forged`: passed.
- `npm run test:e2e`: 11 passed, 1 opt-in real-model test skipped. Includes
  existing CLI and Pi SDK tests plus real Codex launcher/daemon process tests.
- Codex process tests verify silent automatic renewal, fresh-instance conflict,
  normal close, launcher SIGKILL, unchanged lease expiry after loss, real TTL
  reclaim, and daemon restart rejecting an old capability.
- `FORGE_REAL_CODEX=1 npm run test:codex-live`: passed with a real model and shell
  calling the normal Forge executable through its launcher. Recorded successful
  SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, Stop, SessionEnd.
  The fixture issue is closed with typed no-change evidence and has no lease.
  Records contain event names/success flags only; captured output is checked for
  worker/admin credential disclosure and is not dumped on failure.
- Actual npm tarball extracted outside the repository: standard help, Codex help,
  hook generation/install and skill install passed with the shared dependency
  tree. This is packaging/import coverage, not a clean-room dependency install.

Subagent, compaction, interrupted/continued Stop, and ambiguous receipt edge
cases have protocol fixtures and server race tests. They are not each a separate
real multi-agent model run. Kata checkout/schema were unchanged by this package;
its complete PostgreSQL suite was not rerun for this adapter-only integration.
No global installation, activation, push or publication is implied.
