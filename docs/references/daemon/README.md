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

The management server and offline CLI share `manageapi.RevocationExportResult`
for the armored self-revocation and its backup digest, keeping the export result
shape in the interface that owns it.

## What

### Randomness and filesystem ownership

`Config.Rand` supplies keys, engine randomness and `Env.Rand`; nil resolves to
`crypto/rand.Reader`. `Config.TempRand` optionally supplies temporary-file names
for every OS filesystem the daemon opens, including Init, Start and adapter
package readers. Its nil default is the resolved `Rand`, preserving the existing
shared stream and its consumption behavior. Both supplied readers must be safe
for concurrent use. Defaults are resolved only by `Config.withDefaults`.

An explicit `Config.FS` retains its own reader unchanged. The daemon still owns
opening its default filesystem, creating a missing Init directory, refusing a
missing Start directory, permissions, locking and recovery. `fsx.OpenOS` keeps its
existing explicit-reader contract. Independent readers let a caller isolate
identifier allocations from asynchronous temporary-file consumption; a lock alone
serializes a shared stream but cannot fix which consumer obtains its next bytes.

`random_test.go` verifies default sharing, independent temporary draws and an
explicit filesystem's own reader through real Init and Start recovery writes.

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
| 1 | lock | The data directory is resolved; the root, `keys/` and the rest of the tree are checked to be private to the owner (a directory that is not one, a symbolic link among them other than the Codex socket described there, is refused and nothing is narrowed; see "The instance lock and the permissions"); `keys/init.rec` must exist; then the instance lock `daemon.lock` is taken, and a holder is reported as "a daemon is already running"; last, the data directory becomes the daemon's working directory, and failing to change to it fails the start. A daemon over an explicit `Config.FS` does not change it: that home is not necessarily a directory of this machine. | this package |
| 2 | config | The transaction layer recovers (settle, then removal of every `.tmp-*`); the layout of `state/`, `run/`, `manage/` and `logs/` is made again where the owner removed it; the log opens; `config/` is loaded strictly and a file that is not valid stops the start and is named. | this package, `txn`, `config` |
| 3 | keys | Keys, the revocations the owner kept and the serial seen are loaded; a node whose own identity was retired refuses to run. Nothing is signed. | this package, `keys` |
| 4 | clock | The wall clock is judged against the saved high water mark, before anything is signed. A clock found behind it by more than `timing.ClockTolerance` pauses the node: the pause is made durable at once with the mark that was, which is never lowered. | this package, `clock` |
| 5 | certificate | The certificate is renewed when it has ten days or less left, when it has expired, or when an earlier renewal was interrupted; never while the clock is in doubt (the pause step 4 found is respected: a certificate that is due waits, and the log says so). The judgement is the engine's (`engine.SignRefusalOf`), the same one the hourly check is handed; at this step no identity is retired, because step 3 refused it. | this package, `keys`, `engine` |
| 6 | scan | The engine's first recovery entry: `state/` is scanned, damaged files are quarantined, the recovery rules run until none has work, the view is built. The engine is given the release version of the program (`Config.Version`, which the command line takes from `internal/release`) and `hostfacts.Read`, from which it makes the node's profile. The network is built, with the network key taken from the keys once, before the loop exists, and the address book of `network.json`: its host does not listen yet. The router is set as the engine's transport (a call to this node goes to the loopback, made with no knobs; a call to another node goes to the network) and the network is the layer the engine pushes its membership to. The periodic tasks are registered. | `engine`, `transport`, `network` |
| 7 | trust | Hook `TrustSync`: synchronisation of revocations and the trust graph, the gate of vouchers. Empty for a node of one machine. | later stage |
| 8 | manage | The loop starts in the phase of recovery and the management interface opens: reads answer, anything that changes state is refused as `unavailable/recovering`. | this package |
| 9 | agent-probe | Open the Agent listener in probe-only mode, accepting only the one-use probe credential; then run hook `AgentProbeOnly`. | this package, `agentapi` |
| 10 | adapters | The adapter host (`Config.AdapterHost`, when set) starts the enabled adapter instances and the step waits until each has answered `adapter.start` or been judged offline; then hook `Adapters` runs, for what else starts here. | `adapterhost`, `apiwire` |
| 11 to 14 | | The engine's second recovery entry: creation intents and prompt attempts are checked, every record is ticked, feedback that fell due goes out, the outbound worker goes on over the loopback. After step 10 on purpose: step 11 asks the connected adapters about creations, closes and interrupts, releases and prompts, and the busy state they reported is used to renew leases after serving. Until step 12 the engine's schedule holds back the ticks, the feedback and the looks at source records that fell due, so none of them runs before the adapters have reported. Step 14 also lets the engine ask the network for the result fetches of the pairing that it held back until then. | `engine` |
| 15 | serve | The engine serves, then the Agent server enables endpoint methods; hook `AgentServe` runs afterward; last, the network opens its listeners on the addresses `network.json` lists. An address that cannot be opened (a port in use) fails the start, and the error names the address. | this package, `agentapi`, `network` |

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

`RestoreClock(reader, keys, now, log)` owns step 4's clock lifecycle and returns
`ClockState`, the startup snapshot passed to the engine. Its reader and loaded keys
belong to the same recovered node, the logger is non-nil, and `now` is one injected
clock reading. It can write: a new rollback is made durable before it returns;
read errors propagate and save errors retain the `making the pause durable`
context. A persisted pause is never cleared or moved when the clock becomes normal.
Normal forward movement raises only the in-memory mark at startup.

`AddClockState(engine, keys)` registers `clock-high-water` at `ClockStateEvery`
before the loop starts. Its task reads each `TaskInput`, so an owner clear or a new
pause cannot be overwritten by a captured startup snapshot. The daemon uses these
two entries in its existing startup steps; the engine-layer fixture shares the
same lifecycle without taking on the daemon's other periodic tasks.

### The adapter host

`Config.AdapterHost` is the host that runs the adapter instances the owner enabled
(`adapterhost.Host`); nil is a node that starts none. The daemon wires it in three
places and nowhere else:

- **Step 6** gives it to the engine as `Deps.Adapters`, only when it is not nil (a nil
  host inside the interface would not be seen as absent). `daemon` is where the two
  layers meet: `var _ engine.Adapters = (*adapterhost.Host)(nil)` is here.
