# Codex adapter

How the Codex adapter drives Codex: it keeps a private Codex App Server, makes
each session a thread of that server, and maps the adapter protocol onto the
server's own. The adapter lives under `cmd/sidera-adapter-codex/`, outside the
top-level `internal/`, so the core cannot import it and contains no product
name. The protocol it implements is in
[adapter-protocol](../adapter-protocol/README.md); the serve loop it shares with
every adapter is `sdk/go/adapterkit`.

The App Server is Codex's experimental JSON-RPC interface (`codex app-server`).
The adapter is written against one release of it and is held to that release by a
test that reads the schema the installed Codex generates (see "Where").

## What

### The App Server

The adapter runs `codex --no-daemon app-server` itself and does not use the
managed daemon of `codex app-server daemon`. The managed daemon is shared with the
owner's other Codex programs, its `update` interrupts work in progress, and its
version is not ours to fix.

| Platform | Listens on | Reached by |
| -------- | ---------- | ---------- |
| Unix | `--listen unix://<state_dir>/app-server.sock` | WebSocket over the Unix socket. Codex makes the socket `0600` in a `0700` directory, symlinks it at the path given, and takes a lock per path, so a second server for the same path cannot run. |
| Windows | `--listen ws://127.0.0.1:<port> --ws-auth capability-token --ws-token-file <state_dir>/ws-token` | WebSocket over loopback with `Authorization: Bearer <token>`. The port is found by binding port 0 and releasing it; the port, the pid and the process's start time are in `app-server.json`, the token (`0600`, random, 32 bytes) in `ws-token`. |

