# Adapter protocol

How the Sidera daemon talks to an adapter: a separate program that drives a
native agent runtime (Claude Code, Codex) or bridges a chat tool to the channel.
The daemon starts the adapter and the two exchange newline-delimited JSON-RPC 2.0
over the adapter's standard input and output; standard error is for logs. One
process is one adapter instance. The peers are symmetric: either may send
requests and notifications. "D to A" below means the daemon calls the adapter and
"A to D" the other way.

The SDK lives in its own Go module, `github.com/aianyai/sidera/sdk/go`, which
is imported by the daemon and by third-party adapters alike. The packages the
daemon imports use the standard library only; `adapterkit/process` and
`adapterkit/realpath` also use `golang.org/x/sys`. The frames are handled by
`sdk/go/peer`; the messages, enums, application error codes and manifest are
defined in the packages `adapterproto` and `manifest` and, for other languages, by
the JSON Schema under `protocol/adapter/v1`. The daemon-side decisions are in `internal/adapterreg`, and the way the daemon
supervises adapters and carries this protocol between them and the engine is in
[adapter-host](../adapter-host/README.md).
This document has one part per layer: the peer layer, then the messages and
methods, the control signals and the native process identity. How each method relates to the Agent Client Protocol is in
[acp-mapping.md](acp-mapping.md).

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

Behaviour under the default dialect, each point pinned by the tests named in "Where":

- **Framing.** A message is one JSON object plus `\n`. A line over
  `MaxLineBytes` is answered `-32004` with a null id and the connection is
  closed. Invalid UTF-8 (checked with `utf8.Valid`) and invalid JSON are
  `-32700` with a null id; a missing or wrong `"jsonrpc"`, a non-object, a
  batch, a member name that occurs twice in the frame, a null or malformed
  request id, or an id already in flight are `-32600`. After any of these except the overlong line the connection
  continues. The reply carries the id of the line only when the line has a
  string `method` and a valid id, that is, when it is recognisably a request;
  every other malformed line, which may have been meant as a response, is answered
  with a null id.
- **Request routing.** `Handle` registers a named handler; `HandleDefault`
  optionally registers one `DefaultRequestHandler(ctx, method, params)` for
  requests whose named lookup misses. Named handlers always win, including when
  they return `CodeMethodNotFound`. With no default, unknown requests retain
  their immediate `-32601`, before the in-flight id, queue and params checks.
  With a default, they share the same queue, in-flight ids, cancellation,
  concurrency and reply path as named requests. Selection runs under the handler
  lock on acceptance, and only workers invoke handlers. Both registration APIs
  work before and after `Start`; a replacement affects later selections, never
  queued requests. `HandleDefault` replaces the earlier default and panics on
  nil. Its raw params are nil when absent or null, otherwise an object or array;
  the application interprets their schema. Notifications and responses never
  enter the default request handler. A frame with an id and the cancel method
  name is a request and follows this routing without cancelling anything.
- **Overload.** A request that finds the request queue full is answered
  `-32001` before its params are examined and before any handler is started, so
  a `-32001` always means the handler did not run.
- **Notifications** are handled one at a time in arrival order; if the queue
  overflows the connection is closed. Unknown notifications are logged and
  dropped.
- **Cancellation.** `$/cancel_request {request_id}` is processed by the read
  loop and cancels the handler's context. Its params are decoded strictly, like
  the params of every method, so a cancel with an unknown or repeated member
  cancels nothing. The request is still answered exactly
  once: with its result, or with `-32800`.
- **Dialect.** `peer.Config.Dialect` sets the conventions that another
  JSON-RPC protocol on the same line rules may do differently, and nothing
  else: the cancel notification (name, how its params name the request, what
  `Call` sends), `SilentOnCancel`, opt-in document checks, and replacement codes
  for the peer's own `-32001` and `-32004`. The zero value is this protocol's, so the adapter
  protocol and its dependents never name a dialect. Under `SilentOnCancel` the
  request answer is *at most once*: a request whose cancellation found it in
  flight before its answer was queued is not answered, and its id is released;
  a cancellation after the answer was queued changes nothing. The effect rules
  (`NoEffect`, `ResultUnknown`) stay defined by this protocol's code table; a
  dialect that is only used for answering requests does not use them. `New`
  refuses a `CancelMethod` that is not a valid method name (1 to 128
  characters) and a `CancelParams` whose result does not marshal to a JSON
  object. A dialect's replacement codes and the shape of its `CancelParams` are
  the responsibility of the protocol that uses the dialect; the schema covers
  neither. `CancelTarget` runs on the read loop on input from the other side
  and the read loop does not recover, so it must be fast, must not block and
  must not panic. `RejectDuplicateKeysAndInvalidUnicode` is off by default:
  enabling it rejects duplicate members and unpaired UTF-16 surrogate escapes
  anywhere in the frame before dispatch or overload, with `-32600` and a fixed
  message. Unknown members and their values stay accepted. The rejection keeps
  the connection and runs no handler or cancel target. It echoes the raw
  top-level id only when it occurs exactly once and is valid, and the frame has
  exactly one string `method`; every other rejected frame, including a response,
  gets a null id. JSON syntax and UTF-8 errors remain `-32700`. The adapter
  protocol leaves this option off; its nested content remains the responsibility
  of the decoding layer.
- **Write timeout.** A timer per write closes the transport when a line is not
  written within `WriteTimeout`.
- **Backpressure.** When the outbound queue is full, local `Call` and `Notify`
  return `ErrOverloaded` and write nothing. Replies the read loop owes wait for
  room instead, which stops reading from a flooding counterpart for at most
  `WriteTimeout`.
- **Ids.** An integer of at most 2^53-1 in absolute value, or a string of 1 to
  128 characters: `peer.RequestID`, the only statement of the rule, which the
  schema repeats and a conformance test holds equal. A late response, one whose
  call has timed out or been cancelled, is dropped and logged.
- **Frames the peer sends.** Under the zero-value dialect, only lines that
  `message.schema.json` accepts: `params` is always an object (`nil` is sent as `{}`, a list or a scalar is
  refused with `ErrNotSent`), a result is always an object (anything else a
  handler returns is answered `-32603`), a method name has 1 to 128 characters
  (a longer one is refused locally), an error carries a message of 1 to 1024
  characters (an empty message becomes the name of its code, or `error`; a longer
  one is cut), `data` only as an object and on any code, and nothing over
  `MaxLineBytes` (an error reply that long is answered `-32603` too). A nil
  `*Error`, including a typed nil inside a wrapped error, is answered `-32603`.
- **Close** is idempotent, ends every pending call with `ErrClosed`, cancels
  handler contexts and leaves no goroutine behind once handlers return.
  `Peer.Err()` says why the connection ended.
- **Start and Close are safe to call concurrently**, in either order. The adapter
  host closes a peer on shutdown, and shutdown can arrive while the peer is still
  being started; a `Close` that waited for the reader and writer while `Start`
  was still registering them would return early and leave them running. Both
  operations decide under the peer's lock which came first: a `Start` after
  `Close` (or one that loses the race) launches nothing, and `Close` waits for
  everything a winning `Start` launched. `Start` has no result, so the late call
  is silent rather than an error.

The error codes of the peer layer are constants in `sdk/go/peer/errors.go`
(`peer.Codes()` lists them): `-32700` parse error, `-32600` invalid request,
`-32601` method not found, `-32602` invalid params, `-32603` internal error,
`-32001` overloaded, `-32002` shutting down, `-32003` not initialized,
`-32004` line too long, `-32022` unsupported protocol version, `-32800` request
cancelled; codes 1001 to 1099 are the application range, whose individual
meanings belong to the protocol above the peer (`adapterproto` defines 1001 to
1012). The codes below 1000 and the effect rule are defined here and nowhere
else: the message layer and the daemon refer to `peer`.

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
- **Default dispatch is application routing.** Dynamic method availability and
  protocol admission belong to the application. An optional fallback beside
  named registration gives it that responsibility without changing existing
  owners or adding a second scheduling path. It is not a dialect convention or
  a middleware hook: only a lookup miss chooses it, and a named handler's error
  never triggers another handler. Binding the selected function and method to
  the existing request handler keeps queueing, cancellation and replies in one
  place.
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
- **A dialect instead of a second message layer.** Another protocol that needs
  this peer's framing, classification, bounded queues, in-flight table and write
  timeout can also require document ambiguity checks, another cancel convention
  and different error codes.
  Writing it a message layer of its own would give the queue, concurrency and
  write-timeout rules a second home; exporting framing and classification as
  public API would leave them a second home as well and commit the SDK to a
  larger surface; splitting a protocol-agnostic line layer out of the peer
  reorganises the package and every dependent for the same result. The dialect
  is a parameter of the one place that already owns the rules, and the peer
  names no protocol: the values belong to the layer that uses them.
- **Silence after a cancel is a dialect, not a mode of this protocol.** Here
  "cancelled" and "lost" must be told apart, hence the answer to every
  cancelled request. A protocol whose rule is that a cancelled request gets no
  answer sets `SilentOnCancel`, and the cancel is ordered against the answer
  under the same lock that releases the id, so a request is never answered after
  a cancellation that preceded the queuing of its answer, and never twice.
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
- **`id` is a string or an integer every JSON implementation represents
  exactly.** Fractions, exponents and `null` have several spellings of one value
  (`1`, `1.0`, `1e0`), which would defeat the in-flight check and cancellation;
  rejecting them removes the ambiguity. A 64-bit integer would be rounded by a
  peer written in JavaScript or any language that reads numbers as doubles, and
  the echoed id would then name another request, so the range stops at 2^53-1,
  the bound the schema can state. The string length is counted in characters
  because that is what the schema counts. Null ids stay reserved for errors that
  cannot name a request.
- **An error reply carries an id only for a request.** A malformed line without
  a unique string `method` may be a response to a call of the other side.
  Answering it with its id would land in that side's own call-id space: the error
  would fail one of their calls that this side received and may have executed,
  and a code that
  says "no effect" can make them send it again. JSON-RPC sends no reply to a
  response, so the line earns an error with a null id, which is never the
  answer to a call and which the other side's peer logs and drops. The reply
  is kept rather than replaced by a log line only so that a counterpart that
  speaks a broken dialect gets a visible signal.
