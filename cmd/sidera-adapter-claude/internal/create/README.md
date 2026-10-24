# create

The state machine for creating one Claude session, as a pure decision. Read
[../../README.md](../../README.md) for why this lives under the adapter and
[the reference](../../../../docs/references/adapter-claude/README.md) for the
reasons behind the rules.

The package decides; it starts no process, reads no file and never looks at the
clock. The caller observes, calls `Decide`, executes the action, commits the
matching state transition and observes again. Any of those steps may be cut off
by a crash; the next `Decide` runs from the committed state and fresh
observations. The persisted `State` is what the adapter stores as its
checkpoint. A result is never committed together with an answer to the daemon:
the state is persisted first, then `session.create` is answered from it, and a
repeated `session.create` after a crash between the two is answered from the
stored state (`Done`).

## Entry points

| Function | Does |
| -------- | ---- |
| `NewIntent(attempt, session, firstPrompt, projectDir)` | The state to commit before any native interface is called. It persists the session uuid and the id the caller puts on the first user message. |
| `Decide(State, Observations, Params, now)` | The next `Action` with a typed `Reason`. For `ActionRecordProcess` the decision also carries the `Process`; for `ActionWait` a `RecheckBy`; for `ActionControlUnknown` every process seen; for `ActionDone` after a failure the stored `FailReason`. |
| `State.Started(Process)` | Records a started or found turn process (PID and start second). |
| `State.UUIDSeen()` | Records that the session file contains the uuid. |
| `State.Ready()` | Records readiness; the caller persists it, then answers `session.create` created. |
| `State.Failed(Reason)` | Records the failure; the caller persists it, then answers `session.create` failed, symmetrically to `Ready`. |
| `Reason.CreateFailure()` | The `adapterproto.CreateFailure` that `session.create` reports for a failure reason, and whether the reason is a failure at all: the only place a failure reason is mapped to the protocol. |
| `Reason.ControlUnknown()` | Whether the failure is reported as `control_unknown`. |
| `DefaultParams()`, `Params.Validate()` | The not-yet-decided numbers: `StartTimeout`, `MaxStarts`. |

## Actions

| Action | Caller does |
| ------ | ----------- |
| `StartNew` | Start `claude -p --session-id <uuid>`, giving it the first prompt (carrying the first prompt id) with the launch, and commit `Started(process)` immediately after spawning. |
| `StartResume` | The same with `--resume <uuid>`. |
| `Wait` | Start nothing; decide again when the turn ends, when the clock passes `RecheckBy` (the process start plus `StartTimeout`, in Unix seconds) or on the next observation. |
| `RecordProcess` | Commit `Started(decision.Process)`. |
| `MarkUUIDSeen` | Commit `UUIDSeen()`. |
| `MarkReady` | Persist `Ready()`, then answer `session.create` created. |
| `Done` | Nothing; the attempt is final: ready (`ReasonAlreadyReady`) or failed (`ReasonAlreadyFailed`, with `FailReason` the stored reason). |
| `Fail` | Persist `Failed(decision.Reason)`, then answer `session.create` failed with `decision.Reason.CreateFailure()`. A process that is still running is left alone. |
| `ControlUnknown` | Terminal, like `Fail`, and answered `failed` with `control_unknown`: persist `Failed(decision.Reason)`, then answer. Start nothing and touch no process; `decision.Processes` lists every process seen. The session never attaches to the channel, so nothing is marked there; the daemon notifies the local owner when it receives `control_unknown`. |

## Rules

- Never two turn processes for one session. A process counts as running when the
  process table lists it with the session uuid on its command line, or when it
  is the recorded process and its PID is still held with the recorded start. The
  same process reported by several sources counts once. With more than one the
  attempt ends as control unknown (`ReasonMultipleProcesses`) and nothing is
  started or touched. This check comes before readiness: even with the first
  prompt on disk, a second writer means control unknown.
- A process that only the runtime's session registry shows is an outside writer
  (`ReasonOutsideWriter`): the entry names this session, the process table
  confirms the entry's PID and start, the process is not the recorded one and
  its command line, which is always readable, does not carry the uuid (the user
  resumed the session by hand). The attempt ends as control unknown and the
  process is never recorded as ours. The registry never makes a process "ours"
  and never decides readiness; it can only reveal a writer that is not ours.