`<state_dir>` is `instance.state_dir` of `initialize`. On Unix the socket path must
fit the socket address limit; the adapter refuses a longer one with a message that
says so (the limit is checked at 100 bytes, which fits macOS's 104).

**Connect or start.** At `adapter.start` the adapter first tries the address. A
server that answers is used: the server is started detached so that it outlives the
adapter, and finding the one a previous run left is the normal case after the
adapter restarts, with its turns still running. Otherwise it starts one and waits up
to 15 seconds for it to answer. A server that exits before it answered ends the wait
with an error that names the log and says an earlier server may hold the socket.
Codex replaces a stale socket left by a server that was killed.

**A server that is up but does not answer.** On Windows nothing like the socket
lock keeps a second server from running beside the first, and a second server
would overwrite `ws-token` and take the address, leaving the first with threads
and no way in. So when the probe of the recorded address fails, the adapter
checks `app-server.json` before it starts anything: if its pid is a running
process whose start time is the one recorded (a pid alone could be anyone's now;
a file without a start time is checked for a running process only), a server
exists. It is probed again after 250 ms, 500 ms, 1 s and 2 s, each probe bounded
at 2 seconds; the first answer is used. When none comes, the attempt fails with an
error that names the pid and the address, no server is started, and the
reconnect loop tries again later. A process that ends during the wait is
replaced by a start. The token file is written when a start begins, because the
server reads it as it starts; it is reached only when no running server is
recorded, so a running server's token is never overwritten. On Unix the lock per
socket path already makes a second server impossible, and a failed probe goes
straight to a start, which ends with the error above when an earlier server holds
the socket.

**Detached start.** On Unix the child gets a session of its own (`setsid`), so the
signals aimed at the adapter's process group do not reach it and its pid is the id
of its process group. On Windows it is created with `CREATE_NEW_PROCESS_GROUP |
DETACHED_PROCESS | CREATE_BREAKAWAY_FROM_JOB`; when the job the adapter runs in
forbids breakaway (`ERROR_ACCESS_DENIED`) it is started again without that flag and
the log says its life then ends with the job. Standard input is the null device;
standard output and error are appended to `<state_dir>/app-server.log`, which is
moved to `app-server.log.1` at each start.

**Environment.** The server is started with the adapter's environment less: a pair
that is not valid UTF-8 (the server refuses to start on one); `SIDERA_TOKEN` and
`SIDERA_PROBE_TOKEN`, which belong to one session and would be seen by every
thread; `CODEX_THREAD_ID`, `CODEX_SESSION_ID`, `CODEX_SANDBOX*`, `CODEX_CI` and
`CODEX_TOOL_CALL_ID` (the daemon may itself run inside a Codex session); `CLAUDECODE`
and `CLAUDE_CODE_*`. The owner's Codex configuration (`CODEX_HOME`, `config.toml`)
is left alone and loads as usual.

**Ending it.** Only a server this process started and still holds is ever ended
(`Server.Stop`): the process group gets `SIGTERM`, then `SIGKILL` after three
seconds. A server another process started is never touched, because a pid in a file
could be anyone's by now: the check above errs toward waiting, while a wrong guess
here would end someone else's process. The adapter's own shutdown does not stop the
server. On Windows only the process itself
is ended, not what it started.

**Versions.** `adapter.probe` reports the version of the `codex` program
(`codex --version`). The server's own version is in the user agent it answers
`initialize` with. When the two differ (Codex was upgraded under a running server)
the probe reports the `app-server` dependency as `wrong_version`; the adapter does
not restart a server it did not start. Neither the manifest nor the adapter names a
version it was tested with, and nothing judges one: Codex releases several times a
day, so a list could not stay true. Whether `prompt.reconcile` may conclude that a
prompt is absent rests on what the adapter can verify of the record (below), not on
the version.

### The connection

`internal/appserver` is a small client, written by hand rather than generated or
taken from a library:

- **Frames have no `"jsonrpc"` member**, in either direction, so the peer of the SDK
  (which requires it) cannot be used. A frame with a method and an id is a request
  of the server, with a method only a notification, with an id only an answer.
  Unknown members are ignored; the members the adapter depends on are checked by the
  schema guard test.
- **A page of turns read with full items must be in the recognized form.** A prompt is
  found by the `clientId` of a `userMessage` item, and decoding ignores what it does
  not know, so a record in another form would read as holding no prompts. Every turn
  of such a page must therefore have its `items` and an `itemsView` of `full`, and
  every `userMessage` item a `clientId` (null when the prompt had none); a page that
  does not is the error `ErrUnrecognizedRecord`. Callers treat it like a record that
  could not be read, so `prompt.reconcile` answers `record_unreadable` instead of
  concluding absence. The server writes these members always, and the schema guard
  holds them against the installed Codex.
- **The read limit is 32 MiB.** The server sends messages up to 16 MiB; the WebSocket
  library's default limit of 32 KiB would end the connection on the first large read
  of a thread.
- **Requests of the server are answered on the read loop**, at once, from a pure
  decision; **notifications are queued without bound and handed to one goroutine in
  arrival order**, so the read loop never waits for the adapter, and a notification
  is never dropped. `Done` closes after the last queued notification was handled.
- **The adapter opts out of** streamed output and reasoning
  (`optOutNotificationMethods`): they are most of the traffic and nothing reads them.
- **A connection only receives the events of the threads it started or resumed.**
  The adapter keeps one connection for all its sessions and resumes each thread in it.
  When the last connection that holds a thread ends, the server unloads the thread.
  After a connection ends the adapter says `native_unavailable`, reconnects with a
  backoff (starting the server if it is gone), resumes every session with its
  configuration, and settles its books on the turns from the server's list: a turn it
  thought running that ended is reported ended (a turn the server no longer lists is
  `failed`), and a turn running that it did not know is reported started. Every new
  connection starts with every session's thread marked not loaded, and a thread is
  marked loaded only by the resume that loaded it. A resume that fails for a reason that
  says nothing of the thread is tried again by the same reconnect loop, with the same
  backoff, until every session is loaded, and by the next `prompt.deliver` of the session,
  which loads the thread before it asks anything of it.

### Credentials of a session

Every session's agent must reach the daemon with its own `SIDERA_TOKEN` and
`SIDERA_HOME`, though all sessions run in one server. The thread's own `config`
parameter does it, in `thread/start` and again in every `thread/resume`:

```
shell_environment_policy.set = {SIDERA_TOKEN, SIDERA_HOME}
mcp_servers.sidera = {command: <sidera program>, args: ["mcp"],
                      env: {SIDERA_TOKEN, SIDERA_HOME},
                      default_tools_approval_mode: "approve", tool_timeout_sec: 120}
```

The values are the `tool_access.env` of `session.create` or of the session's entry in
`adapter.start`. They are held in memory only: not in a checkpoint, not in a file.

Measured on Codex 0.164.0 with two threads in one server, each with its own pair:

- The commands of each thread see only its own pair, both through `thread/shellCommand`
  and in a model turn (where the sandbox is `seatbelt` and the command still sees them).
  The MCP server each thread starts, a process of its own with its own process group,
  has the thread's pair in its environment and no `CODEX_THREAD_ID`: a fallback that
  maps a thread id to its token could not work for the MCP front end.
- **A thread's `config` is not kept.** After the server restarts, a thread resumed
  without it has no pair; resumed with it, it has.
- **A thread's `config` replaces the `-c` overrides the server was started with** for
  that thread, so the adapter gives the server none and puts everything in the
  thread's configuration. The owner's `config.toml` loads as usual.
- With an approval policy of `never`, a tool call that needs approval fails
  ("requires approval, but approval policy is never"), so the channel's MCP server is
  approved in advance with `default_tools_approval_mode`.
- The unsandboxed `thread/shellCommand` and the MCP server started outside the sandbox
  see the pair whatever the sandbox of the preset is.

The token is an identification, not a security boundary: the session's own user can
read it from the thread's environment, Codex's logs hold the environment it gave a
command (`~/.codex/logs_2.sqlite`), and a model that prints its environment shows it.
It stops working when the session is closed.

### Methods

| Protocol | What the adapter does |
| -------- | --------------------- |
| `initialize` | Declares a runtime with `interrupt: false`, `ack_on_behalf: false`, `server_requests: true` and front ends `mcp` and `cli`. It does not touch the server. |
| `adapter.start` | Reads the configuration, finds the `codex` and `sidera` programs, connects or starts the server, `initialize` and `account/read`, then resumes every session listed: `attached` (with `turn_active` when the thread runs a turn), `lost` when the server has no record of the thread (`no rollout found`), `unreachable` otherwise or when the preset is unusable. It ends with a full availability snapshot. A server that cannot be reached is not a failure of `adapter.start`: every session is `unreachable`, the snapshot says `native_unavailable` and the adapter goes on reconnecting. |
| `adapter.probe` | The runtime version; `auth` from `account/read` (`valid` with an account, `invalid` without, `unknown` when the server is unreachable); the dependencies `codex`, `sidera` and `app-server`; each preset's problems and whether its directory exists; the tool probes. |
| `session.create`, `prompt.deliver`, `prompt.reconcile`, `session.close` | Below. |
| `session.interrupt` | Not declared. The id of the running turn is kept, so that `turn/interrupt` can be added without a change of format. |
| `checkpoint.put` | `scope: attempt` while creating, and only there. |

**Availability.** A full snapshot at `adapter.start`, and again whenever it changes:
`dependency_missing` when `codex` or `sidera` is not found, `native_unavailable` while
there is no connection to the server, `runtime_auth` when the server has no account.

**Tool probes.** The MCP front end is started by Codex outside the sandbox, so its
probe runs `sidera mcp` the same way, with `SIDERA_PROBE_TOKEN` and the request's
environment, sends it an MCP `initialize` (which makes it probe the daemon itself) and
waits up to ten seconds for the line on its standard error that says the probe
succeeded or failed; the result is `ran` with method `unsandboxed`, or `failed` when the
server did not answer or says it could not reach the daemon. The command-line
front end runs in the preset's sandbox, so its probe is `command/exec` of
`sidera probe` under that sandbox policy, through the server, which costs no model
turn: `ran` with method `sandboxed`, or `failed` with the exit code and the first line
of its standard error, and `failed` when the server cannot be reached. A preset that has no
`codex` section (the `default` preset is one; it reaches the adapter as no values at all) gives
`not_run`: the probe did not apply, and the daemon counts it for neither the pass nor the
failure. When `codex` is not found or `codex --version` does not run, every tool probe of a
usable preset is `failed` with that as the detail, and the `codex` dependency is `missing`: the daemon
judges the probe by its tool probes alone, and a server left running by an earlier start answers
whether or not the program is there, so a probe that passed would say the runtime works while no
session can be created. The program is looked up once, at `adapter.start`. A `codex` section that cannot be used (a relative `cwd`, developer instructions that
are too long) gives `failed` with its problems as the detail: the probe applies and cannot be
run, and reporting it as `not_run` would leave a broken preset looking like one that is not
configured.

### Creating a session

One `session.create` is one attempt, and every attempt is idempotent. The adapter
keeps the attempt in memory: a repeat waits for the run in progress, a repeat after a
final answer gets that answer, and the same attempt with other parameters is `1005`.
`create.Decide` is the pure function that picks each next step from what is stored and
what was just observed; the same function serves the first run, a retry and the check
after a restart.

Before a thread exists, the answer is `failed` with `preset_unknown`, `preset_invalid`,
`dependency_missing` (`codex` or `sidera` not found), `native_unavailable` (the server
cannot be reached) or `runtime_auth` (no account); certainly nothing was created. The
detail names what failed and why (`codex was not found: …`, or the error of connecting to
or starting the server), and the adapter logs it, because the daemon maps
`dependency_missing` to `native_unavailable` and passes no detail on. The
steps are:

1. `thread/start` with the preset's `cwd`, `approvalPolicy`, `sandbox` and `model`,
   `ephemeral: false` and the thread's configuration. The thread is registered as the
   session at once, so its events are reported from the first one.
2. `checkpoint.put` (scope `attempt`) of `{"v":1,"thread_id":"…"}`, before anything else
   is done to the thread. Nothing else is ever put: not the credentials, and not that
   the prompt arrived, which the thread's record shows.
3. `thread/name/set` to `sidera <attempt>`. The server writes a thread to disk from its
   first name on, so this is the step that makes it persistent.
4. Wait for the channel's MCP server to report in `mcpServer/startupStatus/updated`.
   `failed` archives the thread and answers `failed` with `dependency_missing`. A
   server that stays silent for 20 seconds is taken as ready and the creation goes on:
   a server that did fail then shows as a failed tool call.
5. `turn/start` with `clientUserMessageId` equal to the first prompt's attempt and the
   prompt's text as it came.
6. Ready is the first prompt in the record: the `item/completed` of a `userMessage`
   whose `clientId` is that attempt, or the same found in `thread/turns/list`
   (consulted at every wake, so a missed event costs a poll, and by the same call after
   a restart). Only then is the answer `created` with the thread id as `native_ref`
   and the front ends of the preset.

`created` carries `first_turn_ended: true` when the turn of the first prompt is already
over at the moment of the answer (found in the record), so a daemon that restarted
while the creation was unsettled does not wait for a `turn.ended` that went to a
connection that is gone.

**Retry.** With the attempt in memory its state is used. After an adapter restart the
state is the checkpoint the daemon hands back: the thread is resumed with its
configuration, and a thread the server does not have (`no rollout found`) was never
written, because naming comes after the stored checkpoint, so the attempt starts over
with a new thread and stores that one. A prompt found in the record is never started
again. A turn that ended without the prompt in the record is started again, at most
twice (`MaxStarts`).

**Outcomes.** If the turn still runs and the prompt has not appeared two minutes after
the latest `turn/start`, the answer is `indeterminate` with `turn_running`; the daemon
repeats the attempt. If the connection ends before the prompt is found it is
`indeterminate` with `native_unreachable`, and the session is dropped until the repeat
loads the thread again. If the two starts ended without the prompt, or the server
refuses a step, it is `failed` with `native_error`. A failure is final: the reason is
stored in the checkpoint first (`{"v":1,"thread_id":"…","failed":"native_error"}`), the
thread is archived, and every later request of the attempt gets the same failure.
Cancellation of the request (the daemon gave up waiting) concludes nothing.

`control_unknown` is never produced: the server is private to the adapter, and two
servers on one socket cannot run.

### Prompts, turns, reconciliation and close

**`prompt.deliver`.** The text goes in unchanged as a `turn/start` whose
`clientUserMessageId` is the attempt; no line with the attempt is appended to the
text, since the server keeps the id on the prompt's record. A session that runs a turn
(the server's word, or the adapter's own books) is refused `busy`: the adapter is the
thread's only writer and neither queues behind nor steers a turn. A thread the server
unloaded is resumed with its configuration first, and that resume brings the session's
turns up to date before the busy check, so a turn that ended while the connection was
down does not keep the session busy. A turn on a thread loaded any other way would run
without the session's credentials. When `turn/start` answers that the thread was not
found, that is not taken for proof: the adapter resumes the thread, and only a resume
that says the thread does not exist makes the session lost (`session_lost`); a thread
that was only unloaded is then started on again. The answer is `accepted`, and the
adapter sends `prompt.persisted` when the `userMessage` of the attempt completes in
the record; if that came before the answer, the answer is `persisted`. The same attempt
and text again gets the same answer, brought up to date; with another text it is
`1005`. A refusal the server words clearly (not signed in, a turn is running) is
`refused` with `runtime_auth` or `busy`, and a resume that says the thread does not exist
is `refused` with `session_lost`; no account or
no connection before the request is `refused` with `runtime_auth` or
`native_unavailable`; any other failure is an internal error, which the daemon settles
with `prompt.reconcile`.

**`prompt.reconcile`** is `reconcile.Judge` over the thread's record, newest turn first,
read page by page until the attempt is found or the record is exhausted:

| Found | Verdict | Grounds |
| ----- | ------- | ------- |
| the attempt is in a turn | `found`, with the turn and its state | `attempt_in_record` |
| the server cannot be asked | `indeterminate` | `native_unreachable` |
| not found, the server says the thread is active, the adapter's own books hold a turn that has not ended (from the answer to its `turn/start`, before the server's events), or a turn of the record is in progress | `indeterminate` | `turn_running` |
| not found, the record was not read to its end, or not in the recognized form | `indeterminate` | `record_unreadable` |
| not found, idle, whole record read | `provably_absent` | `record_complete_without_attempt` |

