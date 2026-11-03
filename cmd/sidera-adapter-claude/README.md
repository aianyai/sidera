# sidera-adapter-claude

The adapter that lets the daemon create and drive Claude Code sessions. It is a
separate executable that talks to the daemon over the adapter protocol; the
daemon never imports it.

## Layout

Two kinds of package live here, and the difference is whether they touch the
world.

**Pure packages** decide and describe; they read no file, start no process, run no
goroutine and never look at the clock. They follow the purity conventions of
[`internal/README.md`](../../internal/README.md) and import the standard library,
`golang.org/x/text` (Unicode normalisation of paths), other pure packages of the
adapter, the protocol types of `sdk/go/adapterproto` and `sdk/go/adapterkit/environ`
(the environment of a child process; pure itself).

| Path | Holds |
| ---- | ----- |
| `internal/create/` | The decision logic for creating one session: what to start, wait for, record or report next, given the persisted attempt and what was observed. |
| `internal/claudeargs/` | What a turn process is started with: the argument vector, the environment, the user message it reads, the MCP configuration, the preset's values and the adapter's system prompt. |
| `internal/record/` | How Claude Code keeps a session on disk: the record directory of a working directory, finding a prompt in a session file and the ends of turns in it, and how a turn process ended from its output stream. |
| `internal/attached/` | How a session a person runs shows itself: its registry entry, the wake that opens its inbox socket and the line written there, and the follower that turns the registry's status and the session record into the notifications of the protocol. |
| `internal/reconcile/` | The answer to `prompt.reconcile`, from the facts the runtime gathered. |
| `internal/turn/` | The state machine of the events a turn process gives rise to: when `turn.started` and `turn.ended` are sent, for a process just started and for one found again after a restart. |

**The runtime** under `internal/runtime/` is where the adapter meets the world: it
may read and write files, start and watch processes, run goroutines and read the
clock. Everything it does to the world goes through a small interface, so that
its flow can be tested with fakes. Runtime packages depend on the standard
library, `sdk/go`, `internal/release` (for the version alone), `golang.org/x/sys` and
`golang.org/x/text`, and nothing else.

| Path | Holds |
| ---- | ----- |
| `internal/runtime/` | The adapter itself, an `adapterkit.Application`: creation, delivery, reconciliation, closing, `adapter.start`, `adapter.probe`, the turn monitor, the checkpoints and attached sessions. See its README. |
| `internal/runtime/proc/` | The operating-system layer: the process table, starting a process detached from the adapter, and interrupting one; the adapter's own interface over `sdk/go/adapterkit/process`, which implements it on macOS, Linux and Windows. |

`main.go` first lets the SDK's interrupt helper (Windows) take over when the executable was started as one, then serves the adapter protocol over the standard input and output with
`sdk/go/adapterkit`, and prints its version for the subcommand `version`.
`sidera-adapter.json` is the package manifest, built into the
executable so that the identity answered in `initialize` is read from it; its version is
the placeholder `@version@`, replaced by the release version when the manifest is read. The
executable is started by the daemon and ends when the daemon closes its input; the
turn processes it started are detached and run on.
`pack.go` is the `package` subcommand: it writes the package for one platform the
manifest lists from a binary built for it.

## Why this code is not under the top-level `internal/`

The top-level `internal/` holds contracts every component applies identically,
and the core must not contain any product name. Go's `internal` rule makes a
package under `cmd/sidera-adapter-claude/internal/` importable only from within
`cmd/sidera-adapter-claude/`, so the compiler, not a review, keeps Claude
specifics out of the core. The adapter is a client of the SDK, as a third-party
adapter would be. The reasons behind the rules are in [`docs/references/adapter-claude/`](../../docs/references/adapter-claude/README.md).
