# Test tooling

The packages under `internal/node/testkit/` and the two programs `cmd/sidera-fake-adapter`
and `cmd/sidera-fake-runtime`. They let a test run the daemon against an adapter and a
native runtime that it did not write, judge it from a record the daemon did not make, and
walk its crash points. Each package has a README with its entry points and rules; this
document gives the reasons and says where the tests are.

## What

| Part | Is |
| ---- | -- |
| `ledger` | An append-only record of what the fakes did: canonical JSON lines, a closed set of kinds and attributes, one file per writer and process with every entry synced, or an in-process backend. Bodies are never in it, only SHA-256 digests. |
| `fakectl` | The control channel between the test fixture and the fake processes it starts: loopback TCP, newline-delimited JSON-RPC on the SDK's peer. It carries the test clock, the name of the running script step, the knobs, and reports back. |
| `fakeruntime` | A stand-in for a native agent runtime: sessions, a session record, turns that start and end only when a script says so, in process or as a program of its own per session. |
| `fakeadapter` | An adapter that speaks the adapter protocol as a third party would, drives a fake runtime, and does what its knobs say; in process over `io.Pipe` or as a program. |
| `fakeagent` | The interface of the fake agent (send, reply, receive, acknowledge, control, declare, status), its results and errors, and a wrapper that writes the ledger entries around every call. |
| `fakeagent/engineagent` | The engine-layer backend of the fake agent: the engine's service interface called in process. The methods the engine does not offer yet answer as an unknown method. |
| `scenario` | The language of test scripts, the same in every layer: scripts, the driver, the environment interface a fixture implements, and the judgements that read the ledger. |
| `harness` | The engine-layer fixture: the engine on the real store, configuration and transaction layer over the in-memory file system, one test clock, the ledger, the agents, quiet, process and power-loss crashes and restarts, fault injection. |
| `crashwalk` | The core of the crash-point walk: which points to kill at in each mode, how a rerun is held to its recording, scenario budgets, coverage of the declared writes, and the crashing of recovery itself. |

The first four, with the two programs, are the **third-party side**: they import the
standard library, the SDK module and each other and nothing else. `fakeagent`,
`engineagent`, `scenario`, `harness` and `crashwalk` are the **Sidera side** and may
import any runtime or contract package; `scenario` alone imports neither the engine
nor a fixture.

The backends of the fake agent that run the command-line client and speak to the MCP
server, the binary-layer fixture and the backends of the crash walk build on these
packages and are not part of them.

## Why

- **The fakes are third parties.** The point of testing against a fake adapter is to
  prove that the protocol, as published, is enough to write an adapter. A fake that
  imported an internal package could agree with the daemon on something the published
  protocol does not say, and every test built on it would pass for the wrong reason. The
  import rule is therefore a test (`go list -deps` for three operating systems and both
  tag sets), not a convention. The ledger and the control channel are on this side too:
  a fake that had to import a testkit package that may depend on the runtime would break
  the proof, and the ledger must not share the file layer it is the oracle of.
- **The ledger is the oracle, and it is written before the effect is confirmed.**
  Integration tests that read the daemon's state to learn what happened would agree with
  the daemon's bugs. The fakes record every effect (a native session, a delivered prompt,
  what the model was shown) in a record of their own, and write the entry before they
  tell the other side, so an effect that took place is on record even if the process dies
  in the next instant. Every entry of the file backend is synced because the writers are
  killed on purpose. The writers are separate processes and a test clock stands still, so
  "written first" cannot be read from the order of lines; the adapter's confirmation of a
  native session therefore names the runtime's own entry by writer and sequence number,
  which the runtime's answer carried back, and the ledger can check that the entry exists
  (`Log.CheckConfirmations`). Step names are unique for the same reason: an entry names
  its step and nothing else.
- **Entries are canonical lines.** A line that decodes re-encodes to the same bytes, so
  equal entries are equal text and two readers cannot read one line two ways. The same
  is true of the other formats that cross between processes: the session record, the
  control messages, the knob arguments, the process configuration. Each has golden lines
  written by hand and a fuzz target; nothing in a decoder is tested only against the
  encoder that goes with it.
