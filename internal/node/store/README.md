# store

The durable records of `state/`: deliveries and their bodies, endpoint state,
checkpoints, results, alerts, the archive and the quarantine. Read
[../../../docs/references/storage/README.md](../../../docs/references/storage/README.md)
for the formats, the reasons and what the tests prove.

This is a runtime package of layer 3 of [`internal/node/README.md`](../README.md):
it imports `txn`, `recfile` and the contract packages, and from `fsx` and `clock`
only types. Every write is a transaction of `txn`, declared in `Transactions()`;
every read goes through the reader of the `txn.Writer`. It keeps no index and no
clock: `Scan` returns every decoded record, times are parameters, and randomness
(alert ids) is the caller's.

It decides nothing about deliveries. It encodes and decodes, orders the writes of
each transaction, and offers the predicates of the recovery rules it owns; the
engine applies the delivery rules and calls the writes.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `New(w)`, `Transactions()` | One `Store` over the program's writer; the declarations the composition root adds to its table. |
| `EncodeRecord`, `DecodeRecord` | The file of a record, and its validation in seven steps with a class of damage. The seventh step asks the delivery package whether every delivery record in the body is one its rules can produce (`Record.Valid()`), and the same question is asked before a record is written. A message id in a key is judged by `envelope.ValidMessageID`. |
| `OutRecord`, `InRecord`, `CtlRecord`, `EndpointRecord`, `AttemptsRecord`, `CheckpointRecord`, `AdminRecord`, `AlertRecord`, `SeqRecord` | The records, in the contract types of the other packages. |
| `OutKey`, `TargetKey`, `ParseKey`, `AlertID` | Keys and their canonical text. |
| `Store.Scan` | Every record of every directory, decoded, with the quarantined and the damaged files. |
| `Store.Intake`, `Deliver`, `Lease`, `Ack`, `Tick`, `PromptEnd`, ... | One method per transaction of the design; each encodes first, then opens its `Tx`. |
| `PutEndpoint`, `PutAdminResult` | Write functions for a transaction another package opens; roles `RoleEndpoint`, `RoleResult`. |
| `OrphanBodies`, `OrphanEndpoints`, `UnfinishedQuarantines`, `EffectReplays`, `DrainCandidates`, `PromptReplays`, ... | The predicates of the recovery rules, pure functions of a `Scan`. |
| `Store.Quarantine`, `QuarantineResume`, `QuarantineBody` | Quarantine; the alert is always the last write. |
| `SeqBook` | The next sequence number of a pair, built from a scan; `Note` after the commit. |
| `PlanSweep`, `SweepPlan.Chunk`, `Store.ArchiveSweep`, `RetainUntil` | What leaves the live files, and how it leaves. |
| `Store.ReadBody`, `Store.Available` | Body reads checked against their names, and free space. |
| `MissingStateDirs`, `StateDirs` | The directories of `state/`, for the layout rule of the composition root. |

## Rules

- **Encode before opening a transaction.** A record that cannot be written is an
  error that touches nothing.
- **A record that replaces another is passed whole; a target record is written
  with the next revision.** The revision is the only thing a replay reads.
- **A head record lists its followers as it read them.** `PromptEnd` and `Blocked`
  refuse a follower that is not listed with the revision and acceptance time it was
  read with.
- **The alert is the last write of a quarantine.** A resumed quarantine writes what
  the first pass wrote, in the same order (floors, the replacement under the same
  name, the closed session, the alert last); nothing is skipped because it seems
  done.
- **Bodies first, and removed last.** The caller works out which bodies lost their
  last reference (the reference count is in the engine's view) and says so; a body
  already gone is not an error.
- **A quarantined file's first line is trusted by the rule of a live file, once.**
  The scan and `DecodeRecord` share one function for it (kind held by the
  directory, name equal to the hash of kind and key, key parses); a first line
  that parses but does not match the name the file had leaves `CorruptFile.Key` nil.
- **A header-unreadable quarantined file of a delivery directory is swept after a
  lost record's retention.** With no replacement, nothing but the time of its
  alert says when it may go: the retention of a lost record found then (from
  `delivery.LostReportBy`) is the bound. A file with a record under its name goes
  with that record.
- **Existence is read before the transaction opens.** A call that removes files
  asks the disk which are there first and only consults the answer while it
  writes: a read that failed after the commit point would be an ordinary error on
  a disk that already changed.
- **A removal is checked.** The sweep refuses a name that is not a record file or a
  quarantined file of a directory of records.
- **A control record goes before the delivery derived from it** in a sweep, and a
  quarantined file before its replacement.
- **Floors are written before records are removed, and `ArchiveSweep` works them
  out.** A chunk carries the source records it removes (`SweepChunk.Outs`), not
  their file names; the highest number each used for each target is written first,
  merged into the floor file and never lowering it. A caller has no floors to pass
  and none to leave out.
- **A closed session endpoint waits for its source records.** `PlanSweep` keeps it
  while any source record (whole, lost, or damaged with a trusted key) has it as
  sender; the sweep after the one that removed the last takes the endpoint with its
  attempts, checkpoints and floor file. Its record (`SweepChunk.Endpoints`) is
  removed last, after the directories of the others are durable, so a crash cannot
  leave a file that only the endpoint record would have led a sweep to.
- **A write that meets a damaged floor file does nothing** and returns a
  `*DamagedFileError`; the caller quarantines the file it names and tries again.
  `Scan.Damaged` lists floor files first, so a startup meets none after it began to
  quarantine source records.
- **Only the system endpoint's notices omit the verdict of the authorization check
  and the reply permission,** in the encoder and in the decoder.

## What the caller must guarantee

- Build the table once with `txn.Internal()` and `Transactions()`; call
  `Writer.Recover`, then the layout rule, then `Scan`, and run the recovery rules
  in the order of the startup. The rules run only after `Recover` has returned
  nil: it starts by making every directory durable, so what a rule reads is what a
  power loss cannot take back, and the store has no such barrier of its own.
  `txn.Writer.Begin` panics for the store's transactions until it has.
- Acknowledge and update memory only after a method returns nil; on `txn.IsFatal`
  exit and rescan.
- Call `SeqBook.Note` only after the commit that wrote the number.
- Run a drain, a prompt or block follower write, and a body quarantine in chunks of
  the engine's choosing; the store accepts any size. A sweep is cut from the groups
  of `SweepPlan` (`SweepPlan.Chunk` is the whole plan as one chunk): a source record
  is cut as a record, never as a file name, so its floor goes with it.
- Quarantine the files of `Scan.Damaged` in the order listed, and on a
  `*DamagedFileError` quarantine the file it names and try again.
- Give `Quarantine` and `QuarantineResume` a replacement only for a delivery
  record whose first line was trusted (`DamagedFile.Key`, `CorruptFile.Key` set),
  built with the delivery package's constructor for a lost record.
- On a body that does not hash to its name, replace a record with a lost record
  only when `delivery.Record.NeedsBody()` says it still needs the bytes (the
  replacement comes from `delivery.NewLostRecord`, and on the target side
  `delivery.NewLostDedupRecord`); any other record keeps its state and only drops
  the reference to the body.
