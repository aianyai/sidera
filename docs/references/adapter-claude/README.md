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
whole attempt. The first prompt carries an id allocated with the uuid and
persisted in the intent; the prompt is found in the session record by that id.
Each later step is committed as it happens:

| Stage | Committed | After a restart, check |
| ----- | --------- | ---------------------- |
| intent | attempt id, uuid, project directory | no `<uuid>.jsonl` in the project directory and no process with the uuid on its command line: nothing started, so start |
| started | PID and process start time of the turn process | PID alive with the recorded start time: the turn is running, wait and start nothing |
| uuid seen | the session file contains the uuid | file exists and contains the uuid |
| ready | the first prompt is in the session record | find the first prompt in the record; only then report the control reply |
| failed | the failure reason, together with the control reply | final: no process is ever started for this uuid again |

`Decide` turns the committed state and the observations into the next action
(`StartNew`, `StartResume`, `Wait`, `RecordProcess`, `MarkUUIDSeen`,
`MarkReady`, `Done`, `Fail`, `ControlUnknown`) with a typed reason. The same function serves the
normal step and the check after a restart, so there is one set of rules.

### Rules

- At most one turn process runs for the session. Running means: listed by the
  process table with the uuid on its command line; or the recorded process with
  its PID still held at the recorded start. One process reported by several
  sources is one process. The runtime's session registry never adds a process
  of ours.
- Two or more distinct processes with the uuid, or a process that only the
  registry shows (confirmed by the process table, not the recorded one, uuid
  not on its command line: the user resumed the session by hand), end the
  attempt as control unknown. Nothing is started and no process is touched; the
  caller marks the session's control as unknown and notifies the local owner.
  The check comes before readiness: with the first prompt on disk a second
  writer is still a second writer.
- `--session-id` is used only while `<project dir>/<uuid>.jsonl` does not
  exist. When it exists, `--resume` is used.
- A PID held with a different start is a reused number: not running, not the
  recorded process.
- A running process whose PID was not recorded is recorded before anything else.
- Ready is the first prompt in the session record, whether or not the turn has
  ended.
- A turn that runs longer than `StartTimeout` from its own start without the
  first prompt in the record, or `MaxStarts` processes that ended without it,
  fail the attempt. Both are parameters (provisional: 2 minutes, 3). A waiting
  decision names the time (`RecheckBy`) at which to decide again, so the
  timeout fires even if no turn ends.
- Failure is final. After `Failed` the decision is always `Done` with the stored
  reason; a process that is still running is left alone, and a first prompt that
  it writes later does not make the attempt ready, because the failure was
  already reported.

### What the caller provides

Observations are gathered by the runtime, not by the package: processes before
the session record, from one process-table snapshot; a process counts only if
its argument vector holds `--session-id <uuid>` or `--resume <uuid>` and its
executable is the preset's Claude, each launch once (wrapper and ancestor
processes and the caller's own observers removed); live processes only; start
times in Unix seconds from the process table; the record looked up in the
attempt's project directory (`Decide` refuses another one). The argument
vector of every process of the same user must be readable, and an unreadable
one is an observation error reported as an error result, never "not running".
Transitions are committed atomically; `Ready` and `Failed` are committed
together with the control reply, and for control unknown the mark and the
owner notification too. `Started` is committed immediately after spawning.

## Why

- **The window between starting and recording.** Starting a process and
  committing its PID cannot be one atomic step. A crash in between leaves a
  running process the state does not know. Once the session file exists, a retry
  with `--session-id` is refused by the runtime; before it exists, a retry would
  run beside the first process and both would create the session, and a retry
  with `--resume` would start a second writer on the same session. Two writers
  interleave their records into branches. So after a restart the
  adapter looks for the process before it starts anything: the uuid is on the
  command line of every turn process of the session, which makes a process
  findable without a recorded PID.
- **`--session-id` cannot be re-run.** Claude Code 2.1.308 fails with "Session ID
  ... is already in use" when the session file exists (read from the binary; the
  documentation does not say so). A retry therefore checks the file first and
  resumes. The creation flow resumes only when the first prompt is missing from
  the record, so the "continue" turn that Claude inserts when a resumed session
  ends on an unanswered user message is not triggered by it. The file
  is looked up in one directory only, which is why the project directory is part
  of the attempt and `Decide` refuses a record looked up elsewhere.
