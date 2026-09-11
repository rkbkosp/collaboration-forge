# Package 3: CLI identity facade and two-hop recovery

Test-first: facade tests failed on the missing module; Go replay test failed on
missing RequestID. Added a immutable per-call identity/parameter snapshot and
same-request-ID network retries. This module is wired to the shared CLI after
its separately owned baseline is committed (installation package).

Validation: Node launcher/facade 4/4 and dedicated typecheck pass;
`go test -race ./internal/forge -run TestCodex` passes, including real Kata replay
and a committed acquire whose first response is dropped. Failed collaborative
requests do not erase pending execution retry. Result replay does not restore
an old execution after release. Same ID/different payload is rejected.

Daemon caches the last execution result for explicit retry and 32 recent request
results (16 MiB bound) per thread; older request IDs remain tombstoned, never
reexecuted. Non-idempotent collaborative writes with unknown outcomes are not
automatically reexecuted. A runtime allows up to 4096 mutation request IDs.
All receipts are ephemeral. Daemon restart rejects the instance, rather than
recovering authority into a new runtime. Kata close-v2 receipts remain durable.
