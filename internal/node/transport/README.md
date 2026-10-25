# transport

The seam between the engine and whatever carries an envelope to the node it is
for, and the loopback that carries it when that node is this one. Read
[../../../docs/references/transport/README.md](../../../docs/references/transport/README.md)
for the reasons and for what the tests prove.

This is a runtime package of layer 4 of [`internal/node/README.md`](../README.md).
It imports the contract packages `delivery`, `envelope`, `ident` and `record`, and
`clock` for the injected time. It starts no goroutine, touches no file and reads
no clock but the one its knobs were given.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Sender` | What the engine calls: `Send` an encoded envelope to a node and get its receipt, `Pull` the current reports of one message. |
| `Handler` | What a transport calls on the engine: `Inbound` for an envelope, `Pull` for a pull. Both are told the `Origin`. |
| `Origin`, `RemoteOrigin(node)` | Where a request came from. `Local()` is true only for a request the loopback handed over. |
| `Receipt`, `PullRequest`, `PullAnswer` | The signed records a target answers with, and what a pull asks for. The transport carries them and never reads them. |
| `ErrUnreachable`, `ErrTimeout` | The two ways a call gets no answer: the target did not see it, or it may have. |
| `NewLoopback(self, handler, knobs)` | The single-node transport; implements `Sender`. A nil `knobs` means no faults. |
| `NewKnobs(clock)`, `Knobs` | The fault knobs: `DropReceipt`, `Unreachable`, `HoldReports` and `Release`, `Delay`, `Mutate` and `Mutated`; `Hits`, `Observe`, `Waiting`. |

## Rules

- **A local origin is made in one place.** `Origin.local` is unexported, the
  constructor that sets it (`localOrigin`) is unexported and called only by the
  loopback, and nothing else in the package exports a way to make or change an
  `Origin`. A transport of another package can make `RemoteOrigin` only, which is
  never local, whatever node it names. A test reads the package source to keep
  this true.
- **The loopback reaches only its own node.** A call to another node is
  `ErrUnreachable` without a knob.
- **A receipt is the target's.** An error from the `Handler` passes through
  unchanged and means no receipt exists. `ErrTimeout` and `ErrUnreachable` are the
  transport's own: after a timeout the target may have processed the envelope, so
  the same bytes are sent again and the target deduplicates; after
  `ErrUnreachable` it did not see it.
- **Knobs are faults, not features.** They are all off in a new `Knobs`, are safe
  to set while calls run, count every hit and tell the observer, and exist in
  every build, tagged or not. A test that sets a knob asserts its hit count.
  - `DropReceipt(n)`: the next `n` sends the target processed return
    `ErrTimeout`. A send the target answered with an error is not counted.
  - `Unreachable(node, on)`: every call to the node returns `ErrUnreachable`
    without calling the target.
  - `HoldReports(msg)` and `Release(msg)`: the receipt of the send of `msg`
    carries no record, and a pull for `msg` is answered empty without asking the
    target. The target processes the envelope as usual. The evidence a reply
    carries inside its own envelope is not touched: it never goes through here.
  - `Delay(d)`: every call waits `d` on the clock of the knobs first, and a
    context that ends during the wait ends the call. Calls run one after another
    on the engine's side, so a delayed call holds up the ones behind it.
  - `Mutate(msg)`: after the target has taken the next send of `msg`, the same
    envelope from the same sender is delivered once more with another body (a
    bodyless envelope gets another thread). The sender sees only its own send;
    `Mutated(msg)` gives the target's answer to the replay.
- **Time is the injected clock's.** A delay is a timer of the clock the knobs were
  made with; a test advances the clock and never sleeps.

## What the caller must guarantee

- Call `Send` and `Pull` outside the engine's loop, from one worker that takes
  its work in order, and never from the loop itself: the `Handler` runs in the
  caller's goroutine and may submit a command to the loop and wait for it.
- Treat `ErrUnreachable` and `ErrTimeout` as "no answer" and `errors.Is` them;
  verify every record of a receipt or pull answer before applying it.
- Ask `Knobs.Waiting()` when judging whether the transport is quiet: a call that
  waits out a delay not yet due is not work, and one whose delay is due is.
- Give a `Knobs` a clock before any test sets a delay (`NewKnobs(nil)` panics on
  `Delay`).

## Where the tests are

`knobs_test.go` has `TestKnobs`, one row per knob and one row for none, each
asserting its effect and, through the rig, the hit counts of every knob;
`origin_test.go` has the rule on the local origin, including the scan of the
package source; `fuzz_test.go` sends arbitrary bytes through a loopback with the
replay and the hold armed, the one place the package reads untrusted input;
`leak_test.go` the check that no call is left running after a test.