- **One clock, set by the fixture.** The times an adapter and a runtime report enter the
  daemon's decisions (since when something is blocked). If the fakes read their own
  clocks, a test that moves the daemon's clock by half an hour would see the fakes report
  real time and its outcome would depend on how the two clocks happen to relate. So the
  fakes take a `func() time.Time` (they cannot import the clock package) and in the
  binary layer are set over the control channel, daemon first, fakes after.
- **A turn never ends because time passed.** Turns whose length depends on the machine
  make every test that waits on one flaky. Turns start and end when the script says so;
  what the model does inside is a list of actions run by whoever supplies the runner.
  The quiet conditions that the fakes are responsible for (no unfinished action, no
  unexpected request in flight, all notifications handled, held requests expected) are
  queries, because a sleep is both slow and wrong.
- **A session starts working on its first prompt.** A real runtime does, and the
  protocol counts that prompt as an attempt, so the fake runtime starts a turn for it
  when the adapter that made the session takes charge of it (its first watcher), not
  after the script says so: tests then see what the daemon will see, including a
  `turn.started` that can reach it before the answer to `session.create`. The plan of
  that turn is set before creation, per runtime (a default plan), because the session
  and its attempt do not exist yet for a test to name; the zero plan starts
  automatically and a manual plan leaves the start to the script.
- **Turns have exactly one start and one end, even when the process dies.** A killed
  native process cannot write the end of its turn into its record, but the adapter
  that is online sees it die and ends the turn as failed; a record whose writer
  stopped with a turn not ended reads as failed. A run that left a turn open would
  make every test of a crash during a turn depend on a case the protocol forbids.
- **A runtime's events are requests, and quiet includes them.** The runtime process
  tells its adapter of a turn's start and end and of what the model read with
  requests the adapter answers after it has passed the event on, so a fixture that
  waits for the runtime to be idle cannot advance the clock in the gap between the
  runtime having finished and the daemon having been told. A notification would leave
  that gap, and a ping does not close it.
- **A launcher checks whom it reached.** A runtime process is found again by a file
  and an address; a released port can be taken by the next process that asks for one,
  so after connecting the launcher asks the process for its reference, process id and
  start key and takes it only if they are the file's.
- **One interface over the knobs of both layers.** The knobs of an adapter in the
  test process and of the adapter program are used through one interface
  (`fakeadapter.Controller`), as the turns of both runtimes are through
  `fakeruntime.Controller`, so a scenario is written once. `Held` reports methods and
  never pause points, because the daemon's in-flight requests are compared with it by
  method.
- **Configuration reaches an adapter program by its arguments first.** Several fake
  adapters under one daemon share the environment the daemon gives them but not the
  arguments of their manifests, so the file named in the arguments wins and the
  environment variable is the fallback. A configuration field that was left out must
  not turn into a process nobody can end (the control address is required) or into
  1970 (the clock start must be positive).
- **A runtime process outlives its adapter.** A native session does not die when its
  adapter does; what the daemon must survive is an adapter that restarts and finds the
  session still there, or gone. So each session is a detached process, found again by a
  file it writes atomically, and a process that is gone is still a session whose record
  can be read.
- **Loopback TCP for the control channel,** not a Unix socket or a named pipe: it is the
  same on all three platforms, has no path-length limit, and nothing on the channel needs
  protecting. A fake whose control connection ends exits, so no test leaves an orphan.
- **`testing/synctest` is not the definition of quiet.** It fakes all time inside a
  bubble, so the real-time behaviour that other tests depend on (pings, write timeouts)
  would differ between layers; it cannot reach other processes; and the binary layer would
  still need its own definition.
- **A walk records first and compares every rerun with the recording.** A crash at the
  n-th write of a run proves something about the recorded run only if the rerun is that
  run, so each pair of transaction and role must have written the same steps to the same
  files; a target that was not reached, a scenario that grew past its budget, a declared
  write nothing reaches and a write no declaration lists all fail the walk instead of
  being skipped. Sync steps are not kill points for a process kill, since the page cache
  survives it; the power-loss model on the in-memory file system is what they are for.
  Recovery is crashed too, because a recovery that is only correct when it is not
  interrupted is not a recovery.
- **Shards number the runs by an order the recording cannot change.** A shard plans its
  runs from its own recording, and writes of different transactions may interleave
  differently from one recording to the next (the comparison of a rerun allows it, since
  each pair of transaction and role keeps its order). Numbering targets by recording
  order would give "target k" two meanings across shards, so some target would run
  twice and another never, silently. The order is therefore the scenarios by name and,
  inside one, the points sorted by key and hit number, then the step boundaries, then the
  recovery cases by the write they name.
