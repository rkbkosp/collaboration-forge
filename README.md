# Collab Forge

Local-first collaboration forge, bootstrapped with an embedded, patched
[`kata.Service`](https://github.com/kenn-io/kata). Kata owns issues, comments,
links, events, evidence, and leases; Forge does not create an Issue database.

## Run

Requires Go 1.27, macOS or Linux, and a local filesystem supporting `flock`.
Kata is tracked as a fixed patched submodule at `./kata`.

```sh
git submodule update --init --checkout kata
./scripts/bootstrap-kata.sh  # applies the pinned, independently committed patches
go build -o bin/forged ./cmd/forged
./bin/forged serve --data-dir .forge --project forge --listen 127.0.0.1:7347
```

The `kata` submodule is pinned to the public fork commit
`rkbkosp/kata@b67a69678c69b00594dde45c2ffd4c2539a35b6b`, based on
`kenn-io/kata@b1667be5603a308f8c2d44a0d6cbac2463a7eeb2`. The bootstrap script
verifies that the submodule is clean and matches the patched tree. The
versioned patch series remains available for independent reconstruction and
upstream review.

Defaults are shown above. `--listen` accepts only a loopback IP **literal** and
numeric port, including `[::1]:7347`; wildcard addresses and hostnames are
rejected. This bootstrap is not a remotely accessible deployment.

On first startup, the CLI generates a cryptographically random supervisor
credential at `.forge/admin-token` and a distinct worker credential at
`.forge/worker-token` (both 0600). It never prints the token. Alternatively,
pass `--admin-token-file /private/path/token`; an explicit file must already
exist, be a regular 0600 file, and contain at least 32 printable ASCII characters
without whitespace (one final newline is allowed). Protect this credential:
it grants Human supervisor authority, not a worker capability.

The embedded API `forge.New(Config)` always requires a nonempty `AdminToken`;
it never falls back to unauthenticated mode or generates credentials itself.

## HTTP

- `GET /health` is public and returns only `{"status":"ok"}`.
- Every other request requires an authenticated Bearer credential.
- `GET /forge/v1/project` discovers the configured project and close protocol.
- Workers can use only `POST /forge/v1/tools/{operation}`; raw Kata routes are
  denied even if a worker supplies actor/role/protocol headers. See
  [worker API](docs/worker-api.md). Never give a worker the admin credential.
- Supervisor-authenticated `/api/v1/...` requests use Kata's existing HTTP contract.
  Forge assigns subject `human:supervisor` and actor `Human`, ignoring client
  actor fields for identity.
- Native project/token/federation/integration administration is disabled by
  Kata's restricted embedding profile. Forge mounts no native UI.

```sh
curl --fail http://127.0.0.1:7347/health
# Supervisor terminal only; pass the credential over stdin, not argv/history.
forge_auth() { printf 'Authorization: Bearer '; tr -d '\r\n' < .forge/admin-token; printf '\n'; }
forge_auth | curl --fail --header @- \
  http://127.0.0.1:7347/api/v1/projects
# Use the project's numeric ID from the response:
forge_auth | curl --fail --header @- -H 'Content-Type: application/json' \
  -d '{"title":"First issue","actor":"Human"}' \
  http://127.0.0.1:7347/api/v1/projects/PROJECT_ID/issues
# The create response supplies short_id for GET .../issues/SHORT_ID.
unset -f forge_auth
```

## Independent client CLI and Skill

The `forge` client runs without a Pi process (Node >=22.19, macOS/Linux):

```sh
npm ci
npm link   # optional; alternatively: node scripts/forge.mjs ...
forge --help
export FORGE_URL=http://127.0.0.1:7347
export FORGE_WORKER_TOKEN_FILE="$PWD/.forge/worker-token"
forge issue list --status open
forge skill install --target "$HOME/.agents/skills/collab-forge-client"
```

For execution, start a dedicated foreground runtime in one terminal:

```sh
mkdir -m 700 /tmp/my-forge-worker
forge session start --socket /tmp/my-forge-worker/s.sock
```

In that worker's command environment:

```sh
export FORGE_SOCKET=/tmp/my-forge-worker/s.sock
forge session wait
forge issue claim ISSUE_REF --purpose 'Implement the agreed acceptance criteria'
forge session guard          # must allow before using editing/execution tools
# ...perform and verify the work...
forge issue close ISSUE_REF --data-file truthful-close.json
# If uncertain: forge session retry  (never replace the original request)
forge session stop
```

Each independent worker needs its own fresh directory/socket. The runtime owns
heartbeat and private proofs/retry snapshots in memory; short-lived CLI calls do
not restore an execution from disk. Contributions remain available without a
lease. All worker operations, paginated timeline, explicit Human administration,
JSON/file inputs, exit codes, safety boundaries and recovery are documented in
[the client Skill reference](skills/collab-forge-client/references/cli.md).
`forge admin ...` requires a separate supervisor credential; it never falls back
to the worker token. See [client acceptance](docs/client-acceptance.md).

## Codex adapter

Use `forge codex` to launch Codex with a fresh runtime instance. Standard
`forge issue` commands then use daemon-owned execution and automatic renewal;
no background session broker is needed. Install/trust the command hooks for
session/subagent context, compaction recovery, bounded Stop checks and cleanup.
See [Codex setup, lifecycle and retry semantics](docs/codex.md).

## Pi extension

Use Pi 0.85.1 and Node >=22.19:

```sh
npm ci
export FORGE_URL=http://127.0.0.1:7347
export FORGE_WORKER_TOKEN_FILE="$PWD/.forge/worker-token"
pi -e ./extensions/forge.ts
```

Claim an issue before editing. Heartbeat is automatic; other sessions can still
comment, create issues and add links. New runtimes never restore an old execution
from Pi history. Unknown/lost lease state blocks stock editing tools. See
[extension setup and lifecycle](extensions/README.md), including the limits of
cooperative filesystem preflight and exact retry rules after an uncertain close.
This same-account example is for trusted local use, **not an agent sandbox**:
0600 does not stop another process under the same UID from reading token files.
Keep supervisor credentials outside the worker's filesystem access if workers
are untrusted; server role separation cannot compensate for a stolen admin token.

## Human timeline

```sh
./bin/forged timeline ISSUE_UID
# Optional: --url http://127.0.0.1:7347 --token-file /private/admin-token
```

The terminal view first shows current execution authority, its server confirmation
time and lease expiry. Use `--details` for the exact holder and ClaimUID, and
rerun to refresh. This is a timestamped observation, not proof the Agent is active
or permission to start work. Failed reads show unknown rather than unclaimed.
The heading and paginated history are read separately.

The terminal view reads Kata events: actor, comment, execution acquire,
links/dependencies, close reason and typed evidence, release/expiry. It does not
create a second audit store. Pagination scans project events while filtering the
issue and incoming relations. Purged history and page-limit truncation are
explicit; use the reported `--after-id` cursor to continue. Evidence is what was
submitted, not an independent attestation that a command succeeded.

## State and lifecycle

The data directory is 0700; Forge-owned files are 0600. `config.json` persists
one stable project ULID and name, while `kata.db` is exclusively Kata-owned.
`execution-key` signs execution credentials and must be included in backups;
losing it invalidates existing proofs, not the underlying timed leases.
Keep the entire directory together when backing up **after stopping forged**.
Changing `--project` for an existing directory is refused rather than silently
creating or renaming a project. Missing configuration beside an existing database
also fails closed. Use a private, trusted parent directory; do not share the
state directory with untrusted local users or other SQLite writers.

A process-held `forge.lock` prevents a second Forge Store from opening the same
directory. Do not delete the lock file to bypass it. Kata's background `Run`
starts automatically, including timed-lease sweeping. The host monitors worker
and listener failures. SIGINT/SIGTERM triggers bounded HTTP shutdown, cancellation
and joining of workers, then Kata close and lock release. Embedded hosts can use
`Server.Wait()` to observe worker failures and must call the idempotent `Close()`.

```sh
go test ./...
go test -race ./internal/forge ./cmd/forged
go build ./...
```

These commands test/build the root module, not Kata's full test suite.

```sh
npm ci && npm test && npm run typecheck
npm run test:e2e             # real forged/Pi SDK; includes a real ~60s expiry
./scripts/test-bootstrap-kata.sh
./scripts/test-kata.sh       # full Kata suite; requires Docker + Bun
```

See [acceptance evidence and production boundaries](docs/production-readiness.md).
The current target is supervised, trusted local use—not unrestricted production.

## License

Collab Forge is released under the [MIT License](LICENSE). The `kata/`
submodule and its public fork remain under Kata's upstream MIT license.
