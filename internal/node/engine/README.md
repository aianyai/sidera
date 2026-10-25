# engine

The single writer of the node's runtime state. One loop owns the in-memory view
of every record and runs, one after another, every command that changes state;
each command makes its change durable through the transaction layer first,
updates memory second and answers last. Read
[../../../docs/references/engine/README.md](../../../docs/references/engine/README.md)
for the reasons and for what the tests prove.

This is a runtime package of layer 5 of [`internal/node/README.md`](../README.md):
it imports the contract packages, `txn`, `store`, `config` and `clock`, and
reaches the store, the configuration, the transaction writer and the adapter host
through the interfaces of `deps.go`. It does no file I/O of its own, reads no
clock but the injected one (the waits that are about real time name
`clock.System()`), and draws randomness only from `Deps.Rand`.

This package holds the loop, the scheduler, the registration of endpoints, the
capacity check, the storage of alerts and the two recovery entries. Sending,
receiving, prompts, control signals and feedback join the same loop as further
commands and add their methods to this list.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Open(ctx, Deps, Params)` | The scan entry: scans `state/`, runs the recovery rules until none has work, builds the view. Returns a `ScanReport`. |
| `Start`, `RecoverDelivery`, `Serve`, `BeginShutdown`, `Stop`, `Done`, `Err`, `Phase` | The life of the loop: opened, recovering, running, stopping, stopped or failed. `RecoverDelivery` is the second recovery entry; the table of the start steps is in the reference. |
| `AddPeriodic`, `AddClockState`, `Task`, `TaskInput` | A task the composition root has the loop run every period, before `Start`. The clock state's task is also run at once by the command that finds the wall clock set back. |
| `Quiesce` | Returns when no command waits, nothing is due and no goroutine of the engine runs. |
| `AdapterAdmitted`, `RegisterDirect`, `UnregisterDirect` | What the adapter host calls: the admission of an instance, and the registration and unregistration of its direct endpoints. |
| `Declare`, `Caller`, `ByToken`, `ByAdapter` | The declaration of an endpoint, by whoever the endpoint's own caller is, resolved in the command. |
| `Endpoints`, `CloseEndpoint`, `CloseRequest`, `CloseOperation`, `CloseResult`, `Counts`, `Alerts`, `RaiseAlert` | The engine side of `endpoint list` and `endpoint close`, the numbers of the status, and the alerts. A close that an administrative operation asks for is one command: replay check, close, and the result built from the outcome, stored in the same transaction in every outcome. |
| `CheckCapacity`, `CapacityInput`, `CapacityVerdict` | The one capacity check, a pure function. |
| `PlanRecovery`, `RecoveryPlan` | The work of the recovery rules as a pure function of a scan. |
| `Reason`, `Refusal`, `ReasonOf` | The closed set of reasons a method refuses for; the interface packages alone map them to the wire. |
| `Transactions()` | The declarations of the transactions of two owners that the engine opens: `direct-register`. |
| `AgentService`, `ManageService` | What the Agent and management interfaces ask of the engine; `*Engine` implements them, and the methods are added as the engine learns to do them. |
| `Store`, `Config`, `Beginner`, `Adapters` | What the engine asks of the store, the configuration, the transaction writer and the adapter host. |

## Rules

- **One loop, typed commands, the order of the inbox.** Every command is a type
  with a `run`; commands run in the order they entered the inbox. Commands a
  command queues for itself (the chunks of a long batch) run after the commands
  the inbox holds at that moment and before those that arrive later, so a busy
  inbox neither starves them nor is held up by more than one chunk. A command is answered exactly once, on a channel of capacity one,
  so the loop never waits for a caller that has gone.
- **A command that entered the inbox is run, whoever waits for it.** A caller
  whose context ends first learns only that it did not hear.
- **Two kinds of submitter.** An outside caller waits `Params.SubmitWait` for
  room in a full inbox and is then refused with `ReasonLoopBusy`. An internal one
  (the adapter host, the engine's workers, the recovery entry) waits, because its
  order must be kept and its back pressure is the buffer behind it.
- **One reading of the clock per command**, observed against the high water mark
  before anything else. A wall clock set back beyond the tolerance pauses new
  work that depends on a deadline until the owner clears it; moving the clock on
  does not clear it. The pause is made durable by the command that finds it.
- **Disk first, memory second.** Everything that can be refused is checked before
  `Begin`. A write that failed before its commit point was acknowledged to no one
  and changed nothing: `ReasonStorageFull` for a full volume, otherwise
  `ReasonStorageUnavailable`, and memory stays as it was. A fatal error stops the
  loop for good (`PhaseFailed`, `Err`), answers the command `ReasonStorageFatal`
  and refuses the rest; the composition root exits and the next start rescans.
- **The cap of endpoints is the node's.** An endpoint that is open or being
  created, of any kind, takes a place against `publication.MaxEndpointsPerNode`;
  the system endpoint is not registered and takes none. The adapter's own limit
  is judged first and says which limit was hit.
- **The same key is the same endpoint.** The id is kept in `config/` before the
  record is written, in one transaction; a crash between the two leaves an id that
  no record uses, which takes no place and is given back for the same key.
- **A closed endpoint is not opened again while records are queued for it.** The
  closing is a head record, then chunks of at most `Params.ChunkReplaces`
  records, then nothing. A registration of the key meanwhile is refused with
  `ReasonEndpointDraining`, which a retry cures.
- **Only the token of an open session endpoint opens the Agent interface.** The
  store keeps a token on every open record; the engine mints one for a direct
  endpoint and never lets it in.
- **What the engine owns about an endpoint is in the declaration field of its
  record:** the display name and description the adapter gave, and the
  declaration. One compact JSON object of a fixed shape; a text that is not the
  exact encoding of the value it decodes to is not read, and a record whose field
  cannot be read stops `Open`.
- **A recv that waits is a waiter the loop owns.** It is registered in the same
  command that found the queue empty, so no arrival can slip between the check and
  the registration. Any later command that makes a message of the endpoint ready
  hands the page to the waiter itself. The deadline is an entry of the schedule on
  the injected clock. An endpoint has at most one waiter: a newer one ends the
  older with an empty page. Shutdown ends every wait with an empty page, before
  the interfaces stop. The registry is in place; the command that registers a
  waiter and the page that wakes it come with the pull of messages.
- **The capacity check is in one place.** `CheckCapacity` judges the clock, then
  the queue, then the disk, and a bound that is not set refuses. The engine feeds
  it with the settings of the node, the free space and its own count of unfinished
  deliveries: target records that still need their body (accepted, queued or
  leased), accepted control records, and source deliveries no receipt answered;
  a delivery whose outcome is unknown holds no body and is not counted. System feedback, status reports, control
  replies and the local target side of a delivery do not go through it.
- **Recovery is a plan, then rules.** `PlanRecovery` is a pure function of a scan.
  `Open` runs the rules in the order the store's README fixes, works the plan out
  again from a fresh scan after a rule has written, and repeats until nothing is
  left. Work of a rule that is not wired yet (the quarantine of damaged files,
  the replay of prompts, blocks and feedback) makes `Open` return a
  `PendingRecoveryError` before it writes anything in that pass.
- **The recovery of deliveries is four phases in the order of the startup
  steps,** each a command of the loop that logs `startup step N begin` and
  `startup step N end`; each is empty until the feature it belongs to is added.
- **No method makes a session endpoint.** The engine has no method that takes or
  gives the record of one; a session endpoint exists only through a session
  creation, which is added with it.

## What the caller must guarantee

- Call `txn.Writer.Recover` and make the directories of `state/` exist before
  `Open`; load the configuration after `Recover`. `Open` reads what a power loss
  cannot take back only because the transaction layer settled it.
- Pass the persisted high water mark and the persisted pause in `Deps`, and add
  the task that persists them with `AddClockState`: it writes `TaskInput.HighWater`
  and `TaskInput.RollbackPaused`, and it is the loop that runs it at once when the
  pause begins, so the pause is on disk before the command that found it goes on.
- Call `AddPeriodic` before `Start`, `RecoverDelivery` between `Start` and
  `Serve`, and `BeginShutdown` before `Stop`.
- Admit an adapter instance with `AdapterAdmitted` before its endpoints register,
  and only an instance the configuration has enabled.
- Give `Deps.Rand` a reader the engine alone uses; it is read in the loop.
- Treat `Err() != nil` after `Done` as the order to exit and rescan.
- Run the long work of a periodic task outside the loop: a task runs in it, and
  the longest stall of the loop is what an adapter's notifications have to
  outlast.
- On a platform other than macOS, `isNoSpace` (`nospace_windows.go`) is pending
  verification by CI on main.

## Where the tests are

`endpoints_test.go` has `TestEndpointBeyondNodeCapRefused`; `close_test.go` the
closing and its chunks; `declare_test.go` the declaration and who may make it;
`recovery_test.go` the scan entry and the recovery entry; `loop_test.go` the
loop, back pressure and the fatal path; `sched_test.go` and
`sched_internal_test.go` the schedule; `waiters_test.go` the waits; `capacity_test.go` the capacity check;
`crash_test.go` the crash walk of `direct-register`; `static_test.go` the lists
the review holds the engine to; the `_internal_test.go` files the encoding of
the declaration field, the view and the batches of a drain.
`TestQuarantineClosesEveryCreatingSessionEndpointOfTheDamagedControlRecord` is a
requirement kept in sight and skipped until the quarantine is wired.
