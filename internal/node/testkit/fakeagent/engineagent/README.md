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
| `MapError(err)` | An engine refusal as the error of the fake agent: its category and code. Anything that is not a refusal is internal. |
| `NotSupported(method)`, `IsNotSupported(err)` | The error of a method the engine does not offer yet: `not_found/method_unknown` that is also `fakeagent.ErrNotAvailable` (the same value as `scenario.ErrNotAvailable`), and its test. |
| `ErrNodeNotRunning` | What a call to a node that is down carries; `MapError` turns it into `unavailable/daemon_not_running`. |
| `ErrProcessEnded` | What a call carries when the process it was made to ended before it returned; `MapError` turns it into `outcome_unknown/connection_lost`. |

## Rules

- **All eleven methods exist.** `Declare` calls the engine. The others (`Me`, `Peers`,
  `Directory`, `Probe`, `Send`, `Reply`, `Recv`, `Ack`, `Control`, `Status`) answer
  `not_found/method_unknown`, which is how the agent interface answers a method the
  server does not know; the error is also `scenario.ErrNotAvailable`, so that a test
  written before the method exists cannot pass on an answer the server never gave. When the engine learns a method it replaces the line here; the
  interface and the scripts do not change. There is no category of its own for "not
  yet": the interface's table has none, and a new category would be a change to it.
- **A refusal is mapped by its reason, one table.** The expectations of
  `TestEveryEngineReasonMapsToACategoryAndCode` are written out from the interface's
  table of categories and fail when a reason is added without its mapping. The codes
  the table does not list (`endpoint_limit`, `storage_unavailable`, `storage_fatal`,
  `instance_unknown`) are the reasons' own names, in the category the reason's
  behaviour gives: back pressure for a write that failed before its commit point,
  an unknown outcome for the fatal one, which is a command that may or may not have
  taken effect.
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
