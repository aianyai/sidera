# scenario

Test scripts, the same in every layer. Read [`../README.md`](../README.md) first for
where the package sits.

A script is Go code, not a text language: the reader reads what runs and the
compiler checks the names. It imports no engine, no fixture and no backend of the fake
agent (`TestScenarioPackageDoesNotImportTheEngineOrAFixtureOfAnyLayer` enforces it),
because the engine-layer fixture implements `Env` and so will the binary-layer one.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Script`, `Setup`, `Step`, `CrashPlan` | A scenario: its direct endpoints, its steps, the judgements it is held to, and the crash plan. |
| `Run(env, script)` | The driver: validates, sets up, runs each step, and after each waits for quiet; then judges the ledger. |
| `Script.Scenario()` | The script as a `crashwalk.Scenario`: name, step names, budget, recovery cases. A method of its own because `Name` and `Steps` are the script's fields. |
| `Env` | What a step may use: the context, the agents and addresses of endpoints, the clock, `Quiesce`, the step announcement, the adapter's and the runtime's controllers, the ledger. |
| `Advance(name, d)`, `RecvAndAckAll(name)` | A step that moves the clock, and the body of a step that reads an endpoint until it is empty and acknowledges every page. |
| `ErrNotAvailable`, `NotAvailable(what)` | What an `Env` method returns for a capability its layer does not have; the same value as `fakeagent.ErrNotAvailable`, which a backend's error carries for a method its layer lacks. |
| `QuiesceOption`, `WithTimeout`, `ResolveQuiesce` | The options of `Env.Quiesce`, read by every fixture the same way. |
| `Judgement`, `Judgements`, `Invariants`, `Check` | The rules a finished run is held to, readable from the ledger alone. |

## Rules

- **The driver quiesces after every step and the step never sleeps.** A step does one
  thing from outside: a call, an advance of the clock or a knob. A step does not start
  goroutines or read the real clock.
- **Step names are unique,** because an entry of the ledger names its step and
  nothing else. `Validate` refuses a script with a missing, repeated or empty-bodied
  step before the system is touched.
- **Session endpoints come only from a session creation.** `Env` has no method that
  registers one, and `Setup` lists direct endpoints only.
- **A layer that lacks something says so.** `Adapter` and `Runtime` return an error
  that is `ErrNotAvailable` where the layer has nothing to return; a script is never
  run against a stand-in.
- **A script is held to every judgement** unless it waives one with a reason; an
  unknown judgement or an empty reason is refused. `Check` names every judgement that
  fails, not the first.
- **Judgements are read from the ledger,** never from the daemon's state: the effect
  of a confirmation is on record before it is confirmed; a creation attempt made at
  most one native session; a prompt attempt reached a runtime at most once; a
  message part the model saw again is marked possibly duplicate on every sighting after
  the first (an unmarked sighting that the ledger proves came after another one fails,
  by the order inside one writer and between steps; two unmarked sightings fail
  whatever their order; where nothing proves an order, one unmarked sighting may be
  the first). The rules whose subject does not exist yet (no message lost, one
  feedback per delivery and kind, acknowledgements on behalf matching what the model
  saw, every record closed, the drain) are added with the code that makes that
  subject.

## What the caller must guarantee

- An `Env` implementation returns from `Quiesce` only when its system has stopped
  reacting at the time of its clock, and from `BeginStep` after the step is announced.

## Tests

`TestRunSetsUpDirectEndpointsThenRunsEachStepAndQuiescesAfterIt`,
`TestRunStopsAtTheFirstFailedStepAndNamesIt`,
`TestRunReportsAFailureToGoQuietAndNamesTheStep`,
`TestRunRefusesAScriptThatCannotRunBeforeTouchingTheSystem`,
`TestRunStopsWhenADirectEndpointCannotBeSetUp`,
`TestAdvanceIsOneStepThatMovesTheClockByExactlyTheDuration`,
`TestRecvAndAckAllAcknowledgesEveryPageAndStopsAtAnEmptyOne`,
`TestRecvAndAckAllReturnsTheErrorOfTheCallThatFailed`,
`TestAScriptIsAScenarioOfTheCrashWalk`, `TestNotAvailableNamesWhatIsMissingAndIsTheSentinel`,
`TestQuiesceOptionsDefaultToTheLimitOfSixtySeconds`,
`TestCheckHoldsEveryRuleOfAnEmptyLedgerAndOfACorrectOne`, `TestCheckRejects` (one row
per judgement, and more for the mark),
`TestACheckOfRepeatSightingsFailsOnlyWhatTheLedgerProvesIsAfterAnotherSighting`, `TestCheckIsTheSameForEveryOrderOfTheJudgementsAndNamesEveryOneThatFails`,
`TestJudgementsListsEachOnceAndEachCanBeWaived`, `TestRunJudgesTheLedgerAfterTheLastStep`.
