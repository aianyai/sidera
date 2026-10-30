# Acceptance support

Support for exercising the built program through its public front ends.
[`agent/cli`](agent/cli/README.md) implements the layer-neutral fake Agent by
starting the actual CLI. [`agent/mcp`](agent/mcp/README.md) drives a persistent
MCP subprocess through its stdin/stdout pipes, negotiating either supported era.
The fixture starts and waits for that real process; it never calls the MCP
server in process for binary acceptance. These drivers are test tooling and are not imported by
the executable.

Give a driver the built binary, a data directory, a project working directory,
and the endpoint or probe credential issued by the node. Session endpoints come
from `control ... session.create`, never from a test-only registration path.
Callers own their process context deadlines and any ledger recording wrapper.

## Foreground-process fixture

The root package owns a real foreground `sidera` process and a control listener
that survives its generations. `NewFixture` takes prebuilt tagged and plain node
programs, the two fake programs, an absolute short root directory, a stable seed
and a logical clock origin. The caller owns that directory and removes it after
`Close`. The fixture never builds a program or calls the daemon or engine in
process. `daemon.Table` supplies declarations only.

`Init` runs the short initialization command and waits for it. `Start` starts a
new foreground daemon, waits for public status to report `running`, checks the
complete tagged crash-point table, and waits for enabled-instance probes and
quiet. A tagged ping alone may answer while recovery is still running. Its first start
installs the caller-owned fake package, enables the `fake` instance and adds the
`fixture` preset through management. Bootstrap retains each complete request,
including its operation ID and expected version, before sending it. A later
generation resumes at the first unanswered stage with that same request, so a
committed change replays its stored result; completed stages are not repeated.
Later starts reuse the installed package; only the separate input configuration's
clock origin is updated. The default
adapter provides direct endpoints. A private fixture profile supplies the catalog
instance and preset names, runtime declarations, extra instances, and an installed
warning package that remains disabled until a recipe enables it. Runtime profiles declare only the CLI front end, with its actual wait limit and
budget; they make no MCP coverage claim. Their fake adapters execute the actual
`sidera probe --json` command for every requested preset, including init's default,
using only that request's environment. Each generation must present its newly
issued tokens before `Start` succeeds. Static process inputs contain only command
paths and public arguments. Runtime processes
use the actual fake-runtime program. The tagged daemon consumes and clears its
crash controls before launching adapters, so descendant CLI processes do not
inherit the daemon generation's target or record file.

`Manage`, `Adapter` and `Runtime` expose the existing external clients. The
fixture owns one logical `Now` across daemon generations. `SetTime` retains the
requested target before sending it to the daemon, even if the RPC fails: a
daemon may have changed its clock before losing its response, and the next
generation starts at the retained target. An RPC failure remains an error and
does not synchronize the fakes. After a successful daemon RPC, `SetTime` waits
for every connected fake's clock update. `BeginStep` records a unique step and
synchronizes connected fakes. Handles and quiet checks synchronize connections
again before returning. No runtime session is created by this base fixture;
the fake runtime currently records its creation before its fixture control
connection exists, so that initial event cannot acquire a current step here.

`Quiesce` samples every enabled adapter's notification count, held-method
multiset and outstanding requests, requires the remote runtime to be idle,
submits the expectations to `test.quiesce`, and samples again. It succeeds only
when both samples agree and the daemon and fakes are satisfied. With no enabled
adapters, the request omits the optional `expect` field: the strict wire decoder
rejects an explicitly empty map. Its timeout is the scenario default, bounded by
the caller's context. Each daemon wait is capped
at 100 ms within that same deadline, so asynchronous notifications cannot leave
it waiting against stale counts for the whole budget. An unsatisfied round is
sampled again; a round timeout never counts as quiet. Diagnostic failures name
the daemon report and fake progress. Wall-clock tickers pace external readiness
queries; they never move logical time.

Every started node command gets its own stdout, stderr and append-only trace
files and exactly one `Wait` goroutine. `Stop` freezes the complete-line scenario
prefix before the public `daemon.stop` operation, then waits for actual exit.
`Kill` kills only the current daemon, waits, and records only the PID it actually
killed. Native runtimes belong to the control listener rather than a daemon
generation. `Close` stops the daemon and closes the listener and fixture ledger.

`ReadTrace` requires completed `Wait`. It validates generations, the full
declaration table's kinds, contiguous per-key ordinals and complete JSON lines;
ordinary observations go through `fsx.ParsePoint` and `crashwalk.NewHit` once.
A matching second crash-marker line is termination evidence, not another hit.
`Reached` additionally requires the actual abnormal process exit. CLI process
points are returned separately and never count toward transaction coverage.
The first `BeginStep` marks the setup prefix without resetting any ordinal.

