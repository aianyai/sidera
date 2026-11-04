# adapterkit

The serve loop that every adapter has the same way, so that adapters do not each
copy it. An adapter writes an `Application`, the kit does the rest of the
protocol's lifecycle. Read
[the adapter-protocol reference](../../../docs/references/adapter-protocol/README.md)
for the rules; the kit adds none, it only applies them once.

This package imports the standard library, `peer` and `adapterproto`, and nothing
else, so the instance lock is `flock` on Unix and a file opened without sharing on
Windows, through `syscall`. What an adapter needs beyond the serve loop lives in
subpackages that an adapter imports only when it needs them; `process` and
`realpath` alone bring in `golang.org/x/sys`, so an adapter that imports only this
package does not build it:

| Package | Holds |
| ------- | ----- |
| [`environ`](environ/README.md) | Composes the environment of a child process |
| [`process`](process/README.md) | Starting a process detached from the adapter, its identity, listing, interrupting |
| [`realpath`](realpath/README.md) | The absolute path of an existing path with its symbolic links resolved (and, on Windows, the platform's case and long form) |

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Serve(ctx, r, w, Config)`, `ServeStdio(ctx, Config)` | Serve the protocol over a pair of streams, or over the standard input and output, until the daemon closes the stream (`nil`), `ctx` ends (its error) or the connection fails. The instance lock is released when it returns. |
| `Config` | The identity and capabilities answered in `initialize`, the `Application`, and the limits and logger of the peer. They are checked before anything is served: an identity or a set of capabilities that would make `initialize` answer something invalid is refused. |
| `Application` | What the adapter implements: `Initialize`, `Shutdown`, `Start`, `Probe`, the five runtime methods, `AttachSession`, `SessionDetached` and `EndpointPending`. |
| `Base` | Embed it to implement only the methods the adapter has: an empty start and probe, and `capability_not_declared` for the runtime methods and `session.attach`, with `session.detached` ignored. |
| `Conn` | What the adapter sends: `Call` and `Notify` for any method it may send, and typed helpers for each (`CheckpointPut`, `Register`, `Recv`, `Ack`, `Send`, `MessageStatus`, `RecvObserved`, `Availability`, `PromptPersisted`, `TurnStarted`, `TurnEnded`, `NativeRequestHandled`). |

## Rules

- **The lock comes before the answer to `initialize`.** After the version is
  negotiated the kit takes the lock on `instance.lock_path`; when another process
  holds it the answer is `1011` and the adapter stays uninitialized. The
  application's `Initialize` runs after the lock and an error from it releases the
  lock. The lock dies with the process.
- **The state of the connection is the kit's.** Before `initialize` only
  `initialize`, `ping` and `$/cancel_request` are served and every other request,
  a method that does not exist included, is `-32003`; after `shutdown` began only
  `ping` is served and the rest is `-32002`, `initialize` too. The state is judged
  before the parameters are read. A second `initialize` is `-32600`. A version range
  with nothing in common is `-32022` with both ranges, and takes no lock.
- **Requests are decoded strictly and answered validated.** Parameters go through
  `adapterproto.Decode`: a failure is `-32602` and the application is not called.
  A result that fails `Validate` is answered as an internal error and logged, never
  sent. What the adapter sends through `Conn` is validated the same way, and a result
  is decoded strictly before it is returned.
- **A runtime method needs the runtime declared.** `session.create`,
  `prompt.deliver`, `prompt.reconcile`, `session.interrupt` and `session.close` are
  answered `1003` without calling the application when `Config.Capabilities` has no
  runtime.
- **The methods of attached sessions need `attach` declared.** `session.attach` is
  answered `1003` without calling the application, `session.detached` is dropped, and
  an `adapter.start` that lists an attached session is answered `1003`, unless the
  runtime in `Config.Capabilities` declares `attach`. Nothing of attached sessions
  reaches an application that did not declare it.
- **`Start` and `Probe` have a deadline.** Their context ends 2 seconds short of
  `adapterproto.HandshakeLimit` after the request arrived, because the daemon ends the
  connection of an adapter that has not answered by then. An application that has more
  to do than fits answers by the deadline and does the rest after the answer.
- **The kit declares `shutdown_intent`.** `Serve` adds the capability to
  `Config.Capabilities`, reads the `intent` of `shutdown` and hands it to
  `Application.Shutdown`; a `shutdown` without one reaches the application as
  `restart`, so `p.Intent` is never empty. An unknown intent is `-32602` and the
  application is not told. The error `Shutdown` returns is the answer to the request
  (build it with `peer.NewError` to choose the message the daemon logs); the adapter
  is shutting down either way.
- **Errors are the application's to choose.** A `*peer.Error` is answered as it is;
  any other error is `-32603` with a fixed message, as the peer does for every
  handler.

## What the caller must guarantee

- `Config.Adapter` is what the adapter's manifest says.
- The application does not block `Shutdown`: it stops its work and returns, and the
  process ends when the daemon closes the stream.
- The application keeps no state in the kit's connection: it is told the
  instance in `Initialize`, and `instance.state_dir` is where it keeps its own.
