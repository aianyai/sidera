# Claude adapter

How the Claude adapter drives Claude Code: how it creates a session so that a
crash at any step leaves neither two processes nor two sessions, how it starts
the process of a turn, finds a prompt in the session record, reports the turn's
events and answers reconciliation. The adapter lives under
`cmd/sidera-adapter-claude/`, outside the top-level `internal/`, so the core
cannot import it and contains no product name.

Two directories are told apart throughout. The **working directory** is where
the agent works: the preset's `project_dir`, the current directory of every turn
process. The **record directory** is where Claude Code keeps the session files of
that working directory, one `<uuid>.jsonl` per session; it is derived from the
working directory (see below) and is not the working directory.

## What

### Creation attempt

One `session.create` for a Claude runtime is one attempt. Before any native
interface is called, the adapter commits the attempt id, a session uuid
allocated in advance and the record directory of the session, which is fixed for the
whole attempt. The first prompt carries an id allocated with the uuid and
persisted in the intent; the prompt is found in the session record by that id.
Each later step is committed as it happens:

| Stage | Committed | After a restart, check |
| ----- | --------- | ---------------------- |
| intent | attempt id, uuid, record directory | no `<uuid>.jsonl` in the record directory and no process with the uuid on its command line: nothing started, so start |
| started | PID and process start time of the turn process | PID alive with the recorded start time: the turn is running, wait and start nothing |
| uuid seen | the session file contains the uuid | file exists and contains the uuid |
| ready | the first prompt is in the session record | find the first prompt in the record; only then answer `session.create` |
| failed | the failure reason | final: no process is ever started for this uuid again; `session.create` is answered failed |

Each stage is persisted before `session.create` is answered from it. The answer
is a result of the adapter protocol; the control reply the sender reads is a
record the daemon signs in another process, so the two cannot be one
transaction. A crash between persisting and answering is repaired by the
repeated `session.create`, which is answered from the stored state.

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
  attempt as control unknown. Nothing is started and no process is touched. The
  check comes before readiness: with the first prompt on disk a second writer is
  still a second writer.
- `--session-id` is used only while `<record dir>/<uuid>.jsonl` does not
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
- A failure is reported to the daemon as `session.create` `failed` with the
  reason `Reason.CreateFailure` gives, which is the one place a failure reason
  becomes a protocol value:

  | Failure | Reported as |
  | ------- | ----------- |
  | two or more distinct processes with the uuid | `control_unknown` |
  | a process only the registry shows (an outside writer) | `control_unknown` |
  | a start that timed out while its process still runs | `control_unknown` |
  | `MaxStarts` processes ended without the first prompt | `native_error` |

  `failed` means no session will be attached to the channel for this attempt. For
  `control_unknown` a native process may remain; the adapter leaves it alone, and
  the daemon notifies the local owner when it receives the reason (see
  [the adapter protocol](../adapter-protocol/README.md)).

### What the caller provides

Observations are gathered by the runtime, not by the package: processes before
the session record, from one process-table snapshot; a process counts only if
its argument vector holds `--session-id <uuid>` or `--resume <uuid>` and its
executable is the preset's Claude, each launch once (wrapper and ancestor
processes and the caller's own observers removed); live processes only; start
times in Unix seconds from the process table; the record looked up in the
attempt's record directory (`Decide` refuses another one). The argument
vector of every process of the same user must be readable, and an unreadable
one is an observation error reported as an error result, never "not running".
Transitions are committed atomically; `Ready` and `Failed` are persisted before
`session.create` is answered, never after. `Started` is committed immediately
after spawning.

### The adapter's lifecycle

The adapter is served by `sdk/go/adapterkit`, which answers `initialize` (taking the instance
lock first), ping and shutdown and gates everything else. It declares one runtime with the
front ends `cli` and `mcp`, and neither `interrupt`, `ack_on_behalf` nor `server_requests`: a
`claude -p` process sends the adapter no request that needs an answer, and interrupting and
acknowledging on the agent's behalf are not built yet. The identity answered in `initialize` is
read from the manifest built into the executable.

- **`adapter.start`** carries the instance configuration, the presets and the sessions to take
  back. The adapter finds `claude` and `sidera` (the instance's `claude_path` and `sidera_path`,
  else the program on `PATH`), writes the MCP configuration file into the state directory and
  keeps the presets, which are fixed for the life of the process because the host restarts the
  instance when they change. A session is `lost` when its session file is gone (Claude Code
  removes records of sessions idle longer than its cleanup period, 30 days by default),
  `unreachable` (and logged) when its checkpoint is missing or unusable, and otherwise
  `attached`, with `turn_active` true exactly when a turn process of the session runs. A process
  of the session that the checkpoint does not know is the writer all the same and is followed. What
  the adapter lost while it was away is reported after the answer (see turn events).
