# Engine

The single writer of the node's runtime state: the loop, its schedule, the
registration of endpoints, the life of an adapter's connection, the capacity check,
the two recovery entries, the sending and taking in of messages, and control signals
with the creation of sessions they carry, the initial message of a creation, the
closing and interrupting of a session, and the waking of sessions by prompts. What the
features still to come add to the same loop is described with them.

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
While the phase is `recovering` the schedule holds back the entries of the three kinds
the recovery steps own: the ticks of records, the flush of feedback and the look at a source
record. They stay in the heap, due or not; the timer is armed for the earliest entry that
is not held, and the entries count as due to `Quiesce` only once they can fire. Step 12
takes the due ticks itself, and steps 13 and 14 do the work of the other two; `Serve` ends the
hold and wakes the loop, so what fell due after its step and before the switch goes at once.
Periodic tasks, the deadlines and retries of session creations, drains and the waits of recv
fire as ever. The reason is the order of the start: a record that fell due while the node was
down must not be ticked, nor its feedback formed, before the adapters have said which turns
run (step 10) and the creations were settled (step 11), because the release of a lease on a
record whose turn still runs is decided by that.

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
checks that the manifest of the **copy** is, byte for byte, the one it read first, and
that it lists this machine's platform (`adapterreg.RunsOn`); a source whose manifest
changes while it is copied is refused as an invalid change (`config_invalid`, with the
manifest as the field), so the check and the install cannot disagree and nothing is left on the disk. A directory
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
What a token's text is (32 bytes in unpadded base64url, in exactly one spelling) is
`adapterproto.Token.Validate`'s rule alone, and both doors that take a token as text, this
one and the lookup of a session endpoint by `ByToken`, ask it first: Go's base64 decoder
skips a newline and takes spare bits in the last character, so a decoder of their own
would take a text that is not the token for it.
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
key and the open sessions of the instance, each with the native session it maps to,
the tool access its agent was created with (its token and the data directory, in
`SIDERA_TOKEN` and `SIDERA_HOME`) and the state the adapter kept for it.

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
back may put its state at once. The scope `session` needs a session endpoint of the
instance that is open or whose creation has not ended: the adapter keeps the state of
a session while it creates it, before the node has been told the session exists, and a
creation whose adapter was refused that put would fail for it.
The scope `attempt` needs an attempt of such a session: its creation attempt, the first
prompt that follows from it, or the prompt in flight of an open session of the instance (the
state a reconciliation of that prompt is given). The creation reads the state back by attempt,
so an adapter that could put one under an attempt of another instance would write what that
instance's creation believes it saved itself. An attempt that no session of the instance has is
refused as `endpoint_unknown`, as a session of another instance is; so is a prompt attempt that
has settled, whose checkpoint is already dropped and which a late put would leave behind.

`Quiesce` judges, in the same loop command as its other conditions, each instance
named in `Expect.Adapters` against the host's counts: the notifications handled
equal what the adapter says it sent, none is buffered, no request of the adapter is
being answered, the host is not working, and the requests the host has sent that
have no answer are exactly the ones the caller says the adapter holds on purpose
(as multisets). Instances not named are not judged. A worker of the engine that is
inside a call to an adapter is work until the adapter answers, except for the calls the
caller says the adapter holds on purpose: those are the requests the host's own check
has just matched with the held list, so the workers that wait for them are not counted.

**Pulling.** `Recv` and `Ack` are commands of the loop; the caller is an open
session endpoint by its token or an open direct endpoint by its adapter. One recv, in
this order: ends the wait the endpoint already has (a newer recv wins, whether it waits
or not); draws one lease for the page from `Deps.Rand`, also for an empty page, because
a page always carries one; acknowledges the page named by `AckLease` first, and when
that fails only says so in the log, because the page that was not acknowledged comes
back marked as a possible duplicate; ticks the endpoint's own records that are due, so
a message past a deadline is settled and not handed out behind the agent's back (a fatal
storage error there ends the recv, and no page is built or written after one; a record the
tick could not settle, because the clock is in doubt or the write waits to be tried again,
stays due, and the delivery rules refuse to hand out a message the time has not judged, so
the recv leaves it out of the page and hands out the rest; a part of it asked for by number
is the empty page, as for a suspended message, because the message is still queued); judges
every queued message again with the declarations as they are now (the second check);
builds the page; and writes what the page changed in one `lease` transaction before it
answers. The queue is the target records of the endpoint in the order they were taken in
(`view.queuedIn`), those under a live lease first, and never a control record, though the
view's queue of an endpoint holds those too: a control signal is not for an agent.

The second check is the authorization rules asked at the moment the body would be handed
out (`evaluate`, the one place the engine asks them, for intake as for the hand-out): a
local sender is judged by the declarations of the endpoint alone and any other by the
node's rules, which a node elsewhere does not satisfy yet. A message the rules still
allow is handed out; a suspended one stays queued, with no lease and nobody told; a
refused one is closed by `delivery.Record.Handout` (rejected as `second_check_failed`
when its body was never handed out, `delivery_unknown` when it was) and its body goes with
it. Only what the page reaches is judged: a refused message behind the last one the page
holds is left, because the declaration may be loosened before it would be handed out and
a message closed meanwhile would have been lost for a rule never applied to it; the next
recv reaches it. A notice of the system endpoint is exempt traffic and has no check. The
refusal of several recipients is made at intake only, as the pause is (`authz`'s second
check ignores it): whether a message had several recipients is a fact of its entry that is
not kept, so tightening the declaration later does not refuse messages already taken in. A
delivery derived from a control signal is not handed out until the request it was admitted
with is kept, and is logged as an error.

The page is built within the budget of the front end the request names, which is the
adapter's declaration (`Admission.Budget`; a direct endpoint's is that of the front end
`direct`, and the default when none is declared), and the wait is cut to what the adapter
declares for it (`Admission.RecvWait`; a direct endpoint's to `MaxWaitSeconds`), so the
cut is made in one place. Bodies are read through `readBody`, the one reader that settles a
body that is not what its record says or is gone: such a record is settled and the page is
built without it, while the other messages go on. Bodies are read as far as the page
reaches, not for the whole queue. When the settlement of such a body fails for good,
the error of the recv is that failure alone, because the error that says a body is damaged
is read as "settled" and the record may not be. A body that is not text is not handed out and is logged:
a page cannot carry it, and the message ends as a message nobody reads. A long message is
cut for every front end the endpoint can be read through, whichever one asks
(`Admission.PartBudgets`, passed to `handoff.Pack` and `handoff.Part` beside the budget of
the asking front end, which alone bounds the page): the parts are counted and acknowledged
one by one, so the command line and MCP must cut a message in the same parts, or a part
shown through one would be no part of the other. A message that was handed out in some
number of parts is still not handed out in another number, which happens only when an
adapter's upgrade changed its budgets: it is logged, left out, and ends as the message
nobody reads does.

`Ack` takes the lease of the page and the parts, or no parts for all the page. A lease no
record of the caller's endpoint holds is `lease_unknown`, whoever holds it: a record of
another endpoint with the same lease does not make it this endpoint's. Each part is
acknowledged by `delivery.Record.Ack` on a working copy, and the records that changed go in
one `ack` transaction; an acknowledgement is valid after its lease ran out and even after the
delivery was declared unknown, which it then corrects. A part of a message the endpoint
holds no record of is `refused_no_handout`. What a whole page held is remembered in memory for
the longest a lease lives (`pageMemory`); after that or after a restart only a message that
fits whole is known to have handed out its one part, and the parts of a message that was cut
are named by the agent, because acknowledging one that was not shown would deliver a body
nobody saw.

**Waiting.** A recv that finds nothing queued and is asked to wait is registered as
a waiter in the same command, so no arrival is missed between the check and the
registration. The waiter holds the lease and the budget of its recv, and the empty page it
is answered with when nothing wakes it, made when it was registered. A command that makes
a message of the endpoint ready (a target record taken in that waits with no lease on it)
marks the endpoint, and the loop hands the page to the waiter when the command is over
(`serveReady`), as a step of the loop of its own (one reading of the clock, observed like a
command's, so that a worker may be started from it), so that an arrival is never handed out in the
middle of the write that took it in. The deadline is a schedule entry on the injected
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

**Asking about a message.** `MessageStatus` answers from the sender's record of the message, which the reports of the targets bring up to date: one entry for each recipient in the order the sender gave them, with the state and, for a rejection, the reason, named as the delivery rules name them. A message the caller's endpoint did not send is `message_unknown`, whoever sent it, so no endpoint learns what became of another's. For a control signal the status carries its control reply once the node has taken one in, in the values of the adapter protocol; it is read from the record the node verified when it took the reply in, and verified again, so that an agent is never told what nobody checked.

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
all its recipients, so no pull is made for it). The worker sends and asks about the record
of a control signal like any other, and a derived delivery, which only the record of a
control signal has, is planned like the others. The receipt of a send and the answer to a
pull may carry the control reply of a signal: it is verified and applied first, by the
envelope package's one rule, because it ends the signal's delivery and binds what the signal
made, which a report that comes with it may be about; it is written with the record in the
transaction `control-reply`, and a reply that does not verify or that contradicts the one the
record accepted is logged and left out. The answer to it is read as three different things: an error is no answer and
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
that was put back asks for it and the close follows. A delivery of a record the worker does not carry (a lost record, an engine
without a transport) closes at once, so nothing waits for a pull that cannot come;
`outboundWorks` says which, and a control signal is carried like a message, so the pull
before its close brings its control reply too. A delivery removed from the outbox because the target
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
out. A control record is ticked like a target record: one that is accepted and has no reply
at the settle deadline becomes `delivery_unknown`, since the signal was handed to be executed
and no reply was committed, and one not final after `report_by` is closed. The engine settles
a creation long before either (see the creation of a session), so what these rules close is a
signal whose reply a crash lost. A target record under a lease is
ticked by the same rule as any other: a lease that runs out puts its message back in the
queue as a possible duplicate, up to the third redelivery, and the delivery whose fourth
lease runs out is `delivery_unknown`, told to the sender once. Whether a lease that ran out
may be released is asked of `leaseMayRelease`, which says yes for every lease today: nothing
tells the engine that the agent has a turn running, and the signals of turns that will keep a
lease alive decide it there, in the one place a release is made. While the wall clock is in doubt
nothing is ticked and nothing is judged expired: a clock set back and then moved on is
not one to judge a deadline by until the owner has cleared it, and what the node took in
before the pause stays as it was.

**The archive sweep.** What has served its retention leaves the live files in a pass
that a periodic task (`AddArchiveSweep`, registered by the composition root with the
period) begins: it scans `state/` in the loop, as the settlement of damage does, plans
with `store.PlanSweep` and queues the pass as commands that queue each other. The scan
stalls the loop once, as long as a cold start's scan of the same files takes (the storage
reference measures about 3.2 s for 50,000 files and 11 to 13 s for 200,000, warm), and a
caller of the loop may be told `loop_busy` meanwhile, which it retries; nothing is lost.
The same scan names the archive files past the retention the owner set
(`store.ExpiredArchiveFiles`, the retention read only through
`config.NodeSettings.ArchiveRetention`). The first commands remove those files
(`ArchiveExpire`) and append the summaries of what is to go (`ArchiveSummaries`), in slices
of `ChunkRemoves`; then each removes a chunk of at most `ChunkRemoves` files, cut from the groups of the
plan (`SweepPlan.Chunk`) in the order the store removes them, so a control record still
goes before its derived delivery and an endpoint after what hangs on it, and between two
chunks the loop takes what the inbox holds. The summaries are best effort: a failure to
append them is logged, the rest of the pass's summaries are given up, and the removals
go on, because a sweep that waited for them could not free the space of a full disk. A
chunk that was removed takes each record out of the view by the record it was (the entry
of the schedule that stood for it is cleared by the view, as for any record it drops),
an out-lost record out of the queue of feedback, and an endpoint out of the endpoints (a
session endpoint's pairs also out of the sequence book, since its identifier is never used
again; a direct endpoint keeps its floors); a source record also leaves the outbox,
with the entries of the pulls made before the close, which a close by a report does not
clear; a stored administrative result and an acknowledged alert leave their tables. The
plan comes from a scan made when the pass began, so before each chunk the endpoints it has
yet to remove are looked at: a direct or runtime endpoint keeps its identifier for ever, and
one that has been registered or admitted again since has to keep its record and the
files that hang on it, which the plan names in chunks of their own. A pass that finds one
open again ends, and the next one plans from a new scan. A pass that is
still running is not begun again, a removal that failed ends it with a line in the log and
the next period plans anew, and nothing is swept while the wall clock is in doubt, since
retention is judged by it (`TestNoSweepWhileTheClockIsInDoubt`: several periods pass and
what is due stays, and the first period after the owner clears the pause removes it). What a record that feedback depends on keeps is the store's rule
(`PlanSweep`): a notice stays while a record still holds an item it covers, and a source record
that still holds an item stays while its sender is open.

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
no record. The alert ids are the store's. A damaged file of `ctl/` is settled like the
others, and it can be because the pull answers from `ctl/`: the record that goes is
answered as one the node does not hold, so its source is told it was lost, and the session
endpoints still being created by it are closed in the same settlement, since the intent that
would have settled them went with the record. When one was being created a native session
may exist that nobody will settle, so the alert is of kind `control_unknown`, the
alert of a session nobody controls, and not of the plain damage.

