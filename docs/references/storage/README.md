# Storage

How a node writes bytes that must survive a crash or a power loss, what the
test file system can and cannot prove about that, and what each platform is
believed to do. The code is under
[`internal/node/fsx`](../../../internal/node/fsx/README.md) (the file system
interface and the durable write sequences) and
[`internal/node/fsx/fsxtest`](../../../internal/node/fsx/fsxtest/README.md)
(the crashable in-memory file system and the fault injector) and
[`internal/node/txn`](../../../internal/node/txn/README.md) (the transaction
layer every persistent write goes through),
[`internal/node/recfile`](../../../internal/node/recfile/README.md) (the framing
of a record file) and [`internal/node/store`](../../../internal/node/store/README.md)
(the records of `state/`).

Platform paragraphs that say "not exercised by CI" describe behaviour that no CI
job can observe, for the reason given where they say it (for example, no job loses
power, so what a platform makes durable at a power loss is never observed). What a
CI job does observe is stated as what the GitHub-hosted runner showed.

## What

### One narrow file system

Every persistent byte goes through `fsx.FS`. A method of `FS` is one
system-call-sized step: create a temporary file, write, sync, rename, sync a
directory, remove, create a directory, rename a directory. The sequences that
make a write durable are functions over any `FS`, not methods of it:

| Operation | Steps, in order | Used for |
| --------- | --------------- | -------- |
| `Replace` | `create`, `write`, `sync`, `rename`, `sync-dir` | every single-file replacement: records, configuration, keys, manifests |
| `ReplaceDeferred` | `create`, `write`, `sync`, `rename`; no `sync-dir` | several files of one directory, made durable together by one `SyncDir` after the last of them |
| `OpenAppend`, `Append` | `open` once, then `write` per call; no sync | appended event and digest files |
| `SyncFile` | `sync` (on the handle that appended), `sync-dir` | making a batch of appends durable |
| `RenameFile` | `rename`, `sync-dir` | no transaction of the store declares it |
| `Remove` | `remove` | one deletion; a batch of them is followed by one `SyncDir` |
| `SyncDir` | `sync-dir` | after a batch of removals |
| `MakeDir` | `mkdir`, `sync-dir` (the parent) | layout and staging directories |
| `RenameDir` | `rename-dir`, `sync-dir` (the parent) | a staged directory appearing whole |
| `RemoveAll` | `remove-all` | only a directory already renamed to a garbage name |
| `Discard` | `rename-dir` to `.trash-<hex>`, `sync-dir`, `remove-all` | deleting a directory tree; if the removal fails the directory is already gone from its name and startup removes the leftover |

Paths are relative to the data directory root, separated by `/`, and valid in
the sense of `io/fs.ValidPath` (no `..`, no leading `/`, no empty segment); `.`
is the root. No element may hold a backslash, a colon or NUL, on any platform:
on Windows a backslash separates elements and a colon names a drive or an
alternate data stream, so `a\b`, `..\x` or `a:b` would escape the root or name
another file there while being a single innocent element elsewhere. Refusing
them everywhere keeps the node's behaviour the same on every platform; the real
file system additionally maps each path through `filepath.Localize`, the
platform's own check of what is local, and refuses what it refuses. Nothing can
be written outside the root. Renames stay inside one
directory. Files have one of two permissions, 0600 or 0700 for adapter
executables; directories are 0700.

`Info.OwnerOnly`, which callers use to refuse a secret that others could reach,
is true only for a regular file or a directory whose permissions are the owner's.
`Stat` does not follow a final symbolic link and `ReadFile` does, so the bits of a
link (which macOS lets the owner set with `chmod -h`) say nothing about who can
read what it points to: a link, like a socket, a device or a Windows junction, is
never owner-only. Without this a link made 0600 pointing at a world-readable copy
of a private key would pass the check and be read. The tests are
`TestOSUnixOwnerOnlyIsFalseForASymbolicLink`,
`TestDarwinOwnerOnlyIsFalseForASymbolicLinkWithOwnerOnlyBits` and
`TestWindowsOwnerOnlyIsFalseForASymbolicLink`. On the GitHub-hosted
`windows-latest` runner (Windows Server 2025) the last one created a link, did not
skip, and `Stat` of the link returned `OwnerOnly` false and no error. Creating a
link needs a privilege that the runner's administrator account has; an account
without it skips that test.

There is no overwrite in place, no truncate, no rename across directories, no
replacing a directory, no chmod, and no long-held read handle. Each is left out
for a reason in the Why section.

### Steps, tags and crash points

Every write step carries a tag, a transaction name and a role. A step is named
`<tx>/<role>.<step>`, with the step from a fixed vocabulary (`create`, `open`,
`write`, `sync`, `rename`, `sync-dir`, `remove`, `remove-all`, `mkdir`,
`rename-dir`) and the commit of a transaction `<tx>/commit`. A crash point adds
the ordinal of the arrival, counted from 1 per key by an `fsx.Counter`:
`intake/out.rename#2`. Names are `[a-z][a-z0-9-]{0,31}`. `ParsePoint` and
`Point.String` are inverses, and `ParsePoint` accepts only the canonical text
(no sign, no leading zero in the ordinal).

`fsx.Observe` wraps an `FS`, numbers every write step including the `Write` and
`Sync` of files opened through it, and calls a hook before the step runs; a
hook error skips the step. Reads and locks are not steps. The point of an
observed failure travels in the error (`fsx.PointOf`).

Two transaction names are reserved for writes that are not crash points:
`log` (diagnostic logs, whose loss is harmless) and `cleanup` (the removal of
the temporary file of an operation that failed). `Observe` neither numbers nor
reports them and a fault rule matches them only by naming them. A crash before
or after a cleanup removal leaves a `.tmp-*` file that startup removes, the
same state as a crash before the failed step, so it needs no crash point. The
two names are not valid transaction or role names (`ValidName` refuses them, so
`ParseKey`, `ParsePoint`, `Key.Validate` and the transaction table do too): a
transaction that took one would have its steps silently skipped by observers.
The one writer under the `log` tag is `internal/node/logfile`, a handle that can
open, rotate and remove a log under `logs/` and has no other operation, so no state
file can be written under a tag that the crash tests skip; the daemon's own log and
those of the adapter instances go through it (see [daemon](../daemon/README.md)).
`Faulty` follows `Observe`: it does not number steps of reserved transactions.

### Failure classes

A failed composite operation returns an `*fsx.Error` whose class says what the
caller must do. The class is decided in one table (`classify.go`):

| Step that failed | Class | Reason |
| ---------------- | ----- | ------ |
| `create`, `write`, `rename`, `remove`, `remove-all`, `mkdir`, `rename-dir`, `open` | Failed | the call did not complete, so nothing durable changed; the caller rejects the request or applies backpressure. A failed append can leave a partial record that later appends follow, which is why appended formats delimit and checksum their records and a reader must resynchronise after a damaged record anywhere in the file, not only at the tail |
| `sync` of the temporary file of a `Replace`, before its rename | Failed | the target is untouched and nothing was acknowledged; the temporary file is removed. Treating this as fatal would turn a persistent device error into a restart loop |
| any `sync-dir` | Fatal | whether the rename or creation is durable is unknown, and memory may disagree with disk. After a failed write-back Linux marks the pages clean, so a later `fsync` can report success for data that is lost; the only safe answer is to exit and rescan |
| `sync` of an appended file (`SyncFile`) | Fatal | the appended bytes are visible to this process but whether they are on disk is unknown. A later successful sync must not let the caller believe the whole batch is durable, for example by deleting what the batch stood for |

A call refused before any step ran (an invalid path, a missing directory to
look in) is Failed. `fsx.IsFatal` finds a Fatal error through any wrapping. The
process exit itself is not in `fsx`: it returns the class and the composition
root exits.

