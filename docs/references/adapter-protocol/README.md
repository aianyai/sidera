# Adapter protocol

How the daemon and an adapter process talk. The adapter's SDK lives in its own
Go module, `github.com/aianyai/sidera/sdk/go`, which depends on the standard
library only and is imported by the daemon and by third-party adapters alike.
This document has one part per layer.

## Peer layer

### What

Package `sdk/go/peer` is a symmetric JSON-RPC 2.0 peer over any
`io.Reader` / `io.WriteCloser` pair. The daemon runs it over the stdin and
stdout of an adapter child process; stderr stays a log. Either side can send
requests and notifications. The package is protocol-agnostic: it frames lines,
classifies messages, queues, answers, cancels and closes, and it knows nothing
about which methods exist.

Starting values of its limits (`peer.DefaultParams`), all configurable:

| Parameter | Value | Meaning |
| --------- | ----- | ------- |
| `MaxLineBytes` | 1 MiB | Longest message in either direction |
| `MaxPendingRequests` | 64 | Inbound request queue, per direction |
| `MaxConcurrentHandlers` | 16 | Request handlers running at once |
| `MaxPendingNotifications` | 256 | Inbound notification queue, handled in order |
| `MaxOutbound` | 256 lines | Outbound queue |
| `WriteTimeout` | 10 s | Longest a single line may take to write |

Behaviour, each point pinned by the tests named in "Where":

- **Framing.** A message is one JSON object plus `\n`. A line over
  `MaxLineBytes` is answered `-32004` with a null id and the connection is
  closed. Invalid UTF-8 (checked with `utf8.Valid`) and invalid JSON are
  `-32700` with a null id; a missing or wrong `"jsonrpc"`, a non-object, a
  batch, a null or malformed request id, or an id already in flight are
  `-32600`. After any of these except the overlong line the connection
  continues.
- **Overload.** A request that finds the request queue full is answered
  `-32001` before its params are examined and before any handler is started, so
  a `-32001` always means the handler did not run.
- **Notifications** are handled one at a time in arrival order; if the queue
  overflows the connection is closed. Unknown notifications are logged and
  dropped.
- **Cancellation.** `$/cancel_request {request_id}` is processed by the read
  loop and cancels the handler's context. The request is still answered exactly
  once: with its result, or with `-32800`.
- **Write timeout.** A timer per write closes the transport when a line is not
  written within `WriteTimeout`.
- **Backpressure.** When the outbound queue is full, local `Call` and `Notify`
  return `ErrOverloaded` and write nothing. Replies the read loop owes wait for
  room instead, which stops reading from a flooding counterpart for at most
  `WriteTimeout`.
- **Ids.** Strings or integers that fit in 64 bits. A late response, one whose
  call has timed out or been cancelled, is dropped and logged.
- **Close** is idempotent, ends every pending call with `ErrClosed`, cancels
  handler contexts and leaves no goroutine behind once handlers return.
  `Peer.Err()` says why the connection ended.

The error codes of the peer layer are constants in `sdk/go/peer/errors.go`:
`-32700` parse error, `-32600` invalid request, `-32601` method not found,
`-32602` invalid params, `-32603` internal error, `-32001` overloaded, `-32002`
shutting down, `-32003` not initialized, `-32004` line too long, `-32022`
unsupported protocol version, `-32800` request cancelled; codes 1001 to 1099
are the application range, whose individual meanings belong to the protocol
above the peer.

**The effect rule.** An error response says whether the request did anything.
`-32700`, `-32600`, `-32601`, `-32602`, `-32001`, `-32002`, `-32003`, `-32004`,
`-32022` and every application code mean "no effect": retrying is safe. Only
`-32603`, `-32800`, a timeout, no response and a lost connection leave the
result unknown; for methods with side effects the caller then reconciles or
retries idempotently. `peer.NoEffect` and `peer.ResultUnknown` encode this
once so callers do not re-derive it; local refusals before anything is written
(`ErrOverloaded`, `ErrNotSent`, `ErrMessageTooLarge`) count as no effect, and
anything not listed, including unknown codes, counts as unknown.

### Why

- **Written here rather than taken from a library.** The widely used JSON-RPC
  library for Go (`creachadair/jrpc2`) has a line channel with no length limit
  (the reader keeps appending while its buffer is full), an unbounded inbound
  queue, no way to refuse a request as overloaded before queuing it, no way to
  turn batches off, no cancel message on the wire, and no write timeout, so one
  stalled reader blocks the whole server. Closing those gaps around it costs as
  much code as the peer itself and adds two modules to a package third parties
  import. `golang.org/x/exp/jsonrpc2` has no newline framing and no
  compatibility promise; `sourcegraph/jsonrpc2` has no batches and unverified
  limits.
- **The SDK is its own module and the peer is one package of it.** Third
  parties then download only the SDK and the root module's dependency graph
  (libp2p and others) never reaches them. The daemon imports the same code, so
  there is one framing and message layer, not two. The repository root `go.work`
  ties the modules together for development; there is no `replace` directive,
  because a module with one cannot be installed with `go install ...@version`.
- **An overlong line closes the connection instead of being skipped.** Skipping
  to the next newline would resynchronize in the middle of an oversized message
  and let its tail be read as a message. The reader also refuses an
  unterminated flood as soon as it passes the limit, without buffering more.
- **Overload is decided before queuing.** Refusing after parsing params would
  spend work on exactly the traffic that is overloading the peer, and a refusal
  after the handler started could not promise that nothing happened. The
  guarantee "`-32001` means the handler did not run" is what makes a retry safe.
- **Notifications are never dropped; overflow closes the connection.** A
  silently dropped "turn ended" or "prompt persisted" notification would leave
  the owner believing an agent is busy or idle when it is not. Closing forces a
  reconnect and a full resynchronization, which is correct by construction.
