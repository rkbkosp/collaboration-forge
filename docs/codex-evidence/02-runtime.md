# Package 2: daemon-owned Codex runtimes

Test-first: TestCodexRuntime initially failed to compile (missing runtime and
command types). The implementation now authenticates instance capabilities
behind the existing worker Bearer, binds session/thread/instance to a separate
execution subject, and owns timed renewal in forged. No schema or private SQL.

Validation: `go test -race ./internal/forge ./cmd/forged` passed; extended
`go test -race ./internal/forge -run TestCodex` passed after adding real embedded
Kata claim/close/knowledge contribution and fast end-under-contention cases.
Tests cover PID loss with zero renew/release, distinct threads, wrong capability,
automatic renewal, exact close, Codex timeline attribution and idempotent end.

End retires the instance before queuing bounded best-effort release, without
waiting for a busy thread. Crash retires without release. Each HTTP dispatch has
a 5-second bound; close receipts remain Kata-owned. Fresh daemon rejects old
instances, including retries; users must launch a fresh instance and wait TTL.
The 30-minute per-thread inactivity ceiling stops renewal even if the parent
process survives; later packages connect hooks and expose this state.