A server that was killed while a prompt was being taken in leaves a record that ends
in an interrupted turn without the prompt: the last row.

**Turn events.** `turn/started` becomes `turn.started` with the attempt that caused
the turn: the answer to `turn/start` says which turn it made, and an event that comes
first waits up to two seconds for it, then goes without an attempt. The thread counts
as running a turn from that answer, not from the event: the server sends the events
only after the thread's MCP servers have started, seconds later, and a creation that
took the thread for idle in between would put the first prompt in a second time. A turn the adapter
did not start has no attempt. `turn/completed` becomes `turn.ended` (`completed`,
`interrupted`, `failed`). Every turn has one pair: a turn whose start was missed is
reported started just before it is reported ended. When the adapter restarts, the
busy state comes from `turn_active` in the result of `adapter.start`, the running turn
is taken from the server's list silently and its end is reported when it comes.

**`session.close`.** A held session that runs a turn is `close_unconfirmed` with
`turn_still_running` (interrupt is not declared). Otherwise the thread is unsubscribed
and archived, which unloads it (the `sidera mcp` it started ends when its input does)
and keeps its record, and the answer is `closed`. An endpoint the adapter does not hold
but that comes with a `native_ref`: a thread held under another open endpoint is left
alone and answered `closed`; otherwise it is archived, and a thread the server does not
have is as closed as an archived one. No server connection is `close_unconfirmed` with
`native_unreachable`; an endpoint not held and without a reference is `1001`.

