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
| Windows | `--listen ws://127.0.0.1:<port> --ws-auth capability-token --ws-token-file <state_dir>/ws-token` | WebSocket over loopback with `Authorization: Bearer <token>`. The port is found by binding port 0 and releasing it; the port, the pid, the process's start key and container are in `app-server.json`, the token (`0600`, random, 32 bytes) in `ws-token`. |

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
process whose start key is the one recorded (a pid alone could be anyone's now; a
file that records no start at all is checked for a running process only), a server
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

**The state file identifies the process.** `app-server.json` records the pid, the SDK's
start key of the process (`process.Identity`, the same pair that names a Claude turn
process) and the container `process.Start` returned, besides the address, the working
directory and `started_ns`. It is written for every start, on every platform, by
replacing the file whole, and every platform reads it. A start that does not end with a
reachable server (the server ended first because it lost the socket to an earlier one that
is still running, or it did not answer within the start timeout, or the caller gave up)
ends the new process, if it still runs, and leaves the file as it was before the start, or
no file when there was none, because stop, retire and replace find a server only through
this file. Whether the recorded process is the server is asked of the process table by pid
and key (`process.Running`).

**A file an earlier adapter wrote has a start time and no key.** `started_ns` is the
wall-clock start, which the SDK says never to use as identity: on Linux it moves when
the clock is set after boot. A record without a key is therefore taken for the process
that holds the pid only when its `started_ns` equals the process's start time exactly,
and the key is then written into the file (when the adapter finds the server running
and when it ends it). On macOS and Windows both are the same kernel value, so an
earlier server is always recognised; on Linux it is unless the clock was set after boot.
Any other record, one whose start time differs or that has none, is not proven to name
the process: the adapter does not end the process (`ErrNotProven`) and the server is
restarted by hand.

**Two questions, answered in opposite directions when the process table does not
answer.** The process table (`process.Lookup`, `process.Running` of
`sdk/go/adapterkit/process`) can fail to answer, access denied included. Whether to
start a server takes that for "not running", and the adapter then starts one. This
differs on purpose from the Claude adapter, which treats a table that cannot be read
as an error: a wrong "not running" there leaves two writers on one session, silently
and beyond repair, while here it starts a second App Server, which shows (two
processes, a log, a waiting reconnect loop) and ends with the owner stopping one.
Whether to end a server takes it for "not to be touched": a wrong guess there ends
someone else's process, silently and beyond repair, so `Server.Terminate` returns the
error and ends nothing.

**Detached start.** The Codex program is resolved to an absolute path first (a name through `PATH`, a relative
path against the adapter's directory), because `process.Start` requires an absolute path and runs
it as written, and the server's working directory is not the adapter's. The server is started with
`process.Start`, not interruptible (it is
ended by signalling its process group, and Codex needs no console of its own), with
standard input on the null device and standard output and error appended to
`<state_dir>/app-server.log`, which is moved to `app-server.log.1` at each start. The
package decides how far the server escapes the adapter, and the adapter logs it: a
`Detachment` whose `OutlivesDaemon` is false is a warning, and any non-empty `Caveat` is
logged at info level.

- **Unix.** The child gets a session of its own (`setsid`), so the signals aimed at the
  adapter's process group do not reach it and its pid is the id of its process group,
  which `Server.Terminate` signals.
- **Linux, systemd user scope.** The child is also moved out of the adapter's control
  group, into a scope named `sidera-codex-<16 hex digits>.scope`, so that stopping the
  unit the daemon runs in does not take it along. The pid and the process group are the
  same as without a scope. The scope is used only when the user manager answers
  (`systemd-run --user --scope --quiet -- true` succeeds) and either `Linger=yes` or the
  adapter itself is under `user@<uid>.service/`; otherwise, and when `systemd-run` is
  missing, the child gets the session only. A scope belongs to the user manager, which ends
  at the last logout of a user without linger, while a daemon started from a login
  session (SSH, a terminal multiplexer) is not under the user manager, and a process in a
  session of its own survives that logout where a scope would not. Without linger a
  daemon under the user manager ends at that logout together with the server, so the
  scope adds no risk there; a node that must run with nobody logged in needs
  `loginctl enable-linger`.
- **Windows.** It is created with `CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS |
  CREATE_BREAKAWAY_FROM_JOB`; when the job the adapter runs in forbids breakaway
  (`ERROR_ACCESS_DENIED`) it is started again without that flag. The job is then read:
  a job that merely forbids breakaway leaves the server running when the daemon ends.
  The log carries the warning whenever `process.Start` cannot promise that the server
  outlives the daemon: the job kills its processes when it closes, its limits cannot be
  read, or an outer job of a nest refused the breakaway, whose limits are not known.

**Working directory.** The server runs in an empty directory of the adapter's own,
`<user cache>/sidera-adapter-codex/<instance>-<hash>` (`os.UserCacheDir`: `~/Library/Caches`
on macOS, `$XDG_CACHE_HOME` or `~/.cache` on Linux, `%LocalAppData%` on Windows), where
`<instance>` is the last element of `<state_dir>` and `<hash>` the first 8 hex digits of the
SHA-256 of its absolute path, so the instances of two Sidera homes never share one. A restart
of the same instance uses the same directory. The start fails, with a message that says so,
when there is no user cache directory. Nothing is ever put in it, and nothing reads it.
The adapter deletes it when the instance is disabled or removed (see "When the adapter ends").

Codex derives a sandbox's writable roots from a working directory, and the server's own is
the one it falls back to (measured on 0.166, and in its source):

- `command/exec` with a `sandboxPolicy` takes the server's working directory as the sandbox's
  workspace, whatever `cwd` the request names; the request's `cwd` is only where the command
  runs. Only a request with a `permissionProfile` binds the sandbox to its own `cwd`.
- `thread/start`, `thread/resume` and `command/exec` without a `cwd` use the server's.
- A thread's turns derive their roots from the thread's (or the turn's) `cwd`.
- On Windows, under the elevated sandbox, the setup refresh that runs before a sandboxed
  command grants the sandbox's accounts write access (an ACE) to each writable root.

So a server started in `<state_dir>` would give sandboxed commands write access to
`ws-token`, with which a command could reach the server and start a thread without a
sandbox. An empty directory gives a sandbox nothing it can use; `/` or the home directory
would give it everything the user can write. The socket, the token, the state file and the
log stay in `<state_dir>`: Codex reads them by the paths it is given and never makes them
roots. Every thread, turn and command the adapter asks for names the preset's `cwd`.

The state file records the working directory (`work_dir`). A server found running whose
record has none was started in `<state_dir>` by an earlier adapter; `adapter.probe` then
reports the `app-server` dependency as `wrong_version` with a detail that says to restart
it, since the adapter does not restart a server it did not start. Restarting it does not take
back an ACE the Windows sandbox already granted on `<state_dir>`.

**Environment.** The server is started with the adapter's environment (composed by
`environ.Compose` of `sdk/go/adapterkit`, with this list of what to leave out) less: a pair
that is not valid UTF-8 (the server refuses to start on one); `SIDERA_TOKEN` and
`SIDERA_PROBE_TOKEN`, which belong to one session and would be seen by every
thread; `CODEX_THREAD_ID`, `CODEX_SESSION_ID`, `CODEX_SANDBOX*`, `CODEX_CI` and
`CODEX_TOOL_CALL_ID` (the daemon may itself run inside a Codex session); `CLAUDECODE`
and `CLAUDE_CODE_*`. On Windows names have no case, so a variable is left out whatever the
case it is spelled in (`Codex_Thread_Id`). The owner's Codex configuration (`CODEX_HOME`, `config.toml`)
is left alone and loads as usual.

**Ending it.** `Server.Terminate` ends the server the state file names, whichever
adapter started it, and only when the file and the process table show that the process
is the one that was started (above): `process.Terminate` gives the process group
`SIGTERM`, then `SIGKILL` after three seconds, and returns once the server is gone. A
process table that does not answer, and a record that is not proven (`ErrNotProven`),
leave the server running and are errors, each logged with the pid. Nothing is done when
there is no record or the recorded process has ended. A server that did not become
reachable within the start timeout is ended the same way, but by the identity the start
returned, not by the state file, which may not have been written (the process that was
just started is killed directly in the one case that its key could not be read). The
adapter's shutdown ends the server by the intent it is given (below). On Windows only the
process itself is ended, not what it started.

**When the adapter ends.** The server is meant to outlive the adapter, so what happens to it
depends on why the adapter ends, which the daemon says in the intent of `shutdown`:

| Intent | The server | The working directory |
| ------ | ---------- | --------------------- |
| `restart` (the daemon restarts, `sidera update`, a changed configuration or package) | kept: a turn that runs goes on and the next adapter finds the server by the state file | kept |
| `stop` (the daemon ends) | ended only when it is shown to be idle, kept otherwise | kept |
| `retire` (the instance is disabled or removed) | ended whatever it is doing; a turn that runs ends with it | deleted |

An adapter that is killed, crashes or loses its connection to the daemon gets no
`shutdown`, so the server stays, as for a restart.

- **`stop` is the replacement's idleness test.** The adapter takes the replacement gate
  exclusively without waiting and applies the same strict test of idleness (above), and
  also needs the adapter to have been started and its connection to be open: otherwise the
  sessions are not known. A gate that is taken (an operation is using the server) or any
  session that is not known to be idle keeps the server; the reason is logged. When the
  server is ended the gate stays taken, so that no turn starts in it. A server that is kept
  serves the next start of the instance, or ends at the instance's `retire`.
- **`retire` does not need `adapter.start`.** The daemon retires an instance whose adapter
  is not running by starting the adapter once, only to `initialize` and `shutdown`. The
  adapter then knows only its state directory, and finds the server by the state file
  there, as `Terminate` does.
- **The order.** The adapter answers `shutdown` once the server has ended, and only then
  deletes the working directory, so a server that cannot be ended keeps the directory it
  runs in: the answer is an error naming the reason, which the daemon logs, and the adapter
  ends all the same. Because the answer comes after the server ended, the state directory
  that the daemon deletes next (when the instance is removed) holds nothing that is still
  in use. After `shutdown` began, no connection to the server is opened, so the reconnect
  loop and a late request cannot start a server behind the one that was ended.
- **The working directory** is deleted with `Server.RemoveWorkDir`, which takes only this
  instance's directory (its name carries the hash of the state directory), not the cache
  directory of the adapter that holds the others. The state directory's files (the log,
  the state file, the token) are left for the daemon to delete with the instance.

