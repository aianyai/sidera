# Test tooling

The packages under `internal/node/testkit/` and the two programs `cmd/sidera-fake-adapter`
and `cmd/sidera-fake-runtime`. They let a test run the daemon against an adapter and a
native runtime that it did not write, judge it from a record the daemon did not make, and
walk its crash points. Each package has a README with its entry points and rules; this
document gives the reasons and says where the tests are.

## Explicit adapter inputs across generations

A test run may own `fakeadapter.ExternalInputs`: typed creation and prompt refusals
and a complete availability snapshot. All fields empty revoke the declaration. These
are external conditions, separate from process-local outcomes and observed daemon
state. Refusal takes priority without consuming queued outcomes; `ClearOutcomes`
and the one-shot `Availability` Controller operation keep their previous meanings.
`fakeruntime.ValidateCreateRefusal` owns the creation rule: empty or an SDK-valid
creation failure other than `control_unknown`. The SDK validates prompt refusals
and availability, and all nested slices are copied.
No arbitrary knob, script, credential or daemon observation is retained.

Creation refusal reaches the existing runtime `Create` owner through the optional
`CreateRequest.Refusal`. The in-process owner returns an existing session for that
attempt first; the process launcher first recovers published ProcessInfo or waits
on an existing native claim. Existing-native retries therefore still return that
native after adapter replacement, even without a checkpoint. Only a currently
absent attempt is refused, with `CreateRefusedError` and no runtime file, spawn or
ledger effect from the call. Refusal is not persisted or terminal: clearing it
allows the same attempt to create. This does not arbitrate a candidate started
before the absence check; such a candidate can still arrive afterward. Native
record formats and publication waits are unchanged. Controller `OutcomeFail`
remains a contemporary outcome with its previous semantics.

The adapter maps typed creation refusal to its exact failed reason. An ordinary
runtime `Create` error does not prove that no native session exists and returns
the protocol's internal error with unknown effect instead of a fictional
`native_error` failure. Even an underlying native protocol application's error
code cannot establish the outer creation's effect. The existing peer logs the
cause internally and sends its generic error response; cancellation remains
cancellation. An explicit `native_error` is still available through the external
input or Controller `OutcomeFail`.

`InputRegistry` fixes the run's instance names at construction, including disabled
instances, and rejects unknown names. Each instance independently serializes Set,
Attach and detach through a cancellable boundary that includes installation and
its acknowledgement. Set retains the complete desired value before applying it;
failed application propagates its error and keeps the new desired state. Attach
installs the latest declaration before publishing its receiver; failure preserves
the existing binding. Detach removes only its own binding and never desired state,
so an old process's exit cannot disconnect its replacement. Receivers cannot call
back into the registry.

The in-process fixture constructs this owner once from `AdapterOptions.Inputs`.
`LauncherConfig.Inputs` attaches after knob creation and `Arm`, before `StartPipe`;
failed attachment starts no pipe, and actual process exit detaches the binding.
The binary fixture constructs its owner once from `Options.AdapterInputs` while
preparing every installed instance. `Fixture.SetAdapterInputs(ctx, instance,
inputs)` and `H.SetAdapterInputs(instance, inputs)` replace the same declaration.
`scenario.Env` exposes only the latter operation; its binary implementation uses
the current environment context. There are no script callbacks or fixture handles
in scenario data. Initial disablement and later real enablement use the same
attachment path as every restart.

A binary `ProcessConfig.RestoreInputs` opts into the fake adapter's own
`adapter.inputs.attach` request after hello and before Serve. Params and result
are strict `{}`. The server selects the instance from the adapter-role hello,
then calls its `external-inputs` knob in reverse and waits for the `{}` install
acknowledgement inside the same boundary. The knob's params are the complete
ExternalInputs value. To preserve generic fakectl's nonempty-args rule, its remote
sender encodes a clear with explicit `availability: []`; the strict adapter-owned
decoder accepts this and standalone `{}` as equivalent clears. The generic hello,
clock and knob protocol does not change. Control closure cancels the pending
install independently of detach; failure or cancellation cannot enter Serve.
Standalone programs with RestoreInputs false and launchers with nil Inputs keep
their old behavior.

Knobs installs refusals before Serve can make a creation or prompt decision. After session
reattachment, `adapter.start` sends the currently declared availability before its
successful response. This is a legal notification window after admission; an
ordinary start with no conditions sends no extra empty snapshot. Startup and
online replacement read and send under one input lock, so new snapshots cannot
be overtaken by old ones. Every online replacement sends its complete snapshot,
including an empty one; retrying a failed clear therefore still enqueues the clear.
Successful apply proves local installation and notification enqueueing, not daemon
consumption. Existing quiet and real feedback remain responsible for convergence.
A decision already made is not cancelled retroactively. Product reconnection
still clears observed availability; these fakes re-report an external condition.

`fakeadapter/inputs_test.go` covers desired state, copies, isolation, reconnects,
failed and cancelled application, concurrent Attach/Set and stale detach.
`inputs_contract_test.go` runs the same protocol and ledger assertions in process
and against the actual fake programs. `create_inputs_test.go` does the same for
creation refusal across generations, queued outcomes, clearing the same attempt
and existing-native retries without checkpoints. The runtime's
`create_refusal_test.go` covers the rule and absence of native side effects;
`create_error_test.go` covers unknown-effect errors and cancellation around real
creation. `inputs_lifecycle_test.go` requires attach
cancellation and control closure to terminate without premature Serve or leaked
processes. The harness's disabled-instance test exercises later real enablement,
replacement and explicit clearing through its run-owned registry.

## What

| Part | Is |
| ---- | -- |
| `ledger` | An append-only record of what the fakes did: canonical JSON lines, a closed set of kinds and attributes, one file per writer and process with every entry synced, or an in-process backend. Bodies are never in it, only SHA-256 digests. |
| `fakectl` | The control channel between the test fixture and the fake processes it starts: loopback TCP, newline-delimited JSON-RPC on the SDK's peer. It carries the test clock, the name of the running script step, the knobs, and reports back. |
| `fakeruntime` | A stand-in for a native agent runtime: sessions, a session record, turns that start and end only when a script says so, in process or as a program of its own per session. |
| `fakeadapter` | An adapter that speaks the adapter protocol as a third party would, drives a fake runtime, and does what its knobs say; in process over `io.Pipe` or as a program. |
| `fakeagent` | The interface of the fake agent (send, reply, receive, acknowledge, control, declare, status), its results and errors, and a wrapper that writes the ledger entries around every call. |
| `fakeagent/engineagent` | The engine-layer backend of the fake agent: the engine's service interface called in process. Ten methods call the engine; Probe needs a credential this caller-bound backend does not hold. |
| `scenario` | The language of test scripts, the same in every layer: scripts, the driver, the environment interface a fixture implements, and the judgements that read the ledger. |
| `harness` | The engine-layer fixture: the engine on the real store, configuration and transaction layer over the in-memory file system, one test clock, the ledger, the agents, quiet, process and power-loss crashes and restarts, fault injection, the in-process crash-walk backend, and the generator of control signals for model tests. |
| `diskfacts` | Named read-only lifecycle observations shared by execution bindings: init preservation, signing-file snapshots and stopped-disk operation results and verified revocations. |
| `acceptance/agent/mcp` | The MCP backend of the fake agent: independently encoded exchanges for both supported revisions, exact receive text, and tool errors mapped to the scenario categories. It takes process pipes and imports neither the production MCP server nor an MCP SDK. |
| `acceptance/recipes`, `acceptance/disk`, `acceptance/inproc` | Fixture-independent acceptance descriptions and their in-process binding; fresh closures per run, real lifecycle entry points and externally evidenced domain checks. |
| `crashwalk` | The core of the crash-point walk: which points to kill at in each mode, how a rerun is held to its recording, scenario budgets, coverage of the declared writes, and the crashing of recovery itself. |

The first four, with the two programs, are the **third-party side**: they import the
standard library, the SDK module and each other and nothing else. `fakeagent`,
`engineagent`, `scenario`, `harness` and `crashwalk` are the **Sidera side** and may
import any runtime or contract package; `scenario` imports neither the engine
nor a fixture.

`diskfacts` is also on the Sidera side, with a narrower dependency boundary: the
standard library, `fsx`, `keys`, `config`, `store`, `admin`, `ident` and
`revocation`. It imports no engine, daemon, harness, recipe or in-process binding.
Its callers keep process lifecycle and domain judgements.

The backends of the fake agent that run the command-line client and speak to the MCP
server, the binary-layer fixture and the binary backend of the crash walk build on
these packages and are not part of them.

The root package of `cmd/sidera/internal/acceptance` owns the foreground-process
fixture. It runs real initialization and daemon commands, bootstraps a direct
fake adapter through public management after public status reports `running`,
retains bootstrap requests with their IDs and expected versions across daemon
generations, and skips stages whose successful replies were received. A change
committed before a targeted kill returns its stored result when the unanswered
request is resent, rather than becoming a new mutation. The fixture compares the
complete tagged transaction table and owns the logical clock across generations.
It retains each `SetTime` target before the daemon RPC, because a failed response
cannot establish whether the daemon changed time. The next generation starts at
that target even after an error. Fakes are synchronized only after a successful
daemon clock RPC; failure still propagates to the caller. Clock and step fanout
synchronize a momentary snapshot of live control connections. A member can close
after that snapshot, for example while an adapter is stopping after disablement.
A `peer.ErrClosed` synchronization failure is discarded only after that exact
connection's `Done` closes within the existing caller context. This confirms
departure, not synchronization or clean process exit. An unconfirmed departure,
cancellation and all other control errors remain failures; explicit-target
controller calls are unchanged. `TestSyncConnectionsRequiresOnlyLiveMembers`
checks clock and step departures, continued synchronization of a live member and
ordinary remote refusals through real control peers.
The fixture requires stable fake progress around the daemon's quiet report.
With no enabled adapters, its
typed quiet request omits the optional expectation map; an explicitly empty map
violates the strict decoder's existing wire rule. A real empty-daemon regression
uses that same request type for one `test.quiesce` call and requires a satisfied
response, without a retry loop hiding a malformed request. Each node process has
one `Wait` owner and separate file output and trace. Setup marks a complete-line
prefix without renumbering observations. Final traces are read only after exit; orderly stop
freezes the scenario prefix before the stop operation writes. A target is reached
only with both its matching termination marker and an abnormal process exit.
Tagged daemons clear their process crash controls before launching adapters,
so fake native runtimes and their CLI descendants cannot inherit a daemon target.
The fixture's README describes the runtime creation-step limitation.

