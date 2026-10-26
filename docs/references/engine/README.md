# Engine

The single writer of the node's runtime state: the loop, its schedule, the
registration of endpoints, the life of an adapter's connection, the capacity check,
the two recovery entries, and the sending and taking in of messages. What the
features still to come add to the same loop (pulling, prompts, control signals,
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
The timer is armed with the earliest entry's time itself (`Timer.ResetAt`), not with a
duration measured from an earlier reading: a clock that moves between the reading
and the arming would otherwise leave the timer waiting for the entry's time plus
that distance.

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
stripped of its body reference, whose own body file the store removes in the same
transaction.
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

**The owner's operations on the clock, the identity, the alerts and the settings.**
Each is a type of the engine behind the door above, whose effect runs in the loop
and whose result is made by a function the interface passes in, so the words of the
wire stay with the interface.

| Operation | Effect | Where the result is |
| --------- | ------ | ------------------- |
| `ClearClockRollbackOp` | the mark becomes the clock as it reads and the pause flag is removed, in one replacement of `keys/clock.rec`; memory forgets the pause after the commit | `state/admin/`, the last write of `clock-clear` |
| `ConfirmRevocationBackupOp` | `config/revocation-backup.json` names the digest of the self-revocation the owner keeps outside the device and the time | `state/admin/`, `backup-confirm` |
| `RetireIdentityOp` | the self-revocation the keys verified is kept as a revocation under `config/revocations/`; nothing is signed from the commit on; the daemon is asked to stop | `state/admin/`, `identity-retire` |
| `AcknowledgeAlertOp` | the alert is marked | `state/admin/`, `alert-ack` (the store's) |
| `SetNodeSettingsOp`, `SetEndpointSettingsOp` | a patch merged and judged whole by `config`, which checks the version inside the change | the `last_operation` of `node.json` and of `instances.json` |
| `InstallAdapterOp` | a package is copied outside the loop, the copy is judged, and the loop renames it to its version and replaces the pointer that makes it current | `state/admin/`, the last-but-one write of `adapter-install`, or alone in `adapter-install-current` |
| `EnableAdapterOp`, `DisableAdapterOp` | the instance is made (or turned on) or turned off in `instances.json`; then the host is asked to run or stop it | the `last_operation` of `instances.json` |
| `AddPresetOp`, `RemovePresetOp` | a preset is added from its document or removed, in `presets.json`; adding is judged as loosening and removing as narrowing | the `last_operation` of `presets.json` |

Each of the first three is one transaction of two owners that the engine opens,
because only the composer can declare a transaction of two owners: the effect and
then the result, which commits with its first write. A crash between them leaves
the effect without its result and the request sent again finds the effect there:
a clear that finds no pause says `cleared: false`, a confirmation or a retirement
or an acknowledgement is harmless to do again and answers as the first did. The
result is never there without the effect. The owner names what the request is for
(the node to retire, the digest to confirm), and a request for another is refused
before anything is written (`invalid_argument`, details `node_mismatch` and
`digest_mismatch`): a retirement is for good and a confirmation that put the
reminder out for a copy that cannot retire this node would be a lie.

`Status` reads, at one command, the phase, when the loop started, the reminders,
the clock (the pause with the time it began, the mark), the certificate and what is
left of it, the room (the deliveries that still have work, the queue limit, the
sent messages no receipt answered, the free space and the reserve), the deliveries
this node holds as a target by state, and the endpoints. The reminders are derived:
the one there is, that the backup of the self-revocation is not confirmed, is true
while the confirmation on disk is not of the statement this node holds, and has no
record of its own. `ExportRevocation` is the one thing of the interface that is
neither a read of the loop nor stored: the statement does not change while the node
runs, and its result is not stored because the statement can retire the node.
`NodeSettings` reads `node.json` in the loop, with the version a change names.

**Installing an adapter package.** The copy of a package takes as long as the package
is large and the loop must not wait for it, so `InstallAdapterOp` has work outside the
loop and the engine runs it in a fixed order (`preparedOperation`). The loop looks the
request up in the stored results first, and a request that was done is answered
without the package being read; otherwise it takes the one place of the install worker
(a second installation meanwhile is refused with `install_in_progress`, nothing of it
done; the same request sent again while it is copied is `ReasonInstallRepeated`, which the
interface words as that installation still being in progress) and draws the randomness that names the staging directory, because the engine
draws randomness only in the loop. The worker, which is a goroutine the engine owns and
`Quiesce` counts, opens the directory through `Deps.OpenPackage`, copies it next to the
versions of its adapter with every file and directory synced (`config.StageAdapter`),
parses the **copy** and checks that the copy lists this machine's platform
(`adapterreg.RunsOn`); what is judged is what would be installed, and a source that
changes while it is copied cannot make the check and the install disagree. A directory
or file of the package that cannot be read is refused as `invalid_argument` with the
detail `source_unreadable` and, in `Refusal.Path`, the place inside the package (empty
when the directory the owner named is the fault). It then has
the loop run the operation as any other, which looks the request up again (an identical
resend may have arrived meanwhile), refuses an upgrade of an adapter that has an
enabled instance, or a disabled one whose process the host still reports as ending
(`Adapters.Stopping`), as `adapter_in_use` (the instance runs the version that is
current, and a process that is ending may still hold its files, which a running
executable does not allow to be removed on Windows),
and commits one transaction, `adapter-install`: discard a leftover directory of the
version, rename the copy to the version, replace the pointer (the commit point), store
the result, discard the previous version. A package whose version is already the
current one stores only its result (`adapter-install-current`), and `AdapterInstalled.AlreadyCurrent`
lets the interface say so in that result. Last, the worker drops
what the commit did not take over (a refusal, a version found current, a resend that was
answered) and only then is the request answered, so that a refused package leaves
nothing and the answer never comes with staging still on the disk. After a fatal failure of the disk the worker
writes nothing more, neither the copy it has not begun nor the dropping: the next start
removes what is left (a copy under way finishes, because staging takes no context). A crash before the
pointer leaves the old version current and a directory that the repair of the adapter
directory removes at the next start; after it the new version is current and the repair
removes what is left of the old.

**Enabling and disabling an instance.** Both change `instances.json` through `config`
(one replacement, with the version of the whole collection checked inside and the
operation and its result in the same file) and what follows is driven by the state that
was committed, so a restart does the same. Enabling asks the host to run the instance;
the host starts it in the background and the owner reads whether it came up from the
list of the adapters. Disabling asks the host to stop it and marks it offline here, with
the time of the disabling, because the host reports nothing when it stops an instance
and `AdapterGone` is taken only for an instance that is enabled; it forgets the
admission, the probe and the tokens of the adapter that ran, and the runtime endpoint
stops being declared. With the admission gone, a request of the adapter that was past
the host's check when it was stopped is refused as `instance_unknown`: it cannot open
an endpoint that the closing would not reach and that would hold the next enabling for
ever. The next adapter that runs is admitted anew.
The endpoints of the instance are then closed, at most `Params.ChunkReplaces` at a time
(`instance-drain`, a continuation of the loop), each as any closed endpoint is closed,
its queued records closed after it in chunks. If a chunk cannot be written it is tried
again after `RetryBackoff`, and a crash between chunks is finished by the scan entry
(`instance-drain-replay`). An instance that is enabled again while any of its endpoints
is open or has records not closed waits for that to end (the request is held as a
registration is) so that the closing never meets an endpoint of an instance that is
enabled.

**The probe of an instance.** The adapter's report that its tool probes ran is
necessary and not enough: the node believes it only for the tokens it has seen
presented. `PresentProbeToken(token, front end)` is what the Agent interface calls: a
token no instance holds is not authenticated, a token presented before is
`probe_token_used`, one older than five minutes `probe_token_expired`, and a front end
other than the token's is refused (`invalid_argument`, `probe_frontend_mismatch`) and
leaves the token good, so a misconfigured front end does not use up the probe. The
token is compared with every token held, in constant time, and used up when accepted.
The probe of an enabled instance is `pending` until the adapter has reported and every
token issued for the round is presented or expired, `passed` when the adapter reported
every tool probe as ran and every token was presented (an adapter with nothing to probe
passes with its report), and `failed` when a tool probe did not run or a token expired
unpresented. A runtime endpoint becomes part of what the node declares the first time
its probe passes, and is not withdrawn when the adapter later becomes unusable, so that
the declaration does not flap; disabling the instance ends it. `AdapterList` reads the
installed adapters, the instances and the probe of each enabled one at one command.

`Declare` changes the declaration of the endpoint the caller is: `ByToken` is the
session endpoint of an open token, `ByAdapter` a direct endpoint of the adapter's
own instance. The declaration is the engine's field of the endpoint record
(compact JSON, fixed member order, at most 256 senders listed); the display name
and description the adapter gave are fields of the record of their own. A patch that names nothing is refused as
`invalid_argument` with the detail `empty_patch`: the detail is a typed value of
the `Refusal`, as the limit of `endpoint_limit` is, so that the interface maps it
without reading text. `Endpoints` lists for each endpoint how many messages wait
for it (`Queued`).

**The life of an adapter's connection.** The adapter host tells the engine, in
order, that an instance was admitted (`AdapterAdmitted`), what it answered to
`adapter.start` (`AdapterStarted`), asks for the probe tokens (`ProbeTokens`),
tells what `adapter.probe` found (`AdapterProbed`), then the notifications
(`AdapterNotified`) until the connection ends (`AdapterGone`).

The admission of an adapter that declared a runtime makes its runtime endpoint
exist, in the transaction `runtime-admit`: the id in `config/endpoint-ids/`, minted
once and kept for ever, then the record in `state/endpoints/`. An open record is
not written again; a closed one is opened again with a new token. Either way a new
or reopened endpoint takes a place in the cap of the node, exactly as a direct
endpoint does, and an admission refused for the cap (`endpoint_limit`, node) leaves
the instance not admitted. The token of a runtime endpoint is minted because the
store requires one and is shown to no one. The admission returns what
`adapter.start` carries: the node, the runtime endpoint, the instance's values, the
presets with the values each holds for this adapter, the open direct endpoints by
key and the sessions (none yet).

An instance is online from `adapter.start` until the engine is told the connection
ended, and offline otherwise. The time it went offline is the engine's own clock at
the report, and the earlier time stays when it is reported again; an instance that
has not been online since the engine started is offline since an unknown time. The
host reports nothing when it stops an instance (`Host.Stop`, `Shutdown`, `Abort`), so
the disabling of an instance marks it offline itself, as the report of a lost
connection does, and `AdapterGone` is taken only for an instance that is enabled.
`AdapterSignals` is `adapterreg.Signals` over the last availability snapshot and
that observation. The snapshot, the busy turns and the requests the runtime saw
refused are memory: they are rebuilt by the adapter after a restart, and a
notification about an endpoint the engine does not know is kept by reference and
writes nothing. The probe result is kept per instance; when its runtime is outside
the tested ranges of the installed manifest the owner gets one alert of kind
`version_untested`, once for the same instance and words, across restarts too
because alerts are persistent. `ProbeTokens` makes a token for each front end of
each preset, valid for five minutes on the injected clock and kept in memory only;
the Agent interface will present it.

A checkpoint (`CheckpointPut`) is one replaced file under `state/checkpoints/` in
the transaction `checkpoint-put`, answered after the commit, stored compact, one
value for each (scope, id). It is taken while the node recovers as well as after it
serves, because adapters start before the node serves and a session being taken
back may put its state at once. The scope `session` needs an open session endpoint
of the instance, so until sessions can be created every such put is refused.

`Quiesce` judges, in the same loop command as its other conditions, each instance
named in `Expect.Adapters` against the host's counts: the notifications handled
equal what the adapter says it sent, none is buffered, no request of the adapter is
being answered, the host is not working, and the requests the host has sent that
have no answer are exactly the ones the caller says the adapter holds on purpose
(as multisets). Instances not named are not judged.

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
work: a retired identity first (`identity_retired`: the node never starts again, so an
acknowledgement would promise a delivery nothing will make), then a clock set back
(`clock_rollback`), both by the one judgement of signing (`SignRefusalOf`), then the queue (`node_queue_full`
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
the system endpoint as one. A repeat of an
idempotency key returns the first message when the content is the same and is a
conflict when it is not; the same content is the request digest the target compares
with, made with the first request's deadlines because the deadline a repeat gets is its
own and says nothing of the content, and the same request is also for the same
recipients in the same order, which that digest does not cover unless they are disclosed;
whether they are disclosed is part of the request, so the source record keeps the flag and a
message sent again rebuilds its envelope from it, with several recipients being `len(To) > 1`
(`TestAMessageToSeveralRecipientsSentAgainIsTheSameRequestWhetherOrNotTheyAreDisclosed`,
`TestTheSameIdempotencyKeyDisclosingTheRecipientsDifferentlyIsAConflict`).
What no node would take is what the envelope refuses: the request is made into its
envelope and encoded before anything else is judged, and the envelope's own errors name
the field (a body over the limit is `too_large`, the others `invalid_argument`). Then the capacity check, then the next sequence
number of the pair (sender endpoint, recipient address) from the store's book, and the
body and the source record in one transaction. A reply finds its recipient and its
reply-to in the node's own record of the message the endpoint received (a message the
endpoint refused at intake has a record, but the endpoint never had it, so it is not one
that can be answered); the caller names
neither. The deadlines are whole seconds, because that is what an envelope can say and
the target's record must agree with it.

**Asking about a message.** `MessageStatus` answers from the sender's record of the message, which the reports of the targets bring up to date: one entry for each recipient in the order the sender gave them, with the state and, for a rejection, the reason, named as the delivery rules name them. A message the caller's endpoint did not send is `message_unknown`, whoever sent it, so no endpoint learns what became of another's. The control reply that a control signal's status carries is not part of the result until control signals are sent.

**Sending.** One worker, outside the loop, makes the calls to the transport in the order
the work was given: a send of the envelope (made anew from the source record and the
body, with the evidence of a reply signed at that moment) or a pull of the reports of a
message. The loop schedules the next call and applies each result. A send that got no
answer is made again every `ResendBackoff` (30 s) until the delivery deadline, to each
node that has not answered (the envelope is made anew, so a reply's evidence is signed
anew, and the request digest is the same); a delivery that is not settled is asked about
every `PullInterval` (1 min) until its report deadline, which already holds the clock
tolerance, and once more after it (below); after the delivery deadline a node that never
answered is asked and not sent to. A send that carries evidence is put off while the wall clock cannot be trusted. The reports in a receipt or an answer are
verified with the target's certificate and applied to the source record by the envelope
package's one rule; the record is written with the feedback the change calls for and with
the body reference dropped once no delivery needs the body. A report that does not verify
or that no state can follow is logged and left out. A pull names the recipients on the node
whose deliveries are not settled (`outboundPlan`; the send of a node still owed one carries
all its recipients, so no pull is made for it). A derived delivery, which only the record of
a control signal has, would be planned like the others, but the worker takes no control
record, so none is asked about. The answer to it is read as three different things: an error is no answer and
the pull is made again after `ResendBackoff`; a recipient in `NoRecord` is applied by the
envelope package's rule for it, which turns an accepted delivery inside its report window
into `delivery_unknown` (`lost_at_target`) with one notice, and ignores it for every other
state or after the window (the answer was slow, and the window the target keeps its
records for is over); a successful answer that has the recipient in neither list says
nothing yet. What the answer lacks is also a fact of its own: a successful pull that has
no report for a delivery that has no receipt, whether the recipient is listed as having no
record or is in neither list, is a query that found no receipt
(`delivery.EventExpiryQueried`, dated now), the only thing an expiry is judged by, and it
is applied in `applyAnswer`, the one place the answers are read. A call that got no answer
is the other fact: the first failure of a run of failures to a node is remembered (in
memory, per record and node) and each further failure applies
`delivery.EventContactFailing` with that start, so that the unreachable hint is given
once contact has failed for two minutes while the sender still retries; an answer that
comes ends the run. Both go through the envelope package's `ApplyObservation`, and
neither is applied while the wall clock is in doubt. The source never
sends the message again because of a `NoRecord`: a message it holds a receipt for is not in
doubt, only the target's record of it is, and whether to send again is the agents' to decide
from the notice. The reports and the `NoRecord` entries are applied by one function and
written in one transaction with the feedback they call for, and a `NoRecord` for a recipient
the record does not track is dropped. A target says at most one thing about a recipient, so
a `NoRecord` for a recipient the same answer reports is logged as the peer's fault and left
out: the report is signed and stands, and the listing is not applied. Nothing of the sending is persisted:
the last step of the start looks at every unsettled source record again.

**The pull before the close.** A source delivery that is still `queued` or `accepted`
after `report_by` has never been told an outcome to its sender, and the target keeps its
record, and answers pulls from it, for about a day longer (`finalPullUntil`, which is
`reply.RetainFactsUntil`: the longest term of what this implementation's store keeps for a
delivery, a pin in `TestTheEnginesLastPullIsNeverLaterThanTheStoreKeepsARecord`). The
protocol guarantees only `RecordRetainUntil(report_by)`, so the pull after it is an extra
chance taken while this implementation still keeps the record, not a promise of the
target. So its
close waits for one more pull of the target (`finalPullDue`; `workFinalPull`), the only
pull made after `report_by`. The answer is applied by the same rules as any other, with
its true meaning though `report_by` has passed: a rejection is told as a rejection, a
delivery stays silent, a hand-over is told (`ApplyReport` judges a report by the time the
target recorded its state, not by when it arrived). Then, whatever came of the pull, the
loop ticks the record: what is still `queued` or `accepted` closes with the notice the
delivery rules give for that (the expiry, or that the result could not be learned). A
failed call is not tried again, an answer that the target holds no record is ignored after
`report_by` (the target may have forgotten legitimately, so it is not a loss), and when
the target's retention is already over the delivery closes at once, with no call. A
delivery in any other state (`expired_unconfirmed`, `delivery_unknown`) is closed without
the pull, silently: the sender holds the notice. Nothing is persisted for this: the loop
remembers which deliveries have been pulled (`outbox.finalPulled`), and after a restart
the pull is made again, which is harmless. At the start the tick of step 12 leaves such a
delivery open and step 14 makes the pull, so a node that slept past `report_by` learns what
its targets settled before it closes and tells anything; while the node runs, the tick at
`report_by` asks the worker for the pull and a pull still in flight when `report_by` passes
is followed by it. The tick also moves the record's own entry of the schedule to the
first instant after `finalPullUntil`, so that a delivery whose pull never comes (the
worker was busy with a send that a conflict then ended, or the window was over before it
took the record up) still closes with its notice when the target's retention is over,
without waiting for a restart. While the wall clock is in doubt no pull is made either
(`finalPullDue` is false while the node is paused, and the tick does not close): the
pull is a step towards judging a deadline, so after the owner clears the pause the tick
that was put back asks for it and the close follows. A delivery of a record the worker does not carry (a control signal, a
lost record, an engine without a transport) closes at once, so nothing waits for a pull that
cannot come; `outboundWorks` says which. A delivery removed from the outbox because the target
answered a send with a conflict is still queued, is no longer sent to, and is still pulled
before its close; what that learns depends on what the target holds. When the target cannot
be reached or holds no record of the message, the delivery closes with the notice like any
other. When the target holds the conflicting request, its pull returns that request's
report (a report is not bound to the digest of the request it was made for) and the
delivery follows it, as it would follow any report of the target; the engine does not tell
the two requests apart.

**The passing of time.** Each target record and each source record has an entry of the
schedule at its next time-driven change (`Record.NextTick`), which the view keeps current
(`view.onTick`: a record taken in or dropped moves its entry, so no write has to
remember to). When entries fall due their records are ticked together: a target message
nobody read is rejected as `backlog_expired` when the endpoint's queue time runs out (the
body reference goes with it), and any record not final after `report_by` is closed: a target
record silently, a source record with the notice the delivery rules give for the state it was
in (nothing for a delivery the sender was told about), after the pull described above. The records go to one transaction (`tick`) in chunks of `ChunkTickRecords`, with
the feedback the changes call for, and the entries of the ones that changed move with the
view. Startup step 12 ticks every record that is due before the feedback of step 13 goes
out. Control records are not ticked here: a control signal is settled with its control
reply, which belongs to the creation of sessions. A target record under a lease is
ticked by the same rule as any other; leases are made by the hand-out of messages, which
the pull of messages adds together with the rule of when an expired lease is released. While the wall clock is in doubt
nothing is ticked and nothing is judged expired: a clock set back and then moved on is
not one to judge a deadline by until the owner has cleared it, and what the node took in
before the pause stays as it was.

**Damage.** A file of `state/` that does not check out is settled by the store's
quarantine at the scan entry, first of the rules and one file at a time, each with a
scan taken after the one before: every other rule reads what a settlement raised,
overwrote or wrote. What the engine adds is what the delivery rules say about the
loss. A source record whose first line can be trusted is overwritten with the
sender's lost record (`delivery.NewLostRecord` for each recipient: `delivery_unknown`,
cause `lost_at_sender`), and the feedback it owes goes into the queue with it; a source
file that is all zeros has no record to replace, and the store writes an out-lost
record that names the sending endpoint, whose one item (`KindLostUnknownMessage`) is
loaded into the queue at the start and told by one notice that covers it however many
files were lost; a target record is removed and nothing replaces it, because a target
keeps no loss: its source learns it from the next pull, in which the target says it holds
no record. The alert ids are the store's. A damaged file of `ctl/` is the one thing not
settled: removing it would remove a control signal's record before the pull of
control signals answers from `ctl/`, so the scan entry still refuses to open and says
which rule, before it has written anything in that pass, damaged files beside it
included.

