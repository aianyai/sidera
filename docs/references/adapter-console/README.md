# Console adapter

How the node's owner starts a first agent and reads what comes back, without
anything in the core that knows about an owner. The console adapter registers one
direct endpoint, `owner`, on the node; a client program run in a terminal sends
from it and reads it. It lives in `cmd/sidera-adapter-console/`, outside the
top-level `internal/`, and imports only the SDK, as a third-party adapter would, and
`internal/release` for its version alone.

## What

### The two roles

One program, `sidera-adapter-console`, is both halves.

- **The adapter** (`serve`) is what the daemon starts, from the `entry.args` of the
  package manifest. It declares `direct: {max_endpoints: 8}` and no runtime, so it
  has no presets and no session endpoints. In `adapter.start` it opens the local
  control channel and answers; right after, it registers a direct endpoint with the
  key `owner`; the same key is the same endpoint, so the address is the same after
  every restart of the adapter and of the daemon. The registration is not part of
  the answer to `adapter.start` because the daemon holds `endpoint.*` requests until
  it is running (see [adapter-protocol](../adapter-protocol/README.md)); the adapter
  retries it with a growing pause, and until it is done every local request is
  refused with an error saying the owner endpoint is not registered yet.
- **The client** is every other command. It finds the control channel of the
  instance, sends one request and prints the answer.

The owner's endpoint is an endpoint like any other for the rest of the node: other
agents send to its address, and the owner's own policy and the sender's rules
apply to what arrives.

### Commands

| Command | Does |
| ------- | ---- |
| `whoami` | Prints the address of the owner endpoint, `<node>/<endpoint>`. |
| `new <runtime-address> --preset <name> [--body <text> \| --body-file <file>] [--timeout <s>]` | Sends `endpoint.send` with the `session.create` control signal to the runtime endpoint; the body is the initial message. Prints `message: <id>`, then asks `endpoint.message_status` once a second until the control reply is there and prints `session: <address>`, the address of the session on the node of the runtime. |
| `recv [--wait <s>]` | Calls `endpoint.recv` (`wait_s` 0 to 30), prints every message with its sender and id, then `endpoint.ack` for what it printed. Follows a message cut into parts to its last part and takes the rest of the queue. |
| `reply <message-id> [--to <address>] (--body \| --body-file)` | `endpoint.send` with `reply_to`, addressed to the sender of that message. |
| `send --to <address>[,<address>...] (--body \| --body-file)` | `endpoint.send` of a plain message. |
| `status <message-id>` | `endpoint.message_status`: the delivery state of each recipient and the control reply. |
| `package` | Writes an adapter package (below). |
| `version` | Prints the version of the release this program belongs to (`internal/release`), the commit and the Go version. |

`--body-file -` reads standard input. Every client command takes `--home` (the data
directory: the flag, then `SIDERA_HOME`, then `~/.sidera` or on Windows
`%LOCALAPPDATA%\Sidera`, by the same function as for `sidera`, `datahome.Resolve`, which also
refuses the former Windows default when it is the only one there) and
`--instance` (default `console`, the name `adapter enable console` gives). Flags may
come before or after the positional argument.

Exit codes: 0 done; 1 refused or failed (the daemon's refusal is printed with its
protocol code); 2 usage; 3 not settled: `new` got no reply within `--timeout`
(the message id and the `status` command are printed), or the session is
`unconfirmed` (its address is printed, it may exist).

### How replies arrive

The session `new` creates replies to the sender of its initial message, the owner
endpoint, with the ordinary `reply`. The daemon queues that message for the direct
endpoint like any message; the adapter does nothing on its own, and the owner reads
it with `recv`. A reply from an agent on another node comes the same way, over the
node's transport. The owner can also be written to by any agent that knows the
address (`whoami`).

`recv` shows a message once: the page is acknowledged after it was printed, never
before. A client that dies between the print and the acknowledgement leaves the
page unacknowledged and the next `recv` hands it out again, marked as possibly shown
before.

### The control channel

The channel is local to the machine and reachable by the owner alone. Its address
is derived from the data directory and the instance name as
`adapter-state/<instance>` in the data directory, the state directory the daemon
makes owner-only and sends in `initialize` (see
[adapter-host](../adapter-host/README.md)). The adapter is given the path, the
client derives the same one, in a single function (`stateDir`).

- **Unix**: the socket `console.sock` in the state directory, mode 0600. A socket
  left by an adapter that died is removed when the next one starts, which is safe
  because the instance lock is held by then; anything else at that path is left
  alone and the adapter does not start. A path over about a hundred bytes does not
  fit a socket address; the error says so.
- **Windows**: a loopback TCP port. The adapter writes the port and a random token
  to `console.addr` in the state directory, whose ACL the daemon restricts to the
  owner, and the client sends the token as the first line of its connection; a
  connection without it is closed. The file is replaced at every start and removed
  when the adapter closes the channel.

The protocol on the channel is one request per connection, one line of JSON each
way, at most 1 MiB. A request is an operation (`whoami`, `send`, `status`, `recv`,
`ack`) with the parameters of the `endpoint.*` call it becomes; the adapter fills in
the endpoint, so a client never names one. An answer is the result of that call,
or an error with the daemon's code. The adapter calls the daemon with the context
of the request, so a recv waits as long as the client asked, up to 30 seconds.

### The package

