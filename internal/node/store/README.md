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
| `Store.Intake`, `Deliver`, `Lease`, `Ack`, `Tick`, `PromptEnd`, ... | One method per transaction of the design; each encodes first, then opens its `Tx`. A write of target records returns them as written. |
| `PrepareEndpoint`, `PrepareAdminResult`, `PutEndpoint`, `PutAdminResult` | The two halves of a write into a transaction another package opens (roles `RoleEndpoint`, `RoleResult`): the prepare step validates and encodes and runs before `Begin`; the put step writes the prepared value and can fail only as a write of the transaction does. |
| `OrphanBodies`, `OrphanEndpoints`, `UnfinishedQuarantines`, `EffectReplays`, `DrainCandidates`, `PromptReplays`, ... | The predicates of the recovery rules, pure functions of a `Scan`. |
| `Store.Quarantine`, `QuarantineResume`, `QuarantineBody`, `QuarantineSessions` | Quarantine; the alert is always the last write. `QuarantineSessions` is the session endpoints a control record's quarantine closes, so a caller can tell whether a session may remain (the kind of the alert); the store writes the same list itself. |
| `SeqBook` | The next sequence number of a pair, built from a scan; `Note` after the commit. |
| `PlanSweep`, `SweepPlan.Chunk`, `Store.ArchiveSweep`, `RetainUntil` | What leaves the live files, and how it leaves. |
| `Store.ReadBody`, `Store.Available` | Body reads checked against their names, and free space. |
| `MissingStateDirs`, `StateDirs` | The directories of `state/`, for the layout rule of the composition root. |

## Rules

- **Encode before opening a transaction.** A record that cannot be written is an
  error that touches nothing.
- **A record that replaces another is passed whole; a target record is written
  with the next revision.** The revision is the only thing a replay reads.
- **A write that changes the stored form of a target record returns it as
  written.** `Deliver`, `DeliverReply`, `PromptEnd`, `Blocked`, `Lease`, `Ack`,
  `Tick`, `EndpointDrain`, `InstanceDrain`, `EndpointDrainReplay`,
  `InstanceDrainReplay`, `PromptReplay`, `BlockedReplay` and `QuarantineBody`
  return `[]InRecord` (the records given, one revision on, in the same order) and
  `FeedbackForm` returns the
  `InRecord`. A caller keeping a view stores what comes back and never works the
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
  record alone does not say which endpoint is the live one. A quarantined or
  damaged record keeps the endpoint for the quarantine.
- **The alert is the last write of a quarantine.** A resumed quarantine writes what
  the first pass wrote, in the same order (floors, the replacement of a source
  record under the same name, the closed sessions, the alert last); nothing is
  skipped because it seems done.
- **A quarantine closes every creating endpoint of the damaged control record,
  and the store decides which.** A control record accepted again after a failed
  first acceptance has the same name and a new endpoint, so several session
  endpoints still being created can name one record as their creator.
  `Quarantine` and `QuarantineResume` take the list from the `Scan` they are given
  (`QuarantineSessions`: every session endpoint whose creator is the record and
  that is not open, written closed with its token dropped, in the `session` role
  declared as many), and a caller cannot pass another: one left behind would hold
  one of the node's endpoint slots for good, because the rules that remove an
  endpoint leave a creating one alone while its record is quarantined or
  damaged, and one wrongly closed (an open endpoint, or another record's) would be
  a live session whose deliveries a drain then drops. A control record is refused
  without the scan.
- **Bodies first, and removed last.** The caller works out which bodies lost their
  last reference (the reference count is in the engine's view) and says so; a body
  already gone is not an error. `DeliverReply` takes the release like the other writes that can drop a body:
  the evidence in a reply can finish a delivery. The caller counts the records the
  transaction writes as well as the ones it replaces, because records that say the
  same bytes share one body file; a write refuses (`ErrReleasedBodyInUse`, before
  anything is written) a release that names a body one of its own records refers to.
- **A quarantined file's first line is trusted by the rule of a live file, once.**
  The scan and `DecodeRecord` share one function for it (kind held by the
  directory, name equal to the hash of kind and key, key parses); a first line
  that parses but does not match the name the file had leaves `CorruptFile.Key` nil.
- **A header-unreadable quarantined file of `out/` is swept after a lost record's
  retention.** With no replacement, nothing but the time of its alert says when it
  may go: the retention of a lost record found then (from `delivery.LostReportBy`)
  is the bound. A file with a record under its name goes with that record. A
  quarantined file of `in/` or `ctl/` has no replacement and no bound, because a
  target keeps no loss: nothing removes it, and its alert stays with it (the sweep
  keeps an alert while its file is there).
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
  1. Quarantine of the damaged files (`Scan.Damaged`, in the order listed), then
     the resumption of unfinished quarantines, then the resumption of unfinished
     body quarantines. A quarantine writes replacement records, raises floors and
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
  6. `orphan-gc` last: the replays and the quarantines add and drop references to
     bodies, so only after all of them is a body without a reference an orphan.
  7. The sequence book, from a scan taken after all of the above: a quarantine
     raises floors and replaces records, and the book must see both.
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
- Run a drain, a prompt or block follower write, and a body quarantine in chunks of
  the engine's choosing; the store accepts any size. A sweep is cut from the groups
  of `SweepPlan` (`SweepPlan.Chunk` is the whole plan as one chunk): a source record
  is cut as a record, never as a file name, so its floor goes with it.
- Quarantine the files of `Scan.Damaged` in the order listed, and on a
  `*DamagedFileError` quarantine the file it names and try again.
- Give `Quarantine` and `QuarantineResume` a replacement only for a source record
  (`out/`) whose first line was trusted (`DamagedFile.Key`, `CorruptFile.Key` set),
  built with the delivery package's constructor for a lost record. A target or
  control record has none: a target keeps no loss, and the store refuses one.
- On a body that does not hash to its name, replace a source record with a lost
  record only when `delivery.Record.NeedsBody()` says it still needs the bytes (the
  replacement comes from `delivery.NewLostRecord`); any other record keeps its
  state and only drops the reference to the body. A target record has no
  replacement, so `QuarantineBody` refuses one that still needs the bytes and no
  longer refers to them (`ErrBodyStillNeeded`, before anything is written): the
  caller ends that delivery by the delivery rules first. A control record never
  needs a body.

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
  finishes first.
- The coverage assertions derive from the same selection: every declared point is
  crashed at in both kinds by the walk of a workflow, and every point of a
  recovery rule is crashed at during a recovery in both kinds. The head of a body
  quarantine (the rename of the damaged body) is not in the second set: the read
  that finds the body damaged writes it, and a recovery only continues the chunks
  after it.