A body that is not what its record says is found when something reads it
(`readBody`, which the sending of a message uses and the hand-out of a message will
use). Every body belongs to one record, so that one record is settled by what it still
needs: a target record that needs it is removed and leaves the view, a source record
that needs it is replaced by the lost record, a control record or any other record
that does not keeps its state without the reference; the settlement has an alert of
its own. A write that
meets a damaged floor file while it runs (`DamagedFileError`), which is the one
damaged file a write can meet, has the file settled and is tried again; a damaged
file of another directory is not settled while the node runs, and the write fails
with it, logged. The the book of sequence numbers is built again from the disk
afterwards: a settlement raises floors, and the numbers of the sender must stay above
everything a lost record could have used.

**Feedback.** What goes wrong with a delivery is told to the endpoint that sent the
message, once, by a notice of the system endpoint that the endpoint receives like a
message. The delivery rules decide that feedback is owed, and the write of the record
that made the decision stores the item beside it (`withFeedback`); `putOut` is the one
place that takes a written source record into the view, and it queues the items the
record holds that the one before it did not. The queue (`delivery.FeedbackQueue`)
merges items of one kind of anomaly, waits out the merge window (1 minute) and holds
back what an endpoint's rate cap (10 notices in 10 minutes) does not let through, to
send it later. One entry of the schedule is due when the queue next has a notice; a
notice is written by `FeedbackForm` as a record of the system endpoint that is exempt
traffic: a full queue of the endpoint does not refuse it, it takes no place of that
queue, and it does not go through the capacity check, which judges new work. Its key
is made from its items alone, naming each kind by its name rather than its number so
that a kind added later does not change the key of a notice of another, and so a
notice formed again after a crash is the same record. It covers its items by the name of the record that holds each; once the
notice is written the items are taken off those records (`FeedbackClear`), a write
of that which failed is tried again after the retry back-off (`retryClears`), and the
replay of feedback at the start does it for a crash in between. The words of a
notice are the summaries of the kinds (`delivery.Kind.Summary`), one line for each
delivery. Closing ends an endpoint's life for feedback: an endpoint that is not open
when its notice is due is told nothing, and a direct or runtime endpoint opened again
with the same id does not hear of what happened before it was closed, as the records
queued for a closing endpoint are not carried over to the reopened one either; the
items are taken off their records with a word in the log. The queue is
memory, built by the scan entry after the replay of feedback from the items the
records hold and from the notices on disk, so a restart loses no item and gives no
endpoint a new rate window; step 13 sends what fell due while the node was down.

