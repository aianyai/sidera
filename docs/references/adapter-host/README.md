# Adapter host

How the daemon keeps adapters running and carries the adapter protocol between
them and the engine: supervision, taking an adapter on, the pings that judge it,
the backoff before it is started again, what is let through and when, the buffer
that keeps notifications in order, the error contract with the engine, the counts
the tests ask, and the end of the host. Code:
[`internal/node/adapterhost`](../../../internal/node/adapterhost/README.md). The
protocol itself is in [adapter-protocol](../adapter-protocol/README.md); the
declarations the host applies are `internal/adapterreg`'s; the engine's side of the
seam is in [engine](../engine/README.md).

## What

### One supervisor per instance

Each enabled instance has one goroutine that launches the adapter, takes it on,
keeps it in service and starts it again after a backoff, until the instance is
stopped or the host ends. The engine asks for it with `EnsureRunning` and ends it
with `Stop`; `Start` begins the supervision of every instance enabled in the
configuration. The supervisor builds what the launcher is told to start from the
configuration as it is at that pass: the installed adapter of the instance, the
directory of its installed version, the lock path and a writer onto
`adapter-<instance>.log`.

The adapter's standard error goes to that log, one line at a time (a log takes one
line per write and rotates between writes, so the bytes of a pipe are cut into
lines); a log that cannot be opened is a warning and the adapter runs with its
standard error discarded. The protocol never goes there: it is the other stream.

### Taking an adapter on

In this order, each call to the adapter limited by `Params.HandshakeTimeout` on the
host's clock:

1. `initialize`: the protocol range the daemon supports, its version, the instance
   id and the lock path, and no capabilities of its own. The answer is decoded
   strictly, checked against the installed manifest (`adapterreg.CheckHandshake`) and
   admitted (`adapterreg.Admit`). An answer `1011 instance_locked` ends the pass and
   is logged as another process holding the lock.
2. `Engine.AdapterAdmitted`, which returns the parameters of `adapter.start`. Only
   now are the adapter's requests let through (before it they are `-32003`, except
   `ping`), and only now does the forwarder start.
3. `adapter.start`, then `Engine.AdapterStarted`. The connection is in service:
   calls from the engine are possible and whoever waits in `Start` for this instance
   is released.
4. `Engine.ProbeTokens`, `adapter.probe`, `Engine.AdapterProbed`.

Any failure before step 3 was answered kills the process and is reported to the
engine as `GoneStartFailed`: the process did not start, its answers were wrong, it
was refused admission, it held no lock or the engine could not take it. A probe the
adapter fails (an error, a result that is not valid) is logged and nothing more.

### Supervision and the two-ping rule

In service, every `PingInterval` the host sends `ping`, limited by `PingTimeout`.
Any error is a failure. `OfflineAfterPingFailures` (two) failures in a row make the
adapter offline: the forwarding of the connection is ended (the connection's context
ends, the stream closes and what is still buffered is dropped), the engine is told
`GonePingsFailed`, and then the process is killed and waited for at most
`ShutdownWait`, and the host goes on whether it ended or not. The report does not wait
for the process, and no notification reaches the engine after it: a notification that
arrived after the report would find the instance offline and mark it busy. The requests
of the adapter that were being answered make no such promise and may complete after
the report, as they may after a connection that ended by itself. A
process that `Kill` reports it cannot end (the operating system refuses the signal)
is not waited for at all: there is nothing the wait could see. The rule is one for
every kill the host decides on, including the kill of a process that was asked to shut
down and stayed after `ShutdownWait`.
An answer starts the count again; an answer that comes after the timeout is dropped
by the peer and counts for nothing. A connection that ends by itself (the stream
ends, the process exits) is reported as `GoneConnectionEnded`, after the forwarder
has handed the engine every notification that was already buffered.

### Backoff

The wait before the next pass starts at `RestartBackoffMin`, doubles after every
failed pass or lost connection and stops at `RestartBackoffMax`. A connection that
stayed in service for `StableAfter` makes the next wait begin at the minimum again.
`Stop`, `Shutdown` and `Abort` end the wait.

### The lock

The adapter takes `adapter-<instance>.lock` (`LockPath`) before it answers
`initialize` and answers `1011` when it cannot. The host never relies on killing a
process to free it: a process that cannot be ended is left after `ShutdownWait`, its
replacement is refused by the lock until it dies, and the pass fails and waits for
the backoff like any other.

