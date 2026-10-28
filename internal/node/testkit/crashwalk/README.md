# crashwalk

The core of the crash-point walk: which points to kill at, how a rerun is held to
its recording, how big a scenario may grow, which writes must have been reached,
and how the recovery of a crash is itself crashed. Read
[`../README.md`](../README.md) first for where this package sits.

It is independent of how a scenario runs. The system under test is behind a
`Backend`: the engine in process on a crashable in-memory file system, or the built
binary killed from outside. Neither backend is here. The package imports the
transaction layer's declarations (`txn.Table`) and the crash point types of the file
layer (`fsx.Point`) and runs nothing; it reads no clock, so whoever calls it times
the runs through `Options.Observe`.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Walk(ctx, backend, table, scenarios, options)` | The whole walk: a recording per scenario, the planned runs that belong to the shard, the recovery cases, the checks below. Returns a `Report` of what it ran. |
| `Backend`, `RunPlan`, `RunResult`, `Target`, `Trigger` | The interface a backend implements and what it is asked and answers. |
| `Scenario` | What the walk needs of a script: its name, its step names, its budget and its recovery cases. |
| `Plan`, `PlanRecovery` | The targets of a mode, in the canonical order that sharding numbers by. |
| `CheckBudget`, `CompareRuns`, `Assess`, `CheckRecoveryCases` | The individual checks, usable alone. |
| `Mode`, `Shard`, `ParseMode`, `ParseShard`, `EnvMode`, `EnvShard` | `full` or `smoke`, and the part of the walk a CI job takes. |
| `Hit`, `NewHit`, `NormalizePath`, `IsSync` | A reached crash point, the shared conversion from an observation to normalized path fingerprints, and the sync steps. |

## The rules

- **A crash point is a step and which time it was reached**, `tx/role.step#n`
  (`fsx.Point`): a transaction, the role of the file in it, the step, and the
  ordinal the transaction layer counted. The same names serve the in-process walk
  and the binary walk, because the counting is done once, in the layer that owns
  the steps.
- **An observation becomes a fingerprint in one place.** `NewHit(gen,
  observation, setup)` preserves the observation's point and the backend's
  generation and setup window, and normalizes both `Path` and `From`. A
  `CreateTemp` observation carries its parent before any file was created;
  `NewHit` gives it the fingerprint `parent/.tmp-*` (`.tmp-*` at the root).
  A rename's `Path` is its target and `From` its source. File writes and syncs
  already carry their opened name. Only logical commits have empty operands;
  a root directory step has `Path="."`.
- **The setup is not in the window.** What a scenario writes before its first step
  is covered by the startup scenario; repeating it in every scenario would only
  repeat it.
- **Full** takes every point of the window except the sync steps, a kill after
  every script step, and every recovery case. A killed process leaves its page
  cache, so a kill before a sync and a kill after the step before it leave the same
  disk; the power-loss model on the in-memory file system is what a sync is for.
  The commit point is a point: nothing has been acknowledged yet.
- **Smoke** takes one point per transaction: the first time it reaches the
  visible step (rename, remove, make directory) of the last file it writes before
  its commit, or, for an appended file, its first append. That is the point at
  which the transaction is most nearly done and not done. It takes the first case
  of each recovery transaction.
- **No point is skipped silently.** A target that the run never reached fails the
  walk and says which. A scenario whose window has more points than its budget
  fails, so that growth is put in front of a reviewer instead of being sampled
  away.
- **A rerun is compared with its recording per pair of transaction and role.** The
  rerun must end at its target, and each pair must have written the same steps in
  the same order with the same path operands, up to it. Both ends of a rename
  must agree: the same target with a different source is a different operation.
  The order across pairs is not compared: a write outside the loop may interleave with one inside it, which
  changes no pair's order and no file a point names. A rerun that differs is not
  deterministic, and a crash at point n of a run that is not the recorded run proves
  nothing about the recording.