`NewWalkBackend` in that same binary support package runs scripts and actual
startup and terminal lifecycles through this fixture. It accepts prebuilt programs
and an existing absolute short directory owned by its caller; each run creates,
closes and removes only its own child. Fixture and removal errors remain visible.
`NewWalkScenario` copies descriptor metadata and invokes a binding factory on
every run, rejecting changes in name, steps, budget, needs or recovery metadata.
The fresh recipe's closures and recovery evidence survive that run's generations
only. After the initial interrupted script, later generations resume its drain.
Final script judgements run while the daemon still serves; only then is the
ordinary stop performed, with its cleanup writes outside the recorded window.

`StartupWalkScenario` invokes real Init or bare Start without adapter bootstrap.
Init has no boundary after its command exits; only Start has a live `complete`
boundary. Protected initialization bytes, final layout and the periodic clock
record are read through the existing disk fact and audit helpers. A
`TerminalWalkScenario` sends the actual stop or retirement request, waits for exit
and supplies actual responses and stopped-disk results to the shared terminal
judgement. Its independent restart audit proves stop replay or identity-retired
refusal without signing; that audit never contributes coverage. A natural exit
cannot satisfy a selected target or a fabricated after-return boundary.

The backend accepts a transaction cut only with the matching trace marker and
actual abnormal exit. Step boundaries require a successful real kill and wait.
Offline damage happens only on the stopped disk through `acceptance/disk.Damage`;
per-run recovery callbacks reuse `InspectDamage` and `CheckClosed` over actual
read-only scans. No binary fixture, environment, backend or domain binding imports
the in-process binding or harness. Profiles reject filesystem fault needs and
archive periods different from the executable's existing hourly task. Focused
real-process tests exercise these contracts, including complete second-generation
records and second-generation cuts with third-generation convergence. Complete
binary catalog composition and a full walk remain separate callers.

The CLI Agent driver takes optional per-call `KEY=value` environment overrides.
It replaces matching inherited values, so each call can have a seed or process
crash target without changing the fixture's global environment. Calls use
`Cmd.Run`, which waits for each started child. An abnormal exit without a wire
error preserves `*exec.ExitError` for verification alongside the crash record.

The binary fixture binds actual runtime addresses from public `endpoints.list`
after bootstrap, enabled-instance probes and quiet. It validates one runtime
identity per instance and unchanged addresses for known instances before merging
the observations; collection and validation errors follow the startup error path.
The plain business smoke uses the same private binding after public probe readiness.
The runtime-address getter returns only this bound identity, without an RPC or
generation-error check, and fails loudly for an unbound instance. Identities
survive disablement, closure and daemon replacement and do not establish openness.
Real Agent calls continue to propagate retained generation and transport errors.
Public `alerts.list`, including acknowledged entries, counts an unknown-creation
alert by kind and subject. Checkpoints travel through
the current fake adapter Controller's typed `checkpoint.put`. The closed-session
and retention callbacks inspect the real disk through the store's scanner after
quiet: they require the retained closed endpoint's empty token and absent
checkpoint, and count actual delivery records, bodies, archive files and parsed
summaries. Damaged archive lines are errors. Inspection requires a running node
and returns a retained generation failure before reading. The scan neither
recovers transactions nor writes node state. The binary callbacks for source
update, lost-body retention and creation-settlement filesystem faults explicitly
return `scenario.ErrNotAvailable`; capability selection excludes those recipes.
`newSessionOps` supplies a real CLI session Agent alongside these callbacks.
Before every call its private proxy checks the retained generation error, resolves
the open session's instance from public `endpoints.list`, and matches the fake
runtime's published `ProcessInfo` with exactly one current control connection by
endpoint, native reference, role and PID. The current Controller's `SessionEnv`
then provides the actual injected environment. Read, decode, ownership and getter
errors return before a CLI starts. This query establishes live process ownership;
the shared recipes separately judge creation replies and native creation receipts.
The binding does not infer credentials from node state or cache environments.

Session calls run the existing CLI driver against the actual tagged program.
Their deterministic seeds include the scenario, a separate `session-cli` role,
the actual endpoint and a per-endpoint call ordinal that continues across daemon
generations. They pass the current logical clock and clear inherited process
crash controls. The existing recording wrapper writes actual CLI call results
and model-visible page receipts. A regression runs the shared registration and
declaration recipe, real send/receive/acknowledgement across kill, wait and restart,
and repeats the run to compare actual message IDs. The full binary catalog
remains separate from these bindings.

Binary test execution is selected by `acceptance`; the ordinary fixture and
driver sources stay available without that tag. Plain child builds have no test
tags. A plain smoke starts the real daemon, installs the real fake adapter and
runtime, creates a session through public control, and authenticates its actual
plain CLI. Public readiness and result polling replace test clock and quiet
calls. The smoke checks exact receive text, a completed acknowledgement, an empty
next receive and normal exit. It does not claim sender status refresh, which is
governed by the normal one-minute pull schedule. A separate plain test requires
the public unknown-method error for every test control, ignores invalid observer
environment without producing a trace, and proves independent identities under
an identical test seed. The real retention regression kills and waits for the
daemon after closing a native session, replaces the daemon, checks unchanged
retention facts and the closed-session audit, then observes periodic archival
and expiration in the replacement generation.

The acceptance catalog describes actual Init, certificate-renewing Start, a
serving daemon whose clock advances to its periodic persistence task, and direct
send/receive/acknowledgement. The periodic-clock recipe reads the final clock
record after shutdown and requires the advanced high-water mark without a pause;
it neither calls the clock writer nor copies the periodic task. Its domain check requires the body of a
successfully accepted send to appear in the external ledger. A normal recording
uses the complete daemon table for diagnostics, includes observed setup only in
coverage, excludes setup from the main-point budget, and rejects undeclared or
Recovery writes. Declaration kind and observation source are separate dimensions. A clean first
generation must not contain Recovery declarations. Normal coverage can also come
from Normal declarations in a complete second-generation recovery recording;
Recovery coverage comes only from those complete second-generation recordings.
A recovery-only Normal key is one observed there but absent from every clean
recording, and each such non-sync key requires its own actual second-generation
crash and successful third-generation recovery, reported separately from the
Recovery-declaration targets. Later-generation hits cannot substitute for
second-generation evidence, and observing a point cannot prove it was crashed.
Fixed budgets give half a recording of growth allowance; they
are never enlarged dynamically. The catalog and its bindings add no generic
driver or shared environment capability.

The installation acceptance recipes separate ordinary install, upgrade,
current-version reinstall and platform refusal from pointer-failure rollback,
sharing typed actions and installed-state checks. The normal recipe requires
package input only; rollback additionally requires the named pointer filesystem
fault. Each records 74 main points with a fixed budget of 111. Package inputs pass
through the fixture's package-source callback; the engine owns every node write.
Each successful result is replayed and read back from the operation-result entry;
the current pointer and actual version directories must agree with it. Failed
pointer updates leave no successful stored result, keep the old current version,
and either remove or retain the prepared version as the observed cleanup permits.
A retained version is discarded by the next real installation before promotion.

Management acceptance actions use public operations and retain the complete
request identity and expected version for an interrupted request. Each binding
returns the actual result only after checking its stored form and an identical
resend; recipes compare settings, presets, alerts, backup and clock state,
revocation, endpoint closure and instance state with the requested effects.
Version refusals leave neither a successful stored result nor a changed setting.
Stop requests are checked as requests to the composition root, including replay,
and do not call the fixture's stop method from a script step.

The disabled-instance recipe queues both real target messages and control work,
keeps the sender enabled in another instance, then receives and acknowledges the
corresponding feedback. The returned creation request is judged by its actual
public result: an unknown outcome requires the exact `delivery_unknown` notice
for that message and runtime target. Before disable commits, recovery may instead
complete creation. The shared creation judgement requires a delivered public
reply whose returned endpoint resolves to the same external creation attempt
and its real native reference. Missing or mismatched proof fails. Both branches
still require the pending-message evidence, disabled instance, zero open endpoints
and closed-record audit. Its clean record writes the instance endpoints first and
the endpoint drains separately. Normal declarations that run during recovery
remain normal declarations: declaration kind and observation source are separate
coverage dimensions.

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
  The only real-time waiting exception is one direct `time.NewTimer` call in
  `(*ProcessLauncher).waitForProcessInfo`, in
  `internal/node/testkit/fakeruntime/launcher.go`: fixed 10 ms sampling of the same
  native's existing atomic ProcessInfo publication. No existing cross-process event
  reports its completion. The method stops the timer and observes caller
  cancellation. Expiry only triggers another read; it is never readiness, a
  completion deadline, a spawn retry, a logical clock advance or a business decision.
  This exception cannot be reused: future waits must first seek an event mechanism
  and obtain a separate decision if none suffices. The layout scan limits it to that
  exact file, pointer receiver, method and one direct call; it grants no package
  time exemption.
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
- **A real tool probe is an optional adapter responsibility.** `fakeadapter.Config`
  and `ProcessConfig` share `ToolProbeConfig`: an exact frontend-to-argv map, with
  no shell or fallback between frontends. Missing or empty commands report
  `not_run`, failed commands report `failed`, and only successful completion
  reports `ran/unsandboxed`. Each request's opaque environment overrides inherited
  values; tokens are never copied to arguments, configuration, logs or the ledger.
  Both command outputs are discarded and failure details contain only fixed text.
  Standard-library `exec.CommandContext` and `Run` own cancellation and waiting:
  request cancellation, shutdown and the end of the adapter cancel and wait for
  their commands. A probe precedes native session creation, so it has no fabricated
  runtime session identity or record. Nil configuration keeps report-only behavior;
  neither that report nor a successful configured command proves passage. The node
  still requires every token to arrive through its matching frontend. The same
  command, environment, failure, cancellation and secrecy contract is tested in
  process and in the standalone fake adapter by `TestToolProbeCommandsUseEachFrontendAndPresetEnvironment`
  and `TestToolProbeCancellationReapsItsCommand`; `TestToolProbeConfigContainsOnlyStaticCommands`
  rejects request credential fields in static process input.
