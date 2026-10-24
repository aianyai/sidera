# Storage

How a node writes bytes that must survive a crash or a power loss, what the
test file system can and cannot prove about that, and what each platform is
believed to do. The code is under
[`internal/node/fsx`](../../../internal/node/fsx/README.md) (the file system
interface and the durable write sequences) and
[`internal/node/fsx/fsxtest`](../../../internal/node/fsx/fsxtest/README.md)
(the crashable in-memory file system and the fault injector).

Platform paragraphs that say "pending verification by CI on main" describe
behaviour no one has observed on that platform yet: the code and its tests are
written from the platform documentation, type-checked by cross-compilation, and
first run by the CI that follows the first landing on `main`.

## What

### One narrow file system

Every persistent byte goes through `fsx.FS`. A method of `FS` is one
system-call-sized step: create a temporary file, write, sync, rename, sync a
directory, remove, create a directory, rename a directory. The sequences that
make a write durable are functions over any `FS`, not methods of it:

| Operation | Steps, in order | Used for |
| --------- | --------------- | -------- |
| `Replace` | `create`, `write`, `sync`, `rename`, `sync-dir` | every single-file replacement: records, configuration, keys, manifests |
| `OpenAppend`, `Append` | `open` once, then `write` per call; no sync | appended event and digest files |
| `SyncFile` | `sync` (on the handle that appended), `sync-dir` | making a batch of appends durable |
| `RenameFile` | `rename`, `sync-dir` | quarantining a damaged file |
| `Remove` | `remove` | one deletion; a batch of them is followed by one `SyncDir` |
| `SyncDir` | `sync-dir` | after a batch of removals |
| `MakeDir` | `mkdir`, `sync-dir` (the parent) | layout and staging directories |
| `RenameDir` | `rename-dir`, `sync-dir` (the parent) | a staged directory appearing whole |
| `RemoveAll` | `remove-all` | only a directory already renamed to a garbage name |
| `Discard` | `rename-dir` to `.trash-<hex>`, `sync-dir`, `remove-all` | deleting a directory tree |

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
same state as a crash before the failed step, so it needs no crash point.

### Failure classes

A failed composite operation returns an `*fsx.Error` whose class says what the
caller must do. The class is decided in one table (`classify.go`):

| Step that failed | Class | Reason |
| ---------------- | ----- | ------ |
| `create`, `write`, `rename`, `remove`, `remove-all`, `mkdir`, `rename-dir`, `open` | Failed | the call did not complete, so nothing durable changed; the caller rejects the request or applies backpressure. A short write can leave half a record at the end of an appended file, which is why appended formats delimit and checksum their records and readers skip a damaged tail |
| `sync` of the temporary file of a `Replace`, before its rename | Failed | the target is untouched and nothing was acknowledged; the temporary file is removed. Treating this as fatal would turn a persistent device error into a restart loop |
| any `sync-dir` | Fatal | whether the rename or creation is durable is unknown, and memory may disagree with disk. After a failed write-back Linux marks the pages clean, so a later `fsync` can report success for data that is lost; the only safe answer is to exit and rescan |
| `sync` of an appended file (`SyncFile`) | Fatal | the appended bytes are visible to this process but whether they are on disk is unknown. A later successful sync must not let the caller believe the whole batch is durable, for example by deleting what it just archived |

A call refused before any step ran (an invalid path, a missing directory to
look in) is Failed. `fsx.IsFatal` finds a Fatal error through any wrapping. The
process exit itself is not in `fsx`: it returns the class and the composition
root exits.

A multi-file transaction fails Fatal for any failure after its first
non-preparatory write is durable; that rule belongs to the transaction layer,
not to `fsx`.

### Appending and syncing

An appended file is synced through the same handle that appended it, in the
same operation. Linux reports a write-back error only to descriptors that were
open when the error happened, so closing the file and reopening it to `fsync`
can miss the error and succeed. Under the instance lock there is one writer, so
`OpenAppend` positions at the end and appends there.

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

| Primitive | Linux | macOS | Windows |
| --------- | ----- | ----- | ------- |
| file `Sync` | `fsync(2)` | `F_FULLFSYNC` through Go's `File.Sync`; plain `fsync` only hands data to the drive, which may delay or reorder it | `FlushFileBuffers`, which needs a handle opened for writing; temporary files and append handles are |
| `SyncDir` | open read-only and `fsync` the directory | the same; that this makes a rename durable on APFS is not documented by Apple | no flush; a check that the directory exists |
| `Rename` | `rename(2)`, atomic over an existing target | the same | `MoveFileExW` with `MOVEFILE_REPLACE_EXISTING` and `MOVEFILE_WRITE_THROUGH`, retried for about two seconds on access denied, sharing violation and file not found |
| `RenameDir` | target checked absent, then `rename(2)` | the same | target checked absent, `MoveFileExW` with `MOVEFILE_WRITE_THROUGH` and without `REPLACE_EXISTING` |
| `ReadFile` | read and close | the same | read and close at once, retried like a rename |
| `OpenAppend` | `O_WRONLY`, `O_APPEND` and `O_CREATE` at 0600 | the same | `CreateFile` with `GENERIC_WRITE`, positioned at the end |
| `Available` | `statfs`, `Bavail * Bsize` | the same | `GetDiskFreeSpaceExW`, the free bytes available to the caller |
| root creation | `mkdir` 0700 | the same | a directory with a protected DACL for the current user, inherited by what is created inside |

