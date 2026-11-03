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
the session record, from one listing of the process table; a process counts only if
its argument vector holds `--session-id <uuid>` or `--resume <uuid>` and its
executable is the preset's Claude, each launch once (wrapper and ancestor
processes and the caller's own observers removed); live processes only; start
times in Unix seconds from the process table, and whether the recorded process runs
from `Running` of its identity, never from its absence in the listing; the record looked up in the
attempt's record directory (`Decide` refuses another one). The listing leaves out a process whose
details the system denies (on Windows an elevated or protected one), so a process missing from it
may still run: that is why the recorded process is judged by `Running` of its identity and never
by the listing, and why a listing that fails for any other reason is an observation error reported
as an error result, never "not running". The one process this cannot show is an unrecorded one that
also denies the adapter its details, such as a terminal run elevated that resumes the session id by
hand; it belongs with the writer outside the adapter that is not noticed (see below).
Transitions are committed atomically; `Ready` and `Failed` are persisted before
`session.create` is answered, never after. `Started` is committed immediately
after spawning.

### The adapter's lifecycle

The adapter is served by `sdk/go/adapterkit`, which answers `initialize` (taking the instance
lock first), ping and shutdown and gates everything else. It declares one runtime with the
front ends `cli` and `mcp`, with `interrupt` (see [Interrupting a turn](#interrupting-a-turn))
and with `attach` (see [Attached sessions](#attached-sessions)), and neither `ack_on_behalf` nor
`server_requests`: a `claude -p` process sends the adapter no request that needs an answer, and
acknowledging on the agent's behalf is not built yet. The identity answered in `initialize` is
read from the manifest built into the executable.

- **`adapter.start`** carries the instance configuration, the presets and the sessions to take
  back. The adapter finds `claude` and `sidera` (the instance's `claude_path` and `sidera_path`,
  else the program on `PATH`, as an absolute path: a process is started with it as written; on Windows a
  `claude` that is not an `.exe`, such as the npm `claude.cmd`, is not usable: it would start through
  `cmd.exe`, so the turn process would not be the executable the adapter recognises and a multi-line
  argument would be cut, and the probe reports it as `dependency_missing` with the reason, a creation
  fails the same way and a prompt is refused as `native_unavailable`), writes the MCP configuration file into the state directory and
  keeps the presets, which are fixed for the life of the process because the host restarts the
  instance when they change. A session is `lost` when its session file is gone (Claude Code
  removes records of sessions idle longer than its cleanup period, 30 days by default),
  `unreachable` (and logged) when its checkpoint is missing or unusable, and otherwise
  `attached`, with `turn_active` true exactly when a turn process of the session runs. A process
  of the session that the checkpoint does not know is the writer all the same and is followed. What
  the adapter lost while it was away is reported after the answer (see turn events). An entry of an
  attached session is taken back differently (see [Attached sessions](#attached-sessions)).
- **`adapter.probe`** reports the runtime (`claude-code` and the version of `claude --version`), the
  login (`claude auth status`: valid or invalid, the method is logged and the account never read),
  the dependencies `claude` and `sidera`, whether each preset can be used (values valid, working
  directory present, record directory name within bounds) and the tool probes: for the command-line
  front end `sidera probe` is run, for MCP `sidera mcp` is started and sent `initialize` and its
  own probe on connection is waited for, each with the probe token in `SIDERA_PROBE_TOKEN` and no
  model turn; both are reported as `unsandboxed`, `failed` when `sidera` is not found, and
  `not_run` for a preset that is not configured or a front end that has no probe. It then sends the availability snapshot:
  `dependency_missing` for a program that is not found, `runtime_auth` when not logged in. Every
  snapshot the adapter sends holds the probe's conditions together with the `native_unavailable`
  conditions of its attached sessions, because the daemon takes each as the whole truth.

### Configuration

The manifest declares two instance keys, `claude_path` and `sidera_path`, and the preset keys
`project_dir` (the working directory), `permission_mode` (required, one of `default`,
`acceptEdits`, `plan`, `auto`, `dontAsk`, `bypassPermissions`), `model`, `effort`,
`allowed_tools`, `append_system_prompt` and `background_wait_ms` (the runtime's
`CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS`, how long a turn process waits for background tasks
before it ends, 10 minutes if unset). The output budgets are 27000 characters for the command
line and 22500 for MCP, in characters because an MCP result over 50000 characters is moved into a
file and the default limit is 25000 tokens, about one token per character for CJK text; `recv` may
wait 100 seconds on either front end. The manifest lists macOS, Linux and Windows on amd64 and arm64. The per-turn files live
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
not list (today anything but `darwin`, `linux` and `windows` on `amd64` and `arm64`) is refused and nothing is
written; adding a platform is adding it to the manifest. The `version` in the embedded
`sidera-adapter.json` is the placeholder `@version@`: the executable replaces it with the
release version (`internal/release`) when it reads the manifest, for `initialize` and for the
package alike, and a build without the release version writes `0.0.0-dev`. `<binary> version`
prints the version, the commit and the Go version.

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

The loop takes one listing of the process table, then reads the session file, then asks `Decide`
and does what it says, storing the new state before the next round and waiting 200 ms when it says
wait. A turn process is started with `--session-id` for a session that has no file and `--resume`
otherwise. A start that cannot be made is answered `failed` (`native_unavailable`) without ending
the attempt; a process that was started but cannot be identified ends the request as an error, and
the repeated request finds the process by the uuid on its command line. When the first prompt is in the record the adapter stores `ready`, then the session's
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
cannot be started is `refused` as `native_unavailable`; a process that was started but whose
identity cannot be read is an error, because it runs with the prompt and nothing can be said
to have failed or been refused. If the dispatch checkpoint cannot be stored
the request fails with an error and the daemon reconciles: a checkpoint that was never stored is how
a reconciliation proves that nothing was started.

### Closing a session

`session.close` of a session the adapter holds interrupts its turn process the way Ctrl-C does (SIGINT on Unix, a
`CTRL_C_EVENT` sent by a helper on Windows), which ends a turn cleanly (SIGTERM leaves half a turn), waits up to 10 seconds, reports the turn's end as
`interrupted` (as the process left it, when it finished the turn with a success anyway) and answers `closed`; a process that does not end is answered `close_unconfirmed` with
`turn_still_running` and is never killed. A session the adapter no longer holds is found by the
`native_ref` in the process table and treated the same, and none found is `closed`. The uuid of a
session held under another open endpoint is left alone and answered `closed`. A process table that
cannot be read is `close_unconfirmed` with `native_unreachable`. The session file stays as history;
the daemon removes the checkpoint.

### Interrupting a turn

`session.interrupt` of a session the adapter holds stops the turn that runs, the way Ctrl-C does:
SIGINT to the turn process on Unix, a `CTRL_C_EVENT` sent by a helper on Windows (the same
interrupt as the close's; SIGTERM, `CTRL_BREAK_EVENT` and `TerminateProcess` leave half a turn and
are never used). Claude Code ends the turn it was running: the process exits within about two
seconds with exit code 0, its output ends with a `result` of subtype `error_during_execution`
(`terminal_reason` `aborted_tools` or `aborted_streaming`), the session file ends with
"[Request interrupted by user for tool use]" and no `turn_duration`, and the processes of the Bash
tool end with it. So the process's end is the confirmation that the turn stopped, and the final
`interrupted` is reported for it, as for a close, unless the process finished the turn with a
successful `result` anyway (one that ended before the signal reached it): then the turn is
`completed`, as it was.

The adapter first brings the session's turn up to date, then looks at the recorded turn process
(its PID and start key; a look that fails is an error and never "not running"). A turn has begun
when its prompt is in the session record, as for `turn.started`; a process that has not got that
far has no turn to stop, and is not signalled, because a signal would end it with no trace of the
prompt, which the daemon would then find absent and deliver again. Otherwise the adapter marks the
turn as interrupted in the checkpoint before it signals (so that the process's end, however soon,
or an adapter that restarts between the two, is reported as `interrupted`), signals, remembers in
memory that the signal was delivered, and waits up to `InterruptWait` (10 seconds) for the process
to end. The mark and the memory are two facts: the mark says how the end of the turn is reported
and may outlive a signal that was never sent, so only the memory, set after the signal succeeded,
stops a second signal. A signal that fails takes the mark back. The answers:

| Situation | Answer |
| --------- | ------ |
| The process ended within the wait | `turn.ended` (`interrupted`) is reported, then `stopped` |
| The process runs after the wait, or cannot be looked at any more after the signal | `requested`; `turn.ended` is reported when the process ends. The daemon turns `requested` into `stopped` only when `turn.ended` arrives within its own confirm wait and only for a turn it knows runs |
| The session has no turn, the turn is over, its prompt is not in the record yet, or its process is gone | `not_running` |
| The process ended after it was looked at and before the signal reached it | `not_running`; the turn is no longer marked, and ends as the process left it |
| The process cannot be looked at before the signal | `refused`, `native_unreachable` |
| The signal cannot be sent | `refused`, `native_error`; the turn is no longer marked interrupted, and the next request signals |
| The endpoint is unknown (also while the session is still being created, as for a close) | error `1001` |
| The endpoint is an attached session | error `1002` |

A repeated request for a turn whose process was signalled does not signal again; it waits for the
end like the first one. The interrupts and the close of one session are served one at a time, and
a close signals a turn process in the same way, so a close does not signal a process an interrupt
already signalled and an interrupt after a close that could not signal does signal. The process is
never killed: a turn that ignores the interrupt stays `requested`. A turn that was running when the
adapter restarted is the recorded turn all the same.

### Attached sessions

A session a person opened in a terminal can ask to be attached (the management method
`sessions.attach`, made by the session's own `sidera mcp` server or `sidera` command), and the
daemon then asks the adapter with `session.attach`. The adapter does not start, interrupt or close
such a session: it follows its turns and wakes it. It needs neither `claude` nor a preset for this.

**What is read and written.** All of it lives in Claude Code's configuration directory (the
adapter's `CLAUDE_CONFIG_DIR`, else `~/.claude`):

| What | Where | Used for |
| ---- | ----- | -------- |
| The registry entry of the running process | `sessions/<pid>.json`: `pid`, `sessionId`, `cwd`, `kind`, `procStart`, `status`, `statusUpdatedAt`, `peerProtocol` or `messagingSocketPath` | that the session exists and is the same process; its status; its current session id |
| The session record (transcript) | `projects/<record directory>/<sessionId>.jsonl`, the record directory derived from `cwd` as for a created session | the user record of a delivered prompt; the end of a turn |
| The inbox socket | the wake's `CLAUDE_CODE_MESSAGING_SOCKET` (a Unix domain socket; on Windows the named pipe `\\.\pipe\LOCAL\cc-msg-<hex>`, opened with a plain file open when the message is ready, tried again every 10 ms within the 3 seconds while it is busy), one line `{"type":"user","message":{"role":"user","content":"<text>"},"uuid":"<attempt>"}` per prompt, preceded by `{"type":"auth","token":"<token>"}` when the wake has `CLAUDE_CODE_MESSAGING_TOKEN` (optional on macOS and Linux, required on Windows, each line a write of its own) | delivering a prompt; the runtime answers nothing |

The wake is the three keys `CLAUDE_CODE_MESSAGING_SOCKET`, `CLAUDE_CODE_MESSAGING_TOKEN` and
`CLAUDE_PID`, copied from the environment of the process that attached. The adapter holds it in
memory only. Nothing of it is logged, stored with the daemon (the session's checkpoint holds the
process id alone), put in an argument vector or in an error. The socket's path and the process id
are not secret, but are not logged at the information level either.

**Formats this relies on.** The registry file, the transcript's `turn_duration` record and the
socket line are not documented by Claude Code. They were checked against Claude Code 2.1.316 on
macOS and are read in two places only: package `attached` (registry, socket line, the turns) and
`record.Index` (the lines of the transcript). A form that stops matching shows, never silently: an
unreadable registry entry is `native_error` at attachment and, if it stays so for a second, an
error in the log and `native_unavailable` for the endpoint; a transcript whose user lines lack a
`uuid` or `sessionId` makes a prompt undecidable (`record_unreadable`); a turn that the transcript
never ends is ended by the registry after two seconds as `interrupted` and a warning is logged.
`procStart` is kept as written, because its form depends on the platform: on macOS it is read as
`ps -o lstart` in UTC and compared with the process table's start within two seconds; on Linux it is
the start in clock ticks after boot (field 22 of `/proc/<pid>/stat`) and on Windows the creation time as
a decimal FILETIME, each compared for equality with the process table's native start. A form of another
platform is an unreadable entry (`native_error`). That is how a registry entry whose process number was
reused by another process is told from a live session.

**`session.attach`** reads the registry entry of `CLAUDE_PID` and answers:

| Found | Answer |
| ----- | ------ |
| no entry, the process is gone, the process started at another time than the entry says, or the entry is not `kind` interactive | `not_found` |
| an entry that is not in the form the adapter reads | `failed`, `native_error` |
| the entry or the process table cannot be read | `failed`, `native_unreachable` |
| the wake has no `CLAUDE_PID`, or no socket (the detail says "too old" when the entry lists no messaging support) | `failed`, `wake_unavailable` |
| the session, and a wake with a socket | `attached` |

The session is followed and its endpoint is held also when the answer is `wake_unavailable`, so a
later attachment that brings the socket makes it work. A repeat for the endpoint replaces the wake
(none sent means none held) and finds the session again; it starts nothing twice. The first attachment
stores the session's checkpoint (the process id), and reads the transcript once to pass over its
history: only what is written afterwards is followed.

**`adapter.start`** takes an attached entry back from the process id in the wake or, as the daemon
holds the wake in memory only and may have none after a restart, in the checkpoint. The answer is
`attached` with `turn_active` true when the registry shows a turn running, `lost` when the session
is gone, and `unreachable` when nothing names its process or the registry cannot be read. A session
without a wake is followed; it waits for the next `session.attach` to bring the wake, and until then
refuses prompts as below.

**Turns.** One follower per session turns the registry's `status` and the transcript into
notifications:

- `busy` or `waiting` (a permission prompt) is a turn running; `idle` and `shell` (the turn is over
  and a background command runs) are not; a status the adapter does not know counts as no turn, and
  is logged once. A turn the registry shows begins with `turn.started` without an attempt, `person-<time
  of the status>` as its reference. A turn also begins with `turn.started` without an attempt when
  the transcript shows a user record of origin `human`, `peer` or `task-notification` and no turn is
  running (`person-<uuid of the record>` is its reference): a session that stays busy from one turn
  to the next keeps its status and the time of it, so the registry shows no new turn. A message of
  those origins that arrives in a turn that runs begins none. A record of such an origin that is not
  the start of a turn (for example one written after the turn's end for a message queued during it)
  would show as a short turn of the person, ended by the registry's idle status.
- A prompt written by the adapter is found by its attempt, which is the uuid of the message and
  becomes the uuid of the user record. When that record is in the transcript the adapter sends
  `prompt.persisted` and, if no turn runs, `turn.started` with the attempt (the reference is the attempt).
  If a turn already runs, the session takes the message in at its next tool boundary and starts no
  turn: only `prompt.persisted` is sent, and the turn goes on without an attempt.
- A turn ends with `turn.ended` `completed` when the transcript's `turn_duration` record follows. If the
  transcript never shows it, the end is told from the registry once no turn has run for two seconds
  (longer than the 0.1 to 0.3 seconds the runtime takes to write it), as `interrupted`: the
  likeliest cause is that the person stopped the turn.
- The registry is the fast signal (about a tenth of a second) and the transcript the exact one. A
  look reads the registry (for the process and the session id), then the transcript, then the
  registry again, and acts on the last reading, so that the status is never older than the records
  it goes with: a status read before the transcript could show a turn that the records already
  ended. A status that belongs to a turn the transcript already ended is not a new turn. (A second
  reading that fails or names another session leaves the first one in use.)
- `/clear` gives the process a new session id and a new record file: the follower reads the
  registry's `sessionId` at every look. It first reads what is left in the old file (a turn end or a
  prompt written just before the clear is told), then ends a running turn as `interrupted` and
  follows the new file. The endpoint keeps the id it was attached with.
- A session whose entry or process is gone has its running turn ended as `interrupted` and the endpoint
  reported `native_unavailable`; the reports stop when it comes back, or at once when an attachment or a
  delivery reads its registry successfully. A session that is down is looked at every five seconds
  instead of five times a second. What its transcript still shows is told before the turn ends.

**`prompt.deliver`** answers a repeat of the attempt from what it knows (`1005` for another text). The
adapter first reads the registry: a session that is gone, or one with no socket in its wake, is
`refused` as `native_unavailable` and the endpoint is reported `native_unavailable` (the endpoint stays
open; the condition is cleared by the next delivery that is written, or by an attachment with a new
socket). It then stores the attempt's dispatch checkpoint, which `prompt.reconcile` takes as proof that
something may have been written, and writes the lines. A socket that cannot be connected is a refusal,
as nothing was written; a failure after the connection was made is an error, since the message may have
arrived and the transcript decides. The answer is `accepted`, or `persisted` for a repeat whose record
was seen: delivery is proved by the record, never by the write.

**`prompt.reconcile`** reads the transcript of the session now running: the attempt's user record
decides `found`, the turn being `running` (the registry shows a turn and none ended since the prompt),
`completed` (a `turn_duration` follows the record) or `interrupted` (neither); an attempt without a
checkpoint is `provably_absent`; an attempt that is not in the record is `indeterminate` while the
session is busy or its registry cannot be read, and `provably_absent` otherwise. A prompt written just before a `/clear` is in the file before it and is judged absent.

**`session.close` and `session.interrupt`** are answered `1002`, and the person's session is not
touched. `session.detached` stops the watch, drops the wake and clears the endpoint's availability
condition; the same holds for an endpoint that `adapter.start` no longer lists, and a delivery
that was under way cannot bring the condition back. `recv.observed` is never sent for an attached session, and the adapter acknowledges nothing
on the session's behalf.

**Guarantees and limits.**

- A prompt is delivered to an idle session within about a tenth of a second. Written to a busy session
  it is taken in at its next tool boundary; written to one waiting on a permission prompt it is held
  until the person answers. A controller in `bypassPermissions` mode may hold a wake-up for approval
  as well (this could not be checked).
- A prompt can be reported as taken into a person's turn when the registry showed the turn a moment
  before the record appeared: the delivery is proved all the same.
- Delivery on Windows cannot tell a wrong token from a delivery. Claude Code there closes a connection
  whose first line is not a valid authentication line and delivers nothing, but it answers nothing, so
  the write of the message reports a failure only when the pipe was already closed by then; a message
  that was dropped is found missing by the reconciliation, as for any delivery whose outcome is unknown.
  The wake carries the right token (the session's own `CLAUDE_CODE_MESSAGING_TOKEN`), so this is for a
  wake that went stale.
  A wake with no token at all is the same case that is known in advance, so on Windows it is refused
  (`wake_unavailable` at the attachment, `native_unavailable` at a delivery, with the reason in the status
  and the log) before anything is written, rather than answering accepted for a message that is certain
  to be dropped. On macOS and Linux the token is optional.
- Only the transcript of the session now running is read: after a `/clear` the history of the old
  session is not looked at again. A record directory name over 200 characters cannot be located: such a
  session is followed from the registry alone, and its prompts are never reported persisted.
- Turns shorter than a look (a fifth of a second) the person starts are not seen from the registry;
  the transcript shows them when it carries their start. One the adapter's prompt starts is told from
  the transcript.
- The adapter remembers the last 128 prompts it delivered, for a repeat of an attempt to be told from
  a new one; a prompt older than that, whose record never came, is no longer waited for, and its
  record, if it comes, shows as a message of another sender.

### What is not built yet

Claude's operating-system sandbox, which would make the denial of the data directory hold against
Bash (see the turn process), `recv.observed` (it needs the
runtime's `ack_markers` in the manifest and the tool results read from the record), detection of a
writer that is not the adapter's (the runtime's session registry, which `create.Observations`
accepts and the adapter leaves empty, so a session the user resumed by hand is not noticed, nor is
one the listing denies the adapter),
and sandbox settings. The turn files in the state directory are not cleaned up.

### The record directory

The record directory is the base the runtime reports as its projects directory
(`projectsDirectory` of `claude auth status`, which follows `CLAUDE_CONFIG_DIR`) plus
the name of the working directory: its real path, with symbolic links resolved (on Windows also
every name in its on-disk case and long form, never the 8.3 short name, and junctions followed:
`sdk/go/adapterkit/realpath`; Claude Code does not expand a short name, so the turn process is
started with the resolved path as its working directory; an attached session is looked for in the
record directory of the working directory its registry entry gives, expanded the same way, so one
opened in a literal 8.3 path on Windows is looked for in a directory Claude Code did not use and its
prompts are never reported persisted), in
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
  --allowedTools "Bash(sidera me *)" "Bash(sidera peers *)" ... mcp__sidera [--allowedTools <preset rules>]
  --disallowedTools "Read(//<data dir>/**)" "Edit(//<data dir>/**)"
  [--model M] [--effort E] --append-system-prompt <adapter text + preset text>
```

- The prompt is the one line on standard input, a stream-json user message whose
  `uuid` is the prompt's attempt id. Claude Code writes that uuid as the uuid of
  the user record in the session file; that is how the prompt is found there. The
  permission mode and the MCP configuration are given on every launch, because a
  resumed session does not restore them. With `--permission-prompts none` anything
  that would ask a person is denied at once. The channel's tools are always allowed,
  so no permission mode can lock the agent out of the channel, and only they are
  (next bullet). `permission_mode`
  has no default: loosening permissions is a decision the owner writes in the preset.
- The channel's tools are the MCP tools (`mcp__sidera`) and the `sidera` commands
  of the agent interface, one rule each: `me`, `peers`, `directory`, `declare`,
  `send`, `reply`, `recv`, `ack`, `status` and `control` as `Bash(sidera <command> *)`
  (the space before `*` makes the match a whole word and also admits the command
  with no arguments), and `Bash(sidera --help)`. No other `sidera` command is
  allowed, so a session that runs one in `dontAsk` or any other mode is denied:
  `preset`, `trust`, `adapter`, `identity`, `daemon`, `network`, `endpoint`, `node`,
  `alerts`, `clock`, `init`, `mcp`. `sidera mcp` needs no rule, because Claude
  Code starts it from the MCP configuration, not through Bash. Claude Code checks
  every part of a compound command (`;`, `&&`, `|`, `$(...)`) against the rules,
  so an allowed command cannot carry a denied one. One gap: `status` with a
  message identifier is an agent command, but without one the CLI runs the owner's
  node status, and a rule cannot tell them apart; it is read-only. A preset's own
  `allowed_tools` are the owner's decision and are added after these.
- The data directory is denied to Claude's own file tools on every turn, as best-effort
  protection (`claudeargs.DataDirDenyRules`). The directory holds the endpoint tokens
  of every session and the daemon's keys; a session reaches the channel through its
  own endpoint and has no business in it. The path is the `SIDERA_HOME` of the
  session's tool access with symbolic links resolved (Claude Code checks a path and
  the file it resolves to against the rules); a launch whose tool access has none is
  an error, not a session that runs undenied. The rules are `Read` and `Edit` only:
  a `Read` deny also covers Grep, Glob and the Write tool on the same path, and Claude
  Code never matches a `Write` rule. In a rule an absolute path starts with `//`
  (`//home/u/.sidera/**`; one `/` is relative to the settings source, which for a
  command-line rule is the working directory), and on Windows the rule is written
  against the POSIX form of the path (`C:\Users\u\Sidera` is `//c/Users/u/Sidera/**`);
  characters that are patterns in a rule (`\ * ? [ ]`) are escaped. The rules are
  command-line `--disallowedTools` because a deny beats an allow in every settings
  scope: neither the preset's `allowed_tools` nor the owner's own Claude settings lift
  it. The channel's `sidera` commands are not file commands, so the rule does not
  touch them.
- The denial is not a boundary. Claude Code applies a read rule to its file tools and
  to the Bash commands it recognises as file commands (`cat`, `head`, `sed`,
  redirections), but not to a command that reads without naming the file: with the
  owner's settings allowing `Bash(grep *)`, `grep -r <parent of the data directory>`
  still returns the files, and a script that opens files itself is not covered either.
  A guarantee needs Claude's operating-system sandbox (`filesystem.denyRead`, on macOS
  and Linux only), which the adapter does not use yet.
- argv[0] is the configured Claude executable as written. A process is recognised
  as a turn process of the session when its argv[0] is that path and its arguments
  hold `--session-id <uuid>` or `--resume <uuid>`; the resolved path of the executable
  is not used, because after an automatic update a process of the old version has an
  executable that differs from the link's present target.
- The environment is the adapter's own without the runtime's credentials, the
  markers of an enclosing Claude Code session (`CLAUDECODE`, `CLAUDE_PROJECT_DIR`, every
  `CLAUDE_CODE_*` but `CLAUDE_CODE_GIT_BASH_PATH`, which describes the machine and tells Claude
  Code on Windows where Git Bash is), the channel's tokens and entries that are not valid UTF-8, then the
  `tool_access.env` of the session (`SIDERA_TOKEN`, `SIDERA_HOME`) and what the preset
  derives (`CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS`), with the directory of the
  `sidera` executable first in `PATH` for the command-line front end. Names are compared as the
  platform does, without case on Windows, so a `Path` is the `PATH` the directory goes in front of
  and a variable is stripped whatever its spelling. `CLAUDE_CONFIG_DIR` is kept: it decides where the login is. The token is never
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
- The process is started by `sdk/go/adapterkit/process` so that it outlives the adapter and the
  daemon: on Unix in a session of its own (no controlling terminal, a process group it leads) and,
  on Linux, in a systemd user scope when the user manager allows it; on Windows outside the
  adapter's job when the job allows it, with a hidden console of its own so that it can be
  interrupted. Its identity is the PID and the platform's start key (macOS: the kernel's start
  seconds and microseconds; Linux: start ticks and the boot id; Windows: the creation FILETIME),
  which is the protocol's start key. The container the start reports (`systemd_unit`,
  `process_group` or `none`) is stored with the turn and reported with the process identity.
- How far a start escaped the adapter is the `Detachment` the process package returns with it
  (`proc.Started.Detachment`). A start that does not outlive the daemon is made all the same and
  logged at Warn with the reason; a caveat of one that does is logged at Info. The probe logs the
  same for what the host would achieve (`proc.Launcher.Host`), because the probe result has no place
  for it: a dependency is only ok, missing or the wrong version, and adding one would change the
  protocol. So the one place an operator reads that a turn would end with a daemon restart is the
  adapter's log. On Windows that is a daemon in a job that forbids breakaway and kills its
  processes when it closes; on Linux a start without a usable user bus is in a session of its own,
  and without linger a scope ends with the user manager at the last logout.

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
  is `completed` if the last `result` is a success without the error mark, otherwise
  `interrupted` if the adapter interrupted the process (or was about to, and the signal
  failed after the mark was stored), otherwise `failed`, which includes a process with no
  result at all.
- The process is looked at before the record is read, so that a process seen as gone has
  written everything it will.
- The session's checkpoint is stored when the turn's state changes, not at every look: a turn is
  looked at every poll for as long as it runs, and each store is a durable transaction with the
  daemon. A store that failed is tried again at the next look.
- After a restart the adapter picks the turn up from its checkpoint: a turn begun and not
  ended whose process is gone is ended, a start that was lost may be sent again, and a
  process that still runs is watched. The daemon tolerates a repeated start or end.

### Reconciliation

`prompt.reconcile` is answered from these facts: the prompt's user record is in the session
file, the attempt has a checkpoint (the adapter puts it before it starts a process), the file
could be read, every user line of the file carries a uuid and a session id (the form the lookup
relies on), the attempt's own output stream holds a successful result, and a turn process of
the session runs. The process table is read before the file, as for `turn.ended`.

| Facts, in the order they are applied | Verdict | Grounds |
| ------------------------------------ | ------- | ------- |
| the prompt's user record is in the file | `found`, the turn `running` while its process runs, otherwise `interrupted`, `completed` or `failed` as for `turn.ended` | `attempt_in_record` |
| no checkpoint | `provably_absent` | `never_dispatched` |
| the file is missing or cannot be read | `indeterminate` | `record_unreadable` |
| a user line has no uuid or no session id | `indeterminate` | `record_unreadable` |
| the attempt's process ended with a successful result, yet its uuid is not in the file | `indeterminate` | `record_unreadable` |
| a turn process of the session runs (the recorded one, or any the process table shows) | `indeterminate` | `turn_running` |
| none of the above: the writer has stopped and the whole file was scanned | `provably_absent` | `record_complete_without_attempt` |

A running turn process is the recorded one (its PID held at the recorded start) or any process
the table shows with the session's uuid on a command line of the preset's Claude, each counted
once; the recorded one counts even when its command line is not matched, as for a `claude` found
at another path.

## Why

- **No `Bash(sidera *)`.** The management interface identifies the owner by the
  user's uid alone and has no token (see [local-ipc](../local-ipc/README.md)), so
  every `sidera` command that reaches it acts as the owner. A session that was
  allowed all of `sidera` could, whatever its permission mode and on the word of
  any trusted node, add a preset with full access, change trust, retire the
  identity or stop the daemon. The allowed commands are the ones that use the
  session's own token, listed in one place (`claudeargs.ChannelToolRules`).
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
- **PID plus start.** The system reuses PIDs. A PID alone would make a
  recorded process look alive forever when an unrelated process took its number,
  and the attempt would wait for ever. The runtime decides that a process it
  knows still runs with the process table's `Running` of its PID and start key,
  and an error of the table is an error, never "gone". The creation flow also
  keeps the start as an `int64` of Unix seconds, the same unit as every time on
  the wire, and compares it for equality. Each platform derives it from its
  finest source, truncated to seconds: Linux from the boot time plus the start
  ticks (the boot time moves when the clock is set, which is why the start key
  and not the seconds decides whether a process is the recorded one), macOS from
  the process start seconds, Windows from the creation time. A time type with its
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
- **Absence is concluded only from a record form that was verified.** The lookup relies on a
  user line carrying the stream-json uuid as its `uuid` and the session in `sessionId`. A Claude
  Code release that changed that would leave every prompt unfound: each turn would end
  abandoned, reconciliation would call each prompt absent and the daemon would send it again,
  for ever and without a sound. Two checks turn that into `record_unreadable`: a user line
  without a string `uuid` and `sessionId` (every user line of the records read so far has both),
  and a turn whose own output ends in a successful result although its prompt is not in the file
  (a process that worked on a prompt wrote it). An error result does not count, because a
  process that fails before it writes anything, such as a resume of a session that is gone, also
  ends in one.

- **The checkpoint before the process.** Stored first, the dispatch checkpoint makes "none" a proof:
  the adapter never starts a turn process for an attempt it has not recorded, so a reconciliation of
  an attempt without a checkpoint can say it was never dispatched without searching anything.
- **`session.create` stores the session's checkpoint before it reports the first turn.** The
  daemon accepts the checkpoint of a session that is still being created and the notifications of
  an endpoint it does not yet know as open; a crash after the first `turn.started` then leaves a
  state to pick the turn up from instead of a turn nobody follows.
- **A started process is never reported as a failed start.** The launcher says which of the two
  happened (`ErrStartedUnidentified` for a process that runs but whose identity could not be read,
  any other error for nothing started), and the handlers map it where they decide the outcome. A
  refusal or a failure promises that nothing ran; for a process that runs with the prompt that
  would send the daemon to deliver it again beside the first. An error leaves the daemon to
  reconcile, which finds the prompt in the record.
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
  of an output stream (`TestSanitize`, `TestDir`, `TestIndex`, `TestIndexMarkersFollowTheFile`,
  `TestIndexMarksTheUserRecordsThatBeginATurn`, `TestIndexForgetsAnUnwatchedUUID`, `TestLastResult`).
- `.../internal/attached`: the registry entry, the wake and the socket line, and the follower of an
  attached session's turns (`TestParseRegistry`, `TestProcessStartForms`, `TestParseRegistryRefusesAFormItDoesNotRead`,
  `TestWakeLines`, `TestWakeNeverPrintsItsSecrets`, `TestTranscriptSample`, and the follower's tests
  in `follow_test.go`, among them `TestBackToBackTurnsAreToldFromTheTranscript` and
  `TestFinishTellsTheMarkersFirst`), on the synthetic samples in `testdata/`.
- `.../internal/turn`: the event state machine, including the pickup after a restart
  (`TestStep`, `TestRestartAdoption`).
- `.../internal/reconcile`: the verdicts (`TestDecide`).
- `.../internal/runtime/proc`: the process table, detached start and interrupt, over `sdk/go/adapterkit/process` on every platform (`TestLauncherStartsAProcessTheTableFindsAndInterruptEnds`, and on macOS `TestStartKeyOnMacOSIsTheFormCheckpointsHold`). The inbox pipe writer on Windows is tested in `inbox_windows_test.go` against a stand-in server.
- `cmd/sidera-adapter-claude/main.go`, `sidera-adapter.json`: the executable and its manifest
  (`TestManifestAndDeclarationAgree`); `pack.go`: the `package` subcommand
  (`TestPackageIsWrittenOnlyForAListedPlatform`).
- `.../internal/runtime`: the adapter. `app.go` (construction, initialize), `config.go` (instance
  configuration, preset resolution), `create.go` (the creation loop), `deliver.go` (delivery and
  reconciliation), `close.go` and `interrupt.go` (the signal and the wait they share are in `close.go`), `start.go`, `probe.go` and `probe_mcp.go`, `attached.go` (attached sessions), `availability.go` (the conditions
  sent), `session.go` (turn launch,
  the turn monitor, notifications), `checkpoint.go` (the checkpoints), `world.go` (the interfaces to the
  outside) and `osworld.go` (their implementations). Tests run against an in-memory world
  (`fake_test.go`): `create_test.go` (`TestCreateAStartedSessionAndReportItsTurn`,
  `TestCreateAfterACrashBetweenStartingAndRecording`), `session_test.go` (delivery, closing,
  reconciliation, `TestStartTakesSessionsBack`, `TestProbe`), `interrupt_test.go` (`TestInterrupt`,
  `TestRepeatedInterruptDoesNotSignalAgain`, `TestConcurrentInterruptsAreServedOneAtATime`,
  `TestInterruptThatCannotSignalLeavesTheTurnAsItWas`, `TestInterruptBeforeTheTurnStarted`,
  `TestTurnIsMarkedInTheCheckpointBeforeTheSignal`, `TestInterruptAfterACloseThatCouldNotSignal`), `attached_test.go` (attached sessions:
  `TestAttachOutcomes`, `TestADeliveredPromptIsWrittenAndFollowedToTheEndOfItsTurn`,
  `TestATurnThePersonStartsIsReportedWithoutAnAttempt`, `TestClearIsFollowed`,
  `TestAGoneSessionRefusesPromptsAndIsReportedUnavailable`, `TestAnAttachedEndpointIsNotClosedOrInterrupted`,
  `TestDetachingStopsTheWatch`, `TestAnAttachedSessionIsTakenBackAtStart`, `TestTheWakeNeverReachesTheLog`,
  `TestAStatusReadBeforeTheTranscriptOpensNoTurn`, `TestBackToBackTurnsWhileTheSessionStaysBusy`,
  `TestTheEndOfATurnWrittenJustBeforeAClearIsNotLost`, `TestAReachableSessionIsNotReportedUnavailableAnyLonger`,
  `TestADroppedEndpointGetsNoConditionBack`, `TestPromptsNeverSeenAreForgottenWithTheMemoryOfThem`). `real_test.go`
  (`-tags realclaude`, `SIDERA_REALCLAUDE=1`) probes the installed Claude Code, finds and
  interrupts a real turn process given no input, and has `session.interrupt` stop one, so it
  costs no model turn. How a real turn that runs a model ends under the interrupt was measured by
  hand on each platform and is not tested, as it would cost turns.
