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

### Client requests and answers

`NewClient(ClientConfig{Home, Kind, PathPrefix})` binds a client to one node and
interface. `ipc.Locate` makes the home absolute and clean, and `ipc.Dial` performs
the existing peer checks. `Call` takes a method name, body, Bearer token and
timeout from its facade; it sends a POST with JSON content type to that method.
No proxy or redirect can carry credentials elsewhere. No HTTP idempotency header
is set, and there is no automatic resend. Facades own method selection,
credentials, timeout policy and successful-result decoding.

The shared encoding boundary accepts structs, string-key maps, interfaces,
pointers, slices and JSON scalars, including unsigned numbers. They must form a
finite JSON value. Arbitrary custom JSON and text marshalers and other field
kinds are programming errors; the boundary does not run an extensible marshaler
framework. `json.RawMessage` alone retains its standard encoding support after
`strictjson` validates its document-level UTF-8, surrogate escapes, duplicate keys
and single-value framing. This check applies at any nesting depth. Other string
values and map keys are checked before `encoding/json` can replace invalid UTF-8.
Unsafe text, malformed raw documents and JSON encoding failures return fixed
`invalid_argument/malformed_request` errors without request data. Field presence,
nullability and domain rules remain with the facade.

`EncodeRequest` exposes that same checked encoding as `json.RawMessage`, with
HTML escaping disabled and a final newline. A facade can inspect safe JSON when
choosing method policy, such as a receive deadline, and then supply that raw
document to `Call`. Ordinary objects are encoded only after the shared text
checks; facades never need a second safety walk or unchecked preliminary
encoding. `Call` applies the same document check to a supplied raw message.

A call's context lasts through response-body reading and is bounded by the
supplied timeout and caller context. `httptrace.WroteRequest` records send
progress: a transport failure before writing uses `apiwire.FromError`; after
writing it is `outcome_unknown`. An incomplete response body or malformed error
answer also has an unknown outcome. A complete non-200 response is decoded as
`apiwire.Body`. A 200 response returns the exact bytes for domain validation.
The request-body limit does not limit responses: clients read the complete
answer, including receive pages. Closing a client releases idle connections;
in-flight calls remain owned by their contexts.

## Why

The standard library supplies [HTTP framing and lifecycle](https://pkg.go.dev/net/http#Server),
[bounded bodies](https://pkg.go.dev/net/http#MaxBytesReader) and the
[logger bridge](https://pkg.go.dev/log/slog#NewLogLogger). No additional dependency
is needed. `ServeMux` is not used: its path cleaning and redirects would change
exact local-method routing. An automatic result-handler framework would put body
reads before facade gates or obscure raw receive bytes and stop-after-flush
ordering. Handlers therefore retain control of those steps while sharing the
transport mechanics.

The client uses standard-library [HTTP transport](https://pkg.go.dev/net/http#Transport)
and [send-progress tracing](https://pkg.go.dev/net/http/httptrace#ClientTrace).
POST without an HTTP idempotency header prevents the transport from classifying
an application call as idempotent for automatic retries. The existing
[JSON encoder](https://pkg.go.dev/encoding/json#Marshal) supplies encoding after
text checks; a new dependency would add no required capability.

## Where

- `internal/node/localhttp/localhttp.go`: checked listeners, server lifecycle,
  exact routing, bounded reads and responses.
- `internal/node/localhttp/client.go`: checked client dialing, one request
  encoding boundary, send progress, complete answers and shared error mapping.
- `internal/node/localhttp/client_test.go`: nested request text and raw documents,
  fixed errors, unchanged full answers, redirect refusal and request fuzzing.
- `internal/node/localhttp/localhttp_test.go`: public HTTP request and response
  behavior, encoding failure and internal diagnostics.
- `internal/node/localhttp/lifecycle_test.go`: real IPC listener isolation,
  graceful and expired shutdown, and standard-library diagnostics.
- `internal/node/localhttp/timeouts_test.go`: private transport configuration.
- `internal/node/manageapi`: administrative authorization and execution, including
  fingerprints, stored results and stop after the response is flushed.

- `internal/node/agentapi`: probe-only recovery gate, Bearer syntax, strict method
  decoding and raw receive pages; HTTP transport and response mechanics use the
  shared boundary.