### Requests of the server

Every request of the server is answered at once, from the preset, and reported with
`native_request.handled` on the endpoint of the session (the runtime endpoint when
the request names no thread of the adapter). `serverreq.Handle` is the table; the
schema guard holds its list of methods equal to the server's.

| Method | Answer | Disposition |
| ------ | ------ | ----------- |
| `item/commandExecution/requestApproval`, `item/fileChange/requestApproval` | `accept` when the preset's `approvals` is `approve`, else `decline` | `preset` / `rejected_by_preset` |
| `applyPatchApproval`, `execCommandApproval` (older) | `approved`, or `denied` with a fixed reason | the same |
| `item/permissions/requestApproval` | no permission beyond the sandbox | `rejected_by_preset` |
| `mcpServer/elicitation/request` | `decline`: nobody can make the content up | `rejected_by_preset` |
| `item/tool/requestUserInput`, `item/tool/call`, `account/chatgptAuthTokens/refresh`, `attestation/generate` | an error answer; the tool call fails and the model goes on | `rejected_by_preset` |
| any other method | `-32601` | `rejected_unknown` |

The default preset has an approval policy of `never`, so Codex refuses what the sandbox
does not allow itself and almost nothing reaches the adapter; the table is complete
anyway.

### Front ends

The **MCP** front end is the main one on every platform: the thread's own MCP server
`sidera` (above). Codex starts it outside the sandbox, so it reaches the daemon's agent
socket whatever the sandbox of the preset is. The **command-line** front end is offered
(`created` lists it, and the preset's sandbox is `danger-full-access`) only without a
sandbox: on macOS a sandbox of `read-only` or `workspace-write` refuses `connect` on a
Unix socket (measured), and `network_access` opens all networking, which is not
acceptable for the channel. A precise exception needs a permission profile with
`network.unix_sockets`, which is experimental; Linux's sandbox blocks new Unix sockets
and Windows' named pipes under Codex's sandbox are not measured. The capability
declaration lists both front ends; each session is created with the ones its preset
supports.

### What the agent is told

Each thread gets developer instructions: the adapter's fixed statement of what Sidera
is, then, after a blank line, the preset's `developer_instructions` when it has any
(`codexcfg.DeveloperInstructions`). The statement says, as facts, that the session is
connected to a channel on which it exchanges messages with other agents and people in
its owner's own cluster, that it is woken by a short notification that a message is
waiting, and which tools to use: the `sidera` MCP server's tools first (`recv`,
`reply`, `send`, `control`, `status`, `ack`), the `sidera` command only where the
sandbox lets it reach the channel. It also says that `recv` hands out a page that has
not been acknowledged again at once, with the same lease, and that the page is
acknowledged with `ack` (`sidera ack <lease>`) or by passing its lease to the next
`recv` (`ack_lease`, `sidera recv --ack <lease>`); only after that does `recv` with a
wait wait for new messages. It does not say how to treat a message (obey it,
treat it as a task, ignore it): that is the owner's policy and belongs in the preset's
own text. The Claude adapter appends a statement of the same facts to the system prompt.

