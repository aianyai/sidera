# In-process acceptance

Read the parent [README](../README.md) first. `bindCatalog` converts a fresh recipe
catalog into `harness.WalkScenario`, `harness.StartupWalkScenario` and
`harness.TerminalWalkScenario` values. `freshScenario` selects a name from another fresh catalog before each run.
Constructing a catalog allocates no fixture.

`RecordCatalog(t testing.TB) (CatalogEvidence, error)` records the complete
catalog directly and sequentially through `harness.NewWalkBackend(t).Run`.
It returns only clean hits by recipe name and complete recovery records; no
fixture, engine or filesystem handle escapes. Collection and evidence validation
return errors; existing harness failures may still terminate the owning test.
Recipe names must be unique, and each catalog entry is recorded exactly once.
A normal recording must have one generation, no violations, no undeclared writes and no
Recovery writes, and stay within its fixed budget. Coverage diagnostics compare
actual hits, including real setup, with the complete daemon declaration table.
The combined clean and complete second-generation evidence must leave no missing
Normal or Recovery declaration and no undeclared write; the declaration table,
rather than fixed counts, determines completeness.

`TestSendJudgementRejectsAnAcceptedBodyThatWasNotRead` removes the real receive
and acknowledgement drain and requires the domain judgement to reject the run.

`bindingsWithRecovery` injects the fixture's actual fixed `harness.NodeID` once
through `Bindings.LocalNode`; evidence resolves it on its first receive and
retains only the value across restarts. Direct agents need no `Me` for notice
validation.

`newSessionOps` binds session callbacks to the current fixture process. Its
checkpoint callback uses the adapter Controller and the real `checkpoint.put`
protocol rather than submitting directly to the engine.
`checkClosedSession` obtains the current record scan and delegates the read-only
closed-endpoint, cleared-token and absent-checkpoint audit to
`acceptance/disk.CheckClosedSession`. Session recipes run with the real adapter
host and its fake native runtime; callbacks do not cache an engine across restarts.

Recovery evidence is recorded once per recipe and complete trigger, deduplicating
static responsibilities together with extra recipe recovery triggers. The collector
uses `crashwalk.CheckRecoveryRecord` to establish complete two-generation
evidence, empty target results, domain invariants and the crash trigger's
first-generation prefix, then supplies all clean hits
and the complete second generations to `crashwalk.AnalyzeSources`. That shared
function owns generation validation, the three source columns and stable actual
recovery-only Normal targets. Both static and derived interruptions use
`crashwalk.CheckRecoveryTarget` for Reached, exact cutoff, generation count,
violations and path-prefix comparison. The acceptance runner keeps no second
source-classification or target-verification algorithm. Interrupted recovery runs
also use `crashwalk.CheckRecoveryTrigger` to compare their first generation with
the clean recording; offline damage has no crash cutoff to compare.

Offline damage reads the stopped disk directly and changes only records the
product wrote. The stopped creating endpoint is checked before its real control
record is damaged. Final recovery work uses the owners' existing predicates, and
closed-session checks share the session binding's audit.
Installation uses `Options.OpenPackage` to build a fresh read-only package reader
from each recipe's package specification. Only package input files are prepared;
the engine owns staging, validation, installation and cleanup. The binding injects
ordinary current-pointer failures and a version-cleanup failure through the current
process's existing `Faults` wrapper. Result lookup, current-pointer decoding and
version-directory inspection through `acceptance/disk.InspectInstalledPackage`
hold the installation result to its durable effect.

`TestCatalogEvidence` calls `RecordCatalog` once, then reuses its evidence for
all owned recovery interruptions and the derived Normal interruptions in one
sequence. The collector validates complete-table coverage before returning; it
does not execute second-generation interruptions. Collection logs the first real
observation of each key with scenario, trigger, generation, point and both path
operands. The three coverage
columns remain separate, and run totals include the derived interruptions. The
cost is one clean run per catalog entry, one complete run per unique trigger,
one interruption per static Recovery responsibility and one per derived Normal
target. It calls `Backend.Run` directly; it does not execute the full main-point
walk.

`newManagementOps` binds each named action to its public engine operation.
Every call resolves its process once, executes the request, reads the persistent
result and resends the exact request. No engine handle survives a callback.
The warning callback enables the installed disabled `warning-probe` instance with
one deterministic request, waits for the current host's real probe and passes all
actual owner-alert records to the shared `RequireVersionAlert` judgement.
`recipeOptions` supplies only declarative per-instance input: the runtime reports
`1.0.0` while the installed manifest tests `2.0.0` through `2.9.9`. Harness owns
installation and initial disabling; neither catalog construction nor the binding
starts a preparation fixture. No arbitrary alert is raised through the engine.
Read-only inspections decode configuration and clock files or reduce a record
scan to the requested endpoint, instance or alert facts. They do not write state.
The instance preparation uses the existing extra-instance fixture and real
registration, send and held session-create calls; its sender remains enabled so
failure feedback is received and acknowledged through the ordinary agent API.
Additional recovery-record sources retain their domain checks and receive the
same final owner-predicate audit as recovery recipes. The instance-disable
recording therefore proves the pending records converge as well as the visible
management result and feedback.
`TestInstanceDisableJudgesTheActualCreationOutcome` retains the normal unknown
result and real pre-disable recovery creation. Its evidence counterexamples remove
the unknown notice, public creation reply or native creation, and mismatch the
creation attempt or returned endpoint; each must fail the shared judgement.

