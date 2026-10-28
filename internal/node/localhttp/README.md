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

## Tests

`localhttp_test.go` tests routing and POST priority, bounded reads and read
failures, complete unescaped JSON, exact raw bytes, encoding failure (including
unencodable error details), and internal causes confined to the log.
`lifecycle_test.go` uses checked IPC sockets or pipes to test listener isolation,
shutdown with an active request, expiry and cancellation, the end of `Serve`, and
net/http diagnostics reaching the configured logger. `timeouts_test.go` keeps the
transport configuration check beside its private owner.