`thread/start` and `thread/resume` both take `developerInstructions`, and the server
keeps none of a thread's parameters with the thread, so the adapter sends the text at
the start and again at every resume, like the thread's `config`. The schema guard
checks that both parameter sets still have the member.

### Configuration, manifest and package

The instance configuration has `codex_program` and `sidera_program`, each a path or a
name looked up in `PATH` (on Windows `codex.exe`, not a `.cmd` wrapper, which cannot
be detached or ended reliably). A preset has `cwd` (a directory, required), `sandbox`
(`read-only`, `workspace-write`, the default, or `danger-full-access`), `approval_policy`
(`never`, the default, `on-request`, `untrusted`), `approvals` (`deny`, the default, or
`approve`), and optionally `model`, `effort` (`low`, `medium`, `high`, `xhigh`) and
`developer_instructions` (UTF-8, at most 32 KiB; see "What the agent is told");
what is left out is the owner's own Codex configuration, whose default model may be an
expensive one, so a preset for unattended use names both. `codexcfg` holds the
descriptions the manifest declares and the checks that read them, from one list.

The manifest is generated from the constants the adapter answers `initialize` with
(`declare.Manifest`), one package per platform with only that platform in `platforms`,
since the list is what installation checks and the package holds one binary:

```
go build -o <binary> ./cmd/sidera-adapter-codex   # with GOOS and GOARCH set
<binary> package --goos <os> --goarch <arch> --binary <binary> --out <dir>
```

