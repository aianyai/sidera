# Daemon

How the node is put together and taken apart: the order of the start and of the
shutdown, the instance lock and the checks of the data directory, `init`, the
periodic tasks, the logs, and the management interface with the way it hands the
owner's requests to the engine. Code:
[`internal/node/daemon`](../../../internal/node/daemon/README.md) (the composition
root), [`manageapi`](../../../internal/node/manageapi/README.md),
[`apiwire`](../../../internal/node/apiwire/README.md) and
[`logfile`](../../../internal/node/logfile/README.md). The engine's side of the
administrative rules is in [engine](../engine/README.md); the rules themselves in
[administration](../administration/README.md).

## What

### The start

`daemon.Start` takes fifteen steps in this order. Each logs `startup step N begin`
and `startup step N end` (with the name of the step) to `logs/daemon.log`, so the
order is readable from the log; steps 11 to 14 are the engine's and log their own.
A step that fails stops the start and what had begun is shut down in the order of
the shutdown below.

| Step | Name | What happens | Owner |
| ---- | ---- | ------------ | ----- |
| 1 | lock | The data directory is resolved; the root and `keys/` are checked to be private to the owner (a directory that is not one, a symbolic link among them, is refused and nothing is narrowed); `keys/init.rec` must exist; then the instance lock `daemon.lock` is taken, and a holder is reported as "a daemon is already running". | this package |
| 2 | config | The transaction layer recovers (settle, then removal of every `.tmp-*`); the layout of `state/`, `run/`, `manage/` and `logs/` is made again where the owner removed it; the log opens; `config/` is loaded strictly and a file that is not valid stops the start and is named. | this package, `txn`, `config` |
| 3 | keys | Keys, the revocations the owner kept and the serial seen are loaded; a node whose own identity was retired refuses to run. Nothing is signed. | this package, `keys` |
| 4 | clock | The wall clock is judged against the saved high water mark, before anything is signed. A clock found behind it by more than `timing.ClockTolerance` pauses the node: the pause is made durable at once with the mark that was, which is never lowered. | this package, `clock` |
| 5 | certificate | Renewal of the certificate when it has ten days or less left, never while the clock is in doubt. | the owner of renewal (empty here) |
| 6 | scan | The engine's first recovery entry: `state/` is scanned, damaged files are quarantined, the recovery rules run until none has work, the view is built. The periodic tasks are registered. | `engine` |
| 7 | trust | Hook `TrustSync`: synchronisation of revocations and the trust graph, the gate of vouchers. Empty for a node of one machine. | later stage |
| 8 | manage | The loop starts in the phase of recovery and the management interface opens: reads answer, anything that changes state is refused as `unavailable/recovering`. | this package |
| 9 | agent-probe | Hook `AgentProbeOnly`: the Agent interface opens so that it takes only the one-use probe token. | the Agent interface |
| 10 | adapters | Hook `Adapters`: the enabled adapter instances are started; it returns when each has answered `adapter.start` or been judged offline. | the adapter host |
| 11 to 14 | | The engine's second recovery entry: creation intents and prompt attempts are checked, every record is ticked, feedback that fell due goes out, the loopback goes on. After step 10 on purpose: a lease is released only when no turn runs. | `engine` |
| 15 | serve | The engine serves; hook `AgentServe`: the Agent interface switches to full service. | the Agent interface |

A hook runs inside its step, in the state of the engine the step needs (before the
loop exists at step 7, recovering at 9 and 10, running at 15) and is given an `Env`:
the data directory, the clock, the randomness, the log, the transaction writer, the
configuration, the keys, the engine, the handle for adapter logs, and
`RequestStop`.

### The shutdown

One path for an orderly stop, for a fatal failure of the disk and for the unwinding
of a start that failed; a step with nothing to stop is logged and does nothing.

| Step | Name | What happens |
| ---- | ---- | ------------ |
| 1 | refuse | The engine refuses what comes from outside (`unavailable/shutting_down`). |
| 2 | interfaces | The management interface stops taking connections and waits for the requests being served, within 10 seconds; hook `StopInterfaces` stops the Agent interface. |
| 3 | adapters | Hook `StopAdapters`. Native sessions are separate processes and are not affected. |
| 4 | engine | The loop ends after its command, and every goroutine of the engine is waited for. |
| 5 | release | The last line is logged, the log is closed and the lock is let go: the lock goes last, because while it is held no other daemon starts over state this one is still writing. |

