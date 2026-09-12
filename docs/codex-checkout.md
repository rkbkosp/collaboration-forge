# Optional claimed-Issue worktrees

Claim owns execution authority. Checkout owns a filesystem artifact attached to
that **same** authority. Claim does not create a worktree, change branches or
change Codex's working directory. The ordinary in-place workflow remains valid.
There is no new claim, execution broker or renewer when running `forge checkout`.

Codex uses its daemon-managed runtime. CLI uses the existing session broker;
Pi uses its existing Controller through the `checkout`, `checkout_status`,
`checkout_list` and `checkout_archive` tools. All three share the same artifact
store and server-side execution proof checks.

For an ordinary CLI session, claim first using that session's private socket,
then invoke `forge checkout ISSUE --dirty --source REPO --socket SOCKET` (or set
`FORGE_SOCKET`). List/status/archive may also run with the project worker
configuration without a broker. An ambiguous CLI create is resolved with
`forge session retry --socket SOCKET`; Pi repeats the identical checkout tool
arguments. The original private key/proof remain in that runtime's memory.
Do not start a broker inside Pi or Codex to run checkout.

When a host wrapper routes by Git root or refuses socket overrides, use the
absolute installed entrypoint, `node /installed/forge/scripts/forge.mjs`, with
the same explicit socket inside managed CLI worktrees. Pi continues using its
registered tools and fixed project configuration; changing a shell cwd does
not change its Controller. Codex's managed-directory shim is described below.

## Setup after deployment

Deploy compatible forged and CLI versions together. Do not point a running
instance at a replacement daemon or restore credentials from old conversations.
Worktree location is chosen for each source repository in this order:

1. Explicit `project.worktree_root` from the host TOML configuration (or
   `--worktree-root`, which overrides it).
2. A per-user Forge root on the source filesystem:
   `VOLUME_ROOT/.forge-worktrees-OS_UID`. The daemon detects the volume from
   filesystem device IDs, resolves symlinks, and probes directory creation.