**Versions.** `adapter.probe` reports the version of the `codex` program
(`codex --version`). The server's own version is in the user agent it answers
`initialize` with. When the two differ (Codex was upgraded under a running server)
the probe reports the `app-server` dependency as `wrong_version`, as it does for a server
that runs in the state directory (see "Working directory"); what the detail says is
under "After Codex is upgraded". Neither the manifest nor the adapter names a
version it was tested with, and nothing judges one: Codex releases several times a
day, so a list could not stay true. Whether `prompt.reconcile` may conclude that a
prompt is absent rests on what the adapter can verify of the record (below), not on
the version.

**After Codex is upgraded.** The server is replaced when it runs another version than
the Codex now installed and no session is running. A session's turn is the session's
work and is never cut short for an upgrade, so the replacement waits for it.

- **When it is looked at.** At the end of `adapter.start`, every ten minutes, and each
  time a turn ends while a replacement waits. Sessions that `adapter.start` did not wait
  for are not known to be idle at its end and count as busy, so the look is made again
  when their loading ends. Each look resolves `codex` again (`PATH` or the configured
  path) and runs `codex --version`, because an upgrade may move the program; a program
  that cannot be found or run, or a server that is not connected, ends the look.
- **When the server counts as idle.** Strictly: every session the server holds for the
  adapter must have its thread loaded in the current connection and idle, with no turn
  reported running, no turn start being answered and no creation under way. A thread
  the adapter has not seen in this connection, or in a state it does not know, counts as
  busy. Sessions of a thread a person runs (attached sessions) are not counted: their
  turns run in the person's own Codex, not in this server. A session whose thread is
  gone is not counted either.
- **The replacement gate.** Every operation that needs the server to stay as long as it
  lasts holds the gate shared: a turn start (a delivered prompt), a session creation,
  a prompt queued for an attached session, and the command the command-line probe runs
  through the server. The replacement takes it exclusively without waiting, checks that
  every session is idle, and ends the server through `Server.Terminate`, then closes the
  connection. An operation that finds the gate taken answers as it does when there is
  no connection: a prompt is refused as `native_unavailable` (which the daemon
  retries), a creation fails as `native_unavailable` or, when it already has a thread,
  asks to be asked about again, a tool probe fails. The gate is released once the server
  is gone; the reconnect loop then starts the server afresh from the Codex now
  installed and resumes every session in it, the path a server that died takes. The
  threads are in Codex's own store and survive the server.
- **What is not retried.** A server that cannot be shown to be one the adapter started
  (`ErrNotProven`, above) is logged and left; the probe then keeps saying to restart it
  by hand. The same pair of versions is not tried again after a replacement or after
  that refusal, so a Codex whose `--version` is never the version its server reports
  does not end the server at every look. Any other failure to end the server is logged
  and tried again at the next look.
- **The probe.** While a replacement is waiting, the `app-server` dependency is
  `wrong_version` with the detail `it will be replaced when no session is running`.

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
  (`optOutNotificationMethods`): they are most of the traffic and nothing reads them. It does
  not opt out of `item/started`, which tells the commands a turn runs (see `session.interrupt`).
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
mcp_servers.sidera = {enabled: true, command: <sidera program>, args: ["mcp"],
                      env: {SIDERA_TOKEN, SIDERA_HOME},
                      default_tools_approval_mode: "approve", tool_timeout_sec: 120}