- **`adapter.probe`** reports the runtime (`claude-code` and the version of `claude --version`), the
  login (`claude auth status`: valid or invalid, the method is logged and the account never read),
  the dependencies `claude` and `sidera`, whether each preset can be used (values valid, working
  directory present, record directory name within bounds) and the tool probes: for the command-line
  front end `sidera probe` is run, for MCP `sidera mcp` is started and sent `initialize` and its
  own probe on connection is waited for, each with the probe token in `SIDERA_PROBE_TOKEN` and no
  model turn; both are reported as `unsandboxed`, `failed` when `sidera` is not found, and
  `not_run` for a preset that is not configured or a front end that has no probe. It then sends the availability snapshot:
  `dependency_missing` for a program that is not found, `runtime_auth` when not logged in.

### Configuration

The manifest declares two instance keys, `claude_path` and `sidera_path`, and the preset keys
`project_dir` (the working directory), `permission_mode` (required, one of `default`,
`acceptEdits`, `plan`, `auto`, `dontAsk`, `bypassPermissions`), `model`, `effort`,
`allowed_tools`, `append_system_prompt` and `background_wait_ms` (the runtime's
`CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS`, how long a turn process waits for background tasks
before it ends, 10 minutes if unset). The output budgets are 27000 characters for the command
line and 22500 for MCP, in characters because an MCP result over 50000 characters is moved into a
file and the default limit is 25000 tokens, about one token per character for CJK text; `recv` may
wait 100 seconds on either front end. The manifest lists only macOS for now. The per-turn files live
under the instance's `state_dir`, which the daemon provides, so the adapter has no setting for
them.

### The package

```
go build -o <binary> ./cmd/sidera-adapter-claude   # with GOOS and GOARCH set
<binary> package --goos <os> --goarch <arch> --binary <binary> --out <dir>
sidera adapter install <dir>
```

writes `<dir>/sidera-adapter.json` and `<dir>/bin/sidera-adapter-claude` through
`manifest.WritePackage`, which every adapter's `package` shares. The manifest is the one
built into the executable, with `platforms` narrowed to the platform asked for, since the
package holds one binary and the installation checks the list. A platform the manifest does
not list (today anything but `darwin/arm64` and `darwin/amd64`) is refused and nothing is
written; adding a platform is adding it to the manifest.

### Creating a session, as the runtime does it

`session.create` is answered `failed` before anything is persisted or started when the preset is
unknown (`preset_unknown`), its values or working directory are invalid or its record directory
name too long (`preset_invalid`), `claude` or `sidera` is not found (`dependency_missing`), the
runtime is not logged in (`runtime_auth`) or `claude auth status` cannot be run (`native_unavailable`).
Otherwise the attempt's checkpoint is stored first: the attempt, the session uuid and first prompt
id allocated now, the record directory and working directory, the preset, the session endpoint and
a digest of the first prompt's text, and then each state `create.State` goes through. A repeated
request for the attempt with another preset, session endpoint or first prompt is `1005`; the
checkpoint holds no token, because `tool_access` is sent with every request that needs it.

The loop takes one snapshot of the process table, then reads the session file, then asks `Decide`
and does what it says, storing the new state before the next round and waiting 200 ms when it says
wait. A turn process is started with `--session-id` for a session that has no file and `--resume`
otherwise. When the first prompt is in the record the adapter stores `ready`, then the session's
checkpoint with the first turn, then reports the first turn, and only then answers `created` with
the session uuid as the native reference, the turn process's identity and the front ends. If the
first turn's process is already gone (the daemon restarted while it was creating and asks again),
the answer carries `first_turn_ended`. An attempt that failed is answered `failed` with the reason
`Reason.CreateFailure` gives, from the stored state, however often it is asked again.

### Delivering a prompt