writes `<dir>/sidera-adapter.json` and `<dir>/bin/sidera-adapter-codex` (with `.exe`
for Windows) through `manifest.WritePackage`, which every adapter's `package` shares. The platforms are `darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64` and
`windows/amd64`. The version in the manifest, in `initialize` and in the client information sent to
the App Server is the release version (`internal/release`; a build without it is `0.0.0-dev`), and
`<binary> version` prints it with the commit and the Go version. Receive budgets are 8000 tokens for both front ends (Codex cuts a tool
result at 10,000 tokens and counts CJK text conservatively); a receive through MCP waits
at most 100 seconds, 20 less than the tool timeout the thread's configuration sets, and
through the command line at most 8, since Codex moves a command that runs longer than
about ten seconds to the background.

### Known limitations

- **No interrupt, and no acknowledgement on the agent's behalf**; `ack_markers` is
  absent for the same reason.
- **The command-line front end needs `danger-full-access`** (above).
- **`SIDERA_HOME` is readable from the sandbox**, and holds the other sessions' tokens
  on the same node; an exact deny is not done.
- **The token is in Codex's logs** and in anything a model prints.
- **A thread's configuration replaces the `-c` layer** of the server's flags and the
  owner's `shell_environment_policy.set` entries of the same name; whether it replaces
  that table as a whole is not measured.
