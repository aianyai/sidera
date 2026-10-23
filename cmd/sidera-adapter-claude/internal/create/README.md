# create

The state machine for creating one Claude session, as a pure decision. Read
[../../README.md](../../README.md) for why this lives under the adapter and
[the reference](../../../../docs/references/adapter-claude/README.md) for the
reasons behind the rules.

The package decides; it starts no process, reads no file and never looks at the
clock. The caller observes, calls `Decide`, executes the action, commits the
matching state transition and observes again. Any of those steps may be cut off
by a crash; the next `Decide` runs from the committed state and fresh
observations.

## Entry points

| Function | Does |
| -------- | ---- |
| `NewIntent(attempt, session, projectDir)` | The state to commit before any native interface is called. |
| `Decide(State, Observations, Params, now)` | The next `Action` with a typed `Reason`. For `ActionRecordProcess` the decision also carries the `Process`. |
| `State.Started(Process)` | Records a started or found turn process (PID and start time). |
| `State.UUIDSeen()` | Records that the session file contains the uuid. |
| `State.Ready()` | Records readiness; the caller commits it together with the control reply. |
| `DefaultParams()`, `Params.Validate()` | The not-yet-decided numbers: `StartTimeout`, `MaxStarts`. |

## Actions

| Action | Caller does |
| ------ | ----------- |
| `StartNew` | Start `claude -p --session-id <uuid>`, give it the first prompt, then commit `Started(process)`. |
| `StartResume` | The same with `--resume <uuid>`. |
| `Wait` | Start nothing; decide again when the turn ends or on the next observation. |
| `RecordProcess` | Commit `Started(decision.Process)`. |
| `MarkUUIDSeen` | Commit `UUIDSeen()`. |
| `MarkReady` | Commit `Ready()` and the control reply in one transaction. |
| `Done` | Nothing; the attempt was ready before. |
| `Fail` | Report the failure in the control reply. A process that is still running is left alone. |

## Rules

- Never two turn processes for one session. A process counts as running when the
  process table lists it with the uuid on its command line, when the runtime's
  session registry names it and the process table confirms it, or when it is the
  recorded process and its PID is still held. The same process reported by
  several sources counts once. With more than one, nothing is started
  (`ReasonMultipleProcesses`).
- Never `--session-id` when `<project dir>/<uuid>.jsonl` exists: that flag fails
  on an existing session. Then `--resume` is used, which can make the runtime
  add a "continue" turn of its own, so results are matched by the uuid on the
  prompt and the extra turn is counted against the quota.
- A PID alive with another start time is a reused number and not the recorded
  process. Start times are compared in whole seconds.
- The registry is auxiliary: an entry counts only when the process table shows
  the entry's PID alive with the entry's start time. Entries left behind by a
  killed process, entries whose PID was reused and entries of other sessions
  count as nothing.
- A running process whose PID was never recorded (the adapter died between
  starting it and committing it) is recorded first, before readiness or any
  start is considered, so every later step knows the writer.
- Ready means the first prompt is in the session record. It does not wait for
  the turn to end.
- A turn that has run longer than `StartTimeout` from its own start time without
  the first prompt in the record fails the attempt. After `MaxStarts` distinct
  processes ended without the first prompt in the record, the attempt fails.
- Stages only advance. A ready attempt decides `Done`.

## What the caller must guarantee

- Observe the processes (`Recorded`, `Running`, `Registry`) before the session
  record. A process seen as gone has written everything it will, so "no process
  and no file" cannot hide a file written in between.
- The session record is looked up in `State.ProjectDir` and `SessionRecord.Dir`
  says so; `Decide` refuses any other directory. The directory is fixed for the
  attempt.
- `Running` lists live processes only; a zombie is not alive. Start times come
  from the process table, truncated to whole seconds, and the same source is
  used for the start time recorded after a start.
- The first prompt is given to every process started by `StartNew` and
  `StartResume`.
- Every `State` transition is committed atomically before the next `Decide`.
- Zero is never valid input: an empty attempt id, session id, project directory
  or time, a zero PID or start time, a stage of 0 or a non-positive parameter
  makes `Decide` return an error wrapping `ErrInvalidInput` or
  `ErrInvalidParams`. The one fact encoded as a zero is `Lookup{}`: no process
  holds that PID.

## Undecided

Two processes carrying the same uuid is not settled: `Decide` waits, which keeps
the single-writer rule but does not resolve the attempt while both run. See the
skipped test `TestTwoProcessesCarryTheUUIDWhatShouldHappen`.