**Taking in an envelope.** `Inbound` is the handler of the transport. For each recipient
on this node it finds the record of the request or judges it: a repeat gets the record's
report, another body under the same message identifier is refused as a conflict and
nothing changes; for a new request the reply permission and its evidence are decided,
then the endpoint (a closed endpoint is rejected as `endpoint_closed`; one that is not
there, one whose creation has not ended and a runtime endpoint, which takes control
signals only, as `endpoint_missing`), then the authorization check, then the deadline and the endpoint's
backlog. A request from the loopback is judged by the endpoint's declarations alone; one
that came any other way is judged by the node's rules with no trust, which is denied until
trust exists. The accepted record holds the body, the rejected one does not. The reports
of the receipt are signed before anything is written and the records, with the source
record the evidence of a reply changed, are written in one transaction. While the wall
clock cannot be trusted nothing is signed, so an envelope and a pull are refused and the
sender tries again.

**Answering a pull.** A pull names a message and the recipients on this node the asking
source tracks a delivery of it to. For each recipient the answer holds the signed
report of the target record, found by the asking node and the message, or the recipient
in `NoRecord` when there is none. A record the node holds can always be signed (every
state has the moment it was entered), so the one reason a held record goes unreported
is a refusal of the whole answer: while the node may not sign (a clock in doubt, a
retired identity) the pull is refused with that reason and `NoRecord` is not given
alone, because a "no record" next to no report would be said by a node that cannot
vouch for the rest of the answer. A recipient the request does not name is not
answered and one it names twice is answered once; a request that names none is
refused as an invalid argument, since the answer for no recipient is nothing the
source can use. The target only ever sees healthy records: it has no shape for a
record it lost, so absence is the only way a loss shows. Control signals have
no pull path in the engine yet; when they do, the same rule applies to the records of
`ctl/`.
A report is dated with the moment the record entered the state it reports
(`Record.StateAt`, so a rejection made later by an endpoint's closing carries the time of the
closing, not of the acceptance) and, for a delivered record, with that moment as the delivery
time (`TestAReportIsDatedWithTheMomentTheTargetRecordedTheStateItReports`); every state has a
report, and a pull for an unchanged state repeats the same time.