`configureLifecycleStartup` binds the periodic-clock recipe's final read-only
clock-record audit. It checks the daemon's persisted state after shutdown; no
recipe calls the clock writer or reproduces the daemon's periodic task.

`newDeliveryOps` arms the current process's existing filesystem-fault wrapper
at the first source-update create, before any visible source-record replacement.
The recipe checks the sender's real queued status, then completes receive and
reply through the ordinary Agent entry points without transport controls.

Retention recipes enable the existing periodic archive sweep on the fixture.
`newMaintenanceOps` passes the current record scan and disk reader to
`acceptance/disk.InspectRetention` for retention facts; malformed archive lines
fail the audit. The mutation that omits archive expiry must fail, and
representative real cuts before and during the sweep must still finish the bounded drain and pass its domain judgement. Pending
session sends are received and acknowledged before their source is settled and
retention begins. `TestRetentionDrainResumesInterruptedSessionOperations` retains
real committed creation and close intakes whose responses were lost. It repeats
a creation recovery after acknowledgement body removal, and an unfinished initial
read after a later close intake, requiring third-generation convergence. Its
pre-commit cut requires no native creation or body sighting, preserving the legal
unknown path.
Repeated drains preserve large clock advances and allow the
rearmed periodic task a further ordinary pass to complete dependent endpoint or
archive cleanup; the final audit still requires the endpoint to be absent.

The control-expiry preparation fails a current-process control-result create
before any visible replacement. The resulting deadline path is judged through
real Status and receive results, actual management closure, and the owner alert
for the endpoint the adapter's real create request named. The clean judgement
rejects removal of either its write failure or its owner-alert evidence. Named
boundaries include rule installation, the held request, creation and the report
deadline. If restart finishes creation before the deadline phase starts, the
original result must resolve to a real native session; the drain reads that
session and closes it through management before auditing its cleared token and
the matching native runtime's external close. A cut before the closing tick
commits can instead recover the precise `unconfirmed/ready_timeout` reply. That
case still requires its actual owner alert and no native creation; completing the
script's deadline step keeps the stricter no-reply result and exact runtime
uncertainty notice. Repeated-drain tests interrupt actual writes on each side of
that closing commit, then apply the shared third-generation recovery checker.

`acceptance/disk.Damage` changes only an intact, actually retained source body
on the stopped disk. It verifies the existing reference before flipping a byte;
`RecoveryOps.InspectDamage` delegates read-only owner-alert counting and damaged
object identities to `acceptance/disk.InspectDamage` after actual damage. The
binding obtains the current record scan; the recipe owns the judgement.
The archive variant's named preparation arms recoverable create failures in the current process's
existing fault wrapper; it does not hold transport reports or manufacture state.
The recipe remembers successful preparation across generations, so a resumed
drain never rearms old faults after startup has replayed the feedback. The
uninterrupted prepared process must still exercise both fault rules and the real
archive body removal; strict unused-rule cleanup remains in force.
A negative test moves the real periodic sweep beyond the bounded drain and
requires the domain audit to reject the retained body and records.
Body-cut regressions interrupt every main write and the complete recovery clock
recordings, retaining actual cutoffs, trace prefixes and final domain checks.
Close-response counterexamples reject invalid values and error codes after real
closure; an unanswered close must propagate its error and resend the identical
request before the final retention and closed-record audits can pass.

Partial-progress regression checks interrupt direct registration, real session
creation and declaration narrowing, a session send before
blocking, and a lifecycle send before its prompt interval. They use the existing
backend's actual step boundaries and retain every successful send's body
obligation. Recovery creation also judges the successful control's initial body
through public status, its real session target and external model evidence.
`TestCreationJudgementRequiresTheRequestedInitialBody` creates a real session,
receives and acknowledges its actual initial body, and compares matching and
different expected bodies. Only the matching body can satisfy the creation
obligation.

`TestBlockedSessionInputsSurviveAdapterRestarts` interrupts the real
`blocked/attempts.rename#1`, records the replacement's blocking drain, and cuts its
actual blocked rewrite again. The shared recovery checker requires both actual
interruptions, the matching second-generation prefix and a complete third
generation under the recipe's unchanged body and feedback judgement.
`TestBlockedSessionClearedInputsStayClearedAfterRestart` kills after the successful
`restore-credentials` boundary. Only its drain suppresses repeated empty input
declarations, so successful delivery cannot depend on clearing stale inputs again.

`TestFailedCreationRecipesPreserveRefusalAcrossRestart` runs the complete shared
adapter-refusal recipe cleanly and at `control-intake/derived.rename#1`, then
compares the actual interrupted trace prefix. The unknown-preset variant runs
cleanly and after the create boundary. Both retain their fixed budgets, real
feedback, failed control reply and no-native-creation judgement.

