# localhttp

The shared HTTP boundary of the node's local interfaces. Read
[the reference](../../../docs/references/local-http/README.md) for behavior and
trade-offs, and [the parent](../README.md) for runtime rules.

This layer 7 package imports `apiwire` and `ipc`. It owns transport and wire
mechanics; the management and Agent facades own authorization, recovery gates,
credentials, strict domain decoding and execution.

## Entry points

| Entry | Purpose |
| --- | --- |
| `NewClient(ClientConfig)`, `Client.Call`, `Client.Close` | Post JSON to one checked IPC interface and read its complete response without retrying. |
| `EncodeRequest` | Check and encode a request before a facade inspects its JSON for method policy. |
| `New(Config)` | Bind a server to an IPC kind, exact path prefix, method lookup and diagnostic logger. |
| `Listen(kind, address, lock, options)`, `Listener.Close` | Open a matching IPC address under the instance lock; keep the underlying listener private. |
| `Server.Serve`, `Shutdown`, `Close` | Own a matching listener, wait within the shutdown context, and close remaining connections on expiry. |
| `Server.ServeHTTP` | Find the exact method, then require POST before calling its handler. |
| `ReadBody` | Read at most 1 MiB when the domain handler permits it. |
| `Server.JSON`, `RawJSON`, `Fail` | Publish complete JSON, unchanged JSON bytes, or an error mapped by `apiwire`. |

## Invariants and caller guarantees

- A listener's kind must match both its address and its server. A refused listener
  stays with its caller; after acceptance the server owns and closes it. The
  facades keep distinct listener types so management authority cannot cross into
  the Agent interface. No arbitrary `net.Listener` can be supplied.
- Lookup is an exact method-name lookup and returns nil for an unknown method.
  No path cleanup, redirection or early body read occurs in routing.
- Call `ReadBody` only after the facade's authorization, recovery and credential
  gates. The helper owns the limit and read-error mapping, not that ordering.
- Encode before publishing headers. HTML escaping is disabled; JSON encoding
  adds one newline. Raw JSON keeps exactly the caller's bytes. Encoding failure
  writes the fixed internal answer directly, without recursively encoding errors.
- A nil log discards diagnostics. Normal request handling does not proactively
  log request objects, credentials or bodies. Internal causes and net/http's own
  diagnostics are logged whole: error producers and handlers must not put those
  inputs into diagnostic text or panic values. This is not a redaction layer.
- The owner ends domain waiters before waiting for HTTP shutdown and joins the
  goroutine running `Serve`.

## Client boundary

The client resolves an absolute, clean home through `ipc.Locate` and opens only
that address through `ipc.Dial`. It uses no proxy, follows no redirect and sends
only POST requests without HTTP idempotency headers. Facades choose the method,
Bearer credential and timeout for each request; the shared client never chooses
authority or domain policy.

`Request.Body` accepts structs, string-key maps, interfaces, pointers, slices and
JSON scalars, including unsigned numbers. Containers must form a finite JSON
value; arbitrary JSON or text marshalers and other field kinds are unsupported
programming errors. `json.RawMessage` is the exception: document-level
`strictjson` checks reject invalid UTF-8, unpaired surrogate escapes, duplicate
keys and trailing values before encoding or connecting. Ordinary request text,
including map keys, is checked before the standard encoder can replace invalid
bytes. Unsafe documents and encoding failures return fixed
`invalid_argument/malformed_request` errors without including request data.
The client leaves field presence and domain validation to the facade.

`EncodeRequest` is the same safe encoding boundary used by `Client.Call`. It
returns `json.RawMessage`, with HTML escaping disabled and a final newline. A
facade that needs JSON fields for its timeout policy can encode once, inspect
those bytes and pass the raw document to `Call`; it must not encode unchecked
objects first or repeat the text-safety rules itself.

Calls use the supplied timeout, also bounded by the caller's context. A failure
before writing is mapped through `apiwire`; a written request without a complete
answer has `outcome_unknown`. Complete non-200 answers decode as `apiwire.Body`;
malformed error answers have unknown outcomes. Success returns the original
response bytes for the facade to validate, without a response size limit or
re-encoding. `Close` releases idle connections; contexts own in-flight calls.

## Tests

`localhttp_test.go` tests routing and POST priority, bounded reads and read
failures, complete unescaped JSON, exact raw bytes, encoding failure (including
unencodable error details), and internal causes confined to the log.
`lifecycle_test.go` uses checked IPC sockets or pipes to test listener isolation,
shutdown with an active request, expiry and cancellation, the end of `Serve`, and
net/http diagnostics reaching the configured logger. `timeouts_test.go` keeps the
transport configuration check beside its private owner.

`client_test.go` checks safe request containers and raw documents, fixed encoding
errors, unchanged complete responses larger than the request limit, and redirect
refusal. Its fuzz target covers raw documents and nested request text. Agent
client tests retain the real-service checks for credentials, no automatic resend,
cancellation, incomplete answers and unchanged receive pages.
