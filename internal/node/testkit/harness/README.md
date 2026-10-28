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
| `NewStopped(t, Options)` | Prepares the same initialized disk, clock, ledger and fakes as `New`, leaving the node down so the first generation can use controlled startup. |
| `H.TryStart(ctx, StartOptions)` | Synchronously starts a down node or reclaims everything that startup acquired before returning `(crashed, err)`. `Arm` installs ordinary fault rules before transaction recovery; `CrashAt` selects a startup point and crash mode while the fixture owns the cutoff and cleanup. |
| `NewWalkBackend(t)`, `WalkScenario` | Runs a script for `crashwalk.Backend` on a fresh in-memory fixture, with per-generation observations, process crashes, recovery triggers, a repeatable drain and ledger judgements. `WalkScenario.Options` configures the fixture; `Damage` interprets an offline trigger on its stopped disk, and `Check` adds a final judgement. |
| `StartupWalkScenario`, `StartupOperation` | Runs actual `daemon.Init` from an empty disk (`StartupInit`) or actual `daemon.Start` after an unobserved Init (`StartupStart`). Names, budgets, recovery cases, offline damage and read-only checks are scenario data. `StartAfter` advances the same fixed clock between Init and the first observed Start; default zero, non-negative and only valid for Start. |
| `Options` | The origin of the clock, the seed of the random source, the adapter's direct limit, the engine's `Params`, an observer of every write step, whether the engine starts with the clock-rollback pause, whether it runs the archive sweep and how often (`ArchiveSweepEvery`), the file system (`FS`: in memory or the real one), a disk to start on (`Disk`), the node's rules (`Policy`) and a patch of its settings (`NodeSettings`) applied before the first start, the presets the node has besides the one `init` makes (`Presets`, by name, each the JSON `presets add` takes, added through the configuration's own change), and a stand-in for the free space (`DiskFree`), and `Adapter`: the adapter host (below). |
| `H.Loopback`, `H.DropReceipt`, `H.Unreachable`, `H.UnreachableNode`, `H.HoldReports`, `H.Release`, `H.Delay`, `H.Mutate`, `H.Mutated`, `H.VerifyReport`, `H.VerifyControlReply` | The loopback of the running process, its knobs, and the check of a report or a control reply the node signed. Each hit of a knob is a `knob.hit` entry of the ledger. |
| `Options.Adapter`, `AdapterOptions` | Gives the node an adapter host, on the fixture's test clock, that starts a fake adapter for `Instance` in the test process and admits the instance through it as the daemon does. `AdapterOptions.Host` is the host's `Params`; a zero field takes the default. `AdapterOptions.Runtime` makes the adapter declare a runtime beside its direct endpoints and drive one fake runtime that the fixture owns and that outlives every process of the adapter (the manifest installed is `RuntimeAdapterManifest`, so the node makes the runtime endpoint of the instance). `AdapterOptions.ExtraInstances` names further instances, each installed, enabled and run like `Instance` with an adapter, controller, presets and (with `Runtime`) runtime and runtime endpoint of its own (below). Nil: no host, and the fixture admits the instance itself through the engine's entry. |
| `H.Adapter(instance)`, `H.AdapterLaunches(instance)`, `AdapterLaunch` | The controller of the latest fake adapter process (`scenario.ErrNotAvailable` without a host or before a launch), and every process the host started, in order, across restarts of the node: `Methods()` (what the daemon wrote to it), `Controller()`, `Survive()`, `End()`, `Ended()`. Both take the name of any instance of the fixture. |
| `AdapterLauncher`, `NewAdapterLauncher`, `LauncherConfig`, `LauncherInstance`, `AdapterManifest`, `AdapterManifestOf`, `RuntimeAdapterManifestOf` | The one launcher the fixture and a test of the daemon share (`daemon`'s tests start fake adapters with it), and the declaration of the fake adapter, from which both the installed manifest and the capabilities the fake answers `initialize` with are made. `LauncherConfig.Instances` gives an instance a manifest and runtime other than the default pair. |
| `H.Direct`, `H.DirectOn`, `H.MustDirect`, `H.Addr`, `H.Agent` | A direct endpoint registered through the engine's own entry (`DirectOn` on a named instance of the fixture), its address, and its agent: `engineagent` wrapped in `fakeagent.Recording`, calling as the endpoint's own instance. A name stands for one endpoint of the whole fixture. |
| `H.Session` | The agent of a session endpoint the node made by a creation, by the identifier the reply to the creation names. The token is read from the endpoint's record on the disk, so a session is reached only through what the node made. Every agent of the fixture also writes a `model.saw` entry for each message or part a page showed, the notices of the node's own system endpoint told from the messages of others. The fake runtimes of the fixture carry out the actions of a turn (`recv`, `recv_part`, `ack`) by calling the engine as the session the turn belongs to, with the token of its tool access, so the adapter is told what the model was shown and acknowledges on the agent's behalf. |
| `NewControlGen`, `ControlGen.Next`, `ControlGen.Paths`, `ControlPaths` | The generator of control signals for a model test of what becomes of every record: creations with and without an initial message, creations the node refuses, statuses of every kind of endpoint, interrupts and closes of the sessions it made, of one it closed and of endpoints that do not take them. The draw is a function of the seed; `Paths` counts how often each path was taken, so a test asserts that none stopped being reached. |
| `H.Advance`, `H.Set`, `H.Clock` | The one test clock. `Clock().Now` is the `func() time.Time` a fake takes. |
| `H.Quiesce(opts...)`, `QuiesceError` | Waits for quiet, fails with a report. |
| `H.Crash(ProcessCrash \| PowerLoss(seed))`, `H.Restart()` | A crashed process or power, and a clean stop, each followed by a start on the resulting disk. A crash writes a `fixture.crash` entry to the ledger with its mode, the seed of a power loss and the point the test names with `CrashMode.At`. |
| `H.Stop()`, `H.Kill(mode)`, `H.Start()`, `H.StartWith(arm)` | The halves of the two above, for what a test does while the node is down: `Disk()` is then the disk the next start runs on, so a test changes files offline, and `arm` receives the fault wrapper of the new process before its recovery runs, so a rule can target a write only a recovery makes. A call to a node that is down is `unavailable/daemon_not_running`. |
| `H.BeginStep`, `H.Ledger`, `H.CheckLedger` | The script step the ledger stamps, the ledger, and the judgements that need only the ledger. |
| `H.Records` | A read-only scan of the records on the disk. |
| `H.Faults`, `H.Disk`, `H.Config`, `H.Engine`, `H.Log`, `H.Ctx` | The fault wrapper the running process writes through, its disk, configuration, engine, log (what the engine and the adapter host logged) and the test's context. |
| `H.Runtime`, `H.RuntimeAddr`, `H.RuntimeOf`, `H.RuntimeAddrOf` | The controller of the fake runtime of an adapter made with `AdapterOptions.Runtime` (`scenario.ErrNotAvailable` without), and the address of the runtime endpoint of the instance, the target of a session creation. The first two are `Instance`'s; the others take the name of any instance of the fixture. |
| `ProgramTable`, `MakeLayout`, `Mount`, `SeedNode`, `AdapterAdmission`, `RandReader`, `LogCapture`, `NoLeaks`, `DefaultStart`, `NodeID`, `Instance` | The building blocks the fixture is made of, exported so that a test of a package that needs a node but not the fixture (the engine's own tests) makes it the same way. |

## Several adapter instances

By default the node has one instance, `Instance`, and every accessor without a name belongs to it. `AdapterOptions.ExtraInstances` adds instances for a test of what the node does when one adapter is away while another serves (the node-wide cap on prompt turns counts only adapters that are online). Each extra instance is its own adapter package, named for the instance and installed and enabled through the same entries as `Instance`, so the host starts one fake adapter process for it and its sessions are those of a fake runtime of its own (seeded `Seed` plus its place in the list, so no two runtimes name a native session alike). A session is made by a creation at `RuntimeAddrOf(instance)`, and a session agent (`H.Session`) is reached by the identifier as for `Instance`. The names must be valid adapter identifiers, different from `Instance` and from each other, or `New` fails the test; `Options.Disk` is used as it is, so a disk handed in must already have the instances installed and enabled.

## Rules

- **A walk's trace belongs to one generation.** Its observer is bound before the
  writer exists, converts complete observations through `crashwalk.NewHit`, and
  marks first-generation startup and direct-endpoint setup outside the main crash
  window. Recovery starts inside its own window. The selected target is recorded
  before the window closes; callbacks after it still reach `Options.Observer` but
  enter neither that trace nor a later generation's. A runtime target holds its
  program callback while the runner calls `Kill` from outside, then releases and
  joins the interrupted script operation. If that operation has already returned
  an error while another program callback is held, the same cutoff and release
  happen before the error returns; closing the error window prevents later claims.
  Startup targets use controlled startup and its ordinary error unwinding,
  including logical commits with no following filesystem call. A crash abandons the rest of the script and runs its drain on
  the recovered disk; a drain interrupted by another target runs again. Offline
  damage runs only after a clean stop. A recovery crash trigger may select a point
  in the script or its first drain, just as a main target can. Each run clones any
  provided `Options.Disk`, preserving the initial disk for recording and replay,
  and reclaims its fixture before the next run. Ledger judgements and `Check`
  failures become `Violations`.
- **Startup walks use the daemon's own entry points.** `WalkBackend` dispatches
  `WalkScenario` to the engine fixture and `StartupWalkScenario` to actual Init or
  Start. The private run state owns plan validation, per-generation target
  selection, recorders, trigger accounting and trace results for both. A startup
  generation opens its window before its one writer exists. Start's preparatory
  Init is unobserved, so two writers never share one generation's counters.
  `complete` is the one step boundary, after Init returns or Start has quiesced
  and stopped. Start's ordinary shutdown stays inside the observation window.
  Selection uses the same recorder and `NewHit` conversion as engine scripts;
  the startup callback only cuts off access, clones the disk and cancels startup
  waits. Failed Start reclaims its own resources; successful Start is stopped with
  RequestStop/Wait before the generation advances. A selected logical commit
  counts as a crash even if the entry point returns nil. After an interrupted
  Init, an unfinished marker means actual Init runs again; a completed marker
  means verification with an empty next trace, with no synthetic Init events.
  Existing keys, self-revocation, clock and owner configuration must survive
  retries unchanged. Each run has its own short private socket home and in-memory
  disk, fixed clock and synchronized seeded random source. Damage runs only on
  the stopped disk after the window closes; final checks read without a writer.
- **The node is made by the program's own code.** The keys and the configuration are
  written by the `init` plan (the keys are real, from a seeded source, so the node
  identifier is the same in every fixture), the adapter installed and enabled through the install and enable
  entries; no file is written by hand, so the fixture and the program cannot
  disagree about a format. `init` is the daemon's own declaration and the
  installation the engine's, which the fixture runs, and `ProgramTable` is the daemon's table, so
  it holds every transaction the program declares. Each
  fixture starts from a copy of one initialised disk per test binary.
- **There is one clock.** The engine reads it, the ledger and the agents stamp with
  it, and a fake takes its `Now` method value. A crash does not move it.
- **With an adapter host, quiet also means the adapter is.** The fixture adds a condition
  for the latest launch of each instance: it has no request of its own that the daemon
  has not answered (`Controller.Stats().Outstanding` is zero), and its progress is the
  notifications it has sent. The engine is asked with `Expect.Adapters[instance]` set to
  the notifications the latest launch has sent and the requests it holds on purpose
  (`Stats().NotificationsSent`, `Held`), made again for every round, and each round is
  given 100 ms of real time at most, because the numbers the engine holds do not follow
  an adapter that sends while it is asked. Only the latest launch is asked, so a process
  that is hung or abandoned never makes the fixture wait for ever once the host has
  started another. An instance whose latest process has ended is still asked, and its connection's counts
  stay where the process left them, so the fixture's quiet does not return while any
  instance's adapter is away: a test that keeps one away waits with the engine's own
  `Quiesce` until the host has started another.
- **With a fake runtime, quiet also means no turn of it has actions in progress.** The
  fixture adds a condition for the runtime of each instance made with
  `AdapterOptions.Runtime`: `fakeruntime.Runtime.Activity` says it is idle (no turn has
  actions running or waiting to run) and counts how often a turn's actions began or
  finished. What a turn does after it started (a receive through the engine, the report
  of what the model read, its end when its plan says `EndAfter`) runs in a goroutine of
  the runtime that neither the adapter's counts nor the engine's show while it is on its
  way, so a quiet without this condition returns in the gap before the turn's next call
  and the run that follows depends on the speed of that goroutine. A test that holds such
  a turn on purpose cannot wait for quiet until it lets the turn go.
- **The instances of a first boot are taken on in order.** The adapter host starts every
  instance at once and their admissions reach the engine in whichever order the processes
  answer, and the engine draws the identifiers and tokens of each admission from one
  random source, so one seed gave each instance other identifiers from run to run, and
  with them another order of the endpoints a model run picks from. `LauncherConfig.Order`
  (the fixture passes the instances in the order it lists them) makes a launch wait until
  the latest process of the instance before it has been sent `adapter.probe`, which comes
  after the engine has admitted the instance and drawn what it needs. A restart starts the
  instances at once, since the latest process of the one before is then the previous run's.
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
- **A starting generation owns its disk before recovery writes.** `TryStart` allocates
  the generation and its cutoff before transaction recovery, then runs startup on the
  caller's goroutine until serving or cleanup completes. The generation number is
  consumed even if recovery fails before an engine exists. The node remains down to
  agents until the complete start succeeds. `New`, `Start` and `StartWith` use this
  implementation and still fail the test on an unexpected startup error; the public
  mount helpers still return only after transaction recovery succeeds.
- **Startup crash selection does no cleanup in a program callback.** `CrashAt` is
  serialized across program goroutines and accepts at most one decision per attempt.
  It must only select a point and mode: it must not block, call `Kill`, or reclaim
  resources. The fixture marks the generation abandoned, cuts it off, installs the
  `Clone` or seeded `CrashClone` snapshot and writes one `fixture.crash` before
  cancelling startup waits. The callback then returns normally, so the production
  filesystem error unwinds its transactions. Startup checks the cutoff between stages
  and before serving, including at a commit with no subsequent filesystem call.
  The outer call aborts any host, cuts adapter pipes and waits for the engine to stop
  before returning; a cleanup timeout is an error and prevents another start.
- **Controlled startup distinguishes a crash from a failure.** `TryStart` returns
  `(false, nil)` when serving, `(true, nil)` for an accepted crash with successful
  cleanup, `(false, err)` for an ordinary error or cancellation, and `(true, err)` if
  taking the selected crash or cleaning up failed. A selector that never matches may
  still return normal success; the caller asserts that its target was reached.
  Ordinary failures retain the current disk without rolling back writes, cut off
  the unpublished generation and reclaim its resources. The caller's context bounds
  startup cooperatively, not the lifetime of a serving node; synchronous transaction
  recovery ends at its next stage boundary, and a held custom observer must still be
  released by its test. `CrashAt` is disabled when the attempt ends, while the original
  `Options.Observer` continues to see all program steps, including cleanup steps.
  It receives the complete `fsx.Observation`, with the actual path operands and
  the declaration's kind, during serving and startup alike; `CrashAt` still
  selects by `observation.Point` and kind.
- **A seed is a disk.** The power-loss source is `rand.NewPCG(seed, 0x5ce)`: the same
  seed on the same history leaves the same disk.
- **Faults go through the file layer.** `Faults()` is the `fsxtest.Faulty` the running
  process writes through (`fsxtest.DiskFull` is the full-disk error); a rule that never
  fires fails the test. A new process has a new wrapper with no rules.
- **No test-only session registration.** The fixture has no method that registers a
  session endpoint; sessions come from a session creation, which the agent of a direct
  endpoint asks for with `Control` and the runtime endpoint (`RuntimeAddr`) answers.
- **No stand-in for what is missing.** `Adapter` (without `Options.Adapter`) and
  `Runtime` (without `AdapterOptions.Runtime`) return an error that is `scenario.ErrNotAvailable`; a script that needs them
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

The loopback with its knobs, the real file system, the free-space stand-in, the adapter host and
the idle condition of the fake runtime are in. Each later part adds to this package the capability
its stream needs.

## Tests

`walk_startup_test.go` and its internal tests check real Init and Start targets,
completed-marker retries, preservation, layout repair across three generations,
cutoff snapshots, old callbacks, deterministic recordings and read-only oracles.
These are representative backend contracts, not the complete transaction coverage
suite.

`startup_test.go` checks cutoff snapshots, generation seeds and unpublished starts
across transaction recovery, scan recovery and delivery recovery, including commit
points, host cleanup, ordinary errors and cooperative cancellation.

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
`TestTheAdapterOfTheFixtureIsTakenOnThroughTheHost`,
`TestAnExtraInstanceHasItsOwnAdapterRuntimeAndEndpoints`;
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
