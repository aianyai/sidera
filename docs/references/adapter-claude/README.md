# Claude adapter

How the Claude adapter creates a session, so that a crash at any step leaves
neither two processes nor two sessions. The adapter lives under
`cmd/sidera-adapter-claude/`, outside the top-level `internal/`, so the core
cannot import it and contains no product name.

## What

### Creation attempt

One `session.create` for a Claude runtime is one attempt. Before any native
interface is called, the adapter commits the attempt id, a session uuid
allocated in advance and the preset's project directory, which is fixed for the
whole attempt. Each later step is committed as it happens:

| Stage | Committed | After a restart, check |
| ----- | --------- | ---------------------- |
| intent | attempt id, uuid, project directory | no `<uuid>.jsonl` in the project directory and no process with the uuid on its command line: nothing started, so start |
| started | PID and process start time of the turn process | PID alive with the recorded start time: the turn is running, wait and start nothing |
| uuid seen | the session file contains the uuid | file exists and contains the uuid |
| ready | the first prompt is in the session record | find the first prompt in the record; only then report the control reply |

`Decide` turns the committed state and the observations into the next action
(`StartNew`, `StartResume`, `Wait`, `RecordProcess`, `MarkUUIDSeen`,
`MarkReady`, `Done`, `Fail`) with a typed reason. The same function serves the
normal step and the check after a restart, so there is one set of rules.

### Rules

- At most one turn process runs for the session. Running means: listed by the
  process table with the uuid on its command line; or named by the runtime's own
  session registry and confirmed by the process table with PID and start time;
  or the recorded process with its PID still held at the recorded start time.
  One process reported by several sources is one process.
- `--session-id` is used only while `<project dir>/<uuid>.jsonl` does not
  exist. When it exists, `--resume` is used.
- A PID held with a different start time is a reused number: not running, not
  the recorded process.
- A running process whose PID was not recorded is recorded before anything else.
- Ready is the first prompt in the session record, whether or not the turn has
  ended.
- A turn that runs longer than `StartTimeout` from its own start without the
  first prompt in the record, or `MaxStarts` processes that ended without it,
  fail the attempt. Both are parameters (provisional: 2 minutes, 3).
- Two or more distinct processes with the uuid: nothing is started and the
  decision is to wait. Whether that should instead fail or mark the session's
  control as unknown is open.

### What the caller provides

Observations are gathered by the runtime, not by the package: processes before
the session record; live processes only; start times in whole seconds from the
process table; the record looked up in the attempt's project directory (`Decide`
refuses another one). Transitions are committed atomically, and `Ready` is
committed together with the control reply.

## Why

- **The window between starting and recording.** Starting a process and
  committing its PID cannot be one atomic step. A crash in between leaves a
  running process the state does not know. Retrying `--session-id` would not
  start a second session (the runtime refuses an existing session id) but a
  retry with `--resume` would start a second writer on the same session, and
  two writers interleave their records into branches. So after a restart the
  adapter looks for the process before it starts anything: the uuid is on the
  command line of every turn process of the session, which makes a process
  findable without a recorded PID.
- **`--session-id` cannot be re-run.** Claude Code 2.1.308 fails with "Session ID
  ... is already in use" when the session file exists (read from the binary; the
  documentation does not say so). A retry therefore checks the file first and
  resumes. Resuming a session whose last message is an unanswered user message
  makes Claude insert a "continue" turn of its own; results are matched by the
  uuid set on the prompt, and the extra turn counts against the quota. The file
  is looked up in one directory only, which is why the project directory is part
  of the attempt and `Decide` refuses a record looked up elsewhere.
- **PID plus start time.** The system reuses PIDs. A PID alone would make a
  recorded process look alive forever when an unrelated process took its number,
  and the attempt would wait for ever. Start times are compared in whole
  seconds, the resolution of the process table, so a start time read from a
  finer source still matches.
- **The registry is only a hint.** The runtime keeps a registry of sessions
  (one file per process, with session id, start time and busy state). It is an
  unpublished format, it survives `kill -9`, and its entries can name PIDs that
  are reused. It helps when the command line cannot be read, but an entry is
  trusted only after the process table confirms PID and start time. Rejected:
  trusting the registry alone (stale entries would block starting for ever, or
  hide a live process).
- **Record before ready.** A live unrecorded process is recorded before
  readiness is taken, so that when delivery later asks whether the session is
  busy (a Claude session is busy exactly while its turn process lives, judged by
  PID and start time) the answer is based on a recorded process.
- **Ready is the prompt in the record, not the end of the turn.** The prompt is
  durable seconds after the process starts; waiting for the turn to end would
  make `session.create` as slow as the first model reply.
- **Observation order.** The process table is read before the session file. In
  the other order a process could write the file and exit between the two reads,
  and the adapter would see "no file, no process" and use `--session-id` on an
  existing session.
- **Bounds are parameters.** A hung process and a runtime that cannot start must
  end the attempt, but how long to wait and how often to retry is not decided,
  so both are fields of `Params`. A failed attempt does not touch a process that
  is still running; stopping it is a runtime decision.
- **Rejected: starting a session without input.** Claude creates a session only
  when its first input runs, so there is no cheap "create" step to commit
  separately; the first prompt goes with the start, and readiness is its
  appearance in the record.

## Where

- `cmd/sidera-adapter-claude/internal/create/state.go`: `State`, stages,
  transitions, `Process`, `SessionID`.
- `.../observe.go`: `Observations`, `Lookup`, `RegistryEntry`, `SessionRecord`.
- `.../decide.go`: `Decide`, `Action`, `Reason`.
- `.../params.go`: `Params`, `DefaultParams`.
- Tests: `decide_test.go` (`TestDecide` rows named after each rule,
  `TestDecideStartGuards`, `TestDecideRefusesInvalidInput`),
  `scenario_test.go` (`TestCrashAfterStartBeforePIDRecordedKeepsOneProcessAndOneSession`,
  `TestCrashDuringTheResumeRetryIsFoundToo`,
  `TestRecordedPIDReusedByAnotherProcessIsNotAlive`, the skipped
  `TestTwoProcessesCarryTheUUIDWhatShouldHappen`), `state_test.go`
  (transitions, start-time comparison), `model_test.go`
  (`TestEveryCrashPointKeepsOneProcessAndOneSession`: a model of the session
  file, process table and registry, with the adapter killed at every crash point).