A body that is not what its record says, or whose file is gone, is found when something
reads it (`readBody`, which the sending of a message and the hand-out of a message use;
the alert says which of the two it was). Every body belongs to one record, so that one record is settled by what it still
needs: a target record that needs it is removed and leaves the view, a source record
that needs it is replaced by the lost record, a record that does not need it keeps its
state without the reference; the settlement has an alert of its own. A notice of the system
endpoint that is settled this way owes its sender what it told: each item it covered goes
back on the source record it came from (`reissueCovered`), written before the notice is
removed so that a crash in between finds the notice damaged again and writes the same items
again, and the queue tells them with a new notice. What the item says of the delivery is the
record's now (the kind is the cover's). A cover whose source record is gone, and the cover of
an out-lost record, whose item is built from the name of a file that cannot be read again, are
left out with a word in the log; the owner holds the alert of each loss. A control record
that still holds a creation which is not settled needed the body (the initial message goes
to the session the creation makes), and the store keeps a control record and only lets go
of the reference, so the engine fails the creation: the reply says `failed` with
`control_unknown` (no session will be attached, but a call already out may still make one that
nobody controls, so the owner gets that alert), the endpoint it was making is closed, and
nothing is made of a creation whose first message is gone. A write that
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

**Taking in an envelope.** `Inbound` is the handler of the transport. An envelope that carries
a control signal is taken in as described under control signals below. For each recipient
on this node it finds the record of the request or judges it: a repeat gets the record's
report, another body under the same message identifier, or the identifier of a control
signal the node holds for that sender and recipient, is refused as a conflict and
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
record it lost, so absence is the only way a loss shows. The records the target holds for a
message are found by the node and the message the asker sent, whether they are in `in/` or in
`ctl/`: the record of a control signal answers with its report and, when it holds one, its
control reply, and the delivery of an initial message to the session a creation made is an
`in/` record of the same message, so one pull asks about both. A pull that looked in `in/`
alone would answer "no record" for a signal the node accepted, which its sender reads as a
loss.
A report is dated with the moment the record entered the state it reports
(`Record.StateAt`, so a rejection made later by an endpoint's closing carries the time of the
closing, not of the acceptance) and, for a delivered record, with that moment as the delivery
time (`TestAReportIsDatedWithTheMomentTheTargetRecordedTheStateItReports`); every state has a
report, and a pull for an unchanged state repeats the same time.