- **Step 10** starts it with the data directory (`Env.Home`, the absolute path of the
  real directory, which the instance lock files `adapter-<instance>.lock` are in, at
  its root, and `adapter-state/<instance>/`, which the host makes for each adapter), the read-only view of the configuration the hooks get (`Env.Source`),
  the handle of the logs (`Env.Logs`: each instance's `adapter-<instance>.log`) and
  the engine behind `apiwire.AdapterEngine`, which words the engine's refusals as the
  adapter protocol does. The step waits for `Start`: it returns when each enabled
  instance has answered `adapter.start`, has failed its first attempt (judged
  offline, reported, and tried again after a backoff) or the host's start wait has
  passed. An instance that cannot be taken on does not stop the start. Only a host
  that cannot be started at all does (a bad `Env`, a host that was started or ended
  before). The `Adapters` hook runs after it, in the same step.
- **Shutdown step 3** shuts the host down with the step's context, which carries the
  grace: each adapter in service is sent `shutdown` with the intent `restart` when the
  daemon ends for a restart (`RequestRestart`) and `stop` for any other end (a stop, a
  signal, a fatal failure), since the daemon does not say when it comes back, its stream
  is closed, and a
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
| 2 | interfaces | Both management and Agent HTTP servers stop taking connections and wait for active requests within the grace; remaining connections close on expiry. In parallel with them the network stops listening, closes its connections and waits for its goroutines. Their serving goroutines are joined, then hook `StopInterfaces` stops other interfaces. |
| 3 | adapters | The adapter host shuts the adapters down (`Config.AdapterHost`, when set) with the intent `restart` or `stop`, within the grace; then hook `StopAdapters`. Native sessions are separate processes and are not affected. With the intent `stop` the Codex adapter may end its App Server when no session is known to be running a turn; running turns are never affected. |
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

**How the daemon ends.** `RequestRestart` is the same entry with another meaning:
the first request of either kind decides, a later one of the other kind changes
nothing. A signal (or the end of the context given to `Run`) is not such a request:
it is an order for the process to end, so `Run` reports `EndStopped` when one came
before the shutdown was over, whatever the first request was, and a service manager's
stop or Ctrl-C during the shutdown of a restart is not ignored. `Run` returns an
`Ending` with the error: `EndRestart` only when a restart was the first request, no
signal came and the shutdown ended with no error, otherwise `EndStopped`.
So a daemon whose engine has not stopped (`ErrEngineNotStopped`, the lock still held)
or that met a fatal failure of the disk ends the process whatever was asked for. The
daemon does not start anything itself: after step 5 it holds no lock, socket, file
or child process, and the caller of `Run` starts the program again in its place.
`Config.Relauncher` is what the daemon asks before it accepts a restart (see
`daemon.restart` below): `CheckRestart(ctx)` returns the path of the program a restart
would start and the version it reports, an error that wraps `ErrRestartUnavailable`
when the program cannot be started again from here (the daemon logs the reason, and the
wire says only that it cannot), or any other error when the program was run and did not
answer as expected. It may wait and bounds its own wait; its context ends when the
daemon shuts down. Nil makes the daemon one that cannot be restarted: the refusal is
the same and is logged.

### Restart

The foreground command (`sidera daemon`) is the daemon's `Relauncher` and starts the
program again when `Run` returns `EndRestart`. `main` records, before anything else
(the daemon changes the working directory in its first step), the program path, the
arguments and the working directory it was started with. The path is `argv[0]` made
absolute the way the system resolved it, with symbolic links kept, so that a link a
package manager keeps pointing at the current version starts the new version; the path
`os.Executable` reports replaces it when `argv[0]` cannot be resolved or names another
file than the one running, though on Linux that has its links resolved.

`CheckRestart` runs that path as `sidera version --json` within 20 seconds and returns
the path and the version it reports. The program is run under the name `sidera`, never
under the name it was started with: the name `siderad` selects `daemon`, and
`siderad version --json` would run `daemon version --json`. Output is capped, and a
program that fails, prints something other than the version object, reports no version
or does not answer in time is a failed check, refused as `restart_check_failed`. It is
refused as `restart_unavailable` when how the program was started was not recorded and,
on Windows, when a process started now would not outlive the daemon (below).

After `Run` returns `EndRestart`, `cli.Run` has released everything the command held (the
instance lock, the logs, the sockets and the records of a test process) and only then
starts the program, so that the new daemon takes the lock and the sockets over:

- **Unix**: `chdir` to the recorded start directory, then `execve` of the recorded path
  with the recorded arguments and the environment of the process. The process keeps
  its number, its terminal, its standard streams and the signals it ignores (a
  `nohup`), so a daemon in a tmux session stays in it and a foreground one stays in the
  foreground. The Go runtime opens its files close-on-exec, so no descriptor of the old
  daemon reaches the new one. `execve` returns only on failure, which ends the command
  with `error: the daemon stopped but could not start <program> again: <why>` and exit
  code 1: the daemon has gone and nobody is left to start it.
- **Windows**: a process cannot replace itself, so `adapterkit/process.Start` starts a
  detached process with the recorded path, arguments and directory and the environment
  of the process; its standard output and error go on when they are files (the `>>`
  redirection of a start script) and to the null device otherwise, because a pipe would
  tie it to the old process. The old process then ends with exit code 0. The new process
  stays in the job the old one ran in when that job forbids breakaway; that is safe only
  when the job does not end its processes when it closes (as the job of a scheduled
  task does not), which `process.HostDetachment().OutlivesDaemon` says before anything is
  shut down. A job that ends its processes when it closes, or that cannot be read, makes
  the restart `restart_unavailable`: a service wrapper restarts the daemon it started.
  The new process has no console. What it starts, the restart check and the adapters, is
  started without a window (see "Programs the daemon starts"), so a restart opens none.

If `ErrEngineNotStopped` or a fatal failure of the disk ended the daemon, the process ends
as for a stop and starts nothing.

### Programs the daemon starts

`childproc.Windowless(cmd)` is for every console program the daemon starts as its
child (the adapters, the restart check). On Windows it gives the program a console of
its own that has no window (`CREATE_NO_WINDOW`: the process has a console,
`GetConsoleWindow` is 0, and Windows Terminal is not handed anything to show), and a
program that program starts with a plain `exec.Command` shares that console, so it has
no window either. Elsewhere the call does nothing. The flag is added to the creation
flags of the command and the rest of its `SysProcAttr` is kept, so the call comes
after anything that assigns `SysProcAttr`. The daemon that a restart starts in its
place is the one exception: `adapterkit/process` starts it detached, on purpose (see
"Restart").

The rule is needed because a daemon may have no console. A process without a console
that starts a console program gives it a new visible console, and with Windows
Terminal as the default terminal that console becomes a window; closing it ends the
program, and the supervisor starts it again in a new one. A daemon has no console when
it is restarted by `sidera update` or `sidera daemon restart` (the new process is
started detached), when it runs as a service and when it is started through WMI.

Two places start such programs, and both call it: the method `ExecLauncher.Launch` of
package `adapterhost` (the adapters, which are long-lived, see
[adapter-host](../adapter-host/README.md)) and the method `Launch.Probe` of package
`launch` (the `sidera version --json` of the restart check and of `sidera update`).
A program those start with a plain `exec.Command` (an adapter's `claude --version`, its
probes) shares their console and gets no window either. The session processes an
adapter starts through `adapterkit/process` are not among them: that package chooses
their console flags.

The Windows test of the package (`internal/node/childproc`) is the only place that
shows this. It starts the test binary with no console, as the daemon a restart starts
has none, and has that process start a child with `Windowless` and the child start a
grandchild with a plain `exec.Command`; both must have a console and no window for it.
That holds whatever console the test process itself has.

### init

`daemon.Init` makes the data directory in one transaction (`init`) under the
instance lock, which it takes first, so it cannot run beside a daemon or another
init: the root only the owner can reach, the layout of every owner, the keys, the
certificate, the self-revocation, the clock record, the default configuration (one
preset, `default`, open to local sources, and `network.json` listening on
`Config.InitPort`, default 7744) and, last, `keys/init.rec`. It never
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

### Errors and command output

The daemon retains its five exported sentinel values and `FatalError` type.
Their declarations live in the standard-library-only `daemonerr` leaf; `daemon`
re-exports the same sentinel values and aliases the type, preserving `errors.Is`,
`errors.As`, original error text and `Unwrap`. The lower-layer `apiwire` mapper
can therefore recognize them without importing the composition root.

`apiwire.FromError` owns these shared mappings, including wrapped errors:

| Error | Category | Stable code | Action described by the message |
| --- | --- | --- | --- |
| `*ipc.PathTooLongError` | `invalid_argument` | `socket_path_too_long` | Use a shorter `--home` or `SIDERA_HOME`; the extracted error names the path, byte length and platform limit. |
| `ErrAlreadyRunning` | `conflict` | `daemon_already_running` | Wait for the daemon or initialization holding the instance lock, or use another data directory. |
| `ErrAlreadyInitialized` | `conflict` | `data_directory_already_initialized` | Start with `sidera daemon`, or use another data directory. |
| `ErrNotInitialized` | `unavailable` | `data_directory_not_initialized` | Run `sidera init` for the selected directory. |
| `ErrPermissions` | `refused` | `data_directory_unusable` | Check ownership, permissions and path; the root must be a real directory private to its owner, and symbolic links are refused. |
| `ErrEngineNotStopped` | `internal` | `engine_not_stopped` | The instance lock stays held until this process exits. |
| `ErrRestartUnavailable` | `refused` | `restart_unavailable` | Stop the daemon and start it the way it was started, or let whatever supervises it restart it; the reason is in the daemon log, not on the wire. |
| `*FatalError` | `unavailable` | `daemon_storage_fatal` | Check storage and restart the daemon to recover the data directory. |
| `ipc.ErrLockNotHeld` | `internal` | `instance_lock_not_held` | The listener was opened without the instance lock: an internal startup error. |

An existing wire error takes precedence. A `FatalError` takes precedence over
an engine refusal in its cause: a daemon that stopped requires recovery, while
a standalone engine storage-fatal refusal still means
`outcome_unknown/storage_fatal` for a request. All new daemon and missing-lock
messages are fixed, without outer wrapper text or underlying causes. IPC
path-length errors use only the extracted typed error's explanation. A failure to
connect to a local endpoint is the exception to fixed messages: it names the socket
and the reason (`daemon_not_running` for a missing socket or a refused connection,
`peer_check_failed` for a refusal of access), as the
[command-line reference](../command-line/README.md) lists; the other peer checks keep
their mapping. Untyped filesystem and hook errors
retain their existing mapping or the fixed `internal/internal` fallback.

The local `init` and foreground `daemon` commands use these mappings for JSON
failures; human failures retain the original daemon explanation on stderr.
Their exit policy remains `daemon.ExitCode`: normal completion 0, ordinary
failure 1, fatal storage failure 74, independent of the JSON category's generic
command exit code. The [command-line reference](../command-line/README.md)
describes the output streams.

### The instance lock and the permissions

The root, `keys/` and the rest of the tree are checked before anything is written,
before the lock is taken and before a socket is made, so a refused start leaves no
lock file. The check is the file layer's `OwnerOnly`, which fails closed: on Unix any
group or other bit, on Windows a DACL that grants anyone but the owner, SYSTEM and
Administrators (deny entries are skipped, an entry of a type the check cannot judge is
refused). The root is read without following a symbolic link, so a link is not a
directory and is refused, whatever it points to (a link above the data directory is
followed: macOS's temporary directory is one). `keys/` and its files are checked by
`keys.CheckPermissions`. The lock is `daemon.lock` at the root, not in `state/`, which
can be deleted as a whole.

The root alone is not enough. A directory made with its own protected list, as
`adapter-state/<instance>` is, does not inherit from the root, so an entry another
program adds to the root says nothing of it, and one added to it says nothing at the
root: on Windows a sandbox's group was granted Modify on `adapter-state\codex`, and
inherited read on the root, by two different grants. The start therefore walks the whole
tree (`fsx.CheckPrivateTree`) and judges:

- every directory, whatever it holds, at any depth;
- every regular file of `state/endpoints/`, which keeps the token of each session
  endpoint in clear, and of `adapter-state/`, where each adapter keeps what it needs to
  reach its runtime: the Codex adapter's `ws-token`, the console's `console.addr` with
  its token, the Claude adapter's files; with the files of `keys/`, which
  `keys.CheckPermissions` judges, these are the files that hold a token or a key;
- a symbolic link among those files, which is refused: nothing the node writes there is
  one, and a read follows it to a file nobody judged. The one link accepted is the
  Codex App Server's socket. Codex binds a socket path under a short limit by making
  the socket in a private directory under `/tmp`, `codex-daemon-<uid>/`, and
  leaving a symbolic link to it at the path it was given (`adapter-state/codex/app-server.sock`),
  and the server outlives the daemon, so the link is there at every restart. A link is
  accepted when it resolves to a socket that is owned by the user running the process
  and that no group or other can reach, in a directory with the same two properties
  (`fsx.Reader.ResolveLink`). A link to anything else, or to a socket or a directory
  that others can reach, is refused, and a refusal of a socket names the link and the
  socket. On Windows no link is accepted: the Codex App Server listens on TCP there.

A record file of `state/` and every other file holds no secret and relies on its
directory, which is judged. That is a decision about cost and about what can happen: on
Windows a file made in a private directory inherits the directory's list, so a foreign
entry reaches it only when something adds it to the file itself (or moves the file in),
and a program that grants access to a directory does so on the directory. A file can
carry an entry its directory does not show; judging every file would add one read of an
access list per file to every start, measured at 4.3 s for 50,000 record files on
Windows against 0.13 s for the walk that judges the directories and the secret files
(the same walk takes 0.03 s on macOS), and the cost grows with the records.
A socket, a device or a junction among the entries is left to the directory that holds
it. An entry that vanishes between the listing and its look (the temporary file of a
daemon that runs) is skipped.

The refusal is `ErrPermissions` and names the path as the operating system does, who can
reach it and how, so that the owner can find the entry: `the data directory is not private
to its owner: C:\...\adapter-state\codex grants Modify (0x1301bf) to S-1-5-21-...
(unresolved)` (the account name stands where the system resolves one, `DOMAIN\name`; on
Unix, `has mode 0755, which lets the group or others reach it`). Nothing is repaired: only
the owner knows who has already read what the files hold, and rewriting a list would hide
it. The same walk judges the state directory of an instance before the host launches its
adapter (see [adapter-host](../adapter-host/README.md)), because an entry can be added
while the daemon runs.

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
   decoder can place it. A replay-protected request (`Operation.ReplayProtected`: every
   operation that changes state but the attachment of a session) is
   `{"operation_id": <32 lower-case hex digits, not all zero>, "expected_version"?: n,
   "params"?: {...}}`; any other request, a read or an operation that keeps its own
   idempotency, is `{"params"?: {...}}`.
4. `admin.ValidateRequest`: a replay-protected request carries an identifier
   (`missing_operation_id`), a change of a versioned setting carries its expected
   version, zero being none (`missing_expected_version`), and any other request
   carries no such member at all, whatever its value (`unexpected_version`).
5. A request that is not replay protected is a direct call of the engine and is answered
   from it, nothing stored. A replay-protected request is one
   `engine.Execute` command: the actor that was authorized (the engine makes no actor of
   its own and takes this one wherever it judges who asks), the key `admin.KeyFor(owner, id)`,
   the fingerprint, the expected version, the name of the method and the operation. The replay check, the
   version check and the storing of the result are the engine's and `config`'s; the
   interface does none of them.

The owner's in-process client is `manageapi.NewClient(ClientConfig{Home})`.
`Call(ctx, method, Request{OperationID, ExpectedVersion, Params})` uses the shared
[local HTTP client](../local-http/README.md#client-requests-and-answers) for checked
IPC, JSON encoding and transport errors; it supplies no Bearer credential. A zero
operation identifier is omitted, a nil version is omitted, and a non-nil version
retains its presence even when zero. Nil params are omitted. Method and params
validation remain with the server's table. Ordinary calls have a 60-second timeout;
`adapters.install` has five minutes, also bounded by the caller's context.

The client checks the successful response envelope and, for a change,
requires the same operation identifier and the presence of `replayed`; a member of
the envelope it does not know is ignored, as in every successful answer of the
node (a client may be older than the daemon it talks to, so a later release can add
a member to an answer, but never one that a caller must understand to act correctly:
that becomes a new method). It returns
the complete unchanged `json.RawMessage`, including the metadata, for the caller
to print or interpret. A success whose envelope cannot be read is
`internal/answer_unreadable`, with the operation identifier of a change in
`details.operation_id`; an incomplete answer or an error answer the client cannot
read has an unknown outcome. Every unknown outcome for a request with an
identifier retains it in `details.operation_id`; server codes and details such as
`storage_fatal` are preserved. The client performs no resend. The caller owns the
one operation identifier, version and exact request to retain for a resend, since
changing them can execute another operation or ask for a different change.
`Close` releases idle connections; the caller's context owns an in-flight call.

The fingerprint is `SHA-256("sidera-manage-v1" NUL method NUL canonical params NUL
decimal expected version)`; the canonical params are the decoded params encoded again
with the fields of their type in declared order, so a resend written with another
order or spacing is still a resend. The answer to a replay-protected request is
`{"operation_id","replayed","result"}`, the result being the one stored; any other
answers `{"result"}`. When an operation stops or restarts the daemon the answer is
flushed and then the stop or the restart is requested, once: the client has its
answer before the daemon goes.
The [shared local HTTP boundary](../local-http/README.md) owns exact method and
POST routing, the body limit, response encoding, diagnostic logging and shutdown.
The server has no write timeout, because a request is the client's connection and
ends with it; a request whose client goes away has its context cancelled.

The methods the daemon has:

| Method | Operation | Result |
| ------ | --------- | ------ |
| `endpoints.list` | `OpRead`; params `{"instance"?}` | `{"endpoints":[{address, kind, instance, state, queued, draining, origin?}]}`, never a token. `origin` is `created` or `attached` and is present for a session endpoint only |
| `sessions.attach` | `OpAttachSession`; a request with the envelope of a read (no operation identifier, no expected version: they are `malformed_request`); params `{"adapter","instance"?,"native_ref","display_name"?,"wake"?,"create"?}`, `adapter` an adapter identifier, `native_ref` 1 to 256 characters, `display_name` 1 to 128 characters and under the display-name rule of the adapter protocol, `wake` at most 16 entries of an environment-variable name and a value of at most 4096 characters, opaque to the node, `create` a boolean that is true when absent | `{"endpoint","instance","token","wake":{"state","reason"?,"detail"?}}`. Attaches a session a person opened, or finds the endpoint it has: the same instance and native session, while the endpoint is open, answer with the same endpoint and token, and a repeat replaces the wake information (an absent one clears it) and the display name when one is given. `token` is the token of the endpoint as `SIDERA_TOKEN` carries it: **this is the one management method whose answer carries a token**, which is why the owner alone may ask and why a sandbox must deny the management socket (see the [local IPC](../local-ipc/README.md) reference). `wake.state` is what the adapter answered to `session.attach` within ten seconds: `ready`; `unavailable` with `reason` `native_not_found`, or the adapter's `wake_unavailable`, `native_unreachable` or `native_error` and its `detail`; `unknown` with `reason` `adapter_offline` or `no_answer`. The endpoint and the token are returned whatever the state. With `create` false the request only finds: when the session has an open attached endpoint it is answered as above (the wake information is replaced and the adapter asked as for any repeat), and when it has none nothing is made and the request is `not_found/session_not_attached`; a caller that wants to know whether its session is attached already, without attaching it, asks so. Refused as `refused/attach_unsupported` (the adapter of the instance did not declare `attach`), `refused/native_session_created` (the node created the native session), `invalid_argument/instance_required` (several instances, none named; `details.field` `instance`), `invalid_argument/malformed_request` with `details.field` `instance` (an instance of another adapter), `not_found/instance_unknown`, `not_found/session_not_attached` (`create` false and no open endpoint), `refused/endpoint_limit` (`details.limit` `node`; not for `create` false), and while the node starts or stops as for every request that changes state. The wake information is never logged or stored, and no refusal names a value of it |
| `endpoints.close` | `OpManageEndpointSession`; params `{"endpoint": "<node>/<endpoint>", "allow_attached"?}` | `{"endpoint","outcome"}`, outcome `closed`, `closing` (a session the node created, whose native session is still to be released) or `already_closed`; a runtime endpoint is refused `endpoint_kind`, an address of another node is `endpoint_unknown`. An open endpoint of an attached session, which a person is using, is refused `refused/attached_close_not_allowed` unless `allow_attached` is `true`; the refusal changes and stores nothing, so the identifier stays free for the request that sets it. An endpoint that is closed already is answered `already_closed` either way |
| `daemon.stop` | `OpStopDaemon`; no params | `{"stopping":true}`; the daemon then shuts down; a resend, even to a daemon that was started again, asks for the stop again |
| `daemon.restart` | `OpRestartDaemon`; params `{"expect_version"?}` | `{"restarting":true,"program","version","start_id"}`: the program that will be started, the version it reported, and the `start_id` of the daemon that is going away, which the client waits to see change; the daemon then shuts down as for a stop and the caller of `Run` starts the program again. Before it accepts, the daemon finds out what the restart would start (`Relauncher.CheckRestart`; a daemon with no relauncher cannot restart) and refuses, shutting nothing down and storing nothing, as `refused/restart_unavailable` (no relauncher, or what is started would not outlive the daemon; the wire says only that, and the daemon logs the reason), `refused/restart_check_failed` (the program was run and did not answer as expected; the message carries the error of the check, which only the local owner can read) or `conflict/restart_version_mismatch` (the version it reports is not `expect_version`). A resend returns the stored result without checking anything, and asks for the restart again only of the daemon that stored it |
| `status` | `OpRead`; no params | `{"node":{id,version},"daemon":{state,started_at,start_id},"reminders":[{kind,message}],"alerts":{open},"clock":{rollback_paused,rollback_paused_since?,high_water?},"certificate":{not_after,remaining_s},"capacity":{open_deliveries,node_queue_limit,outbound_backlog,disk_free_bytes,disk_reserve_bytes},"deliveries":{<every state>:n},"endpoints":[{address,kind,instance,state}]}`. `start_id` is 16 lower-case hexadecimal digits drawn from the node's randomness when the management interface opens, so it differs between two starts of one node; `started_at` runs to the second only, and a process that replaces itself keeps its process number, so neither tells a client that a restarted daemon is the new one. The reminders come first for a reader and stay until done: `revocation_backup_unconfirmed` is true while the confirmation on disk is not of the statement this node holds. `deliveries` counts the deliveries the node holds as a target, the control signals among them, and lists every state, with zero where there is none. The endpoints that are blocked and the turns started by prompts are the figures of the features that do not exist yet |
| `alerts.list` | `OpRead`; params `{"include_acknowledged"?}` | `{"alerts":[{id,kind,since,subject?,detail,acknowledged}]}`, oldest first, the acknowledged ones only on request. While the clock is paused the list ends with the alert of the pause, `id` `clock_rollback`, which is no record and is lifted by clearing the pause; `status` counts it in `alerts.open`, so the number agrees with the entries the list shows as not acknowledged, and `alerts.ack` of it answers `not_found/alert_unknown` with the words that it is cleared by `sidera clock clear-rollback` |
| `alerts.ack` | `OpAcknowledgeAlert`; params `{"alert": "<32 hex digits>"}` | `{"alert","acknowledged":true}`; `not_found/alert_unknown` for an alert the node does not hold; an identifier that is not 32 hex digits is `malformed_request` |
| `identity.export_revocation` | `OpExportRevocation`; no params, no operation identifier | `{"armored","digest"}`: the statement that retires this node, in the armor the keys format, and the digest the backup is confirmed with. Authorized as an operation of the owner's alone and stored nowhere: its result would be a copy of the statement |
| `identity.confirm_backup` | `OpConfirmRevocationBackup`; params `{"digest"}` (64 lower-case hex digits) | `{"confirmed_at"}`; a digest that is not the one of this node's statement is `invalid_argument/digest_mismatch` and changes nothing |
| `identity.retire` | `OpRetireIdentity`; params `{"node"}` | `{"node","retired":true,"stopping":true}`; the node must be this node (`invalid_argument/node_mismatch`); the daemon then stops, and every later start refuses to run. A resend asks for the stop again |
| `clock.clear_rollback` | `OpClearClockRollback`; no params | `{"cleared","high_water","now"}`; `cleared` is false when no pause was in force |
| `node.show` | `OpRead`; no params | `{"node","version","settings":{…}}`, the settings as `node.json` holds them |
| `node.set` | `OpChangeNodeSettings`; params `{"settings":{…patch}}`, carries the expected version | `{"settings":{…},"version"}`; a patch is judged whole by `config`: `invalid_argument/config_invalid` (with the path), `empty_patch`, and `conflict/version_conflict` with the current version |
| `endpoints.set` | `OpChangeEndpointSettings`; params `{"endpoint","settings":{…patch}}`, carries the expected version of the instances | `{"endpoint","settings":{…},"version"}`, the settings the owner has given the endpoint, those left to the node default not among them; an endpoint the node does not have is `endpoint_unknown` |
| `network.show` | `OpRead`; no params | `{"node","peer_id","version","listen":{"configured":[…],"actual":[…]},"contact","peers":[{"node","addresses":[…],"certificate_held","connected","profile"?}]}`. `version` is that of `network.json`, the one a change names. `listen.configured` is the list in the file and `listen.actual` the addresses the listeners are open on now (the two differ after a change that waits for a restart). `contact` is the line one owner gives another: `<node id> <multiaddr>…`, the actual listen addresses that are neither loopback nor link-local, an unspecified `0.0.0.0` or `::` expanded into the machine's interface addresses of its family, each ending in `/p2p/<peer id>`; it is the empty string when the node has no address to give (it listens nowhere or only on loopback). `peers` are the nodes of the address book and those whose certificate is held, by node id: `addresses` are the ones the owner entered (empty for a node known only by its certificate), `certificate_held` says that a certificate was accepted from it, `connected` that a member connection to it is up, and `profile` is what the node says about itself (the profile object below), left out when this node holds none for it |
| `network.address.set` | `OpChangeNodeSettings`; params `{"node","addresses":[…]}`, carries the expected version of `network.json` | `{"node","addresses":[…],"version"}`, the addresses as kept (written in the form the multiaddr library prints, repeats dropped); they replace those of the node and take effect at once. While no certificate is held for the node, one address must end in `/p2p/<peer id>`; once one is held, every `/p2p/` must be that certificate's peer. A breach of a rule of an address, or of that rule, is `invalid_argument/config_invalid` with `details.path` (`addresses[1]`, or `addresses` for the list) and `details.reason` |
| `network.address.remove` | `OpChangeNodeSettings`; params `{"node"}`, carries the expected version of `network.json` | `{"node","version"}`; a node with no address in the book is `not_found/peer_unknown` |
| `network.listen.set` | `OpChangeNodeSettings`; params `{"addresses":[…]}` (required; empty means the node does not listen), carries the expected version of `network.json` | `{"listen":[…],"version","restart_required":true}`: the listeners open once, at the end of the start, so the list is kept and takes effect with the next start; `listen.actual` of `network.show` does not change until then |
| `trust.expect` | `OpConfirmTrust`; params `{"node"}` | `{"node","expires_at"}`: the owner means to pair with the node, which stands for a confirmation made in advance; entering the node again replaces its expectation. The node itself, or one the pairing refuses to take, is `invalid_argument/malformed_request` with `details.field` `node`; a full list of expectations is `refused/expectations_full`. A pairing in progress or paused does not refuse it |
| `trust.forget` | `OpRejectTrustRequest`; params `{"node"}` | `{"node","forgotten":true}`; a node with no expectation is `invalid_argument/malformed_request` with `details.field` `node` |
| `trust.reject` | `OpRejectTrustRequest`; params `{"pairing_id"}` (64 lower-case hex digits; anything else is `invalid_argument/malformed_request` with `details.path` `params.pairing_id`) | `{"pairing_id","node","status"}`, the fields `trust.list` gives the request: the owner does not want the request, whichever side of the pairing this node is. A request held as a target is rejected (`status` `rejected`); one this node started is withdrawn (`status` `withdrawing`), the target learns of it with the next exchange, and a signature that may have left is revoked first. A request that is not there, or no longer waits (it ended, was confirmed already, or ran out), is `invalid_argument/malformed_request` with `details.field` `pairing_id` and stores nothing. While this node may not sign (its clock is paused, or its identity retired) it is `backpressure/clock_rollback` or `unavailable/identity_retired`, because withdrawing may store a revocation |
| `trust.request` | `OpConfirmTrust`; params `{"node","addresses"?}`. `addresses` are dial hints for this request only, never kept: they are not saved to the address book, so after the pairing each side needs `network address set` for the other before messages flow (at most 16, each judged by the rule of a peer address: `invalid_argument/malformed_request` with `details.path` `params.addresses[i]`, or `params.addresses` for more than 16); without them the node is dialed on the address book. An empty list and an absent one are one request | `{"pairing_id","node","status","code","refusal"?}`, **answered when the online segment of the pairing ends**. `pairing_id` is 64 lower-case hex digits and `code` the six digits (`482 019`) both nodes show for the request, which are informational: trust is established when each owner has entered an expectation for the other (`trust.expect`, on both nodes, before the request), and nothing confirms a request by its code. `status` is `awaiting_target` (this node's owner confirmed and signed), `awaiting_initiator_signature` (this node's owner has yet to confirm), `complete`, or `refused`. A refusal by the target is a successful result: `status` `refused`, `refusal` the target's word (`paused`, `busy`, `not_accepted`, `clock_skew`, `protocol_error`), and `pairing_id` and `code` empty because no request was saved. A node that is retired, locally removed, this node or already trusted is `invalid_argument/malformed_request` with `details.field` `node`; a pairing in progress, or an identical resend while the first request is in flight, is `conflict/pairing_in_progress`; a paused pairing is `backpressure/pairing_paused`; while this node may not sign (its clock is paused, or its identity retired) it is `backpressure/clock_rollback` or `unavailable/identity_retired`; a node that cannot be reached (the dial failed, or the segment ended with neither a saved request nor a refusal) is `unavailable/node_unreachable`, stores nothing and leaves the operation identifier free. A resend after the answer returns the stored result with `replayed: true` and dials nothing |
| `trust.list` | `OpRead`; no params | `{"trusted":[{"node","credential","created_at","not_after"?,"connected","profile"?}],"expectations":[{"node","expires_at"}],"incoming":[{"pairing_id","node","status","code","expected","expires_at"}],"outgoing":[{"pairing_id","node","status","code","reason"?}],"locally_revoked":["node"…]}`; every list is `[]` when empty. `trusted` are the credentials that count now, by node and digest (lower-case hex); `not_after` is left out for a credential that does not expire, and `profile` (the profile object below) for a node this node holds none for. `incoming` are the requests this node holds as a target (`node` is the initiator, `expected` says the initiator was an expected node) and `outgoing` those it started (`node` is the target), ended ones included, by pairing identifier. An outgoing `status` is `awaiting_initiator_signature`, `awaiting_target`, `complete`, or the pairing package's word for the others (`withdrawing`, `rejected`, `expired`, `cancelled`, `unknown`, `gave_up`, `withdrawn`); an incoming one is `awaiting_initiator_signature`, `awaiting_target` (the initiator's signature is in and this node's owner has yet to confirm), `complete`, `rejected`, `expired` or `cancelled`. `reason` (`declined`, `mismatch`) is left out when the owner gave none |
| `trust.remove` | `OpLocallyRevokeNode`; params `{"node"}` | `{"node","revoked_credentials":["<hex digest>"…]}`: the node is refused whatever path leads to it and the credentials held with it are revoked now (empty when none were, or when the node was removed before). The other end is not told, so a pairing with it again is refused by it (a target refusal, `not_accepted`) while it still holds the credential: its owner has to remove the node too |
| `trust.clear` | `OpClearLocallyRevoked`; params `{"node"}` | `{"node","new_pairing_required"}`: the node can be paired with again; `new_pairing_required` is true when its removal had revoked credentials with it, which stay revoked, so it is trusted again only through a new pairing. A node that was not removed changes nothing. The other node still refuses a new pairing until its owner has removed this node and taken it back as well |
| `adapters.install` | `OpInstallAdapter`; params `{"source": "<absolute path of the package directory>"}` | `{"adapter":{"id","version"},"already_current"?,"replaced"?}`: neither member when the adapter was new, `already_current` when the same version with the same contents was installed already, `replaced` when a package that was installed was replaced, an upgrade or the same version with other contents. The daemon copies the package outside the loop and judges the copy; a directory it cannot read, or a directory or file inside the package, is `invalid_argument/source_unreadable` (with `details.path`, the place inside the package, when that is where the fault is), a manifest that does not parse or a package that lacks its program `invalid_argument/config_invalid`, a package for another platform `refused/platform_unsupported`, a package whose adapter protocol versions share none with the ones this node speaks `refused/protocol_unsupported` (installing it over a running instance would otherwise restart that instance into a crash loop at every handshake), a second installation while one is copied `conflict/install_in_progress` (the message says that this installation is still in progress when the request is the one being copied, and that another is being installed otherwise); a package that replaces the installed one, another version or the same version with other contents, is not refused for an instance, enabled or disabled with its process still ending: it is replaced, the enabled instances are restarted, which keep their session endpoints, and the files of the replaced package are kept while a process may still run from them (`conflict/adapter_in_use` stays in the vocabulary, and this method does not return it); such a replacement runs new code as enabling does, so for a requester who is not the local owner it is `refused/not_permitted` and nothing is replaced. The same request again answers with its stored result and writes nothing. The package that is already installed (the same version, the same contents), in another request, writes only its result, and the result says `"already_current":true` (the member is left out when the installation replaced something) |
| `adapters.list` | `OpRead`; no params | `{"version","adapters":[{id,version,fault?:{reason}}],"instances":[{instance,adapter,enabled,probe?:{state,at?,frontends:[{preset,frontend}]},fault?:{reason},availability?:[{condition,endpoints?,detail?}]}]}`. `version` is that of the instances. `probe` is there for an enabled instance only: `pending` until the adapter has reported and each token issued for the probe is presented or expired, `passed`, or `failed`; `frontends` are those whose token the node saw presented. `fault` is there for an enabled instance whose installed package cannot run, as the adapter host last reported it, until the instance is online again; `reason` is `package_invalid` (the package on the disk could not be loaded: its pointer or manifest is missing, unreadable, invalid or of another adapter or version), `platform_unsupported`, `protocol_unsupported`, `program_missing` or `program_not_executable`; the detail (which file, what is wrong) is in the daemon log. `availability` is there for an enabled instance that is online and whose adapter last reported conditions with `adapter.availability`: one entry for each, with the `condition` (`runtime_auth`, `native_unavailable` or `dependency_missing`), the `endpoints` it is limited to (absent for every endpoint) and the adapter's own `detail`. It is what an adapter with direct endpoints only shows the owner, since it has no runtime endpoint whose status carries it; it is left out when the adapter reported none or an empty snapshot, and while the instance is offline, because the snapshot belongs to a connection that has ended. A bad package faults only its adapter: the daemon starts and the other instances run, and installing the package again (`adapters.install`, which restarts the enabled instances) recovers it without restarting the daemon. An adapter whose package could not be loaded is listed in `adapters` with the version its pointer names, empty when the pointer is unreadable, and with `fault` whose `reason` is `package_invalid`: that member is a fact of the installed package, there whether or not an instance of the adapter is enabled, and stays until the package is installed again, while the `fault` of an instance is what the host last reported of a running one |
| `adapters.setup_info` | `OpReadAdapterSetup` (the local owner's alone, a read that stores nothing and takes no operation identifier: the configuration it returns may hold a secret); params `{"instance"}` | `{"instance","adapter","state_dir","config","setup_argv"}`. `adapter` is the adapter the instance belongs to; `state_dir` the absolute path of the instance's state directory; `config` the values the owner set for the instance, the same object `adapter.start` passes as `config`, a JSON object (`{}` for an instance with no values); the daemon does not apply the manifest's field defaults, the setup program applies them as the adapter does. `setup_argv` is `null` when the installed package declares no `setup` in its manifest, and otherwise the argv that runs it: the absolute path of the setup program inside the package directory of the installed version and revision the adapter's pointer names (the one the instance runs, or will run once restarted), resolved by the daemon as it resolves the manifest's `entry`, followed by the manifest's `setup.args`. The daemon resolves it so that a caller never reads a manifest: a command of an older release (the `sidera update` that has just replaced its own program) runs the setup of a newer package correctly. Everything needed to run the setup is in `setup_argv`: the caller executes it directly, with no shell and under the `os/exec` rules, as the launcher runs the entry, in its own working directory and environment; a later version adds no member the caller must honour to run it correctly (a need like that becomes a new method). The daemon does not hold the returned path: a concurrent `adapters.install` that replaces the package can remove it while the setup runs, which fails visibly and is cured by running the setup again; and a package for another platform still returns its argv (only a package that cannot be loaded is refused), which then fails at exec. The member is present, `null` or not, in every answer. An instance that is not there is `not_found/instance_unknown`; one whose adapter is not installed `not_found/adapter_unknown`; one whose installed package could not be loaded (the `package_invalid` fault of `adapters.list`) is `refused/package_invalid`, since what such a package declares is not known, which is not the same as declaring no setup; installing the package again recovers it |
| `adapters.enable` | `OpEnableAdapter`; params `{"adapter","instance"?,"config"?}`, carries the expected version of the instances | `{"instance","enabled":true,"version"}`. The instance is named after the adapter when the request leaves it out; `config` is for a new instance and an instance that exists takes none (`conflict/instance_exists`; `adapters.config` replaces the configuration of one that exists); an adapter that is not installed is `not_found/adapter_unknown`; an instance of another adapter `conflict/instance_adapter_mismatch`. The `config` of a new instance, or its absence, is checked against the instance fields of the adapter's manifest: a value the field does not allow, a key that is no field, or a missing required field is `invalid_argument/config_invalid` with `details.path` `config.<key>` (a list item `config.<key>[<index>]`) and `details.reason` the rule it breaks; a package that cannot be loaded is `not_found/adapter_unknown` too. The host is asked to run the instance after the commit; whether it came up is read from `adapters.list` |
| `adapters.config` | `OpEnableAdapter`; params `{"instance","config"}` (a JSON object, not null), carries the expected version of the instances | `{"instance","enabled","version"}`. The object replaces the instance's configuration whole (`{}` clears it, which a required field refuses) and is checked as `adapters.enable` checks one, against the manifest of the instance's adapter; an instance that is not there is `not_found/instance_unknown`. The instance may be enabled or not; a running one is stopped and started again after the commit, so that its next `adapter.start` carries the values. It is authorized as enabling because the values can name the program the adapter runs |
| `adapters.remove` | `OpDisableAdapter` (it only withdraws, as disabling does); params `{"instance"}`, carries the expected version of the instances | `{"instance","removed":true,"version"}`. The instance and its endpoint settings leave `instances.json`. An instance that is not there is `not_found/instance_unknown`; one still enabled is `conflict/instance_enabled`; one with session endpoints open is `conflict/instance_in_use` (`details.reason` says how many); records still being closed from its disabling are waited for, as `adapters.enable` waits, and so is the end of the adapter's process. The adapter's state directory (`adapter-state/<instance>`) is deleted with the instance; when it cannot be moved aside (on Windows a file in it is still open) nothing is removed and the answer is `conflict/instance_state_busy` (`details.reason` names the path). The stable endpoint ids stay, so an instance made later under the name finds the same endpoints |
| `adapters.disable` | `OpDisableAdapter`; params `{"instance"}`, carries the expected version of the instances | `{"instance","enabled":false,"version"}`; the instance stays with its settings and endpoint ids; the host is asked to stop it and its endpoints are closed |
| `presets.list` | `OpRead`; no params | `{"version","presets":[{name,local_sources,description?,config?}]}` by name |
| `presets.add` | `OpChangePreset` as loosening; params `{"name","preset":{…}}`, carries the expected version of the presets | `{"name","version"}`; the document must say `local_sources` (`allow` or `deny`: `invalid_argument/config_invalid` with the path); each adapter's section of `config` is checked against the preset fields of that adapter's manifest, and a value it does not allow is `invalid_argument/config_invalid` with `details.path` `config.<adapter id>.<key>`; a section for an adapter that is not installed, or whose package cannot be loaded, is `not_found/adapter_unknown` with `details.name` `config.<adapter id>`; a name that is taken is `conflict/preset_exists`; a node that already holds `MaxPresets` (20) presets refuses another as `invalid_argument/config_invalid` with `details.path` `name` |
| `presets.remove` | `OpChangePreset` as narrowing; params `{"name"}`, carries the expected version of the presets | `{"name","version"}`; `not_found/preset_unknown` |
| `operation.result` | `OpQueryOperationResult`; params `{"operation_id"}` | the result stored for that identifier of the owner, as the request that ran it was answered; `not_found/operation_unknown` when there is none (never used, or refused, which stores nothing). A read of the table the replay check uses, so it finds results kept under `state/admin/` and in the versioned files of `config/` |

**The profile object.** `trust.list` and `network.show` give a node's row a `profile` when this node holds one for it: `{"name"?,"host_name"?,"platform"?,"system"?,"system_build"?,"sidera_version"?,"as_of"}`, a member left out when the node gave none, `as_of` the Unix seconds at which this node fetched it. A profile is held in memory only, so a node that has not been connected since the daemon started has none; the member is then absent and not empty. The object is the type `adapterproto.PeerProfile`, made from the engine's `ProfileView` by `apiwire.PeerProfileOf`, and the engine is the one place a profile is looked up (`TrustedNode.Profile`, `NetworkPeer.Profile`), so no interface rebuilds one. The profile is a label for a person to tell nodes apart; nothing here addresses or judges a node by it.

A binary built with a test tag has **test rows** besides these: methods named
`test.<something>` that are the test harness's door to the node (the test clock,
quiescence, the crash points). They are outside the catalogue of administrative
operations and not judged by `admin.Authorize`: they have no catalogue entry, carry
no operation identifier and store no result, and they exist only in a binary built
with the `testclock` or `crashtest` tag, which is never shipped; without the tag the
request is `method_unknown`, which is how a test asserts that the way in is missing.
`test.ping` exists with either tag. `testclock` adds `test.clock.set` with
`{to_ms}` (absolute Unix milliseconds, including rollback) and `test.quiesce`
with `{expect: {instance: {notifications, held}}, timeout_ms}`. `held` is a
multiset of request identities (a method, a `#` and a digest of the params, as
`adapterhost.CallID` builds it). A positive timeout is required. Quiescence
returns `inbox_len`, `continuations`, `due_entries`, `parked`, `workers`,
`adapters_pending`, `satisfied`, and an `error` string when it fails; a failure
keeps the counters in a successful HTTP response for the fixture to explain.
`crashtest` adds `test.crashpoints`, whose `{points: [{key, kind}]}` is the
complete validated daemon table, sync points included, with `normal` or
`recovery` kinds. The composition root supplies it through the tagged
`manageapi.TestConfig`. The tags are independent: `crashtest` alone enables
neither clock method.

Errors are `{"error":{"category","code","message","details"?}}` in the words of
`apiwire`, which is the one mapping from what the engine, `config`, `ipc` and the
decoder say. Ended request contexts are `unavailable/request_canceled` and
`unavailable/request_timeout`, with fixed messages describing only the context;
typed errors take precedence. A written request with an incomplete answer remains
`outcome_unknown`, and so is a connection, once obtained, lost before the request was written (the
daemon closed a kept-alive connection while it shut down), which the restart wait and
the resend of a change treat as "not yet". Categories are `invalid_argument` 400, `unauthenticated` 401,
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
explicit idempotency key may create another message. A client that was
answered success and cannot read the answer says `internal/answer_unreadable` (exit
code 1), not `outcome_unknown`: the node said what happened, so no resend is
advised, and the likely cause is a program older than the node. A refusal for an
invalid argument with no detail is `malformed_request`. The text of an error nobody
expected is logged and never sent. Some words belong to the owner's operations alone:
`not_found/alert_unknown`; `invalid_argument/empty_patch`, `node_mismatch`,
`digest_mismatch` and `source_unreadable`; `not_found/peer_unknown`; `conflict/install_in_progress`;
`refused/platform_unsupported`; `refused/protocol_unsupported`; `refused/package_invalid`, which is what a read of the setup of an instance whose installed package cannot be loaded is told; `refused/not_permitted`; `refused/attach_unsupported`, `refused/native_session_created`, `not_found/session_not_attached` and `invalid_argument/instance_required`, which are what the attachment of a session is told; `refused/attached_close_not_allowed`, which is what the owner's close of an attached session that does not say it means it is told; `conflict/pairing_in_progress`, `backpressure/pairing_paused`, `refused/expectations_full` and `unavailable/node_unreachable`, which are what the owner's operations on trust are told; and `unavailable/identity_retired`, which is what
anything that would sign is told between the retirement and the end of the daemon.

Who the local owner is: whoever connected to the management listener, which is
the user's own socket or pipe, checked by `ipc` on every accept. `manageapi.Listen`
takes only that address, and `Server.Serve` only a listener that `Listen` made, so
the actor of the owner is made in that one package and a scan keeps every other file
of the runtime tree and of `cmd/` from making it. There is no token on this
interface: a sandbox must deny the socket to an agent. This is more than a rule against
administering the node: the answer of `sessions.attach` is a session token, so whoever can
reach the socket can obtain the credential of an endpoint of this node.

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
- **A resend of a restart restarts only the daemon that stored it, where a resend of a
  stop stops either.** A stop asks for a state (no daemon), so a resend asks again and
  a daemon that died between storing the result and stopping is stopped by it. A
  restart asks for a process that started after the request. The daemon that stored
  the result has not yet been replaced, and the answer may not have reached the client
  (a request whose connection was lost after the engine stored its result never asked
  for the restart), so its resend asks again, which is harmless: the shutdown runs
  once. A daemon that loaded the result from the disk is the process the restart
  produced, and restarting it again would loop with a client that resends after an
  unknown outcome. The rule is the engine's (`Ending.replayed`), for every operation
  that ends the daemon.
- **A restart is checked before it is accepted, and not for a resend.** Once the
  daemon has shut down nobody is left to report that the program would not start, so
  what would be started is found out first, and a client that has just replaced the
  program names the version it expects, so that a daemon started from another path is
  not restarted into the wrong program. The check is the engine's to make, after its
  own lookup of the request: a resend is answered with the stored result whatever the
  program is by then, so a restart that happened is never hidden by a refusal.
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
- **The network is built at step 6 and listens at step 15.** The engine and the network
  are made with each other as their node (the network calls the engine's handler, the
  engine pushes its membership to the network), so neither exists before the other and
  the composition root, which imports both, converts between their two `Membership`
  types and between the two types that carry the facts of a pairing connection. The host is built, not listening, at step 6 so that the node can dial from
  the moment the loop runs; the listeners open last, after every hook of step 15, so
  no other node reaches a node that does not serve yet. A port in use fails the start
  naming the address, and what had begun is shut down like any failed start: a node
  that cannot be reached is not one the owner should believe is running. The network
  key is taken from the keys once, before the loop exists, because the keys are not
  safe for concurrent use. The network closes at shutdown step 2 beside the HTTP
  servers: it stops taking connections from other nodes before the adapters and the
  engine go, and its close waits for every goroutine the network started.
- **The daemon works in its data directory.** An adapter process inherits the daemon's
  working directory, and the directory the daemon was started from is not the daemon's
  to keep: a daemon started inside a git worktree that was removed later left every
  adapter in a directory that no longer exists, and a program started there, such as
  `claude auth status`, fails with "The current directory no longer exists", so every
  session failed as unavailable. The data directory is the one directory the node owns
  for as long as it runs, so the start enters it, after the lock is held (a start that
  is refused changes nothing) and before any adapter starts. It fails the start when it
  cannot, rather than run on in a directory nobody chose. Adapters that run a native
  runtime do it in the directory their preset names (`project_dir`, `cwd`), absolute
  paths, never in this one; the paths the daemon is given (the data directory, the
  package directory of `adapter install`, the preset directories) are absolute, so
  nothing depends on where the daemon was started. The test
  `TestTheDaemonWorksInItsDataDirectoryWhateverDirectoryItWasStartedIn` starts a
  daemon in a directory, removes it, and reads the working directory in the adapters
  step and after the start.
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
- **`CREATE_NO_WINDOW` at the start of the program, one function.** It is the only
  choice measured to give a program a console without a window that what it starts
  shares. Rejected: `DETACHED_PROCESS` (the program has no console, so each console
  program it starts gets a window, and the problem moves one level down); a hidden
  console for the daemon itself (`AllocConsole` makes a visible window, and the
  variant without one needs Windows 11 24H2); starting the restarted daemon with the
  flag (it covers the restart only, not a service or WMI, and changes what
  `adapterkit/process.Start` promises); a `SysProcAttr` at each call site (one rule in
  two places, and the next call site forgets it).
- **A new package for the words of the wire** (`apiwire`) rather than strings in the
  engine: the engine says what happened in types, and every interface and client maps
  through one table, so a reason has one pair of words everywhere.

## Where

- Code: `internal/node/daemon` (`daemon.go`, `start.go`, `stop.go`, `init.go`,
  `home.go`, `table.go`, `logging.go`, `periodic.go`), `internal/node/manageapi`
  (`manageapi.go`, `methods.go`), `internal/node/apiwire`, `internal/node/logfile`,
  `internal/node/engine/manage.go`, `internal/node/childproc` (`Windowless`).
- Test of the window rule (`internal/node/childproc`, Windows only):
  `TestWindowlessChildAndGrandchildHaveAConsoleWithoutAWindow`.
- Tests of the adapter host's wiring (`adapters_test.go`):
  `TestEnabledAdapterInstancesAreStartedAtStepTenAndTheStepWaitsForThem`,
  `TestAnInstanceThatCannotBeStartedIsJudgedOfflineAndTheStartGoesOn`,
  `TestShutdownStopsTheAdaptersAtStepThree`, `TestTheDaemonTellsTheAdaptersWhyItEnds`, `TestAFatalFailureStillStopsTheAdapters`,
  `TestANodeWithoutAnAdapterHostStartsNoAdapter`: fake adapters in the test process,
  started by the one launcher the engine-layer fixture also uses (`harness`).
- Tests of the network's place in the start and the shutdown (`network_test.go`):
  `TestTheNetworkListensOnlyAfterTheNodeServesAndStopsWithTheInterfaces`,
  `TestAStartOnAPortInUseFailsNamingTheAddressAndLeavesNothingRunning`, and the check that
  the fatal path closes it in `TestAFatalStorageErrorEndsTheDaemonWithTheExitCodeOfAnIOError`.
- Tests of the daemon: `TestSecondDaemonOnSameHomeRefused`,
  `TestInitOnTheHomeOfARunningDaemonIsRefused`,
  `TestStartupStepsRunInSpecifiedOrder`,
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
  `TestARestartEndsTheDaemonWithARestartAndItsResendDoesNotRestartAgain`,
  `TestARefusedRestartLeavesTheDaemonRunning`,
  `TestOnlyAnOrderlyShutdownAfterARestartRequestEndsInARestart`,
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
  `TestInitWritesTheListenPortItIsGiven`,
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
  `client_test.go` covers envelopes, unchanged full responses, unknown outcomes
  retaining operation identifiers, damaged answers and no automatic resend;
  `FuzzClientResponse` exercises successful-response envelope decoding.
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

## Tagged process crash records

`crashtest` adds `daemon.NewCrashObserver(CrashConfig)`. The caller supplies an
append-only writer, positive generation, and optional canonical point target;
the daemon library never reads process environment. Each process owns its record
file. Each raw observation is written as one JSON line before its primitive:
`{"gen":1,"point":"sample/file.rename#1","path":"state/new.rec","from":"state/.tmp-raw","kind":"normal"}`.
`kind` is `normal` or `recovery`. `Path` and `From` keep the actual filesystem
operands; normalization belongs to the crash-walk consumer. A matching target
adds a second otherwise identical line with `"crash":true`, then sends SIGKILL
on Unix or calls `TerminateProcess` with exit code 3 on Windows. A mutex covers
both lines and termination, so no concurrent observation can split them.
Recording failures panic before the primitive; a missing marker or a normal
exit is never evidence of reaching a crash target. The record is appended
without syncing: process termination preserves the kernel page cache.

The CLI process point `proc/cli.recv-after-response` uses `kind:"proc"` and empty
path operands. It is after the complete HTTP receive page and before stdout,
and is deliberately outside the transaction table. `init` and `daemon` install
the same transaction observer through their existing `Config.Observer` seam.
The CLI owns opening and closing the file, and all test environment parsing.