After a fatal storage error the engine has ended and written nothing more; the same
steps run with 2 seconds instead of 10, and the daemon ends with a `FatalError`:
exit code 74, so the next start scans the disk again. `RequestStop` is the one entry
for every cause (the management interface after its answer, a signal, the program),
from any goroutine, any number of times; the shutdown runs once.

### init

`daemon.Init` makes the data directory in one transaction (`init`) under the
instance lock, which it takes first, so it cannot run beside a daemon or another
init: the root only the owner can reach, the layout of every owner, the keys, the
certificate, the self-revocation, the clock record, the default configuration (one
preset, `default`, open to local sources) and, last, `keys/init.rec`. It never
overwrites. A file that is there and valid is kept byte for byte; one that is there
and not valid stops it, naming it. An init that was interrupted is finished by
running it again, and one that finished is refused (`ErrAlreadyInitialized`) with
not a byte changed. The lengths of the socket paths are judged by `ipc.Locate`
before the first directory is made, and a root that exists with a mode others can
reach is refused, not narrowed.

What the owner can do to the directory afterwards, and what follows:

- Delete `state/`: the start makes the layout again and the node runs with nothing
  in flight. Deliveries in flight are lost, and so is every session endpoint's
  token and mapping, so every native session leaves the control of the node. The
  stable endpoint identifiers are in `config/` and stay.
- Delete `config/`: the start refuses, naming what is missing, and so does init
  (`init.rec` is there). That closes the road on which a retired identity would come
  back because `config/revocations/` was lost; the owner restores `config/` or
  starts over in a new directory.
- Delete `keys/` and run init again: a new identity, certificate and
  self-revocation; the owner's settings, presets, instances, installed adapters and
  stable endpoint identifiers are kept; the revocations of the old identity stay
  and do not touch the new one.
- Two results are not the same after `state/` is deleted: a close of an endpoint
  that is gone says it is unknown, where the first answer said closed. The rule
  that one identifier has one result holds for every result that is still stored.

### The instance lock and the permissions