`sidera-adapter-console package --goos <os> --goarch <arch> --binary <file> --out
<directory>` writes a package for one platform: `sidera-adapter.json` and
`bin/sidera-adapter-console` (`.exe` for Windows, in `entry.program` too: `manifest.WritePackage` adds it to both). The manifest is built from the
constants `initialize` answers from (the version is the release version), so the two cannot disagree, and validated with
`manifest.Validate`. Its `platforms` is the one platform of the package, `entry.args`
is `["serve"]`, and it declares no budgets (a direct endpoint takes the default)
and no configuration. The directory must be new or empty (checked here; the files
are written by `manifest.WritePackage`, which every adapter's `package` shares). The supported platforms are the
six the project builds for: darwin/arm64, darwin/amd64, linux/amd64, linux/arm64,
windows/amd64 and windows/arm64.

```
go build -o /tmp/console ./cmd/sidera-adapter-console
/tmp/console package --goos darwin --goarch arm64 --binary /tmp/console --out /tmp/console-pkg
sidera adapter install /tmp/console-pkg
sidera adapter enable console
```

## Why

- **An adapter, not the core.** The core's endpoints for people would need a
  reserved sender that authorization, the envelope and the engine all special-case.
  A direct endpoint is the path the protocol gives to something that reads for a
  person; the owner's console is the smallest such bridge, and a chat bridge
  follows the same shape.
- **The client is the same program.** The adapter has to keep the connection to
  the daemon, and only it can call `endpoint.*`; a separate client binary would be a
  second thing to install and version together. The adapter has no logic of its
  own beyond filling in its endpoint and resolving the sender of a reply.
- **Acknowledge after printing.** An acknowledgement says the person has seen the
  message, which is true only once it was written. The adapter never acknowledges
  by itself.
- **No `endpoint.pending` handling and no buffer in the adapter.** The notice is
  best effort and `recv` already waits; the daemon holds the messages. Nothing in
  the adapter would survive a restart, so nothing there needs repair.
- **The sender of a reply is remembered in memory only.** A direct reply must name
  the one message's sender, and a client process lives for one command, so the
  adapter keeps the senders of what `recv` showed it. After a restart the map is
  empty and a reply asks for `--to`, which is printed by `recv`; persisting it would
  add state to repair for a case a person fixes with one flag.
- **Registration follows the start.** On a restart of the daemon, `adapter.start` is
  answered before the daemon runs, and the daemon serves `endpoint.register` only
  once it runs; registering inside the start would wait out the host's handshake
  timeout at every restart. Refusing local requests for the moment in between is
  better than answering with an address that is not there.
- **The address is `<node>/<endpoint>`.** The adapter gets the node text in
  `adapter.start` and the endpoint in the result of `endpoint.register`; the
  protocol calls both opaque, and the console writes an address from them the way
  the node does (`ident.Address`). For the owner endpoint it is used for display
  only: a wrong form shows in `whoami`, which is checked against `sidera endpoint
  list`.
- **The session is on the node of the runtime, not on this one.** The control reply
  names the session endpoint as the node that made the session knows it, without a
  node, and that node is the one of the runtime address `new` was given. The
  console prints the node of the message's only recipient (`sessionAddress`), for
  `new` and for `status` alike; the node of this adapter would be right only for a
  runtime on the same node, and sending to that address anywhere else is rejected
  as `endpoint_missing`.
- **Polling lives in the client.** `new` is a `send` and a series of `status`
  requests; the adapter stays a mapping of one request to one call, with no timers.
- **A token on Windows.** The repository's own local interfaces use named pipes
  with a descriptor and a check of the server, because they carry the owner's
  commands. The console channel carries what the owner types to an agent, in a
  directory already restricted to the owner; a pipe would need a descriptor and its
  own server check, and a token in that directory gives the same reach with code that
  runs on every platform and is tested everywhere. What it does not cover: another
  process of the same user can read the token, as it can read the socket on Unix.

## Where

- `cmd/sidera-adapter-console/internal/console`: `adapter.go` (the adapter role and
  the local server), `client.go` and `flows.go` (the commands), `wire.go` (the local
  protocol), `transport_unix.go`, `transport_tcp.go` and `transport_windows.go`
  (the control channel), `paths.go` (the state directory and data directory),
  `pack.go` (the manifest and `package`).
- Tests: `TestCommandsBecomeTheEndpointCallTheyName` (command to request),
  `TestStartRegistersTheOwnerEndpointAndWhoamiNamesIt`,
  `TestStartIsAnsweredBeforeTheOwnerEndpointIsRegistered`,
  `TestNewSendsTheControlSignalAndPollsForTheReply` (a runtime on another node),
  `TestStatusNamesTheSessionOnTheNodeOfTheRuntime`,
  `TestRecvPrintsThenAcknowledgesAndFollowsPartsAndPages`,
  `TestReplyGoesToTheSenderSeenInRecv` (the adapter over pipes against a stand-in
  for the daemon), `TestTokenTransportServesOnlyAClientThatPresentsTheToken`,
  `TestListenTakesOverAStaleSocketAndLeavesOtherFilesAlone`,
  `TestEveryTargetGetsAManifestTheDaemonAccepts`.
- Not yet confirmed: the Windows transport has been built and vetted for
  windows/amd64 but not run on Windows; a run against a real daemon was made on
  macOS only.