### Requests of the adapter

Each is answered exactly once through the peer. `ping` always. Before admission
`-32003`; once shutdown began `-32002`. Then `adapterreg.Admission.Check` decides,
with the kind of endpoint the method belongs to: the `endpoint.*` methods are asked
as direct, `recv.observed` as session, `checkpoint.put` as runtime when the adapter
declared a runtime and as direct otherwise. A method only the daemon may call is
`-32601`, as `Check` says; the host lists none of them. The host never resolves an
endpoint or its kind: the engine does and refuses with its own errors. Parameters
are decoded strictly: `-32602`, and for a `checkpoint.put` whose `state` is beyond
its limit `1006 checkpoint_too_large`. The engine answers `endpoint.register`,
`endpoint.unregister` and `checkpoint.put` itself; `endpoint.send`, `endpoint.recv`,
`endpoint.ack` and `endpoint.message_status` through `EndpointCalls`, and
`recv.observed` through `ObservedCalls`, when the engine given to `Start`
implements them, and as `-32601` when it does not.

### What an adapter is told when the engine refuses

`apiwire.AdapterEngine` puts the engine behind the host's interface and words the
engine's refusals for the four requests the engine answers itself (`endpoint.register`,
`endpoint.unregister`, `checkpoint.put` and, through `ObservedCalls`, `recv.observed`).
One row for each reason of the engine, the same table for all four; the host never maps,
it hands a `*peer.Error` on as it is.

| Reason of the engine | Code | Notes |
| -------------------- | ---- | ----- |
| endpoint unknown | `1001` unknown_endpoint | the endpoint does not exist or is not one of the instance |
| attempt unknown | `1004` unknown_attempt | the attempt is not in flight for a session of the instance; an unknown, settled or foreign attempt is indistinguishable |
| wrong endpoint kind | `1002` wrong_endpoint_kind | |
| endpoint limit | `1007` endpoint_limit | `data`: `{"limit":"instance"}` or `{"limit":"node"}`, so that an adapter can tell its own declared limit from the node's |
| invalid argument | `-32602` invalid_params | the field is in the message |
| shutting down | `-32002` shutting_down | |
| instance unknown, unauthenticated, operation identifier reused, storage fatal | `-32603` internal_error | nothing an adapter could act on; a fatal failure of the disk is a request whose outcome is not known, which is what `-32603` says |
| node queue full, storage full, storage unavailable, clock rollback, loop busy, recovering | `1012` node_busy | `data`: `{"reason":"<name>"}`, the engine's name for the reason; see below |

The production `apiwire.AdapterEngine` also implements `EndpointCalls`, so
`endpoint.send`, `endpoint.recv`, `endpoint.ack` and `endpoint.message_status`
reach the engine through this same host. Each operation binds `ByAdapter` to the
connection's instance and the requested endpoint, with ownership checked inside
its loop command. Direct sends use `SendDirect` to keep parsing, reply routing
and acceptance in that command. Receive pages are decoded from the engine's
JSON page into the protocol's typed result; no page fields are reconstructed.