- Never `--session-id` when `<project dir>/<uuid>.jsonl` exists: that flag fails
  on an existing session. Then `--resume` is used. The creation flow resumes
  only when the first prompt is missing, so Claude's automatic "continue" turn,
  which it adds when a resumed session ends on an unanswered user message, is
  not triggered by it.
- A PID alive with another start is a reused number and not the recorded
  process. Start times are Unix seconds from the process table and are compared
  for equality, together with the PID.
- A running process whose PID was never recorded (the adapter died between
  starting it and committing it) is recorded first, before readiness or any
  start is considered, so every later step knows the writer.
- Ready means the first prompt is in the session record, found by
  `State.FirstPromptID`. It does not wait for the turn to end.
- A turn that has run longer than `StartTimeout` from its own start without the
  first prompt in the record fails the attempt. After `MaxStarts` distinct
  processes ended without the first prompt in the record, the attempt fails.
- Stages only advance. A ready attempt decides `Done`. A failed attempt is
  final too: `Decide` answers `Done` with `ReasonAlreadyFailed` and the stored
  reason whatever it is shown, `Started`, `UUIDSeen` and `Ready` refuse it, and
  `Failed` refuses a ready one. After a failure no process is ever started for
  this uuid; a process that is still running is left alone, and if it writes the
  first prompt later the attempt stays failed.
- A failed attempt is reported as `failed` with the `CreateFailure` of its
  reason (`Reason.CreateFailure`). `control_unknown` is reported for a second
  writer (`ReasonMultipleProcesses`), an outside writer (`ReasonOutsideWriter`)
  and a start that timed out while its process still runs
  (`ReasonStartTimedOut`): a native process may remain, which the adapter leaves
  alone. `ReasonStartsExhausted` is `native_error`: every process ended, none
  remains.
- Contradictions are errors, not decisions: a session file that disappeared
  after the uuid was seen, a lookup although the state records no process, a
  lookup of another PID than the recorded one, a state at the intent stage with
  a recorded process.

## What the caller must guarantee

- Observe the processes (`Recorded`, `Running`, `Registry`, all from one
  process-table snapshot) before the session record. A process seen as gone has
  written everything it will, so "no process and no file" cannot hide a file
  written in between.
- `Running` lists a process only if its argument vector contains the pair
  `--session-id <uuid>` or `--resume <uuid>` and its executable is the preset's
  Claude. Each launch counts once: the caller removes wrapper and ancestor
  processes of the same launch (a shell, a node launcher) and its own observer
  processes. Zombies are not alive.
- The argument vector of every process of the same user must be readable. When
  it is not, that is an observation error the caller reports as an error
  result; it is never reported as "not running", and `Decide` is not called.
- Start times are Unix seconds from the process table (Linux: boot time plus
  start ticks, fixed per boot; macOS: the process start seconds; Windows: the
  creation time truncated to seconds), the same source for every start time
  reported and recorded. For a process found through the registry, the start
  reported is the process table's.
- The session record is looked up in `State.ProjectDir` and `SessionRecord.Dir`
  says so; `Decide` refuses any other directory. The directory is fixed for the
  attempt. The first prompt is searched for by `State.FirstPromptID`, which the
  caller puts on the first user message.
- The first prompt is given to every process started by `StartNew` and
  `StartResume`, with the launch, and `Started` is committed immediately after
  spawning.
- Every `State` transition is committed atomically before the next `Decide`.
  `Ready` and `Failed` are persisted before `session.create` is answered, never
  the other way round, so an answer the daemon holds always has its state behind
  it; the repeated `session.create` after a crash in between gets the same
  answer from the stored state.
- Zero is never valid input: an empty attempt id, session id, first prompt id,
  project directory or time, a zero PID or start, a stage of 0 or a non-positive
  parameter makes `Decide` return an error wrapping `ErrInvalidInput` or
  `ErrInvalidParams`. Facts that are absent are zero by definition: a nil
  `Running` or `Registry` means none, `Recorded` is the zero `Lookup` while the
  state records no process, and `State.FailReason` is zero until the attempt
  fails. When the state records a process, `Recorded.PID` must be its PID
  (`Lookup{PID: n}` says nobody holds PID n); the zero `Lookup` is an error
  then.
