# Forge Pi extension (F5–F6)

Requires Node >=22.19 and Pi **0.85.1**. Runtime dependencies are pinned in the
root package/lockfile: Pi 0.85.1, typebox **1.3.7** (not `@sinclair/typebox`),
UUID 13.0.2. This is cooperative execution control, **not an OS sandbox**.
Forged must independently enforce worker capabilities and exact-lease close.

## Run

```sh
npm ci
export FORGE_URL=http://127.0.0.1:7347
export FORGE_WORKER_TOKEN_FILE=/absolute/path/to/worker-token
export FORGE_TTL_SECONDS=300
pi -e ./pi-extension/forge.ts
```

The token file must be an owned regular file with exactly `0600` permissions;
symlinks, empty tokens, and files over 16 KiB are rejected. Do not paste the token
into Pi prompts, shell arguments/history, or configuration stored in a session.
The URL must be a literal loopback origin (`127.0.0.0/8` or `::1`), optionally
with a port. Hostnames, paths, userinfo, query/fragment, shorthand/octal/integer
IPv4 forms, and redirects are rejected. A private direct HTTP dispatcher bypasses
Node environment proxies, so `HTTP_PROXY` / `NODE_USE_ENV_PROXY` cannot route
Forge credentials away from loopback. Only the three environment variables
above configure the production adapter; defaults are URL `http://127.0.0.1:7347`
and TTL 300 seconds (valid range 60–3600).

For package installation use the root `pi.extensions` manifest, which selects
only `pi-extension/forge.ts`, not the helper/test modules. CLI `-e` is convenient
for trials; follow Pi's extension installation guidance for normal discovery.

## Tools and workflow

Ten model tools: `issue_list`, `issue_get`, `issue_graph`, `issue_create`,
`issue_comment`, `issue_link`, `issue_claim`, `issue_renew`, `issue_release`,
`issue_close`. Tool schemas reject extra fields, including authority, execution,
force/replace, and protocol controls. TTL comes from config, not the model.

- Read/create/comment/add-link do not require an execution lease.
- Claim one issue before editing. New logical acquisitions use UUIDv7 attempts.
  An ambiguous acquire retains its full original attempt/request; retry the
  identical `issue_claim`. Another ref or purpose cannot discard it silently.
- Heartbeat runs every `min(TTL/3, 30s)`. Server timestamps and a monotonic
  request-start clock determine a conservative deadline (full RTT charged,
  capped at requested TTL, minus 1 second). Local wall-clock time is not lease
  authority. A response arriving after the previous deadline cannot revive it.
- Every `tool_call` for `edit`, `write`, `bash`, or `apply_patch` uses the
  `issue_get` endpoint to verify the **exact** ClaimUID, issue UID and principal
  tuple. Despite its name, this read is HTTP **POST**, like all facade tools.
  A same-holder/different-ClaimUID lease is not the same tenure. Expiry,
  force-release, explicit loss, or network uncertainty stops editing. Read and
  additive contributions remain available. Network uncertainty can recover only
  after fresh exact confirmation; definite loss requires a new acquisition.
- `before_agent_start` refreshes the bound issue and adds UID, revision, owner,
  live lease and stop state to the per-turn system prompt. It distinguishes
  long-term owner from short-lived execution authority and forbids fabricated
  test/evidence claims.
- `issue_close` takes typed Kata evidence (e.g. `{type:"test", command:"npm test"}`),
  not free-form evidence strings. Kata remains authoritative for reason-specific
  completion validation. Never report a command as run unless it really was.
- On an ambiguous close, **retry exactly the original arguments**, even after
  `issue_get` reports closed/released or the local lease deadline passes. The
  controller retains the complete canonical request, original execution token,
  and Idempotency-Key in private memory. Retry does **not** require a live lease;
  K7 resolves the receipt before lease validation. A different body cannot reuse
  an unresolved key. Confirmed success clears the lease/heartbeat.

All requests use the worker Bearer token and **real Pi session UUID** from
`ctx.sessionManager.getSessionId()` as `X-Forge-Session`. The controller's random
`runtimeId` is diagnostic only and is not a wire principal. Renewal/release/close
send canonical issue UIDs and the private `X-Forge-Execution` token; close also
sends its private Idempotency-Key. The plugin never sends `If-Lease-Match` or
`retry_protocol`: forged injects exact close-v2 authority from the signed token.
`execution_id` is treated as opaque; no subject-string parsing is performed.

Tokens and attempt IDs never enter model parameters. Results, errors, prompt
context, and spilled output are redacted recursively, including echoed known
credential values. Private execution fields are removed from result objects.
No credentials or retry state are written to `appendEntry` or messages. Tool
output is capped at 2000 lines/50 KiB; larger **redacted** output is saved in an
owned `0600` temporary JSON file, with its location in the tool result.

## Pi lifecycle

Verified against installed 0.85.1 docs, declarations and reload implementation:

