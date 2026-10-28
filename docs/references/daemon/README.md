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
the shutdown below. The context of `Start` is checked before every step: when it
ends (`Run` cancels it on SIGINT or SIGTERM, which it takes from before the first
step) the start stops between two steps, unwinds the same way, and the error wraps
the error of the context. A start that stopped because of a fatal failure of the
disk, in whichever step it met it, ends as a fatal failure does (see the shutdown).

| Step | Name | What happens | Owner |
| ---- | ---- | ------------ | ----- |
| 1 | lock | The data directory is resolved; the root and `keys/` are checked to be private to the owner (a directory that is not one, a symbolic link among them, is refused and nothing is narrowed); `keys/init.rec` must exist; then the instance lock `daemon.lock` is taken, and a holder is reported as "a daemon is already running". | this package |
| 2 | config | The transaction layer recovers (settle, then removal of every `.tmp-*`); the layout of `state/`, `run/`, `manage/` and `logs/` is made again where the owner removed it; the log opens; `config/` is loaded strictly and a file that is not valid stops the start and is named. | this package, `txn`, `config` |
| 3 | keys | Keys, the revocations the owner kept and the serial seen are loaded; a node whose own identity was retired refuses to run. Nothing is signed. | this package, `keys` |
| 4 | clock | The wall clock is judged against the saved high water mark, before anything is signed. A clock found behind it by more than `timing.ClockTolerance` pauses the node: the pause is made durable at once with the mark that was, which is never lowered. | this package, `clock` |
| 5 | certificate | The certificate is renewed when it has ten days or less left, when it has expired, or when an earlier renewal was interrupted; never while the clock is in doubt (the pause step 4 found is respected: a certificate that is due waits, and the log says so). The judgement is the engine's (`engine.SignRefusalOf`), the same one the hourly check is handed; at this step no identity is retired, because step 3 refused it. | this package, `keys`, `engine` |
| 6 | scan | The engine's first recovery entry: `state/` is scanned, damaged files are quarantined, the recovery rules run until none has work, the view is built. The loopback is set as the engine's transport, with no knobs, before the loop starts. The periodic tasks are registered. | `engine`, `transport` |
| 7 | trust | Hook `TrustSync`: synchronisation of revocations and the trust graph, the gate of vouchers. Empty for a node of one machine. | later stage |
| 8 | manage | The loop starts in the phase of recovery and the management interface opens: reads answer, anything that changes state is refused as `unavailable/recovering`. | this package |
| 9 | agent-probe | Open the Agent listener in probe-only mode, accepting only the one-use probe credential; then run hook `AgentProbeOnly`. | this package, `agentapi` |
| 10 | adapters | The adapter host (`Config.AdapterHost`, when set) starts the enabled adapter instances and the step waits until each has answered `adapter.start` or been judged offline; then hook `Adapters` runs, for what else starts here. | `adapterhost`, `apiwire` |
| 11 to 14 | | The engine's second recovery entry: creation intents and prompt attempts are checked, every record is ticked, feedback that fell due goes out, the outbound worker goes on over the loopback. After step 10 on purpose: step 11 asks the connected adapters about creations, closes and interrupts, releases and prompts, and the busy state they reported is used to renew leases after serving. Until step 12 the engine's schedule holds back the ticks, the feedback and the looks at source records that fell due, so none of them runs before the adapters have reported. | `engine` |
| 15 | serve | The engine serves, then the Agent server enables endpoint methods; hook `AgentServe` runs afterward. | this package, `agentapi` |

A hook runs inside its step, in the state of the engine the step needs (before the
loop exists at step 7, recovering at 9 and 10, running at 15) and is given an `Env`:
the data directory, the clock, the randomness, the log, read-only views of the
configuration and of the keys (the identity and the self-revocation: what no renewal
of the certificate changes, because a hook runs beside the loop, and no signer, because
every signature is the engine's and the engine refuses it while the clock is in doubt or
the identity is retired; the certificate and the serial are read through the engine), the engine, the assertion that the instance lock is
held (what `ipc.Listen` asks of whoever listens on an address of this home), the
handle for adapter logs, and `RequestStop`. It holds no transaction writer: the
engine is the single writer, and a hook that changes state asks it.

