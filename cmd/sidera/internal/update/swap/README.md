# swap

Replaces a set of installed programs by new ones so that the set changes all or
not at all. It does the file operations of `sidera update` for the programs in
one directory; where the new programs come from, whether they may be installed
and what runs afterwards are not its concern.

Every path is relative to the root of the `fsx.FS` the caller opened, which is the
directory of the programs, so a rename never leaves a directory. The new programs
are read through an `fsx.Reader` of their own, because they are unpacked elsewhere.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Prepare(dst, src, items)` | Writes each `Item.Source` as `<Target>.new` next to its target and makes the names durable. |
| `(*Swap).Commit()` | Sets each target aside as `<target>.old`, then renames `<target>.new` to the target; undoes everything on the first failure. Once per `Swap`. |
| `Clean(fsys, targets)` | Removes the `.new` and `.old` files of earlier runs and puts back a program a crash took from its name; returns what it restored and the held files it could not remove. |
| `RollbackError` | A failed `Commit` that could not undo itself; `Held` lists the `.old` files that hold the previous programs. |

## Rules

- **A `.new` is complete or absent.** It is written under a temporary name, synced,
  and renamed to `<target>.new`, which is `fsx.ReplaceDeferred`; one directory sync
  per directory then makes every name durable before `Commit` can run.
- **Set aside first, install second.** A program that is running can be renamed
  on Windows, not overwritten or removed, so the current file is renamed to
  `<target>.old` and only then is the new file renamed to the target. The same
  order is used everywhere, so the platform that cannot do it any other way is the
  one that is tested.
- **A failed step undoes the renames made so far, newest first.** Renaming
  `<target>.old` over the target restores a program that was only set aside and
  replaces one that was already installed, so a single step serves both. The
  `.new` files left are removed, and afterwards the targets are as they were and no
  `.old` remains. The final directory sync is a step like the others: if it fails,
  the swap is undone.
- **A rollback that fails says where the program is.** `RollbackError` lists the
  `<target>.old` files that hold the previous programs and the error text says to
  rename each back to its target.
- **After success the `.old` files stay.** They are the way back (rename one over
  its target) until a later `Clean` removes them. `<target>.old` always means the
  manual rollback copy of the last update, and it is free before every `Commit`.
- **`Clean` never loses the only copy.** A target that is missing while
  `<target>.old` exists is a run that stopped between the two renames of one
  target: `Clean` renames the `.old` back to the target (the rollback step) and
  reports it in `Cleaned.Restored`, then removes the `.new`. A lone `.old` with no
  target is restored the same way. A `.old` is removed only when its target is
  there.
- **A `.old` that a process holds is renamed out of the way.** On Windows a
  process that started from the old program, such as a long-lived `sidera mcp`
  helper of an agent session, keeps the file open: it cannot be removed, but it
  can be renamed, as `Commit` relies on. A `Commit` onto such a `.old` would be
  refused (replacing a running image is access denied) and the update would fail
  for as long as the process lives. So `Clean` renames it to the first free
  `<target>.old-<n>` and returns the new name in `Cleaned.Left`. Every `Clean`
  also lists `<target>.old-<n>` next to each target and tries to remove them,
  returning those still held. Only what `fsx.IsHeld` reports (a sharing violation
  or access denied on Windows) counts as held; any other failure to remove a
  `.old` (an immutable flag, an I/O error) is returned in `err` with its cause,
  because renaming would not help.
- **`<target>.old-<n>` is Clean's name, not Commit's.** `n` counts from 1 and a
  name is reused once its file is gone. Only names that are the target's name,
  `.old-` and decimal digits are touched.
- **New programs are owner-only executables (0700).** The `fsx.FS` creates files
  with that mode or with 0600, and has no way to copy the mode of the file it
  replaces or to change a mode.

## States a crash leaves

`Commit` is not atomic across a crash; each rename is. The steps and what is on disk
after a crash before each:

| Crash | Targets | Leftovers | Repair |
| ----- | ------- | --------- | ------ |
| During `Prepare` | all old | `.new` files, perhaps one `.tmp-*` file | `Clean` removes the `.new` files; a `.tmp-*` file is removed by hand |
| Before the first rename of `Commit` | all old | `.new` for each | `Clean` |
| Between the two renames of one target | that target missing | `<target>.old` holds the previous program, `<target>.new` the new one | `Clean` renames `<target>.old` back to `<target>` and removes the `.new` |
| Between the renames of two targets | the first new, the second old | `.old` of the first, `.new` of the second | `Clean` removes both; the next update installs the missing version |

A crash between targets can therefore leave a mixed set, some programs new and
some old; `Clean` restores each target to a runnable program, never to a missing
one, and the next update brings the set to one version. A failed `Commit` leaves
no such state, because it undoes itself. A power cut is the same as a crash, with
the added chance that renames not yet made durable are lost; the crash table
covers both.

## What the caller must guarantee

- **Hold the update lock.** There is one writer: two concurrent updates would
  fight over the same `.new` and `.old` files.
- **Do not interrupt `Commit`.** Block SIGINT and SIGTERM (console Ctrl-C on
  Windows) for its duration. The window between the two renames of a target is
  short on Unix, but on Windows each rename is retried for up to about two seconds,
  so an interrupt there makes the crash states above likely rather than remote. `swap` does not install handlers: a process-wide
  signal policy belongs to the program.
- **Call `Clean` before `Prepare`** of every update, with the same targets.

## Tests

`swap_test.go` runs on `fsxtest.Mem`: the order of the steps this package owns, a
fault at every step of `Prepare` and `Commit`, a fault in the rollback itself,
`Clean` on the state a process crash and a power loss before each step leave, and
`Clean` with removals and restorations that fail. `clean_internal_test.go` gives
`Clean` its own judgment of "held", so that renaming a held `.old` aside is tested
on every platform. `swap_os_test.go` replaces a program that is running twice in a
row, on the real file system of every platform; on Windows that is the one
assumption the order stands on, and the second update proves a held `.old` does not
stop it.
