# apiwire

The words of the wire: how what the engine and the packages below it say becomes
the category and code of an error on the Agent and management interfaces, the body
that carries them, the status of the HTTP answer and the exit code of the command
line. Read
[../../../docs/references/daemon/README.md](../../../docs/references/daemon/README.md)
for the reasons.

This is a runtime package of layer 6 of [`internal/node/README.md`](../README.md):
it imports `engine`, `config`, `ipc`, `daemonerr`, `admin` and `strictjson` to read their errors,
and `adapterhost`, `adapterproto` and `peer` to put the engine behind the adapter
host and word its refusals as the adapter protocol does. It does no I/O. `engine` does
not import it: the words of the wire never enter the engine.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `FromError(err)` | The one mapping: an error of the wire as it is, a fatal daemon failure, an `*engine.Refusal` by its reason, a `*config.ChangeError` by its reason, a `*strictjson.Error`, a typed daemon or IPC failure, an ended request context, and any other error as an internal one. |
| `BudgetUnitName(unit)` | A calculated output-budget unit as its existing wire name, `chars` or `tokens`; an unknown unit is an error. |
| `AdapterEngine(engine)` | The engine behind the interface the adapter host asks of it (`adapterhost.Engine`): the adapter request methods (`RegisterDirect`, `UnregisterDirect`, `CheckpointPut`, `RecvObserved` and the four `EndpointCalls`) turn an `*engine.Refusal` into the `*peer.Error` the adapter is answered with, by one row per reason; every other error is passed on untouched. What each code means is in [docs/references/adapter-host](../../../docs/references/adapter-host/README.md). |
| `FromAdmin(reason)` | The refusal of a request by the administrative rules. |
| `Error`, `Body`, `New`, `Malformed` | An error as the wire carries it, `{"error":{"category","code","message","details?"}}`. |
| `EndpointUnknown()`, `OperationUnknown()` | The answers an interface gives without an error of the engine behind them: an address that is no endpoint of this node, and an operation identifier with no stored result. |
| `OutcomeUnknown(operationID)` | What a client says when it wrote a request and did not read the answer whole: category and code `outcome_unknown`, the identifier in the details. The one definition the command line, the MCP front end and the test fixture share. |
| `Category`, `Categories`, `HTTPStatus`, `ExitCode`, `ExitUsage`, `ExitOutcomeUnknown` | The nine categories, the status of the answer for each, and the exit code of the command line for each, with the one exit code no category gives (2, a command used wrongly). |

## Rules

- **Output units have one wire spelling.** The Agent facade and the process-local
  test backend use `BudgetUnitName`, which takes its names from the SDK manifest
  constants. Budget calculation values are formatted explicitly at a presentation
  boundary; they have no implicit formatter.
- **One mapping.** A reason of the engine, a refused change and a failure of the way
  to the daemon each have one pair of words, written here and nowhere else. A test
  walks every reason the engine and `config` have, so one added without a row fails.
- **The adapter's words are chosen here too, and only for what answers a request of
  the adapter.** `AdapterEngine` maps a refusal by a table with one row for each reason
  of the engine; a test fails when a reason has none. The cause a refusal carries is
  never sent. An error that is no refusal is returned as it is: the host answers it
  `-32603` and keeps its text off the wire. The value it returns has the host's methods
  and both optional interfaces, `ObservedCalls` and `EndpointCalls`, which it maps,
  and nothing else of the engine, on purpose: the host finds the optional parts
  of the engine by asking the value,
  and a method of the engine that was promoted would be answered in the engine's words.
- **Direct sends have a method-specific intake mapping.** Invalid recipient address
  syntax is `1008` with `data.field`; invalid content, a missing reply source or
  an idempotency conflict is `1009` with the engine reason in `data.reason`.
  Back pressure remains `1012`, ownership `1001`, and leases `1010`.
  `endpoint.message_status` and `endpoint.recv` of a `message_id` answer an unknown
  or foreign message with the same fixed `-32602` response (`messageLookupError`),
  revealing no ownership. Unknown effects remain
  `-32603`. Other adapter methods retain their existing mappings.
- **Refusals with no effect are `node_busy` on the adapter side.** The six that mean
  "not now, nothing was recorded" (node queue full, storage full, storage unavailable,
  clock rollback, loop busy, recovering) are `1012` with data `{"reason":"<name>"}`, the
  engine's own name for the reason; the protocol's design note says why the code exists.
  A fatal failure of the disk stays `-32603`: whether the request took effect is not
  known.
- **An unknown attempt is not an unknown endpoint.** An attempt checkpoint the
  instance does not own is `1004 unknown_attempt`; session checkpoints keep
  `1001 endpoint_unknown`. No Agent method takes an attempt, so that reason has
  only the generic internal-error fallback on the HTTP interfaces.