- **A runtime process outlives its adapter.** A native session does not die when its
  adapter does; what the daemon must survive is an adapter that restarts and finds the
  session still there, or gone. So each session is a detached process, found again by a
  file it writes atomically, and a process that is gone is still a session whose record
  can be read.
  All native standard streams use the null device, so losing the original adapter
  during startup cannot break a readiness stdout pipe. A replay with an existing
  exclusive session record waits for that same native's ProcessInfo without
  spawning again. If callers race before either file exists, candidates may start,
  but the existing exclusive record create permits only one real native session
  effect. Creation accepts only a complete publication and a matching live
  `runtime.state` reference, PID and start key; deterministic read, decode, identity
  or connection errors return directly. Each started candidate has one buffered
  `Wait` owner that can reap it after cancellation, which does not kill a possibly
  created native. A claim with no publication may mean initialization is pending or
  has failed, even if that candidate exited; it is never fictional readiness, nor
  grounds to delete the record and recreate. Unlike the old stdout-EOF path, it can
  wait until caller cancellation, with no guaranteed finite duration when the
  context has no deadline. The fixture's control connection still owns lifetime.
  `TestPendingStartupSurvivesLauncherLoss` holds a real hello response in this
  window, kills and waits for the launcher parent or cancels its caller, and
  requires replay of the same actual live identity, environment and sole native
  creation. `TestUnpublishedNativeDeathRetainsItsClaimUntilCallerCancellation`
  kills the test-owned native before publication, confirms control departure,
  and requires cancellation rather than readiness from both the pending call and
  a replay, with the exact original record retained and no second creation effect.
  The replay's test-only deadline bounds its caller, not production startup.
  The complete binary recovery regression retains the real
  `clock-high-water/clock.rename#1` cut and its original recipe judgement.
- **Loopback TCP for the control channel,** not a Unix socket or a named pipe: it is the
  same on all three platforms, has no path-length limit, and stays on the local host.
  Session environment responses contain credentials and must not be copied to arguments, static inputs, records or
  diagnostics. A fake whose control connection ends exits, so no test leaves an orphan.
- **`testing/synctest` is not the definition of quiet.** It fakes all time inside a
  bubble, so the real-time behaviour that other tests depend on (pings, write timeouts)
  would differ between layers; it cannot reach other processes; and the binary layer would
  still need its own definition.
- **A walk records first and compares every rerun with the recording.** A crash at the
  n-th write of a run proves something about the recorded run only if the rerun is that
  run, so each pair of transaction and role must have written the same steps with the
  same path operands, including both source and target of a rename; the same target with
  another source is another operation. `crashwalk.NewHit` converts each complete
  `fsx.Observation` into one `Hit`, preserving its point and the backend's generation
  and setup flag, and normalizing `Path` and `From` with `NormalizePath`. A temporary
  creation reports its parent before a file exists, so the conversion gives it the
  fingerprint `parent/.tmp-*` (`.tmp-*` at the root); a logical commit keeps empty
  operands and a root directory step keeps `.`. A target that was not reached,
  a scenario that grew past its budget, a declared write nothing reaches and a write no declaration lists all fail the walk instead of
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
- **The in-process backend binds a trace to its writer's generation.**
  `harness.WalkScenario` holds a script, fixture options, a repeatable drain, an
  offline damage callback and a final judgement. `WalkBackend` creates a fresh
  fixture for each run, cloning any supplied initial disk so recordings and
  replays cannot change their baseline, and binds its observer before each writer
  exists. It converts actual observations through `NewHit`; first-generation startup
  and direct-endpoint setup are marked outside the main window, and recovery begins
  inside a new window. Recording the selected target closes that generation's
  window before cutoff. Raw observations still reach `Options.Observer`, including
  late attempts through an abandoned writer, but cannot enter a later trace.
  During serving the selected callback waits while the runner calls `Kill` from
  outside it, then returns normally through the cutoff's filesystem error; calling
  `Kill` inside the callback could wait for the goroutine making that callback.
  A step or drain error closes the window against later claims and handles an
  already held callback through the same cutoff and release before returning.
  The worker sends completion from a defer, starting with an abnormal-exit error
  that only normal return replaces. `runtime.Goexit` therefore cannot strand its
  owner waiting for completion or silently pass an aborted script, even when a
  selected cutoff normally discards filesystem errors. The error reaches
  `WalkBackend.Run`; panics are not recovered. `testing.FailNow` belongs only in the
  test goroutine, so fallible fixture calls return errors from program workers.
  Startup uses `TryStart`'s controlled cutoff, including logical commit points.
  After a crash the remaining script steps are abandoned and the drain runs on
  the recovered disk; it runs again if another target interrupts it. The first
  drain remains in the main window, so its points can also trigger recovery.
  Offline damage runs between a clean stop and the next generation. Each run
  reclaims its fixture before the next, checks the script's ledger judgements, and reports
  these and the final judgement as `Violations`.
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
  so that adding a rule tightens every script. Every sighting of a part after the
  first must carry the possible-duplicate mark. Two sightings without it fail whatever
  their order; one without it fails when the ledger proves another came before it (the
  order inside one writer, and between steps), and passes where no order is provable,
  because the order of entries across writers is not known on a test clock and that
  sighting may be the first. Counting unmarked sightings alone would pass a marked
  sighting followed by an unmarked one.
- **Quiet is a round in which no count moved.** The engine's own conditions and those of
  the fakes are asked in turn, and a fake that takes work while the engine is asked,
  finishes it, sends a notification and goes idle again is idle on every look, yet the
  engine has not processed that notification. Each condition of the fixture therefore
  reports a count that never goes down together with whether it holds, and the fixture
  returns only when one whole round (its look, the engine, its look) changed none of
  them. A boolean cannot show that a state was left and reached again in between.
- **A layer that lacks a capability says so.** A fixture made without an adapter host has
  nothing for `Env.Adapter` to return, and one made without a runtime nothing for
  `Env.Runtime`; `Env` returns an error that is `ErrNotAvailable` for them. A stand-in would make a script pass for what it did not exercise, and the
  failure is what tells the stream that adds the capability to remove the error.
- **Offline hooks are the halves of a restart, not modes of a crash.** A scenario that
  corrupts a file between two runs, or that crashes the recovery of a crash, needs the node
  down on a disk the test can change and a start whose fault wrapper is armed before its
  recovery runs. `Stop` and `Kill` leave the node down (calls to it are unavailable, as to
  a daemon that is not running), `Start` and `StartWith` run a process on the disk; a
  restart and a crash are those two in a row. Each boot restores the durable clock
  through `daemon.RestoreClock` after transaction recovery and loading configuration
  and keys. Before starting the loop it registers `daemon.AddClockState`, which saves
  the engine's live mark and pause every minute and immediately on a new rollback.
  A persisted pause and its start survive even a return to normal wall time.
  `Options.RollbackPaused` forces a durable pause at every start when none is restored;
  saving it must succeed before the engine opens. False leaves the restored state
  alone. An owner clear therefore survives ordinary restart and periodic saving,
  while a forced fixture pauses again on its next start.
- **Fallible fixture calls share one current-process lookup.** `onProcess`,
  `Records`, `VerifyReport`, `VerifyControlReply` and `quiesce` return
  `engineagent.ErrNodeNotRunning` when there is no running process, before the
  first Start or after Stop/Kill. `Quiesce` retains its diagnostic wrapper. A
  missing process is never an empty successful scan or verification. Quiescence
  captures the process once and probes that engine throughout the wait, so a
  concurrent cutoff cannot turn a later lookup into `Fatal`. The existing
  abandoned-process rule also covers the entire quiet result, through its final
  fixture check. If that process is abandoned before the wait returns, a satisfied
  round is invalid and the diagnostic `QuiesceError` wraps
  `engineagent.ErrProcessEnded`, preserving the interrupted wait's report. An
  abandoned process is diagnosed as ended, never as a command still running. A
  replacement process cannot satisfy the old wait. Accessors with no
  error result retain their strict test-failing behavior. Tests that require
  quiet check its returned error before judging subsequent evidence.
- **A startup trace comes from actual Init or Start.** The in-process backend
  dispatches engine scripts and startup scenarios to their concrete runners;
  private run state owns their common plan, target, trigger and trace rules. Init
  starts from an empty in-memory disk. Start prepares its disk through an
  unobserved actual Init, then observes its own writer: combining both writers
  in one generation would repeat transaction ordinals. The window opens before
  the observed writer exists and includes ordinary shutdown. A startup callback
  selects through the same recorder and `NewHit`, cuts off filesystem access,
  clones the disk and cancels startup waits; cleanup belongs to the runner.
  Failed Start unwinds itself; successful Start is quiesced and stopped with
  RequestStop/Wait before the next generation. Selection still means a crash if
  a logical commit lets Init return nil. A completed Init marker is verified
  without creating another writer or inventing observations; otherwise actual
  Init finishes the interrupted disk. Retry checks preserve the existing keys,
  self-revocation, clock record and owner configuration. Offline damage is applied
  after shutdown and window closure, and final checks read without a writer.
  A fixed clock and synchronized seeded reader keep recordings reproducible.
  The startup runner supplies no adapter host and runs no agent script, so its
  reader is not shared by asynchronous adapter probes and scripted registrations;
  `StartAfter` optionally advances that clock after preparation and before
  observation, so a scenario can make certificate renewal due at Start. The
  default is zero. `AdvanceAfterStart` optionally advances the same clock once
  after each successful Start has quiesced, then quiesces the resulting work
  before RequestStop/Wait. It reaches the daemon's real periodic tasks and does
  not promise to execute every tick crossed by a large jump. Each successful
  later generation advances again from the clock the previous attempt left;
  an attempt interrupted during startup does not advance. Cancellation during
  the post-start work still reclaims the daemon before returning. Both fields
  require non-negative durations and refuse nonzero use with Init before
  allocating resources; zero retains a startup-only run. Socket homes
  are separate short private temporary directories and daemon data stays in
  memory. Representative Init, renewal and layout replays verify this capability;
  they do not assert complete transaction-table coverage.
