# Transport

The seam between the engine and whatever carries an envelope to the node it is
for, the loopback that carries it on a single node, and the fault knobs that let
a test break the loopback on purpose.

## What

**The seam has two sides, and each is defined by the side that consumes it.** A
transport calls the engine through a `Handler`: `Inbound` for an envelope and
`Pull` for a pull, each told the `Origin` of the request, and each running under
the context of the call that brought the request. `Handler` is defined in
`internal/node/transport`, which consumes it. The engine calls a transport
through the interface the engine defines for itself, with two methods: `Send`
takes the node, the sending endpoint and the envelope as the bytes the engine
encoded, and returns a `Receipt`; `Pull` takes a `PullRequest` (one message and the recipients the
source tracks on the target) and returns a `PullAnswer`. `*Loopback` has both, so it satisfies that interface
without importing the engine, and the engine imports no transport package for
it; `daemon` hands the one to the other. This is the convention of
[`internal/node/README.md`](../../../internal/node/README.md): each side of a seam
defines the interface it needs, and the graph stays acyclic because of it.

**A receipt is the target's signed records.** `Receipt` holds the signed status
reports, one for each recipient of the envelope on the target node, and the signed
control reply when the envelope was a control signal that already has one. A
`PullAnswer` has the same two fields and a third, `NoRecord`. They are `record.Record` values the transport
does not read: the sender verifies them with the `envelope` package and applies
them with its own rules. A receipt may lack the reports of some or all recipients
and the control reply: what is not in the receipt is pulled later, exactly as for
a send whose answer never came. A missing record means nothing yet, as a
recipient that is in neither list of a pull answer does, not that the delivery
does not exist: a receipt never says that. A sender therefore never reads the
number of records of a receipt as a statement about the recipients.

**A pull answers for each recipient it is asked about, in one of three ways.** The
request names the recipients on the target the source tracks a delivery of the
message to. For each of them the answer holds at most one
thing: the target's signed report, or the recipient in `NoRecord`, which says the
target holds no record of a delivery of the message from the asking node to it.
A recipient in neither list has nothing to report yet; a transport may hold
reports back, and that must never read as "no record". The source tells the three
apart without a count: a call that returns an error got no answer (unreachable or
timed out), a recipient in `NoRecord` has no record at the target, and a
successful answer that has the recipient in neither list means nothing yet. A
target that may not sign refuses the whole pull, so that "no record" never comes
alone while the node cannot vouch for the reports beside it.

`NoRecord` is not signed. The pull answer is the answer of the node the call was
made to, over a connection the network key of that node authenticates, and it
goes to the asking node alone: it is never forwarded and is no evidence for a third
party, which is why a status report is signed (a reply carries it to others) and
this is not. Whoever could forge it holds the target's network key and could
forge its signed reports as well. A transport that cannot promise that the answer
came from that node over such a connection must not hand over a `NoRecord`; the
loopback is the node itself. A node that stores for others and forwards what it
stored would have to sign the answer, and the contract is the one place to change
then.

**Two ways to get no answer.** `ErrUnreachable`: the target did not see the call.
`ErrTimeout`: the call went out, no answer came back, and the target may have
processed it, so the same bytes are sent again and the target deduplicates by
message id. Every transport implementation returns these two with this meaning,
wrapped as it likes, so that the engine's retry rules do not depend on the
transport; they live in this package because they are shared by all of them. Any
other error comes from the target and passes unchanged; it means no receipt
exists. The error of the caller's own context is returned as it is, neither of the
two: the target may or may not have seen the call.

**The origin.** `Origin` says where a request came from. `Local()` is true only for
a request the loopback handed to the `Handler`; a transport of any other package
can make `RemoteOrigin(node)` only, which is never local, whatever node it names.
The engine's rules for local sources (judged by endpoint declarations and resource
limits alone) rest on this value and on nothing the request says about itself.

**The loopback.** `NewLoopback(self, handler, knobs)` carries what leaves the
sender's side to the receiving side of the same node through the same two calls a
remote transport would use. It starts no goroutine: a call runs in the caller's
goroutine, which for the engine is its one outbound worker. A call to another node
is `ErrUnreachable`. `Waiting()` is the knobs' count of calls waiting out a delay
that is not yet due, zero with no knobs. `NewLoopback` panics on a zero node id or
a nil handler: it would make local origins that name nobody, or fail on its first
call.

**The knobs.** A `Knobs` value is made with `NewKnobs` and the clock a delay is
waited on, and is off until a test sets something; the zero `Knobs` has no maps and
is not usable. Each knob counts its hits and tells an optional
observer:

