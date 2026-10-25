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
encoded, and returns a `Receipt`; `Pull` takes a `PullRequest` (one message) and
returns a `PullAnswer`. `*Loopback` has both, so it satisfies that interface
without importing the engine, and the engine imports no transport package for
it; `daemon` hands the one to the other. This is the convention of
[`internal/node/README.md`](../../../internal/node/README.md): each side of a seam
defines the interface it needs, and the graph stays acyclic because of it.

**A receipt is the target's signed records.** `Receipt` holds the signed status
reports, one for each recipient of the envelope on the target node, and the signed
control reply when the envelope was a control signal that already has one; a
`PullAnswer` has the same shape. They are `record.Record` values the transport
does not read: the sender verifies them with the `envelope` package and applies
them with its own rules. A receipt may lack the reports of some or all recipients
and the control reply: what is not in the receipt is pulled later, exactly as for
a send whose answer never came. A missing record has the meaning of an empty
answer to a pull: the target has nothing to report yet, not that the delivery does
not exist. A sender therefore never reads the number of records of a receipt as a
statement about the recipients.

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
| `HoldReports(msg)`, `Release(msg)` | The reports of the message are kept from the source: the receipt of its send is empty and a pull for it is answered empty without asking the target. The target processes the envelope as usual. |
| `Delay(d)` | Every call waits `d` on the injected clock before it goes on; a context that ends during the wait ends the call. `Waiting()` counts the calls whose time has not come. |
| `Mutate(msg)` | After the target took the next send of `msg`, the same envelope from the same sender is delivered once more with a changed body (a changed thread when it has no body). The sender sees only its own send. `Mutated(msg)` returns the target's answer to the replay. |

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
- **A pull asks for one message.** The sender keeps pulling a message until its
  deliveries are final, and every pull is about one message's recipients on one
  node. A list of messages is a wire optimization for a later transport and can be
  added without changing what a pull means.
- **Knobs are in the package, with no build tag.** The engine-layer tests run in
  plain `go test`, and a knob that needed a tag would make them a second kind of
  build. The price is fault code in the production binary that nothing in the
  daemon turns on: a loopback made without knobs shows no fault.
- **A delay waits inside the call, on the injected clock.** The outbound worker
  takes its work in order, so holding the call is holding the queue, which is what
  postponing a dequeue means, and nothing outside the transport has to know about
  it. The cost is that a worker waiting for a delay looks busy; `Waiting()` exists
  so the engine's quiescence check can tell a wait for the clock from work that has
  only to run, counting a call whose time is due as work. Handing the delay to the
  engine's schedule instead would need the engine to know a transport's knobs.
- **`HoldReports` also empties the receipt of the send.** The first report a
  source gets is normally the one in the receipt of its own send. A knob that held
  only pulls would leave the source with the report before any pull, and the
  situation it exists to make (a reply with its evidence arrives before the source
  has the report) could not be built without also losing the receipt. The evidence
  a reply carries inside its envelope is outside this seam and not touched.
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
| `Knobs` and the hit counts, the observer, `Waiting` | `internal/node/transport/knobs.go` |
| One row per knob, asserting effect and hit | `TestKnobs` in `knobs_test.go` |
| Knobs are per `Knobs` value, nil means none, safe while calls run, named | `TestKnobsAreNotSharedBetweenLoopbacks`, `TestNilKnobsMeansNoFaults`, `TestKnobsAreSafeToDriveWhileCallsRun`, `TestKnobNamesAreDistinct` |
| The local origin: the loopback makes it, a remote transport cannot, and the source scan | `TestLoopbackMakesTheLocalOrigin`, `TestRemoteOriginIsNeverLocal`, `TestNoCallerCanClaimTheLocalOrigin` in `origin_test.go` |
| What the source scan reports, one synthetic source per rule, and that the legitimate shape passes | `originViolations` and `TestOriginScanCatchesWhatItNames` in `originscan_test.go` |
| The loopback's own reading of the bytes it carries, with the replay and the hold armed | `FuzzSendWithKnobsArmed` in `fuzz_test.go` |
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
