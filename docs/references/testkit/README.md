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
| `crashwalk` | The core of the crash-point walk: which points to kill at in each mode, how a rerun is held to its recording, scenario budgets, coverage of the declared writes, and the crashing of recovery itself. |

The first four, with the two programs, are the **third-party side**: they import the
standard library, the SDK module and each other and nothing else. `fakeagent` and
`crashwalk` are the **Sidera side** and may import any runtime or contract package.

The engine-layer fixture, the backends of the fake agent and the binary-layer fixture
build on these packages and are not part of them.

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
  killed on purpose.
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
- **Rejected: a text language for scenarios, an assertion library, a goroutine-leak
  library.** Scenarios are Go code so the reader reads what runs and the compiler checks
  names; the checks are a few lines of the standard library; the leak check is a scan of
  goroutine stacks for the packages' own frames.

## Where

| Part | Code | Tests |
| ---- | ---- | ----- |
| Import rule | `internal/node/testkit/imports_test.go` | `TestThirdPartySideImportsOnlyStandardLibraryAndSDK`, `TestImportRuleDetectsViolations` |
| Ledger | `internal/node/testkit/ledger` | `TestGoldenEntryVectors`, `TestDecodeRejects`, `TestEncodeRefusesInvalidEntries`, `TestKindTableCoversEveryKind`, `TestWriterStampsEveryEntry`, `TestConcurrentWritersKeepEachSequence`, `TestDirWritesEachEntryBeforeAppendReturns`, `TestReadDirRejects`, `TestReadDirToleratesTheLastLineOfAKilledProcess`, `TestDigestIsSHA256InHex`, `TestLogPIDsListsEveryProcessThatWrote`, `TestDirLedgerOrdersEntriesByTimeThenScriptStep`, `FuzzDecode`, `FuzzParseStream` |
| Control channel | `internal/node/testkit/fakectl` | `TestGoldenParams`, `TestDecodeParamsRejects`, `FuzzDecodeParams`, `TestClockAndStepReachTheClientBeforeTheCallReturns`, `TestKnobCallsTheClientsHandler`, `TestRequestsBeforeHelloAreRefused`, `TestClientEndsWhenTheControlConnectionDrops`, `TestCloseEndsEveryGoroutine` |
| Runtime | `internal/node/testkit/fakeruntime`, `cmd/sidera-fake-runtime` | `TestTurnsEndOnlyWhenTheScriptSaysSoAndTimeNeverEndsOne`, `TestPromptDuringATurnIsRefusedAsBusy`, `TestPromptIsDeliveredAtMostOncePerAttempt`, `TestRecvActionRecordsWhatTheModelSawAndReportsTheToolResult`, `TestKilledNativeStopsReportingAndLeavesTheRecordReadable`, `TestAnExecActionThatNamesAFrontendIsAReceive`, `TestReconcileReadsTheRecord`, `TestGoldenRecordLines`, `TestDecodeEventRejects`, `FuzzDecodeEvent`, `TestProcessSessionIsReachableAndDrivenOverTheControlChannel`, `TestARestartedAdapterTakesTheLiveSessionBack`, `TestAKilledNativeProcessIsGoneAndItsRecordIsReadable`, `TestGoldenLinkMessages`, `FuzzDecodeLink` |
| Adapter | `internal/node/testkit/fakeadapter`, `cmd/sidera-fake-adapter` | the consistency test `TestFakeAdapterOutputMatchesProtocolSchemas` with `TestFakeAdapterDeclarationIsAdmittedByTheDaemonRules` and `TestFakeAdapterAnswersEveryGoldenRequest`; `TestInstanceLockIsTakenBeforeInitializeIsAnswered`, `TestCreateIsIdempotentPerAttemptAndConflictsOnOtherParameters`, `TestCreateOutcomes`, `TestPauseAtCreateAfterNativeStopsBeforeTheAnswer`, `TestPromptOutcomes`, `TestReconcileAnswersFromTheRecord`, `TestStartReattachesWhatItCan`, `TestTimesTheAdapterReportsComeFromTheTestClock`, `TestObservedModes`, `TestHungAdapterAnswersNothingAndKeepsItsLock`, `TestHangLeavesAHit`, `TestPipeProcessIsAProcessTheAdapterHostCanSupervise`, `TestProcessAdapterDrivesARuntimeProcessAndTakesItBackAfterARestart`, `TestProcessAdapterKnobsOverTheControlChannel`, `FuzzDecodeKnobArguments`, `FuzzDecodeProcessConfig` |
| Fake agent | `internal/node/testkit/fakeagent` | `TestErrorCategories`, `TestRecordingWritesTheCallBeforeTheActionAndTheResultAfter`, `TestRecordingCanRecordWhatTheModelSawOutsideATurn` |
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
- The scenario scripts, the fixtures of the two layers and the backends of the fake agent
  and of the crash walk are not in this tree yet.