`prompt.deliver` is `1001` for an endpoint the adapter does not hold and `refused` with
`session_lost` when the session file is gone. A prompt whose uuid is already in the record is
answered `persisted` without starting anything; the same attempt with another text is `1005`. The
session has one writer: while a turn process of it runs (the recorded one, or any process the
process table shows with the session's uuid, each counted once) the prompt is `refused` as `busy`,
and two such processes are `control_unknown`; a process that was started and never recorded is
recorded and followed. Otherwise the adapter stores the attempt's dispatch checkpoint, starts the
process with `--resume`, stores the session's checkpoint with the new turn and answers `accepted`;
`prompt.persisted` and `turn.started` follow when the prompt reaches the record. A process that
cannot be started is `refused` as `native_unavailable`. If the dispatch checkpoint cannot be stored
the request fails with an error and the daemon reconciles: a checkpoint that was never stored is how
a reconciliation proves that nothing was started.

### Closing a session

`session.close` of a session the adapter holds interrupts its turn process with SIGINT, which ends a
turn cleanly (SIGTERM leaves half a turn), waits up to 10 seconds, reports the turn's end as
`interrupted` and answers `closed`; a process that does not end is answered `close_unconfirmed` with
`turn_still_running` and is never killed. A session the adapter no longer holds is found by the
`native_ref` in the process table and treated the same, and none found is `closed`. The uuid of a
session held under another open endpoint is left alone and answered `closed`. A process table that
cannot be read is `close_unconfirmed` with `native_unreachable`. The session file stays as history;
the daemon removes the checkpoint.

### What is not built yet

`session.interrupt` (SIGINT of the turn process on Unix, with the process's end as the confirmation;
the runtime's messaging socket is another way worth testing), `recv.observed` (it needs the
runtime's `ack_markers` in the manifest and the tool results read from the record), detection of a
writer that is not the adapter's (the runtime's session registry, which `create.Observations`
accepts and the adapter leaves empty, so a session the user resumed by hand is not noticed),
process observation on Linux and Windows, `systemd-run --user` on Linux, Windows' detached start
and sandbox settings. The turn files in the state directory are not cleaned up.

### The record directory

The record directory is the base the runtime reports as its projects directory
(`projectsDirectory` of `claude auth status`, which follows `CLAUDE_CONFIG_DIR`) plus
the name of the working directory: its real path, with symbolic links resolved, in
Unicode normal form C, every character that is not an ASCII letter or digit replaced
by `-` (a character outside the basic plane is two, as the runtime replaces each
UTF-16 code unit). The runtime shortens a name longer than 200 characters and adds
a hash; the adapter does not reproduce that: a working directory whose name is
longer is `preset_invalid` at creation and not usable in a probe.

### The turn process

Every turn of a session is one `claude -p` process, started with:

```
claude -p (--session-id|--resume) <uuid> --input-format stream-json
  --output-format stream-json --verbose --strict-mcp-config --mcp-config <file>
  --permission-mode <mode> --permission-prompts none
  --allowedTools "Bash(sidera *)" mcp__sidera [--allowedTools <preset rules>]
  [--model M] [--effort E] --append-system-prompt <adapter text + preset text>
```

- The prompt is the one line on standard input, a stream-json user message whose
  `uuid` is the prompt's attempt id. Claude Code writes that uuid as the uuid of
  the user record in the session file; that is how the prompt is found there. The
  permission mode and the MCP configuration are given on every launch, because a
  resumed session does not restore them. With `--permission-prompts none` anything
  that would ask a person is denied at once. The channel's tools are always allowed,
  so no permission mode can lock the agent out of the channel. `permission_mode`
  has no default: loosening permissions is a decision the owner writes in the preset.
- argv[0] is the configured Claude executable as written. A process is recognised
  as a turn process of the session when its argv[0] is that path and its arguments
  hold `--session-id <uuid>` or `--resume <uuid>`; the resolved path of the executable
  is not used, because after an automatic update a process of the old version has an
  executable that differs from the link's present target.
- The environment is the adapter's own without the runtime's credentials, the
  markers of an enclosing Claude Code session (`CLAUDECODE`, `CLAUDE_PROJECT_DIR`, every
  `CLAUDE_CODE_*`), the channel's tokens and entries that are not valid UTF-8, then the
  `tool_access.env` of the session (`SIDERA_TOKEN`, `SIDERA_HOME`) and what the preset
  derives (`CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS`), with the directory of the
  `sidera` executable first in `PATH` for the command-line front end.
  `CLAUDE_CONFIG_DIR` is kept: it decides where the login is. The token is never
  written to a file or to argv.
- The MCP configuration of an instance is one file, `{"mcpServers":{"sidera":{"command":
  "<sidera>","args":["mcp"]}}}`, without a token: the MCP server inherits the
  environment of the process that starts it, which holds the token.
- The adapter appends a fixed system prompt that says the session is connected to
  Sidera, a channel to other agents and people in the owner's own cluster, and that
  `recv` in a wake-up notice means the `recv` tool of the `sidera` MCP server or
  `sidera recv`. Without it a session that has only the command-line front end
  cannot know. It names the tools (`recv`, `reply`, `send`, `control`, `status`,
  `ack`) and says that `recv` hands out a page that has not been acknowledged again
  at once, with the same lease, and that the page is acknowledged with `ack`
  (`sidera ack <lease>`) or by passing its lease to the next `recv` (`ack_lease`,
  `sidera recv --ack <lease>`); only after that does `recv` with a wait wait for new
  messages. A session that does not know it takes the same page, returned at once
  by every wait, for a channel with no reply. It says nothing about how to treat a
  message: that is the owner's policy and belongs in the preset's own text. The
  Codex adapter states the same facts in its developer instructions.
- The standard input is a regular file, so that reaching its end closes it and no pipe
  ties the process to the adapter; standard output and error are files too. They are
  `<attempt>.in.jsonl`, `.out.jsonl` and `.err.jsonl` in the instance's state directory,
  under `turns/<session uuid>/`.
- The process starts in a session of its own (no controlling terminal, a process group
  of its own that it leads), so a Ctrl-C in the daemon's terminal does not reach it
  and it outlives the adapter. Its identity is the PID and the start seconds and
  microseconds of the process table, the latter as the protocol's start key.

### Turn events

A Sidera turn is the life of one turn process. A process can write several `result`
lines (a resumed session may run a continuation first, and background work can wake the
session again), all of them belong to the one turn and the last one decides how it ended.

- `turn.started` carries the prompt attempt, which the daemon takes as proof of delivery,
  so it is sent when the prompt's uuid is first seen in the session record and not when
  the process starts. A process that ends before the prompt is on disk reports nothing; the
  daemon finds the prompt absent by reconciling.
- `prompt.persisted` goes before it for a prompt that `prompt.deliver` answered as
  accepted.
- `turn.ended` is sent once, when the process is gone and the turn had started. Its final
  is `interrupted` if the adapter interrupted the process, `completed` if the last
  `result` is a success without the error mark, otherwise `failed`, which includes a
  process with no result at all.
- The process is looked at before the record is read, so that a process seen as gone has
  written everything it will.
- After a restart the adapter picks the turn up from its checkpoint: a turn begun and not
  ended whose process is gone is ended, a start that was lost may be sent again, and a
  process that still runs is watched. The daemon tolerates a repeated start or end.

### Reconciliation

`prompt.reconcile` is answered from four facts: the prompt's user record is in the session
file, the attempt has a checkpoint (the adapter puts it before it starts a process),
the file could be read, and a turn process of the session runs. A prompt in the file is
`found`, with the turn `running` while its process runs and otherwise `interrupted`,
`completed` or `failed` as for `turn.ended`. Without a checkpoint it is `provably_absent`
(`never_dispatched`). A file that cannot be read, or a running process that may yet write the
prompt, is `indeterminate` (`record_unreadable`, `turn_running`). Otherwise the writer has
stopped and the whole file was scanned: `provably_absent`
(`record_complete_without_attempt`).

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
  is looked up in one directory only, which is why the record directory is part
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
  ends with a dedicated action and the dedicated reason `control_unknown`, which
  the daemon turns into a notice to the local owner. A start that timed out is the
  same case in effect: its process still runs and may still write the first
  prompt, so the adapter cannot say who controls the session. The protocol had no
  value for this, and `failed` with `native_error` would have said the runtime
  broke. The reason is therefore part of the `failed` group, and `failed` means
  "no session will be attached", not "nothing was created": a native process may
  remain.
- **Failure is persisted and final.** A process that timed out may still write
  the first prompt later, and a retry after the failure was reported would put a
  second writer beside it. The failure is persisted before it is reported, like
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

- **The record directory, not the working directory, is what Claude keys sessions by.** A
  session is found, resumed and refused as existing by its file in the record directory, so
  the creation flow and every later look use that directory and none other. It is derived
  from the real path because the runtime derives it so, and the derivation is deterministic
  and cheap, while asking the runtime for the directory of a session that does not exist yet
  is not possible. Rejected: reproducing the runtime's shortening of long names (a path can
  be shortened, which is cheaper than tracking a rule the runtime does not publish) and
  looking for the file in every directory of the projects directory (it would find a session
  by luck and not by the attempt's own directory).
- **Standard input as a file.** A pipe from the adapter would be closed when the adapter
  dies and the process would lose its input or its output; a regular file read to its end is
  the same as a closed input and survives. Output files give a restarted adapter the last
  `result` of a turn it did not watch.
- **argv[0] and the uuid recognise the process, not the executable's resolved path.** After
  an automatic update an old process runs a file that the link no longer points at; a rule
  by resolved path would lose it and start a second writer.
- **The turn is the process.** One definition serves both sides: the session is busy
  exactly while its turn process lives, so the busy state of the daemon and the adapter
  cannot fork, and a continuation or a background wake that the runtime runs inside the
  process is part of the turn instead of a turn nobody announced.
- **`turn.started` waits for the record.** It proves delivery, and the prompt is delivered
  only when it is durable; the normal wait is a second or two. The alternative, announcing at
  the start of the process, would confirm a prompt that a crash could still lose.
- **The record is judged by uuid and type.** Queue operations and attachments carry no prompt
  and other uuids, and the line is looked for in the raw file, because a later branch of the
  conversation does not remove it from disk.

- **The checkpoint before the process.** Stored first, the dispatch checkpoint makes "none" a proof:
  the adapter never starts a turn process for an attempt it has not recorded, so a reconciliation of
  an attempt without a checkpoint can say it was never dispatched without searching anything.
- **`session.create` stores the session's checkpoint before it reports the first turn.** The
  daemon accepts the checkpoint of a session that is still being created and the notifications of
  an endpoint it does not yet know as open; a crash after the first `turn.started` then leaves a
  state to pick the turn up from instead of a turn nobody follows.
- **A process that was never recorded is followed, not refused.** When a prompt finds a process of the
  session that the checkpoint does not know (the adapter died between starting it and recording it),
  refusing as busy without following it would leave the daemon waiting for a `turn.ended` that
  nobody sends. Its turn is reported from the moment it is known, with no prompt attempt, and its end
  is a failure because there is no output file of its own to read.
- **Presets are read once.** Changing the presets restarts the instance, so the adapter keeps the
  values it was given in `adapter.start` and needs no way to learn of a change.
- **No setting for the turn files.** The state directory is the daemon's place for what belongs to the
  instance and outlives its process, so a second setting would only be a second place.

## Where

- `cmd/sidera-adapter-claude/internal/create/state.go`: `State`, stages,
  transitions, `Process`, `SessionID`.
- `.../observe.go`: `Observations`, `Lookup`, `RegistryEntry`, `SessionRecord`.
- `.../decide.go`: `Decide`, `Action`, `Reason`, `Reason.CreateFailure`
  (failure reason to protocol reason), `Decision`.
- `.../params.go`: `Params`, `DefaultParams`.
- Tests: `failure_test.go` (`TestCreateFailureOfEveryReason`: every reason
  against a table written by hand), `decide_test.go` (`TestDecide` rows named after each rule,
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
- `cmd/sidera-adapter-claude/internal/claudeargs`: the argument vector, environment, user
  line, MCP configuration, preset and system prompt (`TestArgv`, `TestSanitizeEnv`,
  `TestEnviron`, `TestUserLine`, `TestParsePreset`).
- `.../internal/record`: the record directory, the session-file index and the last result
  of an output stream (`TestSanitize`, `TestDir`, `TestIndex`, `TestLastResult`).
- `.../internal/turn`: the event state machine, including the pickup after a restart
  (`TestStep`, `TestRestartAdoption`).
- `.../internal/reconcile`: the verdicts (`TestDecide`).
- `.../internal/runtime/proc`: the process table, detached start and signal (macOS; the others fail with `ErrUnsupported`).
- `cmd/sidera-adapter-claude/main.go`, `sidera-adapter.json`: the executable and its manifest
  (`TestManifestAndDeclarationAgree`); `pack.go`: the `package` subcommand
  (`TestPackageIsWrittenOnlyForAListedPlatform`).
- `.../internal/runtime`: the adapter. `app.go` (construction, initialize), `config.go` (instance
  configuration, preset resolution), `create.go` (the creation loop), `deliver.go` (delivery and
  reconciliation), `close.go`, `start.go`, `probe.go` and `probe_mcp.go`, `session.go` (turn launch,
  the turn monitor, notifications), `checkpoint.go` (the checkpoints), `world.go` (the interfaces to the
  outside) and `osworld.go` (their implementations). Tests run against an in-memory world
  (`fake_test.go`): `create_test.go` (`TestCreateAStartedSessionAndReportItsTurn`,
  `TestCreateAfterACrashBetweenStartingAndRecording`), `session_test.go` (delivery, closing,
  reconciliation, `TestStartTakesSessionsBack`, `TestProbe`). `real_test.go`
  (`-tags realclaude`, `SIDERA_REALCLAUDE=1`) probes the installed Claude Code and finds, then
  interrupts, a real turn process given no input, so it costs no model turn.
