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
| `EncodeRecord`, `DecodeRecord` | The file of a record, and its validation in seven steps with a class of damage. |
| `OutRecord`, `InRecord`, `CtlRecord`, `EndpointRecord`, `AttemptsRecord`, `CheckpointRecord`, `AdminRecord`, `AlertRecord`, `SeqRecord` | The records, in the contract types of the other packages. |
| `OutKey`, `TargetKey`, `ParseKey`, `AlertID` | Keys and their canonical text. |
| `Store.Scan` | Every record of every directory, decoded, with the quarantined and the damaged files. |
| `Store.Intake`, `Deliver`, `Lease`, `Ack`, `Tick`, `PromptEnd`, ... | One method per transaction of the design; each encodes first, then opens its `Tx`. |
| `PutEndpoint`, `PutAdminResult` | Write functions for a transaction another package opens; roles `RoleEndpoint`, `RoleResult`. |
| `OrphanBodies`, `OrphanEndpoints`, `UnfinishedQuarantines`, `EffectReplays`, `DrainCandidates`, `PromptReplays`, ... | The predicates of the recovery rules, pure functions of a `Scan`. |
| `Store.Quarantine`, `QuarantineResume`, `QuarantineBody` | Quarantine; the alert is always the last write. |
| `SeqBook` | The next sequence number of a pair, built from a scan; `Note` after the commit. |
| `PlanSweep`, `Store.ArchiveSweep`, `RetainUntil` | What leaves the live files, and how it leaves. |
| `Store.ReadBody`, `Store.Available`, `Store.Settle` | Body reads checked against their names, free space, and the durability barrier of a start. |
| `MissingStateDirs`, `StateDirs` | The directories of `state/`, for the layout rule of the composition root. |

## Rules

- **Encode before opening a transaction.** A record that cannot be written is an
  error that touches nothing.
- **A record that replaces another is passed whole; a target record is written
  with the next revision.** The revision is the only thing a replay reads.
- **A head record lists its followers as it read them.** `PromptEnd` and `Blocked`
  refuse a follower that is not listed with the revision and acceptance time it was
  read with.
- **The alert is the last write of a quarantine.** Nothing a resumed quarantine
  writes is skipped because it seems done.
- **Bodies first, and removed last.** The caller works out which bodies lost their
  last reference (the reference count is in the engine's view) and says so; a body
  already gone is not an error.
- **A removal is checked.** The sweep refuses a name that is not a record file or a
  quarantined file of a directory of records.
- **A control record goes before the delivery derived from it** in a sweep, and a
  quarantined file before its replacement.

## What the caller must guarantee

- Build the table once with `txn.Internal()` and `Transactions()`; call
  `Writer.Recover`, then the layout rule, then `Settle`, then `Scan`, and run the
  recovery rules in the order of the startup.
- Acknowledge and update memory only after a method returns nil; on `txn.IsFatal`
  exit and rescan.
- Call `SeqBook.Note` only after the commit that wrote the number.
- Run a drain, a prompt or block follower write, and a body quarantine in chunks of
  the engine's choosing; the store accepts any size.
- Give `Quarantine` a replacement only for a delivery record whose first line was
  readable, built with the delivery package's constructor for a lost record.