- **The node is made by the program's own code,** through the `init` plan (the keys and
  the configuration) and the install and enable entries, on a disk copied from one made
  per test binary. A hand-written disk would let the fixture and the program disagree
  about a format. The keys are real, drawn from a seeded source, so the node's
  identifier is the one its identity key derives and is the same in every fixture; a
  test that makes an envelope another node's endpoint would send verifies what comes
  back with the node's own certificate (`VerifyReport`).
- **Package sources are injected without another reader or validator.**
  `harness.Options.OpenPackage` is the caller's function that opens a named package
  as a read-only `fsx.Reader` rooted there. Every generation passes that function
  unchanged to `engine.Deps.OpenPackage`, including after a restart; the fixture
  caches no package reader. Tests can supply an in-memory package while the engine
  and configuration retain their own staging, validation and install rules. Nil
  retains the engine's refusal to install without a package reader.
- **Startup has process ownership before its first recovery write.** `NewStopped`
  prepares the same disk, clock, ledger and fakes as `New` without booting a hidden
  first generation. `TryStart(ctx, StartOptions)` synchronously runs one down node's
  start, with `Arm` installing fault rules before transaction recovery and `CrashAt`
  selecting a startup crash point. Every attempt consumes a generation number,
  including one that dies before an engine exists. Agents see the node as down until
  the complete start succeeds. `New`, `Start` and `StartWith` remain fail-loud wrappers;
  the mount helpers retain their recover-before-return contract.
- **A startup observer selects the cutoff; its caller reclaims the process.** The
  fixture serializes `CrashAt` calls from program goroutines and accepts one decision
  at most. The callback must not block, call `Kill`, or do cleanup. Its decision uses
  the same crash barrier as `Kill`: mark the process abandoned, cut off its disk,
  install the snapshot, end its fault-injection scope, then write one crash ledger
  entry. Only after the snapshot is installed are startup waits cancelled. The
  observer returns normally so filesystem errors unwind production transactions;
  panic and `Goexit` would bypass that behavior. Stage boundaries and publication
  check the cutoff even when a commit has no following filesystem call. The outer
  startup call aborts the host and cuts adapter pipes, waits for any engine to stop,
  and releases its context before returning a failed attempt. No next generation
  starts until these resources are reclaimed; a cleanup timeout returns an error
  and prevents another attempt.
- **An interrupted start is not always a crash.** `TryStart` returns `(false, nil)`
  for serving, `(true, nil)` for a selected crash with successful cleanup,
  `(false, err)` for ordinary errors or cancellation, and `(true, err)` for a selected
  crash whose snapshot/ledger or cleanup failed. Missing a selected target can still
  start normally, so the caller must assert target coverage. Ordinary failures retain
  disk effects, without a snapshot or crash entry, and cut off and reclaim the failed
  process. Cancellation is cooperative through existing context-taking calls and
  stage boundaries; synchronous transaction recovery and custom observers are not
  interrupted by a background startup goroutine. A held observer must be released by
  the test. The startup selector ends with its attempt; `Options.Observer` retains
  its raw observation of every generation's steps, including cleanup. Each callback
  receives the complete `fsx.Observation` and declaration kind: actual paths stay
  bound to their point during startup and serving, with empty operands only for
  logical commits. The startup selector still takes `observation.Point` and kind.
  The caller's context bounds startup alone, not a successfully serving generation's
  lifetime.
- **Only a real crash ends a fault scope's future expectations.** An ordinary
  `Faulty` reports every rule that never fired, including after Stop, Restart,
  cleanup, startup failure or cancellation. A crash can instead end a valid
  prefix after a rule was armed and before its target call: the dead generation
  has no opportunity to inject that future fault. Only `cutCrash` calls the old
  wrapper's `Abandon`, after fencing its disk calls and installing the next disk.
  This ends matching and unused-rule checks without changing actual hit counts;
  existing handles pass through to their already cut-off base and cannot write
  either disk. New rules on that wrapper fail loudly. Every next generation has
  a fresh strict wrapper. A failure required before a crash still needs actual
  hits or observable outcome evidence; ending a scope proves no injection.
  Runtime walk and controlled-start tests retain real crash ledger entries,
  target selection and recovery, while test subprocesses prove ordinary
  interruptions and unhit next-generation rules still fail cleanup.
- **The loopback and its knobs belong to the fixture.** One set of knobs lives as long as
  the fixture and each process gets a loopback made over it, so a knob set before a
  restart is still set after it. Every hit is written to the ledger as a `knob.hit` entry
  (knob, call, node, message) from the goroutine of the call that was hit, and the count
  of hits is the loopback's condition of quiet, so a hit between two looks is not missed.
  A knob keyed by a message is armed before the message first goes on the wire: the test
  holds the sender's calls with `Unreachable`, learns the message's identifier, arms the
  knob and lets the calls go.
- **A node can run on the real file system.** `Options.FS` selects a directory of the
  machine's own file system, wrapped by the same fault layer, for what only a real file
  system answers; such a node cannot be crashed or cloned. `Options.DiskFree` stands in
  for the free-space query, `Options.Policy` replaces the rules and `Options.NodeSettings`
  patches the settings through the configuration's own change, both before the first
  start; `Options.Disk` starts a node on a disk a crash walk took; `Options.ArchiveSweepEvery`
  has the node register the archive sweep on that period, as the daemon does.
- **A crash cuts the process off before it copies the disk.** The fixture closes a layer
  of its own between the process's fault wrapper and its disk, so that every file-system
  call the old process makes afterwards fails, then copies the disk; the copy is the
  crash. Stopping the old engine in the background and letting it write to a disk nobody
  reads would not be enough: a call that was inside the engine would finish, be
  acknowledged to the client as a success and be recorded as the node's answer, for a
  node that no longer exists. So a call that returns after its process was abandoned is
  an unknown outcome (the client of a dead process sees a connection that ended), and
  the name of a registration that was in flight is not remembered. The layer is the
  fixture's and not a rule of the fault wrapper, which fails a test whose rule never
  fires. With the adapter host attached, a crash aborts it and cuts the pipes and sends
  no shutdown, and the calls the engine makes on the host go through the same check: a
  call to an adapter that returns after the crash is an unknown outcome and never a
  confirmation. A crash is an event of the run, so it is written to the ledger (mode, the seed of a power
  loss, the point a walk names), and a reader of a failed run need not guess it from the
  disk.
  Power loss is the seeded model of the file system, with a fixed second word, so a seed
  is a disk.
- **The adapter host of the fixture runs on the test clock, over fakes in the process.**
  Its pings, its backoff and its waits for a process to end are timers of the one test
  clock, so a test moves them with `Advance` and a step is taken only when the host has
  armed what it is meant to fire. The fake adapter runs on pipes in the test process and
  is supervised as a child process is, which gives one process's determinism and the
  supervision paths of two. Every launch has knobs of its own, because a restarted
  adapter is a new process; the ledger writer of an instance is one for all its launches,
  because opening a stream twice would fork its sequence. A launch can be told to
  survive: `Kill` returns an error (the host then does not wait for it), the closing of
  its stream does not end it, and the host's closing of
  its end of the pipes does not reach it, so it holds the instance lock as a hung process
  does, which is the case the lock exists for: the host cannot end it, starts the
  instance again after its backoff, and every replacement is refused the lock and never
  controls a session beside it. The launcher is one implementation shared with the
  tests of the daemon, so that the code that starts fakes is not written twice. The
  fixture's startup waits beyond the host's `adapter.start` boundary until every
  enabled instance's latest launch has received `adapter.probe` or ended. The
  probe draws tokens from the same seeded engine reader that registrations and
  controls use; without this barrier, scheduling changes the identifiers and
  paths of crash recordings. The wait uses the launcher's predecessor-ordering
  condition, observes cancellation and has a ten-second real-clock limit.
  Disabled instances are not awaited. Quiet uses the same readiness condition
  for later relaunches, and counts launches to detect replacement between rounds.
  The quiet check adds the fakes' own numbers: with a host, the engine is asked against the
  notifications the latest launch has sent and the requests it holds, made again each
  round, and the fixture waits for that launch to have no request of its own that is not
  answered. With a runtime it also waits for the runtime to have no turn whose actions are
  not finished, and counts how often such a turn began or finished: what a turn does after
  its start (a receive through the engine, the report of what the model read, its end)
  runs in a goroutine of the runtime that nothing else shows while it is on its way, so a
  quiet that did not wait for it would depend on how quickly that goroutine runs.
- **The caller-bound backend does not hold probe credentials.** Its ten endpoint
  methods call the engine, including identity, visible nodes and live directory
  pages. `Probe` retains `not_found/method_unknown` and `ErrNotAvailable` (defined in
  `fakeagent`, re-exported by `scenario`), because an opaque endpoint caller cannot
  present a raw probe token. The real HTTP interface supports probe credentials.
  The sentinel distinguishes this backend limitation from a server's own response.
