# testkit

The test tooling of the runtime packages: stand-ins for what lies outside the daemon
(an adapter, a native runtime, the record of what happened), the interface of the fake
agent and the core of the crash-point walk. Read
[`../README.md`](../README.md) for the rules every runtime package follows and
[`docs/references/testkit/`](../../../docs/references/testkit/README.md) for the
reasons.

## Two sides

The packages fall on two sides of one line.

| Side | Packages | May import |
| ---- | -------- | ---------- |
| Third party | [`ledger`](ledger/README.md), [`fakectl`](fakectl/README.md), [`fakeruntime`](fakeruntime/README.md), [`fakeadapter`](fakeadapter/README.md), and the programs `cmd/sidera-fake-adapter`, `cmd/sidera-fake-runtime` | the standard library, `sdk/go/...` and each other |
| Sidera | [`fakeagent`](fakeagent/README.md), [`crashwalk`](crashwalk/README.md) | anything of the runtime tree and the contract packages |

The third-party side plays programs a stranger would write: an adapter that speaks
the published protocol, a native runtime that has never heard of Sidera, and the
ledger that records what they did. They import nothing of the repository but the SDK
module, so a test against them proves the protocol boundary and not an agreement
between two internal packages. `imports_test.go` enforces it with `go list -deps`
for the three operating systems and the tag sets that change the build. The Sidera
side may know the daemon: it is the part that drives it and judges it.

## The layers a test can run in

| Layer | Subject | Clock | The fakes |
| ----- | ------- | ----- | --------- |
| Unit and contract | one package | `now` parameter or `clocktest.Fake` | none |
| Engine | the engine, the real store and the adapter host in one process | one `clocktest.Fake` | in process: the adapter on `io.Pipe`, the runtime as an object |
| Binary | the built `sidera` and the two fake programs | the fixture's, sent over `fakectl` | programs, one runtime process per session |

The same fakes serve the engine and binary layers; only the way they are started
differs. The third-party packages are written so that both are the same code:
`fakeadapter.Serve` takes a reader and a writer, `fakeruntime.Launcher` is an
interface with an in-process and a process implementation.

## One test clock

Every time a fake reports comes from one clock: the adapter's `since_ms` and
`at_ms`, the runtime's turn times, the ledger's `at_ms`. In the engine layer the
fixture creates a `clocktest.Fake` and hands the method value `clock.Now` to every
fake as a `func() time.Time` (the fakes cannot import the clock package). In the
binary layer the fixture sets the daemon's clock first and then sends `clock.set`
to each fake process over `fakectl` and waits for every answer. The order matters:
the daemon treats a time that an adapter reports as later than its own now as
unknown, so a fake that is ahead of the daemon would make blocked-delivery tests
depend on how two clocks happen to relate. No fake sleeps, reads the wall clock or
ends a turn because time passed.

## The ledger is the oracle

Tests judge what happened from the ledger the fakes wrote, never from the daemon's
own state. An entry for every effect is written before the effect is confirmed:
the runtime writes `runtime.session.created` before the adapter writes
`native.create.returned` and before it answers `created`, so an effect that took
place is always on record, even if the process dies in the next instant. Bodies
never reach the ledger; a SHA-256 digest (`ledger.Digest`) stands for one. A
directory ledger is written by several processes, one file each, every entry
synced; it reads back whole except for the cut last line of a process the fixture
recorded as killed.

## Quiet

A fixture that advances the clock must wait until the system has stopped reacting,
and not sleep. The conditions that are the fakes' to answer:

- the fake runtime has no turn action running or waiting (`Controller.Idle`);
- the fake adapter has no unexpected request of the daemon in flight
  (`Knobs.Outstanding`), and the notifications it has sent
  (`Knobs.NotificationsSent`) are all handled by the daemon, which matters because
  the protocol peer handles notifications in order after the answer of a ping;
- the requests a knob holds on purpose (`Knobs.Held`) are expected, not in flight.

The conditions that are the daemon's (empty loop, no due timer, empty buffers) are
checked by the engine.

## Identifiers of crash points

A crash point is `tx/role.step#n`: a transaction, the role of the file in it, a step
of the file layer and the ordinal the transaction layer counted. They are the types
of `fsx` (`Point`, `Key`) and the declarations of `txn`; `crashwalk` adds nothing to
them.

## Rules for the packages

- **Time** only from the injected clock, as everywhere in the runtime tree; the
  scans of `internal/node/layout_test.go` apply, and none of the packages here is an
  exception for time. Real waiting is a channel or a context.
- **Files**: `ledger`, `fakeadapter` and `fakeruntime` write files that are not daemon
  state (the ledger, the runtime's records, the instance lock), and are on the
  scanner's exception list for that reason. `fakectl` and the Sidera side touch none.
- **Goroutines** are owned by a context or a `Close`, and the tests of each package
  fail on a goroutine left behind.
- **Deterministic**: native references, process identities and ledger order come from
  seeds and counters, never from the system's random source.
- **Strict decoders** for everything that crosses between processes (ledger lines,
  session records, control messages, knob arguments, process configuration), each
  with golden lines written by hand, a table of rejected inputs and a fuzz target
  that checks an accepted input re-encodes to what it was.

## What starts the fake adapter in process

An engine-layer fixture connects an adapter host to `fakeadapter.Serve` through a
pair of `io.Pipe`: it makes the two pipes, runs `Serve` on one end in a goroutine of
its own context, and gives the adapter host the other end as the process's standard
output and input. "Killing" the adapter is cancelling that context and closing both
ends; the fake runtime is a separate object and survives it, as a native process
survives its adapter.