- **One script runs in both layers.** The environment a step uses is an interface in a
  package that imports no engine, and results and errors are the types of `fakeagent`,
  so the binary-layer fixture can implement the same interface over the built program
  and a scenario written once is compared across layers. A script's fields are `Name`
  and `Steps`, so the walk's interface is a view the script returns, not a set of
  methods on it.
- **Judgements read the ledger, and live where both layers can reach them.** A judgement
  that needed the daemon's own state would agree with the daemon's bugs. They are in
  `scenario`, not in `ledger` (which only records) and not in a fixture (which would tie
  them to one layer). A script is held to all of them unless it waives one with a reason,
  so that adding a rule tightens every script. A part shown to the model again is
  judged by the count of sightings without the possible-duplicate mark, because the
  order of entries across writers is not known on a test clock.
- **A layer that lacks a capability says so.** The engine layer has no adapter host and
  no way to create a session yet; `Env` returns an error that is `ErrNotAvailable`
  for them. A stand-in would make a script pass for what it did not exercise, and the
  failure is what tells the stream that adds the capability to remove the error.
- **The node is made by the program's own code,** through the `init` plan and the install
  and enable entries, on a disk copied from one made per test binary. A hand-written disk
  would let the fixture and the program disagree about a format.
- **A crash takes the disk before it touches the process.** The copy of the disk is the
  crash; the old process is stopped in the background and writes to a disk nobody reads.
  Halting the file system instead would need a rule that never fires, which the fault
  wrapper's own check forbids. Power loss is the seeded model of the file system, with
  a fixed second word, so a seed is a disk.
- **The back end answers a method the engine lacks as an unknown method.** The agent
  interface has no category for "not yet", and adding one would change its table; an
  unknown method is what the server would say, and the scripts already handle it. A
  skipped test keeps the question of a dedicated category in sight.
- **Rejected: a text language for scenarios, an assertion library, a goroutine-leak
  library.** Scenarios are Go code so the reader reads what runs and the compiler checks
  names; the checks are a few lines of the standard library; the leak check is a scan of
  goroutine stacks for the packages' own frames.

## Where