**A full volume.** `fsx.IsNoSpace` is the one place that says whether an error
means the volume or the quota is full, so that a caller that tells "storage full"
from "storage unavailable" does not test operating system error codes itself: the
codes differ by platform, and on Windows `syscall.ENOSPC` is a value Go invents
that `Errno.Is` does not map from `ERROR_DISK_FULL` or `ERROR_HANDLE_DISK_FULL`, so
`errors.Is(err, syscall.ENOSPC)` is wrong there. It is true for `ENOSPC` and
`EDQUOT` on Unix and for `ERROR_DISK_FULL`, `ERROR_HANDLE_DISK_FULL` and
`ERROR_DISK_QUOTA_EXCEEDED` (1295, documented as "the requested file operation
failed because the storage quota was exceeded") on Windows, through any wrapping,
and for nothing else: the production code recognises only what the platform
returns. The fault a test hands to `fsxtest.Faulty` for a full disk is
`fsxtest.DiskFull`, `ENOSPC` on Unix and `ERROR_DISK_FULL` on Windows, so such a
test means the same on both. It does not change the class of the failure: a write
that failed for want of space is an ordinary `Failed`
operation. The Windows codes are pending verification by CI on main.

A multi-file transaction fails Fatal for any failure after the commit step of
its first non-preparatory write has completed; that rule belongs to the
transaction layer, not to `fsx` (see The transaction layer).

### Appending and syncing

An appended file is synced through the same handle that appended it, in the
same operation. Linux reports a write-back error only to descriptors that were
open when the error happened, so closing the file and reopening it to `fsync`
can miss the error and succeed. Under the instance lock there is one writer, so
`OpenAppend` positions at the end and appends there.

The file's `Write` and `Sync` steps are attributed to the tag the file was opened
with, and `File` has no way to change it. The tag given to `Append` and
`SyncFile` must therefore be the tag the file was opened with; the transaction
layer guarantees they agree, and its declaration of crash points lists the
`sync` and `sync-dir` of an appended file under the appending role.

### Leftovers

Temporary files are `.tmp-<hex>` in the directory of their target, created
exclusively with 8 random bytes from an injected reader (`fsxtest.Mem` uses a
counter, so clones reproduce). Discarded directories are `.trash-<hex>`; the
number is the smallest unused one so that a leftover from an earlier crash is
never reused. `fsx.Leftovers` lists both kinds under a directory, without
descending into a leftover directory, so a startup step can remove them through
its own tagged operations.

### The instance lock

`FS.Lock` takes an exclusive, non-blocking lock on a lock file, creating it if
absent, and returns `fsx.ErrLocked` at once if it is held. The kernel releases
the lock when the process dies, so there is nothing to recover. Unix uses
`flock(LOCK_EX|LOCK_NB)`, which belongs to the open file description: a second
`Lock` in the same process conflicts like one from another process. Windows uses
`LockFileEx` on the first byte. The data directory must be on a local file
system: on NFS `flock` is emulated with `fcntl` byte-range locks.

### Platform behaviour

The Windows column of this table and the Windows paragraphs after it are
exercised by the Windows tests listed under Windows tests and by the contract
tests of every file system implementation (`TestOSFSConformsToTheFSContract`),
which pass in the Windows CI jobs. What Windows makes durable at a power loss is
not observable by a test, and the tests that log a result or skip without a
privilege are marked where they occur.

| Primitive | Linux | macOS | Windows |
| --------- | ----- | ----- | ------- |
| file `Sync` | `fsync(2)` | `F_FULLFSYNC` through Go's `File.Sync`; plain `fsync` only hands data to the drive, which may delay or reorder it | `FlushFileBuffers`, which needs a handle opened for writing; temporary files and append handles are |
| `SyncDir` | open read-only and `fsync` the directory | the same; that this makes a rename durable on APFS is not documented by Apple | no flush; a check that the directory exists |
| `Rename` | `rename(2)`, atomic over an existing target | the same | `MoveFileExW` with `MOVEFILE_REPLACE_EXISTING` and `MOVEFILE_WRITE_THROUGH`, retried for about two seconds on access denied, sharing violation and file not found |
| `RenameDir` | target checked absent, then `rename(2)` | the same | target checked absent, `MoveFileExW` with `MOVEFILE_WRITE_THROUGH` and without `REPLACE_EXISTING` |
| `ReadFile` | read and close | the same | read and close at once, retried like a rename, except file not found |
| `OpenAppend` | `O_WRONLY`, `O_APPEND` and `O_CREATE` at 0600 | the same | `CreateFile` with `GENERIC_WRITE`, positioned at the end |
| `Available` | `statfs`, `Bavail * Bsize` | the same | `GetDiskFreeSpaceExW`, the free bytes available to the caller |
| root creation | `mkdir` 0700 | the same | a directory with a protected DACL for the current user, inherited by what is created inside |

Specifically, for Windows:

- **Directory flush.** Windows does not flush directories in `fsx`. The
  documentation of `FlushFileBuffers` mentions files and volumes only and
  requires write access; other storage engines (Pebble, RocksDB, NATS) skip the
  flush on Windows and PostgreSQL ignores the error. A Windows test opens the
  directory with write access and with backup semantics, calls
  `FlushFileBuffers`, and logs the result. On the GitHub-hosted `windows-latest`
  runner (Windows Server 2025) `FlushFileBuffers` on a directory handle returned
  no error with `GENERIC_WRITE` and with `GENERIC_READ|GENERIC_WRITE`, and
  `Access is denied` with `GENERIC_READ`. That is the return value of the call
  and nothing more: the test cannot show that anything becomes durable, and no job
  loses power. Making `SyncDir` a real flush is a change inside the Windows file
  only. Today `SyncDir` persists nothing on Windows: it only checks that the
  directory exists. So the directory barriers of
  the layers above (the `SyncDir` after a group of `ReplaceDefer`, after a batch
  of removals and after a rename of a directory, and the settle of every start)
  make nothing durable there by themselves. The durability of directory entries
  on Windows rests on the ordering of NTFS metadata journaling and on
  `MOVEFILE_WRITE_THROUGH`, which no test in this repository establishes; not
  exercised by CI: no job loses power.
- **Rename durability.** The documentation of `MOVEFILE_WRITE_THROUGH` says the
  call does not return until the file is moved on disk, but its guarantee is
  spelled out only for a move done as copy and delete. The power-loss behaviour
  of a same-volume rename is therefore not established. Go's own `os.Rename`
  passes only `MOVEFILE_REPLACE_EXISTING`, which is why `fsx` calls
  `MoveFileExW` itself.
- **Open handles.** Go opens files without `FILE_SHARE_DELETE`, so a handle that
  stays open makes a rename over that file fail. `ReadFile` reads and closes at
  once, and a rename retries (1, 2, 4, ... milliseconds, two seconds in total)
  for the error set that the Go toolchain itself retries, which covers
  antivirus scanners and indexers briefly holding a file. The loop is modelled
  on Go's own (`cmd/internal/robustio`); this one doubles deterministically from
  one millisecond with no jitter and gives up after a budget of accumulated
  sleep, not a count of attempts. A read does not retry `file not found`, which
  Go's own read retry also leaves out: the file may genuinely not exist, and an
  absent file would otherwise cost the whole budget. The waits use a timer of
  `clock.System()`, the one use of a clock in `fsx`.
- **Append handles.** Go maps `O_APPEND` on Windows to a handle without
  `GENERIC_WRITE`, on which `FlushFileBuffers` fails; `fsx` opens the handle
  itself.
- **Instance lock.** `LockFileEx` locks are released by the system when the
  process ends, but the documentation says that "the time it takes ... depends
  upon available system resources", so a daemon restarted immediately after
  being killed may briefly see `ErrLocked`. `fsx` does not retry: for a real
  second daemon an immediate error is right. A caller that restarts a killed
  daemon retries a bounded number of times and logs how long it waited.
- **Permissions.** The data directory root is created with the security
  descriptor `D:P(A;OICI;FA;;;<current user SID>)`: protected, so it inherits
  nothing from its parent, with full control for the user running the process,
  inherited by files and directories created inside. `Info.OwnerOnly` fails
  closed. It is true only when the object's owner is the user running the
  process, Administrators or SYSTEM (an owner can always rewrite the DACL), the
  descriptor has a DACL, every access-allowed entry names the user running the
  process, SYSTEM or Administrators, and every other entry is of a type that can
  only deny access (the four deny types: plain, object, callback, callback
  object), which cannot widen access and is skipped. An entry of any other type,
  such as a callback or object allow entry, may grant access in a way the check
  does not evaluate and makes the answer false, as does failing to read the
  descriptor, so a key file is never assumed private.

On Linux, `flock` (`TestOSUnixLockConflictsAcrossInstancesAndIsReleasedOnUnlock`)
and `statfs` (`TestOSFSConformsToTheFSContract`) are exercised on the Linux CI
runner, and the `fsync` and directory `fsync` calls run in
`TestOSDurableOperationsEndToEnd`. That they make data and renames durable is
not exercised by CI, because no job loses power; the development machine is macOS.

### The transaction layer

`fsx` makes one write durable. A transaction is several writes that belong
together, and `txn` is the one entry point through which every persistent write
of `keys/`, `config/` and `state/` goes: it is the only holder of a writable
`FS`, and the packages above it get a `Writer`, a `Tx` per transaction, and a
`Reader` that has no write method. Logs, the instance lock and the creation of
the data directory root are not transactions and not crash points.

**Declarations and the table.** A transaction is declared as a literal: a name,
a kind (`Normal`, or `Recovery` for what only repairs a crash) and its writes in
order, each with a role, an operation, and the flags `Many` (may repeat),
`Optional` (may be skipped) and `Prepare` (what it leaves behind is garbage that
recovery removes). The composition root builds one table of every declaration
of the program, `NewTable`, and `Table.Keys()` is every crash point the program
can reach: every step of every write under its role (`intake/out.rename`), and
one `commit` per transaction. Nothing registers at run time, so the expected
set of points is known before anything runs. `NewTable` refuses duplicate or
invalid names (the reserved `log` and `cleanup` included), unknown operations and
kinds, a transaction without writes, a role used twice (except the role of an
`Append`, which one later `SyncFile` repeats), a `SyncFile` whose role is not an
earlier `Append`'s, a `ReplaceDefer` that is not declared immediately before a
`SyncDir`, and a `Prepare` write declared after a write that is not `Prepare`
(after the commit point the flag has no effect, so it would only mislead a
reader).

**Run time checks.** `Begin` panics for a declaration that is not in the table.
Each call of a `Tx` is checked against its declaration: the role, the operation
and the order (a `Many` write repeats, an `Optional` one is skipped, nothing is
made again after a later write, and `Commit` needs every required write; a `Tx` that made no write commits nothing:
`Commit` returns nil and reaches no point, so an owner that opened a transaction
with nothing to do does not leave a commit point of a recovery transaction on a
clean start). A
second open `Tx` of one declaration panics, because crash points are numbered per
key and two transactions advancing the same key would make the numbering depend
on scheduling. These are programming errors, so they panic, and they are found
the first time the code runs. The first failed write is sticky: every later call,
`Commit` included, returns it before any check and runs no step.

**The commit point.** Writes that are `Prepare` leave only garbage until the
first write that is not. The commit point is the completion of the commit step of
the first non-`Prepare` write that has one, the first step of it that readers can
see (`Tx.run` sets it at the first such completion, whatever the writes before it):

| Operation | Commit step |
| --------- | ----------- |
| `Replace`, `ReplaceDefer`, `RenameFile` | `rename` |
| `Remove` | `remove` |
| `MakeDir` | `mkdir` |
| `RenameDir`, `Discard` | `rename-dir` |
| `RemoveAll` | `remove-all` |
| `SyncFile` | `sync` |
| `SyncDir` (a write of its own) | `sync-dir` |
| `Append` | none |

A write without a commit step does not commit. The summaries of `archive-sweep`
are appended first, without a sync, and the sequence floors and removals after
them carry its commit point, so a failed append has the class `fsx` gave it (the
sweep deletes nothing and is done again). `archive-events` only appends, with no
sync that follows, so it has no commit point and a failure of it never becomes
fatal by this rule.

Before it, a failure has the class `fsx` gave it. From it on, every failure, in
the same operation or in a later write, is `Fatal`: the disk is in one of the
intermediate states that replay is designed for, while memory still shows the
state before the transaction, so continuing would write on a view that is wrong.
For a single replacement this is exactly the rule of `fsx.Replace`. The class
returned is the `txn.Error`'s `Class`; `txn.IsFatal` reads it, and also accepts
an `fsx` error.

A `Tx` closed after its commit point with no `Commit` and no recorded failure
panics. The failures of the layer are all fatal from there on and are recorded,
so reaching `Close` in that state means the owner stopped for a reason of its
own: a read of its own that failed, or an early return. The disk then holds part
of the transaction, memory shows none of it, and no error says so; an ordinary
error returned to the caller would be read as "nothing happened". The panic ends
the process and recovery runs, the outcome a fatal state calls for. A `Tx` that
already panicked on a call of its own adds no second panic, so the first message
is the one seen. The rule that keeps owners from meeting it is that every read an
owner needs is done before `Begin`, and so is every validation and encoding. A
package whose files are written inside a transaction another package opens
therefore offers two steps: a prepare step that validates and encodes, with no
transaction, returning a prepared value or an ordinary error (`store.PrepareEndpoint`
and `PrepareAdminResult`, `config.PrepareBackup`, `PrepareRevocation` and
`PlanInstall`, `keys.PrepareClockCleared`; `config.RuntimeID` and `DirectID` encode
the ids they mint), and a put step on that value that can fail only as a write of
the transaction does. A validation inside the transaction fails after an earlier
write of it passed the commit point, and the composer, which did what the
documents say and returned the first error, is met by this panic.

**Removals are synced before the commit.** `Remove` and `RemoveAll` unbind a
name and make nothing durable; the directory sync that follows a batch does. A
power loss after `Commit` could bring a removed name back after the caller
acknowledged the removal, and with it whatever the owner deleted because the name
was gone. So the `Tx` remembers every directory in which it removed a name and
forgets it at a successful `SyncDir` of that directory, and `Commit` panics,
naming the directories, when any is left: a programming error found the first
time it runs, like a deferred group left unsynced. A removal that failed unbinds
nothing and is not remembered; the error is what `Commit` returns.

**Deferred groups.** `ReplaceDefer` is `Replace` without the directory sync, so
that k files of one directory cost k file syncs and one directory sync. The files
of a group are in one directory and the next write is the `SyncDir` of it:
`NewTable` checks the declaration, and the `Tx` tracks the directory at run time
and panics for a second directory, any other write, or `Commit` before the sync.

**Appended files.** The `Tx` opens an appended file at its first append and
syncs through that same handle (`SyncFile` is declared under the role of its
`Append`, because a file's steps carry the role it was opened under). A failed
append or sync closes the handle at once and the `Tx` writes nothing more, so a
handle is never reused after a failure and never outlives its `Tx`; the next `Tx`
opens the file again. A role has one `SyncFile`, which takes one path, so a role
that appended to several paths cannot make them all durable: `Commit` panics,
naming the role and the unsynced paths, when a role that made its `SyncFile`
also appended to a path that sync did not cover, since the declaration reads as
"this role is durable" and the other paths would be lost without a sign. A role
that never syncs is the declaration of an unsynced batch and may append to any
number of paths. An append that no `SyncFile` followed is not durable even after
`Commit` returns nil. The partial record a failed append can leave is the
reader's to skip. The framing of what is appended, including a separator at the
start of a `Tx`'s first append to a file that may end in a partial record, belongs
to the format's owner: this layer writes the bytes it is given.

**Cleanup and the clean-state requirement.** `Writer.Recover` runs the settle
below first and then one `Recovery` transaction, `txn-cleanup`: it removes every
`.tmp-*` file and `.trash-*` directory anywhere under the data directory, files
first, then directories, then one directory sync per directory that held one. The list it works from is the
exported pure function `CleanupWork`, which only reads, so a test evaluates it on
`Mem.Clone()` and the repair uses the same function. The requirement is that on
every clean run (no crash, no damage, no injected fault) the list is empty at
every quiescent point, because a temporary name exists only while a replacement
is under way and a garbage name only while a directory is being discarded. With
nothing to remove `Recover` reaches no point, so a start on a clean disk is not a
recovery, and a clean run that reaches a recovery point is a failure. `Recover`
checks that the table holds `txn-cleanup` and `txn-settle` before it looks at
the disk and panics otherwise, so a composition root that left out `Internal()`
fails at its first start rather than at the first crash. Every other
`Recovery` transaction has the same shape: its trigger is a named pure function
of the disk, used by the repair and by the scenario tests, and holds only for
states a crash or damage can produce.

**Settle.** `Recover` starts with `txn-settle`, a `Normal` transaction that syncs
every directory under the data directory, the root included, once each, parents
before children and names sorted, and only then runs the cleanup. After a process
crash a rename or a removal can be visible in a directory and not durable: the
page cache outlives the process, the directory entry may not have reached the
disk. What follows reads existence and acts on it durably. The recovery of an
owner deletes a body no reference names, or discards a version directory the
current pointer no longer names, and a power loss afterwards can restore the
change it acted on: a durable consequence of a change that no longer exists, such
as a current pointer back at a version that is gone. The cleanup removes the
contents of a directory that `Discard` renamed to a garbage name, and the
removal of the files of that directory is durable on its own, so a power loss
during it could keep the removal of some files and lose the rename, which brings
the directory back under its original name with only some of its files. Settling
first makes the state that every recovery predicate and the cleanup read one a
power loss cannot take back. The predicates of every owner must run after
`Recover` returned nil, and the `Writer` enforces it for the writes: `Begin` of
any declaration that is not one of `Internal()` panics, naming the declaration,
until `Recover` has returned nil. The ordering was once only a promise of the
caller, and a composition root that broke it failed silently and only after a
crash and a power loss; now it fails at its first start. A `Recover` that
returned an error leaves the `Writer` not recovered. A predicate's reads before
its first `Begin` remain the caller's to order. The transaction sits in this package because
`Recover` already runs first at startup and walks the whole data directory, so
there is one home for the rule and no owner repeats it. No order of the syncs is
needed for durability (a directory sync covers that directory's entries only and
renames stay inside one directory); the order only keeps crash point numbers
stable. The directories of debris (`.trash-*`) are not synced: the rename that
gave one its garbage name is an entry of its parent, which is synced, and what is
inside is about to be removed. It is `Normal`, not `Recovery`, because it runs at
every start and a clean start is not a recovery. The cost is one directory sync
per directory at every start. A crash inside it leaves a state that the next
`Recover` settles again.

**Coverage.** `Writer.Coverage()` returns every key of the table with its hit
count, zeros included, each key with the kind of its transaction, so a caller
counts the normal and the recovery paths separately. Counts and ordinals belong
to the `Writer`: one per process, one per test.

**Observing.** An `Observer` is told of every point before its step runs, and of
the commit point after the last step completed and before `Commit` returns, when
nothing has been acknowledged. A test takes `Mem.Clone()` (process crash) or
`CrashClone` (power loss) in it and restarts on the clone.

**Contract with callers.** Acknowledge, and update memory, only after `Commit`
returns nil; do not read back a path written in the same `Tx`; an append that no
`SyncFile` followed is not durable even after `Commit` returns nil; exit and
rescan on `IsFatal`; serialise the transactions of one declaration.

### The records of `state/`

`state/` is the runtime data of a node and is owned by the `store` package;
`keys/` and `config/` have their own owners. Deleting `state/` is a crash that
loses what was in flight and nothing more: the stable identifiers, the keys and
every decision of the owner live outside it.

```
state/
  out/<h>.rec          source record: one per message or control signal sent from this node
  in/<h>.rec           target record: one per message for one endpoint, deduplication included
  ctl/<h>.rec          target control record: deduplication, stored reply, creation intent
  bodies/<sha256>      a body, bare bytes, named by its digest
  endpoints/<h>.rec    endpoint runtime state: kind, instance, name, description, token, session mapping
  attempts/<h>.rec     prompt attempts, blocked report, the followers a head record owes
  checkpoints/<h>.rec  an adapter's checkpoint
  admin/<h>.rec        result and fingerprint of an administrative operation
  alerts/<h>.rec       owner alerts
  seq/<h>.rec          sequence floors of one sending endpoint
  archive/<date>.jsonl summaries and events of records that left the live files
```

`<h>` is the SHA-256 of the kind, a space and the key, in lowercase hex. The
kind is part of it because `out/` holds two kinds (a message and a control
signal) that share a key text.

**Record file.** The first line is `sidera-record 1 <kind> <crc32c> <length> <key>`
(`recfile`), the body follows. The checksum covers the body only; the kind and
the key of the first line are verified by the file name, which is their hash, so
a first line that passed can be trusted to name its key even when the body is
gone, which is what the replacement of a damaged source record needs. The detail of
a damage names the rule and the field, never what the first line holds: a damaged
newline can glue the body of a file, a private key for one under `keys/`, into it.
A file is
validated in seven steps, and each failure has a class:

| Step | Check | Class |
| ---- | ----- | ----- |
| 1 | the first line is at most 8 KiB, ends in a newline, has six fields of single spaces, the magic and the version | first line unreadable |
| 2 | the kind is one the directory holds | first line unreadable |
| 3 | the key is unpadded base64url and the file name is the hash of kind and key | first line unreadable |
| 4 | the key parses as a key of the kind, field by field with the parser of the package that owns the field (a message id with `envelope.ValidMessageID`, so a key accepts exactly the ids a peer can send) | first line unreadable |
| 5 | the bytes after the first line are as many as the length says (at most 256 KiB) | body damaged |
| 6 | their CRC32C is the checksum | body damaged |
| 7 | the body decodes strictly, is the canonical encoding of what it decodes to, and passes the checks of its kind; every delivery record in it also passes `delivery.Record.Valid()` | body damaged |

A first line whose checksum or length field is itself damaged while the body is
intact fails step 5 or 6 and is treated as a damaged body: a source record is
then overwritten by a lost record, and a target or control record is alerted and
removed, with no replacement. That reports "lost, outcome unknown" to the sender where the
record might have been read; nothing is lost and the failure is visible, so there
is no separate path for it. This is a damaged
record file; a body file whose content does not hash to its name is a different
case, settled through the records that refer to it (see the quarantine of a body
below).

A body is JSON written by `encoding/json` from structs of the package, with
times as RFC 3339 in UTC with a `Z` and fractional seconds without trailing
zeros, and read with `strictjson`. Step 7 also requires the body to be exactly
what the encoder gives for what it decoded to, so no two files mean the same
record, a bit of rot that leaves valid JSON cannot pass as another record, and
decoding then encoding gives the bytes back for every file that decodes.

A delivery record is checked by the delivery package, not by the store: after
decoding, `Record.Valid()` says whether the record is one the constructors and
transitions can produce, and a well-formed record that is impossible (a lease
whose chunk flags disagree with its chunk count would make a later hand-out index
out of range) is a damaged body like any other. The same check runs before a
record is written, so a defect upstream is refused where it happens and not
written down as a file that would be settled as damaged when it is read. Besides the
fields of a delivery record that every record has, the stored form carries the
cause of an unknown result and the lost marker (which the sender's replacement
keeps after it closes, so that it is not judged by the shape of a delivery
accepted normally). A target record has neither a lost marker nor a loss cause,
and the deduplication record has no flag for a lost digest: a body that still
carries one is a damaged body.

**The time of the state.** Every stored delivery record, whatever its side, kind
or shape, carries `state_at`, the moment the record entered its current state
(`delivery.Record.StateAt`), right after `state`. It is required and always
written: a body without it, with it `null` or with it at the zero time is a
damaged body (the key is absent from no valid record, and `Record.Valid` refuses
a record that has none), and there is no second key for the delivery time, which
the delivery package derives from `state_at` of a delivered record (an unknown
`delivered_at` key is refused like any unknown field). A signed status report
takes both times from here. Nothing shipped before the field existed, so there is
no reader for a record without it and no migration. Tests: the vectors of
`TestRecordGoldenVectors`, the `state_at` rows of `TestDecodeRecordBodyRules`
and `TestEveryStoredFieldSurvivesTheDisk`.

**What a source record keeps to send again.** The envelope parts the sender needs
to rebuild the message are the recipients (`to`, every recipient, in the order of
the key), the sequence numbers, the send time, `report_by`, the content type, the
thread, the message it answers, the preset and `disclose_recipients`. The flag is
written only when true, right after `to`: the digest of an envelope covers it, so
a message sent again must carry the flag it was first sent with, or the target
would see a different digest for the same message and refuse it as a conflict. That
the message has several recipients is not stored; it is `len(to) > 1`, because
`to` holds all of them. Tests: the `out disclosed` vector of `TestRecordGoldenVectors`,
the disclosure rows of `TestDecodeRecordBodyRules`, the two disclosed records of
`TestRecordsRoundTrip` and the `store.OutEnvelope` row of
`TestStoredContractTypesHaveNoFieldTheRecordsDoNotCarry`.

**Two shapes of a source record.** A source record is regular or lost. A lost
record is what replaces a source record that was damaged: it has the key of the
damaged one, the delivery records the delivery package builds for that case, the
discovery time and the feedback it owes. It has no envelope, no sequence numbers
and no body reference. A target or control record has one shape: a target that
loses a record keeps nothing in its place (the sender learns of it when the target
answers a pull that it holds no record), so a damaged one is settled with an alert
and, for a control record, the closed session endpoints, then removed, and nothing
is put under its name. A resend of a delivery whose target record was lost is taken in as
new before the delivery deadline, and may be handed out a second time.

**Revision.** A regular target record carries `rev`, which the store raises by one
at each write. A head record (the prompt or block of an endpoint) lists the
followers it still owes, each with the revision and the acceptance time the record
had before the head was written. A rule that replays a prompt or a block acts on a
listed follower only if it is the same record and nothing has written it since.
The rule that a write raises the revision has one home, the store: every write
that does so (`Deliver`, `DeliverReply`, `PromptEnd`, `Blocked`, `Lease`, `Ack`,
`Tick`, the drains and their replays, `FeedbackForm`, the followers' replays and
`QuarantineBody`) returns the target records as written, in the order given, and a
caller that keeps a view takes them from the return value instead of working the
revision out again. The return value is `nil` when the write failed. `TestWritesReturnTheTargetRecordsAsAScanReadsThemBack`
compares each returned record with what a new scan reads back.

**Endpoint record.** One per endpoint, under the hash of its id. Its fields, in
the order they are written: `kind`, `instance`, `display_name` and `description`
(both optional, omitted when empty), `status`, `token` (32 bytes, present while the
endpoint is creating or open, absent once it is closed), `declaration` (compact
JSON owned by the engine), `session` (the mapping of a session endpoint) and
`created_by` (the control record that created a session endpoint). The name and the
description are what the adapter registered the endpoint under and are held to the
bounds of that protocol (`adapterproto.MaxDisplayName` and
`adapterproto.MaxDescription`, counted in characters, valid UTF-8): a record the
protocol could not have registered is refused when it is written and when it is
read. The node's declaration to peers bounds a name more tightly
(`publication.MaxNameLen`, in bytes), so a name that is stored may have to be cut
or refused where the declaration is built; the record does not decide that.
`TestEndpointTextIsBoundedAsTheRegistrationIs` checks the bounds against the
protocol's own validation. Text that is not valid UTF-8 never reaches the decoder
as such (the JSON decoder replaces a bad byte), so a body holding it is refused by
the check that a body is the canonical encoding of what it decodes
(`TestEndpointTextThatIsNotUTF8IsRefusedOnRead`)). The bounds on length are checked on read
in the decoder itself (`TestEndpointTextOverTheBoundIsRefusedOnRead`).

**Bodies** are content-addressed and written before the record that refers to
them; a body is removed in the transaction that drops its last reference, at
its end, after the records are durable. Every write that can drop one takes the
bodies to release as an argument: `SourceUpdate`, `ControlResult`, `Ack`, `Tick`,
the drains, and `DeliverReply`, whose reply can carry the evidence that finishes
a delivery and so drops the source record's body (`deliver-reply` removes it with
`body` and `body-dir`; the body of the reply itself is the role `reply-body`, since
a role names one write of a transaction). Tests:
`TestDeliverReplyRemovesTheBodiesItReleases` and the `deliver-reply` walks of
`TestEveryTransactionConvergesFromEveryCrashState`. Bodies are addressed by
content, so a record the same transaction writes can refer to the file it was asked to
remove (a reply whose bytes equal the message it answers refers to that very body); every
write that takes a release refuses such a release with `ErrReleasedBodyInUse` before it
opens a transaction, because the removal would be silent and irreversible: the record
would point at bytes that are gone until someone reads it
(`TestAWriteRefusesToReleaseABodyARecordItWritesRefersTo`, one case for every write that
takes a release). What a
crash leaves between the two is an orphan, removed by the first rule of the next
start. A body is checked against its name when it is read (a start does not read
them: 10,000 bodies of 64 KiB are 625 MiB); a mismatch affects the records that
refer to it and nothing else, and each is settled by a call of its own
(`QuarantineBody`). A record that still needs the bytes is lost with them: a target
record (an accepted message waiting to be handed out) is alerted and removed, a
source record is overwritten with its lost record, as a damaged source record is.
Any other record, and every control record (which never needs a body), keeps its
state and is written with the reference dropped. The body file is left alone and
is an orphan once no record refers to it. Whether a record needs the bytes is the
delivery rules' answer (`delivery.Record.NeedsBody`), asked by the store itself, so
a target record that needs its body cannot be written without it; a call that gives
a replacement the record does not call for, or omits the one it does, is refused
before anything is written (`TestABodyThatDoesNotMatchItsNameSettlesEachRecordThatRefersToIt`).