Real-process tests and their `TestMain` require the `acceptance` build tag.
Pure trace parsing and in-memory contract tests remain ordinary package tests.
The tag selects execution only: fixture, backend and binding sources compile
without it. Tests build the four programs once per outer test process, from
that checkout.
The child builds follow the outer race build. They cover actual targeted
termination, unexpected exits, restart after kill, clock and quiet, setup
ordinals, bootstrap replay after a committed preset's response is lost, trace
rejection, and ordinary builds rejecting tagged controls while ignoring their
environment variables. Their short roots are removed after each
run. Domain recipes, offline damage and crash-walk binding are separate callers
of this process fixture.

Plain-build tests require precise `not_found/method_unknown` replies to all four
test controls while public requests through the same management client succeed.
Two independent plain initializations under one test seed must report different
public identities; invalid clock/crash controls are ignored and create no trace.
The plain business smoke reuses the fixture's input packages, public management
bootstrap and single-Wait process owner, with the plain program for daemon and
CLI. It polls public daemon status, adapter probes and the unique open session,
checks that session with CLI `me`, then receives the exact body, acknowledges it
with `completed` and requires an empty next page before normal daemon exit.
It calls no test clock or quiet controls. These are local delivery results;
the sender's cached status, refreshed by the ordinary one-minute pull interval,
is not part of this smoke's evidence.

## Scenario environment

The private `environment` implements the unchanged `scenario.Env` contract.
`envFrom` gives domain bindings access to the same current fixture. Logical
`Advance` and `Set` use `Fixture.SetTime`; because those methods return no error,
the environment retains the first clock failure and returns it from subsequent
operations. Further clock changes in that failed generation do not advance time.
Only the backend may call `resetGeneration`, after judging the prior
process's trace and actual exit and starting a distinct daemon generation.
Neither quiet nor a successful later RPC clears the error.

Normal scripts initialize through private `prepare` before observed generation 1.
It runs the actual tagged CLI with a dedicated deterministic initialization seed
and the shared logical clock, removing all three crash observer variables. The
command uses the same launch and single-Wait owner as observed commands, including
output files and cancellation cleanup, but creates no generation or trace entry.
Public `Init` remains the observed startup operation with positive unique ids.
Node process seeds include the scenario seed, role and observed generation. Native
runtime seeds include the scenario and instance, remain stable across daemon
restarts, and never depend on a process id or temporary path.

Direct agents use the current fake adapter Controller for each call, including
reconnects after a daemon restart. Registration and addresses come from real
protocol replies and public node status. The driver sends text, control and
replies through `endpoint.send`. Its successful response confirms the request's
canonical recipients in their original order; the daemon rejects aliases and
never rewrites accepted recipient addresses. No follow-up status query is needed
to complete the send result.
Reply routing and implicit acknowledgement parts come only from pages actually
received. The bridge renders each typed page for its own reader; `Raw` contains
that rendered display, without claiming to retain the daemon's JSON formatting.
The existing `fakeagent.Recording` wrapper records the bridge's calls and what
its reader saw. Direct endpoints use the product's direct receive budget even
when a shared script names the CLI reader. The session-only `Me`, `Declare`,
`Peers`, `Directory` and `Probe` methods return `ErrNotAvailable`.

The adapter send result does not report whether an idempotency key replayed a
message. The direct driver returns `ReplayUnknown` for nonempty keys and
`ReplayFresh` for an empty key, which never looks up the idempotency table.
Keyed calls reach the real protocol unchanged, including after a restart.
Missing public protocol implementations remain real call failures, and runtime
profiles must pass the same strict public probe checks as the base fixture. These support types do not
replace either boundary with in-process engine calls.

`TestDirectProtocolLifecycleAcrossGeneration` checks real unkeyed sends are
fresh on both daemon generations. `TestDirectControlReplayAcrossGenerationClosesNativeSessionOnce`
creates a real native session and repeats the unchanged keyed close in the same
generation and after an actual daemon kill, wait and replacement. Each response
must retain the original message identifier and unknown replay evidence; the
public closed reply and external ledger must show one native close and no extra
adapter close dispatch after replacement. `TestDirectSendReportsOnlyAvailableReplayEvidence`
separately checks the controller contract, including canonical recipient order,
reply and control, without a follow-up status lookup.

Package sources belong to the fixture's input directory. The private
`packageInput` helper writes an actual fake-adapter package there; an installation
binding can extend that source before invoking public installation. Installed
node packages and node state are never edited to prepare an input.

## Crash-walk backend

`NewWalkBackend(root, binaries, options)` implements `crashwalk.Backend` over the
real-process fixture. Its root must be an existing absolute short directory owned
by the caller. Each `Run` creates a distinct child, closes its fixture and waits
for its processes before removing that child. Cleanup errors are joined with the
run error; the caller's directory and other contents remain its responsibility.
The backend does not build programs or import the in-process binding, harness or
engine Agent.

