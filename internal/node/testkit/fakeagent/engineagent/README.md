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
| `NotSupported(method)`, `IsNotSupported(err)` | The error of a capability this caller-bound backend cannot offer: `not_found/method_unknown` that is also `fakeagent.ErrNotAvailable` (the same value as `scenario.ErrNotAvailable`), and its test. |
| `ErrNodeNotRunning` | What a call to a node that is down carries; `MapError` turns it into `unavailable/daemon_not_running`. |
| `ErrProcessEnded` | What a call carries when the process it was made to ended before it returned; `MapError` turns it into `outcome_unknown/connection_lost`. |

## Rules

- **Ten methods call the engine.** `Me`, `Declare`, `Peers`, `Directory`, `Send`,
  `Reply`, `Recv`, `Ack`, `Status` and `Control` use its service interface. An empty
  content type means `engine.DefaultContentType`, and a control body is the initial
  message of a session creation. `Me` presents the declaration and frontend limits;
  `Peers` marks the caller's own node; `Directory` preserves continuation cursors.
- **Probe needs a credential this backend does not hold.** `New` binds an opaque
  `engine.Caller`, not a raw probe token. `Probe` therefore retains
  `not_found/method_unknown` with `fakeagent.ErrNotAvailable`; the real local HTTP
  interface supports probes with their separate credentials. No endpoint caller is
  treated as a probe token, and this test-backend limitation does not describe the
  engine's capabilities.
- **A page is kept as printed and decoded strictly.** `RecvResult.Raw` is what the engine printed, which is what
  a model sees and what the acknowledgement on its behalf compares; `Page` is the same bytes decoded with unknown
  fields refused and validated by the protocol's own rules, so a page of another shape fails in the agent that read it.
- **A refusal is mapped by `apiwire`, not here.** `TestEveryEngineReasonMapsToACategoryAndCode`
  holds every reason the engine has to come out as `apiwire.FromError` gives it;
  the table written out from the interface's categories, including the internal
  fallback for reasons only an adapter request can reach, is `apiwire`'s test.
- **A patch field that is nil changes nothing;** `AcceptFrom` replaces the whole list,
  and an address that does not parse is `invalid_argument/malformed_request`.

## Tests

`TestDeclarePatchReachesTheEngineForTheCallersOwnEndpoint`,
`TestDeclareLeavesAFieldThatIsNilAsItWas`,
`TestDeclareRefusalsAreMappedToTheCategoryAndCodeOfTheInterface`,
`TestEveryEngineReasonMapsToACategoryAndCode`, `TestAnErrorThatIsNotARefusalIsInternal`,
`TestACallWhoseProcessEndedIsOutcomeUnknown`, `TestACallToANodeThatIsDownIsUnavailable`,
`TestMePresentsTheCallersDeclarationAndFrontendValues`,
`TestPeersReturnsTheVisibleNodeInTheSharedResultShape`,
`TestDirectoryPresentsEndpointValuesAndPassesTheContinuation`,
`TestProbeNeedsCredentialsThisCallerBoundBackendDoesNotHold`,
`TestOtherErrorsAreNotTakenForNotSupported` (a real server's own `not_found/method_unknown` among them).
