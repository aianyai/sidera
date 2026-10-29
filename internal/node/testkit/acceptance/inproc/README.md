# In-process acceptance

Read the parent [README](../README.md) first. `bindCatalog` converts a fresh recipe
catalog into existing `harness.WalkScenario` and `harness.StartupWalkScenario`
values. `freshScenario` selects a name from another fresh catalog before each run.
Constructing a catalog allocates no fixture.

Tests call `harness.NewWalkBackend(t).Run` directly and sequentially. A normal
recording must have one generation, no violations, no undeclared writes and no
Recovery writes, and stay within its fixed budget. Coverage diagnostics compare
actual hits, including real setup, with the complete daemon declaration table.
The combined clean and complete second-generation evidence must leave no missing
Normal or Recovery declaration and no undeclared write; the declaration table,
rather than fixed counts, determines completeness.

`TestSendJudgementRejectsAnAcceptedBodyThatWasNotRead` removes the real receive
and acknowledgement drain and requires the domain judgement to reject the run.

`newSessionOps` binds session callbacks to the current fixture process.
`checkClosedSession` is the final read-only audit of a closed endpoint, its cleared
token and absent session checkpoint. Session recipes run with the real adapter
host and its fake native runtime; callbacks do not cache an engine across restarts.

Recovery evidence is recorded once per recipe and complete trigger. The finite
runner establishes completion and domain invariants, then supplies all clean hits
and the complete second generations to `crashwalk.AnalyzeSources`. That shared
function owns generation validation, the three source columns and stable actual
recovery-only Normal targets. Both static and derived interruptions use
`crashwalk.CheckRecoveryTarget` for Reached, exact cutoff, generation count,
violations and path-prefix comparison. The acceptance runner keeps no second
source-classification or target-verification algorithm.

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

`TestCatalogEvidence` runs one clean recording per catalog entry, the deduplicated
complete recoveries, all owned recovery interruptions and the derived Normal
interruptions in one sequence. It logs the first real observation of each key with
scenario, trigger, generation, point and both path operands. The three coverage
columns remain separate, and run totals include the derived interruptions. The
cost is one clean run per catalog entry, one complete run per unique trigger,
one interruption per static Recovery responsibility and one per derived Normal
target. It calls `Backend.Run` directly; it does not execute the full main-point
walk.
`newSessionOps` binds session callbacks to the current fixture process.
`checkClosedSession` is the final read-only audit of a closed endpoint, its cleared
token and absent session checkpoint. Session recipes run with the real adapter
host and its fake native runtime; callbacks do not cache an engine across restarts.

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
sweep must still finish the bounded drain and pass its domain judgement.

The control-expiry preparation fails a current-process control-result create
before any visible replacement. The resulting deadline path is judged through
real Status and receive results, actual management closure, and the owner alert
for the endpoint the adapter's real create request named.

`configureBodyRecoveryScenario` damages only an intact, actually retained source
body on the stopped disk. It verifies the existing reference before flipping a
byte and later requires that precise body's owner alert. The archive variant's
named preparation arms recoverable create failures in the current process's
existing fault wrapper; it does not hold transport reports or manufacture state.
A negative test moves the real periodic sweep beyond the bounded drain and
requires the domain audit to reject the retained body and records.

Partial-progress regression checks interrupt registration, a session send before
blocking, and a lifecycle send before its prompt interval. They use the existing
backend's actual step boundaries and retain every successful send's body
obligation. Recovery creation also judges the successful control's initial body
through public status, its real session target and external model evidence.
`TestCreationJudgementRequiresTheRequestedInitialBody` creates a real session,
receives and acknowledges its actual initial body, and compares matching and
different expected bodies. Only the matching body can satisfy the creation
obligation.