`NewWalkScenario(descriptor, bind)` copies only the descriptor's name, step names,
budget, recovery cases, extra recovery triggers and needs. Every run invokes
`bind(RecoveryOps)` anew and rejects different metadata. The factory must recreate
all mutable script closures, domain evidence and drain progress. Only that run's
recipe and recovery state survive its daemon generations. After a crash, later
generations resume the drain rather than replaying ordinary steps. Final script
and domain judgements run after quiet while the daemon still serves; the ordinary
stop then freezes the trace before its own cleanup writes.

A selected transaction interruption requires the fixture's actual matching trace
marker and abnormal exit. A failed RPC alone is an error. An after-step target
requires successful `Process.Kill` and completed `Wait`, and records that exact
PID in the external ledger. Recovery runs retain the first-generation trigger,
the complete second generation, or a second-generation cut followed by a third
generation. Shared crash-walk checkers compare those actual cutoffs and paths.
Finishing an interrupted generation records `fixture.crash` with its verified
point or after-step description and actual daemon PID before any next-generation
drain. An after-step interruption also retains its `fixture.kill` fact. Natural
exit, ordinary stop for offline damage and an unproven target write no crash
event. Recipes can therefore distinguish interrupted scripts and resume their
existing recovery drains through the same ledger contract as the in-process layer.

`StartupWalkScenario{Recipe: ...}` runs actual observed CLI Init or bare daemon
Start, without script package bootstrap. Only a serving Start declares the
`complete` boundary. Init retries compare the existing protected files through
`diskfacts.InitPreservation`; final startup checks initialization and layout.
Start retains the requested startup clock offset, advances after each successful
start when requested, and checks the final periodic clock record after exit.

`TerminalWalkScenario{Recipe: ...}` performs the real stop or retirement management
request and waits for natural exit; it declares no after-return boundary. It
passes actual responses, stopped-disk results, verified revocations and restart
facts to `TerminalRecipe.Check`. A retired node must actually refuse another
start with the identity-retired diagnostic and unchanged signing files. The
extra stop replay or retirement refusal audit is kept outside walk coverage.

Offline damage is applied only after process exit through `acceptance/disk.Damage`.
The run's recovery callbacks use the current read-only store scan and the shared
`InspectDamage` and `CheckClosed` audits. They never invoke recovery or begin a
node transaction. Binary profiles provide no filesystem fault injection; an
explicit archive-sweep period must equal the executable's existing hourly period.

The private `bindCatalog` selects the shared recipe descriptions through
`BinaryCapabilities`. Each script factory captures only its name and constructs
fresh domain bindings and recipe closures for each run, using that run's backend
recovery callbacks. Startup and terminal descriptions use their existing wrappers;
extra recovery triggers come only from the selected scripts.

`TestCrashAtEveryCrashPointPreservesInvariants` is an external test selected by
the `acceptance` build tag. It collects the current in-process catalog once for
responsibility analysis, then calls `crashwalk.Walk` once with the complete binary
catalog and complete `daemon.Table`. Only the external test imports `inproc`;
the test-only bridge exposes catalog construction and a backend using the existing
single binary build pipeline. No in-process hits or targets enter that backend.
`SIDERA_CRASHWALK` and `SIDERA_CRASHWALK_SHARD` select the existing mode and shard
rules. Logs distinguish responsibility evidence, the Required/elsewhere partition,
three actual binary coverage sources, full-table and Required differences, run
counts, dynamic planned/selected/executed counts, timings and smoke skips.

Focused contract tests cover fresh binding and caller-directory preservation,
online domain checks, metadata drift, a real direct exchange's main and step
cuts, complete recovery and second/third-generation recovery cuts, interrupted
Init, offline layout repair, periodic-clock persistence, certificate renewal,
and stop/retirement exit and restart behavior. These tests complement the external
catalog walk; they do not replace a complete full-mode execution.

## Management and package bindings

`newManagementOps` and `newInstallOps` bind domain callbacks to the current
fixture's management client. Changes carry the recipe's exact operation identifier,
expected version where required, and public params. Before returning a successful
first result, the binding queries `operation.result` and repeats the same request,
requiring an unchanged result and an explicit replay. Version-conflict and
unsupported-platform refusals require `operation_unknown` for that identifier.
No engine or in-process acceptance handle enters these bindings.

Inspection reads public collection lists and actual configuration, clock and
endpoint files through read-only interfaces. The private `diskReader` checks the
environment and exposes only `fsx.Reader` for the fixture's current data directory.
Its private `diskFilesystem` opener retains the full interface only for constructing
the existing store scanner; binding audits never begin transactions, recover or
write node state. Closed endpoint token checks use the
persisted endpoint record because public lists intentionally expose no token.
Package inspection passes that reader to
`acceptance/disk.InspectInstalledPackage`, which requires the actual current
pointer and version directories; a refused installation must leave no adapter
directory.