**Existence is read before the transaction opens.** A call that removes files
(a release of bodies, a sweep, an orphan collection) asks the disk which of them
are there before it begins, and the removals only consult that answer. A read
that failed inside the transaction after its commit point would be an ordinary
error on a disk that already changed, and the caller would go on with a view of
memory that is no longer the disk; a failed read before it ends the call with
nothing written. A file that is gone is skipped, so a release interrupted by a
crash can be repeated.

**Quarantine.** A damaged file is settled by one transaction that writes every
effect first and removes the file, or overwrites it in place, last. Each effect
can be written again without harm, so a file that is still there after a crash is
settled again by the next start: nothing records how far a settlement got, and
there is nothing to resume. What the repetition can add is a floor one higher (a
gap, never a number given twice); neither that nor anything else it repeats loses
anything. The bad bytes are not kept. The alert id is the store's: the first 16
bytes of the SHA-256 of the directory, the file name and the bytes of the damaged
file (for a record whose body does not match its name, the directory and name of
the record and the name of the body), so the settlement redone after a crash
rewrites its alert and adds no second, and a later, different damage of a file of
the same name (a resent message taken in again) is an alert of its own, never one
that replaces an alert the owner has acknowledged. The caller states the kind, the
time, the subject and the detail only.
The alert is written after the other effects and before the file goes, each write
durable before the next, so a file that is gone always has its alert.

