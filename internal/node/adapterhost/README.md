# adapterhost

The supervision of adapter processes and the carrying of the adapter protocol
between them and the engine. Read
[../../../docs/references/adapter-protocol/README.md](../../../docs/references/adapter-protocol/README.md)
for the protocol itself and
[../../../docs/references/adapter-host/README.md](../../../docs/references/adapter-host/README.md)
for the reasons behind the rules below.

This is a runtime package of layer 4 of [`internal/node/README.md`](../README.md):
it imports the contract packages, `config`, `logfile` and the SDK, and never the
engine. It defines `Engine`, the few things it asks of the engine, and the engine
defines the interface it asks of the host. Time comes only from the clock it is
given, and every goroutine it starts ends with the host. It touches no file:
starting a program is not a file operation, and its own log goes through the
handle the daemon gives it.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `New(Config)`, `Host.Start`, `Host.Shutdown`, `Host.Abort` | Make the host, start the supervision of every enabled instance once, end it gracefully, end it at once. |
| `Host.EnsureRunning`, `Host.Stop` | Start and end the supervision of one instance. Both are idempotent. |
| `Host.CreateSession`, `DeliverPrompt`, `Reconcile`, `Interrupt`, `Close`, `NotifyPending` | The calls the engine makes on an adapter. They are the host's half of the interface the engine defines for it. |
| `Host.Counts` | What the host counts of the connection of an instance, for the question whether the system is quiet. |
| `LockPath(home, instance)` | The file an instance's adapter locks: the one definition. |
| `ExecLauncher` | The launcher that runs an adapter as a child process. |
| `Engine`, `EndpointCalls`, `ObservedCalls` | What the host asks of the engine: the life of a connection, notifications, and the requests an adapter makes. The last two are optional parts the engine takes on as it learns to answer them. |
| `Notification`, `GoneCause` | A notification of the adapter as the engine receives it, and why an adapter is reported as gone. |
| `Launcher`, `Process`, `Instance` | How an adapter instance is started and what the host holds of it. |
| `Params`, `DefaultParams`, `OfflineAfterPingFailures` | The tunables, and the one decided rule: two pings in a row unanswered make an adapter offline. |
| `ErrNotConnected` | What a call to an adapter that is not in service returns without sending anything. |
| `Env`, `Source`, `LogOpener` | What the host needs of the node when it starts: the data directory, a read-only view of the configuration, the opener of the adapters' logs, and the engine. |
| `ConnCounts` | What the host counts of one connection. |

## Rules

