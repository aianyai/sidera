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
(the id of an alert the caller raises) is the caller's.

It decides nothing about deliveries. It encodes and decodes, orders the writes of
each transaction, and offers the predicates of the recovery rules it owns; the
engine applies the delivery rules and calls the writes.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `New(w)`, `Transactions()` | One `Store` over the program's writer; the declarations the composition root adds to its table. |
| `EncodeRecord`, `DecodeRecord` | The file of a record, and its validation in seven steps with a class of damage. The seventh step asks the delivery package whether every delivery record in the body is one its rules can produce (`Record.Valid()`), and the same question is asked before a record is written. A message id in a key is judged by `envelope.ValidMessageID`. |
| `OutRecord`, `OutLostRecord`, `InRecord`, `CtlRecord`, `EndpointRecord`, `AttemptsRecord`, `CheckpointRecord`, `AdminRecord`, `AlertRecord`, `SeqRecord` | The records, in the contract types of the other packages. |
| `OutKey`, `TargetKey`, `ParseKey`, `AlertID` | Keys and their canonical text. |
| `Store.Scan` | Every record of every directory, decoded, with the quarantined and the damaged files. |
| `Store.Intake`, `Deliver`, `Lease`, `Ack`, `Tick`, `PromptEnd`, ... | One method per transaction of the design; each encodes first, then opens its `Tx`. A write of target records returns them as written. |
| `PrepareEndpoint`, `PrepareAdminResult`, `PutEndpoint`, `PutAdminResult` | The two halves of a write into a transaction another package opens (roles `RoleEndpoint`, `RoleResult`): the prepare step validates and encodes and runs before `Begin`; the put step writes the prepared value and can fail only as a write of the transaction does. |
| `OrphanBodies`, `OrphanEndpoints`, `EffectReplays`, `DrainCandidates`, `PromptReplays`, ... | The predicates of the recovery rules, pure functions of a `Scan`. |
| `Store.Quarantine`, `QuarantineBody`, `QuarantineSessions` | Settling a damaged file, or the record whose body is damaged, in one transaction: the effects first, the alert, and the damaged file last. `QuarantineSessions` is the session endpoints a control record's quarantine closes, so a caller can tell whether a session may remain (the kind of the alert); the store writes the same list itself. |
| `SeqBook` | The next sequence number of a pair, built from a scan; `Note` after the commit. |
| `NewOutLostRecord`, `OutLostKey`, `OutLostReplays`, `Store.OutLostClear` | The notice that a file of `out/` was lost unread, built in one place; the work of the replay that clears its feedback item once a notice covers it, and the write that does. |
| `PlanSweep`, `SweepPlan.Chunk`, `Store.ArchiveSweep`, `RetainUntil` | What leaves the live files, and how it leaves. |
| `BodyFileName`, `Store.ReadBody`, `Store.Available` | The name of a record's own body file, a body read checked against the digest and length its record holds, and free space. |
| `MissingStateDirs`, `StateDirs` | The directories of `state/`, for the layout rule of the composition root. |

## Rules

- **Encode before opening a transaction.** A record that cannot be written is an
  error that touches nothing.
- **A record that replaces another is passed whole; a target record is written
  with the next revision.** The revision is the only thing a replay reads.
- **A write that changes the stored form of a target record returns it as
  written.** `Deliver`, `DeliverReply`, `PromptEnd`, `Blocked`, `Lease`, `Ack`,
  `Tick`, `EndpointDrain`, `InstanceDrain`, `EndpointDrainReplay`,
  `InstanceDrainReplay`, `PromptReplay` and `BlockedReplay`
  return `[]InRecord` (the records given, one revision on, in the same order),
  `FeedbackForm` returns the `InRecord`, and `QuarantineBody` returns a pointer to
  the one it wrote, nil when it removed the record. A caller keeping a view stores what comes back and never works the
  revision out itself: the rule has this one home. After an error nothing is
  returned, and the caller does not apply the write to its view. The records given
  are not changed, but the records returned are shallow copies of them: slices,
  maps and pointers inside a record are shared with the one given, so neither is
  modified in place. No other write sets a field of a record it is given.