| File | Effects, in order | Last |
| ---- | ----------------- | ---- |
| `in/`, any damage | alert | file removed |
| `ctl/`, any damage | the session endpoints being created by it are closed (open ones stay open); alert | file removed; a delivery derived from it stays |
| `out/`, first line trusted | the sequence floors of every pair of its key raised; alert | file overwritten in place with the sender's lost record and its feedback |
| `out/`, first line unreadable (all zero included) | alert | file removed |
| `seq/`, first line trusted | alert | file overwritten with the floors its sender's source records say |
| `seq/`, first line unreadable | alert | file removed |
| any other directory | alert | file removed |

The first line of a file is trusted by the same rule as that of a live one
(steps 1 to 4): a first line that parses but does not hash to the name of the file
is handled like an unreadable one, with no replacement and no sequence floor raised
for the key it claims. An `out/` file whose first line is unreadable says nothing
about what it was, so there is no lost record to put in its place and nobody is
told but the owner (`TestADamagedSourceRecordWithAnUnreadableFirstLineIsAlertedAndDeleted`).
The same rule settles the damage a write meets (`DamagedFileError`). Configuration
and key files are never quarantined: a damaged one stops the start and is named.

A quarantine of a control record closes every session endpoint still being created
that names the record as its creator, not only the first. A control record accepted
again after a failed first acceptance has the same name and a new endpoint, so a
record can have several; the rules that remove endpoints leave a creating endpoint
alone while its record is quarantined or damaged, so one left behind would
hold one of the node's endpoint slots for good. The store decides which endpoints
close, from the scan the quarantine is given (`QuarantineSessions`: every session
endpoint whose creator is the record and that is not open, written closed, in the
`session` role the transaction declares as many): the caller cannot pass a list,
because it could not be checked, and a missed endpoint leaks a slot silently while
a wrong one (an open endpoint, another record's) closes a live session and lets a
drain drop its deliveries, which cannot be undone. A control record is refused
without the scan. `QuarantineSessions` is exported only so a caller can tell
whether a session may remain, for the kind of the alert. The crash walk covers
the quarantine with two endpoints
(`TestQuarantineClosesEveryCreatingSessionEndpointOfTheDamagedControlRecord`), and
its coverage assertion requires that one transaction wrote the session role more
than once (`maxInTx` of the walk's recorder, not the hits of a whole run, which two
transactions could add up to). The rule itself is pinned by
`TestQuarantineSessionsAreTheEndpointsCreatedByTheRecordThatAreNotOpen` (an open
endpoint naming the record and another record's endpoint are not listed) and
`TestQuarantineClosesOnlyTheEndpointsTheStoreFindsInTheScan` (the same on a disk,
and the refusal without a scan).

**Sequence numbers.** The next number of a (sending endpoint, target address)
pair is the highest of the floor of the pair and the numbers of the source records
still there, plus one. A number is given again until the record that carries it is
acknowledged, so a crash makes no gap. The sweep writes the floor of every pair it
is about to remove a record of before it removes anything, and works the floors out
itself from the source records it is given to remove (the highest number each used
for each target): a chunk that removes a source record has no way to leave its
floor out. A damaged source record whose first line can be trusted always raises
the floor of every pair of its key to m+1, where m is the highest of the floor
now on the disk and the numbers of the source records still there: the lost record
may have used the next number, and no other that is not below m. Two such records
of one pair give two gaps (`TestADamagedSourceRecordRaisesTheFloorAboveEverythingKnown`).
A damaged floor file is treated as missing: it is overwritten with the floors its
sender's source records say (a first line that is unreadable names no sender, so
the file only goes), and the owner has an alert. What it said is lost, and a number
of that pair may then be given twice. A source record whose first line is
unreadable raises no floor, for nothing says which pairs it belonged to. A floor
file that is damaged when a write has to read it (a sweep, or the settlement of a
source record of the same sender) is not read as empty and overwritten by that
write, which would lose it without an alert: the write does nothing and returns the
damaged file as the scan lists it, so that the caller settles it by the usual rule
and tries again. The scan lists floor files first for that reason. Deleting
`state/` starts every pair at 1.

**Archive.** A line is `{"crc":"<8 hex>","rec":<compact JSON>}` and a newline; the
checksum covers the record. A transaction that appends starts with a newline, so
the torn tail a crash leaves, and the half line a failed append leaves in the
middle of a file, are lines of their own and cannot swallow the first line that
follows. A reader resumes at every newline, skips empty lines, drops and counts a
line that is not intact and ignores a last line without a newline. Events are not
synced, and neither is a summary: the archive is best effort. A sweep appends its
summaries first and goes on to the floors and removals without waiting for them to
be durable, because nothing in the node reads them back to decide anything. A
failure or a crash may therefore lose a summary (a power loss after the removals),
or write one twice (a crash after the append and before the removals, which the
sweep that follows archives again, and a sweep that failed after a partial append
and was retried). A reader that counts summaries must deduplicate them by the
record's file hash, and must not expect one for every record that left.

**Retention.** A delivery record is a live file until its deliveries are final and
the latest of the retentions of what it holds has passed: the deduplication record
and the idempotency key until `report_by`, the record itself, which answers a
source's pulls, until `delivery.RecordRetainUntil(report_by)` (the clock tolerance
after `report_by`; the facts window below is longer today, and the store takes
the delivery rule as one of the terms so that it holds whatever that window
becomes), the status report until `report_by` or a
whole `report_by` window after its creation, and the facts reply permission reads
until `report_by` plus the clock tolerance plus the reply window. That is about 27
hours after the delivery deadline. Then a summary goes to the archive and the file
is removed. A closed session endpoint goes
with its attempts, checkpoints and floor file, but not while any source record has
it as sender (whole, lost, or damaged with a key that can be trusted): the sweep of
a source record writes the floor of its sender, and a floor file written after the
endpoint's was removed with it would stay forever. The sweep that removes its last
source record leaves the endpoint, and the next one takes it. Administrative
results are kept 7 days, acknowledged alerts 7 days after the acknowledgement, and
archive files 30 days, uncompressed. An archived message costs about 2.4 KiB (two
summaries and about eight events), so at 3,000 messages an hour, the upper scale
the design assumes, a day is about 169 MiB and 30 days about 4.9 GiB; at 500 an
hour about 0.8 GiB. The archive is not part of the free-space reserve a node keeps
for new work.

