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