- **An endpoint's display name and description are the adapter's, held to its
  bounds.** `EndpointRecord.DisplayName` and `Description` are optional, omitted
  when empty, and limited to `adapterproto.MaxDisplayName` and
  `adapterproto.MaxDescription` characters of valid UTF-8, on write and on read.
  They are not part of the engine-owned `Declaration`, and the record does not
  apply the tighter bound of the node's declaration to peers
  (`publication.MaxNameLen`): whoever builds that declaration decides what happens
  to a longer name.
- **A head record lists its followers as it read them.** `PromptEnd` and `Blocked`
  refuse a follower that is not listed with the revision and acceptance time it was
  read with.
- **A session endpoint is prepared for the creation its control record holds.**
  `ControlIntake` refuses an endpoint that the record's intent does not name, and
  `OrphanEndpoints` names a creating endpoint whose control record holds no
  creation of it: an intake that failed before its commit point leaves its endpoint,
  and the signal accepted again has the same record name and a new endpoint, so the
  record alone does not say which endpoint is the live one. A damaged record keeps
  the endpoint for its quarantine.
- **The damaged file goes last, and every effect before it can be written
  again.** `Quarantine` writes the floors it raises, the session endpoints it
  closes, the out-lost record that tells the sender of a source file lost unread,
  and the alert, each durable before the next, and only then removes the
  file or overwrites it in place (a source record with its lost replacement, a
  floor file with the floors its records say). A file that is still there after a
  crash is settled again by the next start, so nothing records how far a
  settlement got and there is nothing to resume; what the repetition can add is a
  higher floor (a gap, never a number given twice). The alert id is the store's, so
  the repetition rewrites the alert and adds no second: it is derived in one place
  (`damageAlert`) from the directory and name of the file and the bytes that show
  the damage (for `QuarantineBody`, the record and the name of its body), so a
  later, different damage of a file of the same name is an alert of its own and
  never replaces one that was acknowledged. What the file held is not kept.
