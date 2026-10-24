# txn

The single entry point of every persistent write of `keys/`, `config/` and
`state/`. Read
[../../../docs/references/storage/README.md](../../../docs/references/storage/README.md)
for the reasons and for what the tests prove.

This is a runtime package: it imports `fsx` and nothing else of this module
(layer 2 of [`internal/node/README.md`](../README.md)). It is the only package
that holds a writable `fsx.FS`. The packages above it receive a `*Writer`:
writes only through a `Tx`, reads through `Writer.Reader()`, which has no write
method.

The package knows names, roles, operations, their order, and which writes are
preparatory. It does not know what a file means or how to repair a half
finished transaction: that is the owner of the files, as a `Recovery`
declaration of its own.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Decl`, `Write`, `Kind`, `Op` | The static declaration of one transaction: name, kind (`Normal` or `Recovery`), and writes in order. Each `Write` has a role, an operation, and the flags `Many`, `Optional`, `Prepare`. |
| `NewTable(decls...)`, `Table.Keys()` | Validates the declarations of the whole program and expands every crash point: every step of every write under its role, and one commit per transaction. |
| `Internal()` | The declarations of this package: the cleanup transaction (`Recovery`) and the settle transaction (`Normal`). The composition root puts them first in its table. |
| `New(fs, table, observer)`, `Writer.Begin(decl)` | One `Writer` per process; `Begin` opens a `Tx` of a declaration of the table. |
| `Tx.Replace`, `ReplaceDefer`, `Append`, `SyncFile`, `RenameFile`, `Remove`, `SyncDir`, `MakeDir`, `RenameDir`, `RemoveAll`, `Discard` | One method per operation; the first argument is the declared role. |
| `Tx.Commit`, `Tx.Close` | `Commit` ends the transaction and reaches its commit point; `Close` is for `defer` and does nothing after `Commit`, and panics when the `Tx` is closed after its commit point with neither `Commit` nor a recorded failure. |
| `Writer.Reader()` | The read side, as an `fsx.Reader` that offers nothing else. |
| `Writer.Recover()`, `CleanupWork(reader)` | Make every directory durable, then remove every `.tmp-*` file and `.trash-*` directory; and the list of what the removal would remove, which a test evaluates on a copy of the disk. |
| `Writer.Coverage()` | Every key of the table with its hit count, zeros included, each key carrying its transaction's kind. |
| `Observer` | Told of every point before its step runs; the hook of a crash test. |
| `Error`, `IsFatal` | What a failed write returns, and the one question a caller asks of it. |

## Rules

- **A transaction is declared, never registered.** A `Decl` is a package-level
  literal of the package that owns the files; the composition root lists all of
  them in one `NewTable` call. `Begin` panics for a declaration that is not the
  one the table was built from, so a transaction nobody listed fails the first
  time it runs, and the coverage assertion compares a run against a list the run
  did not produce.
- **Every call is checked against the declaration.** The role must be declared,
  the operation must be the declared one, and the order must follow the
  declaration: a `Many` write may repeat, an `Optional` one may be skipped,
  nothing may be made again after a later write, and `Commit` needs every
  required write made. A violation panics, naming the rule: it is a programming
  error, not an I/O error. `NewTable` also refuses a `Prepare` write declared
  after a write that is not `Prepare`: past the commit point the flag changes
  nothing, and it would mislead a reader.
- **One open `Tx` per declaration.** Crash points are numbered per key; two
  transactions of one declaration advancing the same keys would make the
  numbering depend on scheduling. A second `Begin` panics. Different
  declarations may run at once.
- **The first error is sticky.** After a failed write every call, `Commit`
  included, returns that error before any check and runs no step.
- **The commit point.** The first write that is not `Prepare` has a commit step:
  `rename` for `Replace`, `ReplaceDefer` and `RenameFile`, `remove`, `mkdir`,
  `rename-dir` (also the first step of `Discard`), `remove-all`, and for the two
  operations without a namespace step the `sync` of `SyncFile` and the
  `sync-dir` of a lone `SyncDir`. An `Append` has none. Before the commit step
  completes, a failure has the class `fsx` gave it (`Failed` unless a directory
  or appended-file sync). From then on every failure, in the same operation or a
  later write, is `Fatal`. The rule is `commitStepOf`, `Tx.run` and `Tx.fail`, and
  nowhere else.
- **Closing after the commit point without a result panics.** From the commit
  point on every failure of the `Tx` is fatal and is recorded, so a `Tx` closed
  there with no `Commit` and no recorded failure means the owner stopped for a
  reason of its own (a read that failed, an early return): the disk holds part
  of the transaction, memory shows none of it, and no error says so. `Close`
  panics, the process exits and recovery runs. A `Tx` that already panicked on a
  call of its own adds no second panic. An owner does every read it needs before
  `Begin`.
- **A deferred group.** `ReplaceDefer` replaces without syncing the directory.
  The files of a group are all in one directory and the very next write is the
  `SyncDir` of it. `NewTable` checks that every `ReplaceDefer` is declared
  immediately before a `SyncDir`; the `Tx` checks at run time that the group is
  one directory and that nothing, `Commit` included, comes before the sync.
- **Appends go through one handle and one role.** The `Tx` opens an appended
  file at its first append, keeps the handle, and syncs through it. The steps of
  the file are attributed to the role it was opened under, so `SyncFile` is
  declared under the role of its `Append` (a declaration repeats the role for
  that pair and for no other), and the `Tx` panics if a path is appended under
  two roles or synced under another role. A role has one `SyncFile`, which
  makes one path durable, so `Commit` panics, naming the role and the paths, when
  a role that made its `SyncFile` also appended to a path that sync did not
  cover: the other paths would be lost without a sign. A failed append or sync
  closes the handle at once; handles never outlive their `Tx`.
- **Reserved names.** `log` and `cleanup` are not valid transaction or role
  names (`fsx.ValidName`): `fsx` uses them for steps that are not crash points.
- **A `Tx` that made no write commits nothing.** `Commit` returns nil and
  reaches no point, so an owner that opened a transaction with nothing to do
  does not leave a commit point of a recovery transaction on a clean start. A
  declaration with a required write that was not made still panics: that is an
  incomplete transaction, not an empty one.
- **Cleanup is one `Recovery` transaction, run after the settle.** `Recover`
  removes the leftover files, then the leftover directories, then syncs each
  directory that held one. With nothing to remove the cleanup does nothing and
  reaches no point. `Recover` panics when the table has no cleanup or settle
  declaration, on a clean disk as well, so a composition root that left out
  `Internal()` fails at its first start.
- **Settle is one `Normal` transaction, run first at every start.** After a
  process crash a rename or a removal can be visible in a directory and not yet
  durable. What follows reads existence and acts on it durably: the recovery of
  an owner deletes what a pointer or a reference no longer names, and the cleanup
  removes the contents of a directory that `Discard` renamed to a garbage name. A
  power loss afterwards could bring the pointer or reference back to something
  that is gone, or bring the directory back under its original name with only
  some of its files, because the rename was not durable when the removal was.
  `Recover` therefore starts by syncing every directory under the data directory,
  the root included, once each, before the cleanup removes anything. Directories
  of debris are not synced: the rename that gave one its garbage name is in its
  parent, which is, and what is inside goes next. No order is needed for
  durability, since a directory sync covers that directory's entries only and a
  rename stays inside one directory; the order is parents before children, names
  sorted, so crash point numbers are stable. The cost is one directory sync per
  directory at every start. The transaction is `Normal`, so a clean start is not
  counted as a recovery.
  On Windows `fsx` does not flush a directory (`SyncDir` only checks that it
  exists), so this barrier, the `SyncDir` after a deferred group and the one after
  a batch of removals persist nothing there by themselves: the durability of
  directory entries on Windows rests on NTFS metadata journaling order and on
  `MOVEFILE_WRITE_THROUGH`, which no test here establishes. Pending verification
  by CI on main.

## What the caller must guarantee

- Acknowledge, and update memory, only after `Commit` returns nil, and do not
  read back a path written in the same `Tx`.
  Return the error a write or `Commit` returned, and no other error, once a
  write has passed its commit point; do every read before `Begin`.
- An `Append` that no `SyncFile` followed is not durable, even after `Commit`
  returns nil. Declare and make the `SyncFile` of every append the caller
  acknowledges as durable.
- On `IsFatal`, exit and rescan on restart. The package returns the class; the
  composition root exits.
- Build the table once, at the composition root, from `Internal()` and every
  owner's declarations, and check it with `NewTable` before anything runs.
- Call `Recover` at startup after taking the instance lock and before loading
  anything. Run every owner's recovery predicate only after `Recover` has
  returned nil: a predicate reads existence, and only after the settle is what
  it reads durable.
- Serialise the transactions of one declaration (the single writer loop does).
  Use a `Writer` from several goroutines only for different declarations.
- A transaction that writes the files of two owners belongs to the composing
  package: it declares the `Decl`, `Begin`s, passes the `*Tx` to each owner's
  write function, and commits. The owners write only their own directories and
  use role names they export. Two `Tx` would let the second fail after the first
  committed.
- The bytes appended are the caller's format. The package does not frame them;
  a format must let a reader resynchronise after a damaged record anywhere in the
  file, and a writer that cannot know whether the file ends in a partial record
  starts its first append of a `Tx` with a separator.
- The `Observer` must be safe for concurrent use and must not write through the
  `Writer`.

## Where the tests are

`TestTransactionTableIsValid` is the composition check; the program's own table
is checked the same way once the owners declare theirs. The crash-state tests
(`TestCrashStatesAtEveryPointOfAMultiWriteTransaction`,
`TestAcknowledgedWritesSurvivePowerLoss`), the class tests
(`TestFailureClassBeforeAndAfterTheCommitPoint`, `TestEachOperationHasItsCommitStep`),
the run time checks, and `TestRandomTransactionsHonourTheCommitPointOrderingAndDurability`
(a fixed-seed property test against a model of the disk written in the test)
make up the fast crash suite of this package.