**The start, as far as the engine is concerned.** The steps of the start are numbered
in the order the node takes them; the engine takes part in these:

| Step | What | Engine |
| ---- | ---- | ------ |
| 6 | Scan `state/`, quarantine and repair, rebuild the view | `Open` (the scan entry) |
| 8 | Management interface opens; reads answer, changes are refused | `Start` |
| 10 | Adapter instances start and are admitted | `AdapterAdmitted`, `AdapterStarted`, `ProbeTokens`, `AdapterProbed` |
| 11 | Check the creation intents and prompt attempts that were not settled | phase `reconcile` of `RecoverDelivery` |
| 12 | Run `Tick` on every record, release leases whose turn is not active | phase `tick` (a source delivery still queued or accepted past `report_by` is left open for the pull of step 14) |
| 13 | Send the feedback that fell due | phase `feedback` |
| 14 | Go on with the loopback of outgoing messages | phase `outbound`: every unsettled source record is sent or asked about again, and a delivery past `report_by` that waits for its last pull is pulled and closed |
| 15 | The Agent interface serves in full | `Serve` |

**Recovery.** `PlanRecovery` is a pure function of a scan: what each recovery rule
has to do. `Open` runs the rules that have work in the order the store fixes
(`quarantine` first, then `ctl-effect-replay`, `instance-drain-replay`,
`orphan-gc-endpoint`, `endpoint-drain-replay`, then the replay of feedback, then `orphan-gc`), works the plan out again from a fresh scan after each rule
that wrote, and repeats until the plan is empty. `RecoverDelivery` runs the startup steps
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
  was lost is the common failure of a send; the sender sends it again and the
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
- **The runtime endpoint is made at admission, with the id kept in `config/`.** The
  adapter protocol promises one runtime endpoint per instance that outlives the
  process, and `adapter.start` names it; making it where the instance is admitted
  keeps one place that decides the cap, the id and the token. The id is kept before
  the record, in one transaction, for the reason the direct endpoints' is: a crash
  between the two leaves an id no record uses, and the next admission builds the
  endpoint under it. A reopened endpoint gets a new token, as a reopened direct one
  does.
- **A runtime endpoint that is still draining is not opened again.** The records
  queued for it while it was closed would land on the open one. The admission fails
  loudly instead of waiting in the loop: the host ends the connection and starts
  the instance after its backoff, which is by when the drain is done.
- **The offline time is the engine's clock, and an unknown start stays unknown.**
  An adapter's clock is for reference only. An instance that never came online has
  no moment to name, and inventing one at the first failed start would hide that.
- **The state an adapter reports is memory.** The adapter says it again on every
  connection (a complete availability snapshot, `turn_active` of each session in
  its start result), so keeping it on disk would only make a restart show state
  that nobody has confirmed.
  For the same reason a new connection does not inherit the state of the one before:
  a successful admission forgets the busy states and the snapshot, and the start
  result replaces the busy states instead of being merged into them, because no
  report of the end need have cleared them.
- **Probe tokens are not on disk.** A token that survives a restart proves nothing
  about the reach of the new process, and a secret that is not written cannot be
  read back from a backup.
- **`checkpoint.put` runs in the recovering phase.** The adapters start before the
  node serves; waiting for `Serve` would hold a session being taken back for the
  whole of the recovery.
- **`Quiesce` polls the host's counts on the real clock while an adapter does not
  hold.** The host's counters move when the adapter or the host does, not when the
  engine does; the engine's own activity alone would not wake a waiter that looked
  a moment too early.
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
- **Recovery work of an unwired rule stops `Open`.** Opening over a damaged control
  record would leave it out of the view as if it were not there, silently. The refusal
  names the rule and happens before any write of that pass.
- **Time is read from the records, by the schedule the view keeps.** A timer for each
  rule of the delivery package would be a second statement of when a record changes;
  `Record.NextTick` says it once, and an entry that follows the view cannot be left stale
  by a write that forgot it. A record the rules do not change is not looked at again until
  it is written: an entry that fell due and found nothing would otherwise spin the loop.
  Nothing is judged by a clock the owner has not cleared.
- **A query that was answered and found no receipt is the only thing that expires a
  delivery, and a failing contact is the only thing that is told for one nobody could
  ask.** Expiry says the target may have the message, which would be false news if the
  question had not been put to it; and a target unreachable for the whole of a
  delivery has no report to carry the news, so the failure of the contact is the only
  word the sender gets, once, before the deadline. An accepted delivery whose target is
  lost from some moment to `report_by` gets no word from a failing contact, and the
  target may have rejected it meanwhile, so it is not closed silent: the pull before the
  close learns what the target settled, and when it cannot the sender is told that the
  result could not be learned (`TestAnAcceptedDeliveryWhoseTargetIsLostToTheEndIsNotClosedSilently`).