3. If the same-volume root cannot be used, macOS falls back to
   `~/Library/Application Support/Forge/worktrees` (the user configuration
   directory's `Forge/worktrees` on other supported systems).

```toml
# /private/forge.toml
[project]
worktree_root = "/Volumes/SuperDisk/forge-worktrees"
```

```sh
forged serve --data-dir /private/fornax-data --project fornax \
  --listen 127.0.0.1:7349 --config /private/forge.toml
# Equivalent root override: --worktree-root /Volumes/SuperDisk/forge-worktrees
```

Explicit roots must be absolute and outside source repositories. An unusable
explicit root fails checkout with `worktree_root_unavailable`; it does not
silently disregard the user's choice. Snapshot capture also rejects a store
inside the source repository. Unknown configuration fields fail startup.

Workspace records remain at `DATA_DIR/workspaces/PROJECT_UID/records` by default.
The older `--workspace-root` option controls this record location; use the new
`--worktree-root` option for checkout files. Retain the old record-root setting
when upgrading an existing service. Existing long paths remain readable and
recoverable without migration, movement, or deletion; a restart orphans their
execution state as before. Root configuration changes apply to new checkouts.

The launcher installs a temporary `forge` shim on the child PATH. It matches cwd
against this project's recorded worktree locations, including subdirectories,
and invokes the current CLI with the instance's existing environment. A path's
spelling or directory hierarchy does not establish project identity. Outside
registered worktrees it delegates to the original host CLI, preserving repository
routing. `FORGE_CODEX_CLI` is the absolute shim path for shells whose login
configuration replaces PATH. This shim is convenience: the server still validates
worker, instance and exact lease.
No globally installed wrapper or running service is changed by the source code.

## Original directory or isolated directory

```sh
forge issue claim ISSUE
# Option A: work directly in the original checkout.

# Option B: choose exactly one explicit snapshot source kind.
forge checkout ISSUE --ref COMMIT --source /path/to/repository
forge checkout ISSUE --dirty --source /path/to/repository
```

`--source` defaults to the current directory. `--dirty` captures HEAD, index,
working bytes and ordinary untracked files; ignored files are excluded. Capture
rescans the source and rejects changes during capture. Stop source writers first.
`--ref` captures the resolved commit and excludes working-copy changes.
Initialized direct submodules are captured at the indexed/selected commit with
their own offline bundles. Dirty mode rejects dirty or unpinned child worktrees;
commit mode excludes their working edits. Remote submodule URLs are never fetched.
Existing snapshot limits still apply: nested submodules/repositories, unresolved
index conflicts, unsupported entries and recognized credential material are
rejected. This does not copy dependency installations, local secrets or ignored
build output; initialize dependencies in the new worktree as appropriate.

The response includes `workspace_id`, `project_uid`, `issue_uid`, `tenure`,
`repository_id`, `source_kind`, `base_commit`, `snapshot_id`, `worktree`, `state`.
Tenure here is a non-authorizing hash used only to group filesystem artifacts;
no execution token, private acquire attempt or instance capability is persisted
in workspace records or returned by checkout.

The CLI waits for completion by default; `--no-wait` returns the preparing job.
A wait timeout returns exit 3 with `wait_timed_out`, the known workspace ID and
state: poll status, do not issue another create to guess the outcome.

```sh
forge checkout list ISSUE
forge checkout status WORKSPACE_ID
```

Automatic network retries reuse the original logical request ID. A replay returns
the same workspace ID; read its status for the current state. A new explicit
checkout invocation is a new job. Four preparations may run concurrently per
service; record capacity is 4096 and preparation is bounded to five minutes.

A subprocess cannot change Codex's own cwd. Subsequent shell, edit and test tools
must explicitly use the returned `worktree`; use absolute paths for edits where
a tool has no cwd. Context hooks show up to 16 recent unarchived workspaces owned
by this runtime; `forge checkout list` returns the project artifact inventory.
Filesystem tools are cooperative: a lease does not fence arbitrary file writes.

## Multi-repository Issues

The same claimed Issue can have multiple checkouts. Run checkout once for each
repository, independently choosing a commit or dirty source. All use the same
Kata tenure, but each has a different workspace ID and directory:

```text
RECORD_ROOT/PROJECT_UID/records/WORKSPACE_ID.json

SELECTED_WORKTREE_ROOT/
  .snapshots/SNAPSHOT_ID/
  WORKSPACE_ID/
    repository.git/
    worktree/
```

The workspace UUID is an opaque location key. Project, Issue, tenure, repository,
and runtime relationships are record fields, never encoded in the directory
hierarchy. Multiple projects may share a worktree root; their record stores and
credentials remain separate. The returned `worktree` path is the tool cwd.

Snapshot capture preserves source content without changing its index, branch or
worktree. Repository IDs describe local Git common directories, not inferred
remote ownership. Preparation/ready observations are recorded through Kata's
existing typed workspace API with the live execution proof. Local records track
filesystem lifecycle only; they are not an Issue store or permission authority.

## Completion, archive and recovery

After verifying the result, close normally using truthful evidence:

```sh
forge issue close ISSUE --data-file truthful-close.json
```

Close is refused while an attached checkout is still preparing. A confirmed
Codex, CLI or Pi facade close automatically marks its attached workspaces `archived`,
including multi-repository work. Archive updates metadata **in place**: no move,
no deletion, no branch removal and no automatic merge. It does not assert that
all file changes were committed or that they passed tests.

The close response separately includes `workspace_archive`. If archive writes
fail, close still succeeds with `workspace_archive.state: pending`. Retry only
archive; never reopen or repeat a new close to repair filesystem bookkeeping:

```sh
forge checkout archive WORKSPACE_ID
```

Manual archive requires the authoritative Issue to be closed. It also supports
archiving retained records after daemon restart or an external Human close. Disk
failure during a close/crash may leave a record orphaned rather than archived;
check the authoritative Issue and run this command to reconcile it.

Codex pause/interrupt marks ready workspaces paused. Release and daemon restart
leave orphaned artifacts, preserving files. CLI/Pi crashes rely on lease TTL;
retained `ready` metadata only describes preparation, never current authority.
Always check the live issue before continuing work. A new
runtime never regains authority merely by reading a retained record. Reclaim the
Issue, then explicitly recover into a **new** worktree:

```sh
forge issue claim ISSUE
forge checkout ISSUE --recover OLD_WORKSPACE_ID --dirty
```

Recovery can also select `--ref COMMIT` from the retained repository. Old and new
files remain separate; no in-place authority transfer is implemented. Records
from another project's store or another Issue cannot be recovered this way.

The legacy `forged checkout ... -- AGENT` still owns an independent execution
runtime. It is not the command above and must not be layered onto a Codex claim.

## Host routing for manually created Git worktrees

A host wrapper that selects a project by cwd prefix must also resolve ordinary
`git worktree add` directories. The packaged `scripts/forge-project-root.mjs`
accepts the host's registered repository roots and prints the matching canonical
root, or exits 2 without output. It compares canonical Git common directories;
linked worktrees, their subdirectories and symlink paths share the registration.
An unrelated clone or nested repository does not inherit the enclosing project's
binding. Inherited `GIT_*` overrides are ignored for this lookup.

Integrate it **before** the host wrapper's project selection and existing
environment/argument conflict checks, for example:

```sh
forge_workspace=$("$node" /installed/forge/scripts/forge-project-root.mjs \
  /registered/project-a /registered/project-b) || fail
# Select the endpoint and credential file paths from forge_workspace.
# Keep the existing checks rejecting conflicting endpoint, token and socket flags.
# exec the installed Forge CLI WITHOUT cd: checkout --source defaults to real cwd.
```

Keep the registration list in the host wrapper, not a worker-controlled environment
variable. This helper reads Git metadata only, never credentials. It does not
create or restore execution authority. A manually created worktree retains the
current runtime only when its shell inherits that runtime's environment; a new
runtime still needs its own launch and claim. The existing launcher shim handles
Forge-managed snapshot repositories, which have their own Git common directories,
and delegates ordinary paths to this host router. Shells that replace PATH can
use `$FORGE_CODEX_CLI` explicitly.

Installing the helper and updating a host wrapper does not activate a new daemon
or the claimed-checkout feature. Those still use the installed runtime version.
