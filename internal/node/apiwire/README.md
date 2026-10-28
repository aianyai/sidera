# apiwire

The words of the wire: how what the engine and the packages below it say becomes
the category and code of an error on the Agent and management interfaces, the body
that carries them, the status of the HTTP answer and the exit code of the command
line. Read
[../../../docs/references/daemon/README.md](../../../docs/references/daemon/README.md)
for the reasons.

This is a runtime package of layer 6 of [`internal/node/README.md`](../README.md):
it imports `engine`, `config`, `ipc`, `admin` and `strictjson` to read their errors,
and `adapterhost`, `adapterproto` and `peer` to put the engine behind the adapter
host and word its refusals as the adapter protocol does. It does no I/O. `engine` does
not import it: the words of the wire never enter the engine.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `FromError(err)` | The one mapping: an error of the wire as it is, an `*engine.Refusal` by its reason, a `*config.ChangeError` by its reason, a `*strictjson.Error`, a failure of the way to the daemon (`ipc`), and any other error as an internal one. |
| `AdapterEngine(engine)` | The engine behind the interface the adapter host asks of it (`adapterhost.Engine`): the four methods that answer a request of an adapter (`RegisterDirect`, `UnregisterDirect`, `CheckpointPut` and `RecvObserved`, the one optional part it offers) turn an `*engine.Refusal` into the `*peer.Error` the adapter is answered with, by one row per reason; every other error is passed on untouched. What each code means is in [docs/references/adapter-host](../../../docs/references/adapter-host/README.md). |
| `FromAdmin(reason)` | The refusal of a request by the administrative rules. |
| `Error`, `Body`, `New`, `Malformed` | An error as the wire carries it, `{"error":{"category","code","message","details?"}}`. |
| `EndpointUnknown()`, `OperationUnknown()` | The answers an interface gives without an error of the engine behind them: an address that is no endpoint of this node, and an operation identifier with no stored result. |
| `OutcomeUnknown(operationID)` | What a client says when it wrote a request and did not read the answer whole: category and code `outcome_unknown`, the identifier in the details. The one definition the command line, the MCP front end and the test fixture share. |
| `Category`, `Categories`, `HTTPStatus`, `ExitCode`, `ExitUsage`, `ExitOutcomeUnknown` | The nine categories, the status of the answer for each, and the exit code of the command line for each, with the one exit code no category gives (2, a command used wrongly). |

## Rules

- **One mapping.** A reason of the engine, a refused change and a failure of the way
  to the daemon each have one pair of words, written here and nowhere else. A test
  walks every reason the engine and `config` have, so one added without a row fails.
- **The adapter's words are chosen here too, and only for what answers a request of
  the adapter.** `AdapterEngine` maps a refusal by a table with one row for each reason
  of the engine; a test fails when a reason has none. The cause a refusal carries is
  never sent. An error that is no refusal is returned as it is: the host answers it
  `-32603` and keeps its text off the wire. The value it returns has the host's methods
  and `ObservedCalls`, which it maps, and nothing else of the engine, on purpose: the host
  finds the optional parts of the engine (`endpoint.send` and its kin) by asking the value,
  and a method of the engine that was promoted would be answered in the engine's words.
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
protocol's own rules, effect rule and list of reasons); `adapter_test.go`: the three requests that answer a refusal through a real engine, a
stopping engine, the absence of the optional parts the wrapper does not map
(`EndpointCalls`), and `recv.observed`, the optional part it offers and maps
(`ObservedCalls`). `apiwire_test.go`: every reason of the engine and of `config` against a table
written out by hand, the listed codes of `invalid_argument`, the outcome that is not
known (from a fatal failure of the disk, and judged by a client), the answers an
interface gives on its own, the details a refusal carries, the faults
in a request and the failures of the way to the daemon, the text of an unknown error
kept off the wire, the golden body of an error, and the status and exit code of each
category.
