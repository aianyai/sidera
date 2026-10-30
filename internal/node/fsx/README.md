# fsx

The narrow file system every persistent byte of a node goes through, and the
durable write sequences built on it. Read
[../../../docs/references/storage/README.md](../../../docs/references/storage/README.md)
for the reasons, the platform behaviour and what the crash model proves.

This is a runtime package: it does I/O. It imports the standard library,
`golang.org/x/sys` and, only to obtain the real clock, `internal/node/clock`, as
the layer table of [`internal/node/README.md`](../README.md) allows. It reads no
clock: the one real wait, the backoff of the Windows retry, goes through a single
unexported function, `sleepSystem`, which waits on a timer of `clock.System()`.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `FS`, `Reader`, `File` | The interface. Each write method is one step; a read is not a step. `Reader.ReadFileLimit` reads a regular file within a byte limit, returning `ErrTooLarge` without partial data on overflow. |
| `OpenOS(root, rand)` | The real `FS` rooted at an existing directory; `rand` names temporary files. |
| `MakeRoot(path)` | Creates the data directory owner-only, once, at `init`. |
| `CreatePrivateFile(path)` | Exclusively creates an empty owner-only writable file at an OS path, verifies its security before returning `*os.File`, and removes it if verification fails. The caller owns writing, syncing, closing and subsequent cleanup. |
| `Replace`, `ReplaceDeferred`, `OpenAppend`, `Append`, `SyncFile`, `RenameFile`, `Remove`, `SyncDir`, `MakeDir`, `RenameDir`, `RemoveAll`, `Discard` | The durable operations: fixed step sequences over any `FS`. |
| `Observation`, `Observe(fs, counter, hook)` | Numbers every write step and passes its point and actual path operands together to the hook before it runs. |
| `Tag`, `Step`, `Key`, `Point`, `Counter`, `ParseKey`, `ParsePoint` | Crash point identity: `intake/out.rename#2`. |
| `Error`, `Class` (`Failed`, `Fatal`), `IsFatal`, `PointOf` | What a failed operation returns and what the caller must do. |
| `IsNoSpace` | Whether an error means the volume or the quota is full (`ENOSPC`, `EDQUOT`; `ERROR_DISK_FULL`, `ERROR_HANDLE_DISK_FULL`, `ERROR_DISK_QUOTA_EXCEEDED`), and nothing a platform does not return: a test injects `fsxtest.DiskFull`. The Windows codes are pending verification by CI on main. The one place that knows the codes. |
| `Exists(r, path)` | Whether a path is there: `fs.ErrNotExist` is false, any other failure of `Stat` is returned, never read as "absent". |
| `Leftovers`, `IsLeftover`, `TempPrefix`, `TrashPrefix` | Finding the debris of interrupted operations. |
| `ErrLocked` | Returned by `FS.Lock` when the lock is held elsewhere. |
| `CheckPath`, `CheckRename`, `ValidName` | The path and name rules, shared with `fsxtest`. Reserved names are not valid names. |

## Rules

- **The operations are functions, not `FS` methods.** The same code runs on the
  real file system and on `fsxtest.Mem`, so the crash model checks what ships.
  An operation does not call `Close` on a handle it did not open, and does
  nothing platform specific: only the `FS` primitives differ per platform.
- **`Replace` is create, write, sync, close, rename, sync the directory.** A
  failure before the rename removes the temporary file and leaves the target.
  `ReplaceDeferred` is the same without the directory sync, for several files of
  one directory that one `SyncDir` then makes durable together; until that sync
  the new names are not durable, so the caller syncs before it acknowledges.
  The removal is not a step: it carries the reserved `cleanup` tag, which
  `Observe` does not number and which fault rules match only by name.
- **The failure class is decided in one table, `classify.go`.** A failed
  directory sync and a failed sync of an appended file are `Fatal`; every other
  failure, and any call refused before a step ran, is `Failed`. Callers read the
  class; nothing else classifies.
- **Appends are synced through the handle that appended them.** `SyncFile` takes
  the `File` from `OpenAppend`; reopening to sync can miss a write-back error.
  The tag given to `Append` and `SyncFile` must be the tag the file was opened
  with: the file's `Write` and `Sync` steps are attributed to that tag, and the
  transaction layer guarantees they agree.
- **Paths are relative, `/`-separated and valid under `io/fs.ValidPath`, and no
  element holds a backslash, a colon or NUL** on any platform, because on Windows
  they separate elements or name streams. `.` is the root and may only be synced
  or listed. Renames stay inside one directory. Nothing can be written outside
  the root; the real file system also maps paths through `filepath.Localize`.
- **Files are 0600, or 0700 for adapter executables; directories are 0700.** On
  Windows the data directory root carries a protected DACL for the current user
  and everything created inside inherits it.
