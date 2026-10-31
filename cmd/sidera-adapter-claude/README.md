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
adapter and the protocol types of `sdk/go/adapterproto`.

| Path | Holds |
| ---- | ----- |
| `internal/create/` | The decision logic for creating one session: what to start, wait for, record or report next, given the persisted attempt and what was observed. |
| `internal/claudeargs/` | What a turn process is started with: the argument vector, the environment, the user message it reads, the MCP configuration, the preset's values and the adapter's system prompt. |
| `internal/record/` | How Claude Code keeps a session on disk: the record directory of a working directory, finding a prompt in a session file, and how a turn ended from its output stream. |
| `internal/reconcile/` | The answer to `prompt.reconcile`, from the facts the runtime gathered. |
| `internal/turn/` | The state machine of the events a turn process gives rise to: when `turn.started` and `turn.ended` are sent, for a process just started and for one found again after a restart. |

**The runtime** under `internal/runtime/` is where the adapter meets the world: it
may read and write files, start and watch processes, run goroutines and read the
clock. Everything it does to the world goes through a small interface, so that
its flow can be tested with fakes. Runtime packages depend on the standard
library, `sdk/go`, `golang.org/x/sys` and `golang.org/x/text`, and nothing else.

| Path | Holds |
| ---- | ----- |
| `internal/runtime/proc/` | The operating-system layer: the process table, starting a process detached from the adapter, and signals. Implemented for macOS; the other platforms compile and fail every call with `ErrUnsupported`. |

## Why this code is not under the top-level `internal/`

The top-level `internal/` holds contracts every component applies identically,
and the core must not contain any product name. Go's `internal` rule makes a
package under `cmd/sidera-adapter-claude/internal/` importable only from within
`cmd/sidera-adapter-claude/`, so the compiler, not a review, keeps Claude
specifics out of the core. The adapter is a client of the SDK, as a third-party
adapter would be. The reasons behind the rules are in [`docs/references/adapter-claude/`](../../docs/references/adapter-claude/README.md).