- **The close waits for one last pull, and a node asleep past `report_by` is the common
  case.** A node that is off for longer than a delivery's life wakes to deliveries whose
  targets settled them meanwhile; closing them at the start would tell nobody, or tell
  "unknown" where the target holds a signed rejection. The target keeps its records for
  longer than `report_by` for this, so one pull is worth making, and only one: a node
  that cannot be reached will not be asked again, the sender is told what it is owed, and a
  node that is asked after the target's retention would learn nothing. The tick does not
  close ahead of the pull because it asks one question ("is the pull made?") of one
  place, `finalPullPending`, and the delivery rules stay free of any timer.
- **Feedback is queued where the view takes a record, and told by a notice that covers
  it.** A decision is stored with the record that made it, so a crash cannot mark a
  delivery reported without its feedback; every write of a source record goes through
  `putOut`, so no write can decide feedback that the queue never sees. A notice and
  the removal of the items it covers are two transactions, with the notice first:
  the other order could lose an item, this one can only leave it on its record until
  the clearing is tried again, which does not wait for the next start: the notice may
  be swept by then, and with it the covers the replay of feedback reads. The key of a notice is a function of
  its items so that one formed again is the same record and not a second notice. The
  queue is rebuilt from the disk at each start instead of being stored: it would be a
  second copy of what the records and the notices say.
- **A damaged file is settled before anything reads it, one at a time, and a lost
  source record is replaced by the sender's, never a target's.** The settlements raise
  floors, overwrite records and write notices that the next one reads, so each takes a
  scan after the one before and the quarantine is first of the rules. The record that
  stands for a lost one of the sender says its deliveries are unknown and owes the
  sender a notice; a target that lost a record cannot say so, the answer to a pull is
  the one place the loss shows, and a replacement there would have to claim something
  the target does not know. `ctl/` is left alone until the pull answers from it: the
  start is refused, loudly and before anything is written, instead of removing the
  record of a signal that may have run.
- **A body is judged where it is read, and the record that owns it is settled
  there.** Reading every body at the start would cost the whole disk for a rare event,
  and the one who reads it is the one who needs it; the settlement does not leave
  the record for the next reader to find. A body belongs to one record, so no other
  record is affected.
- **A notice is the system endpoint's, and exempt.** It takes no endpoint backlog
  and passes no capacity check, but it counts in the node's unfinished records, and so
  in the node queue limit. The anomaly it reports would go unheard if a full queue
  refused it, and a notice about a notice is never made, so a refusal would be
  silent; the rate cap is what bounds notices.
- **The view takes the records the engine passed to a write.** A write of target
  records stores them exactly as given, so the engine puts the same records in its
  view once the write returns nil, and never after an error.
  `TestViewMatchesAFreshScanAfterWrites` compares the view with a fresh scan after
  writes made in several chunks, so a view that drifted from the disk fails there.
  The recovery rules keep no view (it is built from the last scan).
- **An accepted control signal is never rejected.** Closing a control record of a
  closed endpoint is what the delivery rules say: `delivery_unknown`.

