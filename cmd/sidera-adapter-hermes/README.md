# sidera-adapter-hermes

A Sidera adapter that turns a Hermes Agent Kanban board into a lane to other nodes:
a card assigned to `sidera` is handed to an agent on another node, and the answer
comes back to the card. What the lane is, the card format, the endpoint model and
the rules the adapter follows are in
[`docs/references/adapter-hermes/`](../../docs/references/adapter-hermes/README.md).

This directory holds the packages below; the adapter program that wires them is not
part of this directory yet.

## Layout

| Path | Holds |
| ---- | ----- |
| `internal/config/` | The instance configuration and its checks; the alias allow-list. |
| `internal/card/` | The card envelope, strict decoding, validation against the aliases, the assignee rule, the creator check. |
| `internal/kanban/` | The client of the board: runs `hermes kanban --json`, decodes the answers, classifies failures, names the block kinds and checks the Hermes release. Described in [`kanban-cli`](../../docs/references/adapter-hermes/kanban-cli.md). |
| `internal/state/` | The state directory: records of cards, endpoints, remote sessions and processed messages, and the store that writes them. |
| `internal/lane/` | Pure decisions over the records: endpoint keys and capacity, matching a reply to a card, queueing, recovery, failures and the Hermes block kind of each, closing remote sessions, the parent of a continuation. |

## Conventions

- The adapter imports only `sdk/go`, as a third-party adapter would, and never the
  daemon's packages.
- `lane` and `card` do no I/O and read no clock: time is an argument. Only `state`
  touches the disk, and only through `Store`.
- A state transition is written (`Store.Update`) before the effect it describes; the
  effect is the caller's, never a function of these packages.
- A change to a rule or a format updates the reference in the same commit.
