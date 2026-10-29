# manageapi

The management interface of the daemon: HTTP/1.1 over the local socket (a named
pipe on Windows) that the owner's commands talk to. Read
[../../../docs/references/daemon/README.md](../../../docs/references/daemon/README.md)
for the wire, the reasons, and how the interface relates to the engine and to the
administrative rules.

This is a runtime package of layer 8 of [`internal/node/README.md`](../README.md):
it imports `engine` (through the service interface), `ipc`, `apiwire`, `localhttp` and the
contract packages. It starts no goroutine of its own: `net/http` runs one for each
connection, and `Shutdown` ends them. The shared [local HTTP boundary](../localhttp/README.md)
owns transport limits, exact method routing, bounded body reads, responses and
diagnostic logging; this package owns the point at which authorization permits
reading a body.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Listen(addr, lock, opts)`, `Listener` | Listens on the address of the management interface and on no other: it refuses the address of the Agent interface. Only it makes a `Listener`. |
| `New(Config)`, `Server.Serve`, `Shutdown`, `Close` | The server over a `Listener`. `Shutdown` waits for the requests being served and then closes what is left. |
| `NewClient(ClientConfig)`, `Client.Call`, `Client.Close` | Call one node as its local owner; return the complete management response without retrying. |
| `RevocationExportResult` | The shared online and offline export result: the armored statement and its backup digest. |
| `Request` | The caller's operation identifier, optional expected version and params; zero identifier and nil members are omitted. |
| `Config` | The engine's `ManageService`, this node's identifier, the clock, the stop entry of the composition root, the version of the program for the status, and the log. |

The methods are a table in `methods.go`, one row each: the name on the wire, the
catalogue entry of the operation, and how the params are read. The owner of an
operation adds a row (and its type in the engine); the handling of a request does
not change. `operation.result` is one of the rows: a read of the result stored for an
operation identifier of the requester.

The rows are in `methods.go` (`endpoints.list`, `endpoints.close`,
`daemon.stop`, `operation.result`), `methods_status.go` (`status`, `alerts.list`,
`alerts.ack`), `methods_identity.go` (`identity.export_revocation`,
`identity.confirm_backup`, `identity.retire`, `clock.clear_rollback`) and
`methods_settings.go` (`node.show`, `node.set`, `endpoints.set`) and
`methods_adapters.go` (`adapters.install|list|enable|disable`,
`presets.list|add|remove`). The words of each
result, and of the reminders, are the interface's: the engine says which reminder
stands and what an operation did, and a function the row passes to the operation
words it. A patch of settings is passed to the engine as it was written, in a
canonical form (members in order, no spaces, numbers as written) that only the
fingerprint is made of; the names, the values and the rules of the settings are
`config`'s, and this package does not know one.

A row may instead be a **test row** (`method.test`): the door of the test harness to
the node it runs in. Test rows are named `test.<something>`, come only from
`testMethods`, which has rows in a binary built with the `testclock` or `crashtest`
tag (`testmethods.go`) and none otherwise (`testmethods_off.go`), so a shipped binary
answers `method_unknown` for them. A test row is outside the catalogue of
administrative operations and is not judged by `admin.Authorize`: it is no operation
of the owner's, has no catalogue entry (the shipped catalogue must have none),
carries no operation identifier and stores no result. The rows of the harness (the
test clock, quiescence, the crash points) are added by the streams that own them.

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
- **A row is authorized as what it runs as.** Authorization uses the row's `op`
  before the params are read, and what runs is the plan's operation, which the
  engine judges by its own `Admin()`. After the params are read the server checks
  that the two are the same catalogue entry (a change plans an operation and no read,
  a read plans a read and no operation); a row that breaks it is answered as an
  internal error and the engine is never asked, so a request can never be
  authorized as one entry and carried out as another.
- **Strict decoding.** An unknown member, a repeated key, `null`, a key of another
  case and a value of another type are `malformed_request`, with the path of the
  fault when the decoder can place it. The operation identifier is 32 lower-case
  hexadecimal digits that are not all zero; one that is absent is refused by the
  administrative rules. An expected version where the operation carries none is
  `unexpected_version` whatever its value, zero included: the member's presence is
  what is refused, since a sender that wrote it believes a check is made. Where the
  operation carries one, zero is the same as absent (`missing_expected_version`).
- **One fingerprint per meaning.** `SHA-256("sidera-manage-v1" NUL method NUL
  canonical params NUL decimal expected version)`; the canonical params are the
  decoded params encoded again with the fields of their type in declared order. The
  rule is here and nowhere else. A default the interface applies (the instance of an
  enabling is named after the adapter) is applied before the params are encoded, so
  that a request that leaves it out and one that writes it are one request.
- **The answer after the work.** A request that changes state is answered with the
  identifier, whether it was a resend, and the result the engine stored; the stop
  that an operation asks for is requested after the answer was flushed, once.
- **Errors are mapped once** by `apiwire.FromError`; the text of an unexpected error
  is logged whole and never sent.
- **No write timeout.** A request is the client's connection: it ends when the
  client goes away, and a context that ends cancels the call to the engine.

## Client

`ClientConfig.Home` selects the management address through the shared
`localhttp.Client`; no Bearer credential is used. The shared layer owns checked
IPC, safe JSON encoding, POST, send progress, complete reads and transport errors.
This package owns the management request and response envelopes. It converts a
nonzero `admin.OperationID` to its 32 lower-case hexadecimal digits, preserves an
explicit expected version of zero, and leaves validation of methods and params to
the server's existing table. `Params` accepts the JSON values described by
`localhttp.Request.Body`; it is not encoded separately or inspected by another
reflection boundary.

`Call` has a 60-second timeout, extended to five minutes for `adapters.install`,
and the caller's context may end it earlier. Successful responses are strictly
checked as read or change envelopes; a change must answer with the same operation
identifier and a `replayed` boolean. The complete `json.RawMessage`, including
those fields and its original whitespace, is returned for the caller's output.
Method-specific results remain JSON for the caller to interpret.

A damaged or incomplete response is `outcome_unknown`. An unknown outcome carries
the request's operation identifier in `details.operation_id`, whether detected
locally or reported by the server; a server reason such as `storage_fatal` is
retained. The client never resends. A caller that resends retains one identifier,
expected version and exact request: creating new values could execute another
operation or ask for a different change. Closing the client releases idle
connections; contexts own in-flight calls.

## What the caller must guarantee

- Hold `daemon.lock` before `Listen`, as `ipc.Listen` asks, and create `manage/`
  with `init`.
- Give `Config.Stop` an entry that returns at once and runs the shutdown once.
- `Serve` may run while the node starts: a request that changes state is then
  refused by the engine as `recovering`, and a read is answered.
- A sandbox must deny the management socket to an agent: the local owner is the
  user's uid alone (see the local IPC reference).

## Where the tests are

`manageapi_test.go`: malformed requests with
their paths, the operation identifier, the expected version (present or not), the
catalogue, `operation.result`, test rows and the rule for a row's name, what
the engine is asked (key, fingerprint worked out by hand, name, kind), the
fingerprint of equal and different requests, the shape of the answers, the stop
after the answer, the client that goes away, the listener
that takes only the management address, and concurrent requests. Shared HTTP
routing, size, response, logging and shutdown tests live in `localhttp`. `testmethods_test.go` and
`testmethods_off_test.go` (build-constrained) say that a tagged build has its test
rows and an untagged one has none. `methods_owner_test.go` the rows of the owner (the
operation each is authorized as, the strict reading of their params, the commands and
the fingerprint of a patch, the stop after a retirement, and the golden answers of
`status`, `alerts.list`, `node.show` and the export); `fuzz_test.go` fuzzes the one
entry that reads a request, for every method the table has: it never panics, and
what it accepts is canonical. `methods_adapters_test.go` the rows for adapters and
presets: the operation each is authorized as (a preset is added as loosening and
removed as narrowing), the strict reading of their params (a package named by a
path that is not absolute, a member that is empty or null), what the engine is asked
(an enabling that leaves out the instance is the request that names it after the
adapter), and the golden answers of the two lists.

`client_test.go` checks request envelopes, complete unchanged responses, read and
change metadata, explicit version presence, unknown outcomes with their operation
identifiers, malformed and incomplete answers, and no automatic resend over
checked IPC. Its response fuzz target exercises strict envelope decoding.
