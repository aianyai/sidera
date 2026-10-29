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
| `fakeagent/engineagent` | The engine-layer backend of the fake agent: the engine's service interface called in process. Ten methods call the engine; Probe needs a credential this caller-bound backend does not hold. |
| `scenario` | The language of test scripts, the same in every layer: scripts, the driver, the environment interface a fixture implements, and the judgements that read the ledger. |
| `harness` | The engine-layer fixture: the engine on the real store, configuration and transaction layer over the in-memory file system, one test clock, the ledger, the agents, quiet, process and power-loss crashes and restarts, fault injection, the in-process crash-walk backend, and the generator of control signals for model tests. |
| `acceptance/agent/mcp` | The MCP backend of the fake agent: independently encoded exchanges for both supported revisions, exact receive text, and tool errors mapped to the scenario categories. It takes process pipes and imports neither the production MCP server nor an MCP SDK. |
| `acceptance/recipes`, `acceptance/inproc` | Fixture-independent acceptance descriptions and their in-process binding; fresh closures per run, real lifecycle entry points and externally evidenced domain checks. |
| `crashwalk` | The core of the crash-point walk: which points to kill at in each mode, how a rerun is held to its recording, scenario budgets, coverage of the declared writes, and the crashing of recovery itself. |

The first four, with the two programs, are the **third-party side**: they import the
standard library, the SDK module and each other and nothing else. `fakeagent`,
`engineagent`, `scenario`, `harness` and `crashwalk` are the **Sidera side** and may
import any runtime or contract package; `scenario` alone imports neither the engine
nor a fixture.

The backends of the fake agent that run the command-line client and speak to the MCP
server, the binary-layer fixture and the binary backend of the crash walk build on
these packages and are not part of them.

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

The installation acceptance recipe exercises a real first installation, an
upgrade, reinstalling the current version, platform refusal, and recoverable
pointer and cleanup failures. Its package inputs pass through the fixture's
package-source callback. The engine remains responsible for every node write.
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
corresponding feedback. Its clean record writes the instance endpoints first and
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
  restart and a crash are those two in a row. The in-memory state a process starts with
  (the clock's high-water mark, the rollback pause an option asks for) is applied from
  zero at each start, because a process has no memory of the last one.
- **Fallible fixture calls share one current-process lookup.** `onProcess`,
  `Records`, `VerifyReport`, `VerifyControlReply` and `quiesce` return
  `engineagent.ErrNodeNotRunning` when there is no running process, before the
  first Start or after Stop/Kill. `Quiesce` retains its diagnostic wrapper. A
  missing process is never an empty successful scan or verification. Quiescence
  captures the process once and probes that engine throughout the wait, so a
  concurrent cutoff cannot turn a later lookup into `Fatal`. Accessors with no
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
| Engine fixture | `internal/node/testkit/harness` | `TestOneClockIsTheSourceOfTimeForTheEngineAndTheFakes`, `TestADirectEndpointRegisteredThroughTheFixtureAppearsInTheEnginesList`, `TestQuiesceReturnsOnlyWhenTheEnginesConditionsHoldAndOtherwiseFailsWithAReport`, `TestAProcessCrashThenRestartKeepsExactlyWhatWasDurable`, `TestAPowerLossRestartIsReproducibleUnderAFixedSeedAndKeepsWhatWasAcknowledged`, `TestFaultsAreInjectedThroughTheFileLayerAndTheirHitsCounted`, `TestAScenarioScriptRunsThroughEnvOnTheEngineLayer`, `TestACallInFlightAtACrashHasAnUnknownOutcomeAndLeavesNoEffectOfTheAbandonedProcess`, `TestADirectCallThatWasInFlightAtACrashIsNotRecordedUnderItsName`, `TestAProcessCrashKeepsAWriteThatWasNotSyncedYet`, `TestAStoppedNodeIsDownAndAFileChangedOfflineIsWhatTheNextStartReads`, `TestAFaultArmedForTheNextStartAppliesToThatProcess`, `TestAKilledNodeIsDownOnTheDiskTheCrashLeftUntilItIsStarted`, `TestACrashIsWrittenToTheLedgerWithItsModeAndSeed`, `TestACrashThatNamesItsPointIsWrittenWithThePoint`, `TestNoSecondWriterAfterAdapterRestart`, `TestACrashCutsTheAdapterWithoutAShutdownAndACleanRestartShutsItDown`, `TestQuiesceWaitsUntilTheEngineHasHandledEveryNotificationTheAdapterSent`, `TestTheAdapterOfTheFixtureIsTakenOnThroughTheHost`, `TestAnExtraInstanceHasItsOwnAdapterRuntimeAndEndpoints`, `TestACallToTheAdapterHostThatReturnsAfterTheProcessWasAbandonedIsNeverAConfirmation`, `TestQuiesceDoesNotReturnWhileAConditionThatKeepsHoldingStillMakesProgress`, `TestQuiesceReturnsAfterOneRoundWhenNoCountMoves`, `TestAQuiesceErrorSaysACommandIsRunningOnlyWhenTheLoopDidNotAnswer`, `TestTheLeakCheckReportsAGoroutineOfTheMarkedPackages`, `TestControlGenReachesEveryPathAndEveryControlRecordCloses` |
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

The bounded in-process acceptance runner uses these same source and target
checks directly. It owns only catalog execution and diagnostics. Its cost is the
clean catalog plus unique complete recovery triggers, static Recovery
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

Recovery-creation acceptance retains actual successful control results and reads
their public status. A validated creation reply supplies the initial message's
session target; the external creation attempt must confirm a native session for
a created outcome. The requested body must appear there with that control's
identifier, or the sender must have received the corresponding terminal notice.
An absent reply requires both a terminal control result and actual feedback for
the runtime target. Final disk convergence remains an additional audit.

Interrupted acceptance scripts distinguish completed calls from future steps.
A successful registration does not imply a declaration was requested. A session
send owes its body immediately, while blocked/recovered feedback is required
only for a blocking phase that began; an interrupted blocking phase is finished
before delivery is restored. Lifecycle drains receive and acknowledge pending
session messages, or obtain the real feedback caused by a requested closure.
Ordinary session resolution captures evidence while its source exists, so later
retention does not create a dependency on an already removed status record.