- **Not measured**: resuming, with a configuration, a thread that is running a turn
  (the adapter resumes only after a connection ended or a thread was unloaded); that the
  server replays a request that was pending when the connection ended, which the
  adapter would answer as usual (the `late` disposition is never reported); the
  process identity of the server, which `adapter.start` does not report.
- **Linux and Windows are not yet run**: the Linux sandbox's refusal of Unix sockets
  (the MCP front end is expected to be unaffected), a server under `systemd` (its
  cgroup ends with the unit), `--listen unix://` on Windows (which would unify the
  transports), the header the capability token needs, breakaway from the daemon's job,
  ending a whole process tree, a named pipe under Codex's Windows sandbox, and
  environment variables that are not valid UTF-8.

## Why

- **A private server, detached.** A server that dies with the adapter would end every
  running turn at each restart of the adapter or the daemon. Detached, the turns go on
  and the restarted adapter finds the server by its address. The managed daemon is not
  used for the reasons above.
- **Credentials in the thread's configuration, not in the server.** One server for all
  sessions is what lets turns outlive the adapter and what keeps the number of
  processes small. The measurement showed the thread's configuration is the one place
  that gives each thread its own environment for both its commands and its MCP server,
  and that it must be sent again at every resume.
- **A fixed statement of the channel, and none of the owner's policy.** A wake-up
  notice only says a message waits; a session that was never told what Sidera is
  cannot know what to do with it. Whether a message is to be obeyed differs by owner
  and by preset, so the adapter states only facts and the preset carries the policy.
- **Nothing in the server's `-c` flags.** A thread that has its own configuration
  ignores them, so a setting put there would hold for some threads and not for others.
- **The pair of credentials is never stored.** A checkpoint is a file the daemon keeps;
  a token in it would outlive the session.
- **The prompt is found by its client id, not by text.** The server keeps the id on the
  record, so the prompt can be found after a crash without adding a line to its text
  (which the model would read), and the same id makes reconciliation exact.
- **The thread is stored before it is named, and named before it is used.** The server
  writes a thread from its first name on; a crash before that leaves nothing on disk, and
  the stored checkpoint then names a thread that is gone, which the retry detects and
  starts over from. After it the name makes no difference to a retry, since the
  checkpoint already holds the id.