default_permissions, permissions.<profile>   # presets with a sandbox only; see below
```

The values are the `tool_access.env` of `session.create` or of the session's entry in
`adapter.start`. They are held in memory only: not in a checkpoint, not in a file.

**The owner may have a `sidera` server of their own.** The supported way for a person's own
Codex sessions to reach Sidera is `sidera adapter setup <codex instance>`, which installs a
plugin that carries the server (see [Client setup](#client-setup)); a `sidera` entry added by hand
hides the plugin's server of the same name. A person who wants their own Codex
sessions to reach Sidera by hand adds `[mcp_servers.sidera]` to `config.toml`
(`codex mcp add sidera -- <sidera> mcp`), and a thread's `config` is merged into the owner's
file key by key, a table into a table of the same name, not replaced by it. The adapter keeps
the name `sidera`: under another name the thread would also start the owner's server, which
has no token and would attach the adapter's own session as a person's. Under the same name the
adapter's `env` carries the token, and the server does not enter that mode. Every key the
session depends on is therefore stated: `enabled = true` keeps an owner's `enabled = false`
from switching the channel off, and `command`, `args`, `env`, the approval mode and the tool
timeout replace the owner's. What the adapter does not state stays the owner's, such as the
tool allow- and deny-lists of that server. A `url` in the owner's table cannot be removed by a
merge and makes the thread fail to start; the error is Codex's, and the owner takes the key
out.

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

### The data directory is denied to the sandbox

A sandboxed command could otherwise read `SIDERA_HOME`, which holds every session's
token on the node. A preset whose sandbox is `read-only` or `workspace-write` is
therefore a Codex permission profile of the thread, not a legacy sandbox mode:

```
default_permissions = "sidera-adapter-codex"
permissions.sidera-adapter-codex.extends = ":workspace"      # ":read-only" for read-only
permissions.sidera-adapter-codex.filesystem = {<real SIDERA_HOME>: "deny"}
```

The profile is part of the thread's `config`, sent at `thread/start` and again at
every `thread/resume`. The operating system enforces it: Seatbelt on macOS,
bubblewrap on Linux (the directory is replaced by an empty one that nothing can
open), and a deny entry in the access list of the sandbox's accounts on Windows. The
profile is named so that it does not meet one of the owner's own, whose settings Codex
would merge into it. A deeper entry wins over a shallower one, and at equal depth deny
wins.

- **The path is the real one, and exists.** The sandbox compares the paths a command
  opens with the one it is told to deny, so symbolic links in `SIDERA_HOME` (the data
  directory of the session's `tool_access.env`) are resolved first (`realpath.Resolve` of `sdk/go/adapterkit`: on Windows it also follows
  junctions and gives the long form of an 8.3 name, which is the spelling the sandbox itself
  ends up with), and Codex creates a
  denied path that does not exist. A `SIDERA_HOME` that is unset, relative or missing is
  refused before anything is sent (the same failure as a profile that did not take, below).
- **Nothing else may name a sandbox.** When the same `config`, or the `sandbox` member of
  `thread/start` and `thread/resume`, also names a legacy sandbox mode, Codex accepts the
  request and silently ignores the profile: the thread then reports no active profile and
  the data directory is readable. So a preset with a sandbox sends neither, and no per-turn
  `sandboxPolicy` either: a per-turn policy of `dangerFullAccess` drops the profile, while
  `workspaceWrite` and `readOnly` keep it, and sending none leaves a single place that says
  what the sandbox is. A preset without a sandbox sends `sandbox` and `sandboxPolicy` as
  before and defines no profile. An owner's `sandbox_mode` in `config.toml` does not
  override the profile.
- **The thread must report the profile.** The answers of `thread/start` and `thread/resume`
  carry `activePermissionProfile` (`id` and `extends`) on 0.164 and 0.166 without opting
  into the experimental interface, though the published schema lists it only with
  `--experimental`; the schema guard reads it there. The adapter requires the answer to
  name the profile above with the `extends` of the preset. When it does not, the
  thread is unsubscribed (the server unloads it) and used for nothing: a creation fails with
  `native_error` and a detail that says so, a resume leaves the session `unreachable` (the
  reconnect loop tries again, and a `prompt.deliver` refuses with `native_unavailable`), and
  there is no fallback to a thread without the protection.
- **`command/exec` cannot carry the profile.** Its `permissionProfile` is only the name of a
  profile in the server's own configuration, needs the experimental interface and cannot be
  combined with `sandboxPolicy`; there is no per-request configuration. The only
  `command/exec` the adapter sends is the probe of the command-line front end, which only a
  preset without a sandbox offers.

What stays readable: everything of a preset with `danger-full-access` (no sandbox by
design); the thread's own token and `SIDERA_HOME` as a variable, which are in its
environment; the MCP servers Codex starts outside the sandbox, the channel's `sidera mcp`
and any of the owner's own; and a command that runs outside the sandbox after approval
(`sandbox_permissions: require_escalated`, which a preset with `approvals: approve`
grants). The denial protects the other sessions' tokens and the rest of the data
directory, not the session's own credentials.

The denial was measured on macOS (Codex 0.164 and 0.166) and on Linux (0.166, bubblewrap), where
a `workspace-write` or `read-only` command gets `Operation not permitted` or `Permission denied`.

**Windows.** Codex adds the deny entry when a sandboxed session of the adapter runs a
command; a `codex sandbox` run outside the adapter adds none. The entry is tracked per
sandbox account in `CODEX_HOME`, so another,
unrelated Codex session that shares the same `CODEX_HOME` can revoke it. There the
protected data directory (private to its owner) is the primary guard, and the profile
a second one. This was not run on Windows.

### Methods

| Protocol | What the adapter does |
| -------- | --------------------- |
| `initialize` | Declares a runtime with `interrupt: true`, `ack_on_behalf: false`, `server_requests: true`, `attach: true` and front ends `mcp` and `cli`. It does not touch the server. |
| `adapter.start` | Reads the configuration, finds the `codex` and `sidera` programs, then connects or starts the server (`initialize` and `account/read`) and resumes the sessions listed, four at a time, in the background: the answer waits for them only until a second before the deadline of the request (see "Starting within the time the request allows"). A session resumed in time is `attached` (with `turn_active` when the thread runs a turn), `lost` when the server has no record of the thread (`no rollout found`), or `unreachable` when the resume failed or the preset is unusable; one not resumed in time is `unreachable` too. Attached entries are taken back as described in "Attached sessions". It ends with a full availability snapshot. A server that cannot be reached, or not yet, is not a failure of `adapter.start`: every session is `unreachable`, the snapshot says `native_unavailable` and the adapter goes on reconnecting. |
| `adapter.probe` | The runtime version; `auth` from `account/read` (`valid` with an account, `invalid` without, `unknown` when the server is unreachable); the dependencies `codex`, `sidera` and `app-server`; each preset's problems and whether its directory exists; the tool probes. |
| `session.create`, `prompt.deliver`, `prompt.reconcile`, `session.close` | Below. |
| `session.attach`, `session.detached` | "Attached sessions". |
| `session.interrupt` | Below. For an attached endpoint it is `1002`: the turn is the person's. |
| `checkpoint.put` | `scope: attempt` while creating, and only there. |

**Starting within the time the request allows.** The daemon ends the connection of an
adapter that has not answered `adapter.start` in twenty seconds, and the time to take the
sessions back grows with their number and with the length of their threads (a resume loads the
thread's record from disk), while a server that has to start leaves about five of the twenty.
`adapter.start` therefore answers at the deadline of its request less a second, with what was
done by then, and leaves the rest to run: the server is connected or started in the background
(each attempt bounded by `dialTimeout`), and the sessions are resumed four at a time. A session
not resumed at the answer is `unreachable` with `turn_active` false, the state it has when the
server cannot be reached, so the engine needs nothing new. When it is resumed afterwards, a turn
found running in it is reported `turn.started`, as the reconnect loop reports one, and the
availability snapshot is sent again once the connection is up. A thread is resumed once: the
loading of a session is serialized, and a path that finds the thread loaded by another (the
background loading, the reconnect loop, a delivery or an interrupt that found it unloaded) does
not resume it again. A session found `lost` after the answer is not reported to the daemon, as
none found so by the reconnect loop is; it is refused with `session_lost` when a prompt is
delivered. Another `adapter.start` ends the loading of the earlier one.

**Availability.** A full snapshot at `adapter.start`, and again whenever it changes:
`dependency_missing` when `codex` or `sidera` is not found, `native_unavailable` while
there is no connection to the server, `runtime_auth` when the server has no account, and
`native_unavailable` limited to one endpoint while the thread of an attached session takes
no prompt (below). The snapshot is always the whole of these together.

**Tool probes.** The MCP front end is started by Codex outside the sandbox, so its
probe runs `sidera mcp` the same way, with `SIDERA_PROBE_TOKEN` and the request's
environment, sends it an MCP `initialize` (which makes it probe the daemon itself) and
waits up to ten seconds for the line on its standard error that says the probe
succeeded or failed; the result is `ran` with method `unsandboxed`, or `failed` when the
server did not answer or says it could not reach the daemon. The command-line
front end is offered only without a sandbox, so its probe is `command/exec` of
`sidera probe` under the `dangerFullAccess` policy, through the server, which costs no model
turn: `ran` with method `sandboxed`, or `failed` with the exit code and the first line
of its standard error, and `failed` when the server cannot be reached. A preset that has no
`codex` section (the `default` preset is one; it reaches the adapter as no values at all) gives
`not_run`: the probe did not apply, and the daemon counts it for neither the pass nor the
failure. So does a front end the preset does not offer (the command line of a preset whose
sandbox is not `danger-full-access`): the sandbox would refuse the command, and the daemon
would take that for a failure of an instance whose sessions work.
When `codex` is not found or `codex --version` does not run, every tool probe of a
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
`model_choice_not_allowed` (the requester's model or effort is not allowed, see "A requester's
choice of model and effort"), `dependency_missing` (`codex` or `sidera` not found), `native_unavailable` (the server
cannot be reached) or `runtime_auth` (no account); certainly nothing was created. The
detail names what failed and why (`codex was not found: …`, or the error of connecting to
or starting the server), and the adapter logs it, because the daemon maps
`dependency_missing` to `native_unavailable` and passes no detail on. The
steps are:

1. `thread/start` with the preset's `cwd`, `approvalPolicy` and the session's `model` (the
   preset's, or the requester's choice), the `sandbox` of a
   preset without one, `ephemeral: false` and the thread's configuration, whose permission
   profile has to be reported active (see above). The thread is registered as the session at
   once, so its events are reported from the first one.
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
   and the front ends of the preset. When the requester chose a model or an effort, the
   session checkpoint is stored first (see below), and a store that fails is an error of the
   request, which the daemon repeats.

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

### A requester's choice of model and effort

The adapter declares the capability `model_choice` with the efforts Codex runs (`low`,
`medium`, `high`, `xhigh`, taken from the one list that the preset's `effort` also reads), so
the daemon sends the optional `model` and `effort` of a `session.create` to it.
A preset states what a requester may choose in `allowed_models`, a `map` from a model to
the list of efforts allowed with it. `codexcfg.ParsePreset` reads it with the Go SDK's
`modelchoice.ParseAllowed`, and each of its refusals (an empty map, a model with no effort, a
model name or an effort that is not valid, an effort Codex does not run, an effort listed
twice) is a problem of the preset, which makes the preset unusable like any other.
A preset's own `model` must also be a valid model name, since the probe reports it.

The creation resolves the request with `modelchoice.Resolve` against the preset's own
`model` and `effort`, and nothing is ever downgraded or substituted: what the preset does
not allow is `failed` with `model_choice_not_allowed`, before any thread exists (a thread an
earlier run of the attempt started is archived), and the detail goes to the log only. A
request with neither member is not a choice: the session follows its preset, stores nothing
and behaves as it did before the capability existed. A member that neither the preset
states nor the requester gave stays empty and is not sent; Codex decides it by the owner's
own configuration.

**The session checkpoint.** A session whose requester chose stores `{"v":1,"choice":{"model":…,
"effort":…}}` (scope `session`, keyed by the session endpoint), the pair `Resolve` returned
with each member that is empty left out, before the answer is `created`. `adapter.start` reads
it from the session's `checkpoint` into the session. A checkpoint that cannot be read (a
version this adapter does not know, an empty choice, a model or an effort that is not valid)
leaves the session reported `unreachable` and logged, rather than run on values nobody chose.
A retry of a creation after a restart resolves the request again, so it stores the checkpoint
again. The pair is stored, not the requester's words, because the allowlist is checked once,
when the session is created: the session keeps the combination that was checked, even if the
owner later edits or narrows the preset.

One function (`session.modelAndEffort`) gives the model and the effort of a session, the
choice if it has one and the preset's otherwise, and `thread/start`, `thread/resume` and every
`turn/start` take them from it. The thread's own state is not used for this, for two
reasons found by measuring Codex 0.166: a `thread/resume` that names a model drops the effort
the thread persisted, and the adapter names the model on every resume; and what a thread
reports cannot tell the requester's choice from what the runtime defaulted to, nor keep it
when the owner changes the preset. The effort is therefore sent by each `turn/start`, so the
effort a resume dropped does not matter.

**The owner's part.** Codex's reaction to an effort the model does not support is not
established, so the owner lists in `allowed_models` only the efforts each model supports.
The adapter checks only that an effort is one Codex runs.

The probe reports each usable preset's `model`, `effort` and `allowed_models` in its
`PresetStatus`, sorted by model with the efforts in the order of the scale; a preset with a
problem reports its problems alone.

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
is taken from the server's list silently and its end is reported when it comes. A session
resumed only after `adapter.start` answered is unreachable in that answer, and its running
turn is reported started when it is found.

**`session.close`.** A held session that runs a turn is `close_unconfirmed` with
`turn_still_running` (the sender interrupts first). Otherwise the thread is unsubscribed
and archived, which unloads it (the `sidera mcp` it started ends when its input does)
and keeps its record, and the answer is `closed`. An endpoint the adapter does not hold
but that comes with a `native_ref`: a thread held under another open endpoint is left
alone and answered `closed`; otherwise it is archived, and a thread the server does not
have is as closed as an archived one. No server connection is `close_unconfirmed` with
`native_unreachable`; an endpoint not held and without a reference is `1001`.

**`session.interrupt`.** `turn/interrupt` stops a turn, but the server leaves running the
processes of the commands the turn had started: a `sleep` the turn waited for was alive twenty
seconds after the turn reported `interrupted`, and went on until it ended or the server did.
The server lists those processes as the thread's background terminals
(`thread/backgroundTerminals/list`, `terminate` and `clean`, experimental), whichever turn
started them. So an interrupt that reports `stopped` has to end them, or it would say a turn's
work stopped while its commands run on. The adapter keeps, for each turn it sees start, the
items of its commands that have a process (`item/started`, a `commandExecution` item with a
`processId`), and:

1. Asks the server to stop the turn with `turn/interrupt` (thread and turn it keeps as running).
   The server refuses a thread that runs no turn (`no active turn to interrupt`, `-32600`) at
   once; that is `not_running`. A thread the server unloaded is resumed first. Any other failure
   is `failed` (`native_unreachable` when the connection is gone, `native_error` otherwise).
2. Waits for the end of the turn (`turn/completed`, about ten milliseconds later).
3. When the turn, which an interrupt asked to stop, ended without completing (`interrupted`, or `failed`, which includes a turn the
   server no longer lists once the connection is back), lists the thread's background terminals
   and terminates those whose item is one of the turn's commands, **before `turn.ended` is
   reported**. Commands that earlier turns left running on purpose are not touched, and a turn
   that ran no command with a process costs no further request. For a turn whose end the adapter
   sees as it happens, the session is still busy while this goes on: it is made idle only after
   the commands are ended, so a prompt delivered at the end of the turn is refused as busy, and
   the turn it would start never has its own commands ended along with the old one's. A turn
   found ended when the connection came back is made idle before its commands are ended, so a
   prompt delivered in that gap is accepted and its commands may be ended along with the old
   turn's.
4. Answers `stopped` once `turn.ended` has been reported. The end of the turn and the answer
   therefore both mean the commands ended too.

`requested` is the answer when the turn is still running after the adapter's wait (ten
seconds); the daemon then waits for `turn.ended` itself, and the commands are ended just before it
is reported, so its `stopped` holds as well. Asking again while the turn runs does not ask the
server again. `not_running` is a session with no turn running, answered without asking the
server. If a command cannot be ended the answer is `failed` with `native_error`, or with
`native_unreachable` when the connection to the server is gone (the server cannot be asked to end them), and `turn.ended` is reported all the same: the turn did end.

A turn the adapter did not see start (it was found running after a restart, or the connection
that was telling its events ended meanwhile) has commands the adapter was not told of, and none
can be told from the thread's others; for it every background terminal of the thread is ended
(`thread/backgroundTerminals/clean`).

Each request of an interrupt is bounded to ten seconds. On Codex 0.166.0 (Linux) a second
`turn/interrupt` sent to a thread whose first one had left a command running was once not
answered at all, where one sent to a thread without such a command is refused at once; so a turn
is asked to stop once, and interrupts of one session are served one at a time.

### Attached sessions

An attached session is a thread that a person's own Codex runs (the terminal interface, or the
shared daemon behind it) and that attached itself: its `sidera mcp` server called
`sessions.attach` with the thread id as `native_ref`. Codex has no wake keys, so the attachment
carries none. The adapter's server shares `CODEX_HOME` with the person's Codex, which is the
whole mechanism: both see the same thread store and the same queue file. What the server can and
cannot do with a thread another process has loaded:

- It **cannot resume** it (`already has an active writer`) and gets **no event** of it.
- It **can read** it: `thread/read` for the thread's metadata and `thread/turns/list` for its
  turns, from the stored record.
- It **can queue a prompt** for it: `thread/queue/add` puts the prompt in the queue file of the
  Codex home, and the process that has the thread loaded takes it. The queue is part of the
  experimental API, which is why the adapter's connection asks for it at `initialize`
  (`experimentalApi`); the request is refused without it.

The connection is one for created and attached sessions, so `experimentalApi: true` reaches the
created ones too. For them it changes two things: experimental requests and notifications become
available, which the adapter ignores unless it handles them, and the server stops stripping
`additionalPermissions` from approval requests, a member the adapter's handling of server
requests never reads. Nothing else changes.

**`session.attach`.** `thread/read` on the thread. `attached` for a normal stored thread;
`not_found` when the server does not know it; `failed` with `wake_unavailable` for an ephemeral
thread or a sub-agent's, which take no queued prompt, with `native_unreachable` when the server
cannot be reached and `native_error` for any other answer of the server. An archived thread is
not told apart here (the record carries no flag); it is refused when a prompt is queued. The
call is idempotent: a repeat (the heartbeat of the session's `sidera mcp` server) reads the
thread again and starts no second watch. The wake is ignored. A request that comes before
`adapter.start` is answered with an error, since `adapter.start` replaces the sessions and would
lose one attached before it; the daemon asks again.

**`adapter.start`** takes an attached entry back the same way: `lost` when the thread is gone
or takes no prompt, `unreachable` when the server cannot be asked, `attached` otherwise with
`turn_active` when a turn runs. The watch runs in either case and keeps trying while the
server is unreachable.

**Reading the turns.** For each attached session, while it is attached, the adapter calls
`thread/turns/list` (the five newest turns, summary view). Every call makes the server rebuild
the thread's history, so the pace follows need: once a second (`AttachPollEvery`) while one of
the adapter's own prompts is awaited (queued, or taken and its turn not yet seen) or a turn is
open, and every ten seconds (`AttachIdlePollEvery`) otherwise; queueing a prompt ends the slow
wait at once. The slow pace cannot cause a prompt in the middle of a person's turn that the
adapter has not seen yet: Codex's queue never puts an item into a running turn, the owner takes
it only when the turn is over. While the server is unavailable or the call fails, the wait
doubles up to the reconnect limit and falls back to the pace of the moment after a success. The
first reading counts the turns the thread has as history and reports none, except that a
running turn is reported started (`adapter.start` tells the daemon through `turn_active`
instead). Later, a turn that was not there before is reported `turn.started` and, when it is
over, `turn.ended`, within about a second of the change while the quick pace holds. A turn a
prompt started names its attempt; a turn the person started names none, which is how the
engine knows the session is busy with the person's work and does not prompt it. The summary
view keeps only the first user message of a turn, which is the one a queued prompt starts the
turn with; `prompt.reconcile` reads the full view.

A turn that another process runs reads from outside as `status: "interrupted"` with
`completedAt: null`; it has ended when `completedAt` is set, with the status the record gives
(`completed`, `interrupted` after the person pressed Esc, `failed`). Two rules keep a turn from
looking busy for ever: a turn that is not the newest cannot still be running, and one that began
more than two hours ago and shows no end is taken for abandoned (its Codex died) and reported
ended as `failed`. A turn that shows no start is counted from when the adapter first read it,
which also covers a thread forked in the middle of a turn that holds an old unfinished turn. The
second rule can end the books on a turn that does run for longer; the prompts the engine then
sends are queued and wait for the turn, so nothing is lost.

**`prompt.deliver`.** `thread/queue/add` with the text unchanged and the attempt as
`clientUserMessageId`; the answer is `accepted` and nothing more is reported until the owner
takes the prompt. The owner's process polls the queue file every ten seconds, so an idle
session starts the turn within about ten seconds (0.4 to 9 seconds measured), and a busy one
starts it about 0.3 seconds after the running turn ends; the prompt is never put into a turn that
runs. The server keeps the client id on the user message of the turn that takes the prompt, and
the adapter finds the turn by it: `prompt.persisted` and `turn.started` with the attempt come
from the reading that first shows that message. A turn is written before its prompt, so a turn
seen with no prompt in it is looked at once more when a prompt of ours is awaited before it is
reported. The match is on the client id only, never on the text: a prompt whose id did not
survive would show as a turn the person started, and a repeat of the attempt would be judged by
`prompt.reconcile` from the record and the queue.

Sidera keeps at most one wake-up waiting in the person's queue. The adapter keeps the id
`thread/queue/add` gave each prompt it queued, and before it queues a new prompt it reads the
queue and deletes (`thread/queue/delete`) its own items that are still there; a prompt the owner
took in the meantime is no longer queued and is left to its turn, and an item the adapter did not
add is never touched. A prompt that was taken back is judged by `prompt.reconcile` like any
that is not in the record, and a repeat of its attempt is queued as new. The ids are in memory
only: after the adapter restarts, an item it queued earlier is no longer known to it and stays
for the owner.

A prompt that stays queued stays queued, and nothing is reported for it; it is delivered when the
person returns. The queue is shared, so the prompt waits in the following cases, which are
the limits of this delivery and not failures:

- It arrives as an unattributed user message after the current turn, not inside it.
- After the person presses Esc in the terminal interface, the queue is held until their next
  turn completes.
- An active `/goal` can starve the queue.
- When no process has the thread loaded (the terminal interface is closed and the shared daemon
  unloaded the thread after about a minute) there is no delivery; the item stays in the queue
  file until some process loads the thread.

A thread that cannot take a prompt (archived, ephemeral, a sub-agent's, unknown) is refused by the
server as an invalid request before anything is queued, or with "no rollout found" for an unknown
one; the adapter answers `refused` with `native_unavailable` and the endpoint stays open. It
also publishes the availability condition `native_unavailable` limited to that endpoint
(`endpoints`), with the server's words as the detail and the time of the first refusal as
`since_ms`, merged into the one snapshot with the adapter's other conditions: a single archived
thread must not block the sessions the adapter created or the other attached ones. The
condition ends when the server queues a prompt for the thread again, or when the endpoint
detaches. Only a refusal that names the thread begins it; an answer that says nothing of the
thread leaves the outcome unknown, which the daemon settles with `prompt.reconcile`.
A thread the server says is gone while it is being read (`thread not loaded`) ends the watch:
a turn that was open ends with `turn.ended` as `failed`, and the endpoint gets the same
`native_unavailable` condition with the server's words. The next `session.attach` that finds
the thread again ends the condition and starts the watch again.
No connection is `refused` with `native_unavailable`. `busy` is never answered: a running turn
is no reason not to queue.

**`prompt.reconcile`** is the same judgement over the thread's record, with one more fact:
`thread/queue/list` (one page of a hundred; a longer queue counts as holding the prompt). A
prompt still in the queue is `indeterminate` with `turn_running`, since the turn that will record
it has not begun; one the queue list could not be read for makes the record count as not read.
A turn another process runs, `interrupted` with no completion time and not abandoned by the rule
above, counts as running for an attached session (for a created one that shape means the server
died): the turn the owner took the prompt into is written before its user message, and the
verdict must not be `provably_absent` in that gap, nor report a turn that runs as interrupted.

**`session.detached`** takes back the adapter's own prompts that are still queued (a wake-up
must not outlive its endpoint), stops the watch and forgets the session; the person's thread and
the rest of its queue are not touched. **`session.close`** and **`session.interrupt`** answer `1002` and touch nothing, and
`recv.observed` is never sent for an attached session (the adapter does not declare
acknowledgement on the agent's behalf).

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
(`codexcfg.DeveloperInstructions`). The statement is `guidance.Full` of the SDK, the
statement every adapter gives, then one sentence of Codex's own
(`codexcfg.ChannelStatement`). It says, as facts, that Sidera is the message channel to
the other agents and people of the owner's own cluster, what to use it for, that a
wake-up notice only says that a message is waiting and `recv` is the first call, and
what each tool of the `sidera` MCP server does (`recv`, `reply`, `send`, `control`,
`status`, `ack`). It also says that `recv` hands out a page that has not been
acknowledged again at once, with the same lease, and that the page is acknowledged
with `ack` (`sidera ack <lease>`) or by passing its lease to the next `recv`
(`ack_lease`, `sidera recv --ack <lease>`); only after that does `recv` with a wait
wait for new messages. Codex's own sentence is that the tools come first and the
`sidera` command works only where the sandbox lets it reach the channel. The statement
does not say how to treat a message (obey it, treat it as a task, ignore it): that is
the owner's policy and belongs in the preset's own text. The Claude adapter appends the
same statement to the system prompt, and `sidera mcp` gives the short form of it as
the instructions of the MCP server (see [mcp](../mcp/README.md)).

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
`approve`), and optionally `model`, `effort` (`low`, `medium`, `high`, `xhigh`),
`allowed_models` (a `map` from a model to a list of efforts; see "A requester's choice of model
and effort") and `developer_instructions` (UTF-8, at most 32 KiB; see "What the agent is told");
what is left out is the owner's own Codex configuration, whose default model may be an
expensive one, so a preset for unattended use names both. `codexcfg` holds the
descriptions the manifest declares and the checks that read them, from one list.

The program has four commands: `serve`, which the daemon starts (the manifest's
`entry.args` is `["serve"]`), `setup`, which `sidera adapter setup` runs (the manifest's
`setup.args` is `["setup"]`; see "Client setup"), `package` and `version`. `--help`, `-h` and `help` print the help
to standard output and exit 0; no argument prints the help to standard error, an unknown command
names itself and prints the help there, and an argument after `serve` is refused with "serve
takes no arguments" (all three exit 2), so a program run by a person never waits on its input as
if the daemon had started it. The commands are the kit's `adapterkit.Program`, the same in all
four adapter programs. The daemon starts an installed adapter with
the entry of the manifest in its own package, so the program and its arguments are always from
the same release.

The manifest is generated from the constants the adapter answers `initialize` with
(`declare.Manifest`), one package per platform with only that platform in `platforms`,
since the list is what installation checks and the package holds one binary:

```
go build -o <binary> ./cmd/sidera-adapter-codex   # with GOOS and GOARCH set
<binary> package --goos <os> --goarch <arch> --binary <binary> --out <dir>
```

writes `<dir>/sidera-adapter.json` and `<dir>/bin/sidera-adapter-codex` (with `.exe`
for Windows) through `manifest.WritePackage`. `package` itself is `adapterkit.RunPackage`, which every adapter
program shares: the directory must be new or empty, a missing flag or an extra argument is a
usage error (exit 2) and success prints `Wrote the adapter package.` with the platform and the directory. The platforms are `darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64`,
`windows/amd64` and `windows/arm64`. The version in the manifest, in `initialize` and in the client information sent to
the App Server is the release version (`internal/release`; a build without it is `0.0.0-dev`), and
`<binary> version` prints it with the commit and the Go version. Receive budgets are 8000 tokens for both front ends (Codex cuts a tool
result at 10,000 tokens and counts CJK text conservatively); a receive through MCP waits
at most 100 seconds, 20 less than the tool timeout the thread's configuration sets, and
through the command line at most 8, since Codex moves a command that runs longer than
about ten seconds to the background.

### Client setup

A session a person opens themselves reaches Sidera only if Codex has the `sidera` MCP
server and approves its tools, and knows what Sidera is only if it has the Skill. The
adapter's `setup` command (`internal/clientsetup/`, the contract in
[adapter-setup](../adapter-setup/README.md)) puts all three in one **plugin** and installs
it. The state directory holds the plugin's source, `<state_dir>/setup/codex/marketplace/`,
owner-only throughout:

| File | Content |
| ---- | ------- |
| `.agents/plugins/marketplace.json` | The marketplace `sidera` with the one plugin `sidera` (`source` a local path, `policy`, `category`) |
| `plugins/sidera/.codex-plugin/plugin.json` | Name, description and `version`, the adapter's version |
| `plugins/sidera/.mcp.json` | `mcpServers.sidera`: `command` the program, `args` `["mcp"]` (`["--home", <home>, "mcp"]` when the data directory is not the default) and `default_tools_approval_mode: "approve"` |
| `plugins/sidera/skills/sidera/SKILL.md` | `guidance.Skill()`, one self-contained file |

The program is the instance's `sidera_program` when the instance sets one, as for the
sessions the adapter starts, and otherwise the program that ran setup (the request's
`sidera_program`). It is resolved by the one function the adapter's threads use as well
(`codexcfg.ResolveProgram`: looked up, then made absolute), so that a relative
`sidera_program` becomes the same absolute path in the plugin's `.mcp.json` and in the
threads. A name that is not found is written as it was given, and `check` says when it
finds nothing. The record keeps the program the items name, not the one that ran setup.

**Why the legacy plugin layout.** Codex has two plugin layouts. The portable one (the
open "Agent Plugins" format) cannot name an absolute program (a bare name or a path
inside the plugin only) and cannot state an approval mode. A session that nobody is
there to ask fails a tool call that needs approval ("MCP tool call requires approval, but
approval policy is never"), and the tools of Sidera are not all annotated as read-only,
so without `approve` every call of `send` would stop at a prompt or fail. The legacy
layout (`.codex-plugin`, `.mcp.json`) takes the entry as `config.toml` does, command and
approval mode included, and `approve` is honoured under every approval policy. Codex
calls it a compatibility fallback and its own scaffolding still writes it; no removal is
announced.

**The items.** Three are recorded: `plugin_source` (the directory above, no consent: it is
the adapter's own file in its own state directory), `marketplace` (`codex plugin
marketplace add <dir>`) and `plugin` (`codex plugin add sidera@sidera --json`), the last two
with consent, each requiring the ones before it. A fourth, `mcp_server`, is only ever
offered, with the action `remove`: a person's own `[mcp_servers.sidera]` in `config.toml`
takes the place of the plugin's server of the same name, so the plugin's approval and
program never apply. Setup offers `codex mcp remove sidera`. It is offered when the server
Codex lists under that name is not the plugin's (another program or arguments, no
`approve`, disabled), and not while the plugin is about to be installed over an installed
one, because the server Codex lists is then the old plugin's; the next plan or check
judges it. An entry that is the plugin's own (same program, same arguments, approved in
advance, enabled), such as one added exactly as the instructions of earlier releases said, is
kept and not offered: Codex still lets it take the place of the plugin's server (the
configuration layer outranks the plugin layer and replaces the entry whole), so the plugin's
server never starts, but a session gets the same server either way. The entry is offered as
soon as it differs, for instance when the Sidera program moves and the plugin is updated.
The `mcp_server` item is not recorded, so undo does not bring the person's entry back.

**Refusals.** A marketplace named `sidera` that Codex already has from another directory is
never re-pointed: the plan fails with a message that names both directories (Codex itself
refuses to add it). One that Codex has from the directory setup writes, and a plugin that is
installed, but that no record holds (the record was lost, or the instance was removed with
`--force` and made again), are taken into the record with consent (`adopt`); adopting the
plugin installs it again from the current files, which also makes sure that what is installed
is what setup wrote.

**Environment.** Setup runs the Codex command line named by the instance's `codex_program`
with the environment of the program that runs setup, not the daemon's, so `CODEX_HOME` is
that of the person who ran `sidera adapter setup` or `sidera update`. A controller needs the
same `CODEX_HOME` as the daemon anyway. Every Codex command is a
[`adaptersetup.ClientCommand`](../adapter-setup/README.md#running-a-clients-command-line):
the program's absolute path, no shell, no standard input, a time limit of a minute and a
process group that ends with it, and the end of its standard error is part of the error of
a failure. Its working directory is the instance's **state directory**, which holds no
`.codex/config.toml`: Codex reads the project configuration of a trusted project it is
started in, which would change what setup sees and edits from one run to the next. A Codex
that cannot be started because it is not there is reported as not installed.

**The Codex home is recorded.** What setup makes in Codex (the marketplace, the plugin in its
cache) lives in one Codex home, `CODEX_HOME` or else `~/.codex`; a variable that is unset and
one that is empty both mean the default, and a relative value is made absolute against the
directory of the person, because Codex is run in the state directory, where the same text
would name another directory. What setup made in one home is not in another, so the record
keeps which one an item was made in, in the target of the `marketplace` and the `plugin`
(`sidera in CODEX_HOME=/path`, or `sidera in the default Codex home (~/.codex)`). A plan and an
apply work in the home the environment names now, because that is what the person is setting
up; they refuse when the record holds items made in another home, since both homes' marketplaces
would read the one source directory, and the message says to run `sidera adapter setup
<instance> --remove` under the recorded `CODEX_HOME` first. Undo, check and refresh work in the
recorded home: they run `codex` with `CODEX_HOME` set to it, or removed when it was the default,
whatever their own environment says. Without this a removal run under another `CODEX_HOME` would
find neither the plugin nor the marketplace there, report both "already removed", drop the record
and delete the source directory, leaving the real home's marketplace pointing at a directory with
no manifest, which makes Codex fail to list any marketplace or plugin. When the two homes differ,
an undo and a refresh say so in the message of their outcomes and a check in a note.

**What Codex is asked.** `codex plugin marketplace list --json` (decoded strictly; a reply
that is not that list is an error, since reading it as "none" would plan to add a
marketplace that exists), `codex plugin list --json`, `codex mcp list --json` and, for the
approval mode that its JSON does not carry, the text of `codex mcp get sidera`.

**A listing that is slow.** Codex 0.166.0 has no option that lists only the plugins installed
on the machine (`--available` only adds the uninstalled plugins to the JSON), and
`codex plugin list` waits for the remote plugin catalogue, so the first listing after a
while can take longer than the 60 second limit of one run. The next listing finds the
catalogue cached and answers in seconds. So a command that only reads (the listings and
`codex mcp get`) that ran out of time is run once more, and a second timeout, like any other
failure, is final. A command that changes Codex's configuration (adding or removing a
marketplace or a plugin, removing a server) is never run again, because the first run may have
taken effect. The error of a command that ran out of time names the command, says that Codex
did not answer in time, likely while it fetched its remote plugin catalogue, and that running
`sidera adapter setup codex --refresh` again usually succeeds.

**An unreadable marketplace.** When any configured marketplace has no readable manifest,
Codex fails every listing of marketplaces and plugins, and names each marketplace it cannot
read with the directory it has for that one:

```
- `foo` at /path/to/foo: marketplace root does not contain a supported manifest
```

Only an error that names `sidera` at the directory setup writes means that **our** source is
gone. An error that names another marketplace is that marketplace's problem, and setup then
does not know whether the marketplace `sidera` or the plugin exists, or whether the plugin is
enabled: `check` reports one entry (`broken`) saying that Codex cannot list plugins because
that marketplace is unreadable and that the person must repair or remove it; `plan` fails with
the same words and installs nothing; `refresh` rewrites the source files if they are stale
but does not install the plugin (a plugin the person disabled would be enabled again);
`undo` uninstalls the plugin but leaves the marketplace, whose directory it cannot see.
An error that names no marketplace is treated as somebody else's, never as ours.

**After the install, a self-check.** `plugin add` succeeds for a plugin whose layout Codex
no longer reads, and then offers no server. So after installing, setup asks `codex mcp list
--json` for the server `sidera`. If it is not there the `plugin` item is reported as failed,
with a message that says the plugin layout may no longer be supported (the plugin stays
recorded, so that it can be undone). The fallback, if the legacy layout is ever dropped, is a
portable plugin that carries the Skill only, and the MCP server written to `config.toml`
as `[mcp_servers.sidera]` with `default_tools_approval_mode = "approve"`, which Codex reads
whatever the plugin layout is. A person's own entry of that name is then the right place for
the server, and the item above no longer applies.

**Refresh** (what `sidera update` runs; asks nothing, creates nothing). A recorded item is
out of date when its recorded digest is not what this version writes: the plugin's version,
its Skill, the program or the arguments changed. Setup rewrites the source directory, and
then installs the plugin again with `codex plugin add`, which copies the plugin into Codex's
cache again even at the same version. Codex never writes the source directory. It does not
install when the source files or the marketplace are missing or were changed since setup
wrote them, and it does not install when the person disabled the plugin: `plugin add` sets
`enabled = true` again, which would undo their decision. Nor does it install when
Codex cannot list plugins, since whether the plugin is disabled is then not known. All are
reported (an outcome `skipped`, and the drift). A thread that uses the plugin's own server is moved to the new
process within about three seconds, so a tool call in flight at that moment can be lost; the
agent calls again, which is what agents do. A thread the adapter started brings its own
`sidera` server in its configuration, which replaces the plugin's, so such a thread is not
touched.

**Check** lists, besides the digests of the recorded items (`missing`, `changed`, `stale`):
the plugin's installed version is not the recorded one (`changed`); the plugin is disabled
(`broken`; the entry tells the person to enable it in Codex or to run `sidera adapter setup
<instance> --remove`, since setup does not enable it: that would be the person's choice undone); Codex
lists no server `sidera` although the plugin is installed and enabled (`broken`); a server
of that name that is not the plugin's hides it (`broken`, and `setup` offers the removal);
the program the plugin starts does not exist (`broken`). When the source directory is gone
the installed plugin keeps running from Codex's cache while Codex's listings fail (see "An
unreadable marketplace"); check reports the source as `missing` and the marketplace as
`broken` with Codex's message instead of failing. A Codex that cannot be run at all is one
`broken` entry, so that the instance can still be removed; the checks that need no answer
from Codex (the program exists, the source files' digests) are made anyway, and a source
directory that cannot be read is its own `broken` entry that does not blame Codex. A hiding
server whose removal the person declined is not reported again.

**Declines.** The record is read and its items changed; `Declined` is the SDK's to write and
is never touched. The record counts as present only when it holds an item
(`Record.Installed`). The plan offers what the person declined again.

**Removing a hiding server.** `codex mcp remove sidera` edits the user's `config.toml` only.
After it, setup asks Codex again, and when a server of that name that is not the plugin's is
still listed (it comes from a project's or a managed configuration) the item is reported as
failed, not done.

**Undo** uninstalls the plugin (`codex plugin remove`), removes the marketplace (only when it
still points at the directory setup wrote) and deletes the source directory, in that order,
each requiring the one before; an item that is declined, fails or is left because it is no
longer ours stays in the record.

**Adapter threads.** The adapter does not disable the plugin in the threads it starts: the
thread's own `sidera` server (with its credentials in the environment) replaces the plugin's
of the same name, no second server runs, and the plugin's Skill still reaches the thread.
Disabling the plugin for a thread would take the Skill away with it. Nothing in the thread
code depends on setup.

### Known limitations

- **How Codex reacts to an effort the model does not support is not established**, which is why
  the owner lists only supported efforts in `allowed_models`.
- **No acknowledgement on the agent's behalf**; `ack_markers` is absent for the same reason.
- **Interrupting ends the processes the server lists for the turn's commands, and looks no
  further.** What a command started on its own (a process it detached) ends only as far as
  Codex's own termination takes it; that was not measured.
- **An attached session is reached through the shared queue** (see "Attached sessions"):
  seconds of latency, a prompt held behind the person's turn, after Esc and behind an active
  `/goal`, and none delivered while no process has the thread loaded. That a prompt taken by the
  terminal interface keeps its client id is read from the server's source, not measured with it.
- **The command-line front end needs `danger-full-access`** (above).
- **A session's own token is readable by its commands**, and the data directory is
  readable wherever no sandbox applies (see "The data directory is denied to the sandbox"),
  including after an approved escalation. On Windows another Codex session sharing the
  `CODEX_HOME` can revoke the denial.
- **The token is in Codex's logs** and in anything a model prints.
- **A thread's configuration replaces the `-c` layer** of the server's flags and is merged
  into the owner's `config.toml` key by key, tables into tables of the same name (the
  `shell_environment_policy.set` entries of the same name are the thread's, the others stay).
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
- **The server runs in an empty directory outside `SIDERA_HOME`.** Codex makes the server's
  working directory a writable root of sandboxed commands, so the directory it runs in must
  hold nothing a sandboxed command could use: above all no secret of Sidera. A directory under
  the temporary directory was not taken: the system cleans it while the server runs, and
  in a shared one (`/tmp`) another user could create the path first.
- **The sandbox is a profile that names the data directory.** Codex's legacy sandbox modes
  cannot exclude a path, and a sandboxed command reading `SIDERA_HOME` would learn every other
  session's token. The profile is the one mechanism that denies a path in the sandbox itself, and
  it is enforced by the operating system rather than by the adapter's judgment of commands. It is
  checked against the thread's own answer because Codex ignores it without an error when anything
  else names a sandbox; a thread without it is not used, since a silent fallback would be the
  failure the profile exists to prevent.
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
- **The server is replaced when idle, not when Codex is upgraded.** Codex's own managed
  daemon replaces itself on a schedule and says it may interrupt running work; here the
  turn is the session's paid, long work and the one server per instance cannot be run
  beside a second of another version, so the replacement waits for idleness and treats
  anything it does not know as busy. Rejected: ending the server at the next start of
  the adapter (it would cut turns), and starting a second server beside the first (the
  state directory holds one socket and one lock, and the threads would have to move).
- **Only a server shown to be ours is ended, and "cannot ask" means "do not end".**
  The process is named by pid and start key, which the state file records for every
  start, not by the process that happens to hold the pid. A wrong kill ends someone
  else's process; a leftover server is found and reused anyway, so every doubt (a
  table that does not answer, an old record that does not match) leaves it running.
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
| Start, connect, working directory, detach, environment, identity and termination | `internal/server/` (`server.go`, `env.go`, `version.go`; tests `terminate_test.go`, `connect_test.go`) |
| Preset and instance configuration, thread configuration and permission profile, sandbox policy | `internal/codexcfg/` |
| Starting and resuming threads, and checking the profile took | `internal/adapter/thread.go` |
| Creation decisions, the attempt checkpoint and the session checkpoint | `internal/create/` |
| The allowlist, read with the Go SDK's `adapterkit/modelchoice` | `internal/codexcfg/` |
| Reconciliation | `internal/reconcile/` |
| Answers to the server's requests | `internal/serverreq/` |
| Identity, capabilities, manifest, package | `internal/declare/` |
| Client setup: the plugin's files, the Codex command line, the items | `internal/clientsetup/` (`bundle.go`, `client.go`, `handler.go`) |
| The adapter's methods, events, reconnect | `internal/adapter/` |
| The replacement of the server after an upgrade: the gate, the version check, the idleness test | `internal/adapter/replace.go` |
| Attached sessions: attachment, the watch of a person's thread, queued prompts | `internal/adapter/attached.go` |
| The executable and the `package` subcommand | `cmd/sidera-adapter-codex/main.go` |

Tests: `TestCallsAndNotificationsOverBothTransports`, `TestServerRequestsAreAnsweredFromTheHandler`,
`TestPendingCallsFailWhenTheConnectionDrops`, `TestLargeMessagesAreRead` (the client against
the fake server); `TestEnviron` (the environment of the server); `TestStartRunsTheServerInADirectoryOfItsOwn`,
`TestConnectReportsAServerThatRunsInTheStateDirectory` (where the server runs); `TestTerminateEndsOnlyWhatTheRecordProves` (which records may end a process);
`TestServerIsReplacedWhenIdleAfterAnUpgrade`, `TestServerThatIsNotProvenOursIsLeftForSomeoneToRestart`,
`TestFailureToEndTheServerIsRetried`, `TestPromptDuringTheReplacementIsRefusedAsUnavailable`, `TestBusyReason`
(the replacement after an upgrade); `TestParsePreset`,
`TestSandboxIsAProfileNotALegacyMode`, `TestThreadConfig`, `TestFrontendsFollowTheSandbox` (configuration); `TestDecide`,
`TestCheckpointRoundTrip`, `TestSessionCheckpointRoundTrip`, `TestParseSessionCheckpoint` (creation); `TestParsePresetReadsTheAllowlist`,
`TestSupportedEffortsAreTheOnesAPresetMayName` (the allowlist and the efforts); `TestChoiceSurvivesARestartAndAResume`,
`TestSessionWithoutAChoiceFollowsThePreset`, `TestChoiceFillsOnlyWhatTheyStateAndSendsNothingElse`,
`TestRetriedCreationResolvesTheChoiceAgain`, `TestSameAttemptWithAnotherChoiceConflicts`,
`TestRefusedChoiceFailsWithoutStartingAThread`, `TestProbeReportsTheChoiceMembersOfEachPreset`,
`TestUnreadableSessionCheckpointLeavesTheSessionUnreachable` (a requester's choice); `TestJudge` (reconciliation); `TestHandle`
(requests of the server); `TestManifestAndCapabilitiesAreValid`, `TestWritePackage`
(the package); `TestPlanOfAnInstanceWithNothing`, `TestCheck`, `TestRefresh`, `TestUndoLeavesWhatItDidNotUndoRecorded`
and the rest of `internal/clientsetup` (the setup against a fake Codex that names each unreadable marketplace with its own directory); `TestCreateSession`, `TestCreateAfterARestartFindsThePromptInTheRecord`,
`TestCreateStartsOverWhenTheStoredThreadIsGone`, `TestCreateRefusals`,
`TestStartAttachesSessions`, `TestStartAnswersByItsDeadlineWithManySlowSessions`,
`TestStartAnswersBeforeTheServerIsReachedAndLoadsTheSessionsWhenItIs`, `TestDeliverPrompt`, `TestReconnect`, `TestServerRequests`,
`TestCloseSession`, `TestInterruptEndsTheCommandsOfTheTurn`,
`TestInterruptRequestedEndsTheCommandsWhenTheTurnEnds`, `TestInterruptOfAFoundTurnEndsEveryCommandOfTheThread`,
`TestInterruptWithoutATurn`, `TestThreadsTurnsAndCommandsNameThePresetDirectory`,
`TestSandboxIsAPermissionProfileThatDeniesTheDataDirectory`, `TestThreadWithoutTheProfileIsNotUsed`,
`TestAttachSessionOutcomes`, `TestAttachSessionBeforeStartIsAnError`, `TestAttachedHumanTurns`,
`TestAttachedTurnWithoutStartIsStaleFromFirstSight`, `TestAttachedReconcileWhileTheOwnerRunsTheTurn`,
`TestAttachedReadingPaceFollowsWhatIsAwaited`, `TestAttachedPromptDelivery`,
`TestAttachedThreadGoneEndsTheOpenTurnAndTheEndpointIsUnavailable`, `TestAttachedPromptsQueuedAreWithdrawnWhenSuperseded`,
`TestAttachedPromptsQueuedAreWithdrawnWhenTheEndpointDetaches`, `TestAttachedPromptRefusedWhenTheThreadTakesNone`, `TestSessionDetachedStopsTheReading`,
`TestAttachedSessionTakesNeitherCloseNorInterrupt`, `TestStartResumesAttachedSessions` (the adapter against the fake server); `TestRunMCPProbe` (the MCP probe
against a stand-in program).

Against the installed Codex, in `internal/server/realcodex_test.go`,
`realcodex_sandbox_test.go` and `realcodex_schema_test.go`, and `TestRealCodex` in
`internal/clientsetup/realcodex_test.go` (the whole setup with a `CODEX_HOME` of its own:
install, check, refresh, a marketplace of somebody else's that Codex cannot read, a hiding
server, undo), behind `-tags realcodex` and
`SIDERA_REALCODEX=1` (no account and no quota: the one test that runs turns uses a mock model;
the temporary directory must be short enough for a socket path):

```
SIDERA_REALCODEX=1 go test -tags realcodex -count=1 ./cmd/sidera-adapter-codex/...
```

`TestSchemaGuard` generates the schema with `codex app-server generate-json-schema` and
fails when the server's requests are not exactly those `serverreq` answers (a new one
has to be decided on, not refused by default), when a method, member or notification the
adapter uses is missing (the queue methods, `thread/queue/delete` included, and the turn and thread members attached sessions read
are looked up in the experimental schema), or when `clientUserMessageId` or `clientId` is gone.
`TestThreadEnvironmentAndServerCleanup` starts a real server, gives a thread its
credentials through its configuration, reads them back from a command the thread runs,
runs a command under each legacy sandbox policy, checks that a sandboxed command cannot write to the
state directory (Unix), checks the words that say a thread is gone, and
stops the server and checks that its process group is gone.
`TestSandboxedCommandsCannotReadTheDataDirectory` (Unix) runs a real server with an isolated
`CODEX_HOME` that names a mock model provider, which answers each turn with the shell command
the prompt carries; it checks that a thread reports its permission profile when started and
when resumed, that a command in a `workspace-write` or `read-only` thread cannot read a secret
in a throwaway data directory, that a command in a `danger-full-access` thread can, and that the
profile still lets a `workspace-write` command write to its project and keeps a `read-only` one
from writing.

`TestRealCodexInterruptEndsTheRunningCommand`, in `internal/adapter/realcodex_test.go`, is
the one test that runs a real model turn (the model the server lists as affordable, in a
`workspace-write` thread; it spends quota). It runs the adapter against a server of its own
(a state directory of the test, so another adapter's server is never reached), has a turn run
a `sleep` of forty seconds, interrupts, and checks that the answer is `stopped`, that the
process is gone when it comes, that the turn is reported `interrupted`, and that the next prompt
runs. Its thread stays in the Codex home.
