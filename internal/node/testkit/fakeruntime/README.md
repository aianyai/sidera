# fakeruntime

The stand-in for a native agent runtime such as Claude Code or Codex: the program an
adapter drives and the daemon never talks to. Read [`../README.md`](../README.md)
first for where this package sits and the rules every testkit package follows.

It imports the standard library, the SDK module and the other packages of the
third-party side (`ledger`, `fakectl`). It is a program a stranger could have
written: it knows nothing of the daemon.

## Two shapes, one interface

A session is driven through `Session`, and sessions are made and found through a
`Launcher`. Two launchers exist.

- `Runtime` holds sessions in the test process. It is also the script's
  `Controller`: it starts and ends turns, sets the plan of the next turn and of the
  first turn of later sessions, kills the native process, reads a live session's
  actual environment and says whether it is idle.
- `ProcessLauncher` starts a program (`cmd/sidera-fake-runtime`) per session,
  detached, and drives it over the control channel. `RemoteController` is its
  script side. A runtime process outlives the adapter that started it, so a
  restarted adapter finds it again with `Find`, which asks the process who it is
  (reference, process id, start key) and takes it only if it is the session the info
  file names: a released port can be taken by the next process that asks for one.
  When a process is gone, or is not that session, `Find` still returns the session,
  dead, and answers `Reconcile` from its record.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `New(Config)`, `Runtime` | The in-process runtime. `Create`, `Find`, `SetPlan`, `SetFirstTurn`, `StartTurn`, `EndTurn`, `KillNative`, `SessionEnv`, `Idle`, `Activity` (idle with a count of how often a turn's actions began or finished, read together, for a fixture's quiet), `Close`. `Config.FirstTurn` is the plan of the first turn of every session. |
| `Session`, `Launcher`, `Controller`, `Watcher` | The interfaces. A watcher gets `TurnStarted`, `TurnEnded` and `ToolResult` one at a time and in order. `Session.Created` names the runtime's own ledger entry for the session. |
| `CreateRequest`, `CreateRefusedError`, `ValidateCreateRefusal` | Creation with an optional current refusal; a typed refusal without a creation effect from this call, and the shared validation rule. |
| `TurnPlan`, `TurnAction`, `ActionRunner`, `ActionResult`, `SessionInfo` | What the model does inside a turn, and who does it. |
| `ExecRunner` | An `ActionRunner` that runs a real command with the session's tool access. An `exec` action that names a `frontend` in its arguments is a receive: its output is the page the command printed, and it is recorded like the page of a `recv` action. With a `Seed` every command gets `SIDERA_TEST_SEED=<seed>/<native ref>/<n>`, so a client that draws its message numbers from it makes the same ones when a crash walk reruns a scenario. |
| `ModelSaw` | The ledger attributes of what a page showed the model, one entry per message. |
| `Event`, `EncodeEvent`, `DecodeEvent`, `ParseRecord` | The session record. |
| `ProcessSpec`, `RunProcess`, `ProcessLauncher`, `RemoteController`, `ProcessInfo` | The process shape. The control address is required: a process nobody can end is left behind when its test ends. |
| `DecodeLink`, `DecodeProcessSpec`, `DecodeProcessInfo` | Strict decoding of what crosses between processes. |

## Rules

- **A turn never ends because time passed.** A turn starts when a prompt arrives,
  unless the plan holds the start back (`ManualStart`), or when the script calls
  `StartTurn`; it ends when the script calls `EndTurn`, when an interrupt or a close
  ends it, or after the last action of its plan when the plan says `EndAfter`.
  Advancing the clock changes nothing here: tests that depend on how long a turn
  took would otherwise depend on the machine.
