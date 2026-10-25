# manageapi

The management interface of the daemon: HTTP/1.1 over the local socket (a named
pipe on Windows) that the owner's commands talk to. Read
[../../../docs/references/daemon/README.md](../../../docs/references/daemon/README.md)
for the wire, the reasons, and how the interface relates to the engine and to the
administrative rules.

This is a runtime package of layer 6 of [`internal/node/README.md`](../README.md):
it imports `engine` (through the service interface), `ipc`, `apiwire` and the
contract packages. It starts no goroutine of its own: `net/http` runs one for each
connection, and `Shutdown` ends them.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Listen(addr, lock, opts)`, `Listener` | Listens on the address of the management interface and on no other: it refuses the address of the Agent interface. Only it makes a `Listener`. |
| `New(Config)`, `Server.Serve`, `Shutdown`, `Close` | The server over a `Listener`. `Shutdown` waits for the requests being served and then closes what is left. |
| `Config` | The engine's `ManageService`, this node's identifier, the clock, the stop entry of the composition root and the log. |

The methods are a table in `methods.go`, one row each: the name on the wire, the
catalogue entry of the operation, and how the params are read. The owner of an
operation adds a row (and its type in the engine); the handling of a request does
not change.

## Rules

- **Local owner by the listener, never by the request.** `Server.actor` is the one
  place `admin.LocalOwner()` is made, and `Serve` takes only a `Listener`, which
  only `Listen` makes and `Listen` makes only for the address of the management
  interface. A connection of the Agent interface cannot reach this server, and
  nothing in a request says who sent it. A scan in `internal/node` keeps the actor
  from being made anywhere else.
- **The order of a request.** Method (404), verb (405), `admin.Authorize` against
  the catalogue, the body (at most 1 MiB), the strict decode of the envelope and
  the params, `admin.ValidateRequest`, then the engine: a read is answered, a
  request that changes state is one `Execute` command with the key
  `admin.KeyFor(owner, id)`, the fingerprint and the expected version. The replay
  check, the version check and the storing of the result are the engine's and are
  not made here.
- **Strict decoding.** An unknown member, a repeated key, `null`, a key of another
  case and a value of another type are `malformed_request`, with the path of the
  fault when the decoder can place it. The operation identifier is 32 lower-case
  hexadecimal digits that are not all zero; one that is absent is refused by the
  administrative rules, and an expected version where the operation carries none
  is `unexpected_version`.
- **One fingerprint per meaning.** `SHA-256("sidera-manage-v1" NUL method NUL
  canonical params NUL decimal expected version)`; the canonical params are the
  decoded params encoded again with the fields of their type in declared order. The
  rule is here and nowhere else.
- **The answer after the work.** A request that changes state is answered with the
  identifier, whether it was a resend, and the result the engine stored; the stop
  that an operation asks for is requested after the answer was flushed, once.
- **Errors are mapped once** by `apiwire.FromError`; the text of an unexpected error
  is logged whole and never sent.
- **No write timeout.** A request is the client's connection: it ends when the
  client goes away, and a context that ends cancels the call to the engine.

## What the caller must guarantee

- Hold `daemon.lock` before `Listen`, as `ipc.Listen` asks, and create `manage/`
  with `init`.
- Give `Config.Stop` an entry that returns at once and runs the shutdown once.
- `Serve` may run while the node starts: a request that changes state is then
  refused by the engine as `recovering`, and a read is answered.
- A sandbox must deny the management socket to an agent: the local owner is the
  user's uid alone (see the local IPC reference).

## Where the tests are

`manageapi_test.go`: routing and verbs, the size limit, malformed requests with
their paths, the operation identifier, the expected version, the catalogue, what
the engine is asked (key, fingerprint worked out by hand, name, kind), the
fingerprint of equal and different requests, the shape of the answers, the stop
after the answer, the mapping of refusals, the client that goes away, the listener
that takes only the management address, shutdown with a request in flight, the
timeouts, and concurrent requests. `fuzz_test.go` fuzzes the one entry that reads
a request: it never panics, and what it accepts is canonical.
