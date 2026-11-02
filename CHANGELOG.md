# Changelog

All notable changes to Sidera are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Every program of a
release (`sidera` and the adapters) reports the release version; the adapter
protocol has a version of its own.

## [Unreleased]

## [0.9.0] - 2026-11-02

A Claude Code or Codex session you opened yourself can now take part as a controller:
it has an endpoint of its own on the node and is woken when a reply arrives. The release
also ships a Windows arm64 package. State written by 0.8.0 needs no migration. The adapter
protocol stays at version 1, and the daemon sends nothing new to an adapter that does not
declare the new capability `attach`; the Go SDK has source-incompatible changes, listed
under Changed.

### Added

- **Controller sessions.** A Claude Code or Codex session you opened becomes a controller
  when it has the `sidera` MCP server (or runs `sidera` agent commands) and no `SIDERA_TOKEN`:
  it attaches itself through `sessions.attach` on first use, keeps the token in memory only
  and uses the ordinary Agent tools. `sidera mcp` repeats the attachment every minute while it
  runs, only finding the endpoint and never making one, and tells the model, in a second text item
  of the first result, whether the session can be woken. When the endpoint is closed the next
  tool call attaches the session as a new endpoint and its result names the new address and says
  the old one is closed.
- **`sidera setup controller`.** It prints the commands that add the MCP server to Claude
  Code and Codex, naming the program by its absolute path (a client starts it without a shell and may
  not find the bare name on its `PATH`: Codex on Windows does not) and the data directory with `--home`
  when it is not the default, and the permission each client needs so that a woken controller does not
  stall at a prompt: the rule `mcp__sidera` for Claude Code, the line
  `default_tools_approval_mode = "approve"` for Codex.
- **A resumed Claude Code session is woken at once.** When it starts, the `sidera` MCP server
  of a Claude Code session looks for an endpoint the session already has and, if it finds one,
  keeps its token and starts the heartbeat, so that what waited for the session while it was
  away reaches it without a first tool call. A session with no endpoint is left alone until its
  first tool call.
- **A session that attaches again is prompted at once.** An attachment judges the endpoint for
  a prompt without the minute that follows a failed prompt, so that a session the person resumed
  is prompted for what waited while it was away.
- **An attached endpoint is closed when its controller is gone.** Every attachment counts as the
  heartbeat of the session's controller. An attached endpoint whose controller has not attached
  for the endpoint's queue time (30 minutes by default, and never less than three minutes),
  counted in the time the node was awake, is closed once nothing is queued or leased on it.
- **Attached sessions are outside the node's turn cap.** The setting `node_prompt_turns` no
  longer counts the turns of attached sessions, and a full node no longer holds back their
  prompts.
- **A Windows arm64 package.** A release carries `sidera_<version>_windows_arm64.zip` beside
  the five archives it had, with the Codex and console adapter packages for windows/arm64. The
  Claude Code adapter stays macOS only.
- **Claude Code adapter: attached sessions.** The adapter declares `attach`. A Claude Code
  session a person opened and that attached itself is woken through the session's inbox socket
  and followed through its registry entry and its session record: the turns the person starts are
  reported without an attempt (also when a busy session goes from one turn straight to the next),
  `/clear` is followed, and a session that is gone refuses prompts as `native_unavailable`.
  Closing or interrupting such an endpoint is a wrong endpoint kind.
- **Codex adapter: attached sessions.** The adapter declares `attach` and takes hold of a Codex
  thread a person opened: it reads the thread's turns (once a second while it waits for a
  prompt it queued or a turn is open, every ten seconds otherwise), reports the turns the person
  runs, and delivers a prompt by queueing it in the Codex home it shares with the person's
  Codex (delivered within about ten seconds when the session is idle, after the current turn
  when it is busy, and held while no Codex process has the thread loaded). At most one wake-up
  of its own waits in the person's queue: an older one nobody took is withdrawn when a newer
  one is queued and when the endpoint detaches. The adapter's connection now asks for Codex's
  experimental API, which the queue needs. A thread that takes no prompt (archived, unknown) is
  reported as the availability condition `native_unavailable` of that endpoint alone, until it
  takes one again or the endpoint detaches.
- **Management: `sessions.attach`.** The owner can attach a session a person opened to an
  adapter that declared `attach`: the answer is the endpoint, its token and whether the adapter
  can deliver prompts to the session. The request is safe to repeat, carries no operation
  identifier and stores no result. Because the answer holds a token, a sandbox has to deny the
  management socket to an agent. With the parameter `create` set to false the method only finds:
  it answers like any attachment when the session already has an open endpoint, and with
  `not_found/session_not_attached` when it has none, without making one. `endpoints.list` shows
  the `origin` of a session endpoint.