| Part | Code | Tests |
| ---- | ---- | ----- |
| Import rule | `internal/node/testkit/imports_test.go` | `TestThirdPartySideImportsOnlyStandardLibraryAndSDK`, `TestImportRuleDetectsViolations` |
| Ledger | `internal/node/testkit/ledger` | `TestGoldenEntryVectors`, `TestDecodeRejects`, `TestEncodeRefusesInvalidEntries`, `TestKindTableCoversEveryKind`, `TestWriterStampsEveryEntry`, `TestConcurrentWritersKeepEachSequence`, `TestDirWritesEachEntryBeforeAppendReturns`, `TestReadDirRejects`, `TestReadDirToleratesTheLastLineOfAKilledProcess`, `TestReadDirRejectsARepeatedStepName`, `TestCheckConfirmations`, `TestDigestIsSHA256InHex`, `TestLogPIDsListsEveryProcessThatWrote`, `TestDirLedgerOrdersEntriesByTimeThenScriptStep`, `FuzzDecode`, `FuzzParseStream` |
| Control channel | `internal/node/testkit/fakectl` | `TestGoldenParams`, `TestDecodeParamsRejects`, `FuzzDecodeParams`, `TestClockAndStepReachTheClientBeforeTheCallReturns`, `TestKnobCallsTheClientsHandler`, `TestRequestsBeforeHelloAreRefused`, `TestClientEndsWhenTheControlConnectionDrops`, `TestCloseEndsEveryGoroutine`, `TestAHandlerRegisteredWhileAConnectionIsBeingMadeReachesIt` |
| Runtime | `internal/node/testkit/fakeruntime`, `cmd/sidera-fake-runtime` | `TestTurnsEndOnlyWhenTheScriptSaysSoAndTimeNeverEndsOne`, `TestPromptDuringATurnIsRefusedAsBusy`, `TestPromptIsDeliveredAtMostOncePerAttempt`, `TestRecvActionRecordsWhatTheModelSawAndReportsTheToolResult`, `TestKilledNativeStopsReportingAndLeavesTheRecordReadable`, `TestAnExecActionThatNamesAFrontendIsAReceive`, `TestARecvPartActionIsAReceive`, `TestAReceiveThatPrintsNoPageIsAnErrorInTheRecord`, `TestTheFirstPromptStartsTheFirstTurnWhenTheAdapterTakesCharge`, `TestARuntimeProcessIsBusyUntilTheAdapterHasHandedTheEventOn`, `TestARuntimeProcessIsBusyUntilTheAdapterHasHandedTheToolResultOn`, `TestAKilledProcessEndsItsRunningTurnAsFailed`, `TestFindDoesNotTakeAProcessThatIsNotTheSessionItsInfoFileNames`, `TestExecRunnerDerivesASeedForEveryCommand`, `TestReconcileReadsTheRecord`, `TestGoldenRecordLines`, `TestDecodeEventRejects`, `FuzzDecodeEvent`, `TestProcessSessionIsReachableAndDrivenOverTheControlChannel`, `TestARestartedAdapterTakesTheLiveSessionBack`, `TestAKilledNativeProcessIsGoneAndItsRecordIsReadable`, `TestGoldenLinkMessages`, `FuzzDecodeLink` |
| Adapter | `internal/node/testkit/fakeadapter`, `cmd/sidera-fake-adapter` | the consistency test `TestFakeAdapterOutputMatchesProtocolSchemas` with `TestFakeAdapterDeclarationIsAdmittedByTheDaemonRules` and `TestFakeAdapterAnswersEveryGoldenRequest`; `TestInstanceLockIsTakenBeforeInitializeIsAnswered`, `TestCreateIsIdempotentPerAttemptAndConflictsOnOtherParameters`, `TestCreateOutcomes`, `TestPauseAtCreateAfterNativeStopsBeforeTheAnswer`, `TestPromptOutcomes`, `TestReconcileAnswersFromTheRecord`, `TestStartReattachesWhatItCan`, `TestTimesTheAdapterReportsComeFromTheTestClock`, `TestObservedModes`, `TestHungAdapterAnswersNothingAndKeepsItsLock`, `TestHangLeavesAHit`, `TestPipeProcessIsAProcessTheAdapterHostCanSupervise`, `TestProcessAdapterDrivesARuntimeProcessAndTakesItBackAfterARestart`, `TestProcessAdapterKnobsOverTheControlChannel`, `FuzzDecodeKnobArguments`, `FuzzDecodeProcessConfig`, `TestOneScriptDrivesTheKnobsOfBothLayers`, `TestHeldIsAMultisetOfMethodsWhateverHoldsTheRequest`, `TestTheFirstPromptStartsATurnTheDaemonHearsOf`, `TestAKilledNativeProcessEndsItsRunningTurnAsFailedExactlyOnce`, `TestAConfirmedCreationNamesTheRuntimesEntry`, `TestTheDaemonSideIsToldBeforeTheCallThatCausedTheEventReturns`, `TestTheAdapterProgramStartsFromTheConfigFileOfItsArguments`, `TestAnInitializeThatRunsAfterTheAdapterWasReleasedTakesNoLock` |
| Fake agent | `internal/node/testkit/fakeagent` | `TestTheInterfaceHasAMethodForEveryAgentCommand`, `TestErrorCategories`, `TestRecordingWritesTheCallBeforeTheActionAndTheResultAfter`, `TestRecordingCanRecordWhatTheModelSawOutsideATurn` |
| Engine backend | `internal/node/testkit/fakeagent/engineagent` | `TestDeclarePatchReachesTheEngineForTheCallersOwnEndpoint`, `TestDeclareLeavesAFieldThatIsNilAsItWas`, `TestDeclareRefusalsAreMappedToTheCategoryAndCodeOfTheInterface`, `TestEveryEngineReasonMapsToACategoryAndCode`, `TestCallsTheEngineDoesNotOfferYetReturnTheNotSupportedError` |
| Scripts | `internal/node/testkit/scenario`, `internal/node/testkit/scenario_imports_test.go` | `TestRunSetsUpDirectEndpointsThenRunsEachStepAndQuiescesAfterIt`, `TestRunStopsAtTheFirstFailedStepAndNamesIt`, `TestRunRefusesAScriptThatCannotRunBeforeTouchingTheSystem`, `TestRecvAndAckAllAcknowledgesEveryPageAndStopsAtAnEmptyOne`, `TestAScriptIsAScenarioOfTheCrashWalk`, `TestCheckRejects`, `TestScenarioPackageDoesNotImportTheEngineOrAFixtureOfAnyLayer` |
| Engine fixture | `internal/node/testkit/harness` | `TestOneClockIsTheSourceOfTimeForTheEngineAndTheFakes`, `TestADirectEndpointRegisteredThroughTheFixtureAppearsInTheEnginesList`, `TestQuiesceReturnsOnlyWhenTheEnginesConditionsHoldAndOtherwiseFailsWithAReport`, `TestAProcessCrashThenRestartKeepsExactlyWhatWasDurable`, `TestAPowerLossRestartIsReproducibleUnderAFixedSeedAndKeepsWhatWasAcknowledged`, `TestFaultsAreInjectedThroughTheFileLayerAndTheirHitsCounted`, `TestAScenarioScriptRunsThroughEnvOnTheEngineLayer`, `TestTheLeakCheckReportsAGoroutineOfTheMarkedPackages` |
| Crash walk | `internal/node/testkit/crashwalk` | `TestPlanFullTakesEveryWindowPointExceptTheSyncSteps`, `TestPlanSmokeTakesOnePointPerTransactionBeforeItsLastFileRole`, `TestShardsPartitionTheTargets`, `TestWalkShardsWhoseRecordingsInterleaveDifferentlyRunEveryTargetOnce`, `TestWalkFullReportsWhichMainCrashPointMadeWhichRecoveryWrites`, `TestWalkReportsCoverageOfNormalAndRecoveryPathsApart`, `TestCompareRuns`, `TestCheckBudget`, `TestAssessCoverage`, `TestCheckRecoveryCasesUnion`, `TestWalkFailsWhenATargetIsNotReached`, `TestWalkFailsWhenARerunDivergesFromTheRecording`, `TestWalkSecondLayerCrashesTheRecoveryAfterTheMainCrash` |

