# Collab Forge

Local-first collaboration forge, bootstrapped with an embedded, patched
[`kata.Service`](https://github.com/kenn-io/kata). Kata owns issues, comments,
links, events, evidence, and leases; Forge does not create an Issue database.

## Run

Requires Go 1.27, macOS or Linux, and the patched Kata checkout at `./kata`
(a separate ignored repository). Use a local filesystem supporting `flock`.

```sh
go build -o bin/forged ./cmd/forged
./bin/forged serve --data-dir .forge --project forge --listen 127.0.0.1:7347
```

Defaults are shown above. `--listen` accepts only a loopback IP **literal** and
numeric port, including `[::1]:7347`; wildcard addresses and hostnames are
rejected. This bootstrap is not a remotely accessible deployment.

On first startup, the CLI generates a cryptographically random supervisor
credential at `.forge/admin-token` (0600). It never prints the token. Alternatively,
pass `--admin-token-file /private/path/token`; an explicit file must already
exist, be a regular 0600 file, and contain at least 32 printable ASCII characters
without whitespace (one final newline is allowed). Protect this credential:
it grants Human supervisor authority, not a worker capability.

The embedded API `forge.New(Config)` always requires a nonempty `AdminToken`;
it never falls back to unauthenticated mode or generates credentials itself.

## HTTP

- `GET /health` is public and returns only `{"status":"ok"}`.
- Every other request requires `Authorization: Bearer <admin-token>`.
- Authenticated `/api/v1/...` requests use Kata's existing HTTP contract.
  Forge assigns subject `human:supervisor` and actor `Human`, ignoring client
  actor fields for identity.
- Native project/token/federation/integration administration is disabled by
  Kata's restricted embedding profile. Forge mounts no native UI.

```sh
curl --fail http://127.0.0.1:7347/health
TOKEN="$(tr -d '\r\n' < .forge/admin-token)"
curl --fail -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:7347/api/v1/projects
# Use the project's numeric ID from the response:
curl --fail -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"title":"First issue","actor":"Human"}' \
  http://127.0.0.1:7347/api/v1/projects/PROJECT_ID/issues
# The create response supplies short_id for GET .../issues/SHORT_ID.
unset TOKEN
```

## State and lifecycle

The data directory is 0700; Forge-owned files are 0600. `config.json` persists
one stable project ULID and name, while `kata.db` is exclusively Kata-owned.
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
