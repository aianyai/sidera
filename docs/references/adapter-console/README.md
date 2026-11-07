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
| `whoami` | Prints the address of the owner endpoint, `<node>/<endpoint>`, as the block `Address  <address>`. |
| `new <runtime-address> --preset <name> [--body <text> \| --body-file <file>] [--timeout <s>, default 120]` | Sends `endpoint.send` with the `session.create` control signal to the runtime endpoint; the body is the initial message. Prints `Session request accepted for delivery.` with the `Message` id and the `To` address at once, so that a long wait shows that something was sent, then asks `endpoint.message_status` once a second until the control reply is there and prints `Session created.` with the `Session` address, the address of the session on the node of the runtime (`Session created, but not confirmed ready.` and exit 10 for an unconfirmed one). |
| `recv [--wait <s>]` | Calls `endpoint.recv` (`wait_s` 0 to 30), prints every message, then `endpoint.ack` for what it printed. Follows a message cut into parts to its last part and takes the rest of the queue. A message is a header block (`From`, `Message`, and when they apply `Part` as `1 of 2`, `Notice` and `Repeat`) and its body, every line of it indented by two columns. With nothing waiting it prints `No messages waiting.` on standard output. |
| `reply <message-id> [--to <address>] (--body \| --body-file)` | `endpoint.send` with `reply_to`, addressed to the sender of that message. Prints `Reply accepted for delivery.` with the `Message` id and, when `--to` was given, the `To` address, and the command that shows whether it was delivered (``Run `sidera-adapter-console status <message-id>` to see whether it was delivered.``). |
| `send --to <address>[,<address>...] (--body \| --body-file)` | `endpoint.send` of a plain message. Prints `Message accepted for delivery.` (which is not that it was delivered) with the `Message` id, the `To` addresses and the same `status` hint. |
| `directory [--node <id>] [--limit <n>] [--cursor <c>]` | Calls `endpoint.directory`: one page of the endpoints a node shows to the owner endpoint, this node without `--node`. A table of `KIND`, `NAME`, `ACCEPTS` (the action names, comma and space between them) and the whole `ADDRESS` last, with `-` for an absent name or an empty list; `No endpoints.` on standard output when the page is empty (it was a line on standard error); when more follow, a sentence with the command for the next page, ``Run `sidera-adapter-console directory [--node <id>] --cursor <c>` for the next page.`` `--limit` is 1 to 100. An outcome other than `ok` is printed with its reason and exits 1. |
| `peers` | Calls `endpoint.peers`: a table of the nodes the owner endpoint can address, this node first: `TRUST` (`self` or `direct`), `CONNECTION` (`connected` or `not connected`) and the `NODE`, whole, last; the node text is what `directory --node` takes. When this node is the only row, `No other node is trusted directly.` follows. |
| `status <message-id>` | `endpoint.message_status`: a table of `STATE` (a rejection adds its reason in brackets) and `TO` for each recipient, then under `Control reply: <signal>` the reply as fields (`Outcome`, `Session endpoint`, `Reason`; for `endpoint.status` the `Activity`, `Queued messages`, `Conditions`, `As of` and `Presets`). `Session endpoint` is the endpoint as the reply carries it, without its node; `new` prints the full address. |
| `package` | Writes an adapter package (below). |
| `version [--json]` | Prints the version of the release this program belongs to (`internal/release`), the commit and the Go version; with `--json`, the object `sidera version --json` writes. |

`--body-file -` reads standard input. Every client command takes `--home` (the data
directory: the flag, then `SIDERA_HOME`, then `~/.sidera` or on Windows
`%LOCALAPPDATA%\Sidera`, by the same function as for `sidera`, `datahome.Resolve`, which also
refuses the former Windows default when it is the only one there) and
`--instance` (default `console`, the name `adapter enable console` gives). Flags may
come before or after the positional argument.

A command that the output tells the reader to run (the `status` command after a send, the
command for the next page of `directory`, the `status` command after a timeout of `new`) goes to
the node and instance of the command that printed it: the hint spells `--home <directory>` after
the command name when the command was given `--home` (a directory from `SIDERA_HOME` is in the
environment of the next command too), and `--instance <name>` when the instance is not
`console`. The values are quoted for the shell of the platform.

`directory` and `peers` need the daemon to have declared the capability
`direct_endpoint_access` in `initialize` (see [adapter-protocol](../adapter-protocol/README.md)).
Without it the adapter sends nothing and the client prints that the daemon of this node
is too old to do it for a direct endpoint, and exits 1. The console does not make
`endpoint.declare` requests: the owner endpoint keeps the declaration it starts with.

