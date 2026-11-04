# sidera-adapter-hermes

A Sidera adapter that turns a Hermes Agent Kanban board into a lane to other nodes:
a card assigned to `sidera` is handed to an agent on another node, and the answer
comes back to the card. What the lane is, the card format, the endpoint model and
the rules the adapter follows are in
[`docs/references/adapter-hermes/`](../../docs/references/adapter-hermes/README.md).

`main.go` is the program: `serve` is the adapter the daemon starts, `package` writes
an installable package, `setup` writes the Skill for the agent and prints (or, on request,
runs) the steps that let Hermes use it, and `version` prints the build. Everything else is in the
packages below. The program receives messages, polls the board and runs timers, and
hands them to three handler interfaces (`adapter.MessageHandler`,
`adapter.CardHandler`, `adapter.TimerHandler`). The lane's flows (`adapter.LaneFlows`)
implement them: they delegate cards, write replies back and close remote sessions.
What other nodes start (a message to the instance endpoint or to a conversation, a
reply card) is the `adapter.Inbound` seam, implemented by `adapter.NewInbound`: a
message becomes a card assigned to `sidera-inbox`, completed at once to wake the
conversation it belongs to, and a reply card is sent back to the sender of the request.

## Layout

| Path | Holds |
| ---- | ----- |
| `internal/adapter/` | The adapter program: the `Application`, the direct endpoints, the receive loop, the board poll loop, the timer loop, the probe, the manifest and the package. The handler interfaces and the lane flows that implement them. |
| `internal/config/` | The instance configuration and its checks; the alias allow-list. |
| `internal/card/` | The card envelope, strict decoding, validation against the aliases, the assignee rule, the creator check. |
| `internal/kanban/` | The client of the board: runs `hermes kanban --json`, decodes the answers, classifies failures, names the block kinds and checks the Hermes release. Described in [`kanban-cli`](../../docs/references/adapter-hermes/kanban-cli.md). |
| `internal/setup/` | `setup`: the Skill generated from the alias table and the steps that prepare Hermes (enable the kanban tools, place the Skill). |
| `internal/state/` | The state directory: records of cards, endpoints, remote sessions and processed messages, and the store that writes them. |
| `internal/lane/` | Pure decisions over the records: endpoint keys and capacity, matching a reply to a card, queueing, recovery, failures and the Hermes block kind of each, closing remote sessions, the parent of a continuation. |

## Conventions

- The adapter imports only `sdk/go`, as a third-party adapter would, and never the
  daemon's packages. The two exceptions are `internal/release` (the version) and
  `internal/datahome` (`setup` finds the data directory as the `sidera` command does,
  so that the two never name different ones).
- `lane` and `card` do no I/O and read no clock: time is an argument. Only `state`
  touches the disk, and only through `Store`.
- A state transition is written (`Store.Update`) before the effect it describes; the
  effect is the caller's, never a function of these packages.
- A change to a rule or a format updates the reference in the same commit.