- **The peer writes only what the schema accepts** (under the zero-value
  dialect, which is the protocol's own). Third parties implement the
  schema, not the Go code: a frame the Go peer wrote that the schema rejects
  would be the Go side's bug that every other implementation has to work around.
  The peer is the one writer of frames, so the guarantee is kept in one place
  instead of in every handler, and a root-module test validates every line the
  peer's own tests make it write. What the peer cannot know is the protocol's
  application codes, so the handlers of the protocol layer return only those the
  protocol defines.
- **A repeated member of the frame is refused where the frame is read.** Two
  readers of `{"id":1,"id":2,...}` can disagree about which request is meant,
  and a cancel or an in-flight check would then act on another request than the
  one that is handled. The line is answered with a null id because its id
  cannot be trusted. Repeats inside params and results are left to the layer
  that decodes them (`strictjson`), so the peer does not scan what it does not
  interpret under the default dialect. The opt-in document check instead uses
  `strictjson.Decode` into `map[string]json.RawMessage`, which rejects ambiguous
  JSON at every depth without imposing a schema on unknown members. This keeps
  the frame boundary in one place for protocols that require the stronger rule.
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
  gives the owner the pieces (handlers, the codes, `Close`). The
  adapter's serve loop, with those states and the instance lock, is
  `sdk/go/adapterkit`, so that adapters do not each copy it.

### Where

| Concern | Code |
| ------- | ---- |
| Line reader with the length limit | `sdk/go/peer/frame.go` (`frameReader`) |
| Classification of one line, id keys, outbound encoding | `sdk/go/peer/message.go` (`classify`, `idKey`) |
| The request id rule, `$/cancel_request` params | `sdk/go/peer/requestid.go` (`RequestID`, `CancelRequestParams`) |
| Error codes, `Error`, effect rule | `sdk/go/peer/errors.go` (`Codes`, `NoEffect`, `ResultUnknown`) |
| Peer lifecycle, parameters, termination | `sdk/go/peer/peer.go` |
| Writer, write timeout, read loop | `sdk/go/peer/io.go` |
| Request and notification dispatch, cancellation | `sdk/go/peer/serve.go` |
| The dialect, its defaults | `sdk/go/peer/dialect.go` (`Dialect`) |
| `Call`, `Notify` | `sdk/go/peer/call.go` |
| Package guide | `sdk/go/peer/README.md` |

Tests (all in `sdk/go/peer`; wire behaviour is tested against a raw byte
connection, API behaviour between two peers over `io.Pipe`, always with `-race`;
`leakGuard` fails any test that leaves a peer goroutine behind):

| Behaviour | Tests |
| --------- | ----- |
| Optional default dispatch, named priority, unchanged raw method/params and fixed queued selection | `TestDefaultRequestRouting`, `TestDefaultHandlerReplacementKeepsQueuedSelection`, `TestParamsMustBeStructuredAndNullMeansAbsent` |
| Named and default routes share overload, in-flight ids, cancellation and document checks | `TestOverloadedIsDecidedBeforeParamsAndHandler`, `TestDuplicateInFlightIDIsInvalidRequest`, `TestCancellation`, `TestDialectCancelledRequestIsNotAnswered`, `TestDialectDocumentValidationBeforeOverload` |
| Overlong line closes; exactly-max accepted; nothing after is read | `TestOverlongLineClosesConnection` |
| Every line the peer writes in its own tests is a frame of the schema | `TestPeerOutputMatchesSchema` (root module `protocol`, running the peer tests with the writers recorded) |
| The rules of what the peer sends | `TestNilParamsAreSentAsAnEmptyObject`, `TestParamsThatAreNotAnObjectAreRefused`, `TestMethodNameLength`, `TestHandlerResultMustBeAnObject`, `TestTypedNilErrorIsAnInternalError`, `TestErrorRepliesFitTheSchema`, `TestWithDataKeepsOnlyObjects`, `TestErrorReplyTooLargeIsAnInternalError` |
| Invalid JSON, invalid UTF-8 | `TestInvalidJSONKeepsConnection`, `TestInvalidUTF8IsParseError` |
| Missing or wrong `jsonrpc`, bad shapes | `TestMissingOrWrongVersionIsInvalidRequest` |
| The document-check dialect rejects deep repeats and invalid Unicode before dispatch or overload, recovers only a unique valid id, and keeps unknown values open | `TestDialectDocumentClassification`, `TestDialectDocumentRejectionHasNoSideEffects`, `TestDialectDocumentValidationBeforeOverload`, `FuzzDialectDocumentClassification` |
| A repeated frame member is refused; a repeat inside params is not the frame's business | `TestRepeatedFrameMemberIsInvalidRequest`, `TestNoDuplicateTopLevelKeys`, `TestClassify` |
| A malformed response-like line is never answered with its id, even when both sides have that id in flight | `TestMalformedResponseIsNeverAnsweredWithItsID`, `TestClassifyDetails` |
| Batch rejected, connection kept | `TestBatchIsRejectedAndConnectionContinues` |
| Overload before params and handler | `TestOverloadedIsDecidedBeforeParamsAndHandler` |
| Cancellation, one reply | `TestCancellation`, `TestCallContextCancellation` |
| A cancelled request is not answered under `SilentOnCancel` and its id is released, whether the handler honours the cancel, ignores it or never started | `TestDialectCancelledRequestIsNotAnswered` |
| `CancelTarget` is given the params as received; `false` or an invalid id cancels nothing | `TestDialectCancelTargetReadsTheParamsItIsGiven`, `TestDialectCancelTargetFalseCancelsNothing`, `TestDialectCancelTargetWithAnInvalidIDCancelsNothing` |
| Renaming the cancel notification alone keeps the answer to a cancelled request | `TestDialectWithoutSilentOnCancelStillAnswersCancelledRequests` |
| A cancellation after the answer was queued still gets that one answer | `TestDialectCancelAfterTheAnswerWasQueuedStillGetsThatOneAnswer` |
| The replacement codes are what is sent, each on its own | `TestDialectReplacementCodesAreSent` |
| `Call` sends the dialect's cancel notification and params; the dialect's cancel method is reserved and must be a valid method name | `TestCallSendsTheDialectsCancelNotification`, `TestDialectCancelNotificationIsReservedAndMustBeAValidMethodName`, `TestDialectCancelParamsMustBuildAnObject` |
| Write timeout; slow reader tolerated; also when the last line is the reply to an overlong line | `TestWriteTimeoutClosesTransportOnStalledReader`, `TestSlowButProgressingReaderDoesNotTimeOut`, `TestOverlongLineWithStalledReaderStillCloses` |
| Owed error replies wait for room and are never dropped (flood backpressure) | `TestReadLoopWaitsWhenErrorRepliesCannotBeQueued` |
| Notification order and overflow | `TestNotificationsAreHandledInArrivalOrder`, `TestNotificationOverflowClosesConnection` |
| Outbound full | `TestOutboundQueueFullReturnsErrOverloadedAndWritesNothing`, `TestRefusedCallLeavesNoPendingEntry` |
| Duplicate and invalid ids, the id rule | `TestDuplicateInFlightIDIsInvalidRequest`, `TestInvalidRequestIDs`, `TestRequestIDJSON`, `TestRequestIDValidate`, `TestClassify` |
| A cancel without a usable id, or with an unknown or repeated member, cancels nothing | `TestCancelWithoutAUsableRequestIDCancelsNothing` |
| Late response, malformed response | `TestLateResponseIsDroppedAndLogged`, `TestMalformedResponseFailsTheCall` |
| Unknown method and notification | `TestUnknownMethodIsMethodNotFound`, `TestUnknownNotificationIsLoggedAndDropped` |
| Close, pending calls, goroutines | `TestCloseEndsPendingCallsWithErrClosed`, `TestCloseWithoutStartAndCloseFromHandler`, `TestCloseWaitsForReaderAndWriter`, `TestNoGoroutineLeakAfterManyPeers`, `TestStartAndCloseAreSafeConcurrently`, `TestStartAfterCloseDoesNothing` |
| Effect rule | `TestEffectClassification`, `TestHandlerErrorsAndEffectRule` |
| No bodies in logs | `TestLogsNeverContainMessageBodies` |
| Many concurrent calls both ways against an independent wire model | `TestConcurrentCallsInBothDirections` |
| Random sequences of requests, duplicates, cancellations, notifications, malformed lines, stray responses and a close, checked against a model that keeps its own books: every well-formed request answered exactly once, no handler after an overload reply, a duplicate of an id in flight rejected, nothing but a cancel or the close of the peer answered `-32800` | `TestRandomOperationSequences`, `FuzzOperationSequence` |
| The id is free again before its reply is queued, even when the reply must wait for room | `TestRequestIDIsReleasedBeforeItsReplyIsQueued` (white box) |
| Frame reader and classifier against separate oracles | `TestFrameReader`, `TestClassify`, `FuzzFrameReader`, `FuzzClassify` |

## Messages and methods

### What

Method names are `noun.verb`; `$/` marks a protocol-level method with no business
meaning. Field names are snake_case. Timestamps an adapter reports are Unix
milliseconds (`*_ms`) and serve display and diagnosis only: the daemon judges time
by the clock it reads when a message arrives.

The result of a request carries every fact the daemon acts on. An error response
only says the request was not understood or not allowed.

| Method | Direction | Kind | Params | Result |
| ------ | --------- | ---- | ------ | ------ |
| `initialize` | D to A | request | `protocol` range, `daemon.version`, `instance` (`id`, `lock_path`, `state_dir`), `capabilities` (the daemon's own, empty for now) | `protocol_version`, `adapter` (`id`, `version`), `capabilities` |
| `ping` | both | request | none | none |
| `$/cancel_request` | both | notification | `request_id`, an id as the peer layer defines it | |
| `shutdown` | D to A | request | none | none |
| `adapter.start` | D to A | request | `node`, `runtime_endpoint?`, `config`, `presets`, `sessions` (an entry of an attached session has `attached`, `wake?` and no `preset` or `tool_access`), `direct_endpoints` | `sessions` (`endpoint`, `state`, `turn_active`, `process?`) |
| `adapter.probe` | D to A | request | `tool_probes` (`preset`, `frontend`, `token`, `env`) | `runtime?` (`name`, `version`), `auth`, `dependencies`, `presets`, `tool_probes` |
| `adapter.availability` | A to D | notification | `conditions`: a full snapshot, empty when all is well | |
| `session.create` | D to A | request | `attempt`, `runtime_endpoint`, `session_endpoint`, `preset`, `first_prompt`, `tool_access`, `checkpoint?` | `outcome` created (with `first_turn_ended?`), failed or indeterminate, with the fields each needs |
| `session.attach` | D to A | request | `endpoint`, `native_ref`, `wake?` | `outcome` attached, not_found or failed, `reason` with a failure, `detail?` |
| `session.detached` | D to A | notification | `endpoint` | |
| `checkpoint.put` | A to D | request | `scope` (attempt or session), `id`, `state` (object, at most 16 KiB compact) | none; answered once the state is durable |
| `prompt.deliver` | D to A | request | `endpoint`, `attempt`, `text` | `stage` accepted, persisted or refused, `reason` with a refusal |
| `prompt.persisted` | A to D | notification | `endpoint`, `attempt` | |
| `turn.started` | A to D | notification | `endpoint`, `turn`, `attempt?`, `process?`, `at_ms` | |
| `turn.ended` | A to D | notification | `endpoint`, `turn`, `final`, `at_ms` | |
| `session.interrupt` | D to A | request | `endpoint` | `stage` requested, stopped, not_running or refused |
| `session.close` | D to A | request | `endpoint`, `native_ref?` | `outcome` closed or close_unconfirmed |
| `prompt.reconcile` | D to A | request | `endpoint`, `attempt`, `checkpoint?` | `verdict`, `turn?`, `grounds` |
| `recv.observed` | A to D | request | `endpoint`, `lease`, `frontend`, `observed` | `verdict`, `reason`, `items` |
| `native_request.handled` | A to D | notification | `endpoint`, `method`, `disposition`, `at_ms` | |
| `endpoint.register` | A to D | request | `key`, `display_name`, `description?` | `endpoint` |
| `endpoint.unregister` | A to D | request | `endpoint` | none |
| `endpoint.pending` | D to A | notification | `endpoint`, `count` | |
| `endpoint.recv` | A to D | request | `endpoint`, `wait_s?`, `message_id?` with `part?` | `page` |
| `endpoint.ack` | A to D | request | `endpoint`, `lease`, `items` | `items` with the outcome of each |
| `endpoint.send` | A to D | request | `endpoint`, `to` (one address when `control` is given), `content_type?` (present exactly when `body` is not empty), `body`, optional reply, thread, disclosure, deadline, `control`, idempotency key | `message_id` |
| `endpoint.message_status` | A to D | request | `endpoint`, `message_id` | `recipients`, `control_reply?` |

**The close of a session.** `session.close` releases the native session, and the
daemon may ask again at any time: after the endpoint was closed in the channel, after the
adapter restarted, and again when an earlier answer was lost. It is idempotent, and a native
session that no longer exists (already released, or gone) is answered `closed`, because for the
node "released" and "already gone" are the same fact; `close_unconfirmed` is for a session that
may still exist. The daemon sends `native_ref`, the native session the endpoint maps to,
whenever it knows it: an adapter that restarted no longer holds a closed endpoint (`adapter.start`
attaches only the open ones), so the reference is what lets it find the native session and release
it. An endpoint the adapter holds takes precedence: the reference is used only when the adapter
does not hold the endpoint. A reference that names a native session the adapter holds under
another, open endpoint is left untouched and the answer is `closed`, because nothing is left to
release for the endpoint that was closed. When the adapter cannot tell whether the native session
exists (the runtime is unreachable, say) it answers `close_unconfirmed` with `native_unreachable`,
never `closed`: `closed` is a fact the node records and never asks about again. An endpoint the
adapter does not hold, asked without a reference, is `unknown_endpoint`.
`CloseParams` and `TestInterruptAndCloseRules` (the rows on the reference), the golden
messages `session.close/request-with-native-ref.json` and `invalid/session-close--native-ref-*.json`,
and the fake adapter's `TestCloseByNativeReferenceAfterARestart` and
`TestCloseTakesTheHeldEndpointBeforeTheNativeReference` hold both sides to it.

**The body of `endpoint.send`.** A message has a body, and the body is not empty:
the channel carries messages for agents, and an empty message carries nothing.
The envelope, the signed wire format, already refuses a message without a body,
so the protocol must not accept what every node will refuse: the schema gives
`body` `minLength: 1` unless `control` is present, and `SendParams.Validate`
refuses an empty body in the same case. The member itself stays required, so the
wire shape is unchanged. A message that carries a control signal is judged by
the signal: the body of a `session.create` is its initial message and an empty
body means none; every other signal carries no body, so its body is empty (the
envelope refuses a body there: it would have no reader, and a field nobody reads
is a place to hide data). The content type follows the body as in the envelope
(both present or both absent): `content_type` is required when the body is not
empty and is omitted when it is, so a `session.create` without an initial
message and the other control signals carry none. The schema states this with a `then`/`else` on the
signal, and `SendParams.Validate` the same. `TestEndpointSendRules` (the rows on
the body) and the golden messages `invalid/endpoint-send--body-empty.json` and
`invalid/endpoint-send--control-*-with-body.json`, `invalid/endpoint-send--content-type-missing.json` and `invalid/endpoint-send--control-*-with-content-type.json` hold both sides to the rule.

**Direct reply routing.** A nonempty `reply_to` must name the unique message
this endpoint actually took in, excluding rejected intake records. `to` must
contain exactly that message's original sender. Unknown reply sources, other
recipients and multi-recipient replies are refused with no effect. A successful
reply retains its thread, deadline and idempotency key; disclosure with one
recipient is normalized away as in every envelope. A control signal cannot carry
`reply_to`, and otherwise preserves its applicable fields, including thread.
These checks belong to the daemon's engine rather than the SDK string schema.
`endpoint.message_status` returns the same `-32602` for an unknown message and
one the endpoint did not send, so it cannot disclose another endpoint's traffic;
`endpoint.recv` with a `message_id` the endpoint holds nothing of answers the same.
The production error mapping and its no-effect tests are described in the
[adapter-host reference](../adapter-host/README.md).

The `display_name` of `endpoint.register` is 1 to `MaxDisplayName` (128)
characters and a `description`, when given, 1 to `MaxDescription` (1024); a
character is a Unicode code point, not a byte. The two constants are exported so
that whoever stores or shows a name applies the bound the protocol states instead
of copying the number.

A display name also holds no character that a terminal acts on or that reorders
text: no control character (U+0000 to U+001F, U+007F to U+009F), no line or
paragraph separator (U+2028, U+2029) and no bidirectional formatting control
(U+061C, U+200E, U+200F, U+202A to U+202E, U+2066 to U+2069). A name that has one
is refused with `-32602`; it is never cleaned, because the node cannot know what
the adapter meant. The rule is the `DisplayName` definition of the schema (a
length and a pattern) and `adapterproto.ValidateDisplayName` in Go, which is the
one rule for every place a display name is read, stored, forwarded or shown;
`ValidateOptionalDisplayName` is the same rule for a name that may be absent.

The `description` is held to its length only, and not to this rule, on purpose: it may
legitimately span several lines, and it is never printed as a line of a terminal. The human
output of the command line does not show it. JSON output escapes its control characters
(Go's encoder) but passes bidirectional formatting characters such as U+202E through
unchanged, which is acceptable because JSON output is read by programs, not shown as a
terminal line.

`MaxAckItems` exports the existing acknowledgement limit of 1024 entries. The
adapter request and result validators, the observed-receive result, and other
interfaces accepting these acknowledgement values use the same bound; exposing
it does not change the accepted protocol values or their format.

Identifiers: `EndpointRef` and `Address` are opaque strings of 1 to 128
characters, handed out by the daemon and returned unchanged; the SDK never parses
them. `AttemptID` is a lowercase UUID version 4. `LeaseRef` is 32 lowercase hex
characters, not all zero. `Token` is 32 random bytes in unpadded base64url.
`NativeRef` and `TurnRef` are opaque strings of up to 256 characters that only the
adapter interprets. The id of a request, and so the `request_id` of
`$/cancel_request`, is the peer layer's `RequestID`; `adapterproto` takes the
type and the params of the cancel notification from `peer` and does not restate
the rule.

**Version negotiation.** A version is a positive integer and each side announces a
closed range. The adapter picks the highest version in both. With no overlap it
answers `-32022` with `data` `{requested, supported}`, each a range. Inside a
negotiated version nothing is added that an older reader would misread, except
by the in-place extension described below. A protocol addition is made in one of
three ways, and only these:

1. a new **capability**, declared before it is used: the adapter in the
   `capabilities` of the `initialize` result, the daemon in the `capabilities` of
   the `initialize` params. A peer uses an addition only when the other side has
   declared it;
2. a reader **ignores a capability key it does not know**: the capability
   objects (`capabilities` and its `runtime` and `direct` parts, and the daemon's
   `capabilities`) are open, so a newer adapter that declares a new capability
   still completes the handshake with an older daemon, which then simply does not
   use it;
3. everything else that would change a message, a new member of any other
   object included, needs a **new protocol version**.

**Extending a version in place.** While no implementation exists outside this
repository, protocol version 1 may still be extended in place, because no
reader of an earlier shape is deployed that the change could break. Five
additions were made this way, and each is a change that rule 3 would otherwise
have sent to a new version:

- the rejection reason `backlog_full`. An older SDK adapter decodes strictly and
  does not know the name, so it refuses the whole result that carries it;
- the manifest member `recv_wait_limits`, a new member of the manifest object.
  An older daemon decodes the manifest strictly and refuses a manifest that has
  the member;
- the application code `node_busy` (`1012`). An older SDK's peer reads it as an
  application code it has no name for, which the effect rule already treats as
  no effect; only a reader that checks the error object against its own code
  table (`ErrorObject.Validate`) refuses it;
- the control creation failure reason `node_endpoint_limit` (10). A reader that
  predates it refuses a control reply that carries the reason, in JSON or a signed
  record, because the reason set stays closed;
- attached sessions, behind the runtime capability `attach`: the request `session.attach`, the
  notification `session.detached`, and the members `attached` and `wake` of an `adapter.start`
  session entry, which make `preset` and `tool_access` absent for such an entry. The capability
  alone would need no new version, but the new members of the session entry and the new
  methods would. The daemon sends none of them to an adapter that did not declare `attach`, and
  an older daemon never sees a declaration it would act on, so no deployed reader meets them; the
  record is kept because rule 3 would otherwise have sent them to a new version.

Three removals were made the same way, because nothing outside this repository reads
what they took away:

- the manifest member `tested`, the closed ranges of runtime versions an adapter was
  tested against, with the alert the daemon raised for a version outside them. Runtimes
  release several versions a day, so no list stays true. An older daemon that decodes
  strictly refuses a manifest that still has the member;
- the status condition `version_untested` (6), which showed that verdict. Its number
  stays reserved, see the control signals below;
- the grounds code `runtime_version_untested` of `prompt.reconcile`, which let an
  adapter say it would not conclude absence on a version it had not tested. A value
  that is no longer a known code is refused by validation like any other unknown one;
  an adapter that reads a record whose form it does not know answers `record_unreadable`.

One rule was **tightened** in place, and this is a change that can refuse what was
valid: a `display_name` with a control or bidirectional formatting character, which
`endpoint.register` accepted before, is refused with `-32602`. It is done in place
because no implementation outside this repository exists, and the adapters in it
register plain names; no name that was ever registered by them is affected. The
`display_name` members of a node's answer to a directory query are tightened the same
way: the node that asked refuses the whole answer that carries a name breaking the rule.

This permission ends when the first implementation outside this repository
exists. From then on an addition of this kind needs a new protocol version, or,
for the manifest, a new `manifest_version`.

**Strict decoding.** Within a version an unknown field is `-32602`, except in the
capability objects, which ignore unknown keys (see version negotiation). The same
rule makes a field name that differs in case, `null`, a missing required field, a
repeated key and an explicit empty value for an optional field errors: they
would otherwise let two implementations read one message two ways. The known
members of a capability object are decoded as strictly as any other, and a key
repeated anywhere in it is still an error, ignored members included.
String escapes must encode Unicode scalar values: a UTF-16 surrogate belongs to
a valid high/low pair, in object keys and values alike, including opaque values
and ignored capability members. An unpaired surrogate is refused rather than
accepting `encoding/json`'s U+FFFD replacement, which would silently give
different messages or idempotency keys the same content. A real U+FFFD and its
escape stay valid.

**Instance lock.** The adapter takes an exclusive lock on `instance.lock_path`
before it answers `initialize`, and answers `1011` when it cannot. A hung old
process and its replacement therefore never control sessions at once; the lock
dies with the process. Before `initialize` only `initialize`, `ping` and
`$/cancel_request` are accepted; anything else is `-32003`. After `shutdown` began only
`ping` is accepted and anything else is `-32002`; the state is judged before the
parameters are read.

**The adapter kit.** `sdk/go/adapterkit` serves these rules for an adapter written in Go:
version negotiation, the lock before the answer (`1011` when it is held), the two gates
above, the capability gate of the methods that need one (a runtime, and `attach` for
`session.attach`, `session.detached` and an `adapter.start` that lists an attached session;
a request without it is answered `1003`, a notification is dropped), strict decoding of
every request, validation of every result before it is sent, and typed calls and
notifications to the daemon. The adapter implements the methods that remain as an
`adapterkit.Application`. An adapter in another language implements the same rules from
this reference.

**Prompt delivery.** The daemon records the attempt as dispatching before it
sends `prompt.deliver`. The adapter delivers `text` unchanged (it may append one
line with the attempt identifier as a fallback for reconciliation). `accepted`
means the runtime took the prompt but has not made it durable; the adapter must
then send `prompt.persisted`. Acceptance leaves the attempt in flight and does not
mark its messages notified: it proves no durable delivery. If neither
`prompt.persisted` nor `turn.started` with that attempt follows within one minute
of the accepted answer, the daemon sends `prompt.reconcile`. `refused` means certainly
nothing was delivered. `prompt.persisted` and `turn.started.attempt` are equally
positive proof that the attempt reached the session. Late proof may correct an
unknown outcome on a best-effort basis.

**Session creation.** `session.create` carries a `first_prompt`: an `attempt`
identifier and the text of the first prompt. Its record is `handoff`'s (see the
handoff reference): with an initial message in the creating control signal it is
that message's first prompt attempt, counted as one; without one the text is
`NewSessionPromptText`, which announces no message and is not a prompt attempt
of the endpoint, so it does not count toward the hourly cap or hold back the
first real message. In both cases its attempt identifier is the one the adapter
finds in the native record to show the session is ready, and the one the daemon
reconciles by after an unknown outcome. For a creation with an initial message,
`created` supplies the delivered proof: the daemon settles one delivered first
attempt and the initial delivery's notification before opening the endpoint.
The attempt's time is when this node confirmed creation (the successful control
record's `DeliveredAt()`), retained through effect replay. Failed and unconfirmed
creations count no delivered first prompt, and a closed endpoint is not reopened.
Creating endpoints cannot successfully recv even though their tools already
hold the token; native first-turn events may still precede the result.

The result is `created`, `failed` or `indeterminate`. `failed` means that no
session will be attached to the channel for this attempt, and `reason` says why:
`preset_unknown`, `preset_invalid`, `runtime_auth`, `native_unavailable`,
`dependency_missing`, `native_error`, or `control_unknown`. With every reason but
the last, certainly no native session was created. `control_unknown` is for an
adapter that cannot tell who controls the native session it was creating (two
processes hold its identifier, a writer other than the adapter does, or a start
timed out while its process still runs): a native process may remain, the adapter
leaves it alone and the daemon notifies the local owner when it receives the
reason.

**Reconciliation.** `prompt.reconcile` answers whether an attempt reached the
runtime. The `grounds.code` belongs to the verdict: `found` goes with
`attempt_in_record` and carries the turn; `provably_absent` with
`record_complete_without_attempt` (the writer has stopped and the whole record was
scanned) or `never_dispatched` (the checkpoint shows the runtime was never
called); `indeterminate` with `turn_running`, `record_unreadable` or
`native_unreachable`.

**Turns.** Each turn has exactly one `turn.started` and one `turn.ended`,
correlated by `turn`. Turns the runtime starts itself (an automatic continuation,
work triggered by background activity) are reported the same way. A turn caused
by a prompt names the attempt.

**The order at start.** The result of `adapter.start` states which sessions have a turn
running as of the moment the adapter answered, and the daemon replaces its busy state with
it. An adapter may therefore emit `turn.started` or `turn.ended` while it is still
answering (Codex takes its sessions back one at a time inside `adapter.start`, and Claude
starts monitors before it returns), and the result then already counts or no longer counts
that turn. The daemon keeps the two in order by delivering the notifications the adapter
sent while `adapter.start` was outstanding only after it has applied the result, in the
order they arrived; an adapter needs nothing special for it, and need not wait to send them.

**The order of a creation's events.** A session works on its first prompt as soon as
it exists, so the first `turn.started` of a session (with the attempt of the first
prompt) may reach the daemon before the result of the `session.create` that made it,
and so may a `turn.ended`. The protocol does not forbid it and a real adapter cannot
help it: it judges the session ready by finding the first prompt in the native record,
and the turn that prompt starts begins first. The daemon therefore accepts the
notifications of a session whose creation has not returned: the host forwards them in
the order they were sent, and the engine keeps the busy state by the endpoint reference
and writes nothing for an endpoint it does not know as open. The endpoint is open when
the result is in. For the same reason the state of a session that the adapter puts with
`checkpoint.put` (scope `session`) is accepted while the endpoint is being created as well
as when it is open: the adapter keeps it before it answers `created`. A checkpoint for an
endpoint that is closed, that belongs to another instance or that does not exist is refused
with `1001`; so is a checkpoint (scope `attempt`) for an attempt that is not one of a session of
the instance: its creation attempt, the first prompt that follows from it, or the prompt in flight
of an open session. An attempt that has settled is refused too, because a put for it would leave a
file that nobody deletes.

The end of the first turn can also fall in a window the daemon did not see: it restarts while a
creation is unsettled, asks again, and the adapter answers `created` from its stored state after
the turn is over, so no `turn.ended` will reach the connection. `first_turn_ended` in a `created`
result says exactly that, the turn the first prompt started was already over when the adapter
answered; the daemon treats it as the end of that turn. It is a fact that stays true, so it is
safe whenever the answer arrives, unlike a busy flag that a notification could outrun. It is
sent as `true` or left out, never as `false`, and only with `created`; without it the daemon
infers nothing and the turn's own `turn.ended` is expected on the connection.

**The state directory.** `instance.state_dir` in `initialize` is an absolute path of the
operating system's file system: a directory of the instance, `adapter-state/<instance>` in the
daemon's data directory, that the daemon has made, reachable only by its owner, before it
started the adapter. The adapter keeps there what must outlive its process and belongs to the
instance: a socket, a log, a state file. The path is the same after every restart of the adapter
and of the daemon, so it is also where a lock or a socket that a restarted adapter must find
again lives. The daemon never reads or writes inside it, removes nothing from it and does not
count its size; what the adapter leaves there is the adapter's to clean. The directory exists
for the life of the instance's configuration, not of one process: it is not removed when the
adapter stops, nor when the owner removes the instance from the configuration, because a
process the adapter started (a detached server, a turn) may outlive the adapter and still use it. It is a path the adapter is given, not a place it chooses, because the layout of
the data directory is the daemon's.

**Checkpoints and state across versions.** The daemon installs another version of an
adapter while its instances are enabled: it restarts them on the new program, keeps their
session endpoints open and hands the new process what the old one stored, the checkpoints of
`checkpoint.put` and the state directory. A session therefore survives the upgrade only if the
new version reads the checkpoint formats and the state files of every earlier version it can be
upgraded from; an adapter that changes one of them keeps reading the old form, as the formats of
the native processes below are kept. It may refuse a format newer than any it knows, which is an
adapter that was replaced by an older version, and it refuses it rather than guessing at it. The
daemon does not check this: it cannot read what it stores for the adapter.

**Attached sessions.** A session endpoint comes from one of two origins: it is created by
`session.create`, or it is attached, at the request of a session that a person opened and
that asked to be attached, through the local owner's management interface. The runtime
declaration says whether the adapter implements them: `attach`. For an attached session the
daemon sends `session.attach` with the endpoint, the native session and, when it has one, a
`wake`, and lists the session in every later `adapter.start` with `attached` and, when known,
`wake`. A `wake` is what the adapter needs to deliver a prompt to a session someone else runs: a
map of at most 16 names (environment-variable names) to values of at most 4096 characters, and
never an empty object. Its names and values are the adapter's own and are defined in the
adapter's reference; the daemon passes them through unchanged, holds them in memory only, and
never writes them down or logs them, and an adapter must not log the values either. After a
restart of the daemon an entry may therefore come without a `wake`; the owner side supplies it
again by repeating the attachment, which the daemon forwards as another `session.attach`. A
repeated `session.attach` for the endpoint replaces the wake the adapter holds, and none sent
means none held.

`session.attach` answers `attached` (the adapter holds the session and can deliver prompts to
it), `not_found` (the runtime has no such session) or `failed` with a reason:
`wake_unavailable` (the session exists but prompts cannot be delivered to it: the runtime is
too old, or the wake is missing or refused), `native_unreachable` (the adapter cannot tell now)
or `native_error`. A `detail` may come with any outcome. The kit answers `1003` to a call
without `attach` declared, and the daemon never makes one.

An attached session differs from a created one in what the daemon asks of the adapter:

- `prompt.deliver` and `prompt.reconcile` have the same parameters and results. The adapter
  delivers through the runtime's own entry for a session someone else runs. While the native
  session is not there it refuses with `native_unavailable` and reports that availability
  condition for the endpoint, so the rules for a blocked delivery apply; the endpoint stays
  open.
- A turn the person starts is reported with `turn.started` and `turn.ended` without an
  `attempt`; a turn a prompt started names the attempt.
- `session.close` and `session.interrupt` are never sent: the session is the person's, and the
  daemon refuses those control signals itself. An adapter that receives either for an attached
  endpoint answers `1002`.
- Closing the endpoint owes no release of a native session. The daemon sends the notification
  `session.detached` instead: the adapter stops watching the session and drops the wake, and the
  person's session goes on. It is best effort like `endpoint.pending`, not recorded and not
  repeated; an adapter that misses it stops at the next `adapter.start`, which no longer lists
  the endpoint.
- No tool access is sent: the session's own tools got their token over the owner's management
  connection.
- `recv.observed` for an attached session is refused with `1002`.

The daemon sends `session.attach`, `session.detached` and the attached entries of `adapter.start`
only to an adapter that declared `attach`.

**Tool access.** The `env` of `tool_access`, in `session.create` and in the entries of
`adapter.start` for sessions the adapter created, holds what the session's tools need to
reach the daemon: the token of the session endpoint in `SIDERA_TOKEN` and the data
directory in `SIDERA_HOME`. The adapter puts both into the environment of the agent. The
token stays opaque: the adapter never parses it or acts on its content. The data directory
is a path the adapter may use to keep the agent's runtime away from it, as the Claude and
Codex adapters do (they deny it to the runtime's file tools and sandbox).

**Acknowledging on the agent's behalf.** `recv.observed` carries the text of a
`recv` tool result as the model received it, read from the runtime's durable
record, with the lease the result names. The daemon decides whether that proves
the model saw the page and, if so, settles every part of it; the adapter may only
do this for session endpoints it created. A refusal (`replaced`, `truncated`,
`content_mismatch`, `unknown_lease`) leaves the page for an explicit
acknowledgement.

**Server requests.** An adapter answers every request the native runtime sends it
locally, from the preset, or refuses it within one second; it never asks the
daemon, because approvals do not go through the channel. Unknown methods are
refused. Each one is reported with `native_request.handled`; `disposition` is
`preset`, `rejected_by_preset`, `rejected_unknown` or `late`.

**Direct endpoints.** An adapter that reads messages itself (a chat bridge)
registers endpoints with `endpoint.register`, identified by a key it chooses; the
same key always gets the same endpoint. It reads with `endpoint.recv`, which
returns the `RecvPage` that `handoff` prints, and settles with `endpoint.ack`.
The daemon serves the `endpoint.*` requests of an adapter only once it is running, and
it is not running while it waits for the answer to `adapter.start` (a restart of the
daemon waits for every adapter to answer it before it runs). An adapter therefore
never waits for an `endpoint.*` request, `endpoint.register` included, while it answers
`adapter.start`: it answers first and registers afterwards, from work that does not
hold the answer. A request made in the meantime is held until the daemon runs, not
refused, so an adapter that waited for it would stall the start of the daemon until
the host's handshake timeout ends the connection and starts it again. The endpoints an
adapter registered before are listed in `direct_endpoints` of `adapter.start`, so it
knows them without a request.
A message on a page, or a part of one, has a body that is not empty, as the
envelope requires of every message: no node can hand out an empty one, so the
schema (`minLength: 1`) and `PageMessage.Validate` refuse a page that holds it
(`TestRecvPageRules`, `invalid/endpoint-recv--result-page-message-with-empty-body.json`).

The daemon tells the adapter of a direct endpoint, with the notification
`endpoint.pending` (`endpoint`, `count`), that messages wait for the endpoint. `count`
is the number of messages that wait, at least 1. The notification is
level-triggered and may be coalesced: it says there is something to read, not how many
notifications were sent, and one carries the count of all that became ready together.
It is best effort: the daemon records nothing, does not repeat it and does not wait for
the adapter, so an adapter must not depend on receiving it and reads with
`endpoint.recv` (which may wait) on its own schedule. The daemon sends it when a message
becomes ready to be handed out to the endpoint, and not for a message that a waiting
`endpoint.recv` of the endpoint took.

**Errors.** Codes `-32700` to `-32600` and `-32603` are JSON-RPC's; `-32001`
to `-32004`, `-32022` and `-32800` are the implementation-defined range; both
groups are defined by the peer layer (`peer.Code*`) and listed here to be read
with the rest. `1001` to `1012` are the protocol's own (`adapterproto.Code*`):

| Code | Name | Meaning |
| ---- | ---- | ------- |
| -32700 | parse_error | invalid JSON or UTF-8; the connection continues |
| -32600 | invalid_request | not an object, a batch, no `jsonrpc`, a repeated member, bad or reused id |
| -32601 | method_not_found | unknown method, or not valid in this direction |
| -32602 | invalid_params | breaks the schema or has an unknown field |
| -32603 | internal_error | the handler failed; the outcome is unknown |
| -32001 | overloaded | refused before queueing; the handler did not run |
| -32002 | shutting_down | shutdown has begun |
| -32003 | not_initialized | before `initialize` |
| -32004 | line_too_long | sent just before the connection closes |
| -32022 | unsupported_protocol_version | `data`: `{requested, supported}` |
| -32800 | request_cancelled | cancelled; the outcome is unknown unless the method says otherwise |
| 1001 | unknown_endpoint | the endpoint does not exist or is not this connection's |
| 1002 | wrong_endpoint_kind | the endpoint's kind cannot be used with this method |
| 1003 | capability_not_declared | the method needs a capability that was not declared |
| 1004 | unknown_attempt | the attempt identifier is unknown |
| 1005 | attempt_conflict | the same attempt identifier with different parameters |
| 1006 | checkpoint_too_large | the checkpoint exceeds 16 KiB |
| 1007 | endpoint_limit | the direct-endpoint limit is reached |
| 1008 | invalid_address | an address cannot be parsed; `data`: `{field}` |
| 1009 | send_refused | the local intake refused the message; `data`: `{reason}`, a snake_case word |
| 1010 | lease_unknown | the lease does not belong to the endpoint |
| 1011 | instance_locked | another process holds the instance lock |
| 1012 | node_busy | the node refused the request before any effect because it could not take it now (back pressure, a failing volume, a node still starting), and sending it again later is safe; `data`: `{reason}`, a snake_case word; a reader treats a reason it does not know as busy with no further hint, and a name once sent is never renamed, removed or reused (this version sends `node_queue_full`, `storage_full`, `storage_unavailable`, `clock_rollback`, `loop_busy`, `recovering`) |

An error may carry `data`, always an object. The four codes marked above define
its shape and require it; any other code may carry an object of the sender's
choosing, which a reader may show or log but must not act on.

Whether a failed request had an effect is the peer layer's effect rule, applied
to the error a call returned (`peer.NoEffect`, `peer.ResultUnknown`; the daemon
does not sort codes itself). Only for the methods with side effects
(`session.create`, `prompt.deliver`, `session.interrupt`, `session.close`) does
an unknown outcome call for reconciliation or an idempotent retry.

**Rejection reasons.** A recipient of `endpoint.message_status` in state
`rejected` carries one `reason`, by name (`RejectReason`), and no other state
carries one. The names are those the daemon's delivery package gives its
rejection reasons, so nothing is translated on the way to the sender. A body
that was handed out is never rejected afterwards; it becomes `delivery_unknown`.
The rules are those of [`../delivery/README.md`](../delivery/README.md), which
is authoritative.

| Reason | Meaning |
| ------ | ------- |
| `unauthorized` | the authorization check at intake denied the message |
| `endpoint_missing` | the recipient endpoint does not exist |
| `endpoint_closed` | the recipient endpoint was closed or deleted, possibly while the message was queued |
| `endpoint_declined` | the endpoint declared that it does not accept this message, for example one with several recipients |
| `too_large` | the body exceeds the size limit |
| `expired` | the delivery deadline had already passed when the message first arrived |
| `backlog_expired` | the message stayed queued, never handed out, past the endpoint's longest queue time or the settle deadline |
| `second_check_failed` | the authorization check made when the body was first to be handed out denied it |
| `malformed` | the envelope is not valid |
| `deadline_out_of_range` | the delivery deadline lies further in the future than the protocol allows |
| `backlog_full` | the recipient endpoint already held as many queued messages as its backlog limit allows when the message arrived; the rejection is final at intake and nothing was stored |

### Why

- **Results carry the semantics, errors only misuse.** A refusal the daemon must
  act on (a prompt that was certainly not delivered) is a result with a reason, so
  it can never be mistaken for a transport failure whose outcome is unknown.
- **The version is an integer range.** One round trip lets both sides give up old
  versions explicitly, and the error data shows both ranges. A minor version has
  no meaning on a wire where additions go through capabilities.
- **Data is allowed on any error code.** JSON-RPC libraries attach details to
  `-32602` and `-32603` routinely; a reader that refused them would turn a useful
  error into a protocol violation, and the sender of such an error could not
  tell which of its replies a peer would reject. Only the four codes whose
  receiver acts on the data fix its shape; the object for any other code is
  for people.
- **Strictness fails closed, and capabilities are the one open place.** A
  lenient reader that ignores an unknown field silently does the wrong thing when
  the field was a safety restriction. A capability is different in kind: the
  peer that declares it also uses it, and a reader that does not know it never
  declares it back, so ignoring it only means the feature is not used. LSP, MCP
  and ACP treat capabilities the same way. Without this a newer adapter could not
  complete the handshake with an older daemon, and every addition would need a
  new version. The daemon declares its own capabilities for the symmetric
  reason: an adapter must be able to learn which daemon-side additions it may
  use, and the object has to exist from the first version for the first addition
  not to need one.
- **Identifiers stay opaque.** The adapter never parses them, so copying their
  syntax into the SDK would only make a second place to keep in step with the
  daemon, for the benefit of an earlier error message.
- **A rejection reason is a name, and the set is closed within a version.** A
  sender that stored or compared a reason must still read it later, so a name
  once sent is never renamed, removed or reused. Within a released protocol
  version the set of reasons is closed: strict decoding refuses an unknown
  reason, so a reason added after a version is released follows the version rule
  above and needs a new protocol version (`backlog_full` was added in place, under
  the exception for a version nobody outside this repository implements yet). `backlog_full` is its own reason
  because the sender can act on it, by retrying later or sending elsewhere,
  where `backlog_expired` means a message that was accepted and then waited too
  long, and `endpoint_declined` means the endpoint refused this sender or
  message, where `backlog_full` means the endpoint's own queue was full.
- **Back pressure has a code of its own, `node_busy`.** The node says "not now,
  nothing was recorded" for a full queue, a full or failing volume, a clock set
  back, a loop that did not take the command in time and a node that is still
  starting. No existing code says that truthfully. `-32001` is the peer's
  promise that the request was refused before it was queued and the handler did
  not run, and the peer's retry argument rests on it; it describes the peer's
  own queue, and widening it to the node's state would make that promise depend
  on a daemon-side judgement. `1007` is the direct-endpoint limit: an adapter
  reads it as a limit it can stop hitting by closing endpoints, which is false
  for `endpoint.send` and `checkpoint.put`. `-32603` leaves the outcome
  unknown, which is the reconcile path for a request that certainly did
  nothing. `node_busy` is an application code, so the effect rule already reads
  it as no effect. Its `data` is required and carries an open reason word, as
  `send_refused` does: the action (no effect, retry later) is decided by the
  code alone, and the reason only tells a person or a log why, so a reader
  treats a reason it does not know as busy with no further hint and a node can
  name a new reason without a new protocol version. A name once sent is never
  renamed, removed or reused, because a sender that stored one must still read
  it later. A failure whose outcome is not known (a fatal storage error) is not
  back pressure and stays `-32603`.
- **Rejected:** per-request version metadata (the connection is long-lived and
  stateful, so one negotiation is enough); streaming the model's output (the
  channel does not read it); forwarding permission requests to the daemon
  (approvals are the adapter's, from the preset).

### Where

- `sdk/go/adapterproto`: `methods.go` (the table), `version.go` (negotiation),
  `codes.go` (the application codes and the rules of the error object),
  `enums.go`, `lifecycle.go`, `session.go`, `prompt.go`,
  `recv.go`, `endpoints.go` (the messages), `decode.go` (`Decode`, `Encode`),
  `validate.go`.
- `sdk/go/adapterkit`: the serve loop of an adapter (`serve.go`: the states and the lock
  order, `conn.go`: what the adapter sends, `application.go`: what it implements, `lock_*.go`:
  the instance lock). Tests: `TestOnlyInitializePingAndCancelAreServedBeforeInitialize`,
  `TestInitializeWithoutACommonVersionIsRefusedAndTakesNoLock`,
  `TestInitializeAnswersInstanceLockedWhileAnotherAdapterHoldsTheLock`,
  `TestAfterShutdownOnlyPingIsServed`, `TestRequestsAndAnswersAreHeldToTheProtocol`,
  `TestAttachMethodsNeedTheAttachCapability`, `TestAServedAdapterAnswersAndSends`.
- `sdk/go/strictjson`: the strict decoder. It is a public package so that readers
  outside the SDK apply the same rules instead of a second copy. Nearly every
  input error names the path of the offending value, a wrong JSON type included, so a
  message can say which member is wrong without repeating its value (the
  messages strictjson produces; a type that decodes itself, a `string`-tagged
  field and a `json.Number` given a non-number string carry other messages); the
  few failures that cannot be placed have no path, and the rules, those cases and
  what a type that decodes itself must guarantee are in its README. Tests:
  `TestDecodeTypeErrorPaths`, `TestDecodeRefusalOfATypeThatDecodesItselfHasPath`,
  `TestDecodeRejectsUnpairedSurrogates`, `TestDecodePreservesUnicodeStrings`,
  `FuzzDecodeTypeErrorsHavePaths`.
- `sdk/go/semver`: the SemVer 2.0.0 syntax of a version (`Parse`, the `Pattern` the schema
  repeats) and its precedence (`Compare`). It is a public package so that readers outside the
  SDK, such as the CLI, order versions by the same code; the
  rules are in its README. Test: `TestCompare`.
- Tests: `TestNegotiate`, `TestMethodTable`, `TestApplicationCodeTable`,
  `TestBusyReasonTable`, `TestNodeBusyIsNoEffect`, `TestRejectReasonTable`,
  `TestEnumNamesMatchContractPackages` (the reasons are the delivery package's
  own names), `TestErrorObjectKnowsEveryCode`,
  `TestErrorObjectValidate`, `TestCapabilitiesAreOpen`, `TestDecodeStrictness`,
  the `Test*Rules` tables (one row per rule), `FuzzDecodeMessages`.

## Capabilities and endpoint kinds

### What

An adapter declares what it supports in the result of `initialize`:

```json
{"runtime": {"interrupt": true, "ack_on_behalf": true, "server_requests": false, "frontends": ["cli", "mcp"], "attach": true},
 "direct": {"max_endpoints": 50}}
```

The declaration is an open object, as are `runtime` and `direct` inside it: a key
this version does not know is ignored, so a newer adapter can declare a
capability to an older daemon. The daemon declares its own capabilities in the
`capabilities` of the `initialize` params, an open object that version 1 sends
empty; an adapter uses a daemon-side addition only when it finds it there.

`runtime` and `direct` are each absent when the adapter provides no such
endpoints. `frontends` lists the agent tool front ends a session can be set up
with (at least one); `max_endpoints` is 1 to 1000. The first three flags say that the
adapter implements `session.interrupt`, calls `recv.observed`, and receives
requests from its native runtime that it must answer and report. The fourth, `attach`, is
optional (absent means false, so an adapter that predates it still completes the handshake)
and says that the adapter implements attached sessions: `session.attach`, the attached entries
of `adapter.start`, `session.detached`, and their prompt and turn semantics.

What an adapter declares is kept apart from what is currently usable. The
declaration comes from `initialize` and the manifest and is recorded per
connection; it fixes which endpoint kinds exist and which methods may be called.
Usability comes from `adapter.probe` and the `adapter.availability` snapshots
that follow; it explains why delivery is blocked, appears in the endpoint status
and explains why a session could not be created. A runtime endpoint is written
into the node's declaration only once the instance is enabled and its first
probe has passed, and is not withdrawn when it later becomes unusable, so the
declaration does not flap.

A tool probe in `adapter.probe` reports `ran`, `failed` or `not_run`. `not_run`
means the probe did not apply: the preset is not one the adapter can use (it has no
section for it), or the preset does not offer the front end (a session of it is never
set up with that front end), or the front end has no probe. It is neither evidence
nor a failure, and the daemon judges the probe of the instance from the others: it
has passed when at least one tool probe ran and its token was seen, has failed when
any tool probe failed, and stays pending while every one was `not_run`. The daemon
asks for a probe of every front end of the runtime declaration for every preset, and
cannot tell which front ends a preset offers: the adapter tells it by answering
`not_run` for the ones it does not, and a probe of a front end that the preset offers
and that is left out of the answer is a failure. An adapter that ran the probe of a
front end its preset does not offer would fail an instance that works, because
whatever stops that front end (a sandbox, say) stops the probe as well. A probe that
applied and could not be run (the `sidera` program is missing, the runtime's server
cannot be reached) is `failed`, never `not_run`.

| Kind | Read by | Created by |
| ---- | ------- | ---------- |
| `runtime` | nobody; it takes no ordinary messages | the daemon, one per adapter that declares a runtime |
| `session` | the native agent itself, with `recv`; the adapter only wakes it and may acknowledge for it (not for an attached session) | `session.create`, or attached at the session's own request (`session.attach`) |
| `direct` | the adapter itself, as a chat bridge reads for a person | `endpoint.register`, up to `max_endpoints` |

A session endpoint cannot exist without a runtime. "Direct" describes only who
reads the messages: the channel does not distinguish a person from an agent on
the other side.

Control signals an endpoint accepts, which the node's directory publishes for the
endpoint (`accepts`) and by which the daemon answers a signal as unsupported: runtime: `session.create`, `endpoint.status`;
created session: `message.send` (delivered as a prompt), `session.close`,
`endpoint.status`, and `session.interrupt` when the adapter declared interrupt; an
attached session: `message.send`, `endpoint.status`, because the session is the
person's and the node neither closes nor interrupts it; direct: `message.send`,
`endpoint.status`. A signal that authorization allows but the endpoint does not accept
is answered by the daemon as unsupported, without calling the adapter. The reply carries
no reason; that an attached session is not under remote control is for the front end
to say.

`adapterreg.GuaranteedSignals` is the single table of signals guaranteed by the kind and
the origin of the session alone; `Admission.Signals` builds on it and adds a declared
interrupt for a created session. The list needs no adapter admission, so an open endpoint
can still be described when its adapter has not connected. It makes no claim about optional
capabilities: `session.interrupt` appears only after admission confirms it.

Which methods apply to which kind (forbidden is refused with `1002`; a method
that needs a capability the adapter did not declare is refused with `1003`). The
table judges calls the adapter makes: a method only the daemon may call
(`initialize`, `session.create`, `prompt.deliver`, `session.interrupt`, `session.attach`, ...) is
`-32601` when the adapter calls it, as an unknown method is, before the kind or a
capability is looked at. `session.interrupt` needs `interrupt` for the daemon to
send it, which the signals an endpoint accepts (above) already state, and
`session.attach` and `session.detached` need `attach`, which `Admission.Attaches` states:

| Method | runtime and session endpoints | direct endpoints |
| ------ | ----------------------------- | ---------------- |
| `initialize`, `ping`, `$/cancel_request`, `shutdown`, `adapter.start`, `adapter.probe`, `adapter.availability` | required | required |
| `session.create`, `prompt.deliver`, `prompt.reconcile`, `turn.started`, `turn.ended`, `session.close`, `prompt.persisted` | required | forbidden |
| `checkpoint.put` | optional | optional |
| `session.interrupt` | optional (`interrupt`) | forbidden |
| `session.attach`, `session.detached` | optional (`attach`) | forbidden |
| `recv.observed` | optional (`ack_on_behalf`), session endpoints only; the daemon refuses it with `1002` for an attached session, judging per endpoint | forbidden |
| `native_request.handled` | required when `server_requests` is declared | forbidden |
| `endpoint.register`, `endpoint.recv`, `endpoint.ack`, `endpoint.send`, `endpoint.pending` | forbidden | required |
| `endpoint.unregister`, `endpoint.message_status` | forbidden | optional |

Both kinds can be declared at once; the requirements add up.

**Admission.** `adapterreg.Admit` takes the capabilities and the manifest and
refuses an adapter that declares neither runtime nor direct endpoints, whose
capabilities or manifest break a rule, whose manifest gives a budget for a front
end it does not declare or a receive wait limit for an agent front end no declared
runtime lists, or whose acknowledgement markers `handoff` cannot compile.
Otherwise the admission records the kinds, the signals of each kind, the budget
per front end (the manifest's, or the default; `Admission.PartBudgets` gives the budgets of
every front end an endpoint can be read through, which a long message is cut for so that
each front end gets the same parts), the receive wait per agent front
end (`Admission.RecvWait`: the manifest's limit, or `adapterreg.DefaultRecvWait`,
8 seconds; it does not apply to direct endpoints) and the compiled markers.
`CheckHandshake` additionally requires that the adapter reports the manifest's id
and version and a protocol version in both the requested and the manifest range,
which is the range `adapterreg.SharedProtocol` returns. The installation uses the
same function: a package whose manifest range has no version in common with
`adapterproto.SupportedRange()` is refused when it is installed
(`refused/protocol_unsupported`), because it would be refused at every handshake.

**Mappings.** The daemon turns what an adapter reports into the values the
contract packages take. `internal/adapterreg` holds the mapping for every row
except the observations that are plain calls (marked "call"):

| Observation | Becomes |
| ----------- | ------- |
| `prompt.deliver` returns `accepted` | no outcome yet (`PromptOutcome` returns zero); keep the attempt in flight until confirmation or reconciliation |
| `prompt.deliver` returns `persisted` | `handoff.AttemptDelivered` |
| `prompt.deliver` returns `refused` | `AttemptFailed` |
| an error response with a code of the effect rule's no-effect list (`-32700`, `-32600`, `-32601`, `-32602`, `-32001` to `-32004`, `-32022`, and any code in the application range 1001 to 1099 except `1005`) | `AttemptFailed` (the request had no effect) |
| no response, a timeout, `-32603`, `-32800`, `1005 attempt_conflict`, a closed connection, and an error response with any other code, a code of no package's table included | `AttemptUnknown`, then `prompt.reconcile` |
| reconcile `found`, `provably_absent`, `indeterminate` | `AttemptDelivered`, `AttemptFailed`, `AttemptUnknown` |
| `prompt.persisted` with an attempt | call: proof the attempt was delivered, handled by the engine |
| `turn.started` with an attempt | call: proof the attempt was delivered, as a `found` verdict |
| availability `runtime_auth` | `Signals.AuthInvalid` |
| availability `native_unavailable`, `dependency_missing` | `Signals.ServiceUnavailable` |
| the daemon's own loss of the connection or two failed pings | `Signals.AdapterOffline`, with `Since` the moment it dropped |
| the cause `handoff.AssessBlocked` names (`AdapterOffline`, `AuthInvalid`, `ServiceUnavailable`) | `delivery.CauseAdapterOffline`, `CauseRuntimeAuth`, `CauseNativeUnavailable` (`BlockedCause`; `HandoffBlockedCause` is its inverse, for a block known only from the marks on target records) |
| `since_ms` | `Signals.Since`; zero (unknown) when absent or later than the moment the snapshot arrived, or when any condition of the cause lacks one |
| a `session.create` the daemon refuses itself, before calling the adapter (`DaemonCreateFailure`): preset unknown, preset closed to local sources, adapter offline, node endpoint limit | control reply failed with `preset_unknown`, `preset_unavailable`, `adapter_offline`, `node_endpoint_limit`, and no session endpoint |
| `session.create` created, failed, indeterminate | control reply created; failed (`preset_invalid` becomes `preset_unavailable`, `dependency_missing` becomes `native_unavailable`, `control_unknown` stays `control_unknown`, the others keep their names); keep reconciling, then unconfirmed |
| `session.interrupt` stopped, requested, not_running, refused | reply stopped; stopped if `turn.ended` came in time else requested; not_running; failed |
| `session.close` closed, close_unconfirmed | reply closed; close_unconfirmed with the same reason |
| `recv.observed` verdict and reason | call: `handoff.CheckBeforeAck` over the observed text and the stored page output; allowed settles every part through `delivery.Record.Ack`. The reasons and the item acknowledgements are named by `AckReason` and `ItemAck` |
| item acknowledgement | `recorded`, `completed`, `duplicate`, `refused_no_handout`, `refused_lease`, `too_late`: the names of the `delivery` verdicts (a refused token cannot occur: the connection owns its endpoints). A part the endpoint was never given is `refused_no_handout` whatever the reason: a part number beyond the message (`delivery.Record.Ack`), and a message the daemon holds nothing of for the endpoint. |

### Why

- **Declared and usable are separate** so that a transient fault never rewrites
  what the node says about itself, which other nodes act on.
- **`endpoint.*` is forbidden on session endpoints** because the model must read
  its own messages; an adapter reading them for it would swallow them.
- **An undeclared capability is `1003`, a wrong kind `1002`**, so an adapter
  author can tell a bug in their choice of endpoint from a missing declaration.
  A method of the other direction is `-32601`: it does not exist for the sender,
  so neither the kind nor a capability could make it valid.
- **`1005 attempt_conflict` leaves a prompt attempt unknown** although the
  conflicting request itself did nothing: the identifier is already known, so an
  earlier request of the attempt reached the adapter and may have delivered.
- **A creation the daemon refuses itself has one mapping, beside the adapter's.**
  Which of its own checks becomes which wire reason is a rule about the
  protocol, so it lives with `CreateReply` instead of being spelled out wherever
  the daemon makes the check. The set of reasons is closed, so a new refusal needs
  a decision about what the sender is told. A preset closed to local sources is
  `preset_unavailable` and not a new value: for the sender it is the same
  situation as an adapter reporting that a preset cannot be used for this
  creation (the preset exists, nothing was created), and the protocol's reasons
  are persisted in signed records, so a new one is not added for a case an existing
  one already states truthfully.
- **The daemon, not the adapter, judges acknowledgements.** The adapter only
  reports what it saw in the durable record, so one rule decides what counts as
  seen.

### Where

- `internal/adapterreg` (`admit.go`, `methods.go`, `mappings.go`); tests
  `TestAdmitDecisions`, `TestDeclaredKindsAndSignals`, `TestMethodMatrix`,
  `TestCreateReplyMapsEveryCreateFailure`, `TestCheck`, `TestCheckRefusesADaemonToAdapterMethodCalledByTheAdapter`, `TestPromptOutcomeOnAttemptConflictIsUnknown`, `TestInvalidEndpointKindFailsClosedInAuthorization`, `TestSignals`, `TestBlockedCause`, `TestStatusConditions`, `TestCreateReply`, `TestDaemonCreateFailure`, `TestInterruptReply`,
  `TestCloseReply`, `TestCheckHandshake`, `TestAPackageSpeaksTheProtocolWhenTheRangesShareAVersion`.

## Control signals

### What

A message can carry a control signal for its target endpoint instead of text for
its agent. The values below are defined as protocol types in `adapterproto`, with
the integer a binary encoding will carry and the name a JSON reply uses. Every
enum is a small integer from 1; 0 is never valid; a field that does not apply is
absent. A message with a control signal has exactly one recipient (`endpoint.send`
with `control` takes a single address in `to`): one adapter executes the signal
and one reply comes back; to act on several endpoints, send one message to each.

`control.session.create` (target: a runtime endpoint). Parameter: the preset name,
1 to 64 characters of `[a-z0-9][a-z0-9._-]*`. The initial message is the body of
the carrying message; an empty body means none. Reply:

| Field | Values | Present |
| ----- | ------ | ------- |
| `outcome` | 1 created (readiness shown), 2 unconfirmed (may exist; no readiness before the deadline), 3 failed (no session will be attached to the channel; certainly not created, except for reason 9) | always |
| `session_endpoint` | the endpoint allocated for the session, as the node of the runtime knows it: the node is not part of it, and a sender on another node completes the address with the node of the runtime it signalled | outcome 1 or 2 |
| `reason` | failed: 1 preset_unknown, 2 preset_unavailable, 3 adapter_offline, 4 runtime_auth, 5 native_unavailable, 6 native_error, 9 control_unknown, 10 node_endpoint_limit; unconfirmed: 7 ready_timeout, 8 adapter_lost | outcome 2 or 3, from its group |

`control.session.close` (target: a session endpoint), no parameters. Reply
`outcome`: 1 closed, 2 close_unconfirmed, 3 unsupported; `reason` only with 2:
1 turn_still_running, 2 adapter_offline, 3 native_unreachable.

`control.session.interrupt` (target: a session endpoint), no parameters. Reply
`outcome`: 1 stopped, 2 requested, 3 not_running, 4 failed, 5 unsupported; `reason`
only with 4: 1 adapter_offline, 2 native_unreachable, 3 native_error. Stopping a
turn does not undo its side effects.

`control.endpoint.status` (target: any endpoint), no parameters, answered by the
daemon alone from what it has observed:

| Field | Meaning |
| ----- | ------- |
| `activity` | 1 idle, 2 busy, 3 unknown (the adapter is offline and the last report cannot be checked) |
| `queued` | messages waiting, not counting those paused by authorization |
| `conditions` | ascending, no repeats, omitted when empty: 1 adapter_offline, 2 runtime_auth, 3 native_unavailable, 4 server_request_rejected, 5 control_unknown; 6 version_untested is obsolete and reserved for ever (see below) |
| `presets` | `name`, `available`, sorted by name; runtime endpoints only |
| `as_of` | Unix seconds, 1 to 253402300799 (9999-12-31T23:59:59Z, the latest time any record carries; the millisecond bound would admit times billions of years away) |

In JSON (the `control_reply` of `endpoint.message_status`) a reply is one object
with `signal` beside its fields, and enums are their names.

### Why

- **One recipient per control signal.** A signal is executed once, by one
  adapter, and has one reply; several recipients would bundle several executions
  and their different outcomes under one message identifier and one
  `control_reply`. The schema states it (`if` `control` is present, `to` has at
  most one entry) and `SendParams.Validate` repeats it.
- A creation that times out is `unconfirmed` and not `failed` because a session
  may exist; the pre-allocated endpoint is returned either way so a retry cannot
  create a second one.
- `control_unknown` is a reason of `failed` and not a third outcome because for
  the channel the attempt is over either way: no session is attached. What
  differs is what the owner must be told, a native process that nobody controls,
  so the reason has its own name and its own number (9: the numbers are the ones
  signed records carry and never change, so it follows the unconfirmed group
  instead of renumbering it) and the daemon notifies the local owner on receiving
  it. The adapter does not stop the process: it cannot do so safely when two
  writers hold the session.
- **Condition 6, `version_untested`, is obsolete and its number is reserved for ever.** It
  said the runtime's version was outside the manifest's tested ranges, a verdict the
  daemon no longer makes. The numbers are the ones signed records carry and never change
  meaning, so a reply or status stored before the removal may still carry 6: it decodes
  and validates (`adapterproto.ControlConditionObsoleteVersionUntested`; the schema lists
  the name, and `result-control-status-obsolete-condition.json` is its example), and
  nothing produces it. The next condition added takes 7. Nothing at the start of the
  daemon reads a stored reply: replies are verified when they are taken in and when a
  status is read, so no stored 6 can stop a start.
- `node_endpoint_limit` is a failed creation: the signal was accepted, but another
  endpoint would exceed the node's cap, so no session was created and no session
  endpoint is returned. It has its own reason because `preset_unavailable` would
  falsely blame a usable preset in the signed reply. The daemon decides the cap,
  so the adapter's native `session.create` failure reasons do not include it.
- One reply per signal, with `requested` and `stopped` as values of the same
  field, because a second report later would need a second record; `status`
  answers the question afterwards.

### Where

- `sdk/go/adapterproto/control.go`; tests `TestControlEnumCodes`,
  `TestControlCreateReplyRules`, `TestControlStatusReplyRules`,
  `TestControlReplyJSON`; `TestEndpointSendRules` (rows "control ... with two recipients").

## Native process identity

### What

`ProcessIdentity` appears in the result of `session.create`, in `turn.started`
and in the session entries of `adapter.start` (both ways), and is kept in
checkpoints. The daemon never reads the process table: it stores and shows the
fields and hands them back to an adapter that reconnects.

| Field | What it is for |
| ----- | -------------- |
| `pid` (1 to 2^32-1) | Find the process: to tell whether it is busy, to signal it, to take it over after a restart |
| `start_key` (opaque, at most 64 characters, compared only for equality) | With `pid`, one process instance, so a reused PID is not mistaken for it. Built from the platform's raw start value and, on Linux, the boot identity; its exact formats are under "Native processes" below, and they stay the same across adapter versions |
| `started_at_ms` | An approximate start time for people; never used in a decision |
| `container.kind` | `systemd_unit`, `process_group`, `launchd_job`, `windows_job`, `scheduled_task` or `none`: how the process was isolated so it outlives the adapter |
| `container.name` (at most 256 characters; absent for `none`) | The unit, group, job or task name: the daemon checks it differs from its own unit or group and warns the owner when it does not; orphan cleanup takes the whole group; it also serves diagnosis |

### Why

A PID alone is reused; the start key makes the pair unique without the daemon
reading platform-specific process tables, which stay in the adapter.

### Where

- `sdk/go/adapterproto/lifecycle.go` (`ProcessIdentity`, `Container`); test
  `TestProcessIdentityRules`.

## Native processes (`sdk/go/adapterkit/process`)

### What

Package `sdk/go/adapterkit/process` is what an adapter uses to run a native agent
runtime as a process of its own on macOS, Linux and Windows. On any other
platform the package compiles and `Start`, `Interrupt` and the process table fail
with `ErrUnsupported`, `Start` before anything is started; a process that cannot be
named is never started.

**Start keys.** The `StartKey` of a process, with its PID, is the
`ProcessIdentity.start_key` of the protocol. The formats are fixed; a key written
by one version is read by every later one.

| Platform | `StartKey` | Read from |
| -------- | ---------- | --------- |
| macOS | `<sec>.<usec, 6 digits>` | `kern.proc.pid` and `kern.proc.all`, `P_starttime`; a zombie is not running; arguments from `kern.procargs2` |
| Linux | `<ticks>@<boot_id>`, at most 57 bytes | field 22 of `/proc/<pid>/stat`, counted from the last `)` because the command name is free text, and `/proc/sys/kernel/random/boot_id`; state `Z` or `X` is not running; arguments from `/proc/<pid>/cmdline` |
| Windows | the creation FILETIME as a decimal `uint64` | `OpenProcess(QUERY_LIMITED_INFORMATION | SYNCHRONIZE)` and `GetProcessTimes`; a process whose handle is signalled has ended; arguments from `NtQueryInformationProcess(ProcessCommandLineInformation)` |

`Process.NativeStart` is the same value as plain decimal text (macOS keeps
`<sec>.<usec>`) for comparing with a start another program recorded in that form;
`Process.StartTime` is the wall-clock start for display. On Linux it is the boot
time plus the ticks, so it moves when the clock is set and is never a decision.

**Lookup and List.** `Lookup` answers "found" only for a running process. An error
means the platform did not answer (access denied included), never that the process
is gone: a caller that took it for gone could start a second writer beside one
that runs. `Running(identity)` is `Lookup` compared with the start key, with one
addition on Windows: a process whose handle `OpenProcess` refuses (a SYSTEM or
protected process that took over the PID, as happens after a reboot) has its
creation time read from `NtQuerySystemInformation(SystemProcessInformation)`,
which needs no handle. A different start is another process, so `Running` is false
without an error; the same start stays an error, because without a handle the
process cannot be shown to be running (its handle cannot be waited on); a process
the table does not list has ended. `List` returns the running processes of the
adapter's effective user with their arguments and is a listing to recognise
processes by their arguments, not a proof that a process is not running: a process
absent from it may still run, and whether a known identity runs is `Running`'s
answer. On Linux the user is the effective UID of the `Uid:` line of
`/proc/<pid>/status`, not the owner of `/proc/<pid>`, which the kernel makes root
for a process that is not dumpable (`PR_SET_DUMPABLE 0`, as agent runtimes set).
On Windows the token's user SID is compared, and the processes `OpenProcess` or
the token refuses (higher integrity levels, protected processes) are left out
whoever owns them, because their owner cannot be read without opening them. Those
that end during the pass are left out too; any other failure, token errors
included, is an error. A PID of zero or below is never found.

**Detachment.** `Start` always asks for a process that outlives the adapter and
the daemon, and says in `Started.Detachment` how far that succeeded; falling back
never fails the start. An error that wraps `ErrStartedUnidentified` means the
process runs and only its identity could not be read (`Started` is still filled);
any other error means nothing started. `Spec.Argv[0]` must be an absolute path (an
error before anything starts) and is run as written, never looked up in `PATH`: a
start in a systemd scope would otherwise resolve a bare name through `PATH` and
make a relative path absolute, and the two ways of starting would run different
programs.

- **Unix.** A session and a process group of their own: the PID is the group's ID,
  there is no controlling terminal and a hang-up of the adapter's terminal does not
  reach the process. `Container` is `process_group` named by the PID.
- **Linux, systemd user scope.** When `systemd-run --user --scope --quiet -- true`
  succeeds (checked once per process, with a 5 s limit), the process is also moved
  out of the adapter's control group: `systemd-run --user --scope --quiet
  --unit=<unit> -- <argv>` is started in the session, and `systemd-run` executes
  the command in place, so the PID is the process's. The unit is
  `sidera-<base name of argv[0], characters outside [A-Za-z0-9_.-] replaced by _>-<16
  hex digits>.scope` and `Container` is `systemd_unit` with that name. A scope
  belongs to the user manager, which ends at the last logout of a user without
  linger. A daemon that was started from a login session (SSH, a terminal
  multiplexer) is not under the user manager and a process in a session of its own
  survives that logout, so the scope is used only when `Linger=yes` (`loginctl
  show-user <uid> --property=Linger --value`; without a user the command shows the
  manager's properties, which have no `Linger`) or the adapter's own cgroup in `/proc/self/cgroup` is under
  `user@<uid>.service/`. Otherwise, and when `systemd-run` is missing or the user
  manager cannot be reached, the process gets the session only. `systemd-run`
  reaches the user manager through `XDG_RUNTIME_DIR` and
  `DBUS_SESSION_BUS_ADDRESS`, which the probe found in the adapter's environment; a
  start whose `Spec.Env` leaves out either one the adapter has would exit 1 inside
  `systemd-run` after `Start` returned, so that start is made in a session only and
  `Started.Detachment.Caveat` says which variable was missing. The executable is
  checked for being a regular executable file before a scope start, so a missing
  program or a directory fails the start as it does without a scope. Until
  `systemd-run` has executed the command the process is `systemd-run` itself: `List`
  shows its arguments and an `Interrupt` reaches it. `HostDetachment().Caveat` says which was chosen and why,
  and, for a scope without linger, that the process ends at the last logout.
- **Windows.** Started with `CREATE_BREAKAWAY_FROM_JOB` and the window hidden, so
  that it leaves the job the daemon may have put the adapter in. A process that is
  not interruptible gets `CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS`; an
  interruptible one gets `CREATE_NEW_CONSOLE`, a console of its own that
  `Interrupt` attaches to. When the job forbids breakaway (start fails with access
  denied) the start is retried once without the flag and the process stays in the
  job. The job is then read with `QueryInformationJobObject` on the calling
  process: with `KILL_ON_JOB_CLOSE` the process does not outlive the daemon and
  the caveat says so; otherwise it does and the caveat says it stays in the job.
  The query reads only the job the adapter is directly in; a limit set by an outer
  job of a nest is not seen. When the start was refused although that job allows
  breakaway, an outer job forbade it and its kill-on-close is unknown, so the
  `Detachment` promises nothing (`OutlivesDaemon` false), as for a job that could
  not be read. `Container` is `none`. Before the first interruptible
  start the "ignore Ctrl-C" setting a process inherits (set for a process started
  over SSH or by a service, and silent: the interrupt then has no effect) is
  cleared with `SetConsoleCtrlHandler(NULL, FALSE)`, which changes the adapter's own
  setting too, for the rest of its life.
- **macOS.** The session only; no caveat.

`HostDetachment` reports what a start achieves on this host before anything has
started, and the `Detachment` of each `Started` is the authority. On Linux it is
decided once per process, on the first call to it or to `Start`, and that call runs
two commands with a 5 s limit each, so it can take up to about ten seconds. On
Windows it is read from the adapter's job on every call, the innermost job only.
Elsewhere it is constant.

**Interrupt.** On Unix, `SIGINT` to the process alone after checking its identity.
On Windows there is no way to send Ctrl-C to a process without attaching to its
console, and attaching would take the adapter's own console away, so
`Interrupt` runs the adapter's own executable as a short-lived helper:
`<exe> --sidera-adapterkit-helper interrupt <pid> <start key>`, detached from any
console, hidden, and in the adapter's session (`AttachConsole` is refused across
sessions). The helper checks the identity (exit 3, mapped to `ErrNotRunning`),
frees its console, attaches to the target's, ignores Ctrl-C itself, sends
`CTRL_C_EVENT` to the console, stays attached for 300 ms and prints `ok`. It holds
a handle to the target from the identity check until the event is sent, so that
the PID cannot name another process in between. A
failure exits 1 with the Win32 error on stderr. An adapter that calls
`Interrupt` must call `process.HandleHelperInvocation` as the first statement of
its `main`; the call sets a flag of the package and `Interrupt` fails on every
platform, with nothing sent, until it is set. A helper reply without `ok` is an
error too. The event reaches every process attached to the console, which is why an
interruptible process has a console of its own. `Interrupt` works for any process
that has a console to attach to; a process started without one (the default) has
none and the helper fails. A process gets its console while it starts, not when
`CreateProcess` returns, and until then `AttachConsole` fails with
`ERROR_INVALID_HANDLE` exactly as for a process that has none: so the helper tries
again every 20 ms for as long as the target is younger than 5 s (by the start in
the identity) and fails at once for an older one, and an `Interrupt` right after
`Start` is delivered once the console is there. For a process that never had a
console the failure therefore comes up to 5 s after its start.

### Why

- A PID alone is reused. The key is the platform's own start record, so that no
  clock adjustment can change it: Linux's is in ticks after boot and the boot id
  makes a key from before a reboot differ from every key after it.
- The Linux scope rule exists because a scope is not strictly better than a
  session: a daemon in a login session without linger has children that survive
  the logout today, and a scope would end them with the user manager. When the
  daemon is already under the user manager it ends with it, so the scope risks
  nothing and escapes the daemon's cgroup when it later runs as a user service.
- A helper executable and not a call in the adapter, because `AttachConsole` and
  `FreeConsole` change the console of the whole calling process. The adapter's
  own executable is used so that no second file has to be shipped.
- Rejected: `pidfd` (the window between check and signal is microseconds);
  killing a whole process tree (Windows has none without a job of our own, which
  conflicts with breakaway); a stateful launcher object (the facts are per
  process).

### Where

- `sdk/go/adapterkit/process/` (`identity*.go`, `start*.go`, `interrupt*.go`,
  `helper*.go`); its README lists the entry points.
- Tests: `process_test.go` (the contract on every platform: identity, `Start`,
  `Interrupt`), `identity_test.go` (an error is never "not running", a refused
  process with another start is), `interrupt_test.go` (`Interrupt` without
  `HandleHelperInvocation`), `start_linux_test.go` (the scope rule, the fallback for
  an environment without the user manager, unit names, the executable check),
  `identity_linux_test.go` (the stat and status parsers, the classification of
  what `/proc` answers), `process_linux_test.go` (a non-dumpable process is
  listed), `identity_windows_test.go` (the classification of what `OpenProcess`
  answers, the system process table against the handle, the System process told
  apart by its start), `detachment_windows_test.go` (a start refused breakaway),
  `start_windows_test.go` (the adapter in jobs with the limits breakaway
  forbidden, breakaway forbidden with kill-on-close, and breakaway allowed with
  and without kill-on-close). CI runners have no systemd user manager and no
  desktop session, so the scope and the console interrupt in an interactive
  session are run by hand on those hosts.

## Manifest

### What

`sidera-adapter.json` sits at the root of an adapter package. The daemon reads it
to install an adapter and to admit one. `sdk/go/manifest` parses and validates it, and
writes a package directory (`manifest.WritePackage`: the manifest and the binary at
its entry) for the `package` subcommand of each adapter.

```json
{
  "manifest_version": 1,
  "id": "example",
  "version": "0.3.1",
  "protocol": {"min": 1, "max": 1},
  "entry": {"program": "bin/sidera-adapter-example", "args": ["--stdio"]},
  "platforms": [{"os": "darwin", "arch": "arm64"}, {"os": "linux", "arch": "amd64"}],
  "requires": [{"program": "example-runtime", "os": ["darwin", "linux"]}],
  "budgets": [{"frontend": "cli", "unit": "chars", "limit": 27000},
              {"frontend": "mcp", "unit": "tokens", "limit": 22500}],
  "recv_wait_limits": [{"frontend": "cli", "max_s": 8},
                       {"frontend": "mcp", "max_s": 100}],
  "ack_markers": {"replaced": [{"literal": "[result saved to file]"}],
                  "truncated": [{"regexp": "\\.\\.\\. \\d+ more lines"}]}
}
```

Optional lists and objects are omitted when empty; an explicit empty one is an
error.

The rules are in [sdk/go/manifest/README.md](../../../sdk/go/manifest/README.md).
In short: `id` and `version` are checked as the adapter reports them in
`initialize`, and must equal them; the protocol range and the adapter's
announcement are checked against each other; configuration uses a small field vocabulary; budgets map to the output
budget of `recv` for that front end, and a front end without one gets the default;
`recv_wait_limits` give, per agent front end (`cli` or `mcp`, never `direct`), the
longest an agent's `recv` may wait for a message, in whole seconds from 1 to 120
(`manifest.MaxRecvWaitSeconds`, the longest such a wait may be whatever a
manifest declares), one entry per front end, and a front end without an entry
waits at most `adapterreg.DefaultRecvWait`, 8 seconds. A direct endpoint's
`endpoint.recv` has its own cap, `adapterproto.MaxWaitSeconds` (30 seconds),
which this field does not govern: the adapter chooses that wait in `wait_s`.

`platforms` is what the installation judges: a package whose list does not hold the
operating system and architecture of the machine is refused when it is installed
(`adapterreg.RunsOn`), because it would otherwise be installed and fail to start,
which the owner would meet only as an instance that never comes online. `protocol` is
judged the same way (`adapterreg.SharedProtocol`): a range that shares no version with
the ones this node speaks is refused when it is installed, so that installing it over a
running instance cannot send that instance into a restart loop.

The manifest lists no tested versions of the native runtime, and the daemon neither
judges nor blocks a version: the runtimes upgrade several times a day, so a list
could not stay true and an alert about a version outside it would be noise. The
name and version that `adapter.probe` reported are shown as reported. An adapter
whose reading of a runtime's record may not hold in a newer version verifies what it
can of the form of the record and answers `prompt.reconcile` with `indeterminate` and
`record_unreadable` when it is not the form it reads, never with absence. That guard is
partial, and the protocol does not promise more: the Codex adapter checks the members
of the items it recognizes (a page without its items, a `userMessage` item without its
`clientId`), so a record that lost or renamed those members is caught, but one that
renamed the item type itself would still read as holding no prompts, and a wrong
`provably_absent` is then possible. The guard lowers the chance of a wrong absence; it
does not remove it.

### Why

- **A small field vocabulary, not JSON Schema.** The daemon validates
  configuration at run time and builds forms from it; a full schema engine would
  make a validation library a run-time dependency of the daemon, for roughly a
  hundred lines of checks. The examples elsewhere (extension manifests of other
  agent tools) use small vocabularies too.
- **No "secret" type.** Native credentials are read by the adapter from their
  native place; they never enter Sidera's configuration.
- **Markers are data, and compiled once.** The runtime-specific notice that
  stands in for a tool result belongs to the adapter, so it is declared in the
  manifest; the check that a marker is usable lives in `handoff` where it is
  applied, and the manifest package only parses its shape.
- **The receive wait is declared per agent front end, by the adapter,** because
  the client that runs the front end ends a call at its own timeout and only the
  adapter knows it: a shell command of the Codex CLI returns after 10 seconds with
  the command still running (the default of its unified execution mode) or is
  killed at 10 seconds (its one-shot mode), while MCP tool calls are allowed far
  longer. A page returned after the client gave up is not seen by the agent, but
  nothing is lost: the message stays leased and comes back, marked as a possible
  duplicate, when the lease expires. A wait that is too short only makes the agent
  ask again. So the default for a front end that declares nothing is 8 seconds,
  which leaves 2 under the shortest known timeout of 10 for the connection and the
  output, and a declared limit is capped at 120 seconds so that one wait cannot
  hold a daemon connection for long. The limit is a field of its own, not part of
  a budget entry: a budget entry requires a unit and a limit, so a front end that
  only wants to declare its wait would have to invent a budget. `direct` is not
  allowed: a direct endpoint has no client with a timeout of its own, the adapter
  reads it itself and chooses each wait in the `wait_s` of `endpoint.recv`, capped
  by `adapterproto.MaxWaitSeconds` (30 seconds). Whole seconds are enough for the
  agent front ends; a wait needs no finer limit. The cap of 120 lives in
  `manifest.MaxRecvWaitSeconds` and the default in `adapterreg.DefaultRecvWait`,
  nowhere else.
- **The budget is declared per front end** because the runtime shows different
  amounts through different tool paths; the author subtracts the safety margin so
  the daemon uses the limit as given.

### Where

- `sdk/go/manifest/manifest.go`; tests `TestIdentityRules`, `TestEntryRules`,
  `TestPlatformAndRequirementRules`,
  `TestConfigFieldRules`, `TestBudgetRules`, `TestRecvWaitLimitRules`,
  `TestParseRecvWaitLimits`, `TestAckMarkerShape`, `TestParse`, `FuzzParse`;
  `protocol/conformance_test.go` `TestManifestRecvWaitLimitsMatchSchema`;
  `internal/adapterreg` `TestAdmitDecisions` (a limit for an undeclared front
  end, a limit for `direct`) and `TestRecvWait`.

## Schema and conformance

### What

`protocol/adapter/v1` holds four hand-written JSON Schema 2020-12 files (message,
types, methods, manifest) and golden messages under `examples/`. They are the
definition an adapter in another language implements; see
[protocol/README.md](../../../protocol/README.md) for the files and conventions.
The Go types in `sdk/go` are not generated from them and do not generate them;
tests keep the two in agreement.

`protocol/conformance_test.go` (package `protocol_test`, in the root module so the
SDK module keeps no dependencies) checks:

1. every golden message is accepted by the schema, and each sits in the directory
   of its method with the right kind;
2. every golden message decodes strictly into the Go type of its method, passes
   the Go validation and encodes back to the same data;
3. the method set, directions and kinds, every enum, the error codes with their
   names and the patterns are the same in the schema and in Go; every method has a
   request and a result example; every enum value and error code appears in an
   example, counted per Go type by reflection, not by string;
4. every invalid example is rejected at the layer it names, and every `open/`
   example, a document with unknown capability keys, is accepted by both and read
   by Go as the same document without them; an unknown key added to any object of
   an `initialize` message is refused by the schema and by Go everywhere except
   in the capability objects, where both accept it
   (`TestOpenExamples`, `TestMembersAreClosedExceptInCapabilities`);
5. differential tests give the same document to the schema and to Go and
   require the same verdict, both ways, except for the rules the schema cannot
   state, which Go must report as such. Four of them: a seeded random mutation of
   every golden document (about 30,000 mutants a run, with counts of each outcome
   asserted); every single edit at every position of every golden message (leave
   out, null, retype, a key in another case, an unknown key, and every member
   another message of the same method has); the lengths, counts and numbers around
   every limit the schema states for each position, and around the edges of what
   Go accepts there, found by bisection, so a limit missing from the schema is
   found too; and `FuzzDifferential` with the fuzzer's inputs;
6. the output of `handoff.Pack` and `handoff.Part`, for whole messages, resumed
   and last parts, duplicates and empty queues, is a valid `RecvPage` to both the
   schema and the Go type;
7. the platform vocabulary of the manifest covers every platform the Go
   toolchain lists;
8. every line the Go peer writes while its own tests run is a frame the message
   schema accepts (`peerwire_test.go`, which runs those tests with the peers'
   writers recorded);
9. no pattern of the schema or of Go accepts a value with a trailing newline,
   which engines that let `$` match before one would (`TestPatternsRefuseATrailingNewline`).

### Why

- **The schema is the source of truth, written by hand.** Tools that generate a
  schema from Go types make the Go types the truth, and tools that generate Go
  from a schema do not support `oneOf` with `const`, which the method list needs.
- **A differential test, not only examples.** Examples prove the cases someone
  thought of. Two independent implementations (a schema validator and the Go
  decoder) agreeing on tens of thousands of edited documents find the cases
  nobody thought of: it found a repeated key that Go and the validator read
  differently, and mutating the schema itself (dropping a limit, widening an
  enum) is caught almost everywhere; what is left is rules stated twice, once in
  the message schema and again in the methods schema.
- **Go may be stricter, never looser.** A message Go accepts must be valid for
  every other implementation. The few rules a schema cannot express are the only
  exceptions, and they are listed and marked so the test can tell them from a
  mistake.
- **The validator is a test dependency of the root module only.** It is not a
  dependency of the SDK or the daemon.

### Where

- `protocol/conformance_test.go`, `differential_test.go`, `edits_test.go`,
  `recvpage_test.go`, `peerwire_test.go`, `oracle_test.go`, `helpers_test.go`.
