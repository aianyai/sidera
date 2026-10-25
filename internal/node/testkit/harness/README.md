# harness

The engine-layer fixture: the engine on the real store, the real configuration and
the real transaction layer, on the in-memory file system with its crash model, with
one test clock, a ledger and the agents of the endpoints a test registers. Read
[`../README.md`](../README.md) first for where the package sits, and
[`docs/references/testkit/`](../../../../docs/references/testkit/README.md) for the
reasons.

It is on the Sidera side of the testkit: it imports the engine. `*H` implements
`scenario.Env`, so a script that uses only `Env` runs here and, later, in the binary
layer.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `New(t, Options)` | Makes the node, starts the engine through the startup steps it has (scan, start, admission of the adapter, recovery of deliveries, serve) and stops everything when the test ends. |
| `Options` | The origin of the clock, the seed of the random source, the adapter's direct limit, the engine's `Params`, an observer of every write step, and whether the engine starts with the clock-rollback pause. |
| `H.Direct`, `H.MustDirect`, `H.Addr`, `H.Agent` | A direct endpoint registered through the engine's own entry, its address, and its agent: `engineagent` wrapped in `fakeagent.Recording`. |
| `H.Advance`, `H.Set`, `H.Clock` | The one test clock. `Clock().Now` is the `func() time.Time` a fake takes. |
| `H.Quiesce(opts...)`, `QuiesceError` | Waits for quiet, fails with a report. |
| `H.Crash(ProcessCrash \| PowerLoss(seed))`, `H.Restart()` | A crashed process or power, and a clean stop, each followed by a start on the resulting disk. A crash writes a `fixture.crash` entry to the ledger with its mode, the seed of a power loss and the point the test names with `CrashMode.At`. |
| `H.Stop()`, `H.Kill(mode)`, `H.Start()`, `H.StartWith(arm)` | The halves of the two above, for what a test does while the node is down: `Disk()` is then the disk the next start runs on, so a test changes files offline, and `arm` receives the fault wrapper of the new process before its recovery runs, so a rule can target a write only a recovery makes. A call to a node that is down is `unavailable/daemon_not_running`. |
| `H.BeginStep`, `H.Ledger`, `H.CheckLedger` | The script step the ledger stamps, the ledger, and the judgements that need only the ledger. |
| `H.Records` | A read-only scan of the records on the disk. |
| `H.Faults`, `H.Disk`, `H.Config`, `H.Engine`, `H.Log`, `H.Ctx` | The fault wrapper the running process writes through, its disk, configuration, engine, log and the test's context. |
| `H.Adapter`, `H.Runtime` | Return `scenario.ErrNotAvailable`: the engine layer has no adapter host and no session yet. |
| `ProgramTable`, `MakeLayout`, `Mount`, `SeedNode`, `AdapterAdmission`, `RandReader`, `LogCapture`, `NoLeaks`, `DefaultStart`, `NodeID`, `Instance` | The building blocks the fixture is made of, exported so that a test of a package that needs a node but not the fixture (the engine's own tests) makes it the same way. |

## Rules

- **The node is made by the program's own code.** The configuration is written by
  the `init` plan, the adapter installed and enabled through the install and enable
  entries; no file is written by hand, so the fixture and the program cannot
  disagree about a format. The two transactions the composition root will own
  (`init`, `adapter-install`) are declared here the way it declares them. Each
  fixture starts from a copy of one initialised disk per test binary.
- **There is one clock.** The engine reads it, the ledger and the agents stamp with
  it, and a fake takes its `Now` method value. A crash does not move it.
- **Quiet is a round in which no count moved.** A condition of the fixture's parts
  answers whether it holds and a count that never goes down (notifications sent, turns
  ended). `Quiesce` looks at the conditions, asks the engine (`engine.Quiesce`, with an
  expectation made again each round) and looks again, and returns only when every
  condition held on both looks and no count differs between them: a fake that took work
  while the engine was asked, finished it and went idle again holds on both looks and
  still has something the engine has not seen. It fails on the real clock's limit (60 s
  unless `WithTimeout`) with the engine's last report, whether the engine was asked at
  all, and the names of the conditions that did not hold. A loop that was asked and
  answered neither with a report nor with a refusal is reported as inside a command.
