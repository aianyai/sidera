# daemon

The composition root of the node. It takes the instance lock, checks the data
directory, loads the layers in the order of the start, wires them to one another,
opens the management interface, runs the periodic tasks, and shuts everything down
in the order of the shutdown; `init` is here too, because the transaction that makes
a data directory writes the files of every owner. Read
[../../../docs/references/daemon/README.md](../../../docs/references/daemon/README.md)
for the steps, the reasons and what the tests prove.

This is a runtime package of layer 7 of [`internal/node/README.md`](../README.md): it
imports every package below it and is the only one that connects across layers. It
is the one runtime package that reads `crypto/rand`, to hand the reader down. It
touches files only through `fsx`, and reads time only through the injected clock.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Start(ctx, Config)`, `Run(ctx, Config)` | Take the fifteen steps of the start and return a `*Daemon`; `Run` also waits for the end of the context, a signal or a stop, and returns how the daemon ended. |
| `Daemon.RequestStop`, `Wait`, `Done`, `Engine`, `Home` | The one entry for a shutdown, from any goroutine and any number of times; the end, and what ended it. |
| `Init(ctx, Config)`, `InitReport` | Make a data directory; never overwrite. |
| `Config`, `Attachments`, `Env`, `KeysView`, `ConfigView`, `PeriodicTask` | What a daemon needs, the hooks the features that come later attach to, what a hook is given (read-only views of the configuration and of what of the keys no renewal changes, the instance lock's assertion, the engine, the logs), and a task on a period. |
| `Config.AdapterHost` | The adapter host (`*adapterhost.Host`), or nil for a node that starts no adapter. Given to the engine at step 6, started at step 10 and shut down at step 3 of the shutdown. |
| `ErrAlreadyRunning`, `ErrAlreadyInitialized`, `ErrNotInitialized`, `ErrPermissions`, `ErrEngineNotStopped`, `FatalError`, `ExitCode`, `ExitFatal` | How a start or an init is refused and how a daemon ends; 74 is a fatal failure of the disk, in the start as in a running daemon. |
| `DefaultShutdownGrace`, `FatalGrace`, `Config.ShutdownGrace` | How long each step of the shutdown has to stop in. |
| `Table`, `Declarations`, `Transactions`, `LayoutWork`, `RepairLayout` | The one table of the program's transactions, the two this package declares (`init` and `layout`), and the layout rule. |
| `ClockStateEvery`, `CertificateCheckEvery` | The period of the clock state, and of the look at the certificate. |

## Rules

- **The order of the start is the log.** Every step logs `startup step N begin` and
  `end`; a test reads them. A step that fails stops the start and what had begun is
  shut down by the same path as any shutdown.
- **Refuse before touching.** The permissions of the root and of `keys/` and the
  marker of `init` are judged before the lock is taken, and the lock before
  anything is written. Nothing is narrowed, repaired or created by a start that
  refuses. A symbolic link as the data directory is refused.
- **One shutdown path, lock last.** An orderly stop, a fatal failure and a failed
  start take the same five steps; `RequestStop` runs it once.
- **The adapter host is wired by field, not by hook.** `Config.AdapterHost` is given to
  the engine (only when not nil), started at step 10 with `Env.Home` the absolute data
  directory (the instance lock files `adapter-<instance>.lock` are at its root),
  `Env.Source` the configuration view the hooks get, `Env.Logs` the daemon's log handle
  and the engine behind `apiwire.AdapterEngine`; step 10 ends when each instance has
  answered `adapter.start` or been judged offline, and the `Adapters` hook runs after
  it. Shutdown step 3 shuts the host down within the grace (the short one after a fatal
  failure of the disk), then runs `StopAdapters`. The host reports nothing to the engine
  at shutdown, so the fatal path writes nothing. The hooks stay for what else starts
  and stops with the adapters.
- **The engine reads an adapter package through this package.** `Deps.OpenPackage` is
  `fsx.OpenOS` of the directory the owner named, handed over as a reader: the package
  is on the machine's own file system, whatever file system the data directory is on,
  and the engine, which touches no file of its own, asks for a reader and copies from it.
- **The engine is the single writer; this package writes only what is its own:**
  `init` and the layout rule, both declared here and covered by the crash tests.
- **The certificate is renewed at step 5 and every hour in the loop, never when the
  engine says the node may not sign.** The hourly check reads `TaskInput.SignRefusal`
  and nothing else (the clock in doubt, or an identity retired and a daemon not yet
  down); step 5 asks `engine.SignRefusalOf` the same, with the pause step 4 found. Step 5 is the one goroutine there is, so it does not meet the loop; the
  hourly check runs in the loop, where the engine reads the certificate it signs
  under, so a renewal and a signature are never concurrent. A certificate that is due
  while the node is paused waits for the owner, and the log says so. The keys are not
  safe for concurrent use, and the loop changes the certificate, the serial seen and
  the state of a renewal every hour, so `KeysView` holds only what no renewal
  changes (`Node`, `NetworkSigner`, `SelfRevocation`); a hook that needs the
  certificate or the serial reads it through the engine.
- **The actor of the local owner is not made here.** The management interface is
  `manageapi`, and a scan keeps the actor out of this package.
- **A hook is called inside its step** with an `Env`, in the state of the engine the
  step needs, and a hook that fails stops the start. The `Env` holds no transaction
  writer and only read-only views of the keys and the configuration: the engine is
  the single writer, and one declaration has one open transaction, so a hook that
  changes state asks the engine. `Env.Lock` is what `ipc.Listen` asks of a hook that
  listens on an address of this home.
- **A fatal failure of the disk ends the start as it ends a running daemon.** The
  error `Start` returns is a `*FatalError` (exit code 74) when it was a fatal storage
  error, the engine's refusal for one, or the engine had died, whichever step met it.
- **The lock is not let go under an engine that has not ended.** `Wait` returns
  `ErrEngineNotStopped` and the process exit lets the lock go.
- **The signal is taken before the start.** `Run` cancels the context of `Start` on
  SIGINT or SIGTERM, and the start stops before its next step and unwinds.
- **Lines logged before the log opens carry the time it opened.** They are kept and
  written first, with the attributes of the logger that wrote them; the time on a
  line is the injected clock's at the moment it is written.

## What the caller must guarantee

- `Config.Rand` is safe for concurrent use: it is read by the file layer, the engine
  and the keys. `crypto/rand`'s is, and a nil `Config.Rand` is that.
- Call `Init` before `Start`; a data directory is made by init and nothing else.
- Pass the context of `Start` for the start only: the node lives until `RequestStop`,
  a fatal failure, or the end of `Run`; a context that ends during the start stops it
  before the next step.
- End the process after `Wait` returns `ErrEngineNotStopped`: the instance lock is
  held until it exits.
- A hook returns when its work for the step is done. The adapters hook returns when
  each enabled instance has answered or been judged offline, because the recovery
  of deliveries that follows depends on it.
- A task added in `Attachments.Periodic` runs in the loop and does not wait.
- Exit with `ExitCode(err)`, and on a fatal failure start again to scan the disk.

## Where the tests are

`second_test.go` has `TestSecondDaemonOnSameHomeRefused`; `init_test.go` init, its
crash states and the declared crash points; `permissions_test.go` and
`permissions_windows_test.go` the checks of the data directory and of `keys/`;
`identity_test.go` the retirement, the renewal of the certificate, the reminder and the rollback pause through the whole daemon; `certificate_test.go` the failures of the renewal and its crash states; `owner_test.go` the settings, the alerts and the status through the management interface; `startup_test.go` the order of the start, the hooks, the layout, the log, the share
of the log budget, the periodic tasks and the clock; `shutdown_test.go` the order of
the shutdown, the stop through the management interface, `Run` and a fatal failure;
`fatal_start_test.go` a fatal failure in each step of the start that can be injected
(the layout rule, the recovery, the pause of the clock, the scan, a hook that meets
the death of the engine); `interrupt_test.go` the lock kept under an engine that has
not ended, a stop that races with a failed start, a context or a signal during the
start; `adapters_test.go` the adapter host at step 10 and step 3, over fake adapters in the test
process (the launcher is `harness.AdapterLauncher`); `adapters_install_test.go` installing, enabling, listing and disabling an adapter, and the presets, through the management interface of a whole daemon;
`env_test.go` what a hook is given; `logging_test.go` the deferred log handler;
`periodic_test.go` the clock state's pause; `replay_test.go` a management operation
resent through the whole daemon.