Exit codes: 0 done; 1 refused or failed (the daemon's refusal is printed with its
protocol code); 2 usage; 10 the outcome is not known yet, as for `sidera`: `new` got no reply
within `--timeout` (the message id and the `status` command are printed), or the session is
`unconfirmed` (its address is printed, it may exist). `--timeout` defaults to 120 seconds. These are
codes of the table in [the command-line reference](../command-line/README.md#conventions-of-every-program).

Output is written through [`termout`](../../../sdk/go/termout/README.md) as the layout rules of
[the command-line reference](../command-line/README.md#human-output) have it, one stream for
standard output decided when the client starts. The adapter programs have no `--color`
flag, so `NO_COLOR`, `CLICOLOR_FORCE`, `FORCE_COLOR`, `CLICOLOR=0` and `TERM=dumb` and whether the
stream is a terminal decide; a pipe gets the same words and layout without markers and colors.
Error lines (`sidera-adapter-console: <message>`) and their exit codes are written as they are.
An empty result is one sentence on standard output (`No messages waiting.` for `recv`, `No endpoints.`
for `directory`); an empty directory is no longer a line on standard error.

`send`, `reply`, `status`, `directory` and `peers` lay out their results with
[`agentview`](../../../sdk/go/agentview/README.md), the package `sidera` uses for the same
results, with `sidera-adapter-console` as the program the hints name; so the two print a result
in the same words. The console does not complete the `Session endpoint` of `status` to an
address (`new` does, from the recipient of the signal it sent).

### How replies arrive

The session `new` creates replies to the sender of its initial message, the owner
endpoint, with the ordinary `reply`. The daemon queues that message for the direct
endpoint like any message; the adapter does nothing on its own, and the owner reads
it with `recv`. A reply from an agent on another node comes the same way, over the
node's transport. The owner can also be written to by any agent that knows the
address (`whoami`).

A message body is written by the sender, and a sender may be a peer or an agent a
peer's prompt injection controls, so `recv` prints it through `termout.SanitizeText`: control
characters (an escape sequence that clears the screen, sets the window title or writes the
clipboard, a carriage return that rewrites a line, DEL, U+0080 to U+009F), the
bidirectional formatting characters, the invisible format characters that can carry hidden
text (tag characters, zero-width space) and bytes that are not UTF-8 appear as visible
escapes such as `\x1b` and `\u202e`; line breaks and tabs stay. Nothing else `recv` prints needs it:
the sender, the id and the part label are checked where the node reads them, and a
display name is checked at every entry.

The body is indented so that a body shaped like a header (`From     node-9/boss`) cannot pass for
the start of another message: a real header starts in the first column and nothing of a body does.

`recv` shows a message once: the page is acknowledged after it was printed, never
before. A client that dies between the print and the acknowledgement leaves the
page unacknowledged and the next `recv` hands it out again, marked as possibly shown
before.

A message that is the node's own channel feedback (the notice that a message the owner sent
was refused, expired or went unanswered) is printed with the header line `Notice   channel notice,
from the node`, and a part handed out before and not acknowledged with `Repeat   possibly shown
before`: its sender is the node, not an agent, and there is nobody to reply to.
The marker is the page's `feedback` member, which the daemon sends because the kit the
console is built on declares that it decodes it.

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
`ack`, `directory`, `peers`) with the parameters of the `endpoint.*` call it becomes; the adapter fills in
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
and no configuration. The directory must be new or empty (checked by `adapterkit.RunPackage`, the `package` and
`version` every adapter program shares; the files are written by `manifest.WritePackage`). The supported platforms are the
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
  node, and that node is the one of the runtime address `new` was given.
  `new` prints the node of the message's only recipient (`sessionAddress`) with it;
  `status` prints the endpoint as the reply carries it, until the Agent interface returns
  the full address. The node of this adapter would be right only for a
  runtime on the same node, and sending to that address anywhere else is rejected
  as `endpoint_missing`.
- **The adapter says when the daemon is too old.** Only the adapter holds the
  capabilities the daemon declared, and the SDK refuses the call before sending it, so the
  client learns it as an ordinary failure of the request with a readable reason, and no
  request reaches a daemon that would answer it with an unknown-method error. A page that
  could not be given (`unavailable`, `refused`) is a result of the call, not a failure of
  it, and the client turns it into the exit code.
- **No `declare` in the console.** The owner endpoint keeps the declaration it is
  registered with; nothing a person does at a terminal needs another.
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
  `TestStatusPrintsTheControlReplyAsFields`,
  `TestDirectoryPrintsATableAndTheCommandForTheNextPage`, `TestResultsGoThroughTheSharedLayout`,
  `TestDirectoryThatGaveNoPageFailsWithItsReason`, `TestPeersPrintsATableWithoutTheSelfFlag`,
  `TestDirectoryAndPeersSayWhenTheDaemonDoesNotSupportThem`,
  `TestRecvPrintsThenAcknowledgesAndFollowsPartsAndPages`,
  `TestPrintMessageSanitizesAndIndentsTheBody`,
  `TestReplyGoesToTheSenderSeenInRecv` (the adapter over pipes against a stand-in
  for the daemon), `TestTokenTransportServesOnlyAClientThatPresentsTheToken`,
  `TestListenTakesOverAStaleSocketAndLeavesOtherFilesAlone`,
  `TestEveryTargetGetsAManifestTheDaemonAccepts`.
- Not yet confirmed: the Windows transport has been built and vetted for
  windows/amd64 but not run on Windows; a run against a real daemon was made on
  macOS only.