**Retained facts are single files.** A segment file per hour would need a second
representation, its own compaction and crash rules, and a records index fed from
two places; a record can still change until `report_by`, and a late proof can
change it after. With the work bounded (the number of unfinished deliveries is
limited), the number of live files is the thing to measure. The cold start scan of
live files, measured on the real file system of macOS with a warm page cache (Apple
M4, `BenchmarkColdStartScan`, 2/3 target records and 1/3 source records):

| Live files | Scan |
| ---------- | ---- |
| 10,000 | 0.83 s |
| 50,000 | 3.2 s |
| 200,000 | 11-13 s |

The threshold at which segment files become a task of their own is a cold start of
200,000 files above 30 s on macOS or above 60 s on the CI of Windows. The numbers
above were measured on the development machine; no CI job runs
`BenchmarkColdStartScan`, so the Windows and Linux numbers and a cold page cache
are not exercised by CI.

**The rules of recovery.** Each rule has a predicate, a pure function of a scan,
and the store offers the write that carries it out. The predicates the store owns:
orphan bodies; session endpoints being created that no control record creates (the
record is not there, or it is a regular record whose intent holds no creation of that
endpoint, as after an intake that failed and a signal accepted again with a new
endpoint; a damaged record keeps the endpoint for its quarantine); a session
endpoint whose control record holds the end of its creation or its close; the
followers a prompt or a block owes; feedback a notice already covers; the endpoints
of a disabled instance; and the candidates of the drain of records queued for a
closed endpoint, which the engine narrows with the delivery rules. A run with no
crash and no damage finds none of them at any quiet point, and a finished recovery
leaves none; tests evaluate the predicates on a copy of the disk after every step
of such a run.

**The order of the rules.** The order is a guarantee of the caller, not of the
store: every predicate is a function of one scan, and the store cannot know which
writes the scan it is given has seen. The order is: quarantine of damaged files
(it writes replacement records and closed session endpoints, and raises sequence
floors, which every later rule and the sequence book read);
`ctl-effect-replay` and `instance-drain-replay` (they close endpoints);
`orphan-gc-endpoint`, before `endpoint-drain-replay`, so that a delivery queued
for an orphan endpoint that a power loss left behind is drained as one for an
endpoint that does not exist; `endpoint-drain-replay`, after every rule that
closes or removes an endpoint; `prompt-replay`, `blocked-replay` and
`feedback-replay`, which skip a record a drain rewrote because its revision no
longer matches, so their order among themselves and against the drain does not
change the result; `orphan-gc` after every
replay, because replays and quarantines add and drop references to bodies; and
the sequence book last. Each rule runs on a scan taken after the writes of the
previous one, or on an equivalent view of the committed writes, and the sequence
repeats until no rule has work. The test simulator (`sim.pass`, `sim.recoverAll`
in `internal/node/store/sim_test.go`) is that loop, and the crash walk uses it as
the recovery of every scenario.

A rule whose write has the shape of a normal transaction is that transaction, run
again, and has no declaration of its own: the drain rules write with `endpoint-drain`
and `instance-drain`, the replays of a prompt and of a block with `lease` (their
followers are written as a hand-out writes its records), and the replay of feedback
with `feedback-clear`. A second declaration of the same shape would double the crash
points to walk and could drift from the first. Only the writes no normal transaction
makes are `Recovery` declarations of the store: `quarantine`, `orphan-gc`,
`orphan-gc-endpoint` and `ctl-effect-replay`.

**Settle.** The rules above write their followers durably after reading a head
record that, after a process crash, may be visible and not durable; a power loss
that followed would leave the followers without their head. The store does not
guard against this itself: it relies on the settle transaction that starts
`txn.Writer.Recover` (the Settle paragraph of the transaction section), which
makes every directory durable before any rule runs, and `Begin` refuses the
store's transactions until it has returned nil. On Windows `SyncDir` flushes nothing, so that
barrier persists nothing there (the platform section says what durability rests
on); not exercised by CI, no job loses power. The rules run after
`Recover` returned nil. The store's crash walk runs its restarts through the real
`Recover`, so it exercises that barrier: without it the walk fails on orphan
collection after a process crash.

## The crash model and what it can prove

`fsxtest.Mem` is an in-memory `FS` that keeps, for every file, the current
content and how much of it is synced, and for every directory the current
entries and the entries as of its last sync. It produces the states a crash
can leave. Its rules are weaker than any journaling or copy-on-write file
system (POSIX itself says nothing about crashes), each with its source:

| Rule | Basis |
| ---- | ----- |
| After `Sync`, the file's content and size survive a power loss. | `fsync(2): transfers all modified data and metadata of the file` |
| `Sync` of a file does not make its directory entry durable; an entry is durable only after `SyncDir` of its directory. | `fsync(2)`: calling fsync on a file does not ensure that its directory entry reached disk; Pillai et al., "All File Systems Are Not Created Equal", OSDI 2014, on directory entries |
| After `SyncDir`, the directory's own entries survive. It does not cover the content of the files in it or the entries of its subdirectories. | `fsync(2)` on a directory descriptor |
| Unsynced file data, after a power loss: the length is anything from the synced length to the written length, and each 4 KiB block beyond the synced length is independently the real data, zeros (the size grew but the data never arrived) or garbage. The synced part is a prefix of the content because files are never overwritten. | OSDI 2014: an append is not atomic across blocks and a file may contain garbage when its size was updated before its data; Pebble's `MemFS.CrashClone` cuts at 4096-byte blocks |
| Unsynced directory entries, after a power loss: each name independently keeps its synced binding or its current one. Different directories are independent. A name never takes a third value. | OSDI 2014: ext2 and btrfs reorder directory operations freely; Pebble's crash clone adds each unsynced entry independently |
| A single rename is atomic: a rename over an existing target leaves the old file under the target name with the source still present, or the new file under the target name with the source gone, so the renamed file has exactly one name and the target never holds a third value. Different renames, and different names, stay independent. A name rebound several times between two syncs of its directory shows its synced binding or its last one, never one in between. | OSDI 2014 section 2.2.1: `rename()` and `link()` are seemingly atomic on all file systems that use techniques like journaling or copy-on-write; their table of directory operation atomicity fails only on ext2, ext2 in synchronous mode and reiserfs without a log, which are not modelled. `rename(2)`: the target is atomically replaced and an instance of it always exists |
| A directory rename is one rename like any other: its two names (and the names of a chain of renames that share a name) are decided together, so a directory never has two names or none. | a directory cannot be hard-linked, so it has one name at a time |
| The new name can be durable while the renamed file's content is not, so the target holds a cut or damaged file. | the zero-length-file problem of ext4 after a rename without a file sync |
| An unsynced `mkdir` can vanish together with everything in the directory. | a directory is one name in its parent |
| `RemoveAll` is many unbindings, each with its own fate, so a power loss can leave any subset of a tree. | a consequence of the entry rule; it is why a tree is first renamed to a garbage name |
| A process crash (`Clone`) loses nothing a completed call did; open files and locks are gone. | the page cache belongs to the kernel and outlives the process; the kernel releases locks |

The distribution is fixed so that detection can be reasoned about: a name whose
binding is unsynced keeps its current one with probability 1/2; a file with
unsynced bytes keeps its full length with probability 1/2 and otherwise a
uniform length; each block beyond the synced length is real with probability
1/2, zero with 1/4, garbage with 1/4. `CrashClone(rng)` draws these in sorted
name order, so a seed fixes the result. `CrashCloneWorst()` draws nothing and
keeps exactly what was synced: it is the deterministic way to show that a
missing sync loses data.

The crash walks of the store and of the engine prove three things, the reliability
floor: an acknowledged write survives a power loss that keeps only synced content;
after any crash, recovery terminates and leaves every record valid, closable and
able to give feedback; a crash during recovery is redone. They take two kinds of
crash at every point of every transaction: the process crash and the worst power
loss. The worst one is the one that shows an acknowledged write was synced, and the
process crash is the one that shows recovery copes with everything that was written.
Under the transaction discipline a random power loss only keeps some subset of the
one group of writes that is not synced yet, a state between those two, so the seeded
power losses stay in the property tests, which draw them over random operations
(`TestPowerLossCloneKeepsAcknowledgedWrites`,
`TestRandomOperationsLeaveExactlyTheDiskAModelPredicts`, and the engine's
`TestPowerLossKeepsWhatTheEngineAcknowledged`), and in the walks of the store's
`batch` transactions, whose files are independent single-file transitions, where any
subset of the written files is a state of its own (not under the race detector, which
adds cost and no coverage to a single-threaded walk).

The store does not crash again at every point of the recovery that follows each
crash state (the nested walk). Each point of a recovery rule, which is every point of
a recovery transaction and of the cleanup and settle of `txn.Recover`, is reached by
the recovery of some crash state; the first such state in the order of the walks is
crashed again at every point of its recovery, in both kinds. That redoes the
recovery transactions, the cleanup and the settle after a crash in the middle of
them, which is what the third property needs, from a state where the rule finds
half-written work. A replay that writes under the declaration of a normal
transaction (the drains, the prompt and block replays under `lease`, the feedback
replay under `feedback-clear`) is not a point of its own in this domain, and it
needs no walk of its own: a crash in the middle of it leaves the disk that the walk
of that declaration crashes at, and the recovery that follows is the one that walk
already runs and checks. The assertions are derived from the
same selection: every declared point is crashed at in both kinds by a walk, and
every point of a recovery rule is crashed at during a recovery in both kinds. The
nested walk is cheap enough to run under the race detector as well, so its
assertions apply in every mode.

### Counter-example check

A model that cannot catch the classic bugs proves nothing. The test writes
four independent files with a writer that never syncs the file and with a
writer that never syncs the directory, written out separately from `fsx`, and
requires that over 32 fixed seeds at least one clone loses an acknowledged
file, and at least one keeps unsynced data (otherwise a clone that drops
everything would pass vacuously). In the worst mode each broken writer is
caught by one clone. The correct writer is `fsx.Replace` itself: all of its
files survive every clone. A file written without a directory sync is lost with
probability 1/2, and one written without a file sync is cut or damaged with
probability above 1/2, so 32 seeds of four files miss either bug with
probability below 10^-38.

### What the model cannot prove

- It checks the assumptions written above, not a real kernel or disk. Where a
  real file system is stronger (ext4 in ordered mode, a copy-on-write file
  system), code that passes the model is still correct; where a real device
  lies about flushing, no software test can tell.
- It does not model Windows. Windows does no directory sync (`SyncDir` only
  checks that the directory exists) and the power-loss behaviour of its
  write-through rename is not documented, so `Mem` stands for a file system
  weaker than any journaling or copy-on-write one, and the barriers that the
  crash tests show to be sufficient on `Mem` persist nothing on Windows by
  themselves; the Windows paragraphs above stand on their own tests, which pass in
  the Windows CI jobs. No CI job observes what Windows does at a power loss.
- It takes clones at an instant between calls. A crash in the middle of a
  system call is covered only by the block rule (partial data) and the
  `RemoveAll` rule (partial removal).
- It checks the sequences `fsx` composes, not that a caller used them: a caller
  that writes with a path that bypasses `fsx` is caught by layout rules, not by
  the model.
- Real power loss on Linux (for example with a device-mapper flaky target) is
  an optional strengthening that is not part of this suite.

### Fault injection

`fsxtest.Faulty` wraps any `FS` and fails the calls its rules select: by
transaction, role, step, ordinal of the key, or path, with an error or after a
short write that leaves a partial tail. A test using `Faulty` fails if any of
its rules never fired, because a fault that was not injected proves nothing.
Faults go through the `FS` interface, never through build tags. `RunConformance`
runs the same behavioural cases against `Mem` and against the real file system
so that the double cannot drift from what it stands for.

## Why

- **Composite operations are functions over `FS`, not methods.** If `Replace`
  were a method, the in-memory file system would need its own copy and the crash
  model would check that copy, not the sequence that ships. As functions, the
  same code runs on both, and the counter-example check examines the real
  sequence. Platform code stays minimal: only the primitives differ.
- **Paths are relative and validated; `os.Root` is not used.** Relative paths
  keep every write inside the data directory and let the in-memory system skip
  absolute paths. `os.Root` prevents symbolic-link escapes, but its rename goes
  through `os.Rename`, which cannot pass `MOVEFILE_WRITE_THROUGH`; the root is
  0700, so only the owner could plant a link in it.
- **Renames only within one directory.** The crash behaviour of a cross-directory
  rename differs between file systems; offering no such rename means there is no
  such case to model. Every rename the node needs is in one directory.
- **`Remove` unbinds without syncing.** A removal is one unlink and a directory
  sync is one more step with its own failure class, so a batch of removals costs
  one sync and not one per file, and a failed sync is the single point at which
  the batch is known not to be durable. `Remove` therefore relies on the
  `SyncDir` that follows it, and the transaction layer enforces that: `Commit`
  panics when a directory in which the `Tx` removed a name (or a tree) was not
  synced after the removal, as it does for a group of deferred replacements.
- **No overwrite in place.** Every write is either a new temporary file renamed
  into place or an append, so a file's content after a power loss is a synced
  prefix followed by an unsynced part. With overwrite the model would need
  per-block old/new content.
- **`RenameDir` never replaces.** Windows cannot replace a directory with
  `MoveFileEx`, and Unix `rename(2)` and Go differ on replacing an empty or
  non-empty directory. One rule on all three: the target must not exist. A
  directory replacement is done by a versioned directory and a pointer file
  replaced atomically, not by one primitive.
- **Names decided independently, not by operation order.** A model where each
  directory keeps a prefix of its operation log would assume ordered directory
  operations, which btrfs and ext2 do not provide, and would hide bugs there. The
  independent model is weaker than any journaling or copy-on-write file system, never loses a synced
  binding and never gives a target name a middle value; what it adds is leftover
  temporary or garbage names, which startup removes anyway. A rename, of a file
  or of a directory, is the exception: its two names are decided together,
  because the reordering studied there is of different directory operations
  relative to each other, not a tear inside one rename. Every operation of `fsx`
  that binds a name syncs the directory before it returns, so a name is not bound
  a second time before the first binding is durable, with two exceptions:
  `ReplaceDeferred` and `OpenAppend` do not sync the directory before returning.
  The first is completed by the `SyncDir` that must follow it, the second by
  `SyncFile`.
- **Fault injection through the interface, not build tags.** A tag changes the
  program under test; a wrapper does not, and a wrapper can also wrap the real
  file system.
- **`MoveFileEx` rather than `ReplaceFile` on Windows.** `ReplaceFile` can fail
  with three distinct intermediate states and does not support write-through;
  the Go toolchain's own measurements found `MoveFileEx` fails less under
  concurrency.
- **Libraries rejected.** `google/renameio` does not support Windows and does
  not sync directories; `natefinch/atomic` does not sync directories, does not
  retry and is unmaintained; tailscale's `atomicfile` has an unstable API and
  requires a newer Go than this module. None covers both Windows and the full
  Unix sequence, and the write sequence is short enough to own. Pebble's `vfs`
  was the model for the narrow interface, the in-memory system and the crash
  clone, but is far larger than the problem and is not a dependency.
