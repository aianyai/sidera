# fakeadapter

An adapter that speaks the adapter protocol as a third party would write it, and
does only what a test tells it to. Read [`../README.md`](../README.md) first for
where this package sits and the rules every testkit package follows.

It imports the standard library, the SDK module (`peer`, `adapterproto`, `manifest`,
`strictjson`) and the other packages of the third-party side (`ledger`, `fakectl`,
`fakeruntime`), and nothing else of the repository. That is what makes a test against
it a test of the protocol boundary: if it reached an internal package, the boundary
would be proved against a program that is not a third party. A test enforces it, for
three operating systems and both tag sets.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `StartPipe(ctx, Config)` → `PipeProcess` | `Serve` on a pair of pipes with the shape of a child process (`Stream`, `PID`, `Kill`, `Wait`), for an engine-layer fixture that hands the adapter host a process to supervise. |
| `Serve(ctx, r, w, Config)` | The adapter side of the protocol over a reader and a writer: in a test process the two ends of an `io.Pipe`, in a program standard input and output. |
| `Config`, `ManifestOptions`, `DefaultManifestOptions` | What the adapter is given and what it declares. `ManifestOptions` makes both the manifest (`Manifest`, `Encode`, `WritePackage`) and the capabilities it answers `initialize` with, so they cannot disagree. |
| `Knobs`, `Outcome`, `NewKnobs` | The test's handles. |
| `Controller`, `Knobs.Controller`, `RemoteKnobs` | One interface, with a context, over the knobs of both layers: a script written against it turns the same knobs of an adapter in the test process and of the program, as one written against `fakeruntime.Controller` does for the turns. |
| `RunProcess`, `ProcessConfig`, `DecodeProcessConfig`, `ReadProcessConfig`, `ConfigArgs` | The body of the adapter program; the program is `cmd/sidera-fake-adapter`. |
| `Knobs.Dispatch`, `DecodeKnobArgs` | The knobs by name over the control channel; `RemoteKnobs` is the fixture's side of them. |

## What it does

It answers every method the daemon may call: `initialize` (taking the instance lock
before it answers), `ping`, `shutdown`, `adapter.start`, `adapter.probe`,
`session.create`, `prompt.deliver`, `prompt.reconcile`, `session.interrupt`,
`session.close` (which closes the session of an endpoint it holds, finds a session it no
longer holds by the native reference the request carries, answers `closed` for one that is
gone and leaves alone one that it holds under another endpoint), and `endpoint.pending`,
which it writes to the ledger with its count. It calls `checkpoint.put` while it creates a
session, sends `turn.started`, `turn.ended` and `adapter.availability`, and, from its
knobs, `prompt.persisted`, `native_request.handled` and the `endpoint.*` methods of a
chat bridge. It reports a recv result with `recv.observed` when it declared that it
acknowledges on behalf.

It drives a native runtime through a `fakeruntime.Launcher`: a `Runtime` in process,
a `ProcessLauncher` in the program.

## Rules

- **Before initialize, only `initialize`, `ping` and `$/cancel_request` are
  answered**; anything else is `-32003`. After `shutdown`, only `ping`. A second
  `initialize` is `-32600`; no version in common is `-32022` with the two ranges.
- **The lock before the answer.** The adapter locks `instance.lock_path` before it
  answers `initialize` and answers `1011` when it cannot, so that a hung old process
  and its replacement never control sessions at once. The lock is the standard
  library's only (`flock` on Unix, a handle opened without sharing on Windows) and
  dies with the process.
- **An attempt is idempotent.** The same creation attempt is the same session,
  whatever the number of retries; the same attempt with other parameters is `1005`.
  The same prompt attempt is delivered once, and answered the same; with other text
  it is `1005`. A checkpoint the daemon sent back with a retry does not make a retry
  another attempt.
- **Effect first, then ledger, then answer.** The native session exists, the ledger
  says so (`native.create.returned`, which names the runtime's own entry for the
  session), and only then is `created` answered; the pause point
  `create.after-native` sits exactly there.