- **`CreatePrivateFile` is an OS boundary outside the rooted `FS`.** It accepts
  an ordinary OS path with an existing parent, like `MakeRoot`; it adds no
  transaction, step or crash point. It never replaces an existing final name,
  including a dangling symbolic link. Unix creates with `O_EXCL` and 0600, then
  checks the opened handle's type and owner-only permissions. Windows creates
  with the same protected current-user DACL as `MakeRoot`, an uninheritable
  handle, `CREATE_NEW` and `FILE_FLAG_OPEN_REPARSE_POINT`. Before returning it checks
  an ordinary disk file, the opened handle's volume for `FILE_PERSISTENT_ACLS`,
  and the actual owner and DACL by the same rule as `Info.OwnerOnly`. A failed
  volume query or a volume that discards ACLs is refused; the error suggests
  stdout without `--out`. Windows network shares are currently unsupported.
  Verification failures close and remove the new empty file and report cleanup
  failures too. A device or a handle whose ordinary disk-file type cannot be
  established is closed without removing its path. Parent links follow OS
  resolution; this is not an atomic cleanup guarantee against a hostile process
  replacing names in the parent directory. No power-loss durability is promised.
- **`Info.OwnerOnly` is true only for a regular file or a directory.** A symbolic
  link, a socket, a device or a Windows junction is never owner-only: `Stat` does
  not follow a link and `ReadFile` does, so the permission bits of a link (macOS
  lets the owner set them with `chmod -h`) say nothing about who can read what it
  points to. On the `windows-latest` runner `Stat` of a link returned `OwnerOnly`
  false (`TestWindowsOwnerOnlyIsFalseForASymbolicLink`).
- **`Discard` renames before it removes.** If the removal fails the error is
  returned but the directory is already gone from its name; the `.trash-*`
  leftover is removed at startup.
- **`RenameDir` never replaces.** The target must not exist on any platform.
- **Reads are not steps and hold nothing.** `ReadFile` and `ReadFileLimit`
  follow links, refuse a non-regular opened handle with `ErrNotRegular`, and close
  before they return, so a reader never blocks a replacement on Windows. Unix opens with
  `O_NONBLOCK` before the check: opening a FIFO must not wait for a writer.
  `ReadFileLimit` takes a nonnegative limit, refuses larger files with
  `ErrTooLarge` and no data, and bounds the read even if the file grows after
  the handle's size was checked. A negative limit wraps `fs.ErrInvalid`.
- **Directory entries describe the name itself.** `Entry.IsSymlink` marks a
  symbolic link, and `IsDir` does not follow it. A link to a regular file can
  still be read; a link to a directory, device or FIFO cannot.
- **A Windows read does not retry `file not found`.** The file may genuinely not
  exist; renames and removals do retry it.
- **An observation binds identity to operands before the primitive.** `Path` is
  the actual primitive path; for `Rename` and `RenameDir` it is the target and
  `From` is the source. `CreateTemp` reports its parent directory, since the
  temporary name does not exist yet. File `Write` and `Sync` retain the
  `File.Name` obtained on opening. The root directory is `.`. Metadata does
  not change `Point`, keys, numbering, error stamping or the skip on hook error.
  Callbacks may interleave, including for the same key: each call carries its
  own point and operands; no separate path callback has to be paired with it.
- **Retries on Windows happen below `Observe`.** One logical step reaches the
  hook once, however many times the rename was retried.
- **`.tmp-*` and `.trash-*` are debris.** Anything starting with one of the
  prefixes may be removed at startup.

## What the caller must guarantee

- Hold the instance lock (`FS.Lock`) while writing: there is one writer, which is
  what makes positioning an append handle at its end equivalent to `O_APPEND`,
  and what makes the choice of an unused trash name race-free.
- Treat `Fatal` as the end of the process: exit and rescan on restart. `fsx`
  returns the class; the composition root exits.
- Pass tags made by the transaction layer. `fsx` does not interpret them, and
  uses the names `log` and `cleanup` itself for steps that are not crash points:
  they are reserved, so `ValidName` refuses them and they cannot be transaction
  or role names.
- Make appended formats self-delimiting with a checksum, and let a reader
  resynchronise after a damaged record anywhere in the file, not only at the
  tail: a failed append leaves a partial record that later appends follow, and a
  crash leaves a damaged tail.
- Do not hold a read handle to a file `fsx` may replace; read it with `ReadFile`.
- Put the data directory on a local file system.

## Platform files

`os_unix.go` (macOS and Linux), `os_windows.go`. Platform tests are ordinary
tests in `os_unix_test.go` and the `_windows_test.go` files; the Windows ones
run only in Windows CI.

## Benchmarks

`BenchmarkFilesystemWrites` in `bench_test.go` runs the twelve public operation
functions on a real file system, with a separate named sub-benchmark for each.
Only the target call is timed; preparation, result checks, closing handles and
cleanup are excluded. The [storage reference](../../../docs/references/storage/README.md#filesystem-write-benchmarks)
defines the fixed inputs, durability boundaries and reproduction command.
`MakeRoot` and `CreatePrivateFile` are separate OS boundaries and are outside
this transaction write operation group.
