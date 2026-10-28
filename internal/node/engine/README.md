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
life of an adapter's connection, the capacity check, the storage of alerts, the two recovery entries, and the sending
and taking in of messages: the acceptance of a message from a local endpoint, the
outbound worker that carries it through the transport and applies what comes
back, and the receiving side that judges an envelope and signs the reports, and the
feedback the sender's node tells its endpoints, and the control signals: their sending,
their taking in, the creation of sessions they ask for with the initial message they may carry,
the closing and the interrupting of a session, the release of the native session of a session endpoint that is closed (`release.go`: what is owed is read from the endpoint record, `store.EndpointRecord.ReleasePending`, and every way of closing an endpoint goes through `putEndpoint`, which begins it), and the pulling of messages: what
recv hands out under a lease, what ack settles and what a lease that runs out does, and the
prompts that wake a session: when one is sent, how its attempt is recorded before the adapter is
called, and how one whose outcome is not known is asked about at the start; and the blocked
delivery to a session: when it is judged and how the verdict is recorded.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Open(ctx, Deps, Params)` | The scan entry: scans `state/`, runs the recovery rules until none has work, builds the view. Returns a `ScanReport`. |
| `Start`, `RecoverDelivery`, `Serve`, `BeginShutdown`, `Stop`, `Done`, `Err`, `Phase` | The life of the loop: opened, recovering, running, stopping, stopped or failed. `RecoverDelivery` is the second recovery entry; the table of the start steps is in the reference. |
| `AddArchiveSweep` | Registers the archive sweep (`sweep.go`) as a periodic task with the period the composition root names, before `Start`. |
| `AddPeriodic`, `AddClockState`, `Task`, `TaskInput`, `SignRefusalOf` | A task the composition root has the loop run every period, before `Start`. The clock state's task is also run at once by the command that finds the wall clock set back. A task is told whether the node may sign (`TaskInput.SignRefusal`, from `SignRefusalOf`, the one judgement of signing). |
| `Quiesce`, `Expect`, `AdapterExpect`, `Report` | Returns when no command waits, nothing is due, no goroutine of the engine runs and every adapter named in `Expect.Adapters` is quiet by the host's own counts. |
| `AdapterAdmitted`, `AdapterStarted`, `ProbeTokens`, `AdapterProbed`, `AdapterGone`, `AdapterNotified` | What the adapter host calls for the life of a connection, in that order: the admission of an instance (which makes its runtime endpoint and returns what `adapter.start` carries), what the adapter answered to `adapter.start` and `adapter.probe`, the probe tokens, the end of the connection, and the notifications. `*Engine` satisfies `adapterhost.Engine`. |
| `RegisterDirect`, `UnregisterDirect`, `CheckpointPut` | What the adapter host calls to answer the requests of an adapter: the registration and unregistration of its direct endpoints, and the state it asks the node to keep. |
| `Adapter`, `AdapterView`, `AdapterSignals`, `ProbeTokenView` | Reads of what the engine knows of an instance (admission, online or offline since when, probe result, availability, busy turns), and the availability as `handoff.Signals` for one endpoint. |
| `ProbeTokenEnv`, `TokenEnv`, `HomeEnv`, `ProbeTokenLifetime` | The names of the environment variables that carry a probe token, an endpoint's token and the data directory, and the lifetime of the probe tokens; the Agent interface refers to them. |
| `Send`, `Reply`, `SendRequest`, `ReplyRequest`, `SendResult`, `DefaultContentType` | Accept a message from the endpoint a caller is: the body and the source record are on disk before the call returns, and what the recipients' nodes make of it is learned from the state of the deliveries. |
| `Recv`, `RecvRequest`, `Ack`, `AckRequest` | Hand the page of the queue of the caller's endpoint out under a lease (the bytes the recv command prints), and acknowledge parts of a page. The second check is made again at the hand-out, the budget and the longest wait are the adapter's declaration for the front end, and a wait is a waiter the loop owns. |
| `RecvObserved` | What the adapter host calls for `recv.observed`: acknowledges a page for the agent when the text the adapter read from the runtime's record contains the page the node printed in full, and refuses with the reason otherwise (a cut or replaced result, a page the node does not hold). |
| `Control`, `ControlRequest` | Accept a control signal (`session.create`, `session.close`, `session.interrupt`, `endpoint.status`) from the endpoint a caller is, as `Send` does; a creation may carry an initial message (`Body`, `ContentType`). What the target made of it is read from the status of the message. |
| `MessageStatus` | The state of each delivery of a message the caller's endpoint sent, from the sender's record, and for a control signal its verified reply. Another endpoint's message is unknown to the caller. |
| `SetTransport`, `Transport`, `Keys` | The transport the outbound worker calls (the narrow interface with the signatures of `*transport.Loopback`) and the node's keys, which sign the reports. Both are for the composition root; an engine without them refuses to send and to take in. |
| `Inbound`, `Pull` | The engine as the `transport.Handler`: an envelope for this node, answered with signed reports, and a pull for the reports of a message to the recipients it names: for each, the signed report or the recipient in `NoRecord`. |
| `Declare`, `Caller`, `ByToken`, `ByAdapter` | The declaration of an endpoint, by whoever the endpoint's own caller is, resolved in the command. |
| `Endpoints`, `CloseEndpoint`, `CloseRequest`, `CloseResult`, `Alerts`, `RaiseAlert` | The engine side of `endpoint list` and of a close the engine's own features ask for, and the alerts. The numbers of the status are `Status`'s. |
| `Execute`, `ManagementCommand`, `StoredResult`, `Operation`, `CloseEndpointOp`, `StopDaemonOp` | The one door of every administrative operation of the owner: the replay check, the operation, and the storing of its result with the fingerprint are one command of the loop. The owner's request to close an endpoint and to stop the daemon are the two operations there are; the package that owns the effect of another adds its type here. |
| `ClearClockRollbackOp`, `ConfirmRevocationBackupOp`, `RetireIdentityOp`, `AcknowledgeAlertOp`, `SetNodeSettingsOp`, `SetEndpointSettingsOp` | The owner's operations on the clock, the identity, the alerts and the settings, each an `Operation` with the effect in the loop and its result made by a function the interface passes in (`ClockClear`, `BackupConfirmed`, `EndpointChange` say what was done). |
| `InstallAdapterOp`, `EnableAdapterOp`, `DisableAdapterOp`, `AddPresetOp`, `RemovePresetOp` | The owner's operations on adapters and presets, each an `Operation` with its result made by a function the interface passes in. An installation has work outside the loop (see the rules); enabling asks the host to run the instance, disabling asks it to stop and closes the instance's endpoints. |
| `AdapterList`, `Presets` | Reads of the installed adapters, the instances with the probe of each enabled one, and the presets with the version a change of them names. |
| `PresentProbeToken`, `ProbeTarget`, `ProbeView`, `ProbeState` | What a front end calls through `AgentService` to prove it reaches the node, and what the engine says of the probe of an instance. |
| `NodeSettings` | A read of `node.json` as the loop sees it, with the version a change of it names. |
| `Status`, `NodeStatus`, `ReminderKind` | A read of what the owner is shown of the node, from the loop's state at one command: the phase, the reminders, the clock and its pause, the certificate, the room, the deliveries by state, the endpoints. |
| `ExportRevocation`, `RevocationExport` | The self-revocation the keys verified as this node's, armored, with its digest. Not in the loop and stored nowhere. |
| `OperationResult` | A read: the result stored for a request key, from either place a result is kept, and whether there is one. Taken while the node starts; writes nothing. |
| `PlanRecovery`, `RecoveryPlan` | The work of the recovery rules as a pure function of a scan. |
| `Reason`, `Refusal`, `ReasonOf` | The closed set of reasons a method refuses for; the interface packages alone map them to the wire. |
| `Transactions()` | The declarations of the transactions of two owners that the engine opens: `direct-register` and `runtime-admit`, and for the owner's operations `clock-clear`, `backup-confirm` and `identity-retire`, each the effect followed by the result, and the installation of an adapter, `adapter-install` (the rename of the staged directory, the pointer, the result, the discarding of the old version) and `adapter-install-current` (the result alone). |
| `AgentService`, `ManageService` | What the Agent and management interfaces ask of the engine; `*Engine` implements them, and the methods are added as the engine learns to do them. Everything of `ManageService` that changes state is `Execute`; the other methods read. |
| `Store`, `Config`, `Beginner`, `Adapters` | What the engine asks of the store, the configuration, the transaction writer and the adapter host (its calls to an adapter, and `Counts`, which only reads). |

## Rules

- **A prompt's attempt is on disk before the adapter is called, and is never sent again.**
  `dispatchPrompt` writes the attempt as in flight (`PromptBegin`) and only then spawns the call;
  a result that does not say whether the prompt arrived is asked of the adapter
  (`prompt.reconcile`) and recorded as the verdict says, and step 11 of the start asks about every
  attempt found in flight. No path calls `prompt.deliver` twice for one attempt. An `accepted`
  answer leaves the attempt in flight until delivery proof arrives (`prompt.persisted` or a
  `turn.started` naming it), or until `Params.PromptConfirmWait` elapses and reconciliation begins.
  Each call (`prompt.deliver`, `prompt.reconcile`) ends at `Params.PromptCallDeadline` on the
  injected clock, by cancelling its context; a call that got no answer is an unknown outcome.
  The engine reference's [Prompting description](../../../docs/references/engine/README.md#what)
  covers delivery proof and late corrections.
- **The prompt decision is asked for the whole node, and again whenever its answer may have
  changed.** `servePrompts` runs after each command of the loop once the node serves, and acts
  only when `promptsDirty` says something the decision reads has changed (an arrival, the end of
  a turn, the probe of an adapter being answered, which is when its connection is in service, the
  cap, the clock, the end of a close or interrupt execution, the retry time the decision names
  after a failed prompt). Every event that can change it sets the flag in the place that owns
  the event; no handler asks the decision itself. A single `DecidePrompt` never
  authorizes a send.
- **A session with an accepted close or interrupt is busy for prompting until the signal
  settles.** `promptRound` reads the control records, so a turn ending before the adapter's
  answer cannot start another turn while the signal is being carried out. This holds during
  retries and while a result waits to be written too. The event that waited is kept, and ending
  the execution marks the prompt input changed, so an interrupt's settlement judges the
  still-open session again. Only actual prompt-started turns count against the node's cap.
- **Blocked delivery is judged by the endpoint, whenever its input may have changed, and a
  verdict that changes nothing writes nothing.** `serveBlocked` (`blocked.go`) judges the
  endpoints marked since the last step: a record of the endpoint taken in or dropped
  (`view.onInChange`), an attempt settled, the adapter's availability, connection or probe, the
  start of the node and the clearing of a wall clock that was set back. The rule names the
  instant a short run of failures passes the threshold (`BlockedVerdict.RetryAt`), and that is the
  entry `schedBlocked`. A block is written like a prompt: the target records first, the head last,
  one call (`store.Blocked`). Marks are found by the records' own flag and not by the head, so a
  block a crash left half written is lifted when its cause is gone. Nothing here tells a sender
  anything: the sender pulls the report of the target, which carries the flag, and the delivery
  rules make the item of feedback.
- **A turn counts against the cap from the dispatch of its prompt until it ends or the prompt is
  known not to have started one, and only while its adapter is online.** An endpoint whose
  adapter is offline is not judged.
- **A lease is extended before it runs out, only while the adapter reports a turn running, and
  one that ran out is always released.** `serveLeases` (`lease.go`) extends what is due with
  `delivery.Record.Renew`, which refuses an expired lease and one at the cap, so the tick of a record
  never waits for a turn: there is no state in which an expired lease is held and the record's other
  deadlines (the queue time, `report_by`) are held with it.
- **The node acknowledges for an agent only a page it remembers and sees in full in what the model
  was shown.** `RecvObserved` compares with the page the node printed (`issuedPage.output`); a lease
  it holds no page for, a cut or replaced result and any text that does not contain the page are
  refused, and the agent acknowledges by itself or is handed the message again.
- **Workers start only in the loop.** `spawn` is called from a command or a due
  entry, never from another goroutine: `finish` waits for the workers on the loop's
  goroutine, and a worker started concurrently with that wait could be missed. It
  panics when it is called from any goroutine but the loop's, which it tells by the
  goroutine's id that the loop records when it starts, not by what the loop is doing
  at the moment.
- **One loop, typed commands, the order of the inbox.** Every command is a type
  with a `run`; commands run in the order they entered the inbox. Commands a
  command queues for itself (the chunks of a long batch) run after the commands
  the inbox holds at that moment and before those that arrive later, so a busy
  inbox neither starves them nor is held up by more than one chunk. The target
  records of a prompt or a block are the known exception: they are one call (the
  head is written last, so a chunked write would mark the rest as done), bounded by
  the endpoint's backlog limit plus the system feedback instead of
  `Params.ChunkReplaces`. A command is answered exactly once, on a channel of
  capacity one, so the loop never waits for a caller that has gone.
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
- **A package is copied outside the loop and committed inside it.** An operation that
  has long work first (`preparedOperation`) is run in this order and no other: the loop
  looks the request up and, only if it is to run, takes the one place of the install
  worker and draws the randomness for the staging name; the worker, a goroutine the
  engine owns that `Quiesce` counts, prepares, has the loop run the operation as an
  ordinary one (which looks the request up again) and settles what the preparation left,
  and only then is the request answered. A request that was done is answered without any
  preparation, so nothing is copied twice. What is judged is the staged copy, never the
  source.
- **Enabling again waits for the closing of the disabling.** An operation that cannot
  run until the records of an endpoint are closed returns `waitForDrain`; its command is
  held as a registration is and run again from the start, so the operation is never
  carried out in part.
- **The owner's clear of the pause is one replacement.** `ClearClockRollbackOp` sets
  the mark to the clock as it reads (which may be below the mark it replaces) and
  removes the pause flag in the one replacement of `keys/clock.rec` that
  `keys.PutClockCleared` makes, in the transaction that stores the result, and
  only after the commit does the loop forget the pause. A request that finds no
  pause writes no clock record and stores its result. The same step makes every
  periodic task due at once: they were scheduled from readings of a clock that was
  wrong, and a clock that was ahead would leave them due days from the clock the
  owner accepted. The entries of the other kinds are not moved (see the known gaps
  of the reference).
- **A retired identity signs nothing, from the commit on.** `RetireIdentityOp` keeps the
  statement the keys verified at load as a verified revocation under `config/`, and
  `canSign` then refuses with `ReasonIdentityRetired`, before the daemon is down. The
  operation asks for the stop (`StoredResult.Stop`), also on a resend. The same
  judgement (`SignRefusalOf`: the identity first, then the clock) is read by the
  capacity check, which then refuses new work with `ReasonIdentityRetired` so that
  nothing is acknowledged that no start will ever send, and handed to the periodic
  tasks as `TaskInput.SignRefusal`, so that a task that signs does not run on a
  retired node while the loop is still up. Deliveries that are not settled at the
  retirement are abandoned, as the retirement means.
- **Reminders are derived, never stored.** The reminder that the backup of the
  self-revocation is not confirmed is true while the confirmation on disk is not
  of the statement this node holds (`config.Backup.Confirms`); no record stands for it,
  so none can outlast the fact or be acknowledged away.
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
- **The runtime endpoint of an instance is made by its admission, once.** An
  adapter that declared a runtime gets one runtime endpoint, whose id is minted the
  first time and kept in `config/` for ever, in the same transaction
  (`runtime-admit`) as the record. An open endpoint is not written again; a closed
  one is opened again with a new token, which is a new place in the cap; both count
  against `publication.MaxEndpointsPerNode` like a direct endpoint, and a refusal
  leaves the instance not admitted. Its token is never shown to anyone: a runtime
  endpoint is no caller. A closed runtime endpoint whose queued records are still
  being closed is not opened again: the admission fails, the host ends the
  connection and starts the instance again after its backoff.
- **What `adapter.start` carries is built from the engine's view and the
  configuration, and is valid.** The node, the runtime endpoint (only for a runtime),
  the instance's configuration values, the presets (only for a runtime) each with
  the values it holds for the adapter, the open direct endpoints by key, and the
  open sessions with the native session each maps to, its tool access (token and data
  directory) and the state the adapter kept. Every list is present and sorted.
- **An instance is online from `adapter.start` to the end of the connection, and
  offline otherwise.** Gone sets the time to the engine's clock unless the instance
  was not online, in which case the earlier time stays (zero for an instance that
  has not been online since the engine started). Going away keeps the endpoints,
  records and admission, and makes the busy states unknown. The host reports
  nothing when it stops an instance, so the caller that disables one marks it
  offline itself with `AdapterGone`, which takes only an enabled instance.
- **A new connection starts from what it reports.** A successful admission forgets
  the busy states and the availability snapshot of the connection before, and
  `AdapterStarted` replaces the busy states with the sessions its result lists as
  `turn_active` instead of merging into the old ones.
- **Availability, busy and rejected states are memory.** A snapshot replaces the
  last one and is stamped with the engine's time of receiving it; a turn started or
  ended and a refused native request are kept by endpoint reference, whether or not
  the endpoint is known (a session may report a turn before its creation has
  returned), and write nothing. A notification whose parameters are of another type
  is an error.
- **A probe token is made for each preset and front end, kept in memory, valid
  for `ProbeTokenLifetime`.** A new call replaces the earlier tokens of the
  instance. A token is never written, logged or returned by a read.
- **A runtime outside the tested ranges is one alert, once.** `RuntimeVersionUntested`
  judges it against the installed manifest; the alert of kind `version_untested`
  is not raised again for the same instance and words, which alerts being on disk
  makes true across restarts.
- **A checkpoint is on disk before it is answered.** `CheckpointPut` is taken in
  every phase the loop runs in, stores the state compact, one value for each
  (scope, id), and refuses a session scope that is not an open session endpoint of
  the instance.
  An attempt scope needs an in-flight creation or prompt attempt of a session of
  that instance; an unknown, foreign or settled attempt is `ReasonAttemptUnknown`.
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
  has the page handed to the waiter after it, in a command of its own
  (`serveReady`). The deadline is an entry of the schedule on the injected
  clock. An endpoint has at most one waiter, and a newer recv, waiting or not, ends
  the older with an empty page, which the waiter made when it was registered.
  Shutdown ends every wait with an empty page, before the interfaces stop; the
  waiting side watches the shutdown itself, because the command that ends the waits
  is dropped when the inbox is full.
- **A direct endpoint is told with `endpoint.pending` after the command that made a message
  ready, once for each endpoint, and is not repeated.** `notePending` marks the endpoint where the
  view queues the message, and `servePending` runs after `serveReady`, so a message that a
  waiting recv took is not announced. The notification is best effort: it is neither recorded nor
  repeated.
- **A recv judges again, hands out only what it reaches, and writes before it answers.**
  The second check is made at the hand-out with the declarations as they are then; a
  refused message the page does not reach is left for the next recv; the leases and
  the closes of a page are one transaction, and an agent that gets no answer holds no
  page and finds its messages leased, to be handed out again as possible duplicates.
  Bodies are read through `readBody`, which settles one that is wrong or gone. An
  acknowledgement is of a lease the endpoint was handed, and a lease of another
  endpoint is unknown.
- **The capacity check is in one place.** `checkCapacity`, which only the engine's
  own `admitNewWork` calls, judges the identity, then the clock (both by
  `SignRefusalOf`), then
  the queue, then the disk, and a bound that is not set refuses. The engine feeds
  it with the settings of the node, the free space and its own count of unfinished
  deliveries: target records that still need their body (accepted, queued or
  leased), accepted control records, and source deliveries no receipt answered;
  a delivery whose outcome is unknown holds no body and is not counted. System feedback, status reports, control
  replies and the local target side of a delivery do not go through it.
- **The archive sweep is a pass of commands that queue each other.** `beginSweep` (the
  periodic task) scans and plans with `store.PlanSweep` in the loop, which stalls the loop
  once for the whole scan (about 3.2 s at 50,000 files, 11 to 13 s at 200,000, warm; callers
  meanwhile may be told `loop_busy`, which they retry); the commands that follow stall it for
  one bounded step at a time. They remove the archive files past the retention
  (`store.ExpiredArchiveFiles` of the same scan, with `config.NodeSettings.ArchiveRetention`,
  the only reading of it), append the summaries, both in slices of `Params.ChunkRemoves`,
  then remove chunks of at most that many files cut from `SweepPlan.Chunk` in the store's
  order, and take what each chunk removed out of the view (`forgetSwept`: the view clears
  the schedule entry of the records it drops, forgets a session endpoint's pairs in the
  sequence book), the queue of feedback, the endpoints and the outbox (`forgetOutbound`).
  Expiring the archive and the summaries are best effort: a failure is logged and the
  removals go on. Before each chunk the endpoints the plan has yet to remove are looked at,
  and a pass that finds one open again ends (`planIsStale`): the next one plans from a new
  scan. Nothing is swept while the clock is in doubt.
- **Recovery is a plan, then rules.** `PlanRecovery` is a pure function of a scan.
  `Open` runs the rules in the order the store's README fixes, works the plan out
  again from a fresh scan after a rule has written, and repeats until nothing is
  left. The replay of
  feedback is wired: it takes off the source and out-lost records the items a
  notice already covers, and the scan entry loads the queue of feedback only after
  it has run.
- **The recovery of deliveries is four phases in the order of the startup
  steps,** each a command of the loop that logs `startup step N begin` and
  `startup step N end` (`startup step N failed` with the error when it returns one); each is empty until the feature it belongs to is added,
  except the eleventh, which settles the session creations whose time is up and asks the
  adapter again for the others, and does the same for the closes and interrupts (the
  prompts of that step are not there yet), the twelfth,
  which ticks every record that is due, and the thirteenth, which sends the feedback that
  fell due.
- **The passing of time is read from the records, by a schedule the view keeps.** Each
  target record, control record and source record has an entry at its next time-driven change
  (`Record.NextTick`); `view.onTick` moves it whenever a record is taken in or dropped.
  Entries that fall due are ticked together, in chunks of `Params.ChunkTickRecords`,
  in one `tick` transaction with the feedback the changes call for. Nothing is ticked
  and nothing is judged expired while the wall clock is in doubt. A creation's own time
  is another entry (`CreateReplyDeadline`), which the engine settles long before the
  settle deadline of its control record. A source delivery that is queued or accepted when
  `report_by` passes is not closed by the tick before the outbound worker has pulled its
  target one more time (`finalPullPending`): at the start step 12 leaves it open and step
  14 makes the pull; the close then tells what the pull could not settle. No such pull is
  made while the wall clock is in doubt (`finalPullDue`), and the tick keeps the record's
  entry at the end of the target's retention, so a pull that never comes does not keep the
  delivery open.
- **The recovery steps own the entries that fell due while the node was down.** While the
  phase is `recovering` the schedule gives out no entry of the kinds `schedTick`,
  `schedFeedback` and `schedOutbound` (`recoveryKind`) and does not arm the timer for
  them: step 12 takes the due ticks (`takeDueOf`), steps 13 and 14 do the rest, and
  `Serve` wakes the loop for what is left. Anything that makes the schedule give out one
  of these kinds earlier ticks a record before the adapters have reported turn state.
- **A damaged file is settled first, one at a time, and a lost source record is replaced
  by the sender's.** `quarantineAll` settles the damaged files of the plan, a control record's
  included (the pull answers from `ctl/`, so a record that goes is one the node no longer
  holds), each with a scan taken after the one before, giving the store the scan and the
  node; a source record whose first line can be trusted gets the lost record
  (`lostOut`) and a file lost unread the out-lost record the store writes, whose item the
  queue of feedback loads. A body that is not what its record says, or is gone (`readBody`, which
  takes the record that owns it) settles that one record, and a notice among them is told
  again from what it covered (`reissueCovered`); a write that meets a damaged
  floor file (`retryingDamaged`) has it settled and is tried again, and the book of
  sequence numbers is built again from the disk. A damaged file of any other
  directory is not settled while the node runs: `settleLive` refuses it and the write
  fails, with a line in the log.
- **A query that was answered and found no receipt expires a delivery; a call that was
  not answered is a failing contact.** Both are read in the one place the answers are
  read (`applyAnswer`, `outResult`) and applied through
  `envelope.Outbound.ApplyObservation`, so the rules stay in the delivery package.
- **Feedback is queued by the one place that takes a source record into the view,
  and told by one notice per merged group.** `putOut` replaces a source record in the
  view and queues the items the record holds that the one it replaces did not, so a
  decision made by any write (a report, a reply's evidence, a pull's answer) is told
  and an item that was held already is not queued twice. The queue
  (`delivery.FeedbackQueue`) is memory: the scan entry builds it from the items the
  records hold and the notices already on disk (for the rate cap), so a restart
  neither loses an item nor gives an endpoint a new rate window. An entry of the
  schedule is due when the queue next has a notice to send; the flush works on a copy
  of the queue and a notice that could not be written goes back with its items and
  without the mark of having been sent.
- **A notice is a record of the system endpoint, and its identifier follows from its
  items.** It is written by `FeedbackForm` as exempt traffic, so a full queue of the
  endpoint does not refuse it and it takes no place in it, and it is not asked of the
  capacity check; its key is made from the items alone (`noticeKey`), so one formed
  again is the same record. It covers the items by the name of the record that holds
  each, and only then are the covered items taken off their records (`clearCovered`):
  a write of that which failed is tried again after `RetryBackoff` (`retryClears`),
  and a crash between the two is repaired by the replay of feedback. Feedback for an
  endpoint that is not open is dropped, with a word in the log, and taken off its
  records: closing ends the endpoint's life for feedback.
- **A message is accepted once, in one place.** `accept` makes the request into its
  envelope, whose errors are what refuses a request no node would take, answers a
  repeat of an idempotency key with the first message (the same content for the same
  recipients) or a conflict, asks the one capacity check, takes the next sequence number of the pair
  from the store's book, and writes the body and the source record in one
  transaction; memory and the book follow the disk, and only then is the message given
  to the outbound worker. A message is refused with nothing recorded when the queue is
  full, the disk has no room, the wall clock cannot be trusted, or the request is not
  one a node takes (no body, a body over the protocol's limit, a deadline beyond the
  longest, no recipient or the system endpoint as one).
- **One worker sends, in order, outside the loop.** The loop schedules and applies;
  the worker calls the transport in the order the work was given and waits until the
  loop has applied each result before the next call. A send that got no answer is made
  again after `ResendBackoff`, until the delivery deadline, to each node that has not
  answered (a node that answered does not make the others' sends unnecessary); a send
  again is made anew from the record, so its evidence is signed anew while the request
  digest, and so the request, is the same. A delivery that is not settled is asked
  about every `PullInterval` until its report deadline, which already holds the
  tolerance of the clock (a pull names each delivery of the record on that node that
  is not settled, and the target answers for those only; a derived delivery, which only a
  control signal's record has, is asked about like the others, since the worker carries a
  control signal as it carries a message), and after the delivery deadline a node that never
  answered is asked, not sent to. The evidence of a reply is a signature: while the wall clock
  cannot be trusted the send is put off. Nothing about the sending is persisted: after a restart every unsettled
  source record is looked at again (the start's last step), and the target answers a
  send it already took in from its record. A worker waiting out a delay that the
  transport was asked to make is not work for `Quiesce`.
- **What a pull brings back is told apart by what it holds.** A call that returns an
  error got no answer: it is made again after `ResendBackoff`. On a successful answer
  each report is verified and applied, and each recipient in `NoRecord` is applied by
  the one rule of `envelope.Outbound.ApplyNoRecord` (an accepted delivery before its
  report deadline is lost at the target, the sender is told once, and every other
  state ignores it); a recipient that is in neither list changes nothing and is
  asked about again. The source does not send the message again on `NoRecord`: it
  holds a receipt, so the message is not in doubt, only its record at the target.
  An answer about a recipient the record does not track is dropped, and one about a
  delivery that is on another node than the answering one is logged as the peer's
  fault, as is a `NoRecord` for a recipient the same answer reports (the report
  stands, the listing is left out). Reports and `NoRecord` take the one path that writes the record and its
  feedback (`applyAnswer`).
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
- **A pull is answered for the recipients it names, from the records the target
  holds.** For each recipient the pull names, the target gives the signed report of
  its record, or lists the recipient in `NoRecord` when it holds none for the
  message from the asking node; a recipient not named is not answered, and one named
  twice is answered once. Every record the target holds can be signed, so a held
  record is never missing from both lists. The whole answer is refused while the node
  may not sign, and a pull that names no recipient is an invalid argument.
- **A report is dated by the record.** The target record keeps the moment it entered
  its state (`Record.StateAt`), and a report is signed with it as `reported_at` and,
  for a delivered record, as `delivered_at` (`Record.DeliveredAt`); the engine keeps no
  time of its own for any state, so every state has a report and a pull for an
  unchanged state repeats the same time.
- **The source record keeps whether the recipients are disclosed.** A send again
  rebuilds the envelope from the record, and the request digest the target compares
  covers the choice, so the flag is stored (`OutEnvelope.DiscloseRecipients`); whether
  the message has several recipients is `len(To) > 1`.
- **The body is held by what still needs it.** The source record drops its reference
  when no delivery of it needs the body, in the write of the receipt that settled it; a
  rejected delivery holds no body at all. Every record has a body of its own, so a
  write that drops a reference only has to write the record without it: the store
  removes that record's own body in the same transaction, and the engine keeps no
  count of who refers to what. One message to several local recipients is stored as a
  copy for each target record that holds the body.
- **Evidence that does not verify is discarded and alerted.** The reply is judged as if
  it carried none, and an alert of the kind `evidence_error` names the message.
- **A control signal is sent as a message is.** Its source record is sent and asked about by
  the same worker, and the control reply the receipt or the answer to a pull carries is applied
  first, by the envelope package's one rule, and written in the transaction `control-reply`.
- **A control signal is taken in by one order, and one that cannot be is rejected.** A
  repeat is answered from the record, an unusable endpoint or an unallowed signal gets a
  control record born rejected with the reason and a receipt that carries its report and no
  control reply (`delivery.NewRejectedTargetControlRecord`; an accepted record is never
  rejected afterwards), and what the node answers itself (a status, a creation it refuses for
  the preset, the adapter or the node endpoint cap) is answered in the transaction that accepts
  the signal. The cap gives a failed creation with `node_endpoint_limit`, no session endpoint
  or initial-message delivery, and its stored reply is kept even after room becomes available.
- **A creation's intent is on disk before the adapter is called, and is settled in one
  transaction.** The call is made again with the same attempt until the reply deadline; then
  the creation is unconfirmed, its endpoint is closed and the owner is alerted. The reply of a
  creation reaches its source by the source's pull.
- **The initial message of a creation is a delivery of its own, written in the intake.**
  A creation that goes on to the adapter writes, in `control-intake`, the delivery of the
  message to the session endpoint it allocated (`initial.go`: `derivedDelivery`, the one
  place it is made, for the intake and for the rule that writes it again after a crash),
  and the sender's record gets the matching delivery from the one rule that takes in a
  control reply (`envelope.HandleControlReply`), whether the reply comes by the pull or as
  the evidence of the session's first reply, which carries the creation's signed control
  reply before the signed report (`evidenceFor`). The delivery is judged as the creation
  was: it is let in because the creation was, and when its body is handed out the
  creation is judged again from the request it remembers (`derivedHandoutAuthorization`).
  A creation the node refuses itself, or one that fails, holds no delivery that is ever
  read: the reply says the message did not arrive. A creation whose initial message is lost
  while it is not settled (the body does not read, or the delivery is gone) is failed with
  `control_unknown`, and one whose endpoint was damaged likewise (`failCreationWithoutMessage`,
  `failCreationWithoutEndpoint`), so that nothing is made of a creation whose first message
  is gone, and the owner is told that a session may remain.
- **A close or an interrupt is carried out when it is accepted, not queued.** A signal
  the endpoint does not take (`Admission.AcceptsSignal`) is answered as unsupported at
  once; one it takes is accepted and the adapter is called by a worker the engine owns
  (`execute.go`). The control record is not in the queue recv hands out of, so a signal
  reaches a session whose queue is full and whose turn runs. The result is a command of
  the loop that settles the signal in one transaction: the reply, the delivery's end and,
  for a close, the session endpoint closed with its token gone, whatever the outcome
  (the channel closes whatever the adapter did). A call that failed, or an adapter that is
  not connected, is tried again until `Params.ControlReplyDeadline`; then a close is
  told as unconfirmed and an interrupt as failed. An interrupt is told failed at once
  when the adapter is not connected. An interrupt the adapter says it asked the runtime
  for waits `Params.InterruptConfirmWait` for the end of the turn and is told stopped if
  it ended and requested if it did not. A start goes on with the signals that were not
  settled.
- **The state kept for a session goes when its endpoint closes.** Every place the engine
  records an endpoint (`putEndpoint`) queues the removal of the checkpoint of a session
  endpoint that has just been closed, whichever way it was closed, and the scan entry
  removes one a crash left (`checkpoint-drop`).
- **No method makes a session endpoint.** The engine has no method that takes or gives the
  record of one; a session endpoint exists only through a session creation, which `Control`
  asks for and the taking in of the signal makes.

## What the caller must guarantee

- Call `txn.Writer.Recover` and make the directories of `state/` exist before
  `Open`; load the configuration after `Recover`. `Open` reads what a power loss
  cannot take back only because the transaction layer settled it.
- Pass the persisted high water mark and the persisted pause in `Deps`, and add
  the task that persists them with `AddClockState`: it writes `TaskInput.HighWater`,
  `TaskInput.RollbackPaused` and `TaskInput.PausedSince` (pass the persisted start in
  `Deps.RollbackPausedSince`; the loop is the only keeper of the time), and it is the loop that runs it at once when the
  pause begins, so the pause is on disk before the command that found it goes on.
- A task that signs reads `TaskInput.SignRefusal` and nothing else to decide
  whether it may; the start, which has no engine yet, asks `SignRefusalOf` the same.
- Call `AddPeriodic` and `AddArchiveSweep` before `Start`, `RecoverDelivery` between `Start` and
  `Serve`, and `BeginShutdown` before `Stop`.
- Admit an adapter instance with `AdapterAdmitted` before its endpoints register,
  and only an instance the configuration has enabled; call the methods of one
  connection in the order the table gives, and notifications in the order the
  adapter sent them. `AdapterGone` may be called for any enabled instance, admitted
  or not.
- Give `Deps.OpenPackage` when packages may be installed: it opens the directory the
  owner names as a reader rooted there, and is the one place the engine reaches a
  directory that is not the node's. `Deps.Rand` is read in the loop only; the staging
  name's randomness is drawn there and handed to the worker.
- Set `Deps.Home` to the absolute data directory when adapters are probed:
  `ProbeTokens` fails without it. Set `Deps.Adapters` when `Quiesce` is asked about
  adapters; `Counts` must read counters and never wait, because it is called in the
  loop.
- Give `Deps.Config` the methods of the `Config` interface: the engine reads the
  presets, the installed adapters and the stable endpoint ids from it.
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
`operations_adapters_test.go` the installation, enabling and disabling of adapters, the presets, the probe and the crash points of each;
`adapters_test.go` the life of a connection (admission and the runtime endpoint, what
`adapter.start` carries, probe tokens, availability and signals, notifications,
checkpoints, and `Quiesce` against a test double of the host);
`powerloss_test.go` the run that cuts the power in the middle of the property of closing (`lease_model_test.go`) and judges every disk the cuts leave, with the coverage it claims over the classes of transaction and of step; `rclean_test.go` the check that a node that never crashed has no recovery work after any step of its life or after a clean restart; `crash_test.go` the crash walks of `direct-register`, `runtime-admit` and `checkpoint-put` and `crashDisks`, the two crashes every walk of the engine takes at each point (the process crash and the worst power loss; random power losses are drawn only by `TestPowerLossKeepsWhatTheEngineAcknowledged`); `clock_test.go` the pause of a wall clock set back until the owner clears it; `operations_test.go`, `operations_effects_test.go` and `operations_crash_test.go` the owner's operations, their effects and their crash points; `nodestatus_test.go` the status and the export; `intake_test.go` the scenarios of
sending and intake judged from the ledger, the answers of the loopback and the records, and
`intake_more_test.go` the rules around them; `intake_crash_test.go` the crash walk of the
transactions that take a message in; `intake_fuzz_test.go` the receiving entry under
arbitrary bytes; `intake_internal_test.go` the decision of what to send and the indexes of the view; `static_test.go` the lists
the review holds the engine to; the `_internal_test.go` files the encoding of
the declaration field (`declaration_internal_test.go`), the view and the batches of a drain.
`session_test.go` holds the tests of the creation of a session and of `endpoint.status`,
judged from the ledger of the fakes, the records and the status of the message;
`session_recovery_test.go` the control record settled by time, the initial message or the endpoint
lost from a creation, what
`adapter.start` carries, and a creation cut off by a crash or overdue at the start;
`session_crash_test.go` the crash walk of the transactions that make a session, with and
without an initial message, and of a close;
`session_signals_test.go` the initial message, the interrupt and the close of a session, and
what the node answers without the adapter;
`quarantine_test.go` the damaged control record and the endpoints it was creating;
`pull_test.go` the scenarios of recv and ack that the floor names (a delivery that goes well,
each anomaly, a message never acknowledged, long messages, the token of another endpoint),
`pull_more_test.go` the second check, a body that is wrong or gone, the lease across a
restart, the parts of a message and the waits, `pull_notice_test.go` a notice whose body
is damaged, and `pull_crash_test.go` the crash walk of the writes of a recv and an ack.

`sweep_test.go` the sweep as the node runs it over a node of the harness (the periodic
task, chunks, a summary that cannot be appended, a notice that is kept for the item it
covers, an endpoint registered again between two chunks, a clock in doubt, the archive
files past the retention, a source record that waits for its last pull) and
`sweep_internal_test.go` the cutting of a plan, the plan that an endpoint open again makes
stale and what leaves the view and the outbox;
`feedback_test.go` has the notices (once, within the window and the cap, at the start,
for a closed endpoint, a full queue, and the crash walk of the writes that tell an
endpoint) and `feedback_helpers_test.go` reads them from the disk;
`quarantine_scenarios_test.go` what a loss of a record or a body tells the sender and
the owner, what a running node settles and what it leaves for the start, and the crash
walk of its settlement;
`feedback_internal_test.go` the key of a notice and the putting back of one that
could not be written; `expiry_test.go` what the passing of time does to a delivery
(backlog expiry, the expiry of a delivery with no receipt, failing contact, closing
after `report_by`) and the crash walk of the `tick` transaction; `finalpull_test.go` the
pull before a source delivery closes at `report_by`: what a node that slept past it learns,
what the sender is told when it cannot learn, and the window it stops asking in.
