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
  `Controller`: it starts and ends turns, sets the plan of the next turn, kills the
  native process and says whether it is idle.
- `ProcessLauncher` starts a program (`cmd/sidera-fake-runtime`) per session,
  detached, and drives it over the control channel. `RemoteController` is its
  script side. A runtime process outlives the adapter that started it, so a
  restarted adapter finds it again with `Find`. When a process is gone, `Find` still
  returns the session, dead, and answers `Reconcile` from its record.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `New(Config)`, `Runtime` | The in-process runtime. `Create`, `Find`, `SetPlan`, `StartTurn`, `EndTurn`, `KillNative`, `Idle`, `Close`. |
| `Session`, `Launcher`, `Controller`, `Watcher` | The interfaces. A watcher gets `TurnStarted`, `TurnEnded` and `ToolResult` one at a time and in order. |
| `TurnPlan`, `TurnAction`, `ActionRunner`, `ActionResult`, `SessionInfo` | What the model does inside a turn, and who does it. |
| `ExecRunner` | An `ActionRunner` that runs a real command with the session's tool access. An `exec` action that names a `frontend` in its arguments is a receive: its output is the page the command printed, and it is recorded like the page of a `recv` action. |
| `ModelSaw` | The ledger attributes of what a page showed the model, one entry per message. |
| `Event`, `EncodeEvent`, `DecodeEvent`, `ParseRecord` | The session record. |
| `ProcessSpec`, `RunProcess`, `ProcessLauncher`, `RemoteController`, `ProcessInfo` | The process shape. |
| `DecodeLink`, `DecodeProcessSpec`, `DecodeProcessInfo` | Strict decoding of what crosses between processes. |

## Rules

- **A turn never ends because time passed.** A turn starts when a prompt arrives,
  unless the plan holds the start back (`ManualStart`), or when the script calls
  `StartTurn`; it ends when the script calls `EndTurn`, when an interrupt or a close
  ends it, or after the last action of its plan when the plan says `EndAfter`.
  Advancing the clock changes nothing here: tests that depend on how long a turn
  took would otherwise depend on the machine.
- **A session is created idle.** Its first prompt is in the record and waits for a
  turn; the script starts it. A prompt that arrives later starts a turn at once
  unless the plan holds it back, and the turn is for the latest prompt waiting;
  every prompt that waited belongs to that turn.
- **One writer.** A prompt that arrives during a turn is refused as busy (`ErrBusy`)
  and leaves no entry anywhere. The same attempt is delivered at most once: a repeat
  is a no-op.
- **What the model saw is recorded as digests.** Each message of a page the runner
  returned for a `recv` action gets a `model.saw` entry: the sender, a digest of the
  body, the part and the number of parts, whether the page marked it as a possible
  duplicate, and, when the configured classifier says so, the kind of node notice it
  is. The page text goes to the session record and to the adapter, as the observed
  tool result; the ledger never holds a body.
- **The ledger entry comes before the confirmation.** A session exists in the
  ledger before `Create` returns; a prompt before `Deliver` returns.
- **Native references are deterministic.** The reference of the session of an attempt
  is derived from the seed and the attempt, so the same attempt, asked again, is the
  same session, and tests can name it.
- **The record is a file of the runtime.** One JSON line per event, strict and
  canonical (`seq` from 1, `at_ms` from the test clock), appended without a sync: a
  killed process leaves its page cache. It holds the text the model saw, because an
  adapter reads it to say what the model was shown; it is not a log of the daemon.
- **A killed native process stops reporting.** It never ends its turn, refuses
  prompts, and leaves its record readable and complete (`WriterStopped`). Closing a
  session that is already dead is not an error.
- **A process ends with its control connection** and writes its address to
  `<native ref>.json` in one atomic step.

## What the caller must guarantee

- Give the shared test clock as `Now`; a runtime never reads another.
- Give an `ActionRunner` if turns have actions. The in-process layer supplies one
  that calls the fake agent; `ExecRunner` runs the real client in the binary layer.
- Stop a `Runtime` with `Close`, which waits for running actions, and a
  `ProcessLauncher` with `Close`, which ends its connections (the processes go on).
- Do not turn on `ModelSaw` recording in a fake agent that a runtime drives:
  `fakeagent.RecordingOptions` explains why.

## Tests

`runtime_test.go` (turns, prompts, plans, the record, kill, close, reconcile),
`record_test.go` (golden record lines, the decoder's rules, `FuzzDecodeEvent`,
`FuzzReadRecord`), `process_test.go` (a real runtime process: creation, the clock of
the fixture, a restarted adapter taking the session back, a killed process and its
record, the end of a process with its control connection, `ExecRunner`, golden
messages and `FuzzDecodeLink`, `FuzzDecodeProcessSpecAndInfo`).