Windows paragraphs are pending verification by CI on main. Specifically:

- **Directory flush.** Windows does not flush directories in `fsx`. The
  documentation of `FlushFileBuffers` mentions files and volumes only and
  requires write access; other storage engines (Pebble, RocksDB, NATS) skip the
  flush on Windows and PostgreSQL ignores the error. A Windows test opens the
  directory with write access and with backup semantics, calls
  `FlushFileBuffers`, and logs the result. If it succeeds, making `SyncDir` a
  real flush is a change inside the Windows file only. The measured result is
  pending verification by CI on main.
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
  antivirus scanners and indexers briefly holding a file. A read of a file that
  does not exist also retries `file not found`, so it costs the whole retry
  budget on Windows.
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
  inherited by files and directories created inside. `Info.OwnerOnly` is true
  when the descriptor has a DACL and every allow entry names the object's
  owner, the user running the process, SYSTEM or Administrators. Reading the
  descriptor failing reports false, so a key file is never assumed private.

Linux paragraphs (the `fsync` and directory `fsync` semantics, `flock`, `statfs`)
are pending verification by CI on main: the development machine is macOS.

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
- It does not model Windows. Windows does no directory sync and the
  power-loss behaviour of its write-through rename is not documented, so `Mem`
  stands for a file system weaker than any journaling or copy-on-write one; the
  Windows paragraphs above stand on their own tests.
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
  a second time before the first binding is durable.
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

## Known gaps

- **File sync under another role.** `File.Sync` has no tag: the sync of an
  appended file is attributed to the tag the file was opened with. A transaction
  that appends under one role and syncs under another therefore reaches the
  file sync under the append role, which a declaration of the sync role would
  not list. The question is recorded as a skipped test,
  `TestSyncStepOfAppendedFileCarriesTheRoleOfTheSyncCall`, for the transaction
  layer to settle.
- **Reading an absent file on Windows** costs the retry budget, because
  `file not found` is in the retried set.
- **Symbolic links.** The test that `Stat` does not follow a final symbolic link
  skips where the platform refuses to create one without privilege.
- **Cross-user permission checks** (a different user opening the data directory)
  are not tested: CI has one user.

## Where

| Subject | Code | Tests |
| ------- | ---- | ----- |
| interface, paths, tags, reserved tags | `internal/node/fsx/fsx.go` | `ops_test.go` (`TestPathElementsNeverHoldABackslashColonOrNul`), `observe_test.go`, the path cases of `RunConformance` |
| steps, keys, points, counter, `ParsePoint` | `internal/node/fsx/point.go` | `point_test.go` (golden points, rejection table, `FuzzParsePoint`, `FuzzPointRoundTrip`) |
| classes and the classification table | `internal/node/fsx/error.go`, `classify.go` | `TestFsyncFailureBeforeRenameFailsOperation`, `TestDirSyncFailureAfterRenameIsFatal`, `TestFailureClassOfEveryStep` |
| composite operations | `internal/node/fsx/ops.go` | `TestCompositeOperationsRunTheirFixedSteps`, `TestReplaceLeavesNoTemporaryAndKeepsTheTargetWhenAnyStepBeforeTheRenameFails`, `TestCleanupAfterAFailureIsNotAStep`, `TestAppendShortWriteIsAFailureThatLeavesAPartialTail`, `TestEachCompositeOperationIsDurableWhenItReturns`, `TestDiscardRenamesToAGarbageNameBeforeDeleting` and the `RenameDir`, path and root tests in `ops_test.go` |
| `Observe`, `PointOf` | `internal/node/fsx/observe.go`, `error.go` | `observe_test.go` |
| leftovers | `internal/node/fsx/leftovers.go` | `leftovers_test.go` |
| retry | `internal/node/fsx/retry.go` | `retry_internal_test.go` |
| real file system | `internal/node/fsx/os.go`, `os_unix.go`, `os_windows.go` | `os_test.go`, `os_unix_test.go`, `os_windows_test.go`, `retry_windows_test.go` |
| crash model | `internal/node/fsx/fsxtest/mem.go`, `crash.go` | `mem_test.go` (each rule above, and the append and directory-rename result table `TestAppendAndRenameDirResultTable`), `counterexample_test.go` |
| fault injection | `internal/node/fsx/fsxtest/faulty.go` | `faulty_test.go` |
| conformance | `internal/node/fsx/fsxtest/conformance.go` | `TestMemConformsToTheFSContract`, `TestOSFSConformsToTheFSContract` |

### Windows tests

All are ordinary tests in `_windows_test.go` files, with no build tag and no
manual step, pending verification by CI on main: replacing a file while a read
handle is held (succeeds once the handle is released within the retry budget,
and fails as an ordinary failure with the target intact when it is not), the
retried error set, the directory flush attempt (logs the measured result), `Sync`
on an append handle, `RenameDir` refusing an existing target, a second `Lock`
getting `ErrLocked`, and the owner-only descriptor of a created data directory.