| Pi action | Extension behavior |
|---|---|
| startup | `session_start`: read env and current Pi session ID; fresh controller |
| new/resume/fork/clone | old `session_shutdown`, then fresh `session_start` with new/current Pi session ID |
| reload | old `session_shutdown(reason:"reload")`, fresh factory/start |
| tree | `session_tree`: explicitly do nothing |
| quit | immediate local invalidation, stop heartbeat, bounded best-effort release |
| crash | no cleanup assumption; server TTL recovers authority |

There is no `session_switch` event in this Pi version. Cancelled before-switch
or before-fork events must not release a still-running session's lease.
Even two controllers using the **same real Pi session UUID** generate different
attempts and cannot inherit each other's active lease. History is never read to
restore an execution, including after resume/reload. Factories register hooks
and tools only; they start no background timers or networking.

`/forge-stop` and `/forge-logout` stop this Forge runtime, discard local execution
state, and best-effort release; reload to initialize a fresh controller. Pi's
built-in provider `/logout` is unrelated OAuth logout and has no Forge lifecycle
event; it is not intercepted. Ordinary agent turn completion is **not** session
shutdown and does not release the lease. Request timeout is 10 seconds;
shutdown release is bounded at 1.5 seconds. Failed release may leave a timed
lease until expiry.

Limits: these hooks cannot sandbox custom tools, other extensions, user `!`
commands, or an already-running stock shell command. Pi parallel-tool preflight
also precedes sibling results; claim and edits should be separate model turns.
The controller serializes its own execution operations but does not claim to
close the cooperative preflight-to-file-write race. Server-side close fencing
is the correctness boundary for issue mutations.

## Exported controller / real-server E2E

`pi-extension/forge.ts` exports `Controller`, `ForgeError`, `loadConfig`, the
`ForgeConfig`/`ControllerOptions`/`Clock` types, and `registerForge`.

```ts
import { Controller } from "./pi-extension/forge.ts";
import { randomUUID } from "node:crypto";

// Production configuration reader; no token literal required.
const piSessionId = randomUUID(); // In Pi: ctx.sessionManager.getSessionId()
const a = await Controller.fromEnv(piSessionId);
const b = await Controller.fromEnv(piSessionId); // SAME session, new runtime
try {
  await a.execute("issue_claim", { ref: "abc4" }); // Bare short ID; qualified refs are forbidden.
  // b.execute("issue_claim", sameRef) must conflict until release/expiry.
  await b.execute("issue_comment", { ref: "abc4", body: "Finding" });
  const blocked = await b.guardTool("edit"); // {block:true, reason:...}
  const allowed = await a.guardTool("edit"); // undefined iff freshly confirmed
  const state = a.state(); // safe mode/reason/binding/pending flags, never tokens
  const context = await a.context(); // freshly read, redacted model context
  await a.execute("issue_release", { ref: "abc4" });
} finally {
  await a.shutdown("test");
  await b.shutdown("test");
}
```

For a server fixture or deterministic fault injection:

```ts
new Controller(
  { url, workerToken, ttlSeconds: 60 },
  { sessionId, fetch: optionalFetchWrapper, clock: optionalMonotonicClock,
    requestTimeoutMs: 10_000, shutdownTimeoutMs: 1_500 },
);
// fromEnv(sessionId, env = process.env, optionsWithoutSessionId = {})
// execute(toolName, modelParams, optionalAbortSignal) -> sanitized raw Kata JSON
// guardTool(toolName, optionalAbortSignal) -> block or undefined
// context(optionalAbortSignal) -> safe string
// state() -> safe synchronous snapshot
// shutdown(reason = "quit") -> idempotent Promise<void>, invalidates immediately
```

A lost-response close integration can wrap `fetch`: await the first real close
response (server commit), throw instead of returning it, then permit the retry.
Assert the same request body, X-Forge-Execution and Idempotency-Key are resent;
inspect the real issue's closed state and release event using the server API.
Do not print captured credentials. Controller injection is an E2E/embedding
interface, not a model-accessible configuration mechanism.

## Validation and evidence

```sh
npm test
npm run typecheck
npm audit --omit=dev
```

Tests use `tsx --test`/`node:test`. The initial test run failed due to the missing
controller, then passed after implementation. A wire-shape correction changed
the fake release from invented `released:true` to Kata's real `granted:true`;
the test failed, and the controller was corrected.

Coverage includes A/B conflict, same-session new-runtime isolation, stable
ambiguous acquire attempts, new-tenure attempts, additive nonholder writes,
automatic renew, monotonic expiry/late renew, force-release/replacement,
immutable close replay after release, sanitized errors/results, serialized
claims, lifecycle hook mapping/tree no-op, environment/schema validation,
shutdown cancellation/bounds and real loopback HTTP redirect rejection.

The hook tests use a typed fake ExtensionAPI; the lease/commit tests use fake
HTTP and clocks. The redirect test uses **real Node fetch and loopback HTTP**,
not forged. These tests are **not** proof of an actual interactive Pi session or
real Kata/forged integration. The coordinator's real-server E2E must verify
that separately.