- **A crash cuts the process off, and what it was doing has no visible effect.** `Crash`
  closes a layer of the fixture's own between the process's fault wrapper and its disk
  (every later file-system call of the old process fails, and the close waits for the
  calls in flight), then copies the disk (`Clone`, or `CrashClone` seeded by
  `PowerLoss`), starts a new process on the copy and stops the old engine in the
  background, which is the test's cleanup and not an event of the node. Three things
  follow. What the old process was in the middle of reaches neither disk. A call made
  to it (an agent's, or `Direct`) that returns after the crash has an unknown outcome:
  an agent sees `outcome_unknown/connection_lost`, and `Direct` returns an error that
  is `engineagent.ErrProcessEnded` and does not remember the name. And when the adapter
  host is attached, a crash cuts the pipe and sends no shutdown, as a process that died
  would; only a clean `Restart` shuts the adapter down. The test releases anything it
  held the old loop with (an observer) before the test ends; the observer still sees
  the steps the old process takes after the crash.
- **Offline hooks are the halves of a restart.** `Stop` and `Kill` leave the node down on
  a disk the test may change (`Disk()`); `Start` and `StartWith` run a process on it. A
  scenario that corrupts a file between two runs, or that crashes the recovery of a
  crash, is built from these and not from a mode of `Crash`.
- **A start forgets the last run's in-memory state.** `Options.RollbackPaused` and the
  clock's high-water mark are applied from zero at every start, as a daemon that was
  just launched has no memory of the last run: a restart does not carry the high-water
  mark over, and a node made to start paused starts paused every time.
- **A seed is a disk.** The power-loss source is `rand.NewPCG(seed, 0x5ce)`: the same
  seed on the same history leaves the same disk.
- **Faults go through the file layer.** `Faults()` is the `fsxtest.Faulty` the running
  process writes through (`fsxtest.DiskFull` is the full-disk error); a rule that never
  fires fails the test. A new process has a new wrapper with no rules.
- **No test-only session registration.** The fixture has no method that registers a
  session endpoint; sessions come from a session creation when the engine offers one.
- **No stand-in for what is missing.** `Adapter` and `Runtime` return an error that
  is `scenario.ErrNotAvailable`; a script that needs them fails with that and says
  what is missing.
- **Records are read, not written.** `Records` is the store's scan, for the scenarios
  whose judgement is the records; everything else is judged from the ledger. It reads
  what the engine has written by now, so read it after `Quiesce`.
- **Goroutines are checked.** `New` registers `NoLeaks`: a goroutine of the engine,
  the transaction layer, the store or the fixture that was not running before the
  test is a failure.

## What the caller must guarantee

- Drive the fixture from one goroutine; `Direct` may run in another while the test
  waits on it.
- Release an observer's hold before the test ends, or `Stop` of the abandoned process
  waits for its limit.
- Announce each step name once.
- Do not run tests that use the fixture in parallel with each other in one binary:
  `NoLeaks` finds goroutines by the package names in their stacks, across the whole
  process, so a goroutine of a test that runs beside it is taken for a leak. (The tests
  of this package call neither `t.Parallel` nor share a process with one that does.)

## What later parts add, and where they attach

Each adds to this package the capability its stream needs:

- the loopback transport's fault knobs: fields of `Options` and methods of `H`, and
  the conditions the loopback registers with `addQuiet`;
- the adapter host and the fake adapter: the launcher on `fakeadapter.StartPipe`, the
  `Adapter` method, the notification and hold counts in the engine's `Expect`, and
  quiet conditions from `fakeadapter.Controller.Stats`;
- session creation: a fake runtime owned by the fixture, the `Runtime` method, the
  presets and node settings in `Options`, and its idle condition in `addQuiet`;
- the real file system and the free-space stand-in: a mode of `Options`.

## Tests

`harness_test.go`: `TestOneClockIsTheSourceOfTimeForTheEngineAndTheFakes`,
`TestADirectEndpointRegisteredThroughTheFixtureAppearsInTheEnginesList`,
`TestTheNodeCapRefusesTheEndpointBeyondItThroughTheFixture`,
`TestQuiesceReturnsOnlyWhenTheEnginesConditionsHoldAndOtherwiseFailsWithAReport`,
`TestQuiesceFollowsAnAdvanceThatMakesWorkDue`,
`TestAProcessCrashThenRestartKeepsExactlyWhatWasDurable`,
`TestACleanRestartKeepsTheEndpointsAndTheClock`,
`TestAPowerLossRestartIsReproducibleUnderAFixedSeedAndKeepsWhatWasAcknowledged`,
`TestFaultsAreInjectedThroughTheFileLayerAndTheirHitsCounted`,
`TestEachFixtureStartsFromItsOwnCopyOfTheInitSnapshot`,
`TestTheLedgerCarriesTheScriptStepTheFixtureAnnounced`,
`TestWhatTheEngineLayerCannotProvideYetIsAnErrorAndNotAFake`,
`TestRecordsAreReadOnlyAndShowTheDisk`,
`TestAScenarioScriptRunsThroughEnvOnTheEngineLayer`,
`TestAProcessCrashKeepsAWriteThatWasNotSyncedYet`,
`TestAStoppedNodeIsDownAndAFileChangedOfflineIsWhatTheNextStartReads`,
`TestAFaultArmedForTheNextStartAppliesToThatProcess`,
`TestAKilledNodeIsDownOnTheDiskTheCrashLeftUntilItIsStarted`,
`TestACrashIsWrittenToTheLedgerWithItsModeAndSeed`,
`TestACrashThatNamesItsPointIsWrittenWithThePoint`,
`TestACallInFlightAtACrashHasAnUnknownOutcomeAndLeavesNoEffectOfTheAbandonedProcess`;
`harness_internal_test.go`: the fixture's own quiet conditions
(`TestQuiesceWaitsForEveryConditionOfTheFixtureAndNamesTheOneThatDoesNotHold`,
`TestQuiesceChecksTheFixtureAgainAfterTheEngineWasQuiet`,
`TestQuiesceDoesNotReturnOnAConditionThatStoppedHoldingWhileTheEngineSettled`,
`TestQuiesceDoesNotReturnWhileAConditionThatKeepsHoldingStillMakesProgress`,
`TestQuiesceReturnsAfterOneRoundWhenNoCountMoves`,
`TestAQuiesceErrorSaysACommandIsRunningOnlyWhenTheLoopDidNotAnswer`), the leak check
(`TestTheLeakCheckReportsAGoroutineOfTheMarkedPackages`) and
`TestADirectCallThatWasInFlightAtACrashIsNotRecordedUnderItsName`.