- **One supervisor per instance.** A goroutine launches the adapter, takes it on,
  watches it and starts it again after a backoff. Only it waits on the host's
  timers. A call that can take long (the adapter's answer, the engine) runs on a
  helper goroutine while it waits.
- **Taking an adapter on** is, in this order: launch; `initialize` (the protocol
  range, the version of the daemon, the instance and the lock path, no
  capabilities of the daemon); `adapterreg.CheckHandshake` and `adapterreg.Admit`
  against the installed manifest; `Engine.AdapterAdmitted`, which returns the
  parameters of `adapter.start`; only then are the adapter's requests let
  through and the forwarder started; `adapter.start` and `Engine.AdapterStarted`;
  the connection is in service; `Engine.ProbeTokens`, `adapter.probe`,
  `Engine.AdapterProbed`. Each call to the adapter is limited by
  `Params.HandshakeTimeout`. Anything that fails before `adapter.start` was taken
  on kills the process and is reported as `GoneStartFailed`. An answer
  `1011 instance_locked` is such a failure and is logged as another process holding
  the lock; nobody is admitted and the next pass waits for the backoff.
- **A probe the adapter fails is logged and nothing more**: whether an adapter is
  usable is a fact for the owner, the pings judge whether it is alive. An error
  of the engine in any method that does not answer a request, `AdapterNotified`
  and the probe methods included, ends the connection.
- **Pings.** In service, every `PingInterval` the host sends `ping`, limited by
  `PingTimeout`. Any error is a failure. `OfflineAfterPingFailures` failures in a
  row (an answer starts the count again) end the forwarding of the connection (what
  is still buffered is dropped), report `GonePingsFailed`, kill the
  process and wait for it at most `ShutdownWait`; no notification reaches the engine
  after the report, and the report does not wait for the process; a process that `Kill` reports it
  cannot end is left at once and one that does not end is left after `ShutdownWait`,
  because the instance lock keeps a second writer out whatever it does. A
  connection that ends by itself is reported as `GoneConnectionEnded`, after the
  forwarder has handed the engine what was already buffered. Reports are made with
  the host's context, and not at all for an instance being stopped or a host that
  ended.
- **Backoff.** The wait before the next pass starts at `RestartBackoffMin`,
  doubles after every failed pass or lost connection and stops at
  `RestartBackoffMax`. A connection that stayed in service for `StableAfter` (a
  timer started when it came into service: the runtime packages have no monotonic
  reading) makes the next wait begin at the minimum again.
- **Requests of the adapter** are answered exactly once through the peer. `ping`
  always. Before admission `-32003`, after shutdown began `-32002`. Then
  `adapterreg.Admission.Check` decides, with the kind of endpoint the method
  belongs to (`endpoint.*` direct, `recv.observed` session, a checkpoint the runtime
  when the adapter declared one and direct otherwise); the host never resolves an
  endpoint. Parameters are decoded strictly (`-32602`; a checkpoint state beyond
  its limit is `1006`). An error of the engine that is or wraps a `*peer.Error` is
  answered as it is, any other is `-32603`, whose text the peer logs and never
  sends. The host does not refuse requests for being too many: the handler waits
  on the engine and the peer's own queue refuses with `-32001`, which therefore
  still means that the handler did not run. `EndpointCalls` and `ObservedCalls` are
  used when the engine given to `Start` implements them, and are `-32601`
  otherwise.
- **Notifications** are decoded and validated; an invalid one is logged and
  dropped. They go into a buffer of `NotificationBuffer` in arrival order and one
  forwarder per connection hands them to `Engine.AdapterNotified` one at a time,
  after `AdapterAdmitted` has returned (what arrives before waits in the buffer).
  One the declaration does not allow is logged and dropped when the forwarder
  takes it; a dropped notification counts as handled. While the buffer is full the
  handler blocks, the peer's queue fills and the peer ends the connection, which
  the host reports and recovers from. Nothing is lost while the engine stalls for
  less than the buffer and the peer's queue hold together. The parameters reach
  the engine as the value of their type, not a pointer.
- **Start** returns when each enabled instance has answered `adapter.start`, has
  failed its first pass (reported), or `StartWait` has passed on the clock (the
  ones still not started are reported as `GoneStartFailed` and keep trying). It
  fails only for a bad `Env`, a host that was started or ended, or its own context.
  The host does not end with that context.
- **Shutdown** sends `shutdown` to each connection in service (limited by
  `HandshakeTimeout`), closes the stream, waits for the process at most
  `ShutdownWait` and kills it if it stays. `Stop` does the same for one instance.
  Neither reports to the engine, and what is still buffered for the engine is
  dropped. **Abort** kills every process at once and writes nothing, not even a
  cancellation of a request in flight. After either, calls return
  `ErrNotConnected` and `Start` fails; both are idempotent.
- **Counts** are exact. `Calls` lists the requests the host sent that have no
  answer yet (a ping is not among them); a request is counted before the host
  starts to wait for its answer. `Working` is true while the supervisor does
  anything of its own (launching, handshaking, calling the engine, reporting,
  ending a process) and while a timer of the instance is due and not yet taken;
  a supervisor parked on its timers with nothing due is not working. `Working` is
  asked before the counts are read and they are read again if the supervisor moved
  meanwhile, so "idle" never comes with counts older than the request the host is
  waiting for. After the test clock moved, a
  caller that sees `Working` false knows the host has acted on everything the move
  made due. Only a test clock shows a due timer. After `Shutdown`, `Abort` and for
  an unknown instance everything is zero.

## What the caller must guarantee

- A `Launcher` ends the stream of a process when the process ends: the host sees
  the end of a connection from the end of its stream. `ExecLauncher` does.
- A test that drives a fake clock moves it only after the host has armed the
  timer the move is meant to fire, because moving a clock fires only timers that
  exist: after the event that arms it (the connection taken on and probed, a ping
  sent), wait until `Counts(...).Working` is false.
- The engine answers requests and notifications with the context it is given,
  and returns when that context ends: the host ends everything with it.
- Home is an absolute path; the installed adapters are at
  `config.AdapterVersionDir` under it.
- The engine can be told `GoneStartFailed` for a connection while an earlier call
  of the same connection (`AdapterAdmitted`, `AdapterStarted`) is still running,
  when `StartWait` passes during it.

## Where the tests are

`supervision_test.go` (pings and what the engine receives after the report of a lost adapter, backoff, `Start`, `EnsureRunning`, `Stop`,
`Shutdown`, `Abort`), `admission_test.go` (what is refused, the lock, the order of
taking an adapter on), `notifications_test.go` (order, the bound, dropped ones),
`requests_test.go` (the adapter's requests, the engine's calls, the counts),
`idle_test.go` (a request counted before the host waits for its answer, and a process
that cannot be ended is not waited for),
`engineerrors_test.go`, `stderr_test.go`, `exec_test.go` (a helper process: the
test binary started again), `lockpath_test.go`, `params_internal_test.go`.
`helpers_test.go` holds the doubles: an engine that records every call in order, a
scripted adapter on the peer, and a rig around the fake adapter and a fake clock.
