# Engine

The single writer of the node's runtime state: the loop, its schedule, the
registration of endpoints, the capacity check, the two recovery entries, and the
sending and taking in of messages. What the features still to come add to the same
loop (pulling, prompts, control signals, feedback) is described with them.

## What

**One loop.** `internal/node/engine` owns the in-memory view of every record, and
one goroutine runs every command that changes state. A command is a type with a
`run`; it is answered exactly once, on a channel of capacity one. Each command
reads the clock once, observes the reading against the high water mark of the wall
clock, makes its change durable through the transaction layer, updates memory only
after `Commit` returned nil, and answers. A command that entered the inbox is run
even if its caller has gone.

**Phases.** `opened` (after the scan entry), `recovering` (the loop runs, the node
is starting), `running`, `stopping`, then `stopped`, or `failed` after a fatal
storage error. In `recovering` reads answer, commands that change state are
refused with `recovering`, and what an adapter asks of its endpoints waits for
`running`. In `stopping` outside commands are refused with `shutting_down`.

**Submitters.** An outside caller (the Agent and management interfaces) waits
`SubmitWait` (10 s, real clock) for room in a full inbox of `InboxLen` (1024)
commands and is then refused with `loop_busy`. An internal submitter (the adapter
host, the engine's workers, the recovery entry) waits without limit.

**Failures.** A write that failed before its commit point acknowledged nothing:
the caller is told `storage_full` when the volume is full and `storage_unavailable`
otherwise, and memory stays as it was. A failure from the commit point on is fatal:
the command is answered `storage_fatal`, the loop stops, `Err` returns the error,
and the process is to exit and rescan. The classification is `txn.IsFatal`, and
whether a failed write was for lack of space is `fsx.IsNoSpace`, which alone knows
the operating system codes (pending verification by CI on main for Windows). The
"no space" case of `TestAFailedWriteIsBackpressureAndMemoryKeepsWhatTheDiskHolds`
injects `fsxtest.DiskFull`, the platform's own full-disk error, so it means the
same on every platform.

**The schedule.** One min-heap of entries keyed by (kind, id), one timer on the
injected clock. Entries due together run in the order they were set. A periodic
task is due one period after `Start` and one period after each run; a clock that
jumps over several periods runs it once. A task that fails with an error a retry
may cure runs again after `RetryBackoff` (1 minute); a fatal error stops the loop.

**The clock.** Every command and every wake of the timer reads the wall clock once
and observes it against the high water mark. A reading set back beyond the tolerance
pauses the work that depends on a deadline, and the command that found it runs the
task registered with `AddClockState` before it goes on, so the pause is on disk
before anything else happens: a pause kept only in memory until the next period
would be lost by a crash, and the clock that was set back trusted again after the
restart. A fatal error of that write stops the engine, a retryable one is retried
after `RetryBackoff` while the pause already holds in memory.

**Endpoints.** `RegisterDirect` gives an adapter instance the same endpoint for the
same key, for ever: the id is minted once and kept in `config/endpoint-ids/`, in
the same transaction (`direct-register`) as the endpoint record. A new endpoint,
and a closed one opened again with a new token, count against the limit of the
instance (`Admission.MaxDirectEndpoints`) and the cap of the node
(`publication.MaxEndpointsPerNode`), which counts every endpoint that is open or
being created, whatever its kind. The refusal is `endpoint_limit` and says which.
The system endpoint is not registered, counted, listed or closable.

Closing an endpoint is a head record (closed, token gone), then chunks of at most
`ChunkReplaces` (16) records queued for it, each closed by the delivery rules and
stripped of its body reference, the bodies nothing refers to any more released.
While queued records remain the endpoint is draining and its key is not
registered again: a registration of the key waits, parked in the loop, until the
last chunk is done, and is then queued behind the commands the inbox holds. It is
neither refused nor tried over and over: when a chunk fails, the drain is retried
after `RetryBackoff`, and the registration waits for that too. `Quiesce` is not
quiet while one waits. A runtime endpoint cannot be closed: it
stands for its instance.

When the owner asks for the close, the request comes through the door of the
administrative operations (below), and its operation builds the result from the
outcome the close reached (`closed`, `closing` for a session endpoint,
`already_closed` for one that was closed before) and stores it with the
fingerprint as the last write of the transaction of the head record. A close of an
endpoint that is closed already writes that record again as it is, only to carry
the result.

**Reading a stored result.** `OperationResult` answers the result stored for a
request key and whether there is one, from the table the replay check reads, so a
result kept under `state/admin/` and one kept in a versioned file of `config/` are
found the same way, before and after a restart. The key is the one the request was
filed under (`admin.KeyFor` of the requester), so an identifier of another person is
another key and is never found. It is a read: it writes nothing, and it is taken
while the node starts, like the other reads. It exists so that a client that did not
read the answer, or the remote management that comes later, can ask what became of an
identifier without sending the request again.

**The door of the administrative operations.** `Execute` takes a management
request that changes state, after the interface has judged who asks and whether
the request is well formed, and carries it out as one command of the loop. A
request whose key and fingerprint were seen returns the stored result and runs
nothing (`Replayed`), the same key with another fingerprint is refused
(`operation_id_reused`), and otherwise the operation runs and its result is stored
with the fingerprint in the transaction of its effect. The rule that one
identifier has one result has this one home, so that two concurrent resends cannot
both run: the lookup and the storing are one step of the single writer, and no
operation and no interface repeats the check. A result is kept in one of two
places, by the kind of operation: under `state/admin/` for an operation with no
file of its own to carry it (closing an endpoint, stopping the daemon), and in the
file that a versioned change writes (the `last_operation` of `node.json`,
`presets.json`, `instances.json`), in the same replacement as the change. A
replacement of a versioned file is whole, but a transaction
commits with its first write and the result under `state/admin/` is its last, so a
crash after the effect is durable and before the result is written leaves the
effect without its result: the request sent again is carried out again and gets the
answer of an operation that finds its effect there (closing an endpoint that is
closed says `already_closed`), where the first answer would have said `closed`. The
result is never there without the effect. The lookup reads both places: `Open`
loads the results of `state/admin/` with the scan and the operations of the
versioned files from the configuration, and every command that stores a result
updates the table, so a resend is answered the same before and after a restart. A request that is refused stores nothing and its identifier stays
free. The expected version of a versioned setting is handed to the operation and is
checked inside the change that writes the file, by the package that owns it: the
check and the write are one step, and `admin.CheckVersion` is called there and
nowhere else. `StoredResult.Stop` says that the daemon is to shut down; it follows
the operation, not the result, so the resend of a request that stops the daemon
asks for the stop again. An operation is a value of a type of the engine, because
its effect needs the view, the store and the configuration that only the loop may
touch; the owner of another operation adds its type next to `CloseEndpointOp` and
`StopDaemonOp`.

`Declare` changes the declaration of the endpoint the caller is: `ByToken` is the
session endpoint of an open token, `ByAdapter` a direct endpoint of the adapter's
own instance. The declaration is the engine's field of the endpoint record
(compact JSON, fixed member order, at most 256 senders listed); the display name
and description the adapter gave are fields of the record of their own. A patch that names nothing is refused as
`invalid_argument` with the detail `empty_patch`: the detail is a typed value of
the `Refusal`, as the limit of `endpoint_limit` is, so that the interface maps it
without reading text. `Endpoints` lists for each endpoint how many messages wait
for it (`Queued`).

**Waiting.** A recv that finds nothing queued and is asked to wait is registered as
a waiter in the same command, so no arrival is missed between the check and the
registration. A command that makes a message of the endpoint ready hands the page
to the waiter in its own command. The deadline is a schedule entry on the injected
clock. At most one recv of an endpoint waits: a newer one ends the older with an
empty page. Shutdown ends every wait with an empty page: `BeginShutdown` posts the
command that does it without waiting, so a full inbox drops it, and the waiting
side (`awaitWaiter`) watches the shutdown signal itself; a page that was woken
before the shutdown is seen is still handed out, and the registry that is left
when the loop stops is ended by `finish`.

**Capacity.** `checkCapacity` is the one judgment of whether the node takes in new
work: a clock set back first (`clock_rollback`), then the queue (`node_queue_full`
when unfinished deliveries plus the new recipients exceed the node's limit), then
the disk (`storage_full` when the free space is below the reserve). A bound that is
not set refuses. The unfinished deliveries are the target records that still need their body
(`delivery.Record.NeedsBody`: accepted, queued or leased), the accepted control
records, and the source deliveries no receipt answered yet. A delivery whose
outcome is unknown holds no body and is not counted: the bound exists for the
bodies a node holds, and what a control record carries is pending work that
entered through a new-work entry all the same.

**Accepting a message.** `Send` and `Reply` are the one place new work from a local
endpoint enters the store. A request that no node would take is refused first: no
body, a body over 64 KiB, no content type, a deadline beyond one hour, no recipient or
the system endpoint as one, a disclosed list of several recipients. A repeat of an
idempotency key returns the first message when the content is the same and is a
conflict when it is not; content is compared by the request digest the target compares
with, made with the first request's deadlines, because the deadline a repeat gets is its
own and says nothing of the content. Then the capacity check, then the next sequence
number of the pair (sender endpoint, recipient address) from the store's book, and the
body and the source record in one transaction. A reply finds its recipient and its
reply-to in the node's own record of the message the endpoint received; the caller names
neither. The deadlines are whole seconds, because that is what an envelope can say and
the target's record must agree with it.

**Sending.** One worker, outside the loop, makes the calls to the transport in the order
the work was given: a send of the envelope (made anew from the source record and the
body, with the evidence of a reply signed at that moment) or a pull of the reports of a
message. The loop schedules the next call and applies each result. A send that got no
answer is made again with the same bytes every `ResendBackoff` (30 s) until the delivery
deadline; a delivery that is not settled is asked about every `PullInterval` (1 min) until
its report deadline plus the clock tolerance; after the delivery deadline a node that
never answered is asked and not sent to. The reports in a receipt or an answer are
verified with the target's certificate and applied to the source record by the envelope
package's one rule; the record is written with the feedback the change calls for and with
the body reference dropped once no delivery needs the body. A report that does not verify
or that no state can follow is logged and left out. Nothing of the sending is persisted:
the last step of the start looks at every unsettled source record again.

**Taking in an envelope.** `Inbound` is the handler of the transport. For each recipient
on this node it finds the record of the request or judges it: a repeat gets the record's
report, another body under the same message identifier is refused as a conflict and
nothing changes; for a new request the reply permission and its evidence are decided,
then the endpoint (a closed or missing endpoint, and a runtime endpoint, answer as one
that is missing), then the authorization check, then the deadline and the endpoint's
backlog. A request from the loopback is judged by the endpoint's declarations alone; one
that came any other way is judged by the node's rules with no trust, which is denied until
trust exists. The accepted record holds the body, the rejected one does not. The reports
of the receipt are signed before anything is written and the records, with the source
record the evidence of a reply changed, are written in one transaction. While the wall
clock cannot be trusted nothing is signed, so an envelope and a pull are refused and the
sender tries again.

**The start, as far as the engine is concerned.** The steps of the start are numbered
in the order the node takes them; the engine takes part in these:

| Step | What | Engine |
| ---- | ---- | ------ |
| 6 | Scan `state/`, quarantine and repair, rebuild the view | `Open` (the scan entry) |
| 8 | Management interface opens; reads answer, changes are refused | `Start` |
| 10 | Adapter instances start and are admitted | `AdapterAdmitted` |
| 11 | Check the creation intents and prompt attempts that were not settled | phase `reconcile` of `RecoverDelivery` |
| 12 | Run `Tick` on every record, release leases whose turn is not active | phase `tick` |
| 13 | Send the feedback that fell due | phase `feedback` |
| 14 | Go on with the loopback of outgoing messages | phase `outbound`: every unsettled source record is sent or asked about again |
| 15 | The Agent interface serves in full | `Serve` |

**Recovery.** `PlanRecovery` is a pure function of a scan: what each recovery rule
has to do. `Open` runs the rules that have work in the order the store fixes
(`ctl-effect-replay`, `instance-drain-replay`, `orphan-gc-endpoint`,
`endpoint-drain-replay`, then the replays of prompts, blocks and feedback, then
`orphan-gc`), works the plan out again from a fresh scan after each rule that
wrote, and repeats until the plan is empty. `RecoverDelivery` runs the startup steps
11 to 14 as four commands and logs `startup step N begin` and `startup step N end`.

## Why

- **The source accepts, the target judges, and they are two transactions.** The call
  returns when the message is on disk; what the recipient's node makes of it comes back
  as a report. A message to another node could not be answered at once, and the agent's
  way of using the interface must not depend on whether the target is on this node. The
  fault knobs of the loopback also act between the two.
- **A single worker, in order.** A step of a scenario then has one order of commands, and
  a crash point is the same in two runs. The loop never waits for the transport: a
  receiving command runs in the loop while the worker waits for it, which would be a
  deadlock the other way round.
- **A repeat is answered from the record, a conflict changes nothing.** A receipt that
  was lost is the common failure of a send; the sender sends the same bytes again and the
  target must not take the message in twice. Another body under the same identifier is a
  sender that is broken or hostile, and the record it would overwrite is the evidence.
- **Reports are signed before the write.** A receipt that exists then describes records
  that exist; a signing failure leaves nothing on disk, and a write that fails leaves no
  receipt, so the sender sends again.
- **No report of a state the record cannot date.** A report carries the moment the target
  recorded the state, and it is what the reply window starts from; a made-up time would
  move the window. The target record keeps the acceptance time only, so the states after
  acceptance wait for a stored time (the skipped test names the question).
- **A local source is judged by declarations, never by node rules,** and which source is
  local is the transport's word, never the request's: a request that claims this node's
  identifier but came another way is a remote one.
- **A repeat of an idempotency key is compared by the digest the target uses,** with the
  first request's deadlines, so the content comparison is the one rule the target also
  applies and a repeat is not a conflict merely for being made later.
- **Pulling at a fixed interval, not on a signal.** The sender asks again because a
  transport to another node has no way to tell it a state changed; the loopback is the
  same seam. At the scale of a node (a few thousand live messages) a pull a minute is a
  cheap command that writes nothing when nothing changed.

- **One writer, not locks.** Every operation that changes state is serialised, so
  the crash points of each transaction can be enumerated and a run can be replayed.
  Per-record locks were rejected: the combinations of crash points explode. At the
  scale of a node the throughput is set by `fsync`, not by the loop.
- **Typed commands, not closures.** The kinds of command are what a crash walk and
  `Quiesce` observe; a list of types can be read, a pile of closures cannot.
- **The waiter is registered where the queue was found empty, and woken by the
  command that fills it.** A waiter that fetches its page itself after waking is
  one window in which another reader takes the message; handing the page over in
  the waking command closes it. The deadline is on the schedule and not on a timer
  of the waiting goroutine, so the injected clock moves it and a test sees it.
  The newer recv wins and the older gets an empty page, not an error: a client
  that was killed and retried did nothing wrong, and a page given to two waiters
  would show an agent a message twice.
- **Workers are started only in the loop.** The engine's stop waits for its
  workers with a `WaitGroup` on the loop's goroutine; adding to a `WaitGroup`
  concurrently with a wait that starts from a zero count is a misuse the library
  documents. Keeping `spawn` to the loop also makes "the engine has ended" and
  "a worker is starting" exclusive. `TestSpawnOutsideTheLoopPanics` pins the guard.
- **An answer channel of capacity one**, as in etcd's raft node, so that the loop
  never blocks on a caller that left.
- **Two kinds of submitter.** An outside caller that waits for ever would hold a
  connection of the Agent interface; one that is refused after a bounded wait gets
  a retryable `loop_busy`. The adapter host has to keep the order of an adapter's
  notifications, and its own buffer behind the inbox is its back pressure; the
  longest stall of the loop that the buffer outlasts is therefore the host's to
  state, and long batches are cut into chunks so the stall stays short.
- **A continuation waits for the commands ahead of it, not for an empty inbox.**
  When it is queued the loop records how many commands the inbox holds and runs
  exactly those first; waiting for an empty inbox would starve a continuation under
  a load that never lets the inbox empty, and running it at once would put it ahead
  of commands that were already waiting. `TestAContinuationRunsAfterTheCommandsAheadOfItAndBeforeTheOnesThatCameLater`
  and `TestAContinuationIsNotStarvedByAnInboxThatIsNeverEmpty` pin both ends.
- **Chunks by count, not time.** A time budget would move the boundary of a chunk,
  and so the crash point that a write is, with the speed of the machine.
- **The loop builds the result record, from the outcome.** A result made by the
  caller before the command ran can only guess the outcome (a session endpoint says
  `closing`, a direct one `closed`, and a repeated close `already_closed`), and a
  caller that stores it separately loses the one-transaction guarantee. The request
  carries the key, the fingerprint, the name of the operation and the encoder; the
  command checks the replay, closes and stores in one step. A result is stored in
  every outcome, because the stored answer to an identifier must be the one the
  first execution gave: without the replay check the second close of a resent
  request would overwrite `closed` with `already_closed`.
- **The head record first.** Whatever a chunk would do is derived from the head
  record, so a crash between chunks is repaired by the same rule at the next start.
- **No registration over a draining endpoint, and no refusal for it either.** The
  records still queued would land on an endpoint that is open again and be handed
  to someone they were not for. A refusal would need an answer the adapter protocol
  does not have, and the adapter could do nothing with it but ask again; the
  registration waits for the drain instead, as the enabling of an adapter instance
  waits for the closing of the endpoints of its earlier life. Parking the command
  until the drain ends, rather than queueing it again at once, is what keeps it from
  spinning in the loop while a failed chunk waits out its back off.
- **The cap counts creating endpoints too.** A session endpoint being created has a
  record, a token and a place in the node's declaration as soon as it is committed;
  leaving it out would let a node pass the cap during a creation.
- **A direct endpoint has a token the store requires and nobody can present.** The
  token table resolves a token only to an open session endpoint, so a token read
  from a direct record opens nothing.
- **The node keeps the display name and the description the adapter gave.** The
  adapter's start tells the node, for each direct endpoint, only its identifier and
  its key; it does not say the name or the description again. What `Endpoints`
  lists has therefore to come from the disk, and the endpoint that was registered
  before the restart is listed with what the adapter said last. They are the
  record's `DisplayName` and `Description`, not part of the engine's declaration
  field: the record has a place for them, and the declaration field holds only the
  declaration. The adapter saying them again rewrites those two fields and leaves
  the declaration as it was (`TestTheRecordHoldsTheNameAndTheDescriptionApartFromTheDeclaration`).
- **A text that is not canonical is not read.** The declaration field is written in
  one form, so a re-encoding of what was read is the bytes that were read, and the
  fuzz target can assert it. A text that carries a display name or a description is
  not a declaration and is refused.
- **Recovery work of an unwired rule stops `Open`.** Opening over a damaged file
  would leave it out of the view as if it were not there, silently. The refusal
  names the rule and happens before any write of that pass.
- **The view takes the records the store hands back.** A write of target records
  returns them as written, the revision included, and the engine stores what comes
  back instead of working the revision out: the rule has one home, the store.
  `TestViewMatchesAFreshScanAfterWrites` compares the view with a fresh scan after
  writes made in several chunks, so a view that kept the records it gave fails
  there. The recovery rules keep no view (it is built from the last scan), so they
  do not use what a write returns.
- **An accepted control signal is never rejected.** Closing a control record of a
  closed endpoint is what the delivery rules say: `delivery_unknown`.

Prior art taken: the typed command with a capacity-one answer and a context that
does not retract a handed command (etcd raft node), the heap with one timer re-armed
to its top (Consul's expiry heap, client-go's delaying queue), chunking by count
(etcd's compaction), fatal exit decided by the owner of the process and not by the
storage layer (PostgreSQL's `fsync` PANIC, etcd's WAL failure). Rejected: closures
as commands (the kinds are not countable), a time budget for chunks (not
reproducible).

## Where

| Rule | Code | Tests |
| ---- | ---- | ----- |
| The 257th endpoint is refused | `endpoints.go` (`registerDirect`) | `TestEndpointBeyondNodeCapRefused`, `TestTheNodeCapCountsOtherKindsOfEndpointOnTheRegistrationPath`, `TestInstanceLimitRefusesBeforeTheNodeCap` |
| Same key, same endpoint; a new token on reopening | `endpoints.go`, `tokens.go` | `TestSameKeyGivesTheSameEndpointAcrossARestart`, `TestReopeningAnEndpointKeepsItsIdAndGivesItANewToken` |
| The registration crashes anywhere | `transactions.go` | `TestDirectRegisterRecoversFromACrashAtEveryPoint` |
| The result of a close is stored with it | `endpoints.go` (`closeEndpoint`), `manage.go` (`CloseEndpointOp`), `view.go` (`results`) | `TestCloseStoresTheResultOfTheOperationWithTheHeadRecord`, `TestTheStoredResultSaysWhatTheCloseDidForEveryKindOfEndpoint` |
| One identifier has one result, for every operation and every place a result is kept | `manage.go` (`Execute`, `execute`, `addConfigResults`), `recovery.go` (`Open`) | `TestAResendReturnsTheStoredResultAndAReusedIdentifierIsRefused`, `TestConcurrentResendsOfOneRequestRunOnce`, `TestARefusedRequestStoresNothingAndTheIdentifierStaysFree`, `TestAVersionedChangeIsAppliedOnceAndAStaleOneNeverIsApplied` |
| A crash at any point of an operation leaves nothing, the effect alone or both, never the result alone; a resend is carried out again or answered from the result | `manage.go`, `endpoints.go` (`closeWithResult`) | `TestAnAdministrativeOperationRecoversFromACrashAtEveryPoint` |
| A stored result is read by its request key, from both places, and by no other key | `manage.go` (`OperationResult`) | `TestTheStoredResultOfAnOperationIsReadByItsRequestKey`, `TestAStoredResultIsReadWhileRecoveringAndNotWhileStopping` |
| The door is closed while the node starts and while it stops; what is not a change does not come through it | `manage.go`, `engine.go` (`step`) | `TestExecuteIsRefusedWhileRecoveringAndWhileStopping`, `TestExecuteRefusesAnOperationThatChangesNothing` |
| A request that stops the daemon says so, resent or not | `manage.go` (`StopDaemonOp`, `StoredResult.Stop`) | `TestStopFollowsTheOperationOfTheRequest` |
| Closing, chunks, bodies | `drain.go`, `endpoints.go` | `TestCloseRejectsTheQueuedDeliveriesInChunksAndReleasesTheirBodies`, `TestCloseBatch…` (`drain_internal_test.go`), `TestRegisteringTheKeyOfAnEndpointStillDrainingWaitsForTheDrain`, `TestARegistrationWaitsAcrossAFailedChunkOfTheDrain` |
| Declarations and callers | `endpoints.go`, `declaration.go`, `view.go` | `declare_test.go`, `declaration_internal_test.go` (golden vectors, refusals, `FuzzDecodeDeclaration`), `TestTheRecordHoldsTheNameAndTheDescriptionApartFromTheDeclaration`, `TestResolveGivesTheEndpointOfACallerAndRefusesTheRest` |
| Capacity | `capacity.go`, `recovery.go` (`admitNewWork`) | `TestCheckCapacityTable` (`capacity_internal_test.go`), `TestAdmitNewWorkReadsTheSettingsTheDiskAndTheViewOfTheEngine` |
| The loop, inbox, back pressure, fatal | `engine.go` | `loop_test.go` |
| The schedule | `sched.go`, `engine.go` | `sched_test.go`, `sched_internal_test.go` |
| The pause of a clock set back is persisted at once | `engine.go` (`observe`, `AddClockState`) | `TestThePauseOfAWallClockSetBackIsPersistedAtOnceAndSurvivesACrash`, `TestAFatalErrorWhilePersistingThePauseStopsTheEngine` |
| Recovery | `recovery.go` | `recovery_test.go` |
| Waiting | `waiters.go` | `waiters_test.go` (`TestShutdownEndsEveryWaitEvenWhenTheInboxIsFull`, `TestAPageThatIsReadyWinsOverTheShutdown`) |
| The view takes what the store returned and agrees with the disk | `drain.go`, `view.go` | `TestViewMatchesAFreshScanAfterWrites` |
| No method makes a session endpoint | — | `TestEngineHasNoMethodThatMakesASessionEndpoint` |
| Node rules do not apply to a local sender, declarations do | `inbound.go` (`judgeAuthz`) | `TestLocalSourcesIgnoreNodeRules`, `TestEndpointDeclarationRestrictsLocalSenders`, `TestARemoteSenderIsJudgedByTheNodeRulesAndALocalOneIsNot` |
| A reply with evidence is admitted before the report was pulled, a forged reply-to gets nothing | `inbound.go` (`admitReply`) | `TestReplyWithEvidenceAdmittedBeforeReportPulled`, `TestForgedReplyToGetsNoPermission`, `TestEvidenceThatDoesNotVerifyIsDiscardedAndTheOwnerIsToldOfIt` |
| A lost receipt and a resend leave one record; another body under one identifier is a conflict | `inbound.go` (`takeIn`), `outbound.go` | `TestLostReceiptRetryKeepsOneRecord`, `TestSameIDDifferentBodyConflicts` |
| The backlog of an endpoint, the queue and the disk of the node | `inbound.go`, `send.go`, `capacity.go` | `TestEndpointBacklogFullIsRejectedWithReason`, `TestNodeQueueFullBackpressuresNewSends`, `TestDiskFullGivesNoFalseAcknowledgement` |
| Requests no node takes, sequence numbers, the clock pause, idempotency retention | `send.go` | `TestASendThatNoNodeWouldTakeIsRefusedAndLeavesNothing`, `TestSequenceNumbersCountEachPairOfSenderAndRecipientFromOne`, `TestNothingIsSignedOrAcceptedWhileTheWallClockCannotBeTrusted`, `TestAnIdempotencyKeyIsRememberedUntilItsRetentionEnds` |
| What is sent and asked, and when | `outbound.go` | `TestOutboundNeedByState`, `TestAnUnreachableNodeIsSentToUntilTheDeadlineAndAskedAfterIt`, `TestHeldReportsAreAskedForUntilTheyCome`, `TestARestartGoesOnWithAMessageThatWasNotSettled` |
| The body is held by what needs it | `inbound.go` (`settledSource`), `outbound.go` | `TestTheBodyIsHeldOnlyByWhatStillNeedsIt` |
| Every write of taking a message in survives a crash | `intake`, `deliver`, `deliver-reply`, `source-update` (declared by the store) | `TestSendingAndIntakeRecoverFromACrashAtEveryPoint` |
| The receiving entry under arbitrary bytes | `inbound.go` | `FuzzInbound` |

Known gaps, each filled in with the feature it belongs to:

- The quarantine of damaged files is not wired. The store decides which session
  endpoints a quarantined control record closes (every creating one that names it,
  `store.QuarantineSessions`), so the engine passes the scan and no list;
  `TestQuarantineClosesEveryCreatingSessionEndpointOfTheDamagedControlRecord`
  is skipped until the engine wires the quarantine.
- The prompt, block and feedback replays and phases 11 to 13 of `RecoverDelivery`
  are empty until the features that own them exist; phase 14 goes on with the outgoing
  messages.
- A report of the target can only be made for the state accepted and for a rejection made
  at intake: the target record keeps one time, when it was accepted
  (`TestAStateAfterAcceptedHasNoStoredTimeToReportItWith`, skipped, holds the question).
  A pull is answered without a report for any other state, and a reply's evidence is made
  only while the record is in one of them.
- An empty body is refused: the Agent interface says a body may be empty and an envelope
  of a message must have one (`TestShouldASendWithAnEmptyBodyBeAccepted`, skipped).
- A disclosed list of several recipients is refused: the source record does not keep the
  flag, so a send again could not repeat the request digest the target stored
  (`TestShouldTheSourceRecordKeepWhetherRecipientsAreDisclosed`, skipped).
- The evidence of a reply that frees the last reference to a body leaves the body for the
  next start's rule for bodies nobody refers to: the transaction that writes the reply has
  no step that removes one.
- The unreachable hint and the expiry of a delivery nobody answered are not applied by the
  sender yet; they come with the feedback.
- Closing a session endpoint does not yet send `session.close` to the adapter: the
  outcome says `closing`, and the release of the native session comes with the
  creation of sessions, which is the feature that makes one.
- `AdapterAdmitted` ignores the runtime capability, and no transaction that opens
  or reopens the runtime endpoint of an instance (`runtime-admit`) is declared yet:
  it belongs to the adapter host.
- A write that fails on `endpoint.register` is refused with the engine's own
  reason (`storage_full`, `storage_unavailable`, `storage_fatal`). Which code of the
  adapter protocol each maps to is for the adapter host to decide; if no existing
  code fits, it stops and says so, and the protocol is changed for it, not worked
  around in the host. The cap of the node maps to the protocol's `endpoint_limit`.
- A stored administrative result is dropped from the engine's table only by a
  restart. The sweep that deletes results after their retention will have to take
  them out of the view too, in the same step.
