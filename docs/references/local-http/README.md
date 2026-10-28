# Local HTTP

The transport boundary shared by the node's local HTTP facades. The implementation
is [`internal/node/localhttp`](../../../internal/node/localhttp/README.md).
[Local IPC](../local-ipc/README.md) owns socket and named-pipe peer checks. The
[management interface](../daemon/README.md#the-management-interface) owns local-owner
authority and administrative operations; the [Agent interface](../agent-interface/README.md)
owns its recovery gate, Bearer credentials and endpoint methods.

## What

A server is bound to `ipc.KindManage` or `ipc.KindAgent`, an exact path prefix and
a method lookup. It cuts the prefix and looks up the remaining method name before
requiring POST. An unknown path or method is `404 not_found/method_unknown`, even
with a different verb. A known method with another verb is
`405 invalid_argument/method_not_allowed` with `Allow: POST`. Paths are neither
cleaned nor redirected.

Routing never reads a body. Each facade calls `ReadBody` after its authorization,
recovery and credential gates. The helper limits a body to 1 MiB and maps excess
to `invalid_argument/request_too_large`; another read failure is
`invalid_argument/malformed_request`. Strict decoding stays with the facade.

The server reads headers within 10 seconds and requests within 30 seconds, keeps
idle connections for 60 seconds and limits headers to 8 KiB. There is no global
write timeout because a method may wait. Request contexts follow `net/http` and
end when the client disconnects.

JSON responses are fully encoded before headers are written, with HTML escaping
disabled and the encoder's final newline. `RawJSON` sends exactly the supplied
bytes. Both set `Content-Type: application/json` and the exact `Content-Length` so
a client can finish reading before a handler returns or starts a daemon stop.
An encoding failure writes a fixed `500 internal/internal` body directly. It
never encodes the error again: even an error whose details cannot be encoded
must have a bounded, complete fallback.

`Fail` uses `apiwire.FromError` once. An internal cause is logged whole and its
public answer has the fixed internal-error message. `http.Server.ErrorLog` uses
`slog.NewLogLogger` at error level with the configured logger and an `interface`
attribute. A nil logger discards diagnostics. Normal request handling does not
proactively record request objects, credentials or bodies. Error producers and
handlers must keep those inputs out of diagnostic text and panic values, since
internal causes and standard-library diagnostics are not redacted.

Only the shared `Listen` can construct its listener, checking that the address
matches the requested kind before calling `ipc.Listen`. `Serve` also checks the
kind. A rejected listener remains the caller's; an accepted listener belongs to
the server and is closed on return. Facades retain distinct typed wrappers,
including their existing public `Listen` and `Serve` signatures. The shared layer
cannot accept an arbitrary `net.Listener`.

`Shutdown` stops accepting connections and waits within its context. On error it
closes remaining connections and returns the original shutdown error. `Serve`
returns nil after shutdown or immediate `Close`. The owner ends domain waiters
first and joins the serving goroutine.

## Why

The standard library supplies [HTTP framing and lifecycle](https://pkg.go.dev/net/http#Server),
[bounded bodies](https://pkg.go.dev/net/http#MaxBytesReader) and the
[logger bridge](https://pkg.go.dev/log/slog#NewLogLogger). No additional dependency
is needed. `ServeMux` is not used: its path cleaning and redirects would change
exact local-method routing. An automatic result-handler framework would put body
reads before facade gates or obscure raw receive bytes and stop-after-flush
ordering. Handlers therefore retain control of those steps while sharing the
transport mechanics.

## Where

- `internal/node/localhttp/localhttp.go`: checked listeners, server lifecycle,
  exact routing, bounded reads and responses.
- `internal/node/localhttp/localhttp_test.go`: public HTTP request and response
  behavior, encoding failure and internal diagnostics.
- `internal/node/localhttp/lifecycle_test.go`: real IPC listener isolation,
  graceful and expired shutdown, and standard-library diagnostics.
- `internal/node/localhttp/timeouts_test.go`: private transport configuration.
- `internal/node/manageapi`: administrative authorization and execution, including
  fingerprints, stored results and stop after the response is flushed.