- **A quarantine closes every creating endpoint of the damaged control record,
  and the store decides which.** A control record accepted again after a failed
  first acceptance has the same name and a new endpoint, so several session
  endpoints still being created can name one record as their creator.
  `Quarantine` takes the list from the `Scan` it is given
  (`QuarantineSessions`: every session endpoint whose creator is the record and
  that is not open, written closed with its token dropped, in the `session` role
  declared as many), and a caller cannot pass another: one left behind would hold
  one of the node's endpoint slots for good, because the rules that remove an
  endpoint leave a creating one alone while its record is damaged, and one
  wrongly closed (an open endpoint, or another record's) would be
  a live session whose deliveries a drain then drops. A control record is refused
  without the scan.
- **A body belongs to one record, is written first and is removed last.** The body
  file is named after its record (`BodyFileName`), so `Deliver` to several recipients
  writes a copy of the message for each target record that holds a reference, and
  nothing is shared or counted. A write refuses, before anything is written, a record
  that holds a reference to bytes not given with it, bytes no record refers to, and
  bytes that are not what the reference says. The writes that can end a record's need
  of its body (`SourceUpdate`, `DeliverReply`, `ControlResult`, `ControlReply`, `Ack`,
  `Tick`, the drains, `FeedbackClear` and its replay) take no list of bodies: a record
  written with no reference has its own body file removed, after the records and
  their directory are durable, in the same transaction, if the file is there (a body
  already gone is not an error). The sweep removes the records, makes their directories
  durable, and only then removes the bodies and syncs `bodies/`. So a crash, a power
  loss included, never leaves a record pointing at a body that is gone. The body of a
  record that holds no reference, or that does not exist, is an orphan.
- **A record that still needs its body is never written without a reference to it.**
  Writing it would remove the body, and the record would be left with nothing to hand
  out or send again, silently. `ErrBodyDropped` refuses it before anything is written:
  an `InRecord` whose delivery needs the body (`NeedsBody`), and an `OutRecord` that
  had a body (`Outbound.BodyNonEmpty`) and still has a queued delivery. The one place
  that says so is `checkBodyKept`, called by `recordFile` for every write.
- **A damaged file's first line is trusted by the rule of a live file.**
  The scan and `DecodeRecord` share one function for it (kind held by the
  directory, name equal to the hash of kind and key and, in `out/`, the sender of
  the key before it, key parses); a first line
  that parses but does not match the name of the file leaves `DamagedFile.Key` nil,
  and nothing is built from the key it claims.
- **A record whose body is damaged is settled by its own call.** The body file is
  checked against its record's digest and length when it is read, never at the start.
  `QuarantineBody` takes the one record that owns it and decides by the delivery rules the store
  already holds (`NeedsBody`): a record that still needs the bytes is lost with them
  (a target record is removed after its alert, a source record is overwritten with
  its lost replacement after its floors are raised), any other record keeps its
  state and is overwritten without the reference. A replacement is given for a
  source record that needs the body and for no other, and a call that gets this
  wrong is refused before anything is written. The body file is not touched: once
  the record is settled it is an orphan.
- **Existence is read before the transaction opens.** A call that removes files
  asks the disk which are there first and only consults the answer while it
  writes: a read that failed after the commit point would be an ordinary error on
  a disk that already changed.
- **A removal is checked.** The sweep refuses a name that is not a record file.
- **A control record goes before the delivery derived from it** in a sweep.
- **Floors are written before records are removed, and `ArchiveSweep` works them
  out.** A chunk carries the source records it removes (`SweepChunk.Outs`), not
  their file names; the highest number each used for each target is written first,
  merged into the floor file and never lowering it. A caller has no floors to pass
  and none to leave out.
- **A closed session endpoint waits for its source records.** `PlanSweep` keeps it
  while any source record (whole, lost, or damaged with a trusted key) or out-lost
  record has it as sender; the sweep after the one that removed the last takes the endpoint with its
  attempts, checkpoints and floor file. An out-lost record is swept as soon as its
  item is gone or its sender is closed or has no record, since a closed endpoint
  can never be handed the notice: otherwise it would keep the endpoint for good.
  Its record (`SweepChunk.Endpoints`) is
  removed last, after the directories of the others are durable, so a crash cannot
  leave a file that only the endpoint record would have led a sweep to.
- **A write that meets a damaged floor file does nothing** and returns a
  `*DamagedFileError`; the caller settles the file it names and tries again.
  `Scan.Damaged` lists floor files first, so a startup meets none after it began to
  quarantine source records.
- **A record of exempt traffic omits the verdict of the authorization check and
  the reply permission, and every other record holds both,** in the encoder and in
  the decoder. The record's own `delivery.Record.Exempt` says which it is (stored
  as `exempt` in its delivery), and it must agree with the sender: the system
  endpoint sends exactly the exempt records.

## What the caller must guarantee

- Build the table once with `txn.Internal()` and `Transactions()`; call
  `Writer.Recover`, then the layout rule, then `Scan`, and run the recovery rules
  in the order below. The rules run only after `Recover` has returned
  nil: it starts by making every directory durable, so what a rule reads is what a
  power loss cannot take back, and the store has no such barrier of its own.
  `txn.Writer.Begin` panics for the store's transactions until it has.
- Run the recovery rules in this order, each on a scan taken after the writes of
  the one before it (or on an equivalent view of what was committed), and repeat
  the whole sequence until no rule has work:
  1. Quarantine of the damaged files (`Scan.Damaged`, in the order listed). A
     quarantine overwrites source records with their replacements, raises floors and
     closes session endpoints, and every later rule reads what it left.
  2. `ctl-effect-replay`, then `instance-drain-replay`: both close endpoints.
  3. `orphan-gc-endpoint`, before `endpoint-drain-replay`: a record queued for an
     endpoint that is removed is then drained as a record for an endpoint that does
     not exist, which a power loss that reordered the writes can leave.
  4. `endpoint-drain-replay`, after every rule that closes or removes an endpoint,
     so it sees the final set of closed endpoints.
  5. `prompt-replay`, `blocked-replay` and `feedback-replay`, after the drain by
     convention: they skip a record a drain already rewrote (its revision no longer
     matches what the head lists), so neither their order among themselves nor
     their order against the drain changes the result.
  6. `orphan-gc` last: the quarantines remove records and overwrite them without
     their reference, so only after all of them is the body of a record that is
     gone, or that holds no reference, an orphan.
  7. The sequence book, from a scan taken after all of the above: a quarantine
     raises floors and replaces records, and the book must see both.
  A rule whose write has the shape of a normal transaction is that transaction, run
  again, with no declaration of its own: the drains use `endpoint-drain` and
  `instance-drain`, the prompt and block replays `lease`, the feedback replay
  `feedback-clear`. The feedback
  replay has two predicates, `FeedbackReplays` for source records and
  `OutLostReplays` for out-lost ones, and the same transaction writes both.
  Only `quarantine`, `orphan-gc`, `orphan-gc-endpoint` and
  `ctl-effect-replay` are `Recovery` declarations.
  Each rule's predicate is a function of the scan it is given: a rule run on a scan
  taken before the previous rule wrote would act on records that rule has already
  settled.