- **One dependency, `golang.org/x/sys`**, for `flock` and `statfs` on Unix and
  `MoveFileEx`, `LockFileEx`, `CreateFile`, security descriptors and disk space
  on Windows. It is the standard supplement to the standard library for these
  calls.
- **Failure-path cleanup goes through the file system under a reserved tag.**
  It must reach the real `Remove` (an in-memory double and a real directory both
  need it removed), yet it is not a crash point, so it is tagged `cleanup` and
  skipped by observers and by fault rules that do not name it.

- **No generic commit manifest.** Every transaction that spans files has one of
  three shapes: prepare then commit (a body file, then the one record that
  refers to it), authority then followers (the first file decides, the others
  follow from it and from the records already there), or an independent batch
  (every file is a valid single-record change, so any subset is a valid state).
  None needs several files that cannot be derived from each other to appear
  all-or-nothing, which is the only thing a manifest buys. A manifest would cost
  more syncs (a four-sync intake becomes seven), more crash points, a second
  opaque copy of the state that the owner's files no longer fully describe,
  secrets copied into it for the transactions that write keys, special cases when
  `state/` is deleted (a replay would write half a transaction into an empty
  tree), and no way to tell a manifest damaged in the middle of a write from one
  damaged after its commit. The price of doing without is that each owner writes
  the replay rules of its own transactions; they stay honest because a rule's
  trigger is a named pure function that must be empty on every clean state, and
  because the layer's ordering checks make the declaration the same thing as the
  code. SQLite's super-journal, LevelDB's `CURRENT` and git's table list serve
  files that must switch together and cannot be derived from one another; git's
  own multi-reference update, which renames one file at a time and orders them so
  the intermediate states are the less bad ones, is the model taken here.
- **The commit point is the first namespace step, not the first durable sync.**
  Once a rename, a removal or a `mkdir` has completed, this process's own later
  reads see it. If the next failure were an ordinary failure, memory would
  still claim "nothing happened" while the disk shows a batch deleted down to the
  third file. Taking the namespace step as the point makes that failure fatal. For
  a single replacement it changes nothing: a failed directory sync was already
  fatal. An `Append` has no commit step because its bytes are not durable until a
  sync and its readers skip a damaged record, so a failed append needs no more
  than an ordinary failure.
- **Deferred groups are one directory with the sync next.** If a write to another
  directory could come between the replacements and the sync, a power loss could
  keep that later write and lose the group, so the file the later write was meant
  to follow would be there without its predecessor. One directory and the very
  next write fixes the order that matters; the ordinary `Replace` of the same
  directory would cost a directory sync per file.
- **Panics for misuse, errors for I/O.** An undeclared role, an operation or an
  order the declaration does not allow, a second open `Tx` of one declaration, a
  deferred group left unsynced: each is a defect in the code that makes the call,
  found the first time it runs, and returning an error would let a caller treat it
  as an I/O failure and carry on.
- **The class is decided in two places, each once.** What a failed step means
  without context is in the `fsx` table; what it means after a commit step is
  `commitStepOf` and `Tx.fail`. A caller only reads the resulting class.
- **A declaration is copied into the table.** The `Tx` enforces the declaration
  that was validated, not whatever the literal was edited to afterwards.
- **The cleanup of `Recover` does nothing on a clean disk.** An empty
  transaction would still reach a commit point of a recovery transaction at every
  start, which would make "a clean run reaches no recovery point" false and blur
  the coverage of the recovery path. The settle that precedes it is of the normal
  kind for the same reason.

### Why the records are shaped as they are

- **One file per record, replaced whole.** A state change rewrites a few KiB, the
  record is the truth a person can read, and there is no second copy to disagree
  with it. The alternatives were a log or a manifest that every multi-file
  transaction replays (more fsyncs, an opaque second copy, and a special case for
  every deleted `state/`) and a database (a dependency the problem does not need).
- **A transaction is a head and followers the head determines.** None of the
  transactions needs several files that cannot be derived from one another to
  appear together. The authority is written first and durably; the followers are
  written after, and a rule that can recompute them from the authority repairs a
  crash between. The rules are stated as implications that hold on every state a
  crash cannot have caused, so they find no work on a clean disk: "closed" is
  absorbing, a follower that no longer exists is done, and a follower of a head
  record is listed with the revision it had.
- **The damaged file goes last, after every effect that can be written again.**
  A settlement interrupted anywhere leaves the file where the next start finds it,
  and writes the same effects once more: nothing has to know how far the first
  attempt got, so there is no resumption, no marker of an unfinished settlement
  and no second copy of the bad bytes to expire. The alternative that kept the
  bytes renamed the file first and wrote its alert last; the state in between had
  to be recognised, resumed and bounded, for bytes that nobody could use.
- **The sweep removes a control record before the delivery derived from it.** The
  other order can leave the delivery without the record that explains it, and a
  rule that rebuilds deliveries from control records would give it a second life.
- **Floors are written before records are removed, and the store works them out.**
  A number derived from the records that happen to be there would be given again
  after a sweep; and a floor the caller had to pass in could be left out.
- **An endpoint record goes after what hangs on it, once that is durably gone.** A
  crash, or a power loss that kept the removal of the endpoint and lost that of its
  floor, attempts or checkpoint file, would leave a file no sweep looks for, since
  every rule finds them through the endpoint.
- **Exempt traffic omits the verdict of the authorization check and the reply
  permission; every other in record stores both.** An exempt record, a notice of
  the system endpoint, is not judged by the policy and cannot be replied to, and
  says so itself (`exempt` in its delivery), so that one that lost the verdicts
  in storage is damaged and not a record that never had them. The marker, not the
  sender, is what the rule reads, because the decision belongs to the delivery
  rules; the sender must still agree (the system endpoint sends exactly the
  exempt records), so a record that lost the marker is damaged too. A notice
  that was lost is settled as a damaged file: a target keeps no replacement.
- **The kind is hashed into the file name.** One flipped bit would otherwise turn a
  message record into a control record.
- **Nothing is skipped in a settlement because it seems to be done.** What a
  crashed process left may be visible and not durable, so a settlement writes its
  effects again, and the file that triggers it goes only after they are durable.

## Known gaps

- **A source file whose first line is unreadable is not reported to its sender.**
  Nothing in such a file (an all-zero one included) says which message it held or
  who sent it, so it is alerted to the owner and removed, and no lost record
  tells the sending endpoint. The owner's alert is the only report.
- **Symbolic links.** The test that `Stat` does not follow a final symbolic link
  and the tests that a link is not owner-only skip where the platform refuses to
  create one without privilege.
- **Assigning another owner on Windows.** The tests that a file owned by
  Everyone or by another user is not owner-only skip where the account lacks
  the privilege to assign that owner, which is the usual case; the decision they
  cover is a short comparison against the trusted set.
- **Cross-user permission checks** (a different user opening the data directory)
  are not tested: CI has one user.

- **The ordering of renames on Windows.** The transaction layer relies on one
  rename becoming durable before the next, which `MoveFileEx` with write-through
  does not document. It is the same weakness the file system layer has; not
  exercised by CI, no job loses power.
- **Handles are closed without checking.** A close error of an appended file is
  ignored: a file that was synced is durable, and one that was not is not
  promised to be.
- **The tests of this layer run on the in-memory file system.** They prove the
  step sequences, the classes and the ordering against the crash model, not
  against a real kernel.

- **`DrainCandidates` is not known to be the whole predicate** of the replay that
  drains the records queued for a closed endpoint: it names the accepted target and
  control records, and the engine decides whether a
  forced close also changes records in other states or shapes. The test that would
  compare it with the delivery package for every state is skipped, with the
  question as its reason.
- **The records are tested on the in-memory file system and, for the cold start,
  on the real one of macOS.** Windows and Linux numbers and the Windows ordering
  of renames are not exercised by CI: no job runs the benchmark or loses power.
- **A cold page cache is not measured.**

## Where