The warning callback enables the profile's installed, disabled warning package
inside the step, retaining its original operation identifier and expected version
before the first send. After real probes and quiet, the shared recipe judgement
requires the unique `version_untested` alert for that instance. A restart resumes
the original request. Acknowledgement uses the public alert operation and list.

Installation creates real caller-owned package sources with the fake program,
optional nested data, and an explicitly foreign platform for refusal. Each binding
instance retains its completed input paths across reentry. The public installer
alone copies or changes installed package state. Filesystem pointer-fault
preparation returns an explicit unsupported error, matching binary capabilities.

Focused tests exercise settings, stale versions, presets, endpoint closure,
instance disable/enable, backup confirmation and clock clearing through the real
daemon. They also kill and replace the daemon before replaying a settings or
installation request, and cut warning enable at its real commit point before
resuming probe and acknowledgement in the next generation.

## Session and retention bindings

`runtimeAddress` selects the unique open runtime endpoint of the requested
instance from public `endpoints.list`; it never derives an endpoint identifier.
Its callback shape has no error result, so missing or ambiguous endpoints and
failed calls fail loudly. `checkpointPut` uses the current adapter Controller's
real `checkpoint.put`. `hasUnknownCreationAlert` reads `alerts.list`, including
acknowledged entries, and requires exactly one matching kind and subject.

`checkClosedSession` and `newMaintenanceOps` inspect actual node files with the
store's existing scanner. The private `readSessionRecords` checks the retained
generation error and requires a running daemon; callers quiesce before inspecting.
The temporary store only scans: it never recovers, begins a transaction or writes.
The callbacks pass that current scan and reader to
`acceptance/disk.CheckClosedSession` and `acceptance/disk.InspectRetention`. The
shared closed-session audit requires a retained closed endpoint, an empty token
and no session checkpoint. Retention counts real delivery records, body files,
archive files and parsed summaries, and rejects damaged archive lines.
`TestSessionInspectionTracksRealCloseAndRetention` creates and closes a native
session through the adapter protocol, checks its actual checkpoint and closure,
and observes the daemon's periodic archival and expiration. The retention test
kills and waits for the daemon after closing the real session, starts a new
generation, requires identical persisted retention facts and a closed-session
audit, then observes archival and expiration from that replacement.

`newDeliveryOps` and `failCreateSettlement` explicitly reject filesystem fault
injection with `scenario.ErrNotAvailable`, as declared by `BinaryCapabilities`.
They preserve a retained generation error before reporting missing capability.
`newSessionOps` composes these callbacks with a real CLI session Agent. Each
Agent call first checks the retained environment error, resolves the requested
open session and instance through public `endpoints.list`, and reads the fake
runtime's published `ProcessInfo` from that instance's caller-owned runtime
directory. The native reference, endpoint and PID must identify exactly one live
runtime control connection. The current Controller then supplies `SessionEnv`;
lookup and getter failures return before any CLI starts. This is a live process
ownership query, separate from the shared recipes' creation-reply judgement.
Credentials are never inferred or read from node records and are never cached
between calls.

The existing CLI driver starts the actual tagged program with that session's
injected tool environment. Its private proxy and recording writer survive daemon
replacement; only the endpoint's CLI call counter persists, with no native
reference or environment retained. Per-call seeds combine the scenario seed,
`session-cli` role, actual endpoint and call ordinal, in a namespace separate from
native turn commands. Counts continue across daemon generations. Each call also
receives the current logical clock and clears inherited process crash controls.
`fakeagent.Recording` records real CLI results and the pages its reader saw.

`TestBinarySessionBindingRunsSharedRecipeAcrossGeneration` executes the shared
registration and declaration recipe, then sends and receives through this binding
on both sides of an actual kill, wait and replacement. Every Agent method retains
the failed generation's original error until explicit reset. Two complete runs
with the same scenario seed must return the same actual CLI message identifiers,
despite different process IDs and directories; calls on different generations
must still have distinct identifiers. The private catalog composes these bindings
with the crash-walk backend.

`TestSessionEnvDrivesRealCLIAcrossDaemonGeneration` creates a native session
through public runtime endpoints and `session.create`, obtains its actual native
reference from the external ledger and queries `Controller.SessionEnv`. The
existing CLI Agent performs real `Me`, declaration, receive and acknowledgement
calls before and after a daemon kill, wait and replacement. It queries the runtime
again after replacement and checks ledger, runtime records, static inputs, traces
and diagnostic output for leaked session credentials. This focused regression
checks the getter and driver directly, independently of the composed session
binding.