| Knob | Effect |
| ---- | ------ |
| `DropReceipt(n)` | The target processes the envelope; the receipt is thrown away and the send returns `ErrTimeout`. Spent one per hit. A send the target refused has no receipt and is not counted. |
| `Unreachable(node, on)` | A send or pull to the node returns `ErrUnreachable` and the target is not called. |
| `HoldReports(msg)`, `Release(msg)` | The reports of the message are kept from the source: the receipt of its send is empty, with no report and no control reply, and a pull for it is answered empty without asking the target: nothing yet for every recipient, never a `NoRecord`. The target processes the envelope as usual. |
| `Delay(d)` | Every call that starts afterwards waits `d` on the injected clock before it goes on; a context that ends during the wait ends the call. `Delay(0)` affects only calls that start afterwards: a call already waiting stays. `Waiting()` counts the calls whose time has not come, and panics when the clock was set back below the start of one of them. |
| `Mutate(msg)` | After the target took the next send of `msg`, the same envelope from the same sender is delivered once more with a changed body (when it has no body, the last character of its thread is changed, or a thread is given to one that has none, so the replay stays valid at the longest thread). The sender sees only its own send. `Mutated(msg)` returns the target's answer to the replay. |

**Hits.** A `Hit` names the knob, the call it acted on (`CallSend` or
`CallPull`), the node and, for the knobs about one message, the message. The
observer sees each hit in the goroutine of the call, so a test can assert that a
fault was reached by the call and for the message it meant, not only how often.

**Arming a knob keyed by a message id.** `HoldReports` and `Mutate` act on the
first call that reaches them after they are armed, and the engine's outbound
worker is asynchronous to the test, so a knob armed after the message was sent is
late and nothing orders it against the worker. A knob keyed by a message id is
armed before that message first goes on the wire: hold the worker first, with
`Delay` or with `Unreachable(self, true)`, arm the knob, then let the worker go.
The knobs that name no message have no such window.

## Why

- **The seam is the same for the loopback and for a remote transport.** Whether a
  target is on this node must not change how the engine sends: the engine encodes,
  sends, gets a receipt or no answer, retries by its own schedule, and pulls. A
  synchronous call straight into the receiving code would hide exactly the steps
  the faults act on, and would have to be replaced when a second node exists.
- **The local origin is decided where a request enters the node.** A node id in a
  request is a claim; the transport it came over is a fact. A connection that shows
  this node's own id (a second instance, a stolen key) must be judged as remote.
  Making the field unexported and the only constructor unexported, and called from
  the loopback alone, makes the rule structural: a later transport cannot get it
  wrong because it has no way to say it. A test reads the source to keep it so,
  because the compiler cannot see a second call site inside the package. The scan
  fails on: any mention of `localOrigin` outside `loopback.go`, called or used as
  a value; inside `loopback.go`, any use of it but as a direct argument of the
  handler's `Inbound` or `Pull` call, so it is never kept, returned or handed to
  another handler; outside `origin.go`, any composite literal of `Origin` with
  elements (keyed, unkeyed or with its type left out in a slice or map), any
  conversion to `Origin` and any other name declared for it; any assignment to
  the `local` field or a literal that sets it to anything but false; and any
  exported function, method, variable or field that builds, holds or returns an
  `Origin` other than `RemoteOrigin`. It cannot catch what source text does not
  show: `unsafe` or reflection writing the unexported field, or an edit that
  weakens the scan itself. It reads syntax, not types. The review of the
  package's local interface has to count those limits. Each rule has a synthetic
  source that the scan must report, and the legitimate shape must pass, so a scan
  that reports everything or nothing fails its own test.
- **Receipts are typed records, not bytes.** The envelope is bytes because the
  sender signs and the target judges exactly what was sent. A receipt is only
  ever read by the verifier of the envelope package, which takes `record.Record`;
  decoding it in the transport would be a second reader of a format that has one
  home. A remote transport encodes the records for its wire and decodes them back.
- **A receipt may lack records.** A transport may answer before the report of
  every recipient exists, and a remote one cannot wait for it; the loopback leaves
  records out on purpose when a test holds them. The pull is the one path by which
  a source gets what it lacks, so a missing record means what a recipient in
  neither list of a pull answer means, and the sender needs one rule for both
  instead of reading the count of a receipt.
- **A pull asks for one message and names its recipients.** The sender keeps
  pulling a message until its deliveries are final, and every pull is about one
  message's recipients on one node. The request names them because a target that
  is only asked for a message cannot tell "I hold no record of the recipient" from
  "nothing to say": with the recipients named, silence for one means "nothing yet"
  and the absence of a record is said. A list of messages is a wire optimization for a later transport and can be
  added without changing what a pull means.
- **Knobs are in the package, with no build tag.** The engine-layer tests run in
  plain `go test`, and a knob that needed a tag would make them a second kind of
  build. The price is fault code in the production binary that nothing in the
  daemon turns on: a loopback made without knobs shows no fault.