### The adapter host

`Config.AdapterHost` is the host that runs the adapter instances the owner enabled
(`adapterhost.Host`); nil is a node that starts none. The daemon wires it in three
places and nowhere else:

- **Step 6** gives it to the engine as `Deps.Adapters`, only when it is not nil (a nil
  host inside the interface would not be seen as absent). `daemon` is where the two
  layers meet: `var _ engine.Adapters = (*adapterhost.Host)(nil)` is here.
- **Step 10** starts it with the data directory (`Env.Home`, the absolute path of the
  real directory, which the instance lock files `adapter-<instance>.lock` are in, at
  its root), the read-only view of the configuration the hooks get (`Env.Source`),
  the handle of the logs (`Env.Logs`: each instance's `adapter-<instance>.log`) and
  the engine behind `apiwire.AdapterEngine`, which words the engine's refusals as the
  adapter protocol does. The step waits for `Start`: it returns when each enabled
  instance has answered `adapter.start`, has failed its first attempt (judged
  offline, reported, and tried again after a backoff) or the host's start wait has
  passed. An instance that cannot be taken on does not stop the start. Only a host
  that cannot be started at all does (a bad `Env`, a host that was started or ended
  before). The `Adapters` hook runs after it, in the same step.
- **Shutdown step 3** shuts the host down with the step's context, which carries the
  grace: each adapter in service is sent `shutdown`, its stream is closed, and a
  process that stays is killed. A failure (the grace passed) is logged as a warning,
  as every step's is, and the hook `StopAdapters` runs after it. The step runs after
  the engine began to refuse (step 1) and before the engine ends (step 4): the
  requests an adapter makes meanwhile are answered `-32002` (shutting down). On a
  fatal failure of the disk the same step runs with the short grace, and nothing new
  is written: the host reports nothing to the engine at shutdown.

A host is for one run of one daemon: it cannot start again after it ended, so a
caller that starts the daemon again makes another.

An instance the owner enables while the daemon runs is started the same way: the
engine asks the host (`EnsureRunning`) once the change of the configuration is on
disk, and the host reads the instance and its installed adapter from the same
configuration the engine changed. One the owner disables is stopped (`Stop`), and
because the host reports nothing when it stops an instance, the engine marks it
offline itself. The package an owner installs is read through the reader that this
package opens on the directory named, whatever file system the data directory is on.

### The shutdown

One path for an orderly stop, for a fatal failure of the disk and for the unwinding
of a start that failed; a step with nothing to stop is logged and does nothing.

| Step | Name | What happens |
| ---- | ---- | ------------ |
| 1 | refuse | The engine refuses what comes from outside (`unavailable/shutting_down`). |
| 2 | interfaces | Both management and Agent HTTP servers stop taking connections and wait for active requests within the grace; remaining connections close on expiry. Their serving goroutines are joined, then hook `StopInterfaces` stops other interfaces. |
| 3 | adapters | The adapter host shuts the adapters down (`Config.AdapterHost`, when set), within the grace; then hook `StopAdapters`. Native sessions are separate processes and are not affected. |
| 4 | engine | The loop ends after its command, and every goroutine of the engine is waited for. |
| 5 | release | The last line is logged, the log is closed and the lock is let go: the lock goes last, because while it is held no other daemon starts over state this one is still writing. If the engine has not ended within the grace, it may still be writing: the lock and the log are kept until the process exits, the line says so, and `Wait` returns `ErrEngineNotStopped`. |

The grace is `Config.ShutdownGrace` (10 seconds when zero). After a fatal storage
error the engine has ended and written nothing more; the same steps run with 2
seconds instead, and the daemon ends with a `FatalError`: exit code 74, so the next
start scans the disk again. The same holds when the failure comes during the start,
in any step: the layout rule and the recovery of the transaction layer at step 2, the
saving of the pause at step 4, the scan at step 6, and every step from 8 on, in
which a hook or the recovery of deliveries sees the engine's refusal for the failure
or the engine already dead; `Start` then returns a `FatalError` that wraps what the
step said. `RequestStop` is the one entry
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
- Two results are not the same after a crash between the effect and its result,
  either: a close commits with its first write and stores its result last, so a
  crash after the effect is durable and before the result is written leaves the
  endpoint closed and no result under that identifier. The request sent again is
  carried out again and finds the endpoint closed already, so it answers
  `already_closed` where the first answer, which never arrived, would have said
  `closed`. The effect is written before the result because that is the only order
  that cannot leave a result for an effect that did not happen; an operation is
  written so that doing it again is harmless.

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
at step 6. The daemon has three of its own. The certificate check runs every hour
(`CertificateCheckEvery`) and does what step 5 does, with the clock of the command:
the certificate lasts 30 days and is renewed with 10 or fewer left, so an hour is a
small part of the margin, and a node that was down is brought up to date by the
start, not by waiting for the next hour. It runs in the loop, as everything that
signs does. A node that may not sign does not renew, and the check reads only the
judgement the engine hands the task (`TaskInput.SignRefusal`, the one rule of signing):
a paused node waits, and the owner's clear makes the check due at once, on the clock the owner accepted, so it renews in
the same pass (a certificate signed on a clock that was ahead and is not yet valid for the real one is due whatever
its validity, so a forward jump of the wall clock is mended by that clear and not days later); a node
whose identity was retired signs nothing in the time before the daemon is down,
because the loop goes on running its tasks until the engine stops. A failure that a retry may cure is tried again after the engine's backoff, a
fatal one ends the daemon as any other. The other is the clock state, every minute (`ClockStateEvery`):
the high water mark and the pause are made durable, and a crash loses at most a
minute of a mark, which only makes the next start trust the clock less (a renewal of
the certificate raises the mark with the certificate, so a certificate is never above
the mark whatever the periodic write did). The pause is persisted with the time the
engine says it began, which the engine keeps while the pause lasts and forgets when the
owner clears it, so a later pause begins at its own time and the daemon holds no copy. The engine
also runs it at once when it finds the clock set back. The third is the archive sweep,
every hour (`ArchiveSweepEvery`): the daemon names the period and the engine owns
the sweep (`engine.AddArchiveSweep`), because the engine is the single writer and holds the
view that follows every removal. What it sweeps has served a retention of a day or more
(a delivery about 27 hours after its deadline, a result or an acknowledged alert a week
after), so it is never urgent; an hour keeps a pass to what a node at its upper scale
finishes in that time. A pass stalls the engine's loop once for a full scan of the live
files (the storage reference measures about 3.2 s for 50,000 files and 11 to 13 s for
200,000, warm), then for a fraction of a second at a time as it removes in chunks;
callers that meet the stall are told `loop_busy`, which they retry. It runs on the
injected clock like the others and waits while the clock is in doubt
(`TestTheDaemonSweepsTheArchiveEveryPeriodOnTheInjectedClock` for the period;
engine's `TestNoSweepWhileTheClockIsInDoubt` for the wait: several periods pass with the
pause in force and what is due stays, then the owner's clear makes the sweep due at once and the next pass
removes it). Other
features add theirs in `Attachments.Periodic`.

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
   version, zero being none (`missing_expected_version`), and any other request
   carries no such member at all, whatever its value (`unexpected_version`).
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
The [shared local HTTP boundary](../local-http/README.md) owns exact method and
POST routing, the body limit, response encoding, diagnostic logging and shutdown.
The server has no write timeout, because a request is the client's connection and
ends with it; a request whose client goes away has its context cancelled.

The methods the daemon has:

| Method | Operation | Result |
| ------ | --------- | ------ |
| `endpoints.list` | `OpRead`; params `{"instance"?}` | `{"endpoints":[{address, kind, instance, state, queued, draining}]}`, never a token |
| `endpoints.close` | `OpManageEndpointSession`; params `{"endpoint": "<node>/<endpoint>"}` | `{"endpoint","outcome"}`, outcome `closed`, `closing` or `already_closed`; a runtime endpoint is refused `endpoint_kind`, an address of another node is `endpoint_unknown` |
| `daemon.stop` | `OpStopDaemon`; no params | `{"stopping":true}`; the daemon then shuts down; a resend, even to a daemon that was started again, asks for the stop again |
| `status` | `OpRead`; no params | `{"node":{id,version},"daemon":{state,started_at},"reminders":[{kind,message}],"alerts":{open},"clock":{rollback_paused,rollback_paused_since?,high_water?},"certificate":{not_after,remaining_s},"capacity":{open_deliveries,node_queue_limit,outbound_backlog,disk_free_bytes,disk_reserve_bytes},"deliveries":{<every state>:n},"endpoints":[{address,kind,instance,state}]}`. The reminders come first for a reader and stay until done: `revocation_backup_unconfirmed` is true while the confirmation on disk is not of the statement this node holds. `deliveries` counts the deliveries the node holds as a target, the control signals among them, and lists every state, with zero where there is none. The endpoints that are blocked and the turns started by prompts are the figures of the features that do not exist yet |
| `alerts.list` | `OpRead`; params `{"include_acknowledged"?}` | `{"alerts":[{id,kind,since,subject?,detail,acknowledged}]}`, oldest first, the acknowledged ones only on request. While the clock is paused the list ends with the alert of the pause, `id` `clock_rollback`, which is no record and is lifted by clearing the pause; `status` counts it in `alerts.open`, so the number agrees with the entries the list shows as not acknowledged, and `alerts.ack` of it answers `not_found/alert_unknown` with the words that it is cleared by `sidera clock clear-rollback` |
| `alerts.ack` | `OpAcknowledgeAlert`; params `{"alert": "<32 hex digits>"}` | `{"alert","acknowledged":true}`; `not_found/alert_unknown` for an alert the node does not hold; an identifier that is not 32 hex digits is `malformed_request` |
| `identity.export_revocation` | `OpExportRevocation`; no params, no operation identifier | `{"armored","digest"}`: the statement that retires this node, in the armor the keys format, and the digest the backup is confirmed with. Authorized as an operation of the owner's alone and stored nowhere: its result would be a copy of the statement |
| `identity.confirm_backup` | `OpConfirmRevocationBackup`; params `{"digest"}` (64 lower-case hex digits) | `{"confirmed_at"}`; a digest that is not the one of this node's statement is `invalid_argument/digest_mismatch` and changes nothing |
| `identity.retire` | `OpRetireIdentity`; params `{"node"}` | `{"node","retired":true,"stopping":true}`; the node must be this node (`invalid_argument/node_mismatch`); the daemon then stops, and every later start refuses to run. A resend asks for the stop again |
| `clock.clear_rollback` | `OpClearClockRollback`; no params | `{"cleared","high_water","now"}`; `cleared` is false when no pause was in force |
| `node.show` | `OpRead`; no params | `{"node","version","settings":{…}}`, the settings as `node.json` holds them |
| `node.set` | `OpChangeNodeSettings`; params `{"settings":{…patch}}`, carries the expected version | `{"settings":{…},"version"}`; a patch is judged whole by `config`: `invalid_argument/config_invalid` (with the path), `empty_patch`, and `conflict/version_conflict` with the current version |
| `endpoints.set` | `OpChangeEndpointSettings`; params `{"endpoint","settings":{…patch}}`, carries the expected version of the instances | `{"endpoint","settings":{…},"version"}`, the settings the owner has given the endpoint, those left to the node default not among them; an endpoint the node does not have is `endpoint_unknown` |
| `adapters.install` | `OpInstallAdapter`; params `{"source": "<absolute path of the package directory>"}` | `{"adapter":{"id","version"},"already_current"?}`. The daemon copies the package outside the loop and judges the copy; a directory it cannot read, or a directory or file inside the package, is `invalid_argument/source_unreadable` (with `details.path`, the place inside the package, when that is where the fault is), a manifest that does not parse or a package that lacks its program `invalid_argument/config_invalid`, a package for another platform `refused/platform_unsupported`, an upgrade of an adapter with an enabled instance, or a disabled one whose process is still ending, `conflict/adapter_in_use` (the message holds for both; `details.name` is `instances.<name>` and `details.reason` says whether that instance is enabled or its process is still ending), a second installation while one is copied `conflict/install_in_progress` (the message says that this installation is still in progress when the request is the one being copied, and that another is being installed otherwise). The same request again answers with its stored result and writes nothing. The version that is already installed, in another request, writes only its result, and the result says `"already_current":true` (the member is left out when the installation replaced something) |
| `adapters.list` | `OpRead`; no params | `{"version","adapters":[{id,version}],"instances":[{instance,adapter,enabled,probe?:{state,at?,frontends:[{preset,frontend}]}}]}`. `version` is that of the instances. `probe` is there for an enabled instance only: `pending` until the adapter has reported and each token issued for the probe is presented or expired, `passed`, or `failed`; `frontends` are those whose token the node saw presented |
| `adapters.enable` | `OpEnableAdapter`; params `{"adapter","instance"?,"config"?}`, carries the expected version of the instances | `{"instance","enabled":true,"version"}`. The instance is named after the adapter when the request leaves it out; `config` is for a new instance and an instance that exists takes none (`conflict/instance_exists`); an adapter that is not installed is `not_found/adapter_unknown`; an instance of another adapter `conflict/instance_adapter_mismatch`. The host is asked to run the instance after the commit; whether it came up is read from `adapters.list` |
| `adapters.disable` | `OpDisableAdapter`; params `{"instance"}`, carries the expected version of the instances | `{"instance","enabled":false,"version"}`; the instance stays with its settings and endpoint ids; the host is asked to stop it and its endpoints are closed |
| `presets.list` | `OpRead`; no params | `{"version","presets":[{name,local_sources,description?,config?}]}` by name |
| `presets.add` | `OpChangePreset` as loosening; params `{"name","preset":{…}}`, carries the expected version of the presets | `{"name","version"}`; the document must say `local_sources` (`allow` or `deny`: `invalid_argument/config_invalid` with the path); a name that is taken is `conflict/preset_exists` |
| `presets.remove` | `OpChangePreset` as narrowing; params `{"name"}`, carries the expected version of the presets | `{"name","version"}`; `not_found/preset_unknown` |
| `operation.result` | `OpQueryOperationResult`; params `{"operation_id"}` | the result stored for that identifier of the owner, as the request that ran it was answered; `not_found/operation_unknown` when there is none (never used, or refused, which stores nothing). A read of the table the replay check uses, so it finds results kept under `state/admin/` and in the versioned files of `config/` |

A binary built with a test tag has **test rows** besides these: methods named
`test.<something>` that are the test harness's door to the node (the test clock,
quiescence, the crash points). They are outside the catalogue of administrative
operations and not judged by `admin.Authorize`: they have no catalogue entry, carry
no operation identifier and store no result, and they exist only in a binary built
with the `testclock` or `crashtest` tag, which is never shipped; without the tag the
request is `method_unknown`, which is how a test asserts that the way in is missing.
The rows themselves belong to the streams that own the harness; the table has the
shape and one row that proves it.