For direct send intake, invalid address syntax is `1008` with `data.field` naming
`to[index]`; content and reply-rule refusals, idempotency conflicts and a send after
the node's identity was retired are `1009` with the engine's stable reason in
`data.reason` (`identity_retired` for the last: nothing is accepted in a retired
node's name, and the refusal has no effect). The existing back pressure,
ownership and lease codes remain `1012`, `1001` and `1010`. A status query for a
message the caller did not send, including an unknown id, always returns the
same fixed `-32602` response without data, and so does an `endpoint.recv` of a
`message_id` and `part` the endpoint holds nothing of (an unknown message is a
normal answer there, not an internal failure). Unknown effects and fatal storage
failures remain `-32603`. These method-specific mappings do not change the
previously reachable registration, checkpoint or observed-receive methods.
`TestDirectProtocolPreservesSendControlAndReplyFields` and
`TestDirectProtocolRefusalsLeaveMessagesAndAcknowledgementsUnchanged` (with
`TestMessageLookupErrorIsTheFixedAnswerForAnUnknownMessage` for the table) exercise
these paths through a real host and compare persisted message and acknowledgement
records after refusals. The binary acceptance lifecycle test repeats the real
send/receive/ack/status round trip after replacing the daemon generation.

The text of a refusal never carries what the engine says about its cause (a path, an
identifier): that is for the log. An error that is no refusal reaches the host
untouched and is `-32603` with a fixed text.

The six refusals that leave no effect and may be tried again later are `1012 node_busy`,
and the reason in its data is the engine's own name, never translated. Why the protocol
has a code of its own for them, and why the reason is an open word, is in the design
notes of the adapter protocol. The engine refuses `recovering` before the command runs
(there is no loop yet, or the phase is still recovery), so it too has no effect. A fatal
failure of the disk stays `-32603`: whether the request took effect is not known.
`TestBackPressureOfAnAdapterRequestIsNodeBusyWithItsReason` (`internal/node/apiwire`)
holds the mapping to the protocol's list of the reasons this version sends.

### Notifications and their buffer

A notification is decoded and validated; an invalid one is logged and dropped. It
goes into the connection's buffer (`Params.NotificationBuffer`) in the order it
arrived. One goroutine per connection takes them in order and calls
`Engine.AdapterNotified`, and a notification counts as handled when that call
returns. Those that arrive before the admission is complete wait in the buffer and
are delivered after it. A notification the declaration does not allow (checked when
the forwarder takes it, because the declaration is not known before) is logged and
dropped and counts as handled. The engine receives the parameters as the value of
their type. Notifications do not wait for a request the host has sent: the host's call
to the adapter is one thing and the stream of notifications another, so the first
`turn.started` of a session that is being created can be handed to the engine before the
result of the `session.create` that made it (the adapter-protocol reference says why the
engine takes it). The host does not reorder either.

When the buffer is full the handler blocks; the peer's own queue then fills and the
peer ends the connection, which the host reports as `GoneConnectionEnded` and
recovers from by starting again. Nothing is lost and nothing ends the connection
while the engine stalls for less than the buffer and the peer's queue hold together.

### The contract with the engine

An error of a method that answers a request of the adapter (`RegisterDirect`,
`UnregisterDirect`, `CheckpointPut` and the optional parts) is answered as it is when
it is, or wraps, a `*peer.Error`, and as `-32603` otherwise; the text of the second
is logged by the peer and never sent. A result that is not a valid result of its
method is `-32603` too. What an engine's refusal means on the wire is decided by
whoever puts the engine behind the interface, which is where its errors are turned
into codes. An error of any other method means the connection cannot be used: the
host ends it, reports it, and starts the instance again after the backoff.

The calls the engine makes on an adapter (`CreateSession`, `DeliverPrompt`,
`Reconcile`, `Interrupt`, `Close`, `NotifyPending`) run on the caller's goroutine,
bounded by its context. Their parameters are validated before anything is written
and their results are decoded strictly. A call to an instance with no connection
past `adapter.start` returns `ErrNotConnected` at once and sends nothing. An error
of a call that was sent comes back as the peer returned it, for the caller to judge
with the effect rule; a result that does not decode is an error that is not a
`*peer.Error`, which the effect rule counts as an unknown outcome.

### Counts and `Working`

`Counts(instance)` returns the notifications handled and buffered, the requests of
the adapter being answered, the methods of the requests the host sent that have no
answer (one entry each, a ping included: while one is out the host is parked
with its timeout armed, and a caller that found the host quiet would move the test
clock past it), whether a connection is in service and
whether the host is working. `Working` is true while the supervisor does anything of
its own and while a timer of the instance is due and not yet taken; the supervisor
answers from inside its park, so the call returns when it next parks. After the test
clock moved, a caller that sees it false knows the host has acted on everything the
move made due. After `Shutdown`, `Abort` and for an unknown instance everything is
zero. The counts are those of the latest connection until a new one replaces them.

### Shutdown and abort

`Shutdown` sends `shutdown` to each connection in service (limited by
`HandshakeTimeout`), closes the stream, waits for the process at most `ShutdownWait`
and kills it if it stays; it stops the supervisors and waits for every goroutine of
the host. It reports nothing to the engine, and the notifications still buffered are
dropped. Its context bounds it all: when it ends first, the rest is ended as `Abort`
ends it. `Abort` is for a crash or a fatal failure: it kills every process at once
and sends nothing, not even a cancellation of a request in flight. Both are
idempotent; after either, calls return `ErrNotConnected` and `Start` fails. `Stop`
does for one instance what `Shutdown` does for all, returns at once, and an
`EnsureRunning` that follows it waits for the old adapter to end before it launches
the next. Because `Stop` returns before the process has ended, `Stopping(instance)` says
whether one is still ending: it is true from `Stop` until the supervision has finished
(the adapter ended, or was killed after `ShutdownWait` and given that long again), and
false for an instance that runs, that waits to be started again or that is not
supervised. A caller that must not remove what the adapter runs from while a process may
hold it (the files of its version, which a running executable keeps open on Windows)
asks it first.
`Stop` reports nothing to the engine either, so the engine goes on showing the
instance as online until it is told otherwise: the caller that disables an instance
marks it offline itself, with `AdapterGone`, which the engine takes only for an
instance that is still enabled.

### The exec launcher

`ExecLauncher` starts the program `entry.program` of the manifest, relative to the
directory of the installed version, with the manifest's arguments and the process's
own environment plus `ExecLauncher.Env`. Standard error goes to the instance's
writer. Standard output is read through a pipe the launcher closes after the process
has been waited for, so every byte the program wrote is read before the stream ends;
reading the process's pipe directly and waiting early can drop output that was not
yet read. The process is not killed when the context given to `Launch` ends: the
host decides when. `Kill` is idempotent and does nothing once the process has
ended, waited for or not. On Windows `os.Process.Kill` reports such a process as an
error, which `Kill` takes as the end it is: `TerminateProcess` refuses a process
that has terminated with access denied (the daemon holds its child's handle with
every access right, so the refusal means nothing else), and after `Wait` package
`os` has released the handle and answers `EINVAL`. Without this an adapter that
had already ended when the host killed it would be logged as one that cannot be
ended and left without the wait for its end. Streams that a descendant of the program
holds open are closed two seconds after the program ends, so that `Wait` returns.
On Windows the program's file must have the extension of an executable (`.exe`):
`os/exec` runs a file that has one of the extensions in `PATHEXT` as it is, tries
those extensions in turn on a path that has none, and never starts a file without
one, so a package whose `entry.program` names `bin/adapter` must hold
`bin/adapter.exe` there, and one that names `bin/adapter.exe` runs that file.
Everything about Windows and Linux is pending verification by CI on main.

## Why

- **A request is counted before it is waited for.** A caller that finds the host
  idle reads the counts to learn whether the adapter is still working on something:
  a request is counted by the goroutine that starts it, before the supervisor parks.
  The supervisor itself answers the question whether it works, together with the
  counts, from inside the park it is in, so the two are of one moment: a request
  counted by a helper goroutine a moment after the supervisor parked, or counts read
  before the supervisor reached its wait, both showed an idle host with nothing
  outstanding while `initialize` was in flight. For the same reason a request stops
  being counted on the supervisor's goroutine, once it has taken the answer, and not
  on the helper that received it: between the two the answer is on its way to a
  supervisor parked with nothing due, and a caller that found the host quiet then
  moved the test clock before the next ping's interval was armed, so the move fired
  nothing.
- **One goroutine owns the timers, and blocks nowhere but in its park.** The
  supervisor is the only waiter on the host's timers, with the adapter's answers and
  every call of the engine on helper goroutines, the report that an adapter is gone
  included, and the forwarder and the helpers it must wait for are waited for in a
  park too. That is what lets `Working` be exact without a counter or a retry: the
  question for the counts is a blocking send to the supervisor, answered inside the
  park with "working" for a park that waits on work of its own (a call of the
  engine) and for an idle park with something due (a timer, an answer, the end of the
  connection, a stop), so a caller cannot see "idle" between the moment a timer's
  value was taken and the moment its work began. A flag set around the work would
  leave that gap. It also keeps the engine's loop, which asks for the counts, from
  waiting on a supervisor that waits on the engine. A fake clock fires only timers
  that exist, so a test must wait for the host to arm what it waits on, and `Working`
  is how it knows.
- **Timers on the injected clock, not elapsed time.** The runtime packages have no
  monotonic reading, so "stable for five minutes" is a timer started when the
  connection came into service, and a limit on a call is a timer too (a context with
  a timeout would be real time and not move with a test's clock). Every timer is
  stopped when its wait ends, so a test can see that none is left.
- **Two failed pings, not one.** One lost ping is a busy adapter; the cost of
  judging it offline is a restart of the adapter and the engine telling senders it
  is unavailable. Two in a row is one that does not answer. It is a rule of the host
  and not a setting.
- **The lock decides, not the kill.** A hung old process and its replacement must
  never control sessions at once, and no kill can promise that on every platform. The
  adapter takes the lock before it answers, so the host may leave a process it cannot
  end and go on; the replacement is refused with `1011` until the old one dies, and
  the refusal costs one backoff, never a tight loop.
- **Admission before anything of the adapter's is let through.** An adapter that has
  answered `initialize` has not yet been judged: its requests would reach an engine
  that does not know it. Until `AdapterAdmitted` returns they are `-32003`, as the
  protocol says for a connection that is not initialised.
- **Requests are not refused on the host's side.** `-32001` means the handler did not
  run, and a retry is safe because of it. A handler that returned `-32001` itself
  would break that promise. The handler waits on the engine instead, bounded by the
  request's context, and when the handlers and the peer's queue are full the peer
  refuses before anything runs. The adapter sees the same code either way.
- **Notifications are buffered, in order, and never dropped.** A turn that ends
  before it starts, or a prompt that is persisted before the attempt is known, would
  leave the engine with a wrong picture. The buffer keeps the order and bounds the
  memory; when the engine stalls beyond it, the peer's overflow closes the
  connection and the new `adapter.start` resynchronises everything, which is correct
  by construction. The bound is the buffer and the peer's queue together; the buffer is a
  parameter so that a node can size it to the stall it must survive.
- **The declaration of a notification is checked when it is taken.** The host does
  not know what the adapter declared until it is admitted, and notifications that
  arrive earlier must wait for it anyway.
- **The engine's errors are turned into codes in one place.** The host passes a
  `*peer.Error` through and calls everything else internal, so that the engine's
  texts, which can name paths or ids, never reach an adapter, and the meaning of a
  refusal is not decided in two places.
- **A failed probe is not a lost connection.** Whether an adapter is usable is a
  fact to show the owner; whether it is alive is what the pings judge. Treating a
  failing probe as a dead adapter would restart an adapter that is running and only
  lacks a login.
- **Abort sends nothing.** A cancellation is a line on the wire, so the contexts of
  the calls in flight are not the host's context; they end when the host has closed
  the stream.
- **Rejected: a library for supervision.** The problem is a loop, a backoff and a
  few timers, about a hundred lines; the libraries that supervise processes bring
  their own clocks, which would defeat the test clock.
- **Rejected: reading the child's stdout with `StdoutPipe` and waiting early.**
  `Wait` closes the pipe, which can drop what the reader has not taken.
- **Rejected: measuring "stable" by elapsed time.** It needs a monotonic reading the
  runtime packages are not given, and it would not move with the test clock.

## Where

| Concern | Code |
| ------- | ---- |
| `Host`, `Start`, `Shutdown`, `Abort`, `EnsureRunning`, `Stop`, `Stopping`, `Counts`, `LockPath` | `internal/node/adapterhost/host.go` |
| The supervisor, its waits and the question whether it is working | `supervisor.go` |
| One pass: launch, handshake, probe, pings, ending a connection | `pass.go` |
| A connection: handlers of the adapter's requests and notifications, the buffer and the forwarder, the counts | `conn.go` |
| The calls the engine makes on an adapter | `calls.go` |
| The adapter's log | `logwriter.go` |
| The launcher that runs a child process | `exec.go` |
| What the host asks of the engine, the launcher, the tunables | `engine.go`, `launcher.go`, `params.go`, `env.go`, `counts.go` |

Tests (in `internal/node/adapterhost`, with a fake clock moved by the test and a leak
check on every test):

| Behaviour | Tests |
| --------- | ----- |
| Two unanswered pings make the adapter offline, one does not, an answer starts the count again; reported once; the second pass after the backoff | `TestTwoUnansweredPingsMakeTheAdapterOfflineAndOneDoesNot` |
| No notification reaches the engine after the report that the adapter is gone | `TestNoNotificationReachesTheEngineAfterItWasToldThatTheAdapterIsGone` |
| The exact waits, the cap, the reset after a stable connection | `TestRestartBackoffDoublesIsCappedAndBeginsAgainAfterAStableConnection` |
| What the adapter did not declare is refused and the engine is never asked | `TestAdmissionRefusesWhatTheAdapterDidNotDeclare` |
| The code of `Admission.Check`, `-32003` before admission, the engine not called | `TestACallOfACapabilityTheAdapterDidNotDeclareIsRefusedWithTheCodeOfAdmissionCheck` |
| `1011` admits nobody and waits for the backoff | `TestAnInstanceLockedAnswerAdmitsNobodyAndTheNextPassWaitsForTheBackoff` |
| The order on the wire and in the calls to the engine, what each step carries, a failed probe | `TestAConnectionIsTakenOnInTheOrderInitializeAdmitStartProbe` |
| The limit of a handshake call on the host's clock | `TestAHandshakeCallThatIsNotAnsweredEndsThePassAtTheHandshakeTimeout` |
| Order of notifications, those before admission wait, an endpoint that does not exist yet | `TestNotificationsReachTheEngineInTheOrderSentAndThoseBeforeAdmissionWaitForIt` |
| Invalid and undeclared notifications are dropped | `TestAnInvalidOrUndeclaredNotificationIsDroppedAndTheOthersAreDelivered` |
| The bound of a stalled engine | `TestAStalledEngineLosesNoNotificationAndBeyondItsBoundTheConnectionEnds` |
| The adapter's requests, the engine's errors, the optional parts | `TestRequestsOfTheAdapterReachTheEngineAndItsErrorsReachTheAdapter` |
| The engine's calls, validation, not in service | `TestCallsToTheAdapterAreForwardedValidatedAndRefusedWhenItIsNotInService` |
| The counts and `Working` | `TestCountsFollowNotificationsRequestsCallsAndTheTimersOfTheHost` |
| What an error of the engine does to the connection | `TestAnErrorOfTheEngineEndsTheConnection`, `TestRequestsAfterShutdownBeganAreAnsweredAsShuttingDown` |
| `Start`, `StartWait`, refusals | `TestStartReturnsWhenEveryEnabledInstanceHasStartedOrBeenJudgedOffline`, `TestStartRefusesWhatItCannotStart`, `TestStartEndsWithItsContext` |
| `EnsureRunning` and `Stop` | `TestEnsureRunningAndStopOfAnInstance` |
| `Stopping` is true from `Stop` until the process has ended | `TestAnInstanceIsStoppingUntilItsProcessHasEnded` |
| `Shutdown` and `Abort` | `TestShutdownSendsShutdownWaitsThenKills`, `TestAbortKillsAtOnceAndSendsNoShutdown` |
| The log of the adapter | `TestTheStderrOfTheAdapterGoesToItsLog` |
| The lock path and the instance the launcher is given | `TestLockPath`, `TestTheLockPathAndTheInstanceAreWhatTheLauncherAndTheAdapterAreTold` |
| The exec launcher over a helper process (the test binary started again) | `TestExecLauncherRunsTheProgramOfTheManifestOverStandardStreams` |
| A process that cannot be ended is not waited for | `TestAProcessThatCannotBeEndedIsNotWaitedFor`, `TestAProcessThatDidNotEndAfterShutdownAndCannotBeKilledIsNotWaitedForAgain` |
| A request counted before the host waits for its answer | `TestARequestIsCountedAsSentBeforeTheHostIsIdleWaitingForItsAnswer` |
| Defaults of the tunables | `TestParamsZeroFieldsTakeTheirDefaults` |
| The codes an adapter is told for each refusal of the engine, one row per reason | `TestEveryReasonOfTheEngineHasItsCodeForARequestOfAnAdapter`, `TestARefusalCarriesTheLimitInItsDataAndTheFieldInItsMessage`, `TestTheCauseOfARefusalIsNeverSentToTheAdapter`, `TestAnErrorThatIsNotARefusalReachesTheHostUntouched` (`internal/node/apiwire`); through a real engine: `TestTheEngineBehindTheHostAnswersItsRefusalsWithTheCodesOfTheProtocol` |

The exec launcher's tests run on macOS here; the same tests run on Linux and Windows
in CI on main, and what they prove there is pending verification.