- **A delay waits inside the call, on the injected clock.** The delay is for the
  call itself, so it is applied where the call is made and nothing outside the
  transport has to know that a knob exists: an engine that scheduled the delay
  itself would need to know a transport's knobs, and a transport of another kind
  would have no such knob to tell it about. The outbound worker takes its work in
  order, so a call that waits holds the queue, which is what postponing a dequeue
  means. The cost is that a worker waiting for a delay looks busy, so the engine's
  quiescence reads one count, `Loopback.Waiting()` (the knobs' `Waiting()`), to
  tell a wait for the clock from work that has only to run: a call whose time is
  due is counted as work. The loopback exposes the count so that the engine needs
  only the loopback it holds.
- **`HoldReports` also empties the receipt of the send, the control reply
  included.** The first report a source gets is normally the one in the receipt of
  its own send, and a control reply comes the same way. A knob that held only pulls
  would leave the source with the report before any pull, and the situation it
  exists to make (a reply with its evidence arrives before the source has the
  report) could not be built without also losing the receipt. A receipt may lack
  records by contract, so the source pulls what it lacks. The evidence a reply
  carries inside its envelope is outside this seam and not touched.
- **A knob keyed by a message id is armed beforehand, and the test holds the
  worker to do it.** Keying by id keeps the knob's meaning the same on every run: a
  count of "the next send" would act on whichever message the worker took next. It
  costs a window between the send and the arming, which a test closes with a
  delay or an unreachable node as described above, rather than the knobs ordering
  themselves against a worker they do not own.
- **`Mutate` replays after the send instead of changing the send.** The replay is
  an extra delivery the source did not make, so the source's own state must not
  change; a changed send would be the source's own conflict. It is armed ahead and
  runs inside the call, in the outbound worker's order, so a scene's sequence of
  commands stays the same on every run and nothing has to keep message bodies
  after they were sent.
- **Rejected: faults as an engine-side wrapper.** A wrapper around the engine's
  send interface in the engine tests would have to be built again for every
  fixture and could not replay a message, which needs the loopback's `Handler`.
- **Rejected: an exported `Sender` interface in this package.** The engine is the
  consumer of it, and a package that defines the interface for its caller makes
  the caller import it for the shape of its own dependency. The convention is that
  the consumer defines what it needs; the loopback and any later transport have
  the two methods, and the engine's fixtures can pass a `*Loopback` directly.

## Where

| What | Where |
| ---- | ----- |
| `Handler`, `Receipt`, `PullRequest`, `PullAnswer`, the two errors | `internal/node/transport/transport.go` |
| `Origin`, `RemoteOrigin`, the unexported constructor of a local origin | `internal/node/transport/origin.go` |
| `Loopback`, `NewLoopback`, `Waiting`, the replay | `internal/node/transport/loopback.go` |
| `Knobs`, `Hit` and `CallKind`, the hit counts, the observer, `Waiting` | `internal/node/transport/knobs.go` |
| One row per knob, asserting effect, hit counts and the call, node and message of each hit; the control reply held with the reports | `TestKnobs` in `knobs_test.go` |
| A replay stays valid at the longest thread; `Waiting` fails when the clock is set back; `Delay(0)` leaves a waiting call; names of calls and knobs | `TestMutateKeepsTheThreadValidAtItsLongestLength`, `TestWaitingFailsLoudlyWhenTheClockIsSetBack`, `TestDelayZeroLeavesACallThatIsAlreadyWaiting`, `TestCallKindNames` in `knobs_test.go` |
| Knobs are per `Knobs` value, nil means none, safe while calls run, named | `TestKnobsAreNotSharedBetweenLoopbacks`, `TestNilKnobsMeansNoFaults`, `TestKnobsAreSafeToDriveWhileCallsRun`, `TestKnobNamesAreDistinct` |
| The local origin: the loopback makes it, a remote transport cannot, and the source scan | `TestLoopbackMakesTheLocalOrigin`, `TestRemoteOriginIsNeverLocal`, `TestNoCallerCanClaimTheLocalOrigin` in `origin_test.go` |
| What the source scan reports, one synthetic source per rule, and that the legitimate shape passes | `originViolations` and `TestOriginScanCatchesWhatItNames` in `originscan_test.go` |
| The loopback's own reading of the bytes it carries, with the replay and the hold armed | `FuzzSendWithKnobsArmed` in `fuzz_test.go` |
| The loopback carries the recipients of a request and a `NoRecord` answer whole, and a held pull is empty, never `NoRecord`, even for a message the target holds none of | `TestLoopbackCarriesTheRecipientsAndTheNoRecordAnswer`, `TestAHeldPullIsEmptyWhateverTheTargetWouldAnswer` in `loopback_test.go` |
| The constructor's panics, the caller's context reaching the handler and coming back as it is, `Loopback.Waiting` | `TestNewLoopbackRejectsWhatItCannotWorkWith`, `TestLoopbackPassesTheCallersContext`, `TestLoopbackWaitingIsTheKnobsOwn` in `loopback_test.go` |
| No call left running after a test | `leak_test.go` |

## Known gaps

- The engine does not call the seam yet: the send and receive features define the
  interface the engine calls, wire `Loopback.Waiting()` into its quiescence
  condition, and the engine-layer fixture attaches the knobs. Until then the tests
  use a fake `Handler`.
- A second node, and with it a remote transport and a choice of transport per
  target node, is not built. The seam is shaped so that a remote implementation
  adds a package and changes nothing here.