- **Absence is concluded only from a whole record of an idle thread.** A running turn
  may be about to record the prompt, and a record cut short or a newer format may hold
  it; saying "absent" wrongly would send the prompt twice.
- **A busy session is refused, not queued or steered.** One writer per thread keeps the
  order of prompts and the books of turns the adapter's own; the daemon retries a
  refusal when the turn ends.
- **Only the server this process started is ended.** The alternative needs a check of
  the identity of a process from a pid and a start time on every platform, which is
  other work; a wrong kill is worse than a leftover server, which is found and reused
  anyway.
- **A hand-written client and subset of types.** The schema is large with many `oneOf`s,
  and a generated copy would carry all of it and its instability. The guard test pins
  what is used. The community Go SDKs found target an older Codex, start the server as
  a child over standard input where this adapter needs to reach one that outlives it,
  and are far larger than the dozen methods used. `github.com/coder/websocket` is
  the one dependency (ISC, no dependencies of its own, maintained): a WebSocket client
  is about three hundred lines with handshake, framing, masking, ping and close, and
  `gorilla/websocket` would also do but is not built around contexts.

## Where

| Concern | Code |
| ------- | ---- |
| Client: dial over Unix socket or TCP, frames, read loop, queue | `cmd/sidera-adapter-codex/internal/appserver/client.go` |
| The subset of types and methods; `IsThreadGone`; `ServerVersion` | `internal/appserver/types.go` |
| A fake App Server for tests | `internal/appserver/appservertest/` |
| Start, connect, detach, environment, stop | `internal/server/` (`server.go`, `env.go`, `detach_*.go`, `version.go`) |
| Preset and instance configuration, thread configuration, sandbox policy | `internal/codexcfg/` |
| Creation decisions and the checkpoint | `internal/create/` |
| Reconciliation | `internal/reconcile/` |
| Answers to the server's requests | `internal/serverreq/` |
| Identity, capabilities, manifest, package | `internal/declare/` |
| The adapter's methods, events, reconnect | `internal/adapter/` |
| The executable and the `package` subcommand | `cmd/sidera-adapter-codex/main.go` |

Tests: `TestCallsAndNotificationsOverBothTransports`, `TestServerRequestsAreAnsweredFromTheHandler`,
`TestPendingCallsFailWhenTheConnectionDrops`, `TestLargeMessagesAreRead` (the client against
the fake server); `TestEnviron` (the environment of the server); `TestParsePreset`,
`TestThreadConfig`, `TestFrontendsFollowTheSandbox` (configuration); `TestDecide`,
`TestCheckpointRoundTrip` (creation); `TestJudge` (reconciliation); `TestHandle`
(requests of the server); `TestManifestAndCapabilitiesAreValid`, `TestWritePackage`
(the package); `TestCreateSession`, `TestCreateAfterARestartFindsThePromptInTheRecord`,
`TestCreateStartsOverWhenTheStoredThreadIsGone`, `TestCreateRefusals`,
`TestStartAttachesSessions`, `TestDeliverPrompt`, `TestReconnect`, `TestServerRequests`,
`TestCloseSession` (the adapter against the fake server); `TestRunMCPProbe` (the MCP probe
against a stand-in program).

Against the installed Codex, in `internal/server/realcodex_test.go` and
`realcodex_schema_test.go`, behind `-tags realcodex` and `SIDERA_REALCODEX=1` (no model
turn; the temporary directory must be short enough for a socket path):

```
SIDERA_REALCODEX=1 go test -tags realcodex -count=1 ./cmd/sidera-adapter-codex/...
```

`TestSchemaGuard` generates the schema with `codex app-server generate-json-schema` and
fails when the server's requests are not exactly those `serverreq` answers (a new one
has to be decided on, not refused by default), when a method, member or notification the
adapter uses is missing, or when `clientUserMessageId` or `clientId` is gone.
`TestThreadEnvironmentAndServerCleanup` starts a real server, gives a thread its
credentials through its configuration, reads them back from a command the thread runs,
runs a command under each sandbox policy, checks the words that say a thread is gone, and
stops the server and checks that its process group is gone.
