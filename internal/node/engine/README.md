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
back, and the receiving side that judges an envelope and signs the reports.
Prompts, pulling, control signals and feedback join the same loop as further
commands and add their methods to this list.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Open(ctx, Deps, Params)` | The scan entry: scans `state/`, runs the recovery rules until none has work, builds the view. Returns a `ScanReport`. |
| `Start`, `RecoverDelivery`, `Serve`, `BeginShutdown`, `Stop`, `Done`, `Err`, `Phase` | The life of the loop: opened, recovering, running, stopping, stopped or failed. `RecoverDelivery` is the second recovery entry; the table of the start steps is in the reference. |
| `AddPeriodic`, `AddClockState`, `Task`, `TaskInput`, `SignRefusalOf` | A task the composition root has the loop run every period, before `Start`. The clock state's task is also run at once by the command that finds the wall clock set back. A task is told whether the node may sign (`TaskInput.SignRefusal`, from `SignRefusalOf`, the one judgement of signing). |
| `Quiesce`, `Expect`, `AdapterExpect`, `Report` | Returns when no command waits, nothing is due, no goroutine of the engine runs and every adapter named in `Expect.Adapters` is quiet by the host's own counts. |
| `AdapterAdmitted`, `AdapterStarted`, `ProbeTokens`, `AdapterProbed`, `AdapterGone`, `AdapterNotified` | What the adapter host calls for the life of a connection, in that order: the admission of an instance (which makes its runtime endpoint and returns what `adapter.start` carries), what the adapter answered to `adapter.start` and `adapter.probe`, the probe tokens, the end of the connection, and the notifications. `*Engine` satisfies `adapterhost.Engine`. |
| `RegisterDirect`, `UnregisterDirect`, `CheckpointPut` | What the adapter host calls to answer the requests of an adapter: the registration and unregistration of its direct endpoints, and the state it asks the node to keep. |
| `Adapter`, `AdapterView`, `AdapterSignals`, `ProbeTokenView` | Reads of what the engine knows of an instance (admission, online or offline since when, probe result, availability, busy turns), and the availability as `handoff.Signals` for one endpoint. |
| `ProbeTokenEnv`, `HomeEnv`, `ProbeTokenLifetime` | The names and the lifetime of the probe tokens; the Agent interface refers to them. |
| `Send`, `Reply`, `SendRequest`, `ReplyRequest`, `SendResult`, `DefaultContentType` | Accept a message from the endpoint a caller is: the body and the source record are on disk before the call returns, and what the recipients' nodes make of it is learned from the state of the deliveries. |
| `MessageStatus` | The state of each delivery of a message the caller's endpoint sent, from the sender's record. Another endpoint's message is unknown to the caller. The reply of a control signal is not part of it yet. |
| `SetTransport`, `Transport`, `Keys` | The transport the outbound worker calls (the narrow interface with the signatures of `*transport.Loopback`) and the node's keys, which sign the reports. Both are for the composition root; an engine without them refuses to send and to take in. |
| `Inbound`, `Pull` | The engine as the `transport.Handler`: an envelope for this node, answered with signed reports, and a pull for the reports of a message to the recipients it names: for each, the signed report or the recipient in `NoRecord`. |
| `Declare`, `Caller`, `ByToken`, `ByAdapter` | The declaration of an endpoint, by whoever the endpoint's own caller is, resolved in the command. |
| `Endpoints`, `CloseEndpoint`, `CloseRequest`, `CloseResult`, `Alerts`, `RaiseAlert` | The engine side of `endpoint list` and of a close the engine's own features ask for, and the alerts. The numbers of the status are `Status`'s. |
| `Execute`, `ManagementCommand`, `StoredResult`, `Operation`, `CloseEndpointOp`, `StopDaemonOp` | The one door of every administrative operation of the owner: the replay check, the operation, and the storing of its result with the fingerprint are one command of the loop. The owner's request to close an endpoint and to stop the daemon are the two operations there are; the package that owns the effect of another adds its type here. |
| `ClearClockRollbackOp`, `ConfirmRevocationBackupOp`, `RetireIdentityOp`, `AcknowledgeAlertOp`, `SetNodeSettingsOp`, `SetEndpointSettingsOp` | The owner's operations on the clock, the identity, the alerts and the settings, each an `Operation` with the effect in the loop and its result made by a function the interface passes in (`ClockClear`, `BackupConfirmed`, `EndpointChange` say what was done). |
| `InstallAdapterOp`, `EnableAdapterOp`, `DisableAdapterOp`, `AddPresetOp`, `RemovePresetOp` | The owner's operations on adapters and presets, each an `Operation` with its result made by a function the interface passes in. An installation has work outside the loop (see the rules); enabling asks the host to run the instance, disabling asks it to stop and closes the instance's endpoints. |
| `AdapterList`, `Presets` | Reads of the installed adapters, the instances with the probe of each enabled one, and the presets with the version a change of them names. |
| `PresentProbeToken`, `ProbeTarget`, `ProbeView`, `ProbeState` | What a front end calls to prove it reaches the node, and what the engine says of the probe of an instance. Not part of `AgentService` yet: the Agent interface adds it when it calls it. |
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
  pause writes no clock record and stores its result.
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
  sessions, empty until sessions can be created. Every list is present and sorted.
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
  own `admitNewWork` calls, judges the identity, then the clock (both by
  `SignRefusalOf`), then
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
  control signal's record has, is not asked about, as the worker takes no control record), and after the delivery deadline a node that never answered is
  asked, not sent to. The evidence of a reply is a signature: while the wall clock
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
- A task that signs reads `TaskInput.SignRefusal` and nothing else to decide
  whether it may; the start, which has no engine yet, asks `SignRefusalOf` the same.
- Call `AddPeriodic` before `Start`, `RecoverDelivery` between `Start` and
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
`crash_test.go` the crash walks of `direct-register`, `runtime-admit` and `checkpoint-put` and `crashDisks`, the two crashes every walk of the engine takes at each point (the process crash and the worst power loss; random power losses are drawn only by `TestPowerLossKeepsWhatTheEngineAcknowledged`); `clock_test.go` the pause of a wall clock set back until the owner clears it; `operations_test.go`, `operations_effects_test.go` and `operations_crash_test.go` the owner's operations, their effects and their crash points; `nodestatus_test.go` the status and the export; `intake_test.go` the scenarios of
sending and intake judged from the ledger, the answers of the loopback and the records, and
`intake_more_test.go` the rules around them; `intake_crash_test.go` the crash walk of the
transactions that take a message in; `intake_fuzz_test.go` the receiving entry under
arbitrary bytes; `intake_internal_test.go` the decision of what to send and the indexes of the view; `static_test.go` the lists
the review holds the engine to; the `_internal_test.go` files the encoding of
the declaration field (`declaration_internal_test.go`), the view and the batches of a drain.
`TestQuarantineClosesEveryCreatingSessionEndpointOfTheDamagedControlRecord` is a
requirement kept in sight and skipped until the quarantine is wired.