Errors are `{"error":{"category","code","message","details"?}}` in the words of
`apiwire`, which is the one mapping from what the engine, `config`, `ipc` and the
decoder say. Ended request contexts are `unavailable/request_canceled` and
`unavailable/request_timeout`, with fixed messages describing only the context;
typed errors take precedence. A written request with an incomplete answer remains
`outcome_unknown`. Categories are `invalid_argument` 400, `unauthenticated` 401,
`not_found` 404,
`conflict` 409, `refused` 422, `backpressure` 429, `unavailable` 503, `internal`
500, and the exit codes of the command line 3 to 9 in that order, 1 for internal, 2
for a command used wrongly. `outcome_unknown` (500, exit code 10) is the outcome of a
request that may or may not have taken effect: a client judges it itself when it
wrote a request and did not read the answer whole, and the daemon says it
(`storage_fatal`) when a failure of the disk stopped the node while the request was
in flight; either way the request is sent again with the same operation identifier,
which returns the stored result if it ran and runs it if it did not. The client's
`OutcomeUnknown` suggests that resend only when given an operation identifier;
without one it reports the uncertainty alone, because an Agent send without an
explicit idempotency key may create another message. A refusal for an
invalid argument with no detail is `malformed_request`. The text of an error nobody
expected is logged and never sent. Some words belong to the owner's operations alone:
`not_found/alert_unknown`; `invalid_argument/empty_patch`, `node_mismatch`,
`digest_mismatch` and `source_unreadable`; `conflict/install_in_progress`;
`refused/platform_unsupported`; and `unavailable/identity_retired`, which is what
anything that would sign is told between the retirement and the end of the daemon.

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
| `AgentProbeOnly`, `AgentServe`, `StopInterfaces` | 9, 15, shutdown 2 | extensions after the built-in Agent server opens, enables endpoint methods and stops |
| `Adapters`, `StopAdapters` | 10, shutdown 3 | what else starts and stops with the adapters; the adapter host itself is `Config.AdapterHost`, wired by this package, and is not a hook |
| `Periodic` | 6 | any feature with a task on a period (disk polling); the daemon's own tasks, the clock state, the hourly certificate check and the archive sweep, are not among them |
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
- **The composition root owns the built-in interfaces.** It opens the Agent
  listener before adapters can probe tools, enables endpoint methods after recovery,
  and shuts both HTTP servers down concurrently under one grace, joining their
  serving goroutines during shutdown. It uses the standard library's
  [HTTP shutdown](https://pkg.go.dev/net/http#Server.Shutdown): stop accepting, wait
  within the grace, then close remaining connections. The engine ends receive
  waiters first. No extra server or lifecycle dependency is needed for these steps.
- **Extension hooks are functions with an `Env`,** because an extension needs only
  the part of the node already up at its step. Built-in interface and adapter
  lifecycles remain explicit fields owned by this package.
- **A hook gets views, not the writer or the stores.** The engine is the single
  writer of the data directory and one declaration has one open transaction at a
  time; a hook that held `*txn.Writer`, or `*keys.Keys` or `*config.Config` whole
  (both write through it), could open a transaction beside the engine's. The views
  list the reads, and a test fixes the list. The instance lock is given because
  whoever listens on an address of this home has to state that it is held.
  Rejected: a helper that listens for the hook, which would make this package know
  the Agent interface's options.
- **The lock is not let go under an engine that has not ended.** The lock is what
  keeps a second daemon from writing beside this one; after the grace the process
  is the only thing that can end the engine for certain, so the lock is left to the
  process exit and `Wait` says why. Rejected: releasing it anyway, which turns a
  stuck task into two writers.
- **The signal is taken before the start.** A signal during the start would
  otherwise end the process with the lock held and a socket left behind, and between
  two steps is the only place the start can stop without leaving a step half done.
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
- Tests of the adapter host's wiring (`adapters_test.go`):
  `TestEnabledAdapterInstancesAreStartedAtStepTenAndTheStepWaitsForThem`,
  `TestAnInstanceThatCannotBeStartedIsJudgedOfflineAndTheStartGoesOn`,
  `TestShutdownStopsTheAdaptersAtStepThree`, `TestAFatalFailureStillStopsTheAdapters`,
  `TestANodeWithoutAnAdapterHostStartsNoAdapter`: fake adapters in the test process,
  started by the one launcher the engine-layer fixture also uses (`harness`).
- Tests of the daemon: `TestSecondDaemonOnSameHomeRefused`,
  `TestInitOnTheHomeOfARunningDaemonIsRefused`,
  `TestStartupStepsRunInTheirFixedOrder`,
  `TestEachHookRunsInsideItsStepWithTheEngineInTheStateTheStepNeeds`,
  `TestTheManagementInterfaceReadsAndRefusesChangesWhileTheNodeStarts`,
  `TestAgentListenerStartsWithProbesAndEnablesMethodsOnlyWhenServing`,
  `TestAHookThatFailsStopsTheStartAndLeavesNothingRunning`,
  `TestTheStartMakesTheLayoutTheOwnerRemovedAgain`,
  `TestTheDaemonLogsToTheFileUnderLogsWithTheTimeOfItsClock`,
  `TestThePeriodicTasksRunEveryPeriodOnTheInjectedClock`,
  `TestTheDaemonSweepsTheArchiveEveryPeriodOnTheInjectedClock`,
  `TestAClockSetBackIsPausedAtStartAndThePauseAndTheMarkAreKept`,
  `TestShutdownRunsInItsFixedOrder`, `TestRequestStopRunsTheShutdownOnce`,
  `TestTheOwnerStopsTheDaemonThroughTheManagementInterface`,
  `TestRunReturnsAfterANormalShutdownWhenItsContextEnds`,
  `TestAFatalStorageErrorEndsTheDaemonWithTheExitCodeOfAnIOError`,
  `TestAFatalStorageErrorInTheLayoutRuleEndsTheStartWithTheExitCodeOfAnIOError`,
  `TestAFatalStorageErrorInTheRecoveryOfTheTransactionLayerEndsTheStartWithTheExitCodeOfAnIOError`,
  `TestAFatalStorageErrorSavingThePauseOfAClockSetBackEndsTheStartWithTheExitCodeOfAnIOError`,
  `TestAFatalStorageErrorInTheScanOfStateEndsTheStartWithTheExitCodeOfAnIOError`,
  `TestAHookThatMeetsTheDeathOfTheEngineEndsTheStartWithTheExitCodeOfAnIOError`,
  `TestTheLockIsNotLetGoWhileTheEngineHasNotStopped`,
  `TestARequestToStopThatRacesWithAFailedStartIsReadInOrder`,
  `TestACancelledContextStopsTheStartBeforeTheNextStep`,
  `TestASignalDuringTheStartStopsItBetweenTwoSteps`,
  `TestAHookIsGivenNoWriterAndOnlyReadsOfTheKeysAndTheConfiguration`,
  `TestAHookCanListenOnAnAddressOfThisHomeWithTheLockItIsGiven`,
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
  `TestAMalformedRequestIsRefusedNamingThePositionOfTheFault`,
  `TestTheOperationIdentifierOfARequestThatChangesSomething`,
  `TestAnExpectedVersionIsRefusedWhereNoneIsCarried`,
  `TestAChangeOfASettingNeedsItsExpectedVersion`,
  `TestAnOperationOutsideTheCatalogueIsNeverRun`, `TestTheCommandTheEngineIsAsked`,
  `TestEqualRequestsHaveOneFingerprintAndDifferentOnesDoNot`,
  `TestTheAnswerOfARequestThatChangesSomething`, `TestAReadAnswersItsResult`,
  `TestStopIsRequestedAfterTheAnswerIsWrittenAndOnlyForAnOperationThatStops`,
  `TestAClientThatGoesAwayCancelsTheRequest`,
  `TestListenRefusesTheAddressOfTheAgentInterface`,
  `TestConcurrentRequestsAreAnsweredEachItsOwn`, `FuzzDecodeRequest`,
  `TestAnExplicitZeroVersionOfAVersionedChangeIsMissing`,
  `TestOperationResultAnswersTheResultStoredForTheOwnersIdentifier`,
  `TestOperationResultNeedsAnIdentifierThatIsWellFormed`,
  `TestOperationResultAnswersAnEngineThatCannotAnswer`,
  `TestATestRowIsOutsideTheCatalogueAndIsNotAuthorized`,
  `TestARowIsAnOperationOrATestRowAndItsNameSaysWhich`,
  `TestATaggedBuildHasItsTestRows`, `TestAnUntaggedBuildHasNoTestRow`.
- Tests of shared HTTP routing, size limits, encoding, raw bytes, diagnostics,
  listener kinds and shutdown live in `internal/node/localhttp`; see its README.
- Tests of the words of the wire (`internal/node/apiwire`) and of the log
  (`internal/node/logfile`): see their READMEs. The scans that keep the local owner
  and the local source where they belong: `internal/node/layout_test.go`.
- Pending verification by CI on main: the two Windows tests above (the first widens
  the access list of the data directory and needs the CI's account; the second needs
  the privilege to make a symbolic link), and everything of the management interface
  on a Windows named pipe; this machine runs the Unix paths only.
