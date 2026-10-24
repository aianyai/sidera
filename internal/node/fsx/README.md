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
| `FS`, `Reader`, `File` | The interface. Each write method is one step; a read is not a step. |
| `OpenOS(root, rand)` | The real `FS` rooted at an existing directory; `rand` names temporary files. |
| `MakeRoot(path)` | Creates the data directory owner-only, once, at `init`. |
| `Replace`, `OpenAppend`, `Append`, `SyncFile`, `RenameFile`, `Remove`, `SyncDir`, `MakeDir`, `RenameDir`, `RemoveAll`, `Discard` | The durable operations: fixed step sequences over any `FS`. |
| `Observe(fs, counter, hook)` | Numbers every write step and calls the hook before it runs. |
| `Tag`, `Step`, `Key`, `Point`, `Counter`, `ParseKey`, `ParsePoint` | Crash point identity: `intake/out.rename#2`. |
| `Error`, `Class` (`Failed`, `Fatal`), `IsFatal`, `PointOf` | What a failed operation returns and what the caller must do. |
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
- **`Discard` renames before it removes.** If the removal fails the error is
  returned but the directory is already gone from its name; the `.trash-*`
  leftover is removed at startup.
- **`RenameDir` never replaces.** The target must not exist on any platform.
- **Reads are not steps and hold nothing.** `ReadFile` reads and closes at once,
  so a reader never blocks a replacement on Windows.
- **A Windows read does not retry `file not found`.** The file may genuinely not
  exist; renames and removals do retry it.
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
