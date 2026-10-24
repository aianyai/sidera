# sidera-adapter-claude

The adapter that lets the daemon create and drive Claude Code sessions. It is a
separate executable that talks to the daemon over the adapter protocol; the
daemon never imports it.

## Layout

| Path | Holds |
| ---- | ----- |
| `internal/create/` | The decision logic for creating one session: what to start, wait for, record or report next, given the persisted attempt and what was observed. Pure, no I/O. |

There is no `main` yet; the executable and the code that observes processes and
files and starts Claude arrive with the runtime.

## Why this code is not under the top-level `internal/`

The top-level `internal/` holds contracts every component applies identically,
and the core must not contain any product name. Go's `internal` rule makes a
package under `cmd/sidera-adapter-claude/internal/` importable only from within
`cmd/sidera-adapter-claude/`, so the compiler, not a review, keeps Claude
specifics out of the core. Packages here still follow the purity conventions of
[`internal/README.md`](../../internal/README.md): no I/O, no goroutines, no
clock reads, no global state, standard library and the protocol types of
`sdk/go/adapterproto` only (the adapter is a client of the SDK, as a third-party
adapter would be). The reasons behind the
rules are in [`docs/references/adapter-claude/`](../../docs/references/adapter-claude/README.md).
