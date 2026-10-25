# engine

The single writer of the node's runtime state. One loop owns the in-memory view
of every record and runs, one after another, every command that changes state;
each command makes its change durable through the transaction layer first,
updates memory second and answers last. Read
[../../../docs/references/engine/README.md](../../../docs/references/engine/README.md)
for the reasons and for what the tests prove.

This is a runtime package of layer 5 of [`internal/node/README.md`](../README.md):
it imports the contract packages, `txn`, `store`, `config` and `clock`, and
reaches the store, the configuration, the transaction writer and the adapter host
through the interfaces of `deps.go`. It does no file I/O of its own, reads no
clock but the injected one (the waits that are about real time name
`clock.System()`), and draws randomness only from `Deps.Rand`.

This package holds the loop, the scheduler, the registration of endpoints, the
capacity check, the storage of alerts, the two recovery entries, and the sending
and taking in of messages: the acceptance of a message from a local endpoint, the
outbound worker that carries it through the transport and applies what comes
back, and the receiving side that judges an envelope and signs the reports.
Prompts, pulling, control signals and feedback join the same loop as further
commands and add their methods to this list.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Open(ctx, Deps, Params)` | The scan entry: scans `state/`, runs the recovery rules until none has work, builds the view. Returns a `ScanReport`. |
| `Start`, `RecoverDelivery`, `Serve`, `BeginShutdown`, `Stop`, `Done`, `Err`, `Phase` | The life of the loop: opened, recovering, running, stopping, stopped or failed. `RecoverDelivery` is the second recovery entry; the table of the start steps is in the reference. |
| `AddPeriodic`, `AddClockState`, `Task`, `TaskInput` | A task the composition root has the loop run every period, before `Start`. The clock state's task is also run at once by the command that finds the wall clock set back. |
| `Quiesce` | Returns when no command waits, nothing is due and no goroutine of the engine runs. |
| `AdapterAdmitted`, `RegisterDirect`, `UnregisterDirect` | What the adapter host calls: the admission of an instance, and the registration and unregistration of its direct endpoints. |
| `Send`, `Reply`, `SendRequest`, `ReplyRequest`, `SendResult`, `DefaultContentType` | Accept a message from the endpoint a caller is: the body and the source record are on disk before the call returns, and what the recipients' nodes make of it is learned from the state of the deliveries. |
| `MessageStatus` | The state of each delivery of a message the caller's endpoint sent, from the sender's record. Another endpoint's message is unknown to the caller. The reply of a control signal is not part of it yet. |
| `SetTransport`, `Transport`, `Keys` | The transport the outbound worker calls (the narrow interface with the signatures of `*transport.Loopback`) and the node's keys, which sign the reports. Both are for the composition root; an engine without them refuses to send and to take in. |
| `Inbound`, `Pull` | The engine as the `transport.Handler`: an envelope for this node, answered with signed reports, and a pull for the reports of a message. |
| `Declare`, `Caller`, `ByToken`, `ByAdapter` | The declaration of an endpoint, by whoever the endpoint's own caller is, resolved in the command. |
| `Endpoints`, `CloseEndpoint`, `CloseRequest`, `CloseResult`, `Counts`, `Alerts`, `RaiseAlert` | The engine side of `endpoint list` and of a close the engine's own features ask for, the numbers of the status, and the alerts. |
| `Execute`, `ManagementCommand`, `StoredResult`, `Operation`, `CloseEndpointOp`, `StopDaemonOp` | The one door of every administrative operation of the owner: the replay check, the operation, and the storing of its result with the fingerprint are one command of the loop. The owner's request to close an endpoint and to stop the daemon are the two operations there are; the package that owns the effect of another adds its type here. |
| `OperationResult` | A read: the result stored for a request key, from either place a result is kept, and whether there is one. Taken while the node starts; writes nothing. |
| `PlanRecovery`, `RecoveryPlan` | The work of the recovery rules as a pure function of a scan. |
| `Reason`, `Refusal`, `ReasonOf` | The closed set of reasons a method refuses for; the interface packages alone map them to the wire. |
| `Transactions()` | The declarations of the transactions of two owners that the engine opens: `direct-register`. |
| `AgentService`, `ManageService` | What the Agent and management interfaces ask of the engine; `*Engine` implements them, and the methods are added as the engine learns to do them. Everything of `ManageService` that changes state is `Execute`; the other methods read. |
| `Store`, `Config`, `Beginner`, `Adapters` | What the engine asks of the store, the configuration, the transaction writer and the adapter host. |

## Rules

- **Workers start only in the loop.** `spawn` is called from a command or a due
  entry, never from another goroutine: `finish` waits for the workers on the loop's
  goroutine, and a worker started concurrently with that wait could be missed. It
  panics when the loop is not running a command.
- **One loop, typed commands, the order of the inbox.** Every command is a type
  with a `run`; commands run in the order they entered the inbox. Commands a
  command queues for itself (the chunks of a long batch) run after the commands
  the inbox holds at that moment and before those that arrive later, so a busy
  inbox neither starves them nor is held up by more than one chunk. A command is answered exactly once, on a channel of capacity one,
  so the loop never waits for a caller that has gone.
- **A command that entered the inbox is run, whoever waits for it.** A caller
  whose context ends first learns only that it did not hear.
- **Two kinds of submitter.** An outside caller waits `Params.SubmitWait` for
  room in a full inbox and is then refused with `ReasonLoopBusy`. An internal one
  (the adapter host, the engine's workers, the recovery entry) waits, because its
  order must be kept and its back pressure is the buffer behind it.
- **One door for administrative operations, and the order is the engine's.**
  `Execute` is a command of the loop: the identifier of the request is looked up
  in every stored result, a resend of the same request returns the stored result
  and runs nothing, the identifier of another request is refused with
  `ReasonOperationIDReused`, and otherwise the operation runs and its result is
  stored with the fingerprint in the transaction of its effect. The lookup covers
  both places a result is kept (`state/admin/` and the operation each versioned
  file of `config/` records), loaded at `Open`; no operation does a replay check of
  its own and no interface does one before the engine. A refused request stores
  nothing, so its identifier stays free. The expected version of a versioned
  setting is handed to the operation, and the check is inside the change that
  writes the file, by the package that owns it (`admin.CheckVersion` in
  `config`), because the check and the write are one step; it is not made a second
  time here. `StoredResult.Stop` follows the operation, so a resend of a request
  that stops the daemon asks for the stop again.
- **One reading of the clock per command**, observed against the high water mark
  before anything else. A wall clock set back beyond the tolerance pauses new
  work that depends on a deadline until the owner clears it; moving the clock on
  does not clear it. The pause is made durable by the command that finds it.
- **Disk first, memory second.** Everything that can be refused is checked before
  `Begin`. A write that failed before its commit point was acknowledged to no one
  and changed nothing: `ReasonStorageFull` for a full volume, otherwise
  `ReasonStorageUnavailable`, and memory stays as it was. A fatal error stops the
  loop for good (`PhaseFailed`, `Err`), answers the command `ReasonStorageFatal`
  and refuses the rest; the composition root exits and the next start rescans.
  Whether a volume is full is `fsx.IsNoSpace`'s to say, not the engine's.
- **The cap of endpoints is the node's.** An endpoint that is open or being
  created, of any kind, takes a place against `publication.MaxEndpointsPerNode`;
  the system endpoint is not registered and takes none. The adapter's own limit
  is judged first and says which limit was hit.
- **The same key is the same endpoint.** The id is kept in `config/` before the
  record is written, in one transaction; a crash between the two leaves an id that
  no record uses, which takes no place and is given back for the same key.
- **A closed endpoint is not opened again while records are queued for it.** The
  closing is a head record, then chunks of at most `Params.ChunkReplaces`
  records, then nothing. A registration of the key meanwhile is not refused: it
  waits, parked in the loop, until the drain is done and is then run, so the
  adapter's request is answered late and no new answer exists for it.
- **Only the token of an open session endpoint opens the Agent interface.** The
  store keeps a token on every open record; the engine mints one for a direct
  endpoint and never lets it in.
- **The declaration field of an endpoint's record holds the declaration alone.**
  One compact JSON object of a fixed shape; a text that is not the exact encoding
  of the value it decodes to is not read, and a record whose field cannot be read
  stops `Open`. The display name and description the adapter gave are the
  record's own `DisplayName` and `Description`.
- **A recv that waits is a waiter the loop owns.** It is registered in the same
  command that found the queue empty, so no arrival can slip between the check and
  the registration. Any later command that makes a message of the endpoint ready
  hands the page to the waiter itself. The deadline is an entry of the schedule on
  the injected clock. An endpoint has at most one waiter: a newer one ends the
  older with an empty page. Shutdown ends every wait with an empty page, before
  the interfaces stop; the waiting side watches the shutdown itself, because the
  command that ends the waits is dropped when the inbox is full. The registry is in place; the command that registers a
  waiter and the page that wakes it come with the pull of messages.
- **The capacity check is in one place.** `checkCapacity`, which only the engine's
  own `admitNewWork` calls, judges the clock, then
  the queue, then the disk, and a bound that is not set refuses. The engine feeds
  it with the settings of the node, the free space and its own count of unfinished
  deliveries: target records that still need their body (accepted, queued or
  leased), accepted control records, and source deliveries no receipt answered;
  a delivery whose outcome is unknown holds no body and is not counted. System feedback, status reports, control
  replies and the local target side of a delivery do not go through it.
- **Recovery is a plan, then rules.** `PlanRecovery` is a pure function of a scan.
  `Open` runs the rules in the order the store's README fixes, works the plan out
  again from a fresh scan after a rule has written, and repeats until nothing is
  left. Work of a rule that is not wired yet (the quarantine of damaged files,
  the replay of prompts, blocks and feedback) makes `Open` return a
  `PendingRecoveryError` before it writes anything in that pass.
- **The recovery of deliveries is four phases in the order of the startup
  steps,** each a command of the loop that logs `startup step N begin` and
  `startup step N end`; each is empty until the feature it belongs to is added.
- **A message is accepted once, in one place.** `accept` makes the request into its
  envelope, whose errors are what refuses a request no node would take, answers a
  repeat of an idempotency key with the first message (the same content for the same
  recipients) or a conflict, asks the one capacity check, takes the next sequence number of the pair
  from the store's book, and writes the body and the source record in one
  transaction; memory and the book follow the disk, and only then is the message given
  to the outbound worker. A message is refused with nothing recorded when the queue is
  full, the disk has no room, the wall clock cannot be trusted, or the request is not
  one a node takes (no body, a body over the protocol's limit, a deadline beyond the
  longest, no recipient or the system endpoint as one, a disclosed list of several
  recipients).
- **One worker sends, in order, outside the loop.** The loop schedules and applies;
  the worker calls the transport in the order the work was given and waits until the
  loop has applied each result before the next call. A send that got no answer is made
  again after `ResendBackoff`, until the delivery deadline, to each node that has not
  answered (a node that answered does not make the others' sends unnecessary); a send
  again is made anew from the record, so its evidence is signed anew while the request
  digest, and so the request, is the same. A delivery that is not settled is asked
  about every `PullInterval` until its report deadline, which already holds the
  tolerance of the clock, and after the delivery deadline a node that never answered is
  asked, not sent to. The evidence of a reply is a signature: while the wall clock
  cannot be trusted the send is put off. Nothing about the sending is persisted: after a restart every unsettled
  source record is looked at again (the start's last step), and the target answers a
  send it already took in from its record. A worker waiting out a delay that the
  transport was asked to make is not work for `Quiesce`.
- **The target judges in a fixed order and writes nothing before it has signed.** A
  repeat of a request is answered from its record and another body under the same
  identifier is a conflict that changes nothing; for a new one the reply permission
  and its evidence are decided first (the evidence is applied even when the endpoint
  then declines the reply), then whether the endpoint exists, is open and takes
  messages, then the authorization check, then the deadline and the backlog. A
  recipient that is closed is rejected as `endpoint_closed`; one that is not there, one
  whose creation has not ended and a runtime endpoint, which takes control signals only
  and so is no endpoint for a message, as `endpoint_missing`. The reports are signed before the records are written, so a
  receipt that exists describes records that do. A rejection is made by the delivery
  rules from the record the constructor makes, and holds no body.
- **A local source is never judged by the node's rules.** Which requests are local is
  the transport's to say (`Origin.Local`); a local request is judged by the endpoint's
  declarations alone, and a remote one by the rules with no trust, which is denied
  until trust exists.
- **A report is only made of a state the record dates.** The target record keeps one
  time, when it was accepted, which dates the state accepted and a rejection made at
  intake; a report of any other state would need a time the record does not have, so
  none is made and a pull is answered as if the target had nothing to report yet.
- **The body is held by what still needs it.** The source record drops its reference
  when no delivery of it needs the body, in the write of the receipt that settled it; a
  rejected delivery holds no body at all. Every write that drops a reference removes,
  in its own transaction, the bodies no record refers to afterwards, and counts the
  records it writes itself (`bodyCounts.unreferencedAfter`): a body is addressed by its
  content, so a reply that says the same bytes as the message it answers refers to the
  very body the evidence just released, and that body stays.
- **Evidence that does not verify is discarded and alerted.** The reply is judged as if
  it carried none, and an alert of the kind `evidence_error` names the message.
- **A control signal is none of this worker's.** The source records of control signals
  are sent by the creation of sessions.
- **No method makes a session endpoint.** The engine has no method that takes or
  gives the record of one; a session endpoint exists only through a session
  creation, which is added with it.

## What the caller must guarantee

- Call `txn.Writer.Recover` and make the directories of `state/` exist before
  `Open`; load the configuration after `Recover`. `Open` reads what a power loss
  cannot take back only because the transaction layer settled it.
- Pass the persisted high water mark and the persisted pause in `Deps`, and add
  the task that persists them with `AddClockState`: it writes `TaskInput.HighWater`
  and `TaskInput.RollbackPaused`, and it is the loop that runs it at once when the
  pause begins, so the pause is on disk before the command that found it goes on.
- Call `AddPeriodic` before `Start`, `RecoverDelivery` between `Start` and
  `Serve`, and `BeginShutdown` before `Stop`.
- Admit an adapter instance with `AdapterAdmitted` before its endpoints register,
  and only an instance the configuration has enabled.
- Give `Deps.Rand` a reader the engine alone uses; it is read in the loop.
- Call `Execute` only for a request that `admin.Authorize` allowed and
  `admin.ValidateRequest` passed, with the key made by `admin.KeyFor` for the
  actor that was allowed, and run what `StoredResult.Stop` asks for after the
  answer was written.
- Make the transport with the engine as its handler, after `Open`, and give it to the
  engine with `SetTransport` before `Start`; give `Deps.Keys` the keys of this node, loaded
  after the transaction layer recovered; an engine given the keys of another node does
  not open.
- Call the loopback from the outbound worker only, which `SetTransport` makes true.
- Treat `Err() != nil` after `Done` as the order to exit and rescan.
- Run the long work of a periodic task outside the loop: a task runs in it, and
  the longest stall of the loop is what an adapter's notifications have to
  outlast.

## Where the tests are

The tests run the engine on the real store and configuration over the in-memory file
system. `rig_test.go` is their rig: it makes the node, the process and the engine's
random source with the building blocks of
[`testkit/harness`](../testkit/harness/README.md) (`ProgramTable`, `MakeLayout`,
`Mount`, `SeedNode`, `AdapterAdmission`, `RandReader`), so the way a node is made is
written once; what is the engine's own (phases, forks of a disk, seeded records) stays
in the rig.

`endpoints_test.go` has `TestEndpointBeyondNodeCapRefused` and the cap counted over
other kinds of endpoint; `close_test.go` the
closing and its chunks; `execute_test.go` and `execute_crash_test.go` the door of the administrative operations and its crash points; `declare_test.go` the declaration and who may make it;
`recovery_test.go` the scan entry and the recovery entry; `loop_test.go` the
loop, back pressure and the fatal path; `sched_test.go` and
`sched_internal_test.go` the schedule; `waiters_test.go` the waits; `capacity_test.go` the capacity check;
`crash_test.go` the crash walk of `direct-register`; `intake_test.go` the scenarios of
sending and intake judged from the ledger, the answers of the loopback and the records, and
`intake_more_test.go` the rules around them; `intake_crash_test.go` the crash walk of the
transactions that take a message in; `intake_fuzz_test.go` the receiving entry under
arbitrary bytes; `intake_internal_test.go` the decision of what to send and the indexes of the view; `static_test.go` the lists
the review holds the engine to; the `_internal_test.go` files the encoding of
the declaration field (`declaration_internal_test.go`), the view and the batches of a drain.
`TestQuarantineClosesEveryCreatingSessionEndpointOfTheDamagedControlRecord` is a
requirement kept in sight and skipped until the quarantine is wired.