- **A clean run reaches no recovery write.** A clean state has nothing to
  recover; if the clean run hits one, the code writes where it should not or the
  declaration is wrong.
- **Every write is declared.** A hit that no declaration of the table lists ends the
  walk at once. Every declared write of a normal transaction must be reached by
  some recording, whatever the shard, or the walk fails: a declared write that
  nothing exercises is a hole in the crash tests.
- **Recovery is crashed too.** Each case names a recovery write and a trigger that
  makes the system need it: a crash at a point, or damage to a file while the system
  is stopped. The walk brings the system to that state, restarts it, kills it at the
  first time it reaches the write, restarts it again and has the backend drain and
  check. The cases of all scenarios together must be exactly the recovery writes
  that are not sync steps, each once; that is checked before anything runs.
- **Sharding partitions the runs.** Runs are numbered across scenarios and kinds,
  and a shard takes those whose number is its residue. The number follows an order
  that no shard's own recording can change: the scenarios by name; within a
  scenario the main crash points sorted by key and then by hit number, then the
  step boundaries in the order of the steps, then the recovery cases sorted by the
  write they name. Writes of different transactions may interleave differently in
  two recordings (the comparison of a rerun allows it), so numbering by the order of
  a shard's own recording would run one target twice and another never. Every shard
  records every scenario. Two scenarios must not share a name.
- **Recovery cases are chosen over all scenarios.** In smoke the first listed case
  of each recovery transaction is run once, however the scenarios share the writes
  of that transaction.
- **A walk reports what it reached.** `Report.Normal` and `Report.Recovery` count the
  declared writes of each kind and how many the recordings (normal) or the
  restarted generations (recovery) reached, apart because different runs reach
  them. In a full walk `Report.Triggers` says which main crash point made the
  restart write which recovery writes, which is what the author of a scenario
  needs to fill its recovery cases.

## What the caller must guarantee

- A backend answers one `Reached` per target, ends a killed generation at its
  target, converts complete observations through `NewHit`, and reports an invariant
  that failed as a sentence in `Violations`.
- A backend counts crash points with the same counter as the transaction layer
  (`fsx.Counter` through `fsx.Observe`), so that the ordinal means the same in the
  recording and in a rerun.
- The scenarios' budgets are set from a recording, with room, and raised on purpose.

## Tests

`TestPlanFullTakesEveryWindowPointExceptTheSyncSteps`,
`TestPlanSmokeTakesOnePointPerTransactionBeforeItsLastFileRole`,
`TestShardsPartitionTheTargets`, `TestPlanOrderDoesNotDependOnTheInterleavingOfTransactions`,
`TestWalkShardsWhoseRecordingsInterleaveDifferentlyRunEveryTargetOnce`,
`TestWalkNumbersRunsByScenarioNameNotByTheOrderTheyAreListed`,
`TestWalkSmokeCrashesARecoveryTransactionOnceWhateverScenarioListsItsWrites`,
`TestWalkFullReportsWhichMainCrashPointMadeWhichRecoveryWrites`,
`TestWalkReportsCoverageOfNormalAndRecoveryPathsApart`, `TestCompareRuns`, `TestCheckBudget`,
`TestAssessCoverage`, `TestCheckRecoveryCasesUnion`,
`TestNewHitConvertsObservationOperandsToStableFingerprints`,
`TestCompareRunsRejectsAChangedRenameSource`, and the `TestWalk…` tests
(`…FailsWhenATargetIsNotReached`, `…FailsWhenARerunDivergesFromTheRecording`,
`…FailsWhenACleanRunWritesARecoveryPath`, `…FailsOnAWriteNoDeclarationLists`,
`…FailsWhenADeclaredNormalWriteIsNeverReached`, `…FailsWhenTheScenarioOutgrowsItsBudget`,
`…SecondLayerCrashesTheRecoveryAfterTheMainCrash`, `…ShardsRunDisjointTargetsAndTogetherAllOfThem`).
The backends are tested with the walk they serve.
