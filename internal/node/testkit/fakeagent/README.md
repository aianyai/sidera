# fakeagent

What a test does as the agent of an endpoint, with the same types in every layer.
Read [`../README.md`](../README.md) first for where this package sits.

The package holds the interface, its inputs, results and errors, and one wrapper.
It holds no backend: the engine layer's backend calls the engine's service
interface and lives with the engine fixture; the binary layer's backends run the
real command-line client and speak MCP to the real server and live with the
binary-layer fixture. Because a result and an error are the types here, one
scenario script runs in both layers, and the layers can be compared.

It imports the standard library, the SDK module (`adapterproto`, whose value types
are the one home of delivery states, rejection reasons, lease and part references)
and, for `Recording`, the ledger and the fake runtime of the testkit.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Agent` | The eleven methods of the agent interface: `Me`, `Declare`, `Peers`, `Directory`, `Send`, `Reply`, `Recv`, `Ack`, `Status`, `Control`, `Probe`. |
| `SendInput`, `ReplyInput`, `RecvInput`, `AckInput`, `ControlInput`, `DeclareInput`, `StatusInput`, `DirectoryInput`, `ProbeInput` | The inputs. A `DeclareInput` is a patch: a nil field does not change. |
| `ReplayState`, `ReplayStateFrom` | Send idempotency evidence: `ReplayUnknown`, `ReplayFresh` or `ReplayRepeated`; conversion of an actual response flag. |
| `SendResult`, `RecvResult`, `AckResult`, `StatusResult`, `MeResult`, `PeersResult`, `DirectoryResult`, `ProbeResult` | The results. `RecvResult.Raw` is the page exactly as the backend printed it; `Page` is the same page decoded. |
| `Error`, `Category`, `CategoryOf`, `CodeOf`, `Categories` | The failure of a call: a category (invalid argument, unauthenticated, not found, conflict, refused, backpressure, unavailable, internal, and the client's own "outcome unknown"), a code, a message. |
| `ErrNotAvailable` | The cause an error carries when the layer a call is made in lacks the capability (a backend's unimplemented method). `scenario.ErrNotAvailable` is the same value, so a test written before the capability exists asserts it is gone. |
| `Recording(inner, writer, endpoint, options)` | Wraps any backend so that every call writes its ledger entries. |

## Rules

- **Results and errors are layer-neutral.** A backend that runs a command line
  maps the exit code and the JSON output back to these types; a backend that calls
  the engine maps the engine's typed reasons. The mapping is the backend's, and a
  difference between two backends for one scenario is a finding.
- **Replay evidence is explicit.** A backend without a replay flag returns
  `ReplayUnknown`. A known false response flag means `ReplayFresh`, and true
  means `ReplayRepeated`. A zero result never claims a fresh send.
- **`Status` takes a message id.** The status of a message cannot be asked
  without naming it.
- **A page is printed exactly as the backend returned it.** The daemon's check
  before an acknowledgement compares the text the model saw with the page it
  handed out, so nothing here rewrites a page.
- **Recording writes the call before the action and the result after it.** A
  kill in the middle of a call still leaves the attempt on the ledger. A test
  judges that a message was sent from the result entry, never from the daemon's own
  state. Bodies never reach the ledger; a failure to write the ledger panics,
  because an oracle with a hole is worse than a failed test.
- **model.saw is written by one party.** The runtime writes it for the pages it
  hands the model of a turn; `RecordingOptions.ModelSaw` is for an agent that is
  not a runtime's model. The entries are made by `fakeruntime.ModelSaw` either way,
  so the oracle reads the same thing however a page arrived.

## What the caller must guarantee

- Give `Recording` the ledger writer named `agent/<endpoint>` and the same
  endpoint, on the shared test clock.
- Do not turn `ModelSaw` on for an agent a runtime drives.
- Give each call a context with a limit: a `recv` that waits is a call that can
  take as long as its wait.

## Tests

`TestErrorCategories`, `TestErrorAccessors`, `TestTheInterfaceHasAMethodForEveryAgentCommand`,
`TestRecordingWritesTheCallBeforeTheActionAndTheResultAfter`,
`TestRecordingRecordsEveryMethodAndItsFailure`,
`TestRecordingCanRecordWhatTheModelSawOutsideATurn`,
`TestRecordingFeedbackClassifierMarksSystemNotices`,
`TestRecordingKeepsTheRawPageUnchanged`.
