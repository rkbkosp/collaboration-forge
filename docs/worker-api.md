# Worker API

Authenticate with the worker Bearer token and `X-Forge-Session` containing the
real Pi session UUID (v4 or v7; Pi 0.85.1 creates v7 sessions). The host configures one project; worker requests cannot
choose another project. All operations use `POST /forge/v1/tools/{name}` with a
single JSON object, at most 1 MiB. Unknown fields are refused. Responses retain
Kata's JSON, with bounded 8 MiB capture. Errors use `{status,error:{code,message}}`.

| Operation | Input |
| --- | --- |
| issue_list | optional status (open/closed), limit (1–1000) |
| issue_get | ref |
| issue_timeline | ref, optional after_id/limit (read projection, used by human CLI) |
| issue_graph | ref, optional depth (1–10) |
| issue_create | title, optional body |
| issue_comment | ref, body |
| issue_link | ref, type (parent/blocks/related), to_ref |
| issue_claim | ref, private attempt_id, optional ttl_seconds/purpose |
| issue_renew | canonical ref, optional ttl_seconds |
| issue_release | canonical ref, optional reason |
| issue_close | canonical ref, reason, message, optional evidence/if_match |

Refs are bare Kata short IDs or ULIDs. Contributions need no execution lease.
Links are additive; replacing parents, deleting links, assignment, priority,
arbitrary edits/metadata, reopening, deleting and force-release are unavailable
to workers. The host checks both the operation and cumulative project scope,
not merely which tools an agent happens to expose.

## Execution credentials

A new logical acquire uses a fresh private UUIDv7 (UUIDv4 is also accepted)
`attempt_id`. Repeat this exact nonce after a lost/ambiguous acquire response.
Do not generate a new nonce on every HTTP retry or restore old nonces from Pi
history into a new runtime. The server derives an opaque execution principal
from session, attempt, and canonical issue identity. Same-session concurrent
runtimes therefore remain different holders.

Successful acquire returns `lease`, `execution_id`, `execution_token`, and
`server_now`. A competing acquire returns `409 claim_denied`. The caller keeps
the token privately in memory and sends it in `X-Forge-Execution` for renew,
release and close. These operations require the canonical issue UID returned
by acquire. The token binds the subject, session, issue UID and ClaimUID; it is
not a caller-supplied principal. Do not show it or the acquire nonce to a model,
write it to session entries, or log it.

Agent leases are always timed: TTL defaults to 300 seconds, allowed range
60–3600. Renew returns `server_now` for conservative expiry calculation. The
lease remains Kata's authority; a valid signature alone never authorizes close.
An acquire racing a host close is reconciled and its closed-issue lease released.

## Strict close and retries

Every close requires a stable `Idempotency-Key` generated for its logical close
attempt. The façade supplies `close-v2` and exact `If-Lease-Match` from the proof;
clients cannot substitute them, choose actors, or request a bypass. Optional
`if_match` is an independent issue-revision precondition, such as `"rev-1"`.
Evidence uses Kata's typed union (test command, commit SHA, PR URL, reviewed
paths, external account, no-change rationale, duplicate/superseded issue ref).
It is a submission of evidence, **not proof that a command was actually run**.

Keep the original key, request body, and execution proof after a network/500
ambiguous outcome. Retry exactly, including after the lease is no longer live:
Kata restores the original receipt before checking current tenure. Do not put a
client/server live-lease preflight in front of a pending close replay. Changed
request inputs or a new tenure must not reuse that key. An old receipt replay
never closes a reopened issue or releases a new tenure. The signing key persists
across daemon restarts so a still-running client can recover its pending receipt.

Credentials are a cooperative local trust boundary, not an OS sandbox. Anyone
who can read the supervisor token or host data directory can supervise the
service. External filesystem writers do not participate in issue lease fencing.

## Timeline current state and history

`issue_timeline` returns the existing `issue`, `events`, scan cursor and reset
fields, plus:

- `lease`: Kata's current lease projection, explicitly `null` when absent.
- `lease_hub_now`: Kata's authority clock when supplied by `showIssue`.
- `pending_leases`: unresolved pending requests when present; do not treat them
  as confirmed execution authority.
- `observed_at`: Forge's UTC time immediately after the issue read completes,
  before reading events. It is present even when no lease exists.

These fields are observations of the configured local project, not a shared
transaction snapshot of the issue, lease and all history pages. Every request
reads current state again. Preserve the observation time when displaying cached
results. An unavailable read means unknown, not unclaimed; old servers omitting
these fields cannot establish current authority through timeline alone.

A timed lease is held only at its authority clock while unexpired. Do not derive
current expiry from `claim.acquired` events: renew updates the lease without
emitting heartbeat events. Displaying a holder proves authorization, not that
its process is actively working. Expiry or a displayed unclaimed state never
substitutes for server-side atomic acquire.

The CLI prints a first-page state heading, server confirmation time and lease
expiry, followed by history. Rerun to refresh; `--details` adds exact holder and
ClaimUID to the heading. It shows unknown on failed reads and retains the last
displayed confirmation time if a later page fails. It does not use the local
wall clock to infer a new holder or availability. Historical close evidence
remains in the event stream.

Automatic execution-evidence collection remains a future goal. This change
only projects existing Kata state and events; it adds no evidence ingestion,
heartbeat events or execution database.
