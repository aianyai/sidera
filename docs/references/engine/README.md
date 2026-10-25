# Engine

The single writer of the node's runtime state: the loop, its schedule, the
registration of endpoints, the capacity check and the two recovery entries. What
the features still to come add to the same loop (sending, receiving, prompts, control signals,
feedback) is described with them.

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
and the process is to exit and rescan. The classification is `txn.IsFatal`; the
engine adds only the one question whether a failed write was for lack of space.

**The schedule.** One min-heap of entries keyed by (kind, id), one timer on the
injected clock. Entries due together run in the order they were set. A periodic
task is due one period after `Start` and one period after each run; a clock that
jumps over several periods runs it once. A task that fails with an error a retry
may cure runs again after `RetryBackoff` (1 minute); a fatal error stops the loop.

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
registered again (`endpoint_draining`). A runtime endpoint cannot be closed: it
stands for its instance.

When an administrative operation asks for the close, one loop command does what
the rules for such operations fix: a request whose key and fingerprint were seen
returns the stored result and executes nothing (`Replayed`), the same key with
another fingerprint is refused (`operation_id_reused`), and otherwise the loop
closes the endpoint and builds the result from the outcome it reached
(`closed`, `closing` for a session endpoint, `already_closed` for one that was
closed before), then stores it with the fingerprint in the transaction of the head
record. A close of an endpoint that is closed already writes that record again as
it is, only to carry the result.

`Declare` changes the declaration of the endpoint the caller is: `ByToken` is the
session endpoint of an open token, `ByAdapter` a direct endpoint of the adapter's
own instance. The declaration, with the display name and description the adapter
gave, is the engine's field of the endpoint record (compact JSON, fixed member
order, at most 256 senders listed).

**Waiting.** A recv that finds nothing queued and is asked to wait is registered as
a waiter in the same command, so no arrival is missed between the check and the
registration. A command that makes a message of the endpoint ready hands the page
to the waiter in its own command. The deadline is a schedule entry on the injected
clock. At most one recv of an endpoint waits: a newer one ends the older with an
empty page. Shutdown ends every wait with an empty page.

**Capacity.** `CheckCapacity` is the one judgment of whether the node takes in new
work: a clock set back first (`clock_rollback`), then the queue (`node_queue_full`
when unfinished deliveries plus the new recipients exceed the node's limit), then
the disk (`storage_full` when the free space is below the reserve). A bound that is
not set refuses. The unfinished deliveries are the target records that still need their body
(`delivery.Record.NeedsBody`: accepted, queued or leased), the accepted control
records, and the source deliveries no receipt answered yet. A delivery whose
outcome is unknown holds no body and is not counted: the bound exists for the
bodies a node holds, and what a control record carries is pending work that
entered through a new-work entry all the same.

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
| 14 | Go on with the loopback of outgoing messages | phase `outbound` |
| 15 | The Agent interface serves in full | `Serve` |

**Recovery.** `PlanRecovery` is a pure function of a scan: what each recovery rule
has to do. `Open` runs the rules that have work in the order the store fixes
(`ctl-effect-replay`, `instance-drain-replay`, `orphan-gc-endpoint`,
`endpoint-drain-replay`, then the replays of prompts, blocks and feedback, then
`orphan-gc`), works the plan out again from a fresh scan after each rule that
wrote, and repeats until the plan is empty. `RecoverDelivery` runs the startup steps
11 to 14 as four commands and logs `startup step N begin` and `startup step N end`.

## Why

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
- **No registration over a draining endpoint.** The records still queued would land
  on an endpoint that is open again and be handed to someone they were not for.
- **The cap counts creating endpoints too.** A session endpoint being created has a
  record, a token and a place in the node's declaration as soon as it is committed;
  leaving it out would let a node pass the cap during a creation.
- **A direct endpoint has a token the store requires and nobody can present.** The
  token table resolves a token only to an open session endpoint, so a token read
  from a direct record opens nothing.
- **The display name and the declaration share a field.** The field the store keeps
  for the engine is the only persistent place for what an adapter said of an
  endpoint; a new field in the store was rejected as a contract change for a value
  that the adapter says again at each start.
- **A text that is not canonical is not read.** The declaration field is written in
  one form, so a re-encoding of what was read is the bytes that were read, and the
  fuzz target can assert it.
- **Recovery work of an unwired rule stops `Open`.** Opening over a damaged file
  would leave it out of the view as if it were not there, silently. The refusal
  names the rule and happens before any write of that pass.
- **The view takes the record as the store wrote it.** The store writes the next
  revision of a target record and does not hand it back; the engine adds the one
  to its copy, and `TestViewAgreesWithTheDiskAfterAWrite` compares the view with a
  fresh scan, so a change of the store's rule fails there.
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
| The 257th endpoint is refused | `endpoints.go` (`registerDirect`) | `TestEndpointBeyondNodeCapRefused`, `TestInstanceLimitRefusesBeforeTheNodeCap` |
| Same key, same endpoint; a new token on reopening | `endpoints.go`, `tokens.go` | `TestSameKeyGivesTheSameEndpointAcrossARestart`, `TestReopeningAnEndpointKeepsItsIdAndGivesItANewToken` |
| The registration crashes anywhere | `transactions.go` | `TestDirectRegisterRecoversFromACrashAtEveryPoint` |
| The result of a close is stored with it, and replayed | `endpoints.go` (`closeEndpoint`), `view.go` (`results`) | `TestCloseStoresTheResultOfTheOperationWithTheHeadRecord`, `TestTheStoredResultSaysWhatTheCloseDidForEveryKindOfEndpoint`, `TestAResendOfAClosingReturnsTheStoredResultAndAReusedIdentifierIsRefused` |
| Closing, chunks, bodies | `drain.go`, `endpoints.go` | `TestCloseRejectsTheQueuedDeliveriesInChunksAndReleasesTheirBodies`, `TestCloseBatch…` (`drain_internal_test.go`), `TestRegisteringTheKeyOfAnEndpointStillDrainingIsRefused` |
| Declarations and callers | `endpoints.go`, `meta.go`, `view.go` | `declare_test.go`, `meta_internal_test.go` (golden vectors, refusals, `FuzzDecodeMeta`), `TestResolveGivesTheEndpointOfACallerAndRefusesTheRest` |
| Capacity | `capacity.go`, `recovery.go` (`admitNewWork`) | `TestCheckCapacityTable`, `TestAdmitNewWorkReadsTheSettingsTheDiskAndTheViewOfTheEngine` |
| The loop, inbox, back pressure, fatal | `engine.go` | `loop_test.go` |
| The schedule | `sched.go`, `engine.go` | `sched_test.go`, `sched_internal_test.go` |
| Recovery | `recovery.go` | `recovery_test.go` |
| Waiting |  `waiters.go` | `waiters_test.go` |
| The view agrees with the disk | `view.go` | `TestViewAgreesWithTheDiskAfterAWrite` |
| No method makes a session endpoint | — | `TestEngineHasNoMethodThatMakesASessionEndpoint` |

Known gaps, each filled in with the feature it belongs to:

- The quarantine of damaged files, and with it the closing of every creating
  session endpoint whose control record is quarantined, is not wired:
  `store.Quarantine` takes one session endpoint, and a control signal that failed
  and was accepted again leaves two. `TestQuarantineClosesEveryCreatingSessionEndpointOfTheDamagedControlRecord`
  is skipped until the store's transaction takes a list.
- The prompt, block and feedback replays and the four phases of `RecoverDelivery`
  are empty until the features that own them exist.
- `isNoSpace` for Windows (`nospace_windows.go`) is pending verification by CI on
  main.