- **PID plus start second.** The system reuses PIDs. A PID alone would make a
  recorded process look alive forever when an unrelated process took its number,
  and the attempt would wait for ever. The start is an `int64` of Unix seconds,
  the same unit as every time on the wire, and two processes are the same when
  PID and start are equal. Each platform derives it from its finest source,
  truncated to seconds: Linux from the boot time plus the start ticks (the boot
  time is fixed for the boot, so the value never changes), macOS from the
  process start seconds, Windows from the creation time. A time type with its
  own precision was rejected because a value read from one source with finer
  resolution would silently differ from the same process read from another.
  More identity fields (a unit, a process group, a job) may be added later; the
  sameness rule stays in one function.
- **The registry is supplementary, and can only reveal.** The runtime keeps a
  registry of sessions (one file per process, with session id, start time and
  busy state). It is an unpublished format, it survives `kill -9`, it is written
  some time after the process starts or not at all, and its entries can name
  PIDs that are reused. So it is never needed to find our own process (the uuid
  on the command line does that) and never makes one: an entry is trusted only
  after the process table confirms PID and start. What it adds is a writer
  that is not ours: a confirmed entry for this session whose process does not
  carry the uuid is the user's interactive resume, and the attempt ends as
  control unknown instead of recording it. Rejected: trusting the registry alone
  (stale entries would block starting for ever, or hide a live process);
  treating an unreadable command line as "not running" (a process whose command
  line cannot be read could be either ours or a second writer, so the caller
  reports an error and nothing is decided).
- **Control unknown is a failure the caller cannot overlook.** Two writers
  interleave one record into branches, and neither can be stopped safely by the
  adapter. Waiting would never settle the attempt while both live, so the attempt
  ends with a dedicated action; the caller marks the session control-unknown and
  notifies the owner, and this package only decides.
- **Failure is persisted and final.** A process that timed out may still write
  the first prompt later, and a retry after the failure was reported would put a
  second writer beside it. The failure is committed with the control reply, like
  readiness, and from then on the decision never starts anything or becomes
  ready, whatever the world shows.
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
- **Contradictions are errors.** A session file that vanished after the uuid was
  seen, a lookup although no process is recorded, a lookup of another PID than
  the recorded one, or a recorded process at the intent stage cannot all be true;
  `Decide` refuses them instead of picking a reading. Absent facts are zero: a
  nil `Running` or `Registry` means none, and `Lookup{PID: n}` says nobody holds
  PID n.
- **Rejected: starting a session without input.** Claude creates a session only
  when its first input runs, so there is no cheap "create" step to commit
  separately; the first prompt goes with the start, and readiness is its
  appearance in the record.

## Where

- `cmd/sidera-adapter-claude/internal/create/state.go`: `State`, stages,
  transitions, `Process`, `SessionID`.
- `.../observe.go`: `Observations`, `Lookup`, `RegistryEntry`, `SessionRecord`.
- `.../decide.go`: `Decide`, `Action`, `Reason`, `Decision`.
- `.../params.go`: `Params`, `DefaultParams`.
- Tests: `decide_test.go` (`TestDecide` rows named after each rule,
  `TestDecideStartGuards`, `TestDecideRefusesInvalidInput`),
  `scenario_test.go` (`TestCrashAfterStartBeforePIDRecordedKeepsOneProcessAndOneSession`,
  `TestCrashDuringTheResumeRetryIsFoundToo`,
  `TestRecordedPIDReusedByAnotherProcessIsNotAlive`,
  `TestTwoProcessesCarryingTheUUIDEndTheAttemptAsControlUnknown`,
  `TestARegistryOnlyProcessIsAnOutsideWriter`,
  `TestFailureIsTerminalEvenWhenTheTimedOutProcessWritesLater`), `state_test.go`
  (transitions, `TestStateFailed`, `TestFailureReasonNumbersAreStable`),
  `model_test.go` (`TestEveryCrashPointKeepsOneProcessAndOneSession`: a model of
  the session file, process table and registry, with the adapter killed at every
  crash point, outsiders that appear beside it, and the adapter deciding on after
  every failure).
