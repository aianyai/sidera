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
| `Config`, `Attachments`, `Env`, `PeriodicTask` | What a daemon needs, the hooks the features that come later attach to, what a hook is given, and a task on a period. |
| `ErrAlreadyRunning`, `ErrAlreadyInitialized`, `ErrNotInitialized`, `ErrPermissions`, `FatalError`, `ExitCode`, `ExitFatal` | How a start or an init is refused and how a daemon ends; 74 is a fatal failure of the disk. |
| `Table`, `Declarations`, `Transactions`, `LayoutWork`, `RepairLayout` | The one table of the program's transactions, the two this package declares (`init` and `layout`), and the layout rule. |
| `ClockStateEvery` | The period of the clock state. |

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
- **The engine is the single writer; this package writes only what is its own:**
  `init` and the layout rule, both declared here and covered by the crash tests.
- **The actor of the local owner is not made here.** The management interface is
  `manageapi`, and a scan keeps the actor out of this package.
- **A hook is called inside its step** with an `Env`, in the state of the engine the
  step needs, and a hook that fails stops the start.

## What the caller must guarantee

- `Config.Rand` is safe for concurrent use: it is read by the file layer, the engine
  and the keys. `crypto/rand`'s is, and a nil `Config.Rand` is that.
- Call `Init` before `Start`; a data directory is made by init and nothing else.
- Pass the context of `Start` for the start only: the node lives until `RequestStop`,
  a fatal failure, or the end of `Run`.
- A hook returns when its work for the step is done. The adapters hook returns when
  each enabled instance has answered or been judged offline, because the recovery
  of deliveries that follows depends on it.
- A task added in `Attachments.Periodic` runs in the loop and does not wait.
- Exit with `ExitCode(err)`, and on a fatal failure start again to scan the disk.

## Where the tests are

`second_test.go` has `TestSecondDaemonOnSameHomeRefused`; `init_test.go` init, its
crash states and the declared crash points; `permissions_test.go` and
`permissions_windows_test.go` the checks of the data directory and of `keys/`;
`startup_test.go` the order of the start, the hooks, the layout, the log, the
periodic tasks and the clock; `shutdown_test.go` the order of the shutdown, the stop
through the management interface, `Run` and a fatal failure; `replay_test.go` a
management operation resent through the whole daemon.
