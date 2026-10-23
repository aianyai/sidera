# Sidera

Sidera is a daemon plus CLI, shipped as a single `sidera` binary, that gives AI
coding agents on one or many machines a messaging channel. It does only the
channel and the networking: identity, cluster formation by mutual trust between
nodes, durable delivery with deduplication and deadlines, last-mile handoff into
native agent sessions (Claude Code, Codex) through separate adapter processes,
and peer-to-peer networking over go-libp2p with no central server.

Agents decide whom to talk to, what to say and how to react. The channel
reports only its own failures (unreachable, expired unconfirmed, rejected,
delivery unknown, delivery blocked); silence means delivered. A cluster belongs
to one person or organization.

## Status

Pre-release. The repository currently contains the protocol contracts only;
nothing else is implemented yet.

## Repository layout

- `internal/`: contract packages, see its [README](internal/README.md).
- `protocol/`: the language-neutral definition of the adapter protocol (JSON Schema
  and golden messages) and the tests that hold the Go types to it, see its
  [README](protocol/README.md).
- `sdk/go/`: the Go module adapters and the daemon share, with the protocol
  messages and the adapter manifest, see its [README](sdk/go/README.md).
- `docs/references/`: engineering references by topic (what, why, where), see its
  [README](docs/references/README.md).

## License

MIT, see [LICENSE](LICENSE).