- **Endpoint record: the origin of a session.** A session endpoint record can say that the
  session was attached rather than created (`origin`). Records without it read as created, so
  existing state needs no migration.
- **Adapter protocol: attached sessions.** An adapter can declare the runtime capability
  `attach` and take hold of a session a person opened: the new request `session.attach`, the
  notification `session.detached` and the `attached` and `wake` members of an `adapter.start`
  session entry. The daemon sends none of them to an adapter that did not declare it. The Go SDK
  has the types (`adapterproto`, with `RuntimeCapabilities.Attach`) and serves them through
  `adapterkit` only for an adapter that declared `attach`.

### Changed

- **Go SDK, incompatible: attached sessions change three shapes.** An adapter written against
  0.8.0 has to be rebuilt and may need edits:
  - `adapterproto.SessionAttach.ToolAccess` is now a `*ToolAccess`, absent for an attached
    entry, and `Preset` may be empty for one. Code that reads either of them from an
    `adapter.start` entry has to dereference the pointer; an adapter that does not declare
    `attach` never receives an attached entry, so the pointer is never nil for it.
  - `adapterkit.Application` gained the methods `AttachSession` and `SessionDetached`. An
    application that embeds `adapterkit.Base` keeps compiling and refuses `session.attach`; an
    application that implements the interface by itself has to add both.
- **The reply to a control signal reaches its sender sooner.** The sender's first pulls for the
  reply to a control signal (the address of a session just created, the answer to a close) come
  2, 5 and 10 seconds apart, then once a minute as for any delivery; before, the reply was seen
  a minute after the signal. Messages are pulled once a minute as before.
- **Codex adapter: the thread's `sidera` MCP server states `enabled = true`.** A person who
  also has a `[mcp_servers.sidera]` table in their own `config.toml` has it merged key by key
  with the one the adapter gives a session; the adapter now states every key it depends on, so
  an `enabled = false` in the person's table no longer switches the session's channel off. A
  `url` key in that table still makes the thread fail to start and has to be removed.

### Fixed

- **A pull that brings no news writes nothing.** The sender no longer rewrites the record of a
  delivery each minute for as long as the target repeats what the record already holds.

## [0.8.0] - 2026-11-02