Prior art taken: the typed command with a capacity-one answer and a context that
does not retract a handed command (etcd raft node), the heap with one timer re-armed
to its top (Consul's expiry heap, client-go's delaying queue), chunking by count
(etcd's compaction), fatal exit decided by the owner of the process and not by the
storage layer (PostgreSQL's `fsync` PANIC, etcd's WAL failure). Rejected: closures
as commands (the kinds are not countable), a time budget for chunks (not
reproducible).

- **The pause is cleared by the owner alone, in one replacement.** The mark and the
  flag are one file; clearing writes both at once, so no disk state has the flag
  gone and the mark unchanged, and a clock that really was set back is trusted
  again from the owner's word. Moving the clock forward does not clear it, because
  nothing but the owner has looked at the clock.
- **The statement of the retirement is the one the keys verified.** The engine does
  not read the file again and compares no node of its own: the retirement is for
  good, and a statement read again would be one more place to be wrong. Retiring
  refuses signing from the commit, because the daemon is still up until the stop
  is carried out and a report signed in that interval would be signed by an
  identity the owner has just retired.
- **Reminders are facts, not records.** A record of "the backup is not confirmed"
  would be a second copy of what `config/revocation-backup.json` says, and a crash
  between the two would leave a reminder for a fact that changed. Deriving it from
  the authority means a confirmation of another statement does not count, and the
  node made again over the same data directory reminds again.

## Where

| Rule | Code | Tests |
| ---- | ---- | ----- |
| The 257th endpoint is refused | `endpoints.go` (`registerDirect`) | `TestEndpointBeyondNodeCapRefused`, `TestTheNodeCapCountsOtherKindsOfEndpointOnTheRegistrationPath`, `TestInstanceLimitRefusesBeforeTheNodeCap` |
| Same key, same endpoint; a new token on reopening | `endpoints.go`, `tokens.go` | `TestSameKeyGivesTheSameEndpointAcrossARestart`, `TestReopeningAnEndpointKeepsItsIdAndGivesItANewToken` |
| The registration crashes anywhere | `transactions.go` | `TestDirectRegisterRecoversFromACrashAtEveryPoint` |
| The runtime endpoint is made once, its id is kept, a closed one is reopened with a new token | `adapters.go` (`admitRuntime`), `transactions.go` | `TestAdmissionOfARuntimeAdapterMakesItsRuntimeEndpointOnceAndKeepsItsId` |
| The runtime endpoint counts against the node's cap; a refused admission leaves the instance not admitted | `adapters.go` (`admitRuntime`) | `TestRuntimeEndpointCountsAgainstTheNodeCap` |
| The admission crashes anywhere | `transactions.go` | `TestRuntimeAdmitRecoversFromACrashAtEveryPoint` |
| What `adapter.start` carries | `adapters.go` (`startParams`) | `TestAdmissionReturnsWhatAdapterStartCarries` |
| Probe tokens | `adapters.go` (`probeTokens`) | `TestProbeTokensAreOnePerPresetAndFrontEndSingleUseAndFiveMinutes` |
| A runtime outside the tested ranges is one alert | `adapters.go` (`probeResult`) | `TestVersionUntestedAlertIsRaisedOnceForARuntimeOutsideTheTestedRanges` |
| Availability becomes handoff's signals; online and offline | `adapters.go` (`notified`, `gone`, `AdapterSignals`) | `TestAvailabilitySnapshotsBecomeHandoffSignals`, `TestAnAdapterThatIsGoneKeepsItsEndpointsItsRecordsAndItsAdmission`, `TestAnInstanceTheConfigurationDoesNotEnableIsUnknownToReadsAndToGone` |
| Notifications about unknown endpoints write nothing; the busy state at start | `adapters.go` (`notified`, `markStarted`) | `TestNotificationsAboutUnknownEndpointsAreAcceptedAndSetNothingOnDisk`, `TestAdapterStartedTakesTheBusyStateOfTheSessionsItKnowsAndIgnoresTheRest`, `TestANewConnectionStartsFromWhatItReportsAndNotFromTheOneBefore` |
| A checkpoint is on disk before it is answered | `adapters.go` (`checkpointPut`) | `TestCheckpointPutIsOnDiskBeforeItIsAnsweredAndALaterPutReplacesIt`, `TestCheckpointPutRecoversFromACrashAtEveryPoint` |
| `Quiesce` judges the host's counts | `recovery.go` | `TestQuiesceJudgesTheAdapterHostsCounts` |
| The result of a close is stored with it | `endpoints.go` (`closeEndpoint`), `manage.go` (`CloseEndpointOp`), `view.go` (`results`) | `TestCloseStoresTheResultOfTheOperationWithTheHeadRecord`, `TestTheStoredResultSaysWhatTheCloseDidForEveryKindOfEndpoint` |
| One identifier has one result, for every operation and every place a result is kept | `manage.go` (`Execute`, `execute`, `addConfigResults`), `recovery.go` (`Open`) | `TestAResendReturnsTheStoredResultAndAReusedIdentifierIsRefused`, `TestConcurrentResendsOfOneRequestRunOnce`, `TestARefusedRequestStoresNothingAndTheIdentifierStaysFree`, `TestAVersionedChangeIsAppliedOnceAndAStaleOneNeverIsApplied` |
| A crash at any point of an operation leaves nothing, the effect alone or both, never the result alone; a resend is carried out again or answered from the result | `manage.go`, `endpoints.go` (`closeWithResult`) | `TestAnAdministrativeOperationRecoversFromACrashAtEveryPoint` |
| A stored result is read by its request key, from both places, and by no other key | `manage.go` (`OperationResult`) | `TestTheStoredResultOfAnOperationIsReadByItsRequestKey`, `TestAStoredResultIsReadWhileRecoveringAndNotWhileStopping` |
| The door is closed while the node starts and while it stops; what is not a change does not come through it | `manage.go`, `engine.go` (`step`) | `TestExecuteIsRefusedWhileRecoveringAndWhileStopping`, `TestExecuteRefusesAnOperationThatChangesNothing` |
| A request that stops the daemon says so, resent or not | `manage.go` (`StopDaemonOp`, `StoredResult.Stop`) | `TestStopFollowsTheOperationOfTheRequest` |
| The owner's clear of the pause is one replacement and the loop forgets the pause after the commit | `operations_clock.go`, `transactions.go` (`clock-clear`), `engine.go` (`clearRollback`) | `TestClockRollbackPausesSigningUntilCleared`, `TestARestartedNodeThatWasPausedIsClearedByTheOwnerAlone`, `TestAWallClockSetBackPausesNewWorkUntilTheOwnerClearsIt` |
| The backup is confirmed only for the statement the node holds; the reminder is derived | `operations_identity.go`, `nodestatus.go` | `TestTheBackupIsConfirmedOnlyForTheStatementTheNodeHolds`, `TestAConfirmationOfAnotherStatementDoesNotPutTheReminderOut` |
| The retirement keeps the statement, asks for the stop, and nothing is signed after it | `operations_identity.go`, `signing.go` (`canSign`) | `TestRetiringTheIdentityKeepsTheStatementAndAsksForTheStop`, `TestAfterTheIdentityIsRetiredNothingIsSigned` |
| After the retirement a send is refused and nothing is recorded; a periodic task is told that the node may not sign | `capacity.go`, `signing.go` (`SignRefusalOf`), `engine.go` (`fire`) | `TestAfterTheIdentityIsRetiredASendIsRefusedAndNothingIsRecorded`, `TestCheckCapacityTable`, `TestAPeriodicTaskIsToldWhetherTheNodeMaySign` |
| An alert is acknowledged and takes the file it names | `operations_alerts.go` | `TestAcknowledgingAnAlertMarksItAndTakesTheFileItNames` |
| Settings are a versioned change through the door and are in force for the next command | `operations_settings.go` | `TestChangingTheSettingsOfTheNodeIsAVersionedChangeThroughTheDoor`, `TestChangingTheSettingsOfAnEndpointFindsItsInstance`, `TestChangedSettingsAreInForceForTheNextCommand` |
| The owner's operations recover from a crash at every point | `transactions.go` | `TestTheOwnersOperationsRecoverFromACrashAtEveryPoint` |
| The status and the export | `nodestatus.go` | `TestStatusShowsWhatTheNodeKnowsOfItself`, `TestTheExportOfTheSelfRevocationIsTheStatementOnDiskAndIsStoredNowhere` |
| A package is copied once, outside the loop, and a resend copies nothing | `manage.go` (`preparedOperation`, `cmdPrepare`, `replayOf`), `operations_adapters.go` | `TestInstallingACopiedPackageStoresItsResultAndAResendCopiesNothing` |
| The copy is what is judged | `operations_adapters.go` (`stagePackage`) | `TestThePlatformIsJudgedOnTheCopyThatWouldBeInstalledAndNotOnTheSource`, `TestAPackageThatIsRefusedLeavesNothingAndItsIdentifierFree` |
| The version that is current is installed again by storing its result; an upgrade needs no enabled instance | `operations_adapters.go`, `transactions.go` | `TestInstallingTheVersionThatIsCurrentWritesOnlyItsResult`, `TestUpgradingWhileAnInstanceIsEnabledIsRefusedAndOtherwiseReplacesTheVersion` |
| After a fatal failure of the disk an installation writes nothing more | `manage.go` (`runPrepared`, `diskFailed`) | `TestAnInstallationWritesNothingMoreAfterAFatalFailureOfTheDisk` |
| One installation at a time, counted by `Quiesce` | `manage.go` (`cmdPrepare`) | `TestOnlyOneInstallationRunsAtATime` |
| An installation, and the store of the result of one that found its version current, recover from a crash at every point | `transactions.go` (`adapter-install`, `adapter-install-current`), `config` (`adapter-stage`, `adapter-unstage`) | `TestAnInstallationRecoversFromACrashAtEveryPoint`, `TestStoringTheResultOfAnInstallationThatFoundItDoneRecoversFromACrash` |
| Enabling starts the instance through the host; what is refused starts nothing | `operations_adapters.go` (`enableAdapterOp`) | `TestEnablingAnInstanceStartsItThroughTheHost` |
| An upgrade is refused while the process of a disabled instance is still ending | `operations_adapters.go` (`instanceStopping`), `config` (`PlanInstall`) | `TestUpgradingWhileTheProcessOfADisabledInstanceIsStillEndingIsRefused` |
| Disabling stops the instance, marks it offline, closes its endpoints | `operations_adapters.go` (`instanceDisabled`), `instance_drain.go` | `TestDisablingAnInstanceStopsItMarksItOfflineAndClosesItsEndpoints` |
| A request of the adapter after the disabling is refused, and enabling again completes | `operations_adapters.go` (`instanceDisabled`) | `TestAnAdapterRequestAfterTheDisablingIsRefusedAndEnablingAgainCompletes` |
| Enabling again waits for the closing of the disabling | `operations_adapters.go` (`instanceClosing`), `manage.go` (`waitForDrain`) | `TestEnablingAnInstanceWaitsForTheDrainOfItsDisabling` |
| Disabling, enabling and the presets recover from a crash at every point | `config` (`adapter-disable`, `adapter-enable`, `presets-change`), `store` (`instance-drain`) | `TestDisablingAnInstanceRecoversFromACrashAtEveryPoint`, `TestEnablingAnInstanceAndChangingPresetsRecoverFromACrashAtEveryPoint` |
| Presets carry the class of the change and the version of the collection | `operations_presets.go` | `TestPresetsAreAddedAndRemovedWithTheirClassAndTheCollectionVersion` |
| The probe passes only for the tokens the node saw; a token is used once, expires, and is for one front end | `probe.go` | `TestAnInstalledAdapterIsDeclaredOnceItsProbeHasPassedAndTheTokensWerePresented`, `TestAProbeTokenExpiresAndAFailedToolProbeFailsTheProbe` |
| Closing, chunks, bodies | `drain.go`, `endpoints.go` | `TestCloseRejectsTheQueuedDeliveriesInChunksAndRemovesTheirBodies`, `TestCloseBatch…` (`drain_internal_test.go`), `TestRegisteringTheKeyOfAnEndpointStillDrainingWaitsForTheDrain`, `TestARegistrationWaitsAcrossAFailedChunkOfTheDrain` |
| Declarations and callers | `endpoints.go`, `declaration.go`, `view.go` | `declare_test.go`, `declaration_internal_test.go` (golden vectors, refusals, `FuzzDecodeDeclaration`), `TestTheRecordHoldsTheNameAndTheDescriptionApartFromTheDeclaration`, `TestResolveGivesTheEndpointOfACallerAndRefusesTheRest` |
| Capacity | `capacity.go`, `recovery.go` (`admitNewWork`) | `TestCheckCapacityTable` (`capacity_internal_test.go`), `TestAdmitNewWorkReadsTheSettingsTheDiskAndTheViewOfTheEngine` |
| The loop, inbox, back pressure, fatal | `engine.go` | `loop_test.go` |
| The schedule | `sched.go`, `engine.go` | `sched_test.go`, `sched_internal_test.go` |
| The pause of a clock set back is persisted at once | `engine.go` (`observe`, `AddClockState`) | `TestThePauseOfAWallClockSetBackIsPersistedAtOnceAndSurvivesACrash`, `TestAFatalErrorWhilePersistingThePauseStopsTheEngine` |
| Recovery | `recovery.go` | `recovery_test.go` |
| Waiting | `waiters.go` | `waiters_test.go` (`TestShutdownEndsEveryWaitEvenWhenTheInboxIsFull`, `TestAPageThatIsReadyWinsOverTheShutdown`) |
| An anomaly is told once, in one notice that waits out the window and the cap; the queue survives a restart; feedback for a closed endpoint is dropped | `feedback.go` (`putOut`, `flushFeedback`, `sendNotice`, `loadFeedback`) | `TestAnAnomalyIsToldToTheSenderOnceInOneNotice`, `TestNoticesOverTheRateCapAreHeldAndNeverDropped`, `TestFeedbackThatFellDueWhileTheNodeWasDownIsSentAtTheStart`, `TestFeedbackForAnEndpointThatWasClosedIsDroppedAndLogged` |
| A notice goes to an endpoint whose queue is full and takes no place of it | `feedback.go` (`formNotice`) | `TestSystemFeedbackIsNotRefusedByFullBacklog` |
| A notice formed again is the same record; a notice that failed to be written goes back whole | `feedback.go` (`noticeKey`, `unsend`) | `TestTheKeyOfANoticeFollowsFromItsItemsAlone`, `TestTheKeyOfANoticeNamesItsKindsByTheirNames`, `TestUnsendPutsBackWhatFlushTookOut` |
| Every write that tells an endpoint survives a crash | `feedback-form`, `feedback-clear` (declared by the store) | `TestFeedbackRecoversFromACrashAtEveryPoint` |
| Every source record the loop writes enters the view through `putOut`, which queues the feedback it holds | `feedback.go` (`putOut`) | `TestEverySourceRecordTheLoopWritesEntersTheViewThroughPutOut` |
| A clearing of the records that failed after a notice is tried again, so a notice swept later cannot cause a second one | `feedback.go` (`clearCovered`, `retryClears`) | `TestANoticeIsNotToldTwiceWhenTheRecordsCouldNotBeCleared` |
| A target message nobody reads is rejected when the queue time runs out and the sender is told once; a record that is not final closes after `report_by`, a source record with the notice of how it closed | `ticks.go`, `view.go` (`onTick`) | `TestBacklogExpiryRejectsAndIsReportedOnce`, `TestARecordThatIsNotFinalIsClosedByTheEndOfItsTime`, `TestNoRecordAfterReportByChangesNothing` |
| One pull of the target before a queued or accepted source delivery closes; its answer is told with its true meaning; a failed call, no record, or a target that no longer keeps its records closes it with one notice; a delivery already told closes silently | `outbound.go` (`finalPullDue`, `finalPullPending`, `finalPullMade`), `ticks.go` (`tickedSource`, `holdForFinalPulls`) | `TestASourceAsleepPastReportByIsToldTheRejectionTheTargetSigned`, `TestASourceAsleepPastReportByTellsOnceWhenTheTargetCannotBeAsked`, `TestASourceAsleepPastTheTargetsRetentionAsksNoOne`, `TestADeliveryLearnedByThePullBeforeTheCloseIsSilent`, `TestADeliveryTheSenderWasToldExpiredClosesWithoutAWord`, `TestNoRecordInThePullBeforeTheCloseIsNotALoss`, `TestADeliveryRemovedFromTheOutboxByAConflictIsClosedWithANoticeWhenTheTargetCannotBeAsked`, `TestAConflictedDeliveryIsPulledOnceAndClosedWithANoticeWhileTheNodeRuns`, `TestADeliveryWhoseLastPullNeverComesClosesWhenTheTargetsRetentionIsOver`, `TestNoPullBeforeTheCloseIsMadeWhileTheClockIsInDoubt`, `TestOutboundNeedByState` |
| A delivery with no receipt expires only after a query that was answered found none, is told once and its correction once | `outbound.go` (`applyAnswer`) | `TestExpiredUnconfirmedCorrectedOnce`, `TestSourceWithoutAReceiptIsNotToldTheTargetLostTheRecord` |
| Contact that keeps failing is told once; twenty messages are one notice; the delivery ends closed after `report_by`, told once more as expired | `outbound.go` (`outResult`, `noteContactFailing`) | `TestTwentyToUnreachableTargetMergeIntoOneNotice`, `TestAnUnreachableTargetIsToldOnceAndTheDeliveryClosesAfterReportBy` |
| Nothing is ticked or judged while the clock is in doubt | `ticks.go` (`tickBatch`), `outbound.go` (`observe`) | `TestClockRollbackPausesSigningUntilCleared`, `TestNoRecordIsNotAnsweredWhileTheNodeMayNotSign` |
| The records of time survive a crash at every step | `tick` (declared by the store) | `TestTheRecordsOfTimeRecoverFromACrashAtEveryPoint` |
| A damaged file is settled by the scan entry first of the rules; a source record becomes the sender's lost record, a file lost unread a notice of its loss, a target record is removed; each tells the sender once and raises one alert | `quarantine.go` (`quarantineAll`, `settleFile`, `lostOut`), `recovery.go` | `TestCorruptDeliveryRecordIsReportedToTheSenderOnce`, `TestOpenQuarantinesADamagedFileAndOpens`, `TestQuarantineRecoversFromACrashAtEveryPoint` |
| A damaged control record stops the start, and everything damaged beside it waits | `recovery.go` (`unwired`, `damagedControl`) | `TestOpenRefusesToOpenOverWorkOfARuleThatIsNotWiredYet` |
| A body that is not what its record says settles that record by what it needs | `quarantine.go` (`readBody`, `quarantineBody`) | `TestBodyWithWrongHashIsQuarantinedAndRecordsTreatedAsLost` |
| A damaged floor file met by a write is settled and the write goes on; a damaged file of another directory is not settled while the node runs | `quarantine.go` (`settleLive`, `retryingDamaged`, `syncAfterSettlement`) | `TestADamagedFileOtherThanAFloorFileIsNotSettledWhileTheNodeRuns`, `TestADamagedFloorFileMetWhileTheNodeRunsIsSettledAndTheWriteGoesOn` |
| The view takes what the store returned and agrees with the disk | `drain.go`, `view.go` | `TestViewMatchesAFreshScanAfterWrites` |
| No method makes a session endpoint | — | `TestEngineHasNoMethodThatMakesASessionEndpoint` |
| Node rules do not apply to a local sender, declarations do | `inbound.go` (`judgeAuthz`) | `TestLocalSourcesIgnoreNodeRules`, `TestEndpointDeclarationRestrictsLocalSenders`, `TestARemoteSenderIsJudgedByTheNodeRulesAndALocalOneIsNot` |
| A reply with evidence is admitted before the report was pulled, a forged reply-to gets nothing | `inbound.go` (`admitReply`) | `TestReplyWithEvidenceAdmittedBeforeReportPulled`, `TestForgedReplyToGetsNoPermission`, `TestEvidenceThatDoesNotVerifyIsDiscardedAndTheOwnerIsToldOfIt` |
| A lost receipt and a resend leave one record; another body under one identifier is a conflict | `inbound.go` (`takeIn`), `outbound.go` | `TestLostReceiptRetryKeepsOneRecord`, `TestSameIDDifferentBodyConflicts` |
| The backlog of an endpoint, the queue and the disk of the node | `inbound.go`, `send.go`, `capacity.go` | `TestEndpointBacklogFullIsRejectedWithReason`, `TestNodeQueueFullBackpressuresNewSends`, `TestDiskFullGivesNoFalseAcknowledgement`, `TestDiskFullAtTheTargetGivesNoReceipt` |
| Requests no node takes, sequence numbers, the clock pause, idempotency retention | `send.go` | `TestASendThatNoNodeWouldTakeIsRefusedAndLeavesNothing`, `TestASendWithAnEmptyBodyIsRefused`, `TestSequenceNumbersCountEachPairOfSenderAndRecipientFromOne`, `TestNothingIsSignedOrAcceptedWhileTheWallClockCannotBeTrusted`, `TestAnIdempotencyKeyIsRememberedUntilItsRetentionEnds` |
| The state of each delivery of a message is read from the sender's record, for its sender alone | `status.go` | `TestTheStatusOfAMessageIsTheStateOfEachDeliveryAsTheSenderHoldsIt`, `TestOnlyTheSenderOfAMessageIsToldItsStatus`, `TestTheStatusOfAMessageSurvivesARestart`, `TestEveryStateAndRejectReasonHasAWireName`, `TestTheRecipientsOfASourceRecordAreTheOnesOfItsKeyInOrder` |
| A key used again for other recipients, or in another order, is a conflict | `send.go` (`repeat`) | `TestTheSameIdempotencyKeyForOtherRecipientsIsAConflict` |
| What no node takes is what the envelope refuses, by the field it names; deadlines are whole seconds | `send.go` (`envelopeOf`) | `TestARequestTheEnvelopeRefusesIsRefusedForTheFieldItNames`, `TestADeadlineOfPartOfASecondIsKeptToTheSecondAnEnvelopeCanSay` |
| An answer is made only to a message the endpoint took in | `send.go` (`heldBy`) | `TestNoAnswerIsMadeToAMessageTheEndpointRefusedAtIntake` |
| A closed endpoint rejects as closed, a missing or runtime one as missing | `inbound.go` (`messageRefusal`) | `TestAMessageToAClosedEndpointIsRejectedAsClosed`, `TestAMessageToAnEndpointThatIsNotThereIsRejectedAsMissing`, `TestTheRefusalOfAMessageForAnEndpointItCannotBeGiven` |
| What is sent and asked, and when | `outbound.go` | `TestOutboundNeedByState`, `TestAnUnreachableNodeIsSentToUntilTheDeadlineAndAskedAfterIt`, `TestHeldReportsAreAskedForUntilTheyCome`, `TestARestartGoesOnWithAMessageThatWasNotSettled`, `TestANodeThatHasNotAnsweredIsSentToAfterAnotherNodeDid`, `TestTheReportsAreAskedForUntilTheReportDeadlineAndNoLonger` |
| A pull names the recipients that are not settled; a target answers each with a report or `NoRecord`, refuses the whole answer while it may not sign, and a held answer is never `NoRecord` | `inbound.go` (`answerPull`), `outbound.go` (`outboundPlan`, `applyAnswer`) | `TestPullAnswersNoRecordForARequestedRecipientTheTargetDoesNotHold`, `TestHeldReportsAreNeverNoRecord`, `TestNoRecordIsNotAnsweredWhileTheNodeMayNotSign`, `TestAPullNamesTheDeliveriesOfTheNodeThatAreNotSettled` |
| A source with a receipt learns of a lost record once and does not send again; an answer after the report deadline changes nothing | `outbound.go` (`applyAnswer`) | `TestSourceWithAReceiptGivesLostAtTargetOnce`, `TestSourceWithoutAReceiptIsNotToldTheTargetLostTheRecord`, `TestNoRecordAfterReportByChangesNothing`, `TestAnAnswerThatReportsAndListsARecipientIsNotReadAsALoss` |
| No evidence is signed while the wall clock cannot be trusted | `signing.go` (`canSign`) | `TestNoEvidenceIsSignedWhileTheWallClockCannotBeTrusted` |
| An engine does not open with the keys of another node | `engine.go` (`newEngine`) | `TestAnEngineWithTheKeysOfAnotherNodeDoesNotStart` |
| The body is held by what needs it, and every record has one of its own | `inbound.go` (`settledSource`, `writeArrivals`), `outbound.go`, `drain.go` (`settledIn`); the engine only writes the record without its reference, and the store removes that record's body | `TestTheBodyIsHeldOnlyByWhatStillNeedsIt`, `TestEachRecordOfAMessageHasABodyFileOfItsOwnThatGoesWithItsFinalState`, `TestEvidenceThatFinishesASourceDeliveryRemovesTheBodyOfTheSourceRecord`, `TestCloseBatchClosesRecordsAndDropsTheBodyReferenceOfEachRecordItClosesAndOfNoOther` |
| Every write of taking a message in survives a crash, and every declared step is crashed at | `intake`, `deliver`, `deliver-reply`, `source-update` (declared by the store) | `TestSendingAndIntakeRecoverFromACrashAtEveryPoint`: a message, a message the target refuses (it holds no body), an envelope and an answer that no source record of this node sent (the target writes the body), an answer with evidence, and an answer whose evidence ends the question's last delivery (the transaction removes the question's own body); a message or an answer that has a source ends handled once, and the test fails when a declared step is reached by no scenario |
| The receiving entry under arbitrary bytes | `inbound.go` | `FuzzInbound` |