| Subject | Code | Tests |
| ------- | ---- | ----- |
| interface, paths, tags, reserved tags | `internal/node/fsx/fsx.go` | `ops_test.go` (`TestPathElementsNeverHoldABackslashColonOrNul`), `observe_test.go`, the path cases of `RunConformance` |
| steps, keys, points, counter, `ParsePoint` | `internal/node/fsx/point.go` | `point_test.go` (golden points, rejection table, `FuzzParsePoint`, `FuzzPointRoundTrip`) |
| classes and the classification table | `internal/node/fsx/error.go`, `classify.go` | `TestFsyncFailureBeforeRenameFailsOperation`, `TestDirSyncFailureAfterRenameIsFatal`, `TestFailureClassOfEveryStep` |
| composite operations | `internal/node/fsx/ops.go` | `TestCompositeOperationsRunTheirFixedSteps`, `TestReplaceLeavesNoTemporaryAndKeepsTheTargetWhenAnyStepBeforeTheRenameFails`, `TestCleanupAfterAFailureIsNotAStep`, `TestAppendShortWriteIsAFailureThatLeavesAPartialTail`, `TestSyncFileStepsCarryTheTagTheFileWasOpenedWith`, `TestEachCompositeOperationIsDurableWhenItReturns`, `TestDiscardRenamesToAGarbageNameBeforeDeleting` and the `RenameDir`, path and root tests in `ops_test.go` |
| a full volume | `internal/node/fsx/nospace.go`, `nospace_unix.go`, `nospace_windows.go`; the injected fault `internal/node/fsx/fsxtest/diskfull_unix.go`, `diskfull_windows.go` | `nospace_test.go` (`TestIsNoSpaceRecognisesTheInjectedFullDisk`, `TestIsNoSpaceSeesThroughTheErrorOfAFailedOperation`), `nospace_unix_test.go`, `nospace_windows_test.go` (pending verification by CI on main) |
| `Observe`, `PointOf` | `internal/node/fsx/observe.go`, `error.go` | `observe_test.go` |
| leftovers | `internal/node/fsx/leftovers.go` | `leftovers_test.go` |
| `Exists` | `internal/node/fsx/fsx.go` | `exists_test.go` |
| retry | `internal/node/fsx/retry.go`, `os_windows.go` | `retry_internal_test.go`, `retry_windows_test.go` |
| real file system | `internal/node/fsx/os.go`, `os_unix.go`, `os_windows.go` | `os_test.go`, `os_unix_test.go`, `os_windows_test.go`, `retry_windows_test.go` |
| crash model | `internal/node/fsx/fsxtest/mem.go`, `crash.go` | `mem_test.go` (each rule above, and the append and directory-rename result table `TestAppendAndRenameDirResultTable`), `counterexample_test.go` |
| fault injection | `internal/node/fsx/fsxtest/faulty.go` | `faulty_test.go` |
| conformance | `internal/node/fsx/fsxtest/conformance.go` | `TestMemConformsToTheFSContract`, `TestOSFSConformsToTheFSContract` |
| declarations, table, keys | `internal/node/txn/txn.go`, `table.go`, `ops.go` | `TestTransactionTableIsValid`, `TestTableKeysAreTheStepsOfEveryDeclaredWrite`, `TestTableKeysOfEveryOperation`, `TestNewTableRejects`, `TestNewTableAccepts`, `TestTableKeepsTheDeclarationItWasBuiltFrom` |
| run time checks | `internal/node/txn/tx.go`, `writer.go` | `recoverfirst_test.go` (`TestBeginOfAnOwnerDeclarationBeforeRecoverPanics`, `TestBeginOfAnOwnerDeclarationAfterASuccessfulRecoverWorks`, `TestBeginOfAnOwnerDeclarationAfterAFailedRecoverStillPanics`, `TestBeginOfAnInternalDeclarationIsNotRefusedBeforeRecover`); `runtime_test.go` (`TestBeginPanicsOnADeclarationThatIsNotInTheTable`, `TestBeginPanicsWhileATxOfTheSameDeclarationIsOpen`, `TestTxPanicsOnACallTheDeclarationDoesNotAllow`, `TestDeferredReplacementsMustBeSyncedBeforeAnythingElse`, `TestSyncFileMustMatchTheAppendInThisTx`, `TestCommitOfATransactionThatMadeNoWriteReachesNoPoint`) |
| commit point and classes | `internal/node/txn/ops.go`, `tx.go` | `classify_test.go` (`TestFailureClassBeforeAndAfterTheCommitPoint`, `TestFirstErrorIsStickyAndNoStepRunsAfterIt`, `TestEachOperationHasItsCommitStep`, `TestFailureAfterTheCommitStepInsideOneOperationIsFatal`) |
| crash states and durability | `internal/node/txn/tx.go` | `crash_test.go` (`TestEveryDeclaredKeyIsReachedInOrder`, `TestCrashStatesAtEveryPointOfAMultiWriteTransaction`, `TestAcknowledgedWritesSurvivePowerLoss`), `TestRandomTransactionsHonourTheCommitPointOrderingAndDurability` |
| a `Tx` closed after its commit point, and removals synced before the commit | `internal/node/txn/tx.go` | `close_test.go` (`TestCloseAfterTheCommitPointWithoutCommitOrFailurePanics`, `TestCloseThatIsNotALapseDoesNotPanic`, `TestCloseDoesNotReplaceThePanicOfTheTxItself`), `unsynced_test.go` (`TestCommitPanicsWhenARemovalWasNotSynced`) |
| appended files | `internal/node/txn/tx.go` | `append_test.go` (`TestFailedAppendDropsTheHandle`, `TestHandlesAreClosedWhenTheTransactionEnds`) |
| cleanup and its predicate | `internal/node/txn/cleanup.go` | `recover_test.go` (`TestRecoverRemovesAllLeftoversAndTheirPredicateIsEmptyAfterwards`, `TestCleanupPredicateIsEmptyOnEveryCleanState`, `TestRecoverOnACleanDiskReachesNoRecoveryPoint`, `TestRecoverPanicsWithoutTheCleanupDeclarationEvenOnACleanDisk`) |
| settle at every start | `internal/node/txn/cleanup.go` | `settle_test.go` (`TestPowerLossCloneAfterRecoverKeepsWhatAProcessCrashLeftVisible`, `TestPowerLossCloneAfterACrashDuringTheSettle`, `TestRecoverSettlesEveryDirectoryInAStableOrder`); `internal/node/txn/discard_test.go` (`TestPowerLossCloneAfterACrashDuringTheCleanupOfADiscardedDirectory`); `internal/node/config/settle_test.go` (`TestPowerLossCloneAdapterGCAfterAnInstallCrashedBeforeTheDirectorySync`) |
| coverage | `internal/node/txn/writer.go` | `coverage_test.go` |
| read side | `internal/node/txn/writer.go` | `reader_test.go` |
| record framing | `internal/node/recfile/recfile.go` | `TestFrameGoldenVectors`, `TestDecodeFrameDamageByRule`, `TestSingletonRecords`, `TestHeaderDamageNeverEchoesTheFirstLine`, `FuzzDecodeFrame` |
| keys | `internal/node/store/key.go` (message ids by `envelope.ValidMessageID`) | `TestKeyTextGoldenVectors`, `TestParseKeyRoundTripsEveryKind`, `TestParseKeyRefusals`, `FuzzParseKey` |
| record codec | `internal/node/store/decode.go`, `codec_*.go`, `wire.go` | `TestVerdictPartsFollowTheExemptMarker`, `TestRecordGoldenVectors`, `TestDecodeRecordStepsAndClasses`, `TestDecodeRecordBodyRules`, `TestTargetRecordsHaveNoLostShape`, `TestStoreRefusesAnInvalidDecodedRecord`, `TestRecordsRoundTrip`, `TestEveryStoredFieldSurvivesTheDisk`, `TestStoredContractTypesHaveNoFieldTheRecordsDoNotCarry`, `FuzzDecodeRecord`, `FuzzDecodeRecordBody`, `TestEndpointTextIsBoundedAsTheRegistrationIs`, `TestEndpointTextThatIsNotUTF8IsRefusedOnRead`, `TestEndpointTextOverTheBoundIsRefusedOnRead` |
| scan, bodies, free space | `internal/node/store/scan.go`, `store.go` | `scan_test.go` (`TestScanTrustsTheKeyOfADamagedFileOnlyWhenItsFirstLineBelongsToItsName` among them), `TestReadBodyChecksTheNameAndTheContent`, `TestAvailableReportsTheVolumeOfState` |
| declarations of the store | `internal/node/store/txns.go` | `TestDeclarationsMatchTheDesignTable`, `TestTransactionTableIsValid` |
| the transactions | `internal/node/store/write.go` | `write_test.go`, `written_test.go` (`TestWritesReturnTheTargetRecordsAsAScanReadsThemBack`, `TestAFailedWriteReturnsNoRecords`),  `existence_test.go` (`TestAFailedExistenceCheckEndsTheCallBeforeAnyWrite`), `TestEveryTransactionConvergesFromEveryCrashState` (every point of every workflow, in every kind of crash, with a crash during recovery; and the coverage of every declared point; under `-race` only the two kinds of crash that draw nothing, and no crash during recovery), `TestPowerLossCloneKeepsAcknowledgedWrites`, `TestRandomOperationsLeaveExactlyTheDiskAModelPredicts` |
| recovery rules | `internal/node/store/recovery.go` | `recovery_scenarios_test.go` (each rule, and the settlement of each kind of damaged file, walked from the state a crash leaves), `quarantine_session_test.go` (the double fault of a session creation, `TestQuarantineSessionsAreTheEndpointsCreatedByTheRecordThatAreNotOpen`, `TestQuarantineClosesOnlyTheEndpointsTheStoreFindsInTheScan`), `TestCleanScenariosLeaveNoRecoveryWork` |
| quarantine | `internal/node/store/quarantine.go` | `quarantine_rules_test.go` (one test for each row of the table: the effects, the file gone or overwritten, one alert, a second start with nothing to do), `recovery_scenarios_test.go` and `quarantine_session_test.go` for the crash walks |
| sequence numbers | `internal/node/store/seq.go`, `write.go` | `seq_test.go` (`TestArchiveSweepWritesTheFloorOfEverySenderAndTargetOfTheRecordsItRemoves`, `TestDamagedFloorFileMetByAWriteIsReportedAndNothingIsWritten`), `sweep_endpoint_test.go` |
| archive | `internal/node/store/archive.go` | `archive_test.go`, `FuzzParseArchive` |
| retention and the sweep | `internal/node/store/retention.go` | `retention_test.go`, `sweep_endpoint_test.go` (`TestClosedSessionEndpointAndItsFloorFileGoOnceItsLastSourceRecordHasGone`, the crash walk of a closed session's sweep) |
| deleting `state/`, the layout rule | `internal/node/store/recovery.go` | `state_deleted_test.go` |
| cold start | `internal/node/store/bench_test.go` | `BenchmarkColdStartScan`, `TestColdStartFixtureScansBack` |

### Windows tests

All are ordinary tests in `_windows_test.go` files, with no build tag and no
manual step; the Windows CI jobs run them and report no failure: replacing a file while a read
handle is held (succeeds once the handle is released within the retry budget,
and fails as an ordinary failure with the target intact when it is not), the
retried error set, the directory flush attempt (logs the result of the call), `Sync`
on an append handle, `RenameDir` refusing an existing target, a second `Lock`
getting `ErrLocked`, the owner-only descriptor of a created data directory (and that Everyone, an untrusted owner, a callback allow entry or a symbolic link make it false), and that reading a missing file returns `fs.ErrNotExist` at once.

The `test` job runs these two packages once more with `-v`, so its log shows what
the tests log and which of them skip. On the `windows-latest` runner (Windows
Server 2025) the directory flush attempt logged the results given under
Directory flush above, and no test of this package skipped: the tests of an
untrusted owner (Everyone, and `BUILTIN\Users`) assigned that owner, and the
symbolic-link test created its link. The tests run as the built-in local
Administrator account (the owner in the logged security descriptors is `LA`, and
the `TokenUser` of the pipe tests ends in `-500`). That account has the privileges
both tests need, and the paths they exercise, assigning another owner and
creating a link, are the ones an unprivileged account skips; the log does not
print whether the token is elevated.
