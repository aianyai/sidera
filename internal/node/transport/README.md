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
| `Handler` | What a transport calls on the engine: `Inbound` for an envelope, `Pull` for a pull. Both are told the `Origin`. The package defines it because it consumes it; the engine defines the interface it calls (`Send`, `Pull`) and every transport has those two methods. |
| `Origin`, `RemoteOrigin(node)` | Where a request came from. `Local()` is true only for a request the loopback handed over. |
| `Receipt`, `PullRequest`, `PullAnswer` | The signed records a target answers with, and what a pull asks for: one message and the recipients the source tracks on the target. The answer to a pull lists, for each recipient asked about, its signed report or the recipient in `NoRecord` (the target holds no record of that delivery); a recipient in neither means nothing yet. The transport carries them and never reads them. A receipt may lack the reports of some or all recipients and the control reply; they are pulled later. |
| `ErrUnreachable`, `ErrTimeout` | The two ways a call gets no answer: the target did not see it, or it may have. Every transport implementation returns them with this meaning. |
| `NewLoopback(self, handler, knobs)` | The single-node transport, with `Send`, `Pull` and `Waiting`. A nil `knobs` means no faults; a zero `self` or a nil `handler` panics. |
| `NewKnobs(clock)`, `Knobs` | The fault knobs: `DropReceipt`, `Unreachable`, `HoldReports` and `Release`, `Delay`, `Mutate` and `Mutated`; `Hits`, `Observe`, `Waiting`. Use `NewKnobs`: the zero `Knobs` is not usable. |

## Rules

- **A local origin is made in one place.** `Origin.local` is unexported, the
  constructor that sets it (`localOrigin`) is unexported and called only by the
  loopback, and nothing else in the package exports a way to make or change an
  `Origin`. A transport of another package can make `RemoteOrigin` only, which is
  never local, whatever node it names. A test reads the package source to keep
  this true: `localOrigin` is mentioned nowhere outside `loopback.go` and there
  only as a direct argument of the handler's `Inbound` or `Pull`, and no literal
  of `Origin` with elements, conversion to it or other name for it exists outside
  `origin.go`. The scan reads syntax; it does not police `unsafe` or reflection.
- **The loopback reaches only its own node.** A call to another node is
  `ErrUnreachable` without a knob.
- **A receipt is the target's.** An error from the `Handler` passes through
  unchanged and means no receipt exists. `ErrTimeout` and `ErrUnreachable` are the
  transport's own: after a timeout the target may have processed the envelope, so
  the same bytes are sent again and the target deduplicates; after
  `ErrUnreachable` it did not see it. The caller's own context error is returned
  as it is, neither of the two: the target may or may not have seen the call. The
  handler runs under the caller's context, and so does a replay.
- **A receipt may be partial.** It may lack the reports of some or all recipients
  and the control reply, and the missing ones are pulled later: the same meaning
  as a recipient that is in neither list of a `PullAnswer`, nothing yet. A
  receipt never says "no record". A sender never reads the number of records of a
  receipt as a statement about the recipients.
- **`NoRecord` is the answer of the node that was called.** It is not signed: it
  is believed because it came over the connection the target's network key
  authenticates, and it is no evidence for anyone else. A transport that cannot
  promise that the answer came from that node over such a connection must not
  hand over a `NoRecord`. The loopback is the node itself.
- **Knobs are faults, not features.** They are all off in a new `Knobs`, are safe
  to set while calls run, count every hit and tell the observer, and exist in
  every build, tagged or not. A test that sets a knob asserts its hit count.
  - `DropReceipt(n)`: the next `n` sends the target processed return
    `ErrTimeout`. A send the target answered with an error is not counted.
  - `Unreachable(node, on)`: every call to the node returns `ErrUnreachable`
    without calling the target.
  - `HoldReports(msg)` and `Release(msg)`: the receipt of the send of `msg` is
    empty, with no report and no control reply, and a pull for `msg` is answered
    empty without asking the target: nothing yet for every recipient, never a
    `NoRecord`, which only the handler can give. The target processes the envelope as usual.
    The evidence a reply carries inside its own envelope is not touched: it never
    goes through here.
  - `Delay(d)`: every call that starts from now on waits `d` on the clock of the
    knobs first, and a context that ends during the wait ends the call.
    `Delay(0)` affects only calls that start afterwards; one that is already
    waiting stays until its time is due. Calls run one after another on the
    engine's side, so a delayed call holds up the ones behind it.
  - `Mutate(msg)`: after the target has taken the next send of `msg`, the same
    envelope from the same sender is delivered once more with another body (a
    bodyless envelope gets another thread). The sender sees only its own send;
    `Mutated(msg)` gives the target's answer to the replay.
- **Arm a knob keyed by a message id before the message first goes on the wire.**
  `HoldReports` and `Mutate` act on the first call that reaches them after they
  are armed, and the engine's outbound worker runs on its own, so a knob armed
  after the send may be armed late, and nothing orders it against the worker. Hold
  the worker first with `Delay` or `Unreachable(self, true)`, arm the knob, then
  release the worker. The knobs that name no message have no such window.
- **A hit says which call it was.** `Hit` has the knob, the call (`CallSend` or
  `CallPull`), the node and, for the knobs about one message, the message, so a
  test can assert that a fault was reached by the call it meant.
- **Time is the injected clock's.** A delay is a timer of the clock the knobs were
  made with; a test advances the clock and never sleeps. `Waiting()` panics when
  the clock was set back below the start of a call that still waits: its due time
  would mean nothing, and a count that went on would hide the mistake.

## What the caller must guarantee

- Call `Send` and `Pull` outside the engine's loop, from one worker that takes
  its work in order, and never from the loop itself: the `Handler` runs in the
  caller's goroutine and may submit a command to the loop and wait for it.
- Treat `ErrUnreachable` and `ErrTimeout` as "no answer" and `errors.Is` them;
  verify every record of a receipt or pull answer before applying it.
- Ask `Loopback.Waiting()` when judging whether the transport is quiet: a call
  that waits out a delay not yet due is not work, and one whose delay is due is.
  The engine's quiescence condition reads this one number from the loopback it
  holds, and it needs no knowledge of the knobs.
- Define, on the engine's side, the narrow interface it calls (`Send` and `Pull`
  with the signatures of the loopback); this package does not name it.
- Give a `Knobs` a clock before any test sets a delay (`NewKnobs(nil)` panics on
  `Delay`).

## Where the tests are

`knobs_test.go` has `TestKnobs`, one row per knob and one row for none, each
asserting its effect, the hits (knob, call, node and message) and, through the
rig, the hit counts of every knob; `loopback_test.go` the constructor's panics,
the caller's context and `Waiting`;
`origin_test.go` has the rule on the local origin and the scan of the package
source, `originscan_test.go` the scan itself and the synthetic sources it must
report; `fuzz_test.go` sends arbitrary bytes through a loopback with the
replay and the hold armed, the one place the package reads untrusted input;
`leak_test.go` the check that no call is left running after a test.
