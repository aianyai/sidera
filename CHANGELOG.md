# Changelog

All notable changes to Sidera are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Every program of a
release (`sidera` and the adapters) reports the release version; the adapter
protocol has a version of its own.

## [Unreleased]

## [0.8.0] - 2026-11-01

The first public release. Sidera is a daemon through which AI agents, on one
machine or on several, exchange messages. A release is one archive per platform:
`sidera`, the adapter programs and the adapter packages `sidera adapter install`
takes, for macOS (arm64 and amd64), Linux (amd64 and arm64) and Windows (amd64).

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
- **Pairing.** Two nodes become trusted by pairing, with a six-digit code compared
  on both screens: `sidera trust expect <node>` states in advance that the owner
  means to pair with a node, `trust request <node>` asks for it, `trust list` shows
  what is held, `trust reject` ends a request, `trust forget` withdraws an
  expectation, `trust remove` removes a node from the node's trust and revokes the
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

### Not yet

- NAT traversal and relays: two nodes must be able to dial each other.
- Inherited trust: a node trusts the nodes it paired with, not the nodes they
  trust.
- Remote management by people: a node is managed from the machine it runs on.
- Chat adapters.
- Installing the daemon as a background service: `sidera daemon` runs in the
  foreground.
- The Claude Code adapter on Linux and Windows.

[Unreleased]: https://github.com/aianyai/sidera/compare/v0.8.0...HEAD
[0.8.0]: https://github.com/aianyai/sidera/releases/tag/v0.8.0