- **A cancelled request is still answered.** If the receiver could stay silent,
  the caller could not tell "cancelled" from "lost". One answer per request,
  always, keeps the caller's bookkeeping simple. A cancel only asks the handler
  to stop, so a handler that finishes anyway has its real result delivered.
- **Cancel is handled by the read loop.** A cancel queued behind the work it is
  meant to stop would arrive after the work finished.
- **The write guard is a timer that closes the transport.** Deadlines on files
  are not available for every pipe on every platform (Windows anonymous pipes),
  and a `Write` that blocks forever would block the connection for good.
  Closing the transport is the one operation that unblocks it everywhere
  tested.
- **Replies owed by the read loop wait for room.** Dropping them would break
  the "every request is answered" promise. Waiting stops the read loop, which
  is the right backpressure on a flooding counterpart, and the write timeout
  bounds it. The cost is that two peers that both flood each other can stall
  each other until the timeout fires, after which the connection is closed.
- **`id` is a string or a 64-bit integer.** Fractions, exponents and `null`
  have several spellings of one value (`1`, `1.0`, `1e0`), which would defeat
  the in-flight check and cancellation; rejecting them removes the ambiguity.
  Null ids stay reserved for errors that cannot name a request.
- **A response with a valid id but an invalid body fails its call.** Dropping
  it would leave the caller waiting for its timeout on a counterpart that
  already answered.
- **A handler's non-`*Error` failure is answered with a fixed message.** The
  text of an internal error can contain paths, queries or secrets; it is logged
  locally and never sent.
- **Message bodies are never logged.** They may carry user text and tokens.
- **Zero means "default" in `Params`.** A zero limit would otherwise mean "no
  queue" or "no timeout", which is never what a caller who left it out wants;
  a negative value is a mistake and is an error.
- **Left to the layers above:** `initialize`, `ping`, `shutdown` and their
  states ("not initialized", "shutting down") are method semantics. The peer
  gives the owner the pieces (handlers, the codes, `Close`); a draining
  shutdown and an admission hook were left out because their shape depends on
  the adapter serve loop that has not been written.

### Where

| Concern | Code |
| ------- | ---- |
| Line reader with the length limit | `sdk/go/peer/frame.go` (`frameReader`) |
| Classification of one line, id keys, outbound encoding | `sdk/go/peer/message.go` (`classify`, `idKey`) |
| Error codes, `Error`, effect rule | `sdk/go/peer/errors.go` (`NoEffect`, `ResultUnknown`) |
| Peer lifecycle, parameters, termination | `sdk/go/peer/peer.go` |
| Writer, write timeout, read loop | `sdk/go/peer/io.go` |
| Request and notification dispatch, cancellation | `sdk/go/peer/serve.go` |
| `Call`, `Notify` | `sdk/go/peer/call.go` |
| Package guide | `sdk/go/peer/README.md` |

Tests (all in `sdk/go/peer`; wire behaviour is tested against a raw byte
connection, API behaviour between two peers over `io.Pipe`, always with `-race`;
`leakGuard` fails any test that leaves a peer goroutine behind):

| Behaviour | Tests |
| --------- | ----- |
| Overlong line closes; exactly-max accepted; nothing after is read | `TestOverlongLineClosesConnection` |
| Invalid JSON, invalid UTF-8 | `TestInvalidJSONKeepsConnection`, `TestInvalidUTF8IsParseError` |
| Missing or wrong `jsonrpc`, bad shapes | `TestMissingOrWrongVersionIsInvalidRequest` |
| Batch rejected, connection kept | `TestBatchIsRejectedAndConnectionContinues` |
| Overload before params and handler | `TestOverloadedIsDecidedBeforeParamsAndHandler` |
| Cancellation, one reply | `TestCancellation`, `TestCallContextCancellation` |
| Write timeout; slow reader tolerated; also when the last line is the reply to an overlong line | `TestWriteTimeoutClosesTransportOnStalledReader`, `TestSlowButProgressingReaderDoesNotTimeOut`, `TestOverlongLineWithStalledReaderStillCloses` |
| Owed error replies wait for room and are never dropped (flood backpressure) | `TestReadLoopWaitsWhenErrorRepliesCannotBeQueued` |
| Notification order and overflow | `TestNotificationsAreHandledInArrivalOrder`, `TestNotificationOverflowClosesConnection` |
| Outbound full | `TestOutboundQueueFullReturnsErrOverloadedAndWritesNothing`, `TestRefusedCallLeavesNoPendingEntry` |
| Duplicate and invalid ids | `TestDuplicateInFlightIDIsInvalidRequest`, `TestInvalidRequestIDs` |
| Late response, malformed response | `TestLateResponseIsDroppedAndLogged`, `TestMalformedResponseFailsTheCall` |
| Unknown method and notification | `TestUnknownMethodIsMethodNotFound`, `TestUnknownNotificationIsLoggedAndDropped` |
| Close, pending calls, goroutines | `TestCloseEndsPendingCallsWithErrClosed`, `TestCloseWithoutStartAndCloseFromHandler`, `TestCloseWaitsForReaderAndWriter`, `TestNoGoroutineLeakAfterManyPeers` |
| Effect rule | `TestEffectClassification`, `TestHandlerErrorsAndEffectRule` |
| No bodies in logs | `TestLogsNeverContainMessageBodies` |
| Many concurrent calls both ways against an independent wire model | `TestConcurrentCallsInBothDirections` |
| Frame reader and classifier against separate oracles | `TestFrameReader`, `TestClassify`, `FuzzFrameReader`, `FuzzClassify` |
