# Code Issue checkout contract

Code work starts from exactly one explicit input: a resolved commit, a quiescent
`--dirty` source repository, or an existing immutable snapshot. Dirty capture
preserves HEAD, index contents, and working bytes separately, including ordinary
untracked files, deletions, binary data, executable bits and symbolic links.
Ignored files, timestamps, owners and extended attributes are excluded. Symlink
targets outside the repository are not copied. Source HEAD, index and files are
never changed; capture uses optional-lock-free reads and a separate object store.

The P0 format refuses submodules, nested repositories, unresolved merge stages,
intent-to-add, assume-unchanged and skip-worktree entries. Known credential file
names and private-key markers cause refusal without echoing content. This is a
conservative detector, not proof that arbitrary source contains no secrets.
Keep snapshots in a private local directory; no snapshot bytes enter Issue logs.
Limits: 32 MiB per captured file and 256 MiB per manifest/bundle. Full base history
is included in the bundle and must be treated as private repository content.

A SHA-256-addressed manifest binds the base bundle and both content layers.
Capture compares two full scans; concurrent changes cause refusal. This assumes
quiescent input and does not claim an atomic filesystem snapshot. The source
repository may be deleted after publication; the snapshot remains restorable.

Restore creates a new directory containing `repository.git` and a linked
`worktree`, on a new execution branch. No checkout filters or hooks run while
restoring bytes. Restored index and working content are verified before use.
Existing destinations are never overwritten. Corrupt or unsupported snapshots
fail closed. Partial execution directories are retained for inspection, never
automatically cleaned. Snapshot files are flushed before atomic publication.

A snapshot contains no lease, token or acquire attempt. Every new runtime must
claim a new execution before launching an Agent. Restoration only restores code.
The starting snapshot is not a checkpoint of later Agent changes: preserve the
old worktree and explicitly capture it again when recovering later progress.
Worktrees do not sandbox network, external effects, arbitrary paths or same-user
credentials. There is no new Issue database or Kata private SQL access.

## Development evidence

Snapshot tests first failed with missing Capture/Restore APIs. They then passed
with separate staged/working versions, source-index byte preservation, removal
of the source repository, corruption refusal, special-index refusal, nested
repositories, path shape changes, unusual filenames and injected concurrent
source modification. Tests create isolated temporary Git repositories only.

## CLI

Build with `go build -o bin/forged ./cmd/forged`. Start the authenticated loopback
service as documented in README, then launch a CLI-aware, noninteractive Agent command:

```sh
bin/forged checkout --url http://127.0.0.1:7347 \
  --token-file /private/forge/worker-token \
  --source /work/my-repo --dirty \
  --store /private/forge-snapshots --dest /work/execution-001 \
  ISSUE_REF -- AGENT_COMMAND ARGUMENTS
```

All flags precede ISSUE_REF. `--dirty` explicitly selects the entire repository's
current dirty state. Alternatively use `--ref COMMIT --store STORE`, or
`--snapshot /private/forge-snapshots/SHA256` to recover the same immutable starting
state into a NEW `--dest`. A restored worktree is `DEST/worktree`; its private
object store is `DEST/repository.git`. Destination parents must already exist.
Artifacts and worktrees stay on disk after success, failure and cancellation.
There is no automatic destructive garbage collection.

The parent CLI claims first, automatically renews (TTL default 300 seconds),
publishes a `preparing` workspace observation after capture, restores and verifies
content, publishes `ready`, confirms the lease again, and starts the command with
cwd set to the worktree. It releases best-effort on exit. An Agent exit code of
zero does NOT close the Issue. Failed stages append a bounded mechanical failure
observation when the service is reachable. No snapshot content is uploaded.

The child receives `FORGE_CLI`, `FORGE_EXECUTION_SOCKET` and `FORGE_ISSUE`, but no
worker token, execution proof or attempt. These environment variables describe a
live parent runtime, not persistent authority. The Agent must use this runtime
for execution operations; do not also load the independent Forge Pi extension or
start another claim-owning client against the same Issue.

Agent integration commands (call `guard` before editing or launching a mutation):

```sh
"$FORGE_CLI" execution guard
"$FORGE_CLI" execution issue_get
"$FORGE_CLI" execution issue_comment '{"body":"Plan or finding"}'
"$FORGE_CLI" execution issue_close '{"reason":"done","message":"Detailed completion result with sufficient substance.","evidence":[{"type":"test","command":"go test ./..."}]}'
"$FORGE_CLI" execution retry
```

`execution TOOL -` accepts JSON on stdin. `state`, Issue list/get/timeline/graph,
create/comment/link are also supported. Issue-scoped calls default to the claimed
Issue. Close is always bound to that Issue and uses a stable private retry key.
After uncertain close, only `retry` resubmits the retained original request; work
is blocked. The socket does not expose claim, workspace registration, credentials,
arbitrary native API routes, or supervisor operations. It lives in a private
0700 temporary directory with a 0600 Unix socket and disappears on parent exit.

Lease confirmation failure stops the child process group best-effort. Parent
SIGKILL cannot guarantee child termination: TTL fences server completion, and
Agent-side `guard` fails once its parent socket disappears. This remains trusted
same-user collaboration, not a malicious-process sandbox. Socket possession lets
same-user callers act through that live runtime; the Agent command must implement
this CLI contract. This does not transparently retrofit arbitrary Agents or Pi
extensions which own an independent execution lifecycle.

Workspace observations are fixed-format Kata comments, requiring a signed current
execution credential at the server. The server derives execution/claim attribution
and idempotency keys. They record a lease observation, not an atomic lease+comment
transaction; live authority always comes from Kata's lease. Like other trusted
local client inputs, snapshot metadata is not an independently attested filesystem
measurement. No migrations, scheduler, PR/Change domain or private SQL were added.

Runtime tests began with missing APIs and a 404 for registration. A malformed mock
renew response then correctly blocked close; the fixture was corrected to return
an actual lease. Real HTTP/SQLite plus Agent subprocess tests cover dirty checkout,
source preservation, mechanical registration and exact close/release. Cancellation
retains the worktree; a new runtime restores the snapshot and claims anew. Separate
network tests cover acquire identity reuse, ambiguous close retry, loss fencing,
redaction and redirect refusal. Automatic test-output capture remains future work;
close evidence above is still an Agent statement under the existing Kata contract.

The launcher currently targets noninteractive/headless commands. Interactive TUI
terminal foreground/PTY integration and automatic loading of Agent adapters are
not part of this checkout contract. A separate real-binary acceptance run also
passed `forged serve` + `checkout` + `execution` with a shell Agent, staged/dirty
assertions, preparing/ready timeline records and guarded close/release. Its local
summary is `/tmp/forge-checkout-e2e.log` (temporary; integration tests are durable).