Known gaps, each filled in with the feature it belongs to:

- The quarantine of a damaged control record is not wired, and the scan entry refuses
  to open while one is there. The store decides which session endpoints a quarantined
  control record closes (every creating one that names it,
  `store.QuarantineSessions`), so the engine passes the scan and no list;
  `TestQuarantineClosesEveryCreatingSessionEndpointOfTheDamagedControlRecord`
  is skipped until the pull of control signals answers from `ctl/`, and the quarantine
  of `ctl/` is wired with it.
- Phase 11 of `RecoverDelivery` is empty until the feature that owns it exists. A prompt
  and a block have no replay: the store writes their target records first and the
  head last, and phase 11 and the prompt logic redo what the head does not say is
  done (a prompt still in flight in the head, a block the records hold and the head
  does not report); phase 12 ticks the target and source records, phase 13
  sends the feedback that fell due and phase 14 goes on with the outgoing messages. The
  tick of a target record does not yet wait for an active turn before it lets an expired
  lease go: there are no leases until messages are handed out.
- An empty body is refused: the Agent interface says a body may be empty and an envelope
  of a message must have one (`TestShouldASendWithAnEmptyBodyBeAccepted`, skipped).
- Closing a session endpoint does not yet send `session.close` to the adapter: the
  outcome says `closing`, and the release of the native session comes with the
  creation of sessions, which is the feature that makes one.
- `adapter.start` lists no session: the list is filled with the open sessions of
  the instance when sessions can be created.
- A write that fails on `endpoint.register` is refused with the engine's own
  reason (`storage_full`, `storage_unavailable`, `storage_fatal`). Which code of the
  adapter protocol each maps to is for the adapter host to decide; if no existing
  code fits, it stops and says so, and the protocol is changed for it, not worked
  around in the host. The cap of the node maps to the protocol's `endpoint_limit`.
- A stored administrative result is dropped from the engine's table only by a
  restart. The sweep that deletes results after their retention will have to take
  them out of the view too, in the same step.