- **A directory that cannot answer in time is unavailable.** A directory timeout
  and an unreachable target map to `unavailable/directory_unavailable`. This
  stable code means try again later; it is never renamed, removed or reused for
  another meaning. It is not an uncertain message outcome, nor an inbox timeout.
- **Daemon lifecycle errors keep actionable, bounded messages.** `daemonerr`
  exposes only the daemon's existing declarations, avoiding an import of the
  composition root. The shared mapping covers those errors and IPC path-length
  and missing-lock failures; the [daemon reference](../../../docs/references/daemon/README.md#errors-and-command-output)
  lists their stable codes. Except for the extracted IPC path-length and peer-check
  messages, wire explanations are fixed: wrappers and causes stay off the wire.
  A `FatalError` is `unavailable/daemon_storage_fatal`, after an existing wire
  error but before an engine refusal it wraps. It means restart the daemon for
  recovery; the standalone engine refusal stays `outcome_unknown/storage_fatal`.
  Init and foreground daemon retain their lifecycle exit codes (0, 1 or 74).
- **An ended request context is unavailable.** After typed wire, daemon, engine, config,
  strict-JSON and IPC errors, `context.Canceled` maps to
  `unavailable/request_canceled`, and `context.DeadlineExceeded` to
  `unavailable/request_timeout`, including wrapped errors. Their messages are
  fixed and describe only the ended context. They make no claim about effects.
  A client that already wrote a request still reports `outcome_unknown` before
  applying this mapping when its answer is incomplete.
- **The text of an unknown error stays off the wire.** Anything that is none of the
  known errors is `internal/internal` with a fixed message; what it said is for the
  log. A client that is not the author of the code should not be handed a path or a
  name that an error happened to carry.
- **Details only where they are needed to act:** the current version of a conflict,
  the path of an invalid value, the limit that was hit, the field of an invalid
  argument or of a body that is too large. A message holds no message body, no token and no key.
- **Every category has its own exit code**, 0 is never one of them, and a category
  that is none of the known ones is an internal error, never a success.
- **A failure of the disk after which it is not known whether a request took effect
  is `outcome_unknown`** (exit code 10, code `storage_fatal`), not `internal`: the
  engine defines the reason as "whether it took effect is not known", and a client
  has to be told to send the request again with the same operation identifier,
  exactly as when an answer is lost. The category is also the one a client judges
  itself (`OutcomeUnknown`), so both come to the same place.
- **A resend is suggested only with an operation identifier.** `OutcomeUnknown`
  without one reports the uncertainty alone: an Agent send without an explicit
  idempotency key may create another message if it is sent again.
- **A message identifier taken before with other content is `conflict/message_conflict`.**
  It is what a target says to the node that sent it, and the interface specification
  lists no code of the category for it; it is worded as the engine names it until the
  specification does.
- **A refusal for an invalid argument that carries no detail is
  `invalid_argument/malformed_request`**, with the field in the details: the codes of
  a category are the ones the interface specification lists for it, and this is the
  listed one that fits. A test writes the listed codes out and checks every mapping
  of the category against them.
- **A daemon that is not running is not a failed check.** `daemon_not_running` and
  `peer_check_failed` are both `unavailable` and never match each other; the second
  may mean that someone holds the name.

## What the caller must guarantee

- Map once, at the edge: an interface turns the error of a call into an `Error`
  with `FromError`, writes the status of its category and the body, and does not
  look at the reason itself.
- Do not put a body, a token or a key into the message of an `Error` it makes with
  `New` or `Malformed`.

## Where the tests are

`adapter_internal_test.go`: the table of codes for the requests of an adapter (every
reason of the engine against rows written out by hand, the limit and the field, the
cause kept off the wire, errors that are no refusal, and `node_busy` with the
protocol's own rules, effect rule and list of reasons); `adapter_test.go`: the
requests that answer a refusal through a real engine, a stopping engine, both
explicitly mapped optional interfaces (`EndpointCalls` and `ObservedCalls`), and
`recv.observed` refusals. `direct_test.go`: complete direct send, control and reply
fields, replay and conflict, endpoint ownership, reply routing, lease and part
refusals, and nondisclosing status errors through the real host, with persisted
messages and acknowledgements checked after refusals. `apiwire_test.go`: every
reason of the engine and of `config` against a table
written out by hand, the listed codes of `invalid_argument`, the outcome that is not
known (from a fatal failure of the disk, and judged by a client), the answers an
interface gives on its own, the details a refusal carries, the faults
in a request, daemon and IPC failures with actionable messages and private wrapper
text kept off the wire, ended contexts and typed-error
precedence, the text of an unknown error kept off the wire, the golden body of an error, and the status and exit code of each
category.