**Control signals.** `Control` accepts a control signal from the endpoint its caller is, as
`Send` accepts a message: the same capacity check (a signal is new work), the same envelope
checks, one recipient, and a source record of the kind `ctl` that the worker sends like a
message. Four signals are offered: `session.create`, which names a preset and may carry an
initial message (an empty body is none), `session.close`, `session.interrupt` and
`endpoint.status`. A message on any other signal, a content type without a message, a
creation without a preset and a signal with a preset that is not a creation are refused for
the field (the rule between a signal and its parameters is `adapterproto.Control.Validate`'s).

The target takes a control signal in by one order. A repeat is answered from the record the
node holds, with its report and its control reply when it has one, and another body under the
identifier is a conflict that changes nothing. The endpoint must exist and be open, the
signal allowed (the declarations of the endpoint, and for a source elsewhere the rules of the
node) and in time. A signal that fails there is refused, and the refusal is answered at once:
the target writes its control record `rejected` with the reason (`delivery.NewRejectedTargetControlRecord`,
in `control-intake`, holding nothing of an execution) and the receipt carries its report and no
control reply, so the sender's delivery is `rejected` with the reason and the sender is told
once, in the notice of a rejection (see below). The reasons are those of the intake: an
endpoint that is not there (`endpoint_missing`), that was closed (`endpoint_closed`), a
request the policy cannot make (`malformed`: a creation aimed at an endpoint that stands for no
runtime), a source elsewhere that the rules of the node refuse (`unauthorized`), a deadline
out of range. A repeat is answered from that record. Then the signal is accepted, and what the node
answers itself is answered in the same transaction, `control-intake`: `endpoint.status` from
what the engine has observed (the activity of the endpoint, the messages waiting, the
conditions `adapterreg.StatusConditions` gives, and for a runtime endpoint which presets can be
used), and a creation it refuses before it calls the adapter, with the reply
`adapterreg.DaemonCreateFailure` gives and no session endpoint and no intent: the preset is not
defined (`preset_unknown`), the preset says it is closed to endpoints on this node and the
signal came from one (`preset_unavailable`, by `authz.LocalCreate`, after the record is accepted
so that the answer is a control reply), or the adapter is not connected (`adapter_offline`).

**The creation of a session.** A creation the node does not refuse makes, in `control-intake`,
the control record with its creation intent and the session endpoint with its token, being
created and naming the record that makes it; both are on disk before the adapter is called.
The adapter is called by a worker the engine owns (`session.create`: the attempt of the intent,
the runtime endpoint, the new endpoint, the preset, the first prompt, the tool access and the
checkpoint of the attempt), and its result is a command of the loop that settles the intent in
one transaction, `control-result`: the control reply is made and stored with the deduplication
record, the delivery of the signal ends as delivered, and the endpoint opens, mapped to the native
session, when the session was created and is closed with its token dropped when it was not. The
first prompt's attempt follows from the creation attempt, so a call made again carries what the
first carried, as the adapter requires, and nothing else is stored for it. Once the intent is settled,
whatever the outcome (a creation failed because its endpoint was lost included), the checkpoints the adapter kept under the creation attempt and under the first
prompt's attempt are dropped: no call will be made again. A call that failed, an
answer of `indeterminate` and an adapter that is not connected are tried again, after
`CreateRetryBackoff` and at the next connection of the instance, until the reply deadline,
`CreateReplyDeadline` (3 minutes) from the moment the signal was accepted. Then the intent is
settled as unconfirmed: the reply says a session may exist and names the endpoint, the endpoint is
closed, and the owner gets an alert of kind `control_unknown`, because a native session may exist
that nobody controls. A creation the adapter reports as failed for `control_unknown` has the same
alert. Whatever the adapter answers after a creation was settled changes nothing, except that a
session reported as created for an endpoint the owner closed meanwhile is recorded on the closed
endpoint and released (see releasing the native session). A control record that was
closed while its creation was unsettled can take no reply, and a retry would fail every time: the engine
logs it as an error that names the record, tells the owner of a session that may exist, and stops the work
for the creation. The alert the engine raises of a session nobody controls has an identifier that follows from the kind
and the name of the control record, and one that is there is not written again, so a creation that is
met unsettled at every start (the closed control record) is one alert for the owner, and an
acknowledgement of it stands. At the start, step
11 settles the creations past their deadline as unconfirmed and calls the adapter again for the
others, with the same attempt; the adapter answers an attempt it knows with the session it made, so a
crash or a killed adapter after the native session was made still yields one session. A turn of the new
session may be reported before the result of its creation (the session works on its first prompt as
soon as it exists): the notification is kept by reference and writes nothing, and the endpoint is
open when the result is in.

**The initial message of a creation.** A creation that goes on to the adapter also writes, in
`control-intake`, the delivery of its message to the session endpoint it allocated: an `in/`
record from the sender, under the message of the signal, with the runtime endpoint it was
for and the shape of the request that let it in (`signal` and `preset`). The body is that
record's own file, and the control record keeps only the content type and the digest and
length of the body, so a crash between the two writes leaves everything the rule
`derived-replay` needs to write the delivery (the engine makes it again from the control
record alone, `derivedDelivery`, the one place it is made). It is a message like any other
from then on: it waits in the session's queue, recv hands it out and the acknowledgement
settles it, and the sender's pulls name it. It is judged as the creation was judged and not
as a message (the message size rules do not apply to it, only the envelope's limit): it is
let in because the creation was, and when its body is handed out the creation is judged
again from the request the delivery remembers, by the declarations of the runtime endpoint
as they are then, and refused when that endpoint is no longer the node's. The sender's
node learns the session from the control reply, by the pull or as the evidence of the
session's first reply, which carries the signed reply of the creation and then the signed
report of the delivery, and the one rule of the control reply (`envelope.HandleControlReply`)
gives it a delivery of its own for the message, to the session endpoint, whichever of the two
came first. Either way the reply is applied by one function (`withControlReply`): the signed text
is stored for the status of the signal, and the feedback the end of the signal calls for is told,
so a pull that comes after the evidence finds the digest it carries and adds nothing. If the session is not made (the creation failed, or was not confirmed and
the endpoint was closed), the delivery is closed with the endpoint, as any record queued for
a closed endpoint is (`rejected`, `endpoint_closed`), and the sender, who tracks the
delivery when the reply names an endpoint at all, hears of it as of any rejected message; a
creation the node refuses itself holds no delivery, and its reply says that the creation
failed. When the message is lost while the creation is not settled (the body does not read,
or the delivery is gone), the creation is failed with `control_unknown` and the endpoint
closed, instead of making a session for a message that cannot be delivered, and the owner
is told that a native session may remain; a creation whose session endpoint file was
damaged is failed the same way, since no token would open its session. The start checks both
before it asks the adapter again.

**Closing and interrupting a session.** A signal for an endpoint that does not take it (a
direct endpoint, or a session of an adapter that declared no interrupt) is answered
`unsupported` when it is taken in, without the adapter; one for the runtime endpoint is
malformed by policy, and one for a session that is closed is refused when it is taken in,
like any signal for a closed endpoint. A signal the endpoint takes is accepted (`control-intake`:
the control record alone) and the adapter is called by a worker the engine owns; the control
record is not in the queue recv hands out of, so the signal is carried out at once, whatever
waits for the session and whether a turn runs. The result settles it in one transaction
(`control-result`): the reply is made and stored, the delivery of the signal ends and, for a
close, the session endpoint is closed with its token dropped, with the outcome the adapter gave
(`closed`, or `close_unconfirmed` with its reason) because the channel is closed whatever
the adapter did; the records queued for the endpoint are then closed by the drain. Until the
result is in, the endpoint stays open, and a close of it by the owner that comes first makes the
signal's record unknown, as for any accepted signal. A call that failed, and an adapter
that is not connected, are tried again after `RetryBackoff` and at the next connection until
`ControlReplyDeadline` from the acceptance, and then the sender is told what could not be
confirmed: a close as `close_unconfirmed` (`adapter_offline` or `native_unreachable`), with the
endpoint closed, and an interrupt as `failed`. An interrupt is told `failed` with
`adapter_offline` at once when the adapter is not connected, because a signal to stop a turn
that waits for the adapter to return stops it late and may be asked again. When the adapter
says it asked the runtime to stop the turn, the sender hears `stopped` if the turn ends within
`InterruptConfirmWait` (the notification of the end of the turn settles it) and `requested`
if it does not. A result that could not be written is kept and written again after
`RetryBackoff`, and the adapter is not asked again for it: it has done what it was asked, and an
interrupt asked a second time may reach the next turn or answer `not_running`, which hides that
the first one stopped the turn. Past the deadline the write is tried again at the same pace by
the deadline itself, so a signal is never left accepted until the next start. A connection that
closes while the call is out is told as an adapter that is not connected. A start goes on with the
signals that were not settled: those past their deadline are settled as the deadline does, and for
the others the adapter is asked again, so a signal reaches the adapter at least once. A close asked
again does what it did before, as a session that is released stays so, but an interrupt asked again
after the adapter restarted may reach a later turn or find none and answer `not_running`.

**Releasing the native session.** Closing a session endpoint ends the session in the channel,
and the native session may still be there. The endpoint record says whether the adapter has
confirmed that it is released (`released` in the session mapping; the store's
`EndpointRecord.ReleasePending` is the one rule of what is still owed). Every way to close a
session endpoint leaves the native session owed unless the answer is already known: the signal
`session.close`, the owner's close through the management interface, the disabling of the
instance, and a creation that ends as created for an endpoint that was closed meanwhile (the
endpoint stays closed, closed being absorbing, and the native session is recorded on it so that
it can be released). A signal the adapter answered `closed` records the release in the
transaction that closes the endpoint; every other way records nothing, and the engine
(`release.go`) asks the adapter in the command after the endpoint was recorded closed:
`session.close` with the endpoint and the native reference, so that an adapter that restarted
and holds no closed endpoint can still find the session. An answer of `closed` is recorded with
`endpoint-update`; an unconfirmed close and a call that got no answer are asked again after
`RetryBackoff`, and so is a call that found the adapter not connected, because the host tells the
node that an adapter started before it puts the connection in service, so a call made between the
two has no later connection to wait for. An adapter that is not online is not asked, and every
connection of its instance asks again once it is in service (after `adapter.start`, and after
`adapter.probe`), as for the signals and the creations it owes. The adapter answers `closed` for a native session
that is released or gone, so asking again after a crash between the answer and its record, which
is how a release is found at the start (step 11), does no harm. The owed state is the record
alone: nothing else is kept, and the archive sweep keeps a closed session endpoint while its
native session is owed. When it stays owed for `ControlReplyDeadline` from the moment the node
began on it (the close, or the start that found it again), the owner gets one alert of kind
`control_unknown`, the kind of a native session that nobody controls: its identifier follows
from the kind and the endpoint record, so the alert is one however many times the node starts,
an acknowledgement of it stands until the alert is swept (`AlertAckRetention`, 7 days after the
acknowledgement), and a later start that finds the release still owed raises it again, and the
adapter is asked on. The reply to a close signal does
not change with any of this: it is `closed` or `close_unconfirmed` as the adapter answered, and
the endpoint is closed either way. The node refuses `checkpoint.put` for a closed session
endpoint, so a creation reaches the end described above only when the owner's close comes after
the adapter's last checkpoint of the session.

The state an adapter kept for a session (its checkpoint) is removed when the endpoint is
closed, by whichever of the ways to close one: every place the engine records an endpoint
(`putEndpoint`) queues the removal of the checkpoint of a session endpoint that has just been
closed, and the scan entry removes one a crash left (`checkpoint-drop`).

**Prompting.** A session endpoint that is open, mapped to its native session and whose adapter
is online is woken by a prompt when messages wait for it. Whether to send one is the decision of
`handoff`, which the engine asks for every waiting endpoint together (`DecideNodePrompts`; a
`DecidePrompt` of one endpoint never authorizes a send, since the cap on the turns that prompts
started is the node's) at every moment its answer may have changed: a message that waits for the
endpoint (an arrival, or a lease that ran out), the end of any turn, an adapter whose probe was
answered (the host puts the connection in service after it has told the node that the adapter
started, so the start is not the moment a prompt can be sent), one that went offline, the owner's
change of the cap or the clearing of a wall clock that was set back, and the `RetryAt` the
decision names for the three holds nothing else brings it back from (the hourly cap, the minimum
interval, and the retry of a failed prompt). Each endpoint keeps, in memory, the event that waits to be judged (it stays until a
prompt is sent or nothing waits), when it last called recv, and whether a turn that a prompt
started may be running: from the dispatch of the prompt until that turn ends or the prompt is
known not to have started one. That count is what `DecideNodePrompts` is given as running; a turn of
an adapter that is offline is not in it, and an endpoint whose adapter is offline does not wait.
What waits is the messages a recv would hand something out of, less those whose check at the
hand-out is a suspension. The cap is the node's setting `node_prompt_turns`, the other values are
the decided ones. After every command of the loop the engine asks again if something changed (a step
of the loop like the serving of waiting recvs), and not before the node serves.

**Announcing.** A direct endpoint that is open is told with `endpoint.pending`, sent to the
adapter instance that serves it, that messages wait for it, at the moments a message becomes
ready to be handed out to it: taken in, or queued again when its lease ran out. The command that
makes a message ready marks the endpoint (`notePending`), and once the command is over, and the
waiting recv has been handed what it could take (`serveReady`), a step of the loop of its own
(`servePending`) sends one notification for each marked endpoint, however many messages became
ready in that command. The `count` is the number of messages that wait for the endpoint, counted
as the prompt rules count them (`pendingOf`: queued, to be handed out, and not suspended by the
check at the hand-out), and a message that the waiting recv took is not among them, so an
endpoint whose waiter took the only message is told nothing. The notification is best effort:
nothing is recorded, nothing is sent again at a later moment and nothing waits for the adapter,
because the adapter reads with `endpoint.recv`, which may wait, and a notice that was lost or
sent while the adapter was away costs it nothing but the time until its next recv. A mark made
while the node does not serve, the wall clock is in doubt or the identity is retired is dropped.
A session endpoint and a runtime endpoint are never sent this notification.

A prompt is carried out in this order. The attempt is written as in flight in the endpoint's
attempts record (`prompt-begin`) before the adapter is called, with its kind and the target
records it covers, so that a node that dies in between finds it. The call runs in a worker the
engine owns, and its result is a command of the loop that records how the attempt ended
(`prompt-end`): when the prompt was delivered the covered target records are marked notified first
and the attempts record last, and the checkpoint of the attempt is dropped after that. The attempts
the record keeps are cut by `handoff.TrimAttempts` each time one is added. A refusal,
and a call that found the adapter not connected, is a failure; the prompt rules then hold the
endpoint for a minute (`handoff.DefaultRetryAfterFailure`) and name the instant the retry is
due, which the engine schedules as it does the other `RetryAt`s, with the event the failed prompt
answered restored on the endpoint; an answer that does not say whether
the prompt arrived is asked of the adapter (`prompt.reconcile`, given the stored checkpoint of the
attempt). A `turn.started` that names the attempt in flight is the same proof as a verdict of found,
whatever the answer of the call was or whether one came: the attempt is settled as delivered at
once, the turn counts against the cap, and the answer that arrives later changes nothing. Without
it an unknown outcome followed by an idle agent would be sent a new first prompt at every turn end,
since no delivered record exists for the allowance of repeats to count. The verdict found is a
delivery, and counts the turn it started while it runs; provably
absent is a failure; anything else, a call that got no answer included, is an unknown outcome,
which counts toward the hourly cap and nothing else. The attempt is never sent again whatever the
verdict. Step 11 of the start does the same for every attempt found in flight; an adapter that is
not connected is asked when it is. A write that fails keeps the outcome and is tried again after
`RetryBackoff`.

**Blocked delivery.** Delivery to a session endpoint is judged blocked by `handoff.AssessBlocked`
(prompts failing for longer than 5 minutes because of a cause that holds: the adapter offline, the
runtime's authentication invalid or its service unavailable). The engine judges an endpoint at
every moment the input of the rule may have changed: a target record of the endpoint taken in or
dropped (an arrival, a lease, an acknowledgement, an expiry), an attempt settled, an availability
snapshot, the adapter's connection ending or its probe being answered, the start of the node and the
clearing of a wall clock that was set back; and at the instant the rule names for a run that is
still short (`BlockedVerdict.RetryAt`), since no event marks it. The adapter's signals are those of
`AdapterSignals`, except that, for an endpoint whose block is not reported, an adapter that has not
connected since the node began to serve is offline since then and not since an unknown time: the
node did not look before, and a message that waited while the node was down would otherwise be
reported blocked in the moment the node starts, for an adapter that is about to connect. For an
endpoint whose block is reported (the head says so, or a message that waits carries the mark, which
a crash between the writes of the marks and of the head leaves) the start of the cause is left unknown, which
measures from the oldest message that waits: counting from the start of the node would shorten the
failing run below the threshold, the judgment would take the block for over and correct it to the
sender, and the block that the adapter's absence then reports again would never be told, since a
delivery is told as blocked once. A reported block therefore goes on across a restart until a
connection shows the cause gone, or a prompt is delivered.

A verdict is recorded like a prompt: the target records first and the endpoint's attempts record,
the head, last, in one call (`store.Blocked`). A new block marks every message that waits with the
cause (`delivery.EventBlocked`, the cause mapped by `adapterreg.BlockedCause`) and stores the cause
and its start in the head; while the block lasts, a message that arrives is marked as well; when
the block is over (the cause is gone, a prompt was delivered, or nothing waits any more) the head is
cleared and every record that still holds a mark is lifted (`delivery.EventUnblocked`). The marks
are found by the records' own flag and not by the head, so a block that a crash left half written
(some records marked, the head not saying so) is lifted by the first judgment after the cause is
gone. A verdict that changes no record and not the head writes nothing, and a write that fails is
tried again after `RetryBackoff`. The sender is told by the usual way: it pulls the report of the
target, which carries the mark and its cause, its delivery rules decide that the news is owed once
per delivery (and the correction once, if the block was told), and the feedback queue merges the
blocks of one target endpoint into one notice and the corrections into another.

**The start, as far as the engine is concerned.** The steps of the start are numbered
in the order the node takes them; the engine takes part in these:

| Step | What | Engine |
| ---- | ---- | ------ |
| 6 | Scan `state/`, quarantine and repair, rebuild the view | `Open` (the scan entry) |
| 8 | Management interface opens; reads answer, changes are refused | `Start` |
| 10 | Adapter instances start and are admitted | `AdapterAdmitted`, `AdapterStarted`, `ProbeTokens`, `AdapterProbed` |
| 11 | Check the creation intents and prompt attempts that were not settled | phase `reconcile` of `RecoverDelivery`: creations past their deadline are settled, the others are asked of the adapter again (a creation whose endpoint or initial message is lost is failed instead), and the same for the closes and interrupts, and for the native sessions of closed endpoints that are still owed; every prompt attempt that was in flight is asked of its adapter (`prompt.reconcile`), and none is sent again |
| 12 | Run `Tick` on every record, release leases whose turn is not active | phase `tick`: the first time a record is ticked after the start, since the schedule holds the ticks back until then (a source delivery still queued or accepted past `report_by` is left open for the pull of step 14) |
| 13 | Send the feedback that fell due | phase `feedback` |
| 14 | Go on with the loopback of outgoing messages | phase `outbound`: every unsettled source record is sent or asked about again, and a delivery past `report_by` that waits for its last pull is pulled and closed |
| 15 | The Agent interface serves in full | `Serve` |

**Recovery.** `PlanRecovery` is a pure function of a scan: what each recovery rule
has to do. `Open` runs the rules that have work in the order the store fixes
(`quarantine` first, a damaged control record included, then `ctl-effect-replay`, `derived-replay`, `instance-drain-replay`,
`orphan-gc-endpoint`, `endpoint-drain-replay`, then the removal of the state of closed sessions and the replay of feedback, then `orphan-gc`;
the write of an initial message's delivery that a crash left comes after `ctl-effect-replay`), works the plan out again from a fresh scan after each rule
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
- **A direct endpoint is told once per command, and only of what still waits.** The
  notification says that there is something to read, not how many notifications were sent, so
  the engine marks the endpoint in the command that made a message ready and sends after
  the command: several messages made ready together are one notification with their number,
  and a message handed to a waiting recv in the same step is not announced. Repeating the
  notification on a timer for an agent that does not read was left out: it would be a rate
  to choose, with nothing lost without it, since the adapter is the reader and chooses its
  own waits.
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
  a moment too early. It polls the same way while the outbound worker runs and has
  not been seen to wait out a delay: the transport's count of waiting calls also
  moves without the engine being told, so a look that came before the worker's call
  began to wait would otherwise be the last one, and the test would wait for the
  limit of its real clock.
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
- **What fell due while the node was down waits for its step.** The loop runs from step 8,
  and an entry that is due fires as soon as the loop looks at the schedule; for the ticks of
  records, the flush of feedback and the look at a source record that would be before the
  adapters have reported turn state, which is what the release of a lease depends on. The
  hold is in the schedule, by the phase, so that no caller of it and no new kind of timed
  rule has to know about the start; a hold of those entries in the handlers would be one
  more special case in each. The test restarts a node whose record fell due, sees no `tick`
  commit before step 12 begins and one in it
  (`TestRecordsDueDuringDowntimeAreTickedByStepTwelveAndNotBefore`).
- **Time is read from the records, by the schedule the view keeps.** A timer for each
  rule of the delivery package would be a second statement of when a record changes;
  `Record.NextTick` says it once, and an entry that follows the view cannot be left stale
  by a write that forgot it. A record the rules do not change is not looked at again until
  it is written: an entry that fell due and found nothing would otherwise spin the loop.
  Nothing is judged by a clock the owner has not cleared.
- **The sweep is the engine's, in the loop, in chunks, and it never waits for the
  archive.** The engine is the single writer and holds the view that has to follow every
  removal, so a sweep run from outside would remove files under a view that still holds
  them. A plan is scanned in the loop because a plan made from the view would be a second
  statement of what is finished, which `store.PlanSweep` says once; the chunks are the
  bound of the stall after the scan, as for the drains; the scan itself stalls the loop
  once per pass, which is accepted at the sizes the storage reference measures (a
  view that kept the scan's inputs up to date would take it out of the loop). The summaries are appended in a
  transaction the sweep does not wait for, because the one time a sweep is urgent is when
  the disk is full and an append fails; the store has no log, so the engine logs what the
  store reports and sweeps on. The plan is older than its chunks and the view is not, so a
  pass ends when an endpoint it has yet to remove is open again: leaving out that record
  alone would still remove its attempts, which an earlier chunk holds.
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
- **An accepted control signal is never rejected afterwards, and a refused one is rejected
  from the start.** Closing a control record of a closed endpoint is what the delivery rules
  say: `delivery_unknown`, because the adapter may have executed it. A control signal the target
  cannot take in at all (the endpoint is not there, is closed or is no runtime, or the node's
  rules refuse the sender) was certainly not executed, so its record is born `rejected` and the
  sender hears the reason with the first answer. Writing nothing and giving no receipt
  would make the sender read a refusal as a send that got no answer: after about two minutes a notice
  that the target is unreachable with the cause `network`, although the target is the node that
  refused it, and after about 12.5 minutes `expired_unconfirmed`, which says the signal may have
  been received. The declarations of an endpoint do not refuse a control signal (they govern
  messages), so the owner can always reach an endpoint that takes nothing. A creation the node
  refuses for what it knows (preset, adapter) is the other case, and is answered with a control
  reply, since the signal was accepted.
- **A creation refused for an adapter that is not connected is answered at once.** Keeping the
  signal until the adapter returns would make the sender wait for a reply that the next restart
  may not bring; the reply `adapter_offline` is the protocol's, and the agent that wants a
  session asks again.
- **An unconfirmed creation closes its endpoint.** Closed is absorbing, and the store's
  recovery closes the endpoint of an intent that is neither creating nor created, so the
  unconfirmed reply names an endpoint that cannot be used; the alert is what the owner has for
  the native session that may remain. The alternative, keeping the endpoint to be opened by a
  late result, would let the endpoint of a reply that said "not confirmed" start taking messages.
- **The reply of a creation is not pushed to its source.** The source pulls until the reply
  comes, so the sender sees it within `PullInterval`; a faster path (the target asking its own
  source node to pull at once) would be a second way a reply reaches a source, and none is
  needed by a sender that polls the status of its message.

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
- **The attempt is on disk before the adapter hears of it, and an unknown one is asked about,
  never sent again.** A prompt that may have started a turn, sent a second time, may start a
  second one, and nothing the node holds can tell the two apart afterwards. So the in-flight
  record comes first, a restart finds it and asks the adapter (the attempt identifier is what
  the adapter finds in the native record), and whatever the adapter says, the next prompt is a
  new attempt chosen by the rules, never the same one again. The cost of the order is one write
  before every prompt, which is the price of being able to say what was sent.
- **One decision for the whole node, asked at every moment its answer may have changed.** The
  cap on the turns that prompts started belongs to the node, so no endpoint can be judged
  alone: a place that falls free is an event of no waiting endpoint, and an endpoint held back
  would wait for ever for one that nothing brings. Asking again after any command that changed
  what the decision reads, and not on a timer, keeps it out of every handler that changes the
  state (the cap, a turn, a connection, an arrival) and makes a place that fell free go to the
  endpoint that has waited longest in the same step. The decision is asked of every endpoint
  that has an event and no other, so an endpoint with an unread prompt is not woken by the
  events of others.
- **A block is a fact on the records of the messages, and the head says it is reported.** The
  sender learns what happened to a delivery from the report of its target, so the mark belongs on
  the target record, where the report reads it, and the endpoint's head holds only what edge
  triggering needs (the cause and when it began, so that a block is reported once and its recovery
  once, and a restart does not report it again). Judging by the endpoint at every change of the
  records is cheap and leaves no case to remember; judging on a timer would report a block up to
  the pace of the timer late. A block that fails to be written is retried and never dropped, since
  a block the sender is not told of is a message that waits for an unreadable reason.
- **A failed prompt without a cause is not a block.** A native process that was killed refuses the
  prompt (or its answer is lost) with the adapter online and nothing reported unavailable. That is
  an agent that cannot be woken, which the retries of the prompt rules go on trying, and no cause
  explains it to the sender; the message stays queued until its queue time and is told as expired
  then, as the other messages that were never read.
- **The probe is the moment a connection is judged, and a wait covers the probe that failed.** The
  host puts a connection in service before it probes, logs a probe that failed and goes on, so the
  engine never hears of it. The sessions of the instance are judged when the probe answers, and
  once `RetryBackoff` after the start for the case it did not: the connection was usable all along,
  and the judgment costs one prompt that may fail and be retried at the pace of the rules, which no
  loop can grow from.
- **A turn that is not observed is not counted, and one that is counted again is not forgotten.**
  The state of a turn of an adapter that is away is unknown, and a turn nobody can see must not
  stop the node; an endpoint whose adapter is away is not judged, because a prompt for it is
  bound to fail and its failure would be taken for a reason to call delivery blocked. When the
  adapter returns, the turns it reports running are the ones that still count.
- **A prompt that found the adapter not in service waits for the probe, not for the next
  message.** The host tells the node that an adapter started before it puts the connection in
  service, so the first judgment after a connection can find the call refused with nothing sent.
  That is a failure the next event cures, and the adapter's probe is that event: without it the
  message would wait for the next arrival or turn end, which may never come.

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
| A checkpoint is on disk before it is answered; a session's is taken while the session is being created | `adapters.go` (`checkpointPut`), `view.go` (`resolveSessionBeingMade`, `resolveAttemptBeingMade`) | `TestCheckpointPutIsOnDiskBeforeItIsAnsweredAndALaterPutReplacesIt`, `TestCheckpointPutRecoversFromACrashAtEveryPoint` |
| `Quiesce` judges the host's counts | `recovery.go` | `TestQuiesceJudgesTheAdapterHostsCounts` |
| `Quiesce` sees the outbound worker begin to wait out a delay after it looked at the worker running | `recovery.go` | `TestQuiesceSeesTheWorkerBeginToWaitOutADelayAfterItLookedAtTheWorkerRunning` |
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
| A session nobody controls is one alert for the control record of its creation, however often the node starts | `alerts.go` (`raiseAlertOnce`), `create.go` (`alertUncontrolled`) | `TestAnUncontrolledCreationIsToldOnceAcrossRestarts` |
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
| The ticks, the feedback flush and the looks at source records that fell due while the node was down wait for their steps of the start; `Serve` lets the rest go | `sched.go` (`recoveryKind`, `held`), `engine.go` (`Serve`, `loop`) | `TestRecordsDueDuringDowntimeAreTickedByStepTwelveAndNotBefore`, `TestSchedulerHoldsTheEntriesOfTheRecoveryKindsAndGivesOutTheOthers` |
| The periodic sweep removes what has served its retention, in chunks, and the view agrees with the disk afterwards | `sweep.go` | `TestThePeriodicSweepRemovesWhatHasServedItsRetention`, `TestTheSweepRemovesInChunksOfAtMostChunkRemoves`, `TestCutSweepTakesAtMostTheBoundInTheOrderOfTheStoreAndLosesNothing`, `TestForgetSweptTakesTheRemovedRecordsOutOfTheView`, `TestDroppingAControlRecordLeavesTheViewAsIfItWereNeverThere`, `TestAPullForASweptControlRecordAnswersNoRecord` |
| A summary that cannot be appended, an I/O error or a full disk, does not stop the sweep | `sweep.go` (`appendSummaries`) | `TestASummaryThatCannotBeAppendedDoesNotStopTheSweep` |
| A notice is not swept while the item it covers is on its record, and the endpoint is told once across a restart | `store` (`PlanSweep`), `sweep.go` | `TestANoticeIsNotSweptWhileTheItemItCoversIsStillOnItsRecord` |
| A pass ends when an endpoint it has yet to remove is open again, and the files of that endpoint stay | `sweep.go` (`planIsStale`) | `TestAPassEndsWhenAClosedEndpointItWasToRemoveIsRegisteredAgainBetweenChunks`, `TestAPlanWithAnEndpointThatIsOpenAgainIsStale` |
| Nothing is swept while the clock is in doubt, and the first period after the clear sweeps | `sweep.go` (`beginSweep`) | `TestNoSweepWhileTheClockIsInDoubt` |
| Archive files past the retention are removed, the others stay | `sweep.go` (`expireArchive`), `store` (`ExpiredArchiveFiles`) | `TestTheSweepRemovesTheArchiveFilesPastTheRetention` |
| A source record that waits for its last pull is not swept, the tick closes it at the window's end, and it is swept at no period before its retention | `store` (`RetainUntil`), `sweep.go` | `TestASourceRecordWaitingForItsFinalPullIsNotSweptBeforeItClosesNorBeforeItsRetention`, `TestPlanSweepNeverPlansASourceRecordWaitingForItsFinalPull` (store) |
| The pause of a clock set back is persisted at once | `engine.go` (`observe`, `AddClockState`) | `TestThePauseOfAWallClockSetBackIsPersistedAtOnceAndSurvivesACrash`, `TestAFatalErrorWhilePersistingThePauseStopsTheEngine` |
| Recovery | `recovery.go` | `recovery_test.go` |
| Waiting | `waiters.go` | `waiters_test.go` (`TestShutdownEndsEveryWaitEvenWhenTheInboxIsFull`, `TestAPageThatIsReadyWinsOverTheShutdown`) |
| A delivery that goes well tells its sender nothing; each anomaly is told once, in its own kind | `pull.go`, `feedback.go` | `TestNormalDeliveryGivesNoFeedback`, `TestEachAnomalyIsReportedOnce` |
| A message never acknowledged is handed out four times, each again as a possible duplicate, then is unknown and told once; a late acknowledgement corrects it once | `pull.go` (`recv`, `ack`), `ticks.go` | `TestNeverAcknowledgedStopsAfterThirdRedeliveryAndReportsOnce` |
| Long messages go whole or in parts within the budget, as many acknowledgements as parts shown, delivered only when the last part is acknowledged; a part can be asked for by number | `pull.go` (`pack`, `recvPart`) | `TestLargeMessagesWholeOrPartsAndAckCountsMatch`, `TestAPartOfAMessageCanBeFetchedByNumber` |
| A long message is cut in the same parts through every front end of its endpoint, and acknowledged normally across them | `pull.go` (`partBudgets`), `adapterreg` (`PartBudgets`) | `TestALongMessageIsCutTheSameThroughEveryFrontEnd` |
| An acknowledgement of a whole page that the node no longer remembers (after a restart, or past `pageMemory`) acknowledges the messages that fit whole and never a part of a message that was cut | `pull.go` (`itemsToAck`) | `TestAWholePageAcknowledgementAfterThePageIsForgottenAcknowledgesOnlyWhatFitsWhole` |
| An endpoint cannot acknowledge a lease it was not handed | `pull.go` (`ack`), `view.go` (`leased`) | `TestOtherEndpointTokenCannotAcknowledge` |
| The second check is made again at the hand-out, and only for what the page reaches | `pull.go` (`tryPage`, `handoutAuthorization`) | `TestADeclarationTightenedAfterIntakeRejectsAtRecv`, `TestAMessageBeyondThePageIsNotJudgedByTheSecondCheck` |
| A body that is wrong or gone is settled, not handed out, the others go on | `pull.go` (`pack`), `quarantine.go` (`readBody`) | `TestRecvOfABodyThatIsWrongOrGoneHandsNothingOutForIt` |
| A record that is due is ticked before recv hands out, and one the tick could not settle is left out while the rest of the page is handed out | `pull.go` (`tickEndpoint`, `pack`, `recvPart`) | `TestRecvTicksTheRecordsThatAreDueAndLeavesOutThoseItCouldNotTick` |
| Nothing is handed out or written by a recv after a fatal storage error, whether it met it ticking or settling a body | `pull.go` (`recv`, `buildPage`), `quarantine.go` (`readBody`) | `TestRecvHandsNothingOutAfterAFatalStorageError` |
| A lease is on disk: live across a restart, released at the start when it ran out; an acknowledgement comes with the next recv | `pull.go`, `ticks.go` | `TestALeaseAcrossARestart`, `TestRecvAcknowledgesTheEarlierPageFirst` |
| A recv that waits is served by an arrival, ends at its deadline, is cut to the front end's limit | `pull.go` (`serveReady`), `waiters.go` | `TestARecvThatWaits` |
| A direct endpoint is told with `endpoint.pending` how many messages wait for it, once for each message made ready by a separate command, not for a message a waiting recv took, and a session endpoint is not told | `pending.go` (`notePending`, `servePending`) | `TestADirectEndpointIsToldOnceThatAMessageWaits`, `TestAMessageHandedToAWaitingRecvIsNotAnnounced`, `TestASessionEndpointIsNotToldWithEndpointPending` |
| A notice whose body is damaged is told again from what it covered, and a crash anywhere in the settlement loses none of it and tells it only once | `feedback.go` (`reissueCovered`), `quarantine.go` (`quarantineOwner`) | `TestANoticeWhoseBodyIsDamagedIsToldAgainFromWhatItCovered`, `TestADamagedNoticeIsToldAgainOnceWhereverTheNodeDiesSettlingIt` |
| Every write of a recv and an ack survives a crash at every point | `lease`, `ack` (declared by the store) | `TestRecvAndAckRecoverFromACrashAtEveryPoint` |
| An anomaly is told once, in one notice that waits out the window and the cap; the queue survives a restart; feedback for a closed endpoint is dropped | `feedback.go` (`putOut`, `flushFeedback`, `sendNotice`, `loadFeedback`) | `TestAnAnomalyIsToldToTheSenderOnceInOneNotice`, `TestNoticesOverTheRateCapAreHeldAndNeverDropped`, `TestFeedbackThatFellDueWhileTheNodeWasDownIsSentAtTheStart`, `TestFeedbackForAnEndpointThatWasClosedIsDroppedAndLogged` |
| A notice goes to an endpoint whose queue is full and takes no place of it | `feedback.go` (`formNotice`) | `TestSystemFeedbackIsNotRefusedByFullBacklog` |
| A notice formed again is the same record; a notice that failed to be written goes back whole | `feedback.go` (`noticeKey`, `unsend`) | `TestTheKeyOfANoticeFollowsFromItsItemsAlone`, `TestTheKeyOfANoticeNamesItsKindsByTheirNames`, `TestUnsendPutsBackWhatFlushTookOut` |
| Every write that tells an endpoint survives a crash | `feedback-form`, `feedback-clear` (declared by the store) | `TestFeedbackRecoversFromACrashAtEveryPoint` |
| Every source record the loop writes enters the view through `putOut`, which queues the feedback it holds | `feedback.go` (`putOut`) | `TestEverySourceRecordTheLoopWritesEntersTheViewThroughPutOut` |
| A clearing of the records that failed after a notice is tried again, so a notice swept later cannot cause a second one | `feedback.go` (`clearCovered`, `retryClears`) | `TestANoticeIsNotToldTwiceWhenTheRecordsCouldNotBeCleared` |
| A target message nobody reads is rejected when the queue time runs out and the sender is told once; a record that is not final closes after `report_by`, a source record with the notice of how it closed | `ticks.go`, `view.go` (`onTick`) | `TestBacklogExpiryRejectsAndIsReportedOnce`, `TestARecordThatIsNotFinalIsClosedByTheEndOfItsTime`, `TestNoRecordAfterReportByChangesNothing` |
| One pull of the target before a queued or accepted source delivery closes; its answer is told with its true meaning; a failed call, no record, or a target that no longer keeps its records closes it with one notice; a delivery already told closes silently | `outbound.go` (`finalPullDue`, `finalPullPending`, `finalPullMade`), `ticks.go` (`tickedSource`, `holdForFinalPulls`) | `TestASourceAsleepPastReportByIsToldTheRejectionTheTargetSigned`, `TestASourceAsleepPastReportByTellsOnceWhenTheTargetCannotBeAsked`, `TestASourceAsleepPastTheTargetsRetentionAsksNoOne`, `TestADeliveryLearnedByThePullBeforeTheCloseIsSilent`, `TestADeliveryTheSenderWasToldExpiredClosesWithoutAWord`, `TestNoRecordInThePullBeforeTheCloseIsNotALoss`, `TestADeliveryRemovedFromTheOutboxByAConflictIsClosedWithANoticeWhenTheTargetCannotBeAsked`, `TestAConflictedDeliveryIsPulledOnceAndClosedWithANoticeWhileTheNodeRuns`, `TestADeliveryWhoseLastPullNeverComesClosesWhenTheTargetsRetentionIsOver`, `TestNoPullBeforeTheCloseIsMadeWhileTheClockIsInDoubt`, `TestOutboundNeedByState`, `TestAControlSignalWhoseSenderSleptPastReportByLearnsItsReplyFromThePullBeforeTheClose` |
| A delivery with no receipt expires only after a query that was answered found none, is told once and its correction once | `outbound.go` (`applyAnswer`) | `TestExpiredUnconfirmedCorrectedOnce`, `TestSourceWithoutAReceiptIsNotToldTheTargetLostTheRecord` |
| Contact that keeps failing is told once; twenty messages are one notice; the delivery ends closed after `report_by`, told once more as expired | `outbound.go` (`outResult`, `noteContactFailing`) | `TestTwentyToUnreachableTargetMergeIntoOneNotice`, `TestAnUnreachableTargetIsToldOnceAndTheDeliveryClosesAfterReportBy` |
| Nothing is ticked or judged while the clock is in doubt | `ticks.go` (`tickBatch`), `outbound.go` (`observe`) | `TestClockRollbackPausesSigningUntilCleared`, `TestNoRecordIsNotAnsweredWhileTheNodeMayNotSign` |
| The records of time survive a crash at every step | `tick` (declared by the store) | `TestTheRecordsOfTimeRecoverFromACrashAtEveryPoint` |
| A damaged file is settled by the scan entry first of the rules; a source record becomes the sender's lost record, a file lost unread a notice of its loss, a target record is removed; each of the first three tells the sender once (a notice that is itself a target record tells nobody, see the known gaps) and each raises one alert | `quarantine.go` (`quarantineAll`, `settleFile`, `lostOut`), `recovery.go` | `TestCorruptDeliveryRecordIsReportedToTheSenderOnce`, `TestOpenQuarantinesADamagedFileAndOpens`, `TestQuarantineRecoversFromACrashAtEveryPoint` |
| A damaged control record is settled with the others, closes the endpoints it was creating, and is a record the node no longer holds | `quarantine.go` (`quarantineAll`, `damagedCtlAlert`) | `TestQuarantineClosesEveryCreatingSessionEndpointOfTheDamagedControlRecord`, `TestControlRecordLostBeforeItsReplyIsTold` |
| A control signal is sent by the worker, taken in by one order, and answered with its report and reply | `control.go`, `intake_control.go`, `inbound.go`, `outbound.go` | `TestSessionCreateMakesAnEndpointAndTellsItsSender`, `TestControlRequestsAreChecked`, `TestControlSignalIsRefusedWhenTheNodeQueueIsFull`, `FuzzInbound` |
| A control signal the target refuses at intake is rejected at once with its reason, answered from its record when repeated or asked about, kept like a finished control record, and survives a crash at every write | `intake_control.go` (`controlTarget`, `refuseControl`), `inbound.go` (`answerPull`) | `TestAControlSignalTheTargetRefusesAtIntakeIsRejectedAtOnceWithItsReason`, `TestAControlSignalFromANodeElsewhereThatTheRulesRefuseIsRejected`, `TestARefusedControlSignalIsAnsweredFromItsRecordWhenItIsRepeated`, `TestARefusedControlSignalIsKeptWhileASenderCanAskAndSweptAfter`, `TestARefusedControlSignalRecoversFromACrashAtEveryPoint` |
| A creation the node refuses itself is answered with the receipt, makes no endpoint and calls no adapter | `intake_control.go` (`createRefusal`) | `TestSessionCreateWithUnknownPresetIsRefused`, `TestSessionCreateWithPresetClosedToLocalSourcesIsRefused`, `TestSessionCreateIsRefusedWhileTheAdapterIsOffline` |
| What the adapter refuses is told, the endpoint is closed and no session is made | `create.go` (`createResult`, `settleCreate`) | `TestSessionCreateWithParamsBeyondPresetIsRefused`, `TestSessionCreateFailedAsControlUnknownAlertsTheOwner` |
| A native session made and an adapter lost are one session | `create.go` (`startCreate`, `redriveCreates`, `reconcileCreations`) | `TestAdapterKilledAfterNativeCreateYieldsOneSession`, `TestCreationCutOffByACrashIsGoneOnWithOneSession` |
| A creation that is not ready by its deadline is unconfirmed, closes its endpoint and alerts the owner; one past its deadline at the start is settled the same way | `create.go` (`createDeadline`, `reconcileCreations`) | `TestSessionCreateNotReadyByItsDeadlineIsUnconfirmedAndALateSessionIsAlerted`, `TestCreationOverdueAtStartIsSettledAsUnconfirmed` |
| A creation whose control record was closed first is reported loudly once and not tried again | `create.go` (`settleCreate`, `forgetCreation`), `intake_control.go` (`withReply`) | `TestCreationWhoseControlRecordWasClosedIsReportedOnceAndNotRetried` |
| A reply or the reply to a creation reaches a sender whose declaration takes nobody's messages | `outbound.go` (`applyControlReply`) | `TestRepliesAndCreateRepliesPassRestrictedDeclaration` |
| `endpoint.status` is answered by the node | `intake_control.go` (`statusReply`) | `TestEndpointStatusIsAnsweredByTheDaemon` |
| A control record is settled by time | `ticks.go` (`tickBatch`), `view.go` (`setCtl`) | `TestControlRecordIsSettledByTime`, `TestTheRecordsOfTimeRecoverFromACrashAtEveryPoint` |
| An initial message is delivered to the session as any message is, and judged again as the creation when handed out | `initial.go`, `intake_control.go` (`takeInCreate`), `pull.go` (`handoutAuthorization`) | `TestLocalEndpointsExchangeMessagesAndControlSignals`, `TestForgedHeaderInInitialMessageDoesNotChangeFrom` |
| The reply of the new session carries the creation's reply, and the sender takes the session from it | `outbound.go` (`evidenceFor`, `encodeFor`, `withControlReply`) | `TestReplyFromNewSessionBeforeControlReplyIsAdmitted` |
| The initial message of a creation that makes no session is told, not lost | `create.go` (`settleCreate`), `drain.go` | `TestInitialMessageOfACreationThatMakesNoSessionIsTold` |
| A creation whose initial message or endpoint is lost while it is not settled is failed | `initial.go` (`failCreationWithoutMessage`, `reconcileInitialMessage`), `create.go` (`failCreationWithoutEndpoint`) | `TestBodyLostFromTheInitialMessageFailsItsUnsettledCreation`, `TestCreationWhoseEndpointWasDamagedIsFailed` |
| An interrupt is carried out when accepted and is not behind the messages of the session | `execute.go`, `view.go` (`queuedIn`) | `TestInterruptIsNotQueuedBehindMessages` |
| A closed session endpoint owes its native session to the adapter until the adapter answers `closed`: asked at once, after the retry backoff and at every connection; the answer is recorded; an owner close, a close signal that was not confirmed and a session made for a closed endpoint are released | `release.go`, `execute.go` (`writeExecution`), `create.go` (`settleCreate`), `endpoints.go` (`putEndpoint`) | `TestOwnerClosingASessionReleasesItsNativeSession`, `TestAClosedAnswerToACloseSignalIsRecordedAsTheRelease`, `TestAnUnconfirmedCloseSignalIsFollowedByARelease`, `TestAReleaseThatCouldNotBeSentIsSentWhenTheAdapterConnects`, `TestASessionCreatedForAnEndpointThatWasClosedMeanwhileIsReleased`, `TestOnlyAClosedAnswerRecordsTheRelease`, `TestAReleasedNativeSessionIsNeverAskedAboutAgain`, `TestAReleaseIsAskedAgainAtEveryConnectionOfItsAdapter`, `TestAReleaseIsAskedAgainAfterACrashBetweenTheAnswerAndTheRecord` |
| A native session that stays owed for the reply deadline is told to the owner once, across restarts | `release.go` (`releaseDeadline`) | `TestAReleaseThatIsNeverConfirmedRaisesOneAlertAfterTheDeadline`, `TestAReleaseThatSucceedsBeforeTheDeadlineRaisesNoAlert` |
| A close releases the native session, closes the endpoint, settles the queue and removes the state kept for the session | `execute.go` (`settleExecution`), `endpoints.go` (`putEndpoint`) | `TestSessionCloseReleasesTheNativeSessionAndSettlesWhatWasQueued` |
| A result of a close or an interrupt that could not be written is written again without asking the adapter again, also after the deadline | `execute.go` (`settleExecution`, `executeRetry`, `executeDeadline`) | `TestFailedWriteOfTheResultOfASignalIsTriedAgainWithoutAskingTheAdapter` |
| An interrupt of a running turn is told stopped when the turn ends within the wait and requested when it does not; the end of a turn finds nothing to settle for a signal that has no execution | `execute.go` (`interruptTurnEnded`, `interruptConfirmDue`) | `TestInterruptOfARunningTurnIsToldByWhetherTheTurnEnds`, `TestTurnEndedForAnInterruptThatHasNoExecutionSettlesNothing` |
| The declarations of a session govern its messages, not the signals that act on it | `intake_control.go` (`controlTarget`) | `TestSignalsReachASessionThatTakesNothing` |
| What the endpoint cannot take is answered without the adapter, what the adapter cannot be asked is told after its deadline | `execute.go` (`takeInExecute`, `executeDeadline`) | `TestSignalsNobodyCanCarryOutAreAnsweredWithoutTheAdapter`, `TestSignalsWhileTheAdapterIsAwayAreTold` |
| `adapter.start` lists the open sessions with their access and checkpoint | `adapters.go` (`sessionAttaches`) | `TestAdapterStartCarriesTheOpenSessionsWithTheirAccess` |
| Every write of a session creation, with or without an initial message, and of a close survives a crash | `control-intake`, `control-result`, `control-reply` (declared by the store) | `TestSessionCreationRecoversFromACrashAtEveryPoint` |
| A body that is not what its record says settles that record by what it needs | `quarantine.go` (`readBody`, `quarantineBody`) | `TestBodyWithWrongHashIsQuarantinedAndRecordsTreatedAsLost` |
| A damaged floor file met by a write is settled and the write goes on; a damaged file of another directory is not settled while the node runs | `quarantine.go` (`settleLive`, `retryingDamaged`, `syncAfterSettlement`) | `TestADamagedFileOtherThanAFloorFileIsNotSettledWhileTheNodeRuns`, `TestADamagedFloorFileMetWhileTheNodeRunsIsSettledAndTheWriteGoesOn` |
| The view takes what the store returned and agrees with the disk | `drain.go`, `view.go` | `TestViewMatchesAFreshScanAfterWrites` |
| No method makes a session endpoint | — | `TestEngineHasNoMethodThatMakesASessionEndpoint` |
| Node rules do not apply to a local sender, declarations do | `inbound.go` (`judgeAuthz`) | `TestLocalSourcesIgnoreNodeRules`, `TestEndpointDeclarationRestrictsLocalSenders`, `TestARemoteSenderIsJudgedByTheNodeRulesAndALocalOneIsNot` |
| A reply with evidence is admitted before the report was pulled, a forged reply-to gets nothing | `inbound.go` (`admitReply`) | `TestReplyWithEvidenceAdmittedBeforeReportPulled`, `TestForgedReplyToGetsNoPermission`, `TestEvidenceThatDoesNotVerifyIsDiscardedAndTheOwnerIsToldOfIt` |
| A lost receipt and a resend leave one record; another body under one identifier, or a message under the identifier of a control signal, is a conflict | `inbound.go` (`takeIn`), `outbound.go` | `TestLostReceiptRetryKeepsOneRecord`, `TestSameIDDifferentBodyConflicts`, `TestAMessageUnderTheIdentifierOfAControlSignalIsAConflict` |
| The backlog of an endpoint, the queue and the disk of the node | `inbound.go`, `send.go`, `capacity.go` | `TestEndpointBacklogFullIsRejectedWithReason`, `TestNodeQueueFullBackpressuresNewSends`, `TestDiskFullGivesNoFalseAcknowledgement`, `TestDiskFullAtTheTargetGivesNoReceipt` |
| Requests no node takes, sequence numbers, the clock pause, idempotency retention | `send.go` | `TestASendThatNoNodeWouldTakeIsRefusedAndLeavesNothing`, `TestASendWithAnEmptyBodyIsRefused`, `TestSequenceNumbersCountEachPairOfSenderAndRecipientFromOne`, `TestNothingIsSignedOrAcceptedWhileTheWallClockCannotBeTrusted`, `TestAnIdempotencyKeyIsRememberedUntilItsRetentionEnds` |
| The state of each delivery of a message and, for a signal, its verified reply are read from the sender's record, for its sender alone | `status.go` | `TestTheStatusOfAMessageIsTheStateOfEachDeliveryAsTheSenderHoldsIt`, `TestOnlyTheSenderOfAMessageIsToldItsStatus`, `TestTheStatusOfAMessageSurvivesARestart`, `TestEveryStateAndRejectReasonHasAWireName`, `TestTheRecipientsOfASourceRecordAreTheOnesOfItsKeyInOrder` |
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
| An endpoint is woken once for messages that arrive together, not while its turn runs or its prompt is unread, once more when a turn ends with the prompt unread, and again for a message that arrives after the minimum interval | `prompts.go` (`noteArrival`, `noteTurnEnded`, `servePrompts`, `retryPrompt`) | `TestBusyOrUnreadAgentGetsNoFeedback`, `TestUnreadPromptRepromptedOnce`, `TestNewArrivalAfterMinIntervalWakesOnce`, `TestFiveArrivalsStartOneTurn` |
| The turns that prompts start are capped for the node, the endpoint that waited longest goes first, a raised cap and a turn that ends free places, and a turn that went on while the adapter was away holds its place | `prompts.go` (`promptRound`, `servePrompts`), `operations_settings.go` | `TestPromptTurnsBeyondNodeCapWaitOldestFirst` |
| An endpoint whose adapter is away is not prompted, and is when the adapter is back | `prompts.go` (`promptRound`, `noteInstanceProbed`) | `TestMessageForAnOfflineAdapterWaitsAndPromptsWhenItIsBack` |
| The endpoints and turns of an adapter that is offline take no place of the node's cap: another adapter's waiting endpoint is prompted in the free place, and a turn that went on while its adapter was away is counted again when the adapter reports it, two adapter instances on one node | `prompts.go` (`promptRound`) | `TestEndpointOfAnOfflineAdapterLeavesTheFreePlaceToAnotherAdapter`, `TestTurnOfAnAdapterThatWentOfflineIsCountedAgainWhenItReportsItRunning` |
| After a restart the time of the last recv is taken from the newest lease of the endpoint's messages, so a prompt the agent had read still looks read | `prompts.go` (`approximateLastRecv`) | `TestARestartDoesNotMakeAReadPromptLookUnread` |
| A turn that began for the prompt in flight settles it as delivered, so an agent idle at every turn end is woken once more and not every time | `prompts.go` (`noteTurnStarted`), `adapters.go` (`notified`) | `TestTurnStartedForThePromptInFlightSettlesItDelivered` |
| A prompt that failed is held for the retry time of the rules and then sent again, with nothing else bringing the endpoint back | `prompts.go` (`settlePrompt`, `servePrompts`), `handoff.DecidePrompt` | `TestRefusedPromptIsRetriedAfterTheRetryTimeAndDelivered` |
| The attempt is on disk before the adapter is called; one that was in flight at a restart is asked of the adapter and not sent again | `prompts.go` (`dispatchPrompt`, `startReconcile`, `reconcilePrompts`), `recovery.go` (`recoverReconcile`) | `TestDaemonKilledNativeTurnContinuesAndReconcilesWithoutResend`, `TestAPromptIsSettledOnceAfterACrashAtEveryPoint` |
| The attempt is recorded as in flight before the adapter is called, and a write that fails sends nothing and is retried after the backoff | `prompts.go` (`dispatchPrompt`) | `TestPromptIsRecordedBeforeTheAdapterIsCalledAndAFailedWriteIsRetried` |
| A refusal is a failure, a lost answer is a delivery or an unknown outcome as the adapter says, and only a delivery marks the message | `prompts.go` (`promptResult`, `reconcileResult`, `settlePrompt`) | `TestPromptOutcomesAreRecordedAndAnUnknownOneIsNotSentAgain` |
| The checkpoints of the creation attempt and of the first prompt are dropped when the creation settles | `create.go` (`settleCreate`, `failCreationWithoutEndpoint`, `dropCreationCheckpoints`), `prompts.go` (`dropAttemptCheckpoint`) | `TestSettledCreationDropsTheCheckpointsOfItsAttempts` |
| The checkpoint of a prompt attempt is dropped when the attempt settles | `prompts.go` (`dropAttemptCheckpoint`), `store.CheckpointDrop` | `TestSettledPromptAttemptDropsItsCheckpoint` |
| A prompt does not make the check at recv pass | `pull.go` (`handoutAuthorization`) | `TestDeclarationTightenedAfterPromptRejectsAtRecv` |
| Delivery blocked past the threshold is reported once with its cause to the sender, merged for several messages, and its recovery once | `blocked.go` (`serveBlocked`, `judgeBlocked`), `adapterreg.BlockedCause` | `TestBlockedReportedOnceWithCauseAndRecoveredOnce` |
| A native process killed during a prompt gives no feedback and the message stays readable | `blocked.go` (`judgeBlocked`; no cause, no block) | `TestNativeKilledDuringPromptGivesNoFeedbackAndKeepsMessage` |
| A block is written marks first and head last, a failed write is tried again, and marks a crash left are lifted whatever the head says | `blocked.go` (`judgeBlocked`), `store.Blocked` | `TestBlockedReportIsWrittenAgainAfterAFailedWrite`, `TestNoRecordKeepsABlockMarkAfterACrashWhenTheCauseIsGone` |
| A message that arrives during a block is marked; reading the messages ends the block | `blocked.go` (`judgeBlocked`), `view.go` (`onInChange`) | `TestMessageArrivingDuringABlockIsMarkedBlockedToo`, `TestReadingTheBlockedMessagesEndsTheBlock` |
| An adapter not connected since the node began to serve is offline since then, unless the block is already reported, which a restart keeps until a connection shows the cause gone | `blocked.go` (`blockedSignals`) | `TestAdapterNotYetConnectedIsOfflineOnlySinceTheNodeBeganToServe`, `TestARestartWhileABlockIsReportedKeepsItUntilTheAdapterShowsTheCauseGone` |
| Messages that already wait when the adapter goes away are blocked after the threshold; a prompt that fails after the cause was reported counts from when it was sent | `adapters.go` (`gone`), `prompts.go` (`settlePrompt`) | `TestMessagesThatWaitWhenTheAdapterGoesAwayAreBlockedAfterTheThreshold`, `TestAPromptThatFailsAfterTheCauseWasReportedCountsFromWhenItWasSent` |
| The sessions of an adapter whose probe failed are judged after a wait | `adapters.go` (`markStarted`), `blocked.go` (`probeFallback`) | `TestSessionsOfAnAdapterWhoseProbeFailedAreStillPromptedAfterAWait` |

Known gaps, each filled in with the feature it belongs to:

- A notice of the system endpoint whose `in/` file is itself damaged is removed at the scan
  like any target record, and cannot say which feedback it covered: the covers are in the
  file that was lost, and `reissueCovered` reads them from the record. What it told is not
  told again; the owner has the `corrupt_record` alert. Only a notice whose body file is
  damaged, which leaves its record readable, is told again.

- A creation that would make the node hold more endpoints than a node may have is refused
  with a call error and no record, so its sender reads the refusal as a failed send (the
  unreachable notice after about two minutes, `expired_unconfirmed` after about 12.5). The signal
  was accepted and no session was made, which is what the reply of a failed creation says, but
  none of its reasons fits (they name the preset, the adapter and the native runtime), and a new
  reason is a change of the protocol.
- Phase 11 of `RecoverDelivery` settles the creations that were not settled and asks the
  adapters about the prompts that were in flight. A prompt and a block have no replay: the
  store writes their target records first and the head last, and phase 11 and the prompt logic
  redo what the head does not say is done (a prompt still in flight in the head, a block the
  records hold and the head does not report); phase 12 ticks the target and source records,
  phase 13 sends the feedback that fell due and phase 14 goes on with the outgoing messages.
  The tick of a target record does not yet wait for an active turn before it lets an expired
  lease go: `leaseMayRelease` says yes, and the signals of turns are to decide it there.
- Prompts go into session endpoints only; a direct endpoint is told that messages wait with
  `endpoint.pending`, which is a different thing (see Announcing).
- A prompt the adapter accepted (`accepted`) and never made durable with `prompt.persisted` is
  taken for delivered at once. The adapter protocol says the node reconciles it when no
  `prompt.persisted` follows but gives no time to wait, and a window is a timing of the protocol,
  so it is not invented here.
- After an adapter reconnects and before its first availability snapshot, the engine sees no cause
  for its sessions: the probe result's authentication and dependency findings are not mapped into
  the signals of `adapterreg`, so a block whose cause is one of those is taken for over (and
  corrected to the sender) at the first judgment after the connection; when the snapshot then says
  the cause holds, the block is marked again, but a delivery is told as blocked once, so its sender
  is not told again.
- The block of a message is judged from the messages that wait, not from one of them: a message
  that is suspended at the second check is not waiting and is not marked. A change of the node
  rules or of the declaration that makes a suspended message wait again is not an event of the
  judgment, as it is none of the prompt rules; the next change of the endpoint's records judges.
- The node does not keep when an endpoint last called recv. After a restart the time is taken
  from the newest lease issued for the endpoint's messages (`approximateLastRecv`), so a recv
  that issued no lease (the queue was empty) or only handed a part out again under a lease that
  still ran is forgotten, and an endpoint whose agent had read its last prompt that way may be
  woken once more by the end of a turn. A prompt that was not read is never taken for read.
- An empty body is refused: the Agent interface says a body may be empty and an envelope
  of a message must have one (`TestShouldASendWithAnEmptyBodyBeAccepted`, skipped).
- A body that is not what the delivery of an initial message says, found when the session
  is already made, loses the message: the delivery is removed with an alert and its sender is
  told by the record the target no longer holds (`lost_at_target`).
- A write that fails on `endpoint.register` is refused with the engine's own
  reason (`storage_full`, `storage_unavailable`, `storage_fatal`). Which code of the
  adapter protocol each maps to is for the adapter host to decide; if no existing
  code fits, it stops and says so, and the protocol is changed for it, not worked
  around in the host. The cap of the node maps to the protocol's `endpoint_limit`.
- A stored administrative result is dropped from the engine's table by the sweep that
  deletes it after its retention, in the step that removes it, or by a restart.