- **Send replay evidence has three states.** `fakeagent.SendResult.Replay` is
  `ReplayUnknown` when the backend has no replay evidence, `ReplayFresh` for a
  known new message, and `ReplayRepeated` for a response that found the earlier
  message under its idempotency key. Zero means unknown rather than fresh.
  Engine, CLI and MCP backends convert the actual response flag through
  `ReplayStateFrom`; an unsuccessful response supplies no replay evidence.
  Body-cleanup recipes require exactly `ReplayFresh` before treating their
  returned message as a new source delivery; unknown is insufficient.
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
| Engine backend | `internal/node/testkit/fakeagent/engineagent` | `TestDeclarePatchReachesTheEngineForTheCallersOwnEndpoint`, `TestDeclareLeavesAFieldThatIsNilAsItWas`, `TestDeclareRefusalsAreMappedToTheCategoryAndCodeOfTheInterface`, `TestEveryEngineReasonMapsToACategoryAndCode`, `TestMePresentsTheCallersDeclarationAndFrontendValues`, `TestPeersReturnsTheVisibleNodeInTheSharedResultShape`, `TestDirectoryPresentsEndpointValuesAndPassesTheContinuation`, `TestProbeNeedsCredentialsThisCallerBoundBackendDoesNotHold` (the scenarios of sending, intake and session creation are in the engine's tests) |
| Scripts | `internal/node/testkit/scenario`, `internal/node/testkit/scenario_imports_test.go` | `TestRunSetsUpDirectEndpointsThenRunsEachStepAndQuiescesAfterIt`, `TestRunStopsAtTheFirstFailedStepAndNamesIt`, `TestRunRefusesAScriptThatCannotRunBeforeTouchingTheSystem`, `TestRecvAndAckAllAcknowledgesEveryPageAndStopsAtAnEmptyOne`, `TestAScriptIsAScenarioOfTheCrashWalk`, `TestCheckRejects`, `TestACheckOfRepeatSightingsFailsOnlyWhatTheLedgerProvesIsAfterAnotherSighting`, `TestScenarioPackageDoesNotImportTheEngineOrAFixtureOfAnyLayer` |
| Engine fixture | `internal/node/testkit/harness` | `TestOneClockIsTheSourceOfTimeForTheEngineAndTheFakes`, `TestADirectEndpointRegisteredThroughTheFixtureAppearsInTheEnginesList`, `TestQuiesceReturnsOnlyWhenTheEnginesConditionsHoldAndOtherwiseFailsWithAReport`, `TestAProcessCrashThenRestartKeepsExactlyWhatWasDurable`, `TestAPowerLossRestartIsReproducibleUnderAFixedSeedAndKeepsWhatWasAcknowledged`, `TestFaultsAreInjectedThroughTheFileLayerAndTheirHitsCounted`, `TestAScenarioScriptRunsThroughEnvOnTheEngineLayer`, `TestACallInFlightAtACrashHasAnUnknownOutcomeAndLeavesNoEffectOfTheAbandonedProcess`, `TestADirectCallThatWasInFlightAtACrashIsNotRecordedUnderItsName`, `TestAProcessCrashKeepsAWriteThatWasNotSyncedYet`, `TestAStoppedNodeIsDownAndAFileChangedOfflineIsWhatTheNextStartReads`, `TestAFaultArmedForTheNextStartAppliesToThatProcess`, `TestAKilledNodeIsDownOnTheDiskTheCrashLeftUntilItIsStarted`, `TestACrashIsWrittenToTheLedgerWithItsModeAndSeed`, `TestACrashThatNamesItsPointIsWrittenWithThePoint`, `TestNoSecondWriterAfterAdapterRestart`, `TestACrashCutsTheAdapterWithoutAShutdownAndACleanRestartShutsItDown`, `TestQuiesceWaitsUntilTheEngineHasHandledEveryNotificationTheAdapterSent`, `TestTheAdapterOfTheFixtureIsTakenOnThroughTheHost`, `TestAnExtraInstanceHasItsOwnAdapterRuntimeAndEndpoints`, `TestACallToTheAdapterHostThatReturnsAfterTheProcessWasAbandonedIsNeverAConfirmation`, `TestQuiesceDoesNotReturnWhileAConditionThatKeepsHoldingStillMakesProgress`, `TestQuiesceReturnsAfterOneRoundWhenNoCountMoves`, `TestQuiesceRejectsAQuietRoundWhoseProcessWasKilled`, `TestAQuiesceErrorSaysACommandIsRunningOnlyWhenTheLoopDidNotAnswer`, `TestTheLeakCheckReportsAGoroutineOfTheMarkedPackages`, `TestControlGenReachesEveryPathAndEveryControlRecordCloses` |
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
- The MCP backend lives in `cmd/sidera/internal/acceptance/agent/mcp`. The binary-layer
  fixture, the command-line backend and the binary backend of the crash walk are separate
  from the engine fixture.
- The engine-layer fixture runs one adapter instance, `Instance`, unless it is made with
  `Options.Adapter.ExtraInstances`, which adds instances that have each an adapter process, a
  controller and, with `Runtime`, a runtime of their own, reached by name (`Adapter`,
  `AdapterLaunches`, `RuntimeOf`, `RuntimeAddrOf`, `DirectOn`); the accessors without a name
  stay on `Instance`. It has a runtime only when it is made with
  `Options.Adapter.Runtime`; `Runtime` returns `scenario.ErrNotAvailable` without it, and so
  does `Adapter` for a fixture made without `Options.Adapter`. Judgements whose
  subject does not exist yet (no message lost, one feedback per delivery and kind,
  acknowledgements on behalf, every record closed, the drain) are not in `scenario`.
- The engine backend maps the engine's refusals itself, with the codes the interface's
  table does not list taken from the reasons' names; when the interface packages' shared
  mapping exists it replaces the table here. The fixture runs the transactions of
  `init` and of the adapter install with the declarations of the daemon and the engine, and
  builds its table of transactions from the daemon's, so a transaction the daemon adds is in
  every table the crash walks and the power-loss run read.
- The program path of a fake adapter has the extension of an executable of the platform
  (`.exe` on Windows, where a path that names a program is used as it is), written for
  Windows and cross-compiled: pending verification by CI on main.

Acceptance delivery judgements relate successful call results to the exact body
shown to the intended endpoint, or to a real terminal feedback notice naming that
message and target. The notice must originate at the receiving endpoint's local
system address and match the committed header and public feedback-kind summary.
The judgement groups feedback by original message, target and kind, deduplicating
legitimate rereads by the notice's own identifier. Its receive drain acknowledges
only returned parts and checks the real per-part outcomes. Session recipes use
explicit instance addresses and actual control replies; final closed-session
checks read only the endpoint status, cleared token and checkpoint absence.

Acceptance recovery descriptions assign each non-sync Recovery declaration to
one real trigger. Additional trigger metadata requests complete recovery records
without claiming target ownership. The runner deduplicates by scenario and the
whole trigger, keeps second-generation provenance, and compares each actual
second crash against that complete window with the existing crash-walk path
comparison. A terminal third generation alone cannot demonstrate either the
second-generation observation or its interruption. `acceptance/disk` shares offline damage and closing audits across execution
layers, accepting `fsx.FS` for stopped-disk damage and `fsx.Reader` for audits.
It modifies only product-created records after shutdown; final owner predicates establish
that quarantine, replay, orphan collection and transaction cleanup have settled.
The in-process acceptance suite uses one recording sequence for all three source
columns. Each recorded key retains its scenario, complete trigger, generation and
normalized path operands in diagnostics; the derived Normal interruptions have
the same exact-cutoff and convergence checks as Recovery interruptions and their
own count.
An instance-disable source interrupts the first endpoint rewrite after the
configuration has disabled the instance. Its restart supplies a nonempty drain
batch, reaching Normal declarations that the ordinary disable path delegates to
per-endpoint drains. Those keys are discovered from the complete second window,
then interrupted separately; their declaration kind stays Normal.


### Coverage responsibility across execution layers

`Walk` can accept `Options.Coverage`, a `CoveragePartition` with `Required` and
`CoveredElsewhere` key lists. Nil retains the complete-table contract. A partition
assigns responsibility only: it cannot supply observations, target results or
successful runs. Walk copies both lists before running anything and verifies that
each declared key appears exactly once, neither list contains an unknown key and
the two lists cover the entire transaction table. A nonempty scenario catalog
cannot have an empty Required list. Adding a declaration without classifying it
therefore fails instead of silently moving it out of this layer's obligations.

The complete `txn.Table` remains the authority for every run and generation.
Undeclared writes, wrong generations, clean Recovery writes, missed targets,
incorrect cutoffs, changed paths, domain failures and budgets retain their original
checks. A real hit of a CoveredElsewhere key also fails: the responsibility
partition disagrees with the layer's actual sources and must be reconsidered.
Normal coverage requires every Required Normal key; Full also requires every
Required Recovery key, including sync steps. Static cases must assign every
Required non-sync Recovery key once and may not assign CoveredElsewhere keys.
Public `CheckRecoveryCases` still checks the whole table, sharing the same internal
assignment algorithm with Walk.

Dynamic recovery-only Normal targets are derived afresh from this layer's clean
and complete generation-2 records, whose hits must all belong to Required. Main
points and step boundaries still follow the selected scenarios' real recordings;
Full, Smoke and stable shard numbering keep their original selection rules.
Every shard records all of its layer's sources and validates the full partition.
No other layer's observations or target ordinals enter the walk.

`Report.Coverage` contains the copied partition. Existing `Normal` and `Recovery`
fields still count actual observations against all declarations in the complete
table, so their reached counts can be lower than their declared counts in a
successful partitioned walk. Required and CoveredElsewhere counts can be derived
from the validated keys and declaration kinds without another stored counter.
Sources, triggers and executed-run fields contain only this walk's real evidence.

### Shared source analysis and complete recovery recordings

`crashwalk.AnalyzeSources` owns the three source columns and the recovery-only
Normal difference for both whole walks and bounded acceptance runners. Callers
supply all clean generation-1 hits and only generation 2 from completed
trigger-only runs. Recovery declarations in clean runs and hits from the wrong
generation are errors. Coverage and missing/undeclared keys use `Assess`;
synchronous steps count only when actually observed. Third-generation hits and
interrupted prefixes cannot fill complete second-generation coverage.

`CheckRecoveryRecord` validates every complete trigger-only run before it supplies
coverage or a baseline: exactly two generations, no target results, no domain
violations, and each hit's generation agrees with its window. It calls
`CheckRecoveryTrigger` to compare a crash trigger's first-generation prefix with
the clean recording through `CompareRuns`. Offline damage has no first-generation
crash cutoff to compare. Whole walks and bounded runners use the same check, so
neither can admit provenance or a divergent trigger the other would reject.

`Walk` records all clean scenarios before analyzing that difference. It also
records every unique `(scenario, complete trigger)` from static recovery cases
and `Options.RecoveryTriggers`, on every shard. Unknown names or invalid triggers
fail before running anything. This adds one complete recovery run per unique
static trigger even when additional triggers are nil; the clean scenario budget
continues to count only first-generation window targets.

Each non-sync Normal key absent from all clean recordings gets one actual
second-generation target. Source selection is stable by scenario name and trigger
fields; the first sighting in that source preserves the observed ordinal. Full
appends these targets in key order after the existing target groups and requires
both declaration kinds to have complete coverage. Recordings do not consume shard
numbers. Shards share the full source set and partition target execution without
duplicates. Smoke keeps its existing main and static recovery selection, reports
the observed dynamic set, and explicitly skips its execution.

Static and derived generation-2 targets use `CheckRecoveryTarget`: one true
Reached result, exactly three generations, an exact final point in generation 2,
no domain violations, and a prefix matching the corresponding complete baseline
through `CompareRuns`. The comparison retains both normalized path operands and
its existing per-transaction/role interleaving rule. Static recovery cases keep
their original exactly-once declaration responsibility and ordinal-1 target rule.
Both kinds of interruption also pass `CheckRecoveryTrigger` against their clean
recording. This keeps the first crash reproducible independently of the second
generation's own prefix comparison.

`acceptance/inproc.RecordCatalog(t)` provides the complete catalog's clean hits
by recipe name and complete recovery records as `CatalogEvidence`. It records
each unique recipe once and each `(recipe, complete trigger)` once, combining
static recovery responsibilities and extra recipe triggers. Before returning it
checks clean generation 1, budgets, declared writes in every generation,
`CheckRecoveryRecord` for every recovery run, and complete Normal and Recovery
coverage through `AnalyzeSources`. Collection and validation errors return no
partial evidence; harness failures may still terminate the owning `testing.TB`.
The value contains observations only, with no fixture or engine handles.

The bounded in-process acceptance test calls this collector once and reuses the
records for all static Recovery and derived Normal interruptions. It uses the
same source and target checks directly and owns only execution and diagnostics.
Its cost is the clean catalog plus unique complete recovery triggers, static Recovery
interruptions and derived Normal interruptions; it does not run every clean main
point. The source key sets and
actual interruption targets therefore have one owner across bounded acceptance
and whole walks. The bounded runner requires the resulting missing Normal,
missing Recovery and undeclared key sets to be empty against the complete daemon
table; logging those sets alone does not establish completeness.

`Report.Sources`, `Normal` and `Recovery` describe observations;
`RecoveryRecordings` counts complete trigger-only runs. `RecoveryRuns` remains the
static interruption count. `RecoveryNormalPlanned` is global,
`RecoveryNormalSelected` is this shard's allocation and `RecoveryNormalRuns` is its
executed count. `RecoveryNormalSkipped` marks Smoke's omitted group. Observer
phases `recovery-record` and `recovery-normal` carry the source `Trigger` for run
timing. Smoke emits one `Skipped` notification with the planned count; it is not
a run start/end pair and cannot count as execution. Diagnostic `Triggers` after
main crashes remain useful for writing cases but cannot supply coverage evidence.

Feedback waits in acceptance recipes have one private owner, `feedbackDrain`,
and separate state for each phase. Two one-minute advances allow a pull and then
feedback merging. Issuance is remembered before `Advance` because the selected
time can already have caused a real write when a process dies before the call
returns. Only successful quiescence completes that step; reentry resumes the
issued minute before issuing the next. Repeating the wait after a crash could
cross a creation reply deadline and create additional correction feedback.
A completed timed phase still receives and acknowledges on every reentry, since
those actual calls can themselves be interrupted. Phases never share progress
merely because they share a sender or evidence object. No clock read or extra
waiting period is added to `scenario.Env`.

The in-process feedback regressions use real recipe drains, `WalkBackend` cuts
and the shared exact recovery-target checks. They compare actual logical time
and audit real feedback and records after clock, receive and acknowledgement
interruptions. A wrapper can hold an advance's return through real due work to
exercise its response window; it manufactures neither time nor write targets.
A real creation checks distinct phase durations, and the instance-management
regression cuts every non-sync clock write of its complete second generation.
The existing unread-body and missing-feedback counterexamples remain required.

A reply acceptance variant fails a source-record create before its visible
replacement, using the existing filesystem-fault facility. The engine continues
serving with a queued source and retained body after the target accepted it.
A real target receive and reply carries the arrival evidence that releases the
source body in the reply transaction. The recipe uses no transport fault or
raw-envelope callback, and judges the received bodies and final closure.

Recovery-creation recipes hold the native first turn open until their real
session receive and acknowledgement finish. They then end that turn explicitly,
so an early idle notification cannot race another prompt against the drain. The
drain is repeatable after crashes and accepts an already-ended turn only through
the fake runtime's named `ErrNoTurn`. Its domain check requires every started
round to end and rejects prompts outside that explicit round.

Acceptance distinguishes a control signal's delivery from the initial message
it derives. An overdue held creation returns the real unconfirmed timeout reply;
its allocated endpoint must be closed with its token cleared. If the initial
message passed its report window while accepted, its actual uncertainty notice
must name that derived session endpoint. A delivered control reply does not prove
the initial body reached a model. Before that deadline phase starts, a restart may
instead finish the real creation. Its created reply must name a native session
with the original control's initial-body evidence, obtained through actual
receive and acknowledgement. Once the deadline phase has started, the strict
timeout reply, closed endpoint and exact feedback remain required. A returned
absent-node send likewise retains its full report-window obligation after an
interruption. Deadline moves are remembered before their resulting writes can
be cut, so repeated drains do not expire unread evidence by advancing again.
Synchronous preset refusal is a separate variant whose first receipt can release
the retained initial body.

The retention acceptance path creates, reads from, sends from and closes a real
session, then advances through record and archive retention using the existing
periodic sweep. Read-only evidence requires real intact summaries after live
records are swept, and no remaining archive files after their retention. Its
bounded drain completes representative real interruptions without synthesizing
records, summaries or cleanup effects. It receives and acknowledges pending
session sends and settles their sources before retention begins. Large retention
advances are remembered across interruptions. A resumed drain allows one further
ordinary sweep period because restart rearms the periodic task, and removing the
last source intentionally leaves its session endpoint until a following pass.
Real second-generation interruptions during source summarizing, endpoint sweeping
and archive expiry must reach their exact cutoffs and finish a third generation
with no retained endpoint, body, live record or expired archive.

The control-expiry acceptance variant also covers a recoverable failure before
its create-result replacement becomes visible. If the control's own report
window closes before a reply commits, its sender reads the precise uncertainty
notice and the owner closes the alerted endpoint through management. The audit
requires the actual owner alert, closed endpoint, empty token, absence of a native
creation and drained records, rather than treating those writes alone as success.
A crash before the closing tick commits can let startup retry the failed result
replacement and deliver the exact `unconfirmed/ready_timeout` reply naming the
allocated endpoint. This known reply establishes the uncertain result without
requiring an additional notice; the unique owner alert, absence of native
creation and closed-session requirements remain. The recipe separately remembers
the clock advance and successful quiescence of the script's deadline step, so
this interrupted result cannot weaken the complete clean recording's no-reply
oracle or repeat the report-window advance.
A cut before that deadline phase can instead let restart finish the real
creation. The original returned control result must then satisfy the shared
creation judgement, including a delivered reply associated with external native
creation. This request has no initial body. The drain receives from the resolved
session and closes it through the real management operation. An already-closed
result permits repeated cleanup; the same native session's external close, final
closed endpoint, cleared token and drained-record requirements still apply.

Body-cleanup acceptance variants keep declaration kind distinct from observation
source. Their healthy absent-node sends end in exact expiry notices. A complete
second-generation recording damages only the existing source body's bytes while
the process is stopped. Normal lazy validation then records a body-corruption
alert and a lost source; the sender must actually receive its precise loss notice.
Feedback clearing can remove that source's retained bytes. A second variant
uses the existing nonfatal source-create faults for feedback clearing and ticking,
reads the notice, closes its sender through management, and leaves final cleanup
to the real periodic archive sweep. Intact summaries and absence of live records
and bodies are the final facts. Complete recordings of these Normal writes
supply the additional second-generation interruptions through shared source
analysis; the recipes do not assign them Recovery ownership or add fixed counts.
Their repeatable drains preserve already-executed clock advances, so a second
crash does not skip the actual unread notice's lifetime.
The archive variant also remembers successful fault preparation across generations.
Those faults belong to the prepared process; startup after a real crash can replay
and clear the feedback before the next drain. Reinstalling its old rules would
then require feedback clearing that no longer exists. The next process performs
ordinary recovery, while the uninterrupted prepared process still has to hit its
strict fault rules and leave body removal to the real archive sweep.
The archive drain also remembers completed receipt, acknowledgement and validation
of its required feedback before requesting close. An unanswered close is retried
with the same request identifier without another receive on the closed sender.
A real code-free `closed` or `already_closed` response is required before closing
is complete. The latter can be a fresh result when the effect survived a crash
before its original result was stored; replay is not required. Final feedback,
corruption, summary, live-record, body and closed-record checks still apply.

Recovery-creation acceptance retains actual successful control results and reads
their public status. A validated creation reply supplies the initial message's
session target; the external creation attempt must confirm a native session for
a created outcome. The requested body must appear there with that control's
identifier, or the sender must have received the corresponding terminal notice.
An absent reply requires both a terminal control result and actual feedback for
the runtime target. Final disk convergence remains an additional audit.

Interrupted acceptance scripts distinguish completed calls from future steps.
Direct registration is a separate recipe: repeating its actual registration key
must return the same address. Declaration recipes use real `session.create`
controls from direct senders, then associate the public reply with external
native creation through the shared session resolver. These two delivery recipes
request session setup explicitly: hiding is checked by the created session's real `Me`, and
second-receive narrowing must reject the previously queued body with its exact
feedback. A successful creation does not imply a declaration was requested. A session
send owes its body immediately, while blocked/recovered feedback is required
only for a blocking phase that began; an interrupted blocking phase is finished
before delivery is restored. Lifecycle drains receive and acknowledge pending
session messages, or obtain the real feedback caused by a requested closure.

The blocked-session recipe declares its complete external prompt refusal and
authentication availability through `Env.SetAdapterInputs`. The recipe captures
availability's `SinceMS` once from the existing real ledger and keeps that value
across generations. The run-owned declaration is installed before each adapter
serves requests; reconnecting therefore cannot silently remove the scripted
refusal and forward the pending body before its blocking phase finishes.
`restore-prompt` clears only refusal and `restore-credentials` clears availability.
The repeatable drain finishes an interrupted blocking phase before explicitly
clearing both. All fourteen steps, the 285-point budget, original clock advances
and both mandatory real blocked/recovered notices remain unchanged.
Focused in-process and binary regressions interrupt the original
`blocked/attempts.rename#1`, then a real blocked rewrite from the complete second
generation, and require third-generation convergence through the shared recovery
checker. A restart after successful credential restoration uses the original
drain with repeated empty declarations suppressed only in that test; this keeps
another clear from hiding stale startup inputs. Actual body and notice judgement
still runs unchanged.

Ordinary session resolution captures evidence while its source exists, so later
retention does not create a dependency on an already removed status record.

Acceptance recipes declare fixture requirements as `recipes.Needs`, including
runtime adapters, extra instances, archive sweep period, package input, offline
damage kind and named filesystem faults. Bindings select through capabilities
before executing callbacks. Binary capabilities include stopped-disk damage but
no filesystem fault injection.

A binary acceptance test's external orchestration may independently call
`RecordCatalog` once and supply its pure return value to `Catalog.AnalyzeLayer`
to define execution-layer duties. Binary fixtures, backends, environments and
domain bindings import neither the in-process binding nor the harness and hold
none of their handles. In-process observations remain separately identified and
never count as binary execution evidence. The binary layer derives interruption
ordinals from its own actual complete second-generation records. Each test
binary collects from its current source; no evidence file or cache is used.

The `acceptance`-tagged external
`cmd/sidera/internal/acceptance.TestCrashAtEveryCrashPointPreservesInvariants`
is that orchestration entry point. It selects the complete supported catalog,
uses the unchanged full `daemon.Table` and passes `LayerEvidence.Reachable` and
`InProcessOnly` as `CoveragePartition.Required` and `CoveredElsewhere`. Each
script's private factory rebuilds every domain binding and recipe closure per run,
retaining only its immutable name between runs. The backend supplies that run's
recovery operations; their damage state survives its daemon generations.
Startup and terminal descriptions use the existing binary wrappers. Additional
recovery recordings come from the selected scripts' declared triggers.

One call to `Walk` owns every clean and complete generation-2 recording and all
mode/shard planning. Its actual binary records alone determine ordinals and
dynamic targets. The entry point logs the separate responsibility source and
partition, the three actual binary source sets, complete-table denominators and
missing Required keys, interruption counts, dynamic planned/selected/executed
counts, per-run and total elapsed times, and the explicit smoke skip reason.
This entry point defines how to execute the full catalog; its presence is not
evidence that a full-mode walk has completed.

`Catalog.AnalyzeLayer` uses the complete clean and second-generation recordings
for supported recipes. Reachable keys are their observed union; in-process-only
keys are the full observed union minus that set. Any supported observation makes
a key reachable. Each layer derives its recovery-only Normal targets through
`crashwalk.AnalyzeSources` using its own supported source records, preserving the
actual recipe, trigger and ordinal. A shared key never makes every trigger that
observed it reachable.

Recovery recipes own quarantine and body-corruption owner-alert judgements.
`RecoveryOps.InspectDamage` supplies read-only counts and the actual damaged
object identity through `acceptance/disk.InspectDamage`, which accepts the current
real record scan and the result of damage actually applied. It counts all owner
alerts and `control_unknown` alerts, and only `corrupt_body` alerts matching the
damaged body. Bindings retain lifecycle ownership and scan errors; recipes decide
whether the facts satisfy recovery.

The session, retention and installed-package disk rules also belong to
`acceptance/disk`, so both execution layers judge the same evidence.
`CheckClosedSession` takes the current real scan and endpoint identifier and
requires the retained endpoint to be closed with an empty token and no session
checkpoint. `InspectRetention` takes a read-only filesystem, that scan and an
endpoint identifier. Its `recipes.RetentionFacts` count every source, lost source,
target and control record, all bodies and archive files, every parsed summary
and the requested endpoint's presence. Archive read failures and damaged lines
return the facts accumulated so far with their error. `InspectInstalledPackage`
takes a read-only filesystem and package identifier and returns the decoded
current pointer and every sorted version name as `recipes.InstalledPackage`.
An absent current pointer requires an absent package directory; unfinished
version directories and read or decode failures remain errors. Bindings obtain
their own current reader and scan, preserving their existing process checks;
the shared helpers neither acquire a store nor recover or write state.

The final closed-record audit uses `acceptance/disk.CheckClosed`, including the
real recovery plan and unresolved deliveries. Failed-creation notification drains
retain the recipe's own `DeliveryEvidence` through repeated runs and validate it
in the final judgement.

Ordinary interrupted staging reaches adapter garbage collection; current-version
unstaging reaches transaction trash cleanup without a filesystem fault. Keeping
those recovery responsibilities on the normal recipe prevents an unrelated fault
later in a combined script from hiding binary-reachable writes.

Acceptance delivery evidence receives the binding's actual node identifier
through `recipes.Bindings.LocalNode`. The common constructor resolves and caches
that value on the first receive, without retaining process handles. Notice checks
use this independent identity, never direct-agent `Me` or the message's claimed
sender. Another node's system endpoint therefore cannot discharge a local delivery
obligation; existing header, message, recipient and duplicate checks remain the
same. The direct-registration, session-declaration and second-receive recordings
contain 7, 52 and 100 main points, with fixed budgets 11, 78 and 150.

The adapter Controller exposes typed `CheckpointPut` with the SDK's checkpoint
parameters. Both its local wrapper and its remote control knob share one adapter
implementation that calls the existing `checkpoint.put` protocol method, records
the knob hit and returns the daemon's answer. It does not forward arbitrary
protocol methods. The acceptance session binding uses that same controller.


Terminal lifecycle walks use `harness.TerminalWalkScenario` for real
`daemon.stop` and `identity.retire` requests through the management HTTP client.
They observe the actual exit, read the original persistent result before any
subsequent attempt, and return facts to a domain judgement. A selected crash is
followed by a real next walk generation; a retirement rejection during that
recovery remains that generation's actual trace. Natural exit never marks a
crash target reached.

Shared disk observations live in `testkit/diskfacts`. `InitPreservation`
remembers the first actually present bytes of the identity key, network key,
self-revocation, clock record and the node, policy, presets and instances
configuration files. Missing files supply no baseline until a later `Remember`
finds them. `Check` requires all remembered bytes to remain unchanged. The
certificate and serial remain outside this set because interrupted init may
reissue them. `ReadSigning` instead snapshots all five signing files: the two
keys, self-revocation, certificate and serial. `SigningSnapshot.Unchanged`
compares their private byte copies and reports missing or unreadable files as
errors. Both snapshot types format only as fixed type names, so diagnostic
printing cannot expose retained key or self-revocation bytes.

`InspectTerminal` accepts a read-only filesystem and the binding's actual
stopped-disk store scan. It copies only the result of the supplied operation ID
under `admin.LocalOwner`, preserving its raw bytes and nil absence. It verifies
every kept revocation with `config.DecodeRevocation`, including the signature
and digest filename, and requires `KindNodeSelf` and the actual initialized node
for retirement. Finding a valid self-revocation does not hide later invalid or
unreadable entries. It neither constructs responses or replays nor obtains a
writer to observe these facts. `diskfacts_test.go` covers each protected and
signing path, late appearance, allowed reissue, result ownership and absence,
foreign-node revocations and errors after a valid self-revocation.

After the chain has already terminated, a separate `TerminalAudit` retains an
additional restart attempt's trace and startup/exit errors. Stop queries its
original operation result and replays the same request; retirement verifies a
signed node-bound revocation, identity-retired rejection and unchanged signing
files. Audit traces are checked against the complete table and logged with
`phase=audit`, but never supply walk coverage, targets or additional walk
generations. Audit and domain failures become violations. An actual recovery
refusal is never reclassified as an audit to hide a generation.


Acceptance stop and retirement descriptions live in `Catalog.Terminals` as
`TerminalRecipe`, outside the ordinary script/replay contract. Their shared
`Check(TerminalFacts)` requires actual exit and an original matching persistent
result for every confirmed response. The persistent result is captured before
the additional audit can replay anything. An unanswered retirement interrupted
after its irreversible record may retain no operation result; it needs verified
node-bound retirement, an actual identity-retired recovery refusal and unchanged
signing files. Missing responses and results stay absent. Both execution layers
consume this judgement rather than interpreting startup failure themselves.

`Scenario.Steps` names only post-action boundaries with a live process to kill.
Init and terminal operations have no after-return boundary. Start's `complete`
boundary occurs after quiescence while the daemon is still serving, before
shutdown. Full still selects every recorded non-sync writing point; this removes
only nonexistent process boundaries, never writing-point coverage.

The in-process fixture accepts per-instance `AdapterOptions.InstanceOptions`.
Each entry names an instance already listed by the fixture and supplies its full
manifest and initial disabled state. One normalized descriptor supplies disk
installation, the fake adapter declaration and runtime ownership. Existing
`Runtime` and `ExtraInstances` defaults remain. Memory and real-file-system
preparation use the same descriptors; memory seed keys include each manifest and
disabled state, while an explicit disk is used as supplied. Disabled input is
installed, enabled and disabled through the existing configuration operations
before any host starts, and has no process or probe until real management enables
it. Initially disabled instances are omitted from first-boot predecessor ordering,
so another enabled instance cannot wait for their nonexistent processes.


The maintenance acceptance recipe creates its warning through real adapter
enablement and probing, inside the recipe step. Its fixture input is an installed,
disabled adapter whose actual runtime version falls outside its manifest's tested
range. `ManagementOps.CreateVersionAlert` retains a deterministic enable request
across interruptions, waits for the current process's probe and returns the real
alert identifier. `RequireVersionAlert` owns the shared judgement of all owner
alerts: exactly one `version_untested` alert, naming that enabled instance. Missing,
additional, wrong-kind and wrong-subject facts all fail. A resumed drain completes
the same request and reads persistent facts, so it cannot create a second warning
just to regain an identifier. Alert acknowledgement, backup confirmation and clock
rollback clearance retain their durable-result and effect checks. Its real recording
contains 44 main points and has a fixed budget of 66.

Both execution layers explicitly support the `ProbeWarning` input need. Unknown
damage or filesystem-fault names are unsupported even when a capability list
contains the same unknown value. Package inputs and their real installation stay
with each fixture; the callback adds no arbitrary-alert API or test RPC.


The binary fixture's private scenario environment retains endpoint identities and
recording clients across real daemon generations. Every direct-agent call obtains
the current fake adapter Controller; it never saves an old connection or uses a
session token for a direct endpoint. Addresses use actual registration replies
and public status. A successful direct send confirms the request's canonical
recipients in their original order: the daemon rejects aliases and never rewrites
accepted addresses, including replies, control and keyed repeats. The driver
copies those confirmed recipients without a follow-up status request, preserving
the accepted message identifier even if a later query would fail. Reply routes
and implicit acknowledgement parts come only from received pages. A direct bridge
renders the typed page it presents to its reader; that rendering is its `fakeagent.RecvResult.Raw`, not the daemon's original JSON bytes.
The shared recording wrapper remains the only writer of bridge model observations.
Session-only Agent operations are explicitly unavailable on direct endpoints.
The adapter send result carries no replay flag, so a nonempty direct idempotency
key returns `ReplayUnknown`. An empty key returns `ReplayFresh` because no
idempotency lookup occurs. Keyed calls are sent unchanged and no local cache
infers replay. Recording keeps the real call and confirmed result without
inventing replay evidence.
Real protocol errors and incomplete runtime probes continue to fail the run.

`TestDirectProtocolLifecycleAcrossGeneration` requires fresh evidence from real
unkeyed binary sends on both generations. The real binary regression
`TestDirectControlReplayAcrossGenerationClosesNativeSessionOnce` creates a native
session and sends the unchanged keyed close twice, then kills and waits for the
daemon, starts its replacement and resets the environment before repeating it.
The public control reply must remain closed under the original message identifier,
with `ReplayUnknown` on every keyed response. The external ledger must show the
created native session closed exactly once and no additional adapter close
dispatch after replacement. This preserves the same keyed request used by the
retention close step and its recovery drain. Controller contract tests separately
cover recipient order, reply, control and no follow-up status dependency.

An environment clock change uses the fixture's retained logical target and real
clock RPC. The first RPC failure is retained because `scenario.Env.Set` and
`Advance` cannot return it; quiet and subsequent operations return that error.
Further clock changes are
ignored in that failed generation rather than advancing recovery time.
The backend alone resets this per-generation failure, after validating an actual
exit and selecting a distinct daemon process. No successful RPC silently clears
an earlier failure or serves as evidence of a crash target.

Normal binary scripts use a private real CLI initialization before observed
generation 1. It owns Start, one Wait, file output and cancellation cleanup through
the same process launcher as observed commands, with observer variables removed
and no observed-generation registration. Public observed `Init` still requires
positive unique generation numbers. Deterministic node seeds distinguish scenario,
process role and observed generation; native runtime seeds distinguish scenario
and adapter instance while remaining stable across daemon restarts.

The binary backend finishes each interrupted generation by recording the existing
`fixture.crash` event before starting the replacement. A writing-point crash
requires its matching actual trace marker and abnormal process exit; an
after-step crash requires the successful actual kill and completed wait. The event
names that point or step and the exact daemon PID. The separate `fixture.kill`
fact remains for an after-step kill. Natural exit, an ordinary stop for offline
damage and a failed or unreached target produce no crash event. This lets the
shared retention recipe recognize its interrupted script and run its existing
recovery drain; final record, endpoint and archive judgements remain unchanged.

Private binary input profiles preserve the public fixture's direct-only default.
They describe CLI-only runtime declarations, extra instance packages and a warning package
installed but not enabled. Runtime inputs execute the built `sidera probe --json`
for every requested preset, including init's existing default, with each request's
environment. A new generation must present its newly issued tokens; CLI success
makes no MCP claim. `TestRuntimeProfileProbesAcrossGeneration` verifies both the
first start and the replacement generation, with default and additional presets;
`TestSuccessfulCommandWithoutTokenPresentationDoesNotPassProbe` keeps a successful
command that never presents the token pending. Bootstrap still retains each exact management request
and its progress across generations. Package inputs are real caller-owned source
directories, extensible before installation; no profile writes installed packages
or daemon business state directly.

The binary management and installation bindings use the real management client.
Each successful change is checked against public `operation.result`, then resent
with its original identifier, expected version and params to require an unchanged
persistent replay. Version-conflict and unsupported-platform refusals must leave
that identifier unknown. Facts come from public lists or read-only decoding of
actual configuration, clock, package and endpoint files; a token is checked only
on disk because the management interface never publishes one.

The warning callback retains its enable request before sending it and resumes
that same request after an interrupted commit. It waits for the real warning
adapter's probe and quiet, then applies the shared unique `version_untested`
judgement. Installation retains completed caller-owned source paths and extends
only those inputs with nested data or a foreign platform. The public installer
owns installed state. Pointer-fault callbacks explicitly fail as unavailable in
the binary layer, consistent with capability selection.

`TestBinaryManagementBindingsPreserveRequestsAndEffects` exercises changes and
refusals through the running daemon, including same-request replay after actual
kill and replacement. `TestBinaryVersionAlertResumesCommittedEnable` cuts the
real adapter enable commit, verifies the target and abnormal exit, and resumes
the original request before acknowledging its real alert.
`TestBinaryInstallationBindingsUpgradeAndReplayAcrossGeneration` checks real
upgrades, current-version installation, platform refusal without residue and
persistent replay after kill and restart.

The runtime Controller's read-only `SessionEnv(ref)` returns a copy of the
actual environment injected into an existing live native session. The local
runtime clones under its session lock with `maps.Clone`; unknown references
return `ErrUnknownSession`, and closed or killed sessions return `ErrNotAlive`.
The remote controller selects the existing runtime control connection by native
reference, rejects other process roles and strictly decodes `session.env`'s typed
`{ref, env}` response, including a matching reference. Missing connections follow
the controller's existing bounded unknown-session error path; there is no cached
fallback. An absent environment is represented by an empty map on the wire.
The getter neither starts a turn nor changes plans, and adds no daemon or adapter
production method. Returned credentials go only to the existing CLI Agent's
process environment; the getter writes no ledger entry, record or diagnostic,
and decoding failures expose no response contents.

`TestSessionEnvReturnsOnlyTheLiveSessionsActualEnvironment` exercises actual
values, copy isolation, two live sessions, unknown and closed references and
private outputs against both implementations. `TestSessionEnvRejectsUntrustedControlReplies`
and `TestSessionEnvDoesNotQueryAnAdapterWithTheSameID` cover the control boundary;
the new golden link replies also seed the existing decoder fuzz target.
`TestSessionEnvDrivesRealCLIAcrossDaemonGeneration` creates a real native session
through public endpoints and control, then uses its actual environment for real
CLI `Me`, declaration, receive and acknowledgement on two daemon generations.
The transition kills and waits for the first daemon before starting its replacement;
the native runtime remains the credential owner. The regression scans the external
ledger, native records, static inputs, traces and diagnostic output for the token.
It makes no claim that complete session recipe bindings are implemented.

### Real-filesystem transaction benchmarks

`BenchmarkTransactionWrites` lives entirely in the in-process acceptance
package's test files. Its private fixture table invokes existing owner operations
on `fsx.OpenOS` or `harness.RealFS`; preparation and read-back checks run outside
the measurement. The existing `txn.Observer` measures the first actual filesystem
step through logical commit, and reports `ns/write-window` with `commits/op`.
The storage reference defines the [measurement boundary](../storage/README.md#transaction-write-window-benchmarks).
No shared measurement API or crash-walk backend is involved.

The denominator is the complete `daemon.Table`, grouped by each owner's existing
declarations. All 53 declarations have fixtures: txn 2, config 8, keys 2, store 32,
engine 7 and daemon 2. A selected declaration without a fixture, nonempty write
window or successful commit fails. Unknown and duplicate fixture names fail as well.
Ordinary Go sub-benchmark selectors can validate an owner subset, such as
`-bench '^BenchmarkTransactionWrites$/txn$'`; a subset is not complete-table
sampling or crash coverage. Run without a sub-benchmark filter for the complete
set, using `-run '^$' -benchtime=1x -count=1`. The benchmark suppresses the default
trigger-latency `ns/op`, whose engine scheduling and waiting would describe a
different interval. One iteration is a platform cost record with no performance
threshold.