- **A session starts working on its first prompt.** The first turn starts when the
  adapter adopts the session, so `turn.started` (with the first prompt's attempt)
  can reach the daemon before the answer to `session.create`; the plan of that turn
  is the runtime's (`ProcessConfig.RuntimeFirstTurn`, the `first-turn` knob).
- **A killed native process ends its turn.** The adapter reports `turn.ended` with
  `failed` for the turn the process was running, so every turn has one end.
- **It never writes a message the protocol would refuse.** Every request,
  notification and result is validated before it is sent, and a knob that would make
  it answer with an error code the protocol does not define, or one that must carry
  data, is refused when it is set. The behaviour-test rig checks every line the
  adapter wrote against the schemas when its test ends.
- **Time is the shared clock.** `at_ms` of every notification comes from the clock
  it was given; the adapter reads no other.
- **Bodies never reach the ledger or the log**, only digests.
- **Capabilities are honoured both ways.** A method of a capability that was not
  declared is `1003` (the daemon would never send it); a knob that needs a
  capability that was not declared returns `ErrNotDeclared`.

## The knobs

Outcomes (`SetCreate`/`QueueCreate`, `SetPrompt`/`QueuePrompt`, `SetInterrupt`…,
`SetClose`…, `SetReconcile`…, `SetStart`…) decide what a request comes to: a failed
or unknown creation, a refused, only accepted or errored prompt, an interrupt that is
only requested, an unconfirmed close, an unreachable start, an error code before or
after the effect, or a hold until `ReleaseHeld`. The queue is used before the sticky
setting. A kind a request does not take is refused when it is set.

`PauseAt` stops the next request that reaches a named point (`create.after-native`,
`prompt.after-forward`, `interrupt.after-forward`, `close.after-forward`,
`start.before-reply`) until `Release`; a point stops one request and is spent.
`Hang` stops all answers, ping included, and keeps the process and the lock; it writes its `knob.hit` when the first request is held. In the program, every stop at a pause point is also reported to the fixture over the control channel (`paused`, with the point), so the fixture need not poll.
`Disconnect` closes the stream. `SetObserved` makes `recv.observed` report the
result faithfully, cut short (with the manifest's truncation notice) or replaced
(with its replacement notice). `Availability`, `Persist`, `NativeRequest`, `Ping`
and the direct methods (`Register`, `Recv`, `Ack`, `Send`, `MessageStatus`,
`Unregister`), and `CheckpointPut` send what the name says and return the daemon's answer. `Held`,
`NotificationsSent` and `Outstanding` tell a fixture which unanswered requests are
expected and how many notifications the daemon must have handled before the system
counts as quiet. `Held` is a multiset of methods (`session.create`, …), one per held
request, whether an outcome, a pause point or `Hang` holds it (a hung adapter holds every
request it receives, a ping included): the fixture compares it with
the multiset of methods the daemon has in flight to the adapter. A request leaves
`Held` before `Release` or `ReleaseHeld` returns, so a fixture that asks for quiet
right after letting it go waits for its answer instead of counting it as still held.
`HeldPoints` names the pause points some of them stopped at.

`CheckpointPut` takes the SDK checkpoint parameters and sends the existing
`checkpoint.put` request through the adapter peer. Local and remote controllers
share that implementation; daemon errors return to the caller unchanged.

Every use of a knob that changes behaviour writes a `knob.hit` entry, so a test can
assert that its injection point was reached.

## The program

`cmd/sidera-fake-adapter` reads a `ProcessConfig` from the file named by its
argument `--config` (`ConfigArgs` makes the arguments for the manifest's `Args`,
which is what tells several fake adapters under one daemon apart), or else from the
JSON in the environment variable `SIDERA_FAKE_ADAPTER_CONFIG`, which they would
share. `ProcessConfig` requires the control address and a clock start after 1970:
a field that was left out must not read as an adapter nobody can end or as 1970. The
program serves the protocol on standard input and output,
writes a file ledger, starts its native sessions as runtime processes, and takes the
test clock, the running script step and its knobs from the fixture over the control
channel. It ends when the daemon closes its input or when the control connection
ends, so no test leaves an orphan.

## What the caller must guarantee

- Give the test clock and a ledger writer named `adapter/<instance>` on it.
- Serve one connection at a time per `Knobs`; a restarted adapter is a new `Serve`
  with the same `Knobs`, runtime and ledger.
- Stop a `Serve` by cancelling its context or closing the daemon's end of the
  stream; it releases the instance lock on return.

## Tests

`consistency_test.go` is the consistency test: every line the adapter writes in a
scenario that uses every method is accepted by the schema and by the Go types, the
scenario speaks every method in both directions, the declaration is admitted by the
daemon's admission rules, and every golden request of the daemon gets an answer the
schema accepts. `behavior_test.go` pins the rules above, one row per rule.
`process_test.go` runs the two programs together: creation in a runtime process, the
clock and the step reaching both, an adapter killed and a second one taking the
session back, a native process killed, the knobs over the control channel, and the
decoders (`FuzzDecodeKnobArguments`, `FuzzDecodeProcessConfig`). `rules_test.go`
holds the rest of the rules above: one knob script run in both layers, `Held` as a
multiset of methods, the first turn and the end of a killed one, what the ledger
names, how the program finds its configuration, and that the daemon side has been
told before the call that caused an event returns.
