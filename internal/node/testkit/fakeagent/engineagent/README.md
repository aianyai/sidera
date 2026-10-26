# engineagent

The engine-layer backend of the fake agent: what an agent does, by calling the engine's
service interface in process. Read [`../README.md`](../README.md) first.

`New(svc engine.AgentService, caller engine.Caller)` returns a `fakeagent.Agent`. The
engine resolves the caller in the command that does the work, so an endpoint closed
between two calls is refused and not acted for. The package is on the Sidera side:
it imports the engine, and a scenario never imports it (the fixture hands the agent
over).

## Entry points

| Name | Purpose |
| ---- | ------- |
| `New(svc, caller)` | The agent of one endpoint. |
| `MapError(err)` | An error of the engine as the error of the fake agent: `apiwire.FromError` says its category and code (the one place the words of the wire are written), and the two failures of the fixture's own (`ErrNodeNotRunning`, `ErrProcessEnded`) are mapped here. |
| `NotSupported(method)`, `IsNotSupported(err)` | The error of a method the engine does not offer yet: `not_found/method_unknown` that is also `fakeagent.ErrNotAvailable` (the same value as `scenario.ErrNotAvailable`), and its test. |
| `ErrNodeNotRunning` | What a call to a node that is down carries; `MapError` turns it into `unavailable/daemon_not_running`. |
| `ErrProcessEnded` | What a call carries when the process it was made to ended before it returned; `MapError` turns it into `outcome_unknown/connection_lost`. |

## Rules

- **All eleven methods exist.** `Declare`, `Send`, `Reply`, `Recv`, `Ack`, `Status` and `Control` call the engine (a content type left empty is
  `engine.DefaultContentType`, as the front ends of the interface take it; a control signal with an initial
  message is refused as an invalid argument, since the engine's creation carries none yet). The others (`Me`, `Peers`,
  `Directory`, `Probe`) answer
  `not_found/method_unknown`, which is how the agent interface answers a method the
  server does not know; the error is also `scenario.ErrNotAvailable`, so that a test
  written before the method exists cannot pass on an answer the server never gave. When the engine learns a method it replaces the line here; the
  interface and the scripts do not change. There is no category of its own for "not
  yet": the interface's table has none, and a new category would be a change to it.
- **A page is kept as printed and decoded strictly.** `RecvResult.Raw` is what the engine printed, which is what
  a model sees and what the acknowledgement on its behalf compares; `Page` is the same bytes decoded with unknown
  fields refused and validated by the protocol's own rules, so a page of another shape fails in the agent that read it.
- **A refusal is mapped by `apiwire`, not here.** `TestEveryEngineReasonMapsToACategoryAndCode`
  holds every reason the engine has to come out as `apiwire.FromError` gives it, and to
  have words of its own (never the internal error); the table written out from the
  interface's categories is `apiwire`'s test.
- **A patch field that is nil changes nothing;** `AcceptFrom` replaces the whole list,
  and an address that does not parse is `invalid_argument/malformed_request`.

## Tests

`TestDeclarePatchReachesTheEngineForTheCallersOwnEndpoint`,
`TestDeclareLeavesAFieldThatIsNilAsItWas`,
`TestDeclareRefusalsAreMappedToTheCategoryAndCodeOfTheInterface`,
`TestEveryEngineReasonMapsToACategoryAndCode`, `TestAnErrorThatIsNotARefusalIsInternal`,
`TestACallWhoseProcessEndedIsOutcomeUnknown`, `TestACallToANodeThatIsDownIsUnavailable`,
`TestCallsTheEngineDoesNotOfferYetReturnTheNotSupportedError`,
`TestOtherErrorsAreNotTakenForNotSupported` (a real server's own `not_found/method_unknown` among them); the skipped
`TestShouldAMethodTheEngineDoesNotOfferYetHaveItsOwnCategory` keeps the question about
a dedicated category in sight.
