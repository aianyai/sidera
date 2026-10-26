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
| `Options` | The origin of the clock, the seed of the random source, the adapter's direct limit, the engine's `Params`, an observer of every write step, whether the engine starts with the clock-rollback pause, the file system (`FS`: in memory or the real one), a disk to start on (`Disk`), the node's rules (`Policy`) and a patch of its settings (`NodeSettings`) applied before the first start, and a stand-in for the free space (`DiskFree`), and `Adapter`: the adapter host (below). |
| `H.Loopback`, `H.DropReceipt`, `H.Unreachable`, `H.UnreachableNode`, `H.HoldReports`, `H.Release`, `H.Delay`, `H.Mutate`, `H.Mutated`, `H.VerifyReport` | The loopback of the running process, its knobs, and the check of a report the node signed. Each hit of a knob is a `knob.hit` entry of the ledger. |
| `Options.Adapter`, `AdapterOptions` | Gives the node an adapter host, on the fixture's test clock, that starts a fake adapter for `Instance` in the test process and admits the instance through it as the daemon does. `AdapterOptions.Host` is the host's `Params`; a zero field takes the default. Nil: no host, and the fixture admits the instance itself through the engine's entry. |
| `H.Adapter(instance)`, `H.AdapterLaunches(instance)`, `AdapterLaunch` | The controller of the latest fake adapter process (`scenario.ErrNotAvailable` without a host or before a launch), and every process the host started, in order, across restarts of the node: `Methods()` (what the daemon wrote to it), `Controller()`, `Survive()`, `End()`, `Ended()`. |
| `AdapterLauncher`, `NewAdapterLauncher`, `LauncherConfig`, `AdapterManifest` | The one launcher the fixture and a test of the daemon share (`daemon`'s tests start fake adapters with it), and the declaration of the fake adapter, from which both the installed manifest and the capabilities the fake answers `initialize` with are made. |
| `H.Direct`, `H.MustDirect`, `H.Addr`, `H.Agent` | A direct endpoint registered through the engine's own entry, its address, and its agent: `engineagent` wrapped in `fakeagent.Recording`. |
| `H.Advance`, `H.Set`, `H.Clock` | The one test clock. `Clock().Now` is the `func() time.Time` a fake takes. |
| `H.Quiesce(opts...)`, `QuiesceError` | Waits for quiet, fails with a report. |
| `H.Crash(ProcessCrash \| PowerLoss(seed))`, `H.Restart()` | A crashed process or power, and a clean stop, each followed by a start on the resulting disk. A crash writes a `fixture.crash` entry to the ledger with its mode, the seed of a power loss and the point the test names with `CrashMode.At`. |
| `H.Stop()`, `H.Kill(mode)`, `H.Start()`, `H.StartWith(arm)` | The halves of the two above, for what a test does while the node is down: `Disk()` is then the disk the next start runs on, so a test changes files offline, and `arm` receives the fault wrapper of the new process before its recovery runs, so a rule can target a write only a recovery makes. A call to a node that is down is `unavailable/daemon_not_running`. |
| `H.BeginStep`, `H.Ledger`, `H.CheckLedger` | The script step the ledger stamps, the ledger, and the judgements that need only the ledger. |
| `H.Records` | A read-only scan of the records on the disk. |
| `H.Faults`, `H.Disk`, `H.Config`, `H.Engine`, `H.Log`, `H.Ctx` | The fault wrapper the running process writes through, its disk, configuration, engine, log (what the engine and the adapter host logged) and the test's context. |
| `H.Runtime` | Returns `scenario.ErrNotAvailable`: the engine layer cannot create a session yet. |
| `ProgramTable`, `MakeLayout`, `Mount`, `SeedNode`, `AdapterAdmission`, `RandReader`, `LogCapture`, `NoLeaks`, `DefaultStart`, `NodeID`, `Instance` | The building blocks the fixture is made of, exported so that a test of a package that needs a node but not the fixture (the engine's own tests) makes it the same way. |

## Rules

- **The node is made by the program's own code.** The keys and the configuration are
  written by the `init` plan (the keys are real, from a seeded source, so the node
  identifier is the same in every fixture), the adapter installed and enabled through the install and enable
  entries; no file is written by hand, so the fixture and the program cannot
  disagree about a format. The transaction the composition root owns
  (`init`) is declared here the way it declares it, and the installation is the
  engine's own declaration, which the fixture runs. Each
  fixture starts from a copy of one initialised disk per test binary.
- **There is one clock.** The engine reads it, the ledger and the agents stamp with
  it, and a fake takes its `Now` method value. A crash does not move it.
- **With an adapter host, quiet also means the adapter is.** The fixture adds a condition
  for the latest launch of the instance: it has no request of its own that the daemon
  has not answered (`Controller.Stats().Outstanding` is zero), and its progress is the
  notifications it has sent. The engine is asked with `Expect.Adapters[instance]` set to
  the notifications the latest launch has sent and the requests it holds on purpose
  (`Stats().NotificationsSent`, `Held`), made again for every round, and each round is
  given 100 ms of real time at most, because the numbers the engine holds do not follow
  an adapter that sends while it is asked. Only the latest launch is asked, so a process
  that is hung or abandoned never makes the fixture wait for ever once the host has
  started another.
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
  host is attached, a crash aborts it (`Host.Abort`) and cuts the pipes: no shutdown is
  sent, as a process that died would send none; only a clean `Stop` or `Restart` calls
  `Host.Shutdown` and so asks the adapter to shut down. The host is aborted after the
  disk was copied, so that what it does while it ends reaches no disk. The calls the
  engine makes on the host (`engine.Adapters`) go through the same abandoned-process
  check as the agents' calls: one made after the process was abandoned, or that returns
  after it was, is `ErrProcessEnded` and never a confirmation; the counts, which are
  no call that can be confirmed, pass through. The test releases anything it held the
  old loop with (an observer) before the test ends; the observer still sees the steps
  the old process takes after the crash.
- **The host runs on the test clock.** Its pings, its restart backoff, its waits for a
  process to end and the limits of its calls are timers of the fixture's clock, so
  `Advance` moves them and nothing in a test waits for real time. A step of a script
  that moves the clock and then waits (`Quiesce`) is only taken when the host has
  armed what the step is meant to fire: the quiet check asks the host whether it is
  working, as the host's own contract says.
- **Every launch has knobs of its own; the ledger writer of an instance is one.** A
  restarted adapter is a new process with new knobs, and opening the same ledger
  stream twice would fork its sequence, so the writer `adapter/<instance>` is made
  once and shared by every launch. A launch that was not told to survive ends on
  `Kill` and when its stream closes, as `fakeadapter.PipeProcess` does. `Survive`
  makes it a hung process the host cannot end: the host's `Kill` returns an error and
  the closing of its stream leaves it running, its connection is not ended by the host closing its
  end of the pipes, it holds the instance lock, and `Wait` does not return until
  `End`, which really ends it whatever `Survive` said. The test ends a survivor before
  it ends, or the host's shutdown waits for it. `Methods` is read from what the
  daemon writes: the peer writes one line per `Write`, and the `method` member of a
  line is all that is read.
- **The instance lock is a real file** under a directory of the test (`t.TempDir()`),
  which is the data directory the host and the engine are told: a lock path is not a
  socket path, so the long temporary path does not matter, and the fixture calls no file
  function of its own.
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
- **No stand-in for what is missing.** `Adapter` (without `Options.Adapter`) and
  `Runtime` return an error that is `scenario.ErrNotAvailable`; a script that needs them
  fails with that and says what is missing.
- **Records are read, not written.** `Records` is the store's scan, for the scenarios
  whose judgement is the records; everything else is judged from the ledger. It reads
  what the engine has written by now, so read it after `Quiesce`.
- **Goroutines are checked.** `New` registers `NoLeaks`: a goroutine of the engine,
  the transaction layer, the store, the adapter host, the fake adapter or the fixture
  that was not running before the test is a failure.

## What the caller must guarantee

- Drive the fixture from one goroutine; `Direct` may run in another while the test
  waits on it.
- Release an observer's hold before the test ends, or `Stop` of the abandoned process
  waits for its limit.
- Announce each step name once.
- End a launch you told to survive (`End`) before the test ends if the host would wait
  for it at a clean stop; the fixture ends the survivors itself at the end of the test.
- Do not run tests that use the fixture in parallel with each other in one binary:
  `NoLeaks` finds goroutines by the package names in their stacks, across the whole
  process, so a goroutine of a test that runs beside it is taken for a leak. (The tests
  of this package call neither `t.Parallel` nor share a process with one that does.)

## What later parts add, and where they attach

The loopback with its knobs, the real file system, the free-space stand-in and the adapter host are in. Each
later part adds to this package the capability its stream needs:

- session creation: a fake runtime owned by the fixture, which the fake adapter then
  declares (today it declares direct endpoints only), the `Runtime` method, the presets
  and node settings in `Options`, and its idle condition in `addQuiet`;

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
`loopback_test.go`: `TestAKnobHitIsWrittenToTheLedgerWithTheCallAndTheNode`,
`TestTheNodeHasRealKeysAndOnlyItsOwnReportsVerify`,
`TestOptionsReplaceTheRulesAndTheSettingsBeforeTheFirstStart`,
`TestANodeOnTheRealFileSystemRunsAndCannotBeCrashed`;
`adapter_test.go`: `TestNoSecondWriterAfterAdapterRestart`,
`TestACrashCutsTheAdapterWithoutAShutdownAndACleanRestartShutsItDown`,
`TestQuiesceWaitsUntilTheEngineHasHandledEveryNotificationTheAdapterSent`,
`TestTheAdapterOfTheFixtureIsTakenOnThroughTheHost`;
`harness_internal_test.go`: the wrapper of the adapter host after a crash
(`TestACallToTheAdapterHostOfALiveProcessIsPassedOn`,
`TestACallToTheAdapterHostAfterTheProcessWasAbandonedIsNeverSent`,
`TestACallToTheAdapterHostThatReturnsAfterTheProcessWasAbandonedIsNeverAConfirmation`,
`TestTheCountsOfTheAdapterHostPassThroughWhetherOrNotTheProcessWasAbandoned`), the
launcher (`TestEveryLaunchOfAnInstanceWritesThroughTheSameLedgerWriter`), the
fixture's own quiet conditions
(`TestQuiesceWaitsForEveryConditionOfTheFixtureAndNamesTheOneThatDoesNotHold`,
`TestQuiesceChecksTheFixtureAgainAfterTheEngineWasQuiet`,
`TestQuiesceDoesNotReturnOnAConditionThatStoppedHoldingWhileTheEngineSettled`,
`TestQuiesceDoesNotReturnWhileAConditionThatKeepsHoldingStillMakesProgress`,
`TestQuiesceReturnsAfterOneRoundWhenNoCountMoves`,
`TestAQuiesceErrorSaysACommandIsRunningOnlyWhenTheLoopDidNotAnswer`), the leak check
(`TestTheLeakCheckReportsAGoroutineOfTheMarkedPackages`) and
`TestADirectCallThatWasInFlightAtACrashIsNotRecordedUnderItsName`.