## The consistency test

The fake adapter is held to the part of the repository it did not write. In a scenario
that uses every method in both directions, each line it writes is accepted by the JSON
Schema under `protocol/adapter/v1` and decodes into the Go type of its method; across the
scenario the adapter speaks every method it must (the methods an adapter that declares a
runtime and direct endpoints is required to call, and the optional ones the fake uses),
and the daemon sends it every method it may receive; its declaration, taken from the same
options as its manifest, is admitted by `adapterreg.Admit`, passes the handshake check
against the manifest and carries receive wait limits that differ from both the default
and the cap; and every golden request of the daemon in `protocol/adapter/v1/examples`, sent
unchanged after a handshake (only the lock path of the handshake names another machine's
directory and is replaced), is answered with a result the schema accepts.

## Known gaps and what is not verified here

- The instance lock on Windows (a file opened with a share mode of 0), the creation of
  a runtime process in a process group of its own, and the claim that it then outlives
  the adapter that started it are written for Windows and cross-compiled, and have never
  run there: pending verification by CI on main. The tests that exercise them are
  ordinary tests that run on every platform.
- On Unix a runtime process whose adapter is still alive and that ended is not reaped
  until the adapter ends; processes are meant to outlive their adapter, so no goroutine
  waits for them.
- A recv result a fake runtime hands its model and a node's own notice cannot be told
  apart by the fake: the classifier that names a notice is supplied by whoever knows the
  format.
- The binary-layer fixture and the backends of the fake agent that run the command-line
  client and speak MCP, and the backends of the crash walk, are not in this tree yet.
- The engine-layer fixture has no adapter host, no loopback knobs, no session creation,
  no real file system mode and no stand-in for free space yet; `Adapter` and `Runtime`
  return `scenario.ErrNotAvailable` until the code they stand for exists. Judgements whose
  subject does not exist yet (no message lost, one feedback per delivery and kind,
  acknowledgements on behalf, every record closed, the drain) are not in `scenario`.
- The engine backend maps the engine's refusals itself, with the codes the interface's
  table does not list taken from the reasons' names; when the interface packages' shared
  mapping exists it replaces the table here. The fixture declares the transactions of
  `init` and of the adapter install as the composition root will, and takes them from it
  when it exists.
- The program path of a fake adapter has the extension of an executable of the platform
  (`.exe` on Windows, where a path that names a program is used as it is), written for
  Windows and cross-compiled: pending verification by CI on main.