- **The first prompt starts a turn.** A real runtime starts working on the first
  prompt of a session at once, and the protocol treats that prompt as a prompt
  attempt, so the fake does too: the turn starts when the adapter that made the
  session takes charge of it (`Session.Watch`, or `runtime.watch` over the control
  channel), because its events have nowhere to go before that. The plan of that turn
  is `Config.FirstTurn` (`ProcessSpec.FirstTurn` in a process), the zero value
  starts it automatically and `ManualStart` leaves the start to the script. A prompt
  that arrives later starts a turn at once unless the plan holds it back, and the
  turn is for the latest prompt waiting; every prompt that waited belongs to that
  turn. The first turn's events can reach the daemon before the answer to
  `session.create`.
- **One writer.** A prompt that arrives during a turn is refused as busy (`ErrBusy`)
  and leaves no entry anywhere. The same attempt is delivered at most once: a repeat
  is a no-op.
- **What the model saw is recorded as digests.** Each message of a page the runner
  returned for a receive (a `recv` or `recv_part` action, or an `exec` that names a
  front end) gets a `model.saw` entry: the sender, a digest of the
  body, the part and the number of parts, whether the page marked it as a possible
  duplicate, and, when the configured classifier says so, the kind of node notice it
  is. The page text goes to the session record and to the adapter, as the observed
  tool result; the ledger never holds a body. Output of a receive that is not a page
  is written to the record as an error, with the text, and nothing is recorded as
  seen or reported to the adapter.
- **The ledger entry comes before the confirmation.** A session exists in the
  ledger before `Create` returns; a prompt before `Deliver` returns.
- **Native references are deterministic.** The reference of the session of an attempt
  is derived from the seed and the attempt, so the same attempt, asked again, is the
  same session, and tests can name it.
- **Creation refusal is a current input.** `CreateRequest.Refusal` is optional;
  `ValidateCreateRefusal` accepts empty or an SDK-valid creation failure other
  than `control_unknown`, which cannot prove absence of a native session. The
  request validator and adapter input validator share that rule. `Runtime.Create`
  first returns a session already held for the attempt. `ProcessLauncher.Create`
  first recovers published ProcessInfo or waits on an existing native record.
  Only a currently absent attempt returns `CreateRefusedError`, before spawning
  or writing runtime files or ledger entries. Refusal is not retained: clearing
  the input allows the same attempt to create. It adds no claim arbitration;
  a candidate started before the absence check can still arrive afterward.
- **The record is a file of the runtime.** One JSON line per event, strict and
  canonical (`seq` from 1, `at_ms` from the test clock), appended without a sync: a
  killed process leaves its page cache. It holds the text the model saw, because an
  adapter reads it to say what the model was shown; it is not a log of the daemon.
- **A killed native process stops reporting.** It refuses prompts and leaves its
  record readable and complete (`WriterStopped`); closing a session that is already
  dead is not an error. A turn it was running is over, and every turn has exactly
  one start and one end: while an adapter watches, the kill ends the turn as
  `failed` (the process cannot write that into its record, but the adapter sees it
  die), and a record whose writer stopped with a turn not ended reconciles as
  `failed`.
- **Events to the adapter are requests.** A runtime process hands `turn.started`,
  `turn.ended` and the tool result to its adapters as requests, one adapter and one
  event at a time, and the adapter answers only after it has passed the event to
  the daemon (for a tool result, after `recv.observed` has been answered). The
  runtime counts as busy until then, so a fixture that waits for `Idle` cannot see a
  gap in which the runtime has finished and the daemon has not been told.
- **A process ends with its control connection** and writes its address to
  `<native ref>.json` in one atomic step.

Process startup is independent of the adapter's standard streams: all three are
the null device. A replay first reads the existing ProcessInfo; if only the
exclusive `<native ref>.jsonl` record exists, it waits for that same native's
publication without starting another candidate. Concurrent callers that both see
neither file may start candidates, but the record's existing exclusive create
permits only one real session effect. Every returned creation checks the published
reference, PID and start key against one live `runtime.state` reply. Invalid
publication, identity and connection errors fail rather than being retried.