- In a transaction that writes the files of another owner as well, call
  `PrepareEndpoint` and `PrepareAdminResult` before `Begin` and only `PutEndpoint`
  and `PutAdminResult` inside it: validation that fails after the commit point of
  the transaction would leave the disk changed with no result to return, and
  `Tx.Close` panics for it.
- Acknowledge and update memory only after a method returns nil; on `txn.IsFatal`
  exit and rescan.
- Call `SeqBook.Note` only after the commit that wrote the number.
- Run a drain or a prompt or block follower write in chunks of the engine's
  choosing; the store accepts any size. A sweep is cut from the groups
  of `SweepPlan` (`SweepPlan.Chunk` is the whole plan as one chunk): a source record
  is cut as a record, never as a file name, so its floor goes with it.
- Quarantine the files of `Scan.Damaged` in the order listed, and on a
  `*DamagedFileError` quarantine the file it names and try again.
- Give `Quarantine` a replacement only for a source record (`out/`) whose first
  line was trusted (`DamagedFile.Key` set), built with the delivery package's
  constructor for a lost record. A target or control record has none: a target
  keeps no loss, and the store refuses one. Give the alert as a `QuarantineAlert`,
  which has no id: the store derives it.
- Give `Quarantine` the scan and `Node`, the node's own id, for every file of
  `out/`, whatever its damage: the store decides from the name of a file it cannot
  read, and from the scan, whether its sender is an endpoint of this node to be told
  by an out-lost record, and a call that needs either and lacks it is refused. The
  discovery time of that record is the time of the alert. Loading the item of an
  out-lost record into the feedback queue, giving it to the sender and clearing it
  with `OutLostClear` is the caller's.
- On a body that is not what its record says (`ErrBodyMismatch`), call
  `QuarantineBody` for that record, with a replacement from `delivery.NewLostRecord`
  exactly when `OutRecord.NeedsBody()` says the source record still needs the bytes. A
  control record never needs a body.

## Crash tests

`crash_test.go` and `scenarios_test.go` walk every crash point of every workflow of
the store. They prove what the reliability floor needs, and nothing more: an
acknowledged write survives a power loss that keeps only synced content; after any
crash, recovery ends and leaves every record valid; a crash during recovery is
redone.

- At each point a walk takes two crashes: the process crash and the worst power
  loss (`crashKinds`). A random power loss only keeps a subset of the one group of
  writes that is not synced yet, between those two, so a transaction of `atomic`
  mode takes none; a `batch` is a set of independent single-file transitions,
  where any subset of the written files is a state of its own, and also takes the
  two seeded power losses (not under the race detector, which adds cost and no
  coverage to a single-threaded walk). The seeded power losses of the property tests
  (`powerloss_test.go`, `property_test.go`) are the other place they remain.
- The crash during recovery is not walked from every crash state. Each point of a
  recovery rule (the recovery transactions of the store, and the cleanup and settle
  that `txn.Recover` runs at every start) is reached by the recovery of some crash
  state; the first such state in the order of the walks is crashed again at every
  point of its recovery, in both kinds of crash (`runNestedWalks`). The order is
  fixed by the seed of the scenario, so the choice does not depend on which walk
  finishes first. A replay that writes under the declaration of a normal
  transaction (the drains, the prompt and block replays under `lease`, the feedback
  replay under `feedback-clear`) is outside that domain: a crash in the middle of
  it leaves the disk the walk of that declaration crashes at, so that walk already
  covers it.
- The coverage assertions derive from the same selection: every declared point is
  crashed at in both kinds by the walk of a workflow, and every point of a
  recovery rule is crashed at during a recovery in both kinds.
