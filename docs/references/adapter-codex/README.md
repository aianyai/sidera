# Codex adapter

How the Codex adapter keeps and talks to its Codex App Server: the private server
it runs, its connection, and how each session gets its own credentials in a server
that all sessions share. The adapter lives under `cmd/sidera-adapter-codex/`,
outside the top-level `internal/`, so the core cannot import it and contains no
product name. The protocol it implements is in
[adapter-protocol](../adapter-protocol/README.md).

The App Server is Codex's experimental JSON-RPC interface (`codex app-server`).
The adapter is written against one release of it.

## What

### The App Server

The adapter runs `codex --no-daemon app-server` itself and does not use the
managed daemon of `codex app-server daemon`. The managed daemon is shared with the
owner's other Codex programs, its `update` interrupts work in progress, and its
version is not ours to fix.

| Platform | Listens on | Reached by |
| -------- | ---------- | ---------- |
| Unix | `--listen unix://<state_dir>/app-server.sock` | WebSocket over the Unix socket. Codex makes the socket `0600` in a `0700` directory, symlinks it at the path given, and takes a lock per path, so a second server for the same path cannot run. |
| Windows | `--listen ws://127.0.0.1:<port> --ws-auth capability-token --ws-token-file <state_dir>/ws-token` | WebSocket over loopback with `Authorization: Bearer <token>`. The port is found by binding port 0 and releasing it; the port and the pid are in `app-server.json`, the token (`0600`, random, 32 bytes) in `ws-token`. |

`<state_dir>` is the instance's private state directory, which the daemon makes and gives the adapter in `initialize` (`instance.state_dir`). On Unix the socket path must
fit the socket address limit; the adapter refuses a longer one with a message that
says so (the limit is checked at 100 bytes, which fits macOS's 104).

**Connect or start.** `Server.Connect` first tries the address. A server that answers is used: the server is started detached so that it outlives the
adapter, and finding the one a previous run left is the normal case after the
adapter restarts, with its turns still running. Otherwise it starts one and waits up
to 15 seconds for it to answer. A server that exits before it answered ends the wait
with an error that names the log and says an earlier server may hold the socket.
Codex replaces a stale socket left by a server that was killed.

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
could be anyone's by now and the adapter does not verify a process's identity. The
adapter's own shutdown does not stop the server. On Windows only the process itself
is ended, not what it started.

**Versions.** `server.CLIVersion` runs `codex --version`. The server's own version is in
the user agent it answers `initialize` with (`appserver.ServerVersion`), so the two can
be compared: they differ when Codex was upgraded under a running server.

### The connection

`internal/appserver` is a small client, written by hand rather than generated or
taken from a library:

- **Frames have no `"jsonrpc"` member**, in either direction, so the peer of the SDK
  (which requires it) cannot be used. A frame with a method and an id is a request
  of the server, with a method only a notification, with an id only an answer.
  Unknown members are ignored, so a release that adds one does not break the client.
- **The read limit is 32 MiB.** The server sends messages up to 16 MiB; the WebSocket
  library's default limit of 32 KiB would end the connection on the first large read
  of a thread.
- **Requests of the server are answered on the read loop**, at once, from a pure
  decision; **notifications are queued without bound and handed to one goroutine in
  arrival order**, so the read loop never waits for the adapter, and a notification
  is never dropped. `Done` closes after the last queued notification was handled.
- **The client can opt out of** streamed output and reasoning
  (`optOutNotificationMethods`, `QuietNotifications`): they are most of the traffic and
  the adapter reads none of it.
- **A connection only receives the events of the threads it started or resumed.** When
  the last connection that holds a thread ends, the server unloads the thread, and the
  next `thread/resume` starts its MCP servers again.

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

The values are the `tool_access.env` the daemon gives for a session.

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

### Known limitations

- **The token is in Codex's logs** and in anything a model prints.
- **A thread's configuration replaces the `-c` layer** of the server's flags and the
  owner's `shell_environment_policy.set` entries of the same name; whether it replaces
  that table as a whole is not measured.
- **Linux and Windows are not yet run**: a server under `systemd` (its cgroup ends
  with the unit), `--listen unix://` on Windows (which would unify the transports), the
  header the capability token needs, breakaway from the daemon's job, ending a whole
  process tree, and environment variables that are not valid UTF-8.

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
- **Nothing in the server's `-c` flags.** A thread that has its own configuration
  ignores them, so a setting put there would hold for some threads and not for others.
- **Only the server this process started is ended.** The alternative needs a check of
  the identity of a process from a pid and a start time on every platform, which is
  other work; a wrong kill is worse than a leftover server, which is found and reused
  anyway.
- **A hand-written client and subset of types.** The schema is large with many `oneOf`s,
  and a generated copy would carry all of it and its instability. A test against the
  installed Codex pins what is used. The community Go SDKs found target an older
  Codex, start the server as a child over standard input where this adapter needs to
  reach one that outlives it, and are far larger than the dozen methods used. `github.com/coder/websocket` is
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

Tests: `TestCallsAndNotificationsOverBothTransports`, `TestTCPWithoutTheTokenIsRefused`,
`TestServerErrorIsAnRPCError`, `TestServerRequestsAreAnsweredFromTheHandler`,
`TestPendingCallsFailWhenTheConnectionDrops`, `TestLargeMessagesAreRead` (the client
against the fake server); `TestEnviron` and `TestParseCLIVersion` (the server's
environment and version).

Against the installed Codex, in `internal/server/realcodex_test.go`, behind
`-tags realcodex` and `SIDERA_REALCODEX=1` (no model turn; the temporary directory must
be short enough for a socket path):

```
SIDERA_REALCODEX=1 go test -tags realcodex -count=1 ./cmd/sidera-adapter-codex/...
```

`TestThreadEnvironmentAndServerCleanup` starts a real server, gives a thread its
credentials through its configuration, reads them back from a command the thread runs,
runs a command under each sandbox policy, checks the words that say a thread is gone, and
stops the server and checks that its process group is gone.