`(*ProcessLauncher).waitForProcessInfo` in `launcher.go` has the only narrowly
permitted real-time wait: one direct `time.NewTimer` call paces re-reading that
same ProcessInfo at 10 ms. There is no existing cross-process publication event.
Timer expiry is never readiness, a completion deadline, another spawn, a turn
decision or a logical clock advance. This exception cannot be reused for future
waits: first seek an event mechanism, then a separate decision if none suffices.
The method stops its timer on return. Each started candidate has one `Wait`
goroutine, which can reap it after the caller cancels; the launcher never kills a
possibly created native on cancellation. The fixture still owns native lifetime
through its control connection.

A creation error after starting does not prove absence of an effect. In
particular, a native that exits after claiming its record but before publishing
ProcessInfo cannot be distinguished from another candidate still initializing.
The record is retained and never rebuilt; creation waits for publication or
caller cancellation rather than returning fictional readiness. Unlike the old
stdout-EOF startup path, such a failure need not return immediately. Contexts do
not always have deadlines, so this wait has no guaranteed finite duration.

`Controller.SessionEnv(ref)` returns an independent environment map from an
actually created, live native session. `Runtime` clones the held map under the
session lock; unknown references return `ErrUnknownSession`, and closed or killed
sessions return `ErrNotAlive`. `RemoteController` queries the existing runtime
control connection on every call, checks its role and matching native reference,
and strictly decodes the typed `session.env` reply. A disconnected process follows
the existing unknown-session or not-alive error path. A session with no environment
has an empty wire map. Nothing reads node state or keeps a credential cache.
Environment replies never enter the ledger, session record, static process input
or diagnostics. Callers pass credentials only through the existing CLI driver's
process environment, never through command arguments.

## What the caller must guarantee

- Give the shared test clock as `Now`; a runtime never reads another.
- Give an `ActionRunner` if turns have actions. The in-process layer supplies one
  that calls the fake agent; `ExecRunner` runs the real client in the binary layer.
- Set the plan of the first turn before the session is created
  (`Config.FirstTurn`, `SetFirstTurn`): once the adapter has the session its first
  prompt has started a turn.
- Stop a `Runtime` with `Close`, which waits for running actions, and a
  `ProcessLauncher` with `Close`, which ends its connections (the processes go on).
- Do not turn on `ModelSaw` recording in a fake agent that a runtime drives:
  `fakeagent.RecordingOptions` explains why.

## Tests

`runtime_test.go` (turns, prompts, plans, the first turn, the record, receives that
print no page, kill, close, reconcile, the activity count of a fixture's quiet),
`record_test.go` (golden record lines, the decoder's rules, `FuzzDecodeEvent`,
`FuzzReadRecord`), `process_test.go` (a real runtime process: creation, the clock of
the fixture, a restarted adapter taking the session back, a process that is not the
session its info file names, a killed process and its record, the end of a process
with its control connection, the busy state until an adapter has handed an event
on, `ExecRunner` and its seeds, golden
messages and `FuzzDecodeLink`, `FuzzDecodeProcessSpecAndInfo`).

`session_env_test.go` checks the same environment contract in process and in a
real runtime process: actual values, independent maps, two-session routing,
unknown and closed sessions, strict replies, runtime role and private outputs.
`create_refusal_test.go` checks the allowed input values, no native effects on
refusal, same-attempt creation after clearing and existing-native priority in
both launchers.
`startup_test.go` holds the real control hello response after exclusive creation
and before publication, then loses the launcher parent or cancels its caller.
Replay must return the same actual live identity and environment with one real
creation; fixture closure ends the native and reclaims launcher goroutines.
`TestUnpublishedNativeDeathRetainsItsClaimUntilCallerCancellation` kills that
test-owned native before publication and confirms actual control departure.
The pending creation ends on explicit cancellation; a replay with a live,
deadline-bounded caller returns only its deadline error, retaining the exact
original record and sole creation effect without inventing readiness.