The root and `keys/` are checked before anything is written, before the lock is
taken and before a socket is made, so a refused start leaves no lock file. The
check is the file layer's `OwnerOnly`, which fails closed: on Unix any group or
other bit, on Windows a DACL that grants anyone but the owner, SYSTEM and
Administrators. The root is read without following a symbolic link, so a link is
not a directory and is refused, whatever it points to (a link above the data
directory is followed: macOS's temporary directory is one). `keys/` and its files
are checked by `keys.CheckPermissions`. The lock is `daemon.lock` at the root, not
in `state/`, which can be deleted as a whole.

### The layout rule

`layout` is a recovery transaction of the start: it makes the directories of
`state/` and of `run/`, `manage/` and `logs/` that are missing, a parent before its
children, and has no work on a data directory that init made or a start repaired
(`LayoutWork` is the predicate). It does not make `keys/` or `config/` again.

### Periodic tasks

The tasks are entries of the engine's schedule on the injected clock, registered
at step 6. The daemon's own is the clock state, every minute (`ClockStateEvery`):
the high water mark and the pause are made durable, and a crash loses at most a
minute of a mark, which only makes the next start trust the clock less. The engine
also runs it at once when it finds the clock set back. Other features add theirs in
`Attachments.Periodic`.

### The logs

The daemon's log is `logs/daemon.log`, written through `logfile`, a handle that
can open, rotate and remove logs under `logs/` and nothing else, under the reserved
`log` tag, so a log write is never a crash point. Each log is rotated to one file,
`.1`, at a size that shares the node's `log_total_bytes` among two files for the
daemon and for each enabled instance, never below 1 MiB. The handle owns that rule:
it is given the total and the source of the number of enabled instances once
(`logfile.Budget`) and reads both at every write, so the daemon and the adapter host
open their logs by name and neither counts instances or divides the total. Lines
logged before the log can be opened (steps 1 and 2) are kept and written first, with
the attributes and groups of the logger that wrote them and only if the log is at
the level that takes them. Lines carry the time of the injected clock when they are
written, so the lines kept from before the log opened carry the time it opened, not
the moment they were logged. Never a message body, a token or a key.

### The management interface

HTTP/1.1 over `manage/manage.sock` (a named pipe on Windows), `POST
/v1/manage/<method>`, JSON. A request is read in this order:

1. The method is found (`404 not_found/method_unknown`) and the verb is POST (`405`).
2. `admin.Authorize` for the local owner against the catalogue: only an operation the
   catalogue does not know is denied, which is a defect of the program
   (`500 internal/unknown_operation`).
3. The body, at most 1 MiB (`400 invalid_argument/request_too_large`), is decoded
   strictly: an unknown member, a repeated key, `null`, a key of another case or a
   value of another type is `malformed_request` with the path of the fault where the
   decoder can place it. A request that changes state is
   `{"operation_id": <32 lower-case hex digits, not all zero>, "expected_version"?: n,
   "params"?: {...}}`; a read is `{"params"?: {...}}`.
4. `admin.ValidateRequest`: a request that changes state carries an identifier
   (`missing_operation_id`), a change of a versioned setting carries its expected
   version (`missing_expected_version`), and any other request carries none
   (`unexpected_version`).
5. A read is answered from the engine. A request that changes state is one
   `engine.Execute` command: the key `admin.KeyFor(owner, id)`, the fingerprint, the
   expected version, the name of the method and the operation. The replay check, the
   version check and the storing of the result are the engine's and `config`'s; the
   interface does none of them.

The fingerprint is `SHA-256("sidera-manage-v1" NUL method NUL canonical params NUL
decimal expected version)`; the canonical params are the decoded params encoded again
with the fields of their type in declared order, so a resend written with another
order or spacing is still a resend. The answer to a request that changes state is
`{"operation_id","replayed","result"}`, the result being the one stored; a read
answers `{"result"}`. When an operation stops the daemon the answer is flushed and
then the stop is requested, once: the client has its answer before the daemon goes.
The server has no write timeout, because a request is the client's connection and
ends with it; a request whose client goes away has its context cancelled.

The methods the daemon has:

| Method | Operation | Result |
| ------ | --------- | ------ |
| `endpoints.list` | `OpRead`; params `{"instance"?}` | `{"endpoints":[{address, kind, instance, state, queued, draining}]}`, never a token |
| `endpoints.close` | `OpManageEndpointSession`; params `{"endpoint": "<node>/<endpoint>"}` | `{"endpoint","outcome"}`, outcome `closed`, `closing` or `already_closed`; a runtime endpoint is refused `endpoint_kind`, an address of another node is `endpoint_unknown` |
| `daemon.stop` | `OpStopDaemon`; no params | `{"stopping":true}`; the daemon then shuts down; a resend, even to a daemon that was started again, asks for the stop again |

Errors are `{"error":{"category","code","message","details"?}}` in the words of
`apiwire`, which is the one mapping from what the engine, `config`, `ipc` and the
decoder say: `invalid_argument` 400, `unauthenticated` 401, `not_found` 404,
`conflict` 409, `refused` 422, `backpressure` 429, `unavailable` 503, `internal`
500, and the exit codes of the command line 3 to 9 in that order, 1 for internal, 2
for a command used wrongly, 10 for a request sent without an answer coming back.
The text of an error nobody expected is logged and never sent.

Who the local owner is: whoever connected to the management listener, which is
the user's own socket or pipe, checked by `ipc` on every accept. `manageapi.Listen`
takes only that address, and `Server.Serve` only a listener that `Listen` made, so
the actor of the owner is made in that one package and a scan keeps every other file
of the runtime tree and of `cmd/` from making it. There is no token on this
interface: a sandbox must deny the socket to an agent.

### What the later features attach to

| Hook | Step | Attached by |
| ---- | ---- | ----------- |
| `TrustSync` | 7 | trust synchronisation |
| `AgentProbeOnly`, `AgentServe`, `StopInterfaces` | 9, 15, shutdown 2 | the Agent interface |
| `Adapters`, `StopAdapters` | 10, shutdown 3 | the adapter host |
| `Periodic` | 6 | any feature with a task on a period (certificate check hourly, archive sweep, disk polling) |
| a row of `manageapi`'s method table, and a type of `engine.Operation` | | the owner of an operation |

## Why

- **One door for administrative operations, in the engine.** The rule that one
  identifier has one result needs the lookup and the storing to be one step of the
  single writer, or two concurrent resends both run. A check in the interface, or in
  each operation, is a second home that drifts; the interface authorizes and
  validates, the engine does the rest. Rejected: a replay check in `manageapi`
  before the call, which would be a second copy and would not see the results that
  the versioned files keep.
- **The permissions are refused, never narrowed, and before the lock.** Only the
  owner knows who should read the keys; a daemon that narrowed a mode would hide a
  breach. Checking before the lock keeps a refused start from changing the directory
  at all. A symbolic link is refused because it can be swapped between the check and
  the use, and what it points to is not what `init` made.
- **init is one transaction that never overwrites,** and takes the lock first. The
  marker is last, so an init that stopped is finished by running it again, and the
  keys, which cannot be made again, are never replaced.
- **The layout rule instead of a mkdir at every start.** It is a declared recovery
  transaction, so the crash tests walk it, and it has no work on a clean directory,
  which a test asserts. It leaves `keys/` and `config/` alone: a missing one is the
  owner removing what `init` made, and refusing is what keeps a retired identity
  from coming back.
- **The clock is judged at step 4, before the step that signs.** A node that signed on
  a clock set back would issue what peers refuse, or worse, what they accept in the
  past. The pause is made durable before the loop runs, with the mark that was.
- **The budget lives in the handle.** The share of each log depends on the number of
  enabled instances at the moment of the write. Had the daemon passed a threshold,
  the adapter host would have had to count instances too, and two counts drift.
  Rejected: a threshold given to `Open`, which is what that was.
- **Logs through a restricted handle, not through a transaction.** Logs are written
  by many goroutines and lost harmlessly; as crash points they would make the
  numbering of the real writes depend on how much was logged. The handle has no
  operation but a log's, so the exception cannot be used for state. The time in a log
  is the injected clock's, which tests control and production does not distinguish.
- **Hooks are functions with an `Env`, not interfaces,** because the later features
  differ in shape (an interface that serves, a host that starts processes, a task)
  and each needs only the part of the node that is up at its step. Rejected: the
  composition root knowing each feature's types, which would make every feature a
  change to it.
- **The shutdown is one path.** An orderly stop, a fatal failure and a start that
  failed all take the same five steps, so the order is tested once and a step cannot
  be skipped on one road. The lock is the last thing let go.
- **`daemon.stop` through the management interface,** because Windows has no portable
  way to ask a background process for a normal shutdown, and ending it would be a
  crash. The stop is requested after the answer is flushed, so a client is not left
  without one, and a resend of the request stops a daemon that died between storing
  the result and stopping.
- **A fingerprint of the canonical params, in the interface.** The engine takes the
  value and never reads a request, so one rule decides when two requests are one.
- **A new package for the words of the wire** (`apiwire`) rather than strings in the
  engine: the engine says what happened in types, and every interface and client maps
  through one table, so a reason has one pair of words everywhere.

## Where

- Code: `internal/node/daemon` (`daemon.go`, `start.go`, `stop.go`, `init.go`,
  `home.go`, `table.go`, `logging.go`, `periodic.go`), `internal/node/manageapi`
  (`manageapi.go`, `methods.go`), `internal/node/apiwire`, `internal/node/logfile`,
  `internal/node/engine/manage.go`.
- Tests of the daemon: `TestSecondDaemonOnSameHomeRefused`,
  `TestInitOnTheHomeOfARunningDaemonIsRefused`,
  `TestStartupStepsRunInTheirFixedOrder`,
  `TestEachHookRunsInsideItsStepWithTheEngineInTheStateTheStepNeeds`,
  `TestTheManagementInterfaceReadsAndRefusesChangesWhileTheNodeStarts`,
  `TestAHookThatFailsStopsTheStartAndLeavesNothingRunning`,
  `TestTheStartMakesTheLayoutTheOwnerRemovedAgain`,
  `TestTheDaemonLogsToTheFileUnderLogsWithTheTimeOfItsClock`,
  `TestThePeriodicTasksRunEveryPeriodOnTheInjectedClock`,
  `TestAClockSetBackIsPausedAtStartAndThePauseAndTheMarkAreKept`,
  `TestShutdownRunsInItsFixedOrder`, `TestRequestStopRunsTheShutdownOnce`,
  `TestTheOwnerStopsTheDaemonThroughTheManagementInterface`,
  `TestRunReturnsAfterANormalShutdownWhenItsContextEnds`,
  `TestAFatalStorageErrorEndsTheDaemonWithTheExitCodeOfAnIOError`,
  `TestAReplayedManagementOperationReturnsTheStoredResultAndAReusedIdentifierIsRefused`.
- Tests of init and the layout: `TestInitMakesTheDataDirectoryWithItsLayoutKeysAndConfiguration`,
  `TestInitRefusesAnInitializedHomeAndChangesNothing`,
  `TestInitTakesTheInstanceLockAndLetsItGo`,
  `TestInitRefusesAPathTheSocketsCannotUseBeforeMakingAnything`,
  `TestStartRefusesAHomeThatInitDidNotMake`,
  `TestInitReplaysToACompleteHomeFromEveryCrashState`,
  `TestEveryDeclaredCrashPointOfInitAndLayoutIsReached`,
  `TestTheLayoutRuleHasNoWorkOnAHomeThatInitMade`,
  `TestInitAfterTheOwnerDeletedKeysMakesANewIdentityAndKeepsTheConfiguration`,
  `TestAHomeWhoseConfigurationWasDeletedIsNeitherStartedNorInitializedAgain`.
- Tests of the permissions: `TestStartRefusesADataDirectoryThatOthersCanReach`,
  `TestInitRefusesAnExistingDataDirectoryThatOthersCanReach`,
  `TestStartAndInitRefuseASymbolicLinkAsTheDataDirectory`,
  `TestStartRefusesKeysThatOthersCanReach`, `TestStartRefusesAKeyThatIsASymbolicLink`,
  `TestThePermissionsAreJudgedBeforeTheLockIsTaken`; Windows:
  `TestWindowsADataDirectoryThatEveryoneCanReadIsRefused`,
  `TestWindowsASymbolicLinkAsTheDataDirectoryIsRefused`.
- Tests of the management interface (`internal/node/manageapi`):
  `TestOnlyAPostToAKnownMethodIsTaken`, `TestARequestOverTheLimitIsRefused`,
  `TestAMalformedRequestIsRefusedNamingThePositionOfTheFault`,
  `TestTheOperationIdentifierOfARequestThatChangesSomething`,
  `TestAnExpectedVersionIsRefusedWhereNoneIsCarried`,
  `TestAChangeOfASettingNeedsItsExpectedVersion`,
  `TestAnOperationOutsideTheCatalogueIsNeverRun`, `TestTheCommandTheEngineIsAsked`,
  `TestEqualRequestsHaveOneFingerprintAndDifferentOnesDoNot`,
  `TestTheAnswerOfARequestThatChangesSomething`, `TestAReadAnswersItsResult`,
  `TestStopIsRequestedAfterTheAnswerIsWrittenAndOnlyForAnOperationThatStops`,
  `TestRefusalsAreAnsweredInTheWordsOfTheWireAndUnknownErrorsStayInTheLog`,
  `TestAClientThatGoesAwayCancelsTheRequest`,
  `TestListenRefusesTheAddressOfTheAgentInterface`,
  `TestShutdownWaitsForARequestAndLeavesNoGoroutine`, `TestTheServerTimeouts`,
  `TestConcurrentRequestsAreAnsweredEachItsOwn`, `FuzzDecodeRequest`.
- Tests of the words of the wire (`internal/node/apiwire`) and of the log
  (`internal/node/logfile`): see their READMEs. The scans that keep the local owner
  and the local source where they belong: `internal/node/layout_test.go`.
- Pending verification by CI on main: the two Windows tests above (the first widens
  the access list of the data directory and needs the CI's account; the second needs
  the privilege to make a symbolic link), and everything of the management interface
  on a Windows named pipe; this machine runs the Unix paths only.