The first public release. Sidera is a daemon through which AI agents, on one
machine or on several, exchange messages. A release is one archive per platform:
`sidera`, `sidera-adapter-console` (which is also the owner's command-line client)
and the adapter packages `sidera adapter install` takes, with the licences of the
software they are built from, for macOS (arm64 and amd64), Linux (amd64 and arm64)
and Windows (amd64).

### Added

- **The node.** `sidera init` creates a node: its private data directory, its keys
  and its default configuration. `sidera daemon` runs it in the foreground. State
  is written so that it survives a crash or a power loss, and the daemon recovers
  it when it starts.
- **Agent interfaces.** An agent on the node uses the command line (`sidera me`,
  `declare`, `peers`, `directory`, `send`, `reply`, `recv`, `ack`, `status`,
  `control`, `probe`) or MCP over standard input and output (`sidera mcp`). Both
  reach the same node through a checked local connection and a credential of the
  agent's own, and the command line can print JSON. An agent declares what it accepts: it can
  pause, refuse messages to several recipients, accept messages only from named
  senders, or stay out of the directory.
- **Delivery.** Messages have a delivery state and a deadline, are deduplicated
  by key, are read in pages with a lease and are acknowledged by part; a message
  that arrives while the receiving agent is not reading can wake it through its
  adapter. `sidera status <message-id>` reports where a message is.
- **Adapters.** An adapter connects a kind of agent to the node. The daemon starts
  it from an installed package and speaks the adapter protocol (version 1) with it.
  `sidera adapter install` installs a package from a directory (the same version
  with other contents replaces the installed one and restarts the instances that
  run it), `enable` and `disable` start and stop an instance, `config` replaces an
  instance's configuration, `remove` removes a disabled instance, and `list`
  shows what is installed and what each probe found. `sidera preset` keeps the
  presets that sessions are created from.
- **The Claude Code adapter** (macOS). It creates Claude Code sessions as `claude -p`
  processes that outlive the adapter, delivers messages to them and reports how
  each turn ended.
- **The Codex adapter** (macOS, Linux and Windows). It keeps a private Codex App
  Server, makes each session a thread of it, delivers messages to it and answers the
  requests Codex sends.
- **The console bridge.** `sidera-adapter-console` gives the node's owner an
  endpoint of their own: `new` starts a session on a runtime endpoint, and `send`,
  `reply`, `recv`, `status` and `whoami` message agents and read their answers from
  a terminal.
- **Several nodes.** Nodes reach each other directly over TCP and QUIC (libp2p),
  with their own identities, on any network where one can dial the other's address
  (for example a local network). `sidera network` shows where the node listens and
  the addresses kept for other nodes, and sets them; `init --port` chooses the
  port (7744 by default).
- **Pairing.** Two nodes become trusted when each owner has named the other node in
  advance: `sidera trust expect <node>` states that the owner means to pair with a
  node, and `trust request <node>` then completes the pairing (the verification code
  it prints is informational). `trust list` shows what is held, `trust reject` ends
  a request, `trust forget` withdraws an expectation, `trust remove` removes a node from the node's trust and revokes the
  credentials held with it, and `trust clear` takes a removed node back.
- **Messages between nodes.** `sidera peers` lists the node and the nodes it trusts,
  `sidera directory --node <id>` lists the endpoints another node shows, and `send`
  and `reply` carry messages between agents on different nodes.
- **Sessions on another node.** `sidera control <address> session.create` creates a
  session on a runtime endpoint of a trusted node, optionally with an initial
  message, and returns the session's address.
- **Owner commands.** `sidera node`, `endpoint`, `alerts` and `clock` show and change
  node settings, endpoint settings, alerts and the clock rollback pause. `sidera
  identity` exports the statement that retires the node's identity, confirms that it
  is kept outside the device, and retires the identity.
- **`sidera version`** prints the version, the commit and the Go version (`--json`
  for an object), and every adapter program prints the same with `version`.
- `sidera status` shows the data directory the command used (`home`, also in `--json`).

### Security

- **The start checks the whole data directory.** The daemon refuses to start when
  anyone but the owner can reach the data directory, a directory below it, a key
  file or a file that holds a token (an endpoint's token, an adapter's state files).
  The refusal names the path, the account or group by SID and name, and the rights.
  Sidera never changes a permission itself. An adapter's state directory is judged
  again before the adapter is launched.
- **The Windows data directory is `%LOCALAPPDATA%\Sidera`.** It was
  `%USERPROFILE%\.sidera`, whose first-level folder some sandboxes grant their own
  group access to. macOS and Linux keep `~/.sidera`; `SIDERA_HOME` and `--home` choose
  another directory everywhere. Nothing is moved automatically: a Windows machine with
  the former directory and none in the new place gets a message with the
  `Move-Item "$env:USERPROFILE\.sidera" "$env:LOCALAPPDATA\Sidera"` command.
- **The Codex App Server runs in an empty directory of its own.** Codex takes the
  working directory of the server as the writable root of a sandboxed command, so a
  server started in the adapter's state directory let a `workspace-write` command write
  there, `ws-token` included, on every OS. The server now runs in an empty
  per-instance directory under the user's cache directory, outside the data
  directory. A server that an earlier build started in the state directory is reported
  by the probe as `wrong_version` until it is restarted; the adapter does not end a
  server it did not start.
- **Sandboxed Codex presets cannot read the data directory.** A preset with a
  `workspace-write` or `read-only` sandbox runs under a permission profile that
  denies reading the data directory (the identity key and every session's token). The
  operating system enforces it: Seatbelt on macOS and bubblewrap on Linux, where it was
  verified, and a deny entry for the sandbox's accounts on Windows, where another Codex
  session can revoke it and the protected data directory is the main guard. A session
  that does not carry the profile is not used. Presets with `danger-full-access`
  are unchanged and read everything, and so do the user's own MCP servers and commands
  approved to run outside the sandbox.
- **Claude Code's file tools are denied the data directory, as best effort.** Every
  turn denies `Read` and `Edit` of it, whatever the preset or the user's own settings
  allow. A Bash command that the user's settings allow and that reads without naming
  the file (`grep -r` on the parent directory, for example) is not covered; a guarantee
  needs Claude's operating-system sandbox, which is not used yet.

### Not yet

- NAT traversal and relays: two nodes must be able to dial each other.
- Inherited trust: a node trusts the nodes it paired with, not the nodes they
  trust.
- Remote management by people: a node is managed from the machine it runs on.
- Chat adapters.
- Installing the daemon as a background service: `sidera daemon` runs in the
  foreground.
- The Claude Code adapter on Linux and Windows.

[Unreleased]: https://github.com/aianyai/sidera/compare/v0.9.0...HEAD
[0.9.0]: https://github.com/aianyai/sidera/releases/tag/v0.9.0
[0.8.0]: https://github.com/aianyai/sidera/releases/tag/v0.8.0
