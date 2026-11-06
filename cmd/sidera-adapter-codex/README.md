# sidera-adapter-codex

The adapter that lets the daemon create and drive Codex sessions, and reach the ones a person opened and attached (attached sessions: it reads their turns and queues prompts through the Codex home it shares with the person's Codex). It is a
separate executable that talks to the daemon over the adapter protocol on its
standard input and output (standard error is its log) and to Codex through a
private Codex App Server; the daemon never imports it. The reasons behind it
are in [`docs/references/adapter-codex/`](../../docs/references/adapter-codex/README.md).

## Layout

| Path | Holds |
| ---- | ----- |
| `main.go` | The command line, an `adapterkit.Program`: the subcommand `serve` (the one the manifest's entry names) serves the protocol through `sdk/go/adapterkit`, `package` writes the adapter package for one platform, `version [--json]` prints the version, and `--help`, no argument and an unknown command print the usage (`usageText`): nothing serves unless `serve` is named. |
| `internal/appserver/` | The App Server client: WebSocket over a Unix socket or TCP, lenient JSON-RPC, the hand-written subset of its types and methods, the read loop, and the queue that hands notifications on in order. `appservertest/` is a fake server for tests. |
| `internal/server/` | The App Server's life: connect to the one that is up or start one detached (through `sdk/go/adapterkit/process`), the empty directory of its own it runs in (never the state directory, which a sandbox would then be given), the environment it is given, the state file, the version of the program, waiting for a recorded server that does not answer instead of starting a second, ending only a server whose pid and start key in the state file name the running process, and deleting the working directory once the server of a retired instance has ended. Its `realcodex_*_test.go` files are the opt-in tests against the installed Codex (`-tags realcodex`). |
| `internal/adapter/` | The protocol methods, the session table, the events, the reconnect loop, the availability snapshot, the probes, and the one place threads are started and resumed (`thread.go`), which checks that a sandboxed thread took its permission profile. `interrupt.go` stops a turn and ends the commands it leaves running before the turn's end is reported. `replace.go` replaces the App Server after Codex is upgraded, when no session is running, behind a gate that every operation which needs the server holds. `app.go` also decides at `shutdown`, by its intent, whether the server is kept (restart), ended when idle (stop) or ended and its working directory deleted (retire). `attached.go` holds attached sessions, which are never resumed: the watch that reads a person's thread, and queued prompts. |
| `internal/codexcfg/` | Pure. Instance and preset configuration, the thread-level configuration that carries a session's credentials and, for a sandboxed preset, the permission profile that denies the data directory, the developer instructions every thread is given (the statement every adapter gives, from the SDK's `guidance`, with one sentence of Codex's own, then the preset's own text), the sandbox policy of a preset without a sandbox, the front ends of a preset. |
| `internal/create/` | Pure. The next step of creating one session, and the checkpoint format. |
| `internal/reconcile/` | Pure. The verdict of `prompt.reconcile` from the thread's record and, for an attached session, its queue. |
| `internal/serverreq/` | Pure. The answer to each request of the server, from the preset. |
| `internal/declare/` | Identity, capabilities and the package manifest, from one set of constants, and writing a package. |

## Why this code is not under the top-level `internal/`

The top-level `internal/` holds contracts every component applies identically, and
the core must not contain any product name. Go's `internal` rule makes a package
under `cmd/sidera-adapter-codex/internal/` importable only from within
`cmd/sidera-adapter-codex/`, so the compiler, not a review, keeps Codex specifics
out of the core. The pure packages (`codexcfg`, `create`, `reconcile`, `serverreq`)
follow the purity conventions of [`internal/README.md`](../../internal/README.md):
no I/O, no goroutines, no clock reads, no global state, the standard library and
the protocol types of `sdk/go` only (the adapter is a client of the SDK, as a
third-party adapter would be). The packages that do I/O are `appserver`,
`server`, `adapter` and `declare`'s package writer.

## Building and packaging

```
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/sidera-adapter-codex ./cmd/sidera-adapter-codex
/tmp/sidera-adapter-codex package --goos linux --goarch amd64 --binary /tmp/sidera-adapter-codex --out /tmp/codex-package
```

The package holds the manifest `sidera-adapter.json` and `bin/sidera-adapter-codex`
(`.exe` for Windows); install it with `sidera adapter install`. The built-for
platforms are `darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64`,
`windows/amd64` and `windows/arm64`. The
manifest is generated from the same constants `initialize` answers with, so they
cannot disagree. Its version is the release version (`internal/release`).

## Tests

`go test ./cmd/sidera-adapter-codex/...` runs table tests of the pure packages, the
client and the adapter against the fake App Server, with no Codex installed. The
tests against the installed Codex start a real App Server and run no model turn, except
`TestRealCodexInterruptEndsTheRunningCommand` in `internal/adapter`, which spends quota:

```
SIDERA_REALCODEX=1 go test -tags realcodex -count=1 ./cmd/sidera-adapter-codex/...
```

On Unix the temporary directory must be short enough for a socket path (about a
hundred bytes); `TMPDIR` can name a shorter one.
