# sidera-adapter-codex

The adapter that lets the daemon create and drive Codex sessions. It is a
separate executable that talks to the daemon over the adapter protocol and to
Codex through a private Codex App Server; the daemon never imports it. The
reasons behind it are in
[`docs/references/adapter-codex/`](../../docs/references/adapter-codex/README.md).

This directory holds the App Server's client and its life so far; the protocol
mapping and the executable arrive with the next step.

## Layout

| Path | Holds |
| ---- | ----- |
| `internal/appserver/` | The App Server client: WebSocket over a Unix socket or TCP, lenient JSON-RPC, the hand-written subset of its types and methods, the read loop, and the queue that hands notifications on in order. `appservertest/` is a fake server for tests. |
| `internal/server/` | The App Server's life: connect to the one that is up or start one detached, the environment it is given, the state file, the version of the program, and ending only a server this process started. Its `realcodex_test.go` is the opt-in test against the installed Codex (`-tags realcodex`). |

## Why this code is not under the top-level `internal/`

The top-level `internal/` holds contracts every component applies identically, and
the core must not contain any product name. Go's `internal` rule makes a package
under `cmd/sidera-adapter-codex/internal/` importable only from within
`cmd/sidera-adapter-codex/`, so the compiler, not a review, keeps Codex specifics
out of the core.

## Tests

`go test ./cmd/sidera-adapter-codex/...` runs the client against the fake App Server
and the table tests, with no Codex installed. The test against the installed Codex
starts a real App Server and runs no model turn:

```
SIDERA_REALCODEX=1 go test -tags realcodex -count=1 ./cmd/sidera-adapter-codex/...
```

On Unix the temporary directory must be short enough for a socket path (about a
hundred bytes); `TMPDIR` can name a shorter one.