Deadline boundary regressions cover a returned absent-node send and creation both
before and after their deadline steps. A creation completed by restart before its
deadline phase owes its actual native session and original initial body through
`AcceptedCreation`; an already-started timeout phase keeps its strict reply and
feedback requirements. `TestRecipeDrainPreservesClockProgressAfterAnotherCrash`
records complete second generations, interrupts representative actual body,
feedback and retention writes, and uses the shared recovery-target checker to
require exact cutoffs, matching prefixes and final third-generation convergence.

`recipeOptions` reads each recipe's `Needs`; script names never choose fixture
behavior. The binding explicitly supplies runtime adapters, extra instances,
sweep periods and package readers, and rejects unsupported capabilities before
execution. Startup binding supports the existing offline layout damage only.
Filesystem fault callbacks remain on the in-process binding; binary capabilities
provide none.

`reportLayerEvidence` uses the same complete clean and recovery recordings to
report both execution layers. It verifies the reachable/in-process-only partition
of the full table. The shared catalog analysis computes keys and actual derived
target sources from declarations; the binding keeps no handwritten exclusion list.

Only an external binary acceptance test's orchestration may consume the pure
`CatalogEvidence` value with `Catalog.AnalyzeLayer` to define coverage duties.
Binary fixtures, backends, environments and domain bindings must not import this
binding or the harness, retain their handles, or count these observations as
binary execution evidence. Each test binary collects fresh evidence from its
current source; there is no evidence file format or cache.

The feedback counterexamples retain the real message, recipient, header and
non-system-source checks and also reject another node's system endpoint. Their
receive wrapper refuses `Me`; repeated drains require exactly one binding identity
resolution, so a direct endpoint never acquires a fabricated Agent identity.

`bindTerminal` maps the harness's actual lifecycle facts to the shared terminal
recipe judgement. Ordinary management callbacks no longer contain stop or
retirement. Terminal requests use real daemon startup and the management HTTP
interface, not an engine-only operation that pretends service continues. The
collector records these explicit catalog entries under the same name, budget,
full-table and source checks as other entries. Full terminal regressions cut
every writing point and compare the actual first-generation cutoff and path
prefix, retain the real recovery generation, and reject invented responses at
irreversible retirement cuts with no durable operation result.

`TestClockClearInterruptedResultKeepsTheDurableClock` retains the clock record's
create, write and rename interruptions through the maintenance recipe's original
result/effect judgement. `TestManagementMaintenanceUsesRealVersionWarning` walks
every main point of that recipe, including immediate pause and post-clear clock
persistence, and compares the actual interrupted prefix with the clean recording.

`feedback_drain_test.go` runs actual recipes through `WalkBackend` and checks
selected second-generation clock, receive and acknowledgement writes with the
shared recovery-target checker. Its timing wrapper delegates to the real
fixture and can delay `Advance`'s return until its actual clock write is cut.
Final logical time, the original feedback/body judgement and the real closed-record
audit detect repeated waits or skipped receipt. A creation interrupted after
its returned control proves that resolution and post-receive phases each retain
their own two minutes. The instance regression interrupts every actual non-sync
clock write in its complete recovery record, retaining the original strict
feedback and final-instance checks.

`BenchmarkTransactionWrites` and its private owner fixtures are test-only real
filesystem measurements of all 53 declarations across the six owners. The runner
enumerates the complete daemon declaration table through the six owners, so a new
declaration immediately needs a nonempty
successful fixture in the unfiltered suite. Unknown and duplicate fixture names
fail at entry, and missing fixtures fail their named declaration sub-benchmarks.
Go's ordinary sub-benchmark selector can run a development subset without claiming
complete-table coverage. Fixtures separate preparation, owner invocation, read-back
verification and cleanup; only invocation arms the one concurrency-safe private
observer. It measures the first filesystem step to logical commit, reports
`ns/write-window` and `commits/op`, and suppresses the unrelated default trigger
latency. The [storage reference](../../../../../docs/references/storage/README.md#transaction-write-window-benchmarks)
defines the boundary and command. These timings are independent of crash-walk
coverage and never use its memory filesystem. Low-level preparation uses the
existing `harness.MakeLayout` and `SeedNode` on a private real disk; the measured
operation always calls its existing owner entry point.

Initial creation settlement adds notification and attempts writes to both
`control-result` and `ctl-effect-replay`. The catalog's complete recordings cover
493 Normal and 67 Recovery keys. The cut before `control-result/in.create#1`
supplies six additional static recovery interruptions, with the existing cases
retained: 43 static targets and 16 unique complete recovery recordings. The
blocked recipe's explicit prompt-retry interval makes an ordinary successful
notification observable before its direct receive, independently of creation.
Binary-capable sources cover 545 keys and leave the same 15 in-process-only
keys; dynamic recovery-normal targets remain 9 here and 8 for those supported
sources. All main-point budgets and domain judgements retain their values.
