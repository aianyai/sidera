# In-process acceptance

Read the parent [README](../README.md) first. `bindCatalog` converts a fresh recipe
catalog into existing `harness.WalkScenario` and `harness.StartupWalkScenario`
values. `freshScenario` selects a name from another fresh catalog before each run.
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

`newSessionOps` binds session callbacks to the current fixture process.
`checkClosedSession` is the final read-only audit of a closed endpoint, its cleared
token and absent session checkpoint. Session recipes run with the real adapter
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
version-directory inspection hold the installation result to its durable effect.

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
Read-only inspections decode configuration and clock files or reduce a record
scan to the requested endpoint, instance or alert facts. They do not write state.
The instance preparation uses the existing extra-instance fixture and real
registration, send and held session-create calls; its sender remains enabled so
failure feedback is received and acknowledged through the ordinary agent API.
Additional recovery-record sources retain their domain checks and receive the
same final owner-predicate audit as recovery recipes. The instance-disable
recording therefore proves the pending records converge as well as the visible
management result and feedback.

`configureLifecycleStartup` binds the periodic-clock recipe's final read-only
clock-record audit. It checks the daemon's persisted state after shutdown; no
recipe calls the clock writer or reproduces the daemon's periodic task.

`newDeliveryOps` arms the current process's existing filesystem-fault wrapper
at the first source-update create, before any visible source-record replacement.
The recipe checks the sender's real queued status, then completes receive and
reply through the ordinary Agent entry points without transport controls.

Retention recipes enable the existing periodic archive sweep on the fixture.
`newMaintenanceOps` reduces real records and parsed archive files to the requested
retention facts; malformed archive lines fail the audit. The mutation that omits
archive expiry must fail, and representative real cuts before and during the
sweep must still finish the bounded drain and pass its domain judgement. Pending
session sends are received and acknowledged before their source is settled and
retention begins. Repeated drains preserve large clock advances and allow the
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
A negative test moves the real periodic sweep beyond the bounded drain and
requires the domain audit to reject the retained body and records.

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
