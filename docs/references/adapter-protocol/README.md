# Adapter protocol

How the Sidera daemon talks to an adapter: a separate program that drives a
native agent runtime (Claude Code, Codex) or bridges a chat tool to the channel.
The daemon starts the adapter and the two exchange newline-delimited JSON-RPC 2.0
over the adapter's standard input and output; standard error is for logs. One
process is one adapter instance. The peers are symmetric: either may send
requests and notifications. "D to A" below means the daemon calls the adapter and
"A to D" the other way.

The SDK lives in its own Go module, `github.com/aianyai/sidera/sdk/go`, which
depends on the standard library only and is imported by the daemon and by
third-party adapters alike. The frames are handled by `sdk/go/peer`; the
messages, enums, application error codes and manifest are defined in the packages
`adapterproto` and `manifest` and, for other languages, by the JSON Schema under
`protocol/adapter/v1`. The daemon-side decisions are in `internal/adapterreg`.
This document has one part per layer: the peer layer, then the messages and
methods, the control signals and the native process identity.

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
  batch, a member name that occurs twice in the frame, a null or malformed
  request id, or an id already in flight are `-32600`. After any of these except the overlong line the connection
  continues. The reply carries the id of the line only when the line has a
  `method` and a valid id, that is, when it is recognisably a request; every
  other malformed line, which may have been meant as a response, is answered
  with a null id.
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
- **Close** is idempotent, ends every pending call with `ErrClosed`, cancels
  handler contexts and leaves no goroutine behind once handlers return.
  `Peer.Err()` says why the connection ended.

The error codes of the peer layer are constants in `sdk/go/peer/errors.go`
(`peer.Codes()` lists them): `-32700` parse error, `-32600` invalid request,
`-32601` method not found, `-32602` invalid params, `-32603` internal error,
`-32001` overloaded, `-32002` shutting down, `-32003` not initialized,
`-32004` line too long, `-32022` unsupported protocol version, `-32800` request
cancelled; codes 1001 to 1099 are the application range, whose individual
meanings belong to the protocol above the peer (`adapterproto` defines 1001 to
1011). The codes below 1000 and the effect rule are defined here and nowhere
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
  `method` may be a response to a call of the other side. Answering it with its
  id would land in that side's own call-id space: the error would fail one of
  their calls that this side received and may have executed, and a code that
  says "no effect" can make them send it again. JSON-RPC sends no reply to a
  response, so the line earns an error with a null id, which is never the
  answer to a call and which the other side's peer logs and drops. The reply
  is kept rather than replaced by a log line only so that a counterpart that
  speaks a broken dialect gets a visible signal.
- **A repeated member of the frame is refused where the frame is read.** Two
  readers of `{"id":1,"id":2,...}` can disagree about which request is meant,
  and a cancel or an in-flight check would then act on another request than the
  one that is handled. The line is answered with a null id because its id
  cannot be trusted. Repeats inside params and results are left to the layer
  that decodes them (`strictjson`), so the peer does not scan what it does not
  interpret.
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
| The request id rule, `$/cancel_request` params | `sdk/go/peer/requestid.go` (`RequestID`, `CancelRequestParams`) |
| Error codes, `Error`, effect rule | `sdk/go/peer/errors.go` (`Codes`, `NoEffect`, `ResultUnknown`) |
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
| A repeated frame member is refused; a repeat inside params is not the frame's business | `TestRepeatedFrameMemberIsInvalidRequest`, `TestNoDuplicateTopLevelKeys`, `TestClassify` |
| A malformed response-like line is never answered with its id, even when both sides have that id in flight | `TestMalformedResponseIsNeverAnsweredWithItsID`, `TestClassifyDetails` |
| Batch rejected, connection kept | `TestBatchIsRejectedAndConnectionContinues` |
| Overload before params and handler | `TestOverloadedIsDecidedBeforeParamsAndHandler` |
| Cancellation, one reply | `TestCancellation`, `TestCallContextCancellation` |
| Write timeout; slow reader tolerated; also when the last line is the reply to an overlong line | `TestWriteTimeoutClosesTransportOnStalledReader`, `TestSlowButProgressingReaderDoesNotTimeOut`, `TestOverlongLineWithStalledReaderStillCloses` |
| Owed error replies wait for room and are never dropped (flood backpressure) | `TestReadLoopWaitsWhenErrorRepliesCannotBeQueued` |
| Notification order and overflow | `TestNotificationsAreHandledInArrivalOrder`, `TestNotificationOverflowClosesConnection` |
| Outbound full | `TestOutboundQueueFullReturnsErrOverloadedAndWritesNothing`, `TestRefusedCallLeavesNoPendingEntry` |
| Duplicate and invalid ids, the id rule | `TestDuplicateInFlightIDIsInvalidRequest`, `TestInvalidRequestIDs`, `TestRequestIDJSON`, `TestRequestIDValidate`, `TestClassify` |
| A cancel without a usable id, or with an unknown or repeated member, cancels nothing | `TestCancelWithoutAUsableRequestIDCancelsNothing` |
| Late response, malformed response | `TestLateResponseIsDroppedAndLogged`, `TestMalformedResponseFailsTheCall` |
| Unknown method and notification | `TestUnknownMethodIsMethodNotFound`, `TestUnknownNotificationIsLoggedAndDropped` |
| Close, pending calls, goroutines | `TestCloseEndsPendingCallsWithErrClosed`, `TestCloseWithoutStartAndCloseFromHandler`, `TestCloseWaitsForReaderAndWriter`, `TestNoGoroutineLeakAfterManyPeers` |
| Effect rule | `TestEffectClassification`, `TestHandlerErrorsAndEffectRule` |
| No bodies in logs | `TestLogsNeverContainMessageBodies` |
| Many concurrent calls both ways against an independent wire model | `TestConcurrentCallsInBothDirections` |
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
| `initialize` | D to A | request | `protocol` range, `daemon.version`, `instance` (`id`, `lock_path`) | `protocol_version`, `adapter` (`id`, `version`), `capabilities` |
| `ping` | both | request | none | none |
| `$/cancel_request` | both | notification | `request_id`, an id as the peer layer defines it | |
| `shutdown` | D to A | request | none | none |
| `adapter.start` | D to A | request | `node`, `runtime_endpoint?`, `config`, `presets`, `sessions`, `direct_endpoints` | `sessions` (`endpoint`, `state`, `turn_active`, `process?`) |
| `adapter.probe` | D to A | request | `tool_probes` (`preset`, `frontend`, `token`, `env`) | `runtime?`, `auth`, `dependencies`, `presets`, `tool_probes` |
| `adapter.availability` | A to D | notification | `conditions`: a full snapshot, empty when all is well | |
| `session.create` | D to A | request | `attempt`, `runtime_endpoint`, `session_endpoint`, `preset`, `first_prompt`, `tool_access`, `checkpoint?` | `outcome` created, failed or indeterminate, with the fields each needs |
| `checkpoint.put` | A to D | request | `scope` (attempt or session), `id`, `state` (object, at most 16 KiB compact) | none; answered once the state is durable |
| `prompt.deliver` | D to A | request | `endpoint`, `attempt`, `text` | `stage` accepted, persisted or refused, `reason` with a refusal |
| `prompt.persisted` | A to D | notification | `endpoint`, `attempt` | |
| `turn.started` | A to D | notification | `endpoint`, `turn`, `attempt?`, `process?`, `at_ms` | |
| `turn.ended` | A to D | notification | `endpoint`, `turn`, `final`, `at_ms` | |
| `session.interrupt` | D to A | request | `endpoint` | `stage` requested, stopped, not_running or refused |
| `session.close` | D to A | request | `endpoint` | `outcome` closed or close_unconfirmed |
| `prompt.reconcile` | D to A | request | `endpoint`, `attempt`, `checkpoint?` | `verdict`, `turn?`, `grounds` |
| `recv.observed` | A to D | request | `endpoint`, `lease`, `frontend`, `observed` | `verdict`, `reason`, `items` |
| `native_request.handled` | A to D | notification | `endpoint`, `method`, `disposition`, `at_ms` | |
| `endpoint.register` | A to D | request | `key`, `display_name`, `description?` | `endpoint` |
| `endpoint.unregister` | A to D | request | `endpoint` | none |
| `endpoint.pending` | D to A | notification | `endpoint`, `count` | |
| `endpoint.recv` | A to D | request | `endpoint`, `wait_s?`, `message_id?` with `part?` | `page` |
| `endpoint.ack` | A to D | request | `endpoint`, `lease`, `items` | `items` with the outcome of each |
| `endpoint.send` | A to D | request | `endpoint`, `to`, `content_type`, `body`, optional reply, thread, disclosure, deadline, `control`, idempotency key | `message_id` |
| `endpoint.message_status` | A to D | request | `endpoint`, `message_id` | `recipients`, `control_reply?` |

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
negotiated version nothing is added that an older reader would misread: new
behaviour is a new capability the peer declares first, or a new version.

**Strict decoding.** Within a version an unknown field is `-32602`. The same rule
makes a field name that differs in case, `null`, a missing required field, a
repeated key and an explicit empty value for an optional field errors: they
would otherwise let two implementations read one message two ways.

**Instance lock.** The adapter takes an exclusive lock on `instance.lock_path`
before it answers `initialize`, and answers `1011` when it cannot. A hung old
process and its replacement therefore never control sessions at once; the lock
dies with the process. Before `initialize` only `initialize`, `ping` and
`$/cancel_request` are accepted; anything else is `-32003`.

**Prompt delivery.** The daemon records the attempt as dispatching before it
sends `prompt.deliver`. The adapter delivers `text` unchanged (it may append one
line with the attempt identifier as a fallback for reconciliation). `accepted`
means the runtime took the prompt but has not made it durable; the adapter must
then send `prompt.persisted`, or the daemon reconciles. `refused` means certainly
nothing was delivered. `turn.started.attempt` also proves the attempt reached the
session.

**Reconciliation.** `prompt.reconcile` answers whether an attempt reached the
runtime. The `grounds.code` belongs to the verdict: `found` goes with
`attempt_in_record` and carries the turn; `provably_absent` with
`record_complete_without_attempt` (the writer has stopped and the whole record was
scanned) or `never_dispatched` (the checkpoint shows the runtime was never
called); `indeterminate` with `turn_running`, `record_unreadable`,
`native_unreachable` or `runtime_version_untested`.

**Turns.** Each turn has exactly one `turn.started` and one `turn.ended`,
correlated by `turn`. Turns the runtime starts itself (an automatic continuation,
work triggered by background activity) are reported the same way. A turn caused
by a prompt names the attempt.

**Acknowledging on the agent's behalf.** `recv.observed` carries the text of a
`recv` tool result as the model received it, read from the runtime's durable
record, with the lease the result names. The daemon decides whether that proves
the model saw the page and, if so, settles every part of it; the adapter may only
do this for session endpoints it owns. A refusal (`replaced`, `truncated`,
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

**Errors.** Codes `-32700` to `-32600` and `-32603` are JSON-RPC's; `-32001`
to `-32004`, `-32022` and `-32800` are the implementation-defined range; both
groups are defined by the peer layer (`peer.Code*`) and listed here to be read
with the rest. `1001` to `1011` are the protocol's own (`adapterproto.Code*`):

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

Whether a failed request had an effect is the peer layer's effect rule, applied
to the error a call returned (`peer.NoEffect`, `peer.ResultUnknown`; the daemon
does not sort codes itself). Only for the methods with side effects
(`session.create`, `prompt.deliver`, `session.interrupt`, `session.close`) does
an unknown outcome call for reconciliation or an idempotent retry.

### Why

- **Results carry the semantics, errors only misuse.** A refusal the daemon must
  act on (a prompt that was certainly not delivered) is a result with a reason, so
  it can never be mistaken for a transport failure whose outcome is unknown.
- **The version is an integer range.** One round trip lets both sides give up old
  versions explicitly, and the error data shows both ranges. A minor version has
  no meaning on a wire where additions go through capabilities.
- **Strictness fails closed.** A lenient reader that ignores an unknown field
  silently does the wrong thing when the field was a safety restriction.
- **Identifiers stay opaque.** The adapter never parses them, so copying their
  syntax into the SDK would only make a second place to keep in step with the
  daemon, for the benefit of an earlier error message.
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
- `sdk/go/internal/strictjson`: the strict decoder.
- Tests: `TestNegotiate`, `TestMethodTable`, `TestApplicationCodeTable`,
  `TestErrorObjectKnowsEveryCode`, `TestDecodeStrictness`, the `Test*Rules` tables (one row per rule),
  `FuzzDecodeMessages`.

## Capabilities and endpoint kinds

### What

An adapter declares what it supports in the result of `initialize`:

```json
{"runtime": {"interrupt": true, "ack_on_behalf": true, "server_requests": false, "frontends": ["cli", "mcp"]},
 "direct": {"max_endpoints": 50}}
```

`runtime` and `direct` are each absent when the adapter provides no such
endpoints. `frontends` lists the agent tool front ends a session can be set up
with (at least one); `max_endpoints` is 1 to 1000. The three flags say that the
adapter implements `session.interrupt`, calls `recv.observed`, and receives
requests from its native runtime that it must answer and report.

What an adapter declares is kept apart from what is currently usable. The
declaration comes from `initialize` and the manifest and is recorded per
connection; it fixes which endpoint kinds exist and which methods may be called.
Usability comes from `adapter.probe` and the `adapter.availability` snapshots
that follow; it explains why delivery is blocked, appears in the endpoint status
and explains why a session could not be created. A runtime endpoint is written
into the node's declaration only once the instance is enabled and its first
probe has passed, and is not withdrawn when it later becomes unusable, so the
declaration does not flap.

| Kind | Read by | Created by |
| ---- | ------- | ---------- |
| `runtime` | nobody; it takes no ordinary messages | the daemon, one per adapter that declares a runtime |
| `session` | the native agent itself, with `recv`; the adapter only wakes it and may acknowledge for it | `session.create` only |
| `direct` | the adapter itself, as a chat bridge reads for a person | `endpoint.register`, up to `max_endpoints` |

A session endpoint cannot exist without a runtime. "Direct" describes only who
reads the messages: the channel does not distinguish a person from an agent on
the other side.

Control signals an endpoint accepts, which are written into the node's
declaration of the endpoint: runtime: `session.create`, `endpoint.status`;
session: `message.send` (delivered as a prompt), `session.close`,
`endpoint.status`, and `session.interrupt` when the adapter declared interrupt;
direct: `message.send`, `endpoint.status`. A signal that authorization allows but
the endpoint does not accept is answered by the daemon as unsupported, without
calling the adapter.

Which methods apply to which kind (forbidden is refused with `1002`; a method
that needs a capability the adapter did not declare is refused with `1003`):

| Method | runtime and session endpoints | direct endpoints |
| ------ | ----------------------------- | ---------------- |
| `initialize`, `ping`, `$/cancel_request`, `shutdown`, `adapter.start`, `adapter.probe`, `adapter.availability` | required | required |
| `session.create`, `prompt.deliver`, `prompt.reconcile`, `turn.started`, `turn.ended`, `session.close`, `prompt.persisted` | required | forbidden |
| `checkpoint.put` | optional | optional |
| `session.interrupt` | optional (`interrupt`) | forbidden |
| `recv.observed` | optional (`ack_on_behalf`), session endpoints only | forbidden |
| `native_request.handled` | required when `server_requests` is declared | forbidden |
| `endpoint.register`, `endpoint.recv`, `endpoint.ack`, `endpoint.send`, `endpoint.pending` | forbidden | required |
| `endpoint.unregister`, `endpoint.message_status` | forbidden | optional |

Both kinds can be declared at once; the requirements add up.

**Admission.** `adapterreg.Admit` takes the capabilities and the manifest and
refuses an adapter that declares neither runtime nor direct endpoints, whose
capabilities or manifest break a rule, whose manifest gives a budget for a front
end it does not declare, or whose acknowledgement markers `handoff` cannot
compile. Otherwise the admission records the kinds, the signals of each kind, the
budget per front end (the manifest's, or the default) and the compiled markers.
`CheckHandshake` additionally requires that the adapter reports the manifest's id
and version and a protocol version in both the requested and the manifest range.

**Mappings.** The daemon turns what an adapter reports into the values the
contract packages take. `internal/adapterreg` holds the mapping for every row
except the two that are plain calls (marked "call"):

| Observation | Becomes |
| ----------- | ------- |
| `prompt.deliver` returns `accepted` or `persisted` | `handoff.AttemptDelivered` |
| `prompt.deliver` returns `refused` | `AttemptFailed` |
| an error response other than `-32603` and `-32800` | `AttemptFailed` (the request had no effect) |
| no response, a timeout, `-32603`, `-32800`, a closed connection | `AttemptUnknown`, then `prompt.reconcile` |
| reconcile `found`, `provably_absent`, `indeterminate` | `AttemptDelivered`, `AttemptFailed`, `AttemptUnknown` |
| `turn.started` with an attempt | call: proof the attempt was delivered, as a `found` verdict |
| availability `runtime_auth` | `Signals.AuthInvalid` |
| availability `native_unavailable`, `dependency_missing` | `Signals.ServiceUnavailable` |
| availability `version_untested` | no cause; shown in the status |
| the daemon's own loss of the connection or two failed pings | `Signals.AdapterOffline`, with `Since` the moment it dropped |
| `since_ms` | `Signals.Since`; zero (unknown) when absent or later than the moment the snapshot arrived, or when any condition of the cause lacks one |
| `session.create` created, failed, indeterminate | control reply created; failed (`preset_invalid` becomes `preset_unavailable`, `dependency_missing` becomes `native_unavailable`); keep reconciling, then unconfirmed |
| `session.interrupt` stopped, requested, not_running, refused | reply stopped; stopped if `turn.ended` came in time else requested; not_running; failed |
| `session.close` closed, close_unconfirmed | reply closed; close_unconfirmed with the same reason |
| `recv.observed` verdict and reason | call: `handoff.CheckBeforeAck` over the observed text and the stored page output; allowed settles every part through `delivery.Record.Ack`. The reasons and the item acknowledgements are named by `AckReason` and `ItemAck` |
| item acknowledgement | `recorded`, `completed`, `duplicate`, `refused_no_handout`, `refused_lease`, `too_late`: the names of the `delivery` verdicts (a refused token cannot occur: the connection owns its endpoints) |

### Why

- **Declared and usable are separate** so that a transient fault never rewrites
  what the node says about itself, which other nodes act on.
- **`endpoint.*` is forbidden on session endpoints** because the model must read
  its own messages; an adapter reading them for it would swallow them.
- **An undeclared capability is `1003`, a wrong kind `1002`**, so an adapter
  author can tell a bug in their choice of endpoint from a missing declaration.
- **The daemon, not the adapter, judges acknowledgements.** The adapter only
  reports what it saw in the durable record, so one rule decides what counts as
  seen.

### Where

- `internal/adapterreg` (`admit.go`, `methods.go`, `mappings.go`); tests
  `TestAdmitDecisions`, `TestDeclaredKindsAndSignals`, `TestMethodMatrix`,
  `TestCheck`, `TestSignals`, `TestCreateReply`, `TestInterruptReply`,
  `TestCloseReply`, `TestCheckHandshake`.

## Control signals

### What

A message can carry a control signal for its target endpoint instead of text for
its agent. The values below are defined as protocol types in `adapterproto`, with
the integer a binary encoding will carry and the name a JSON reply uses. Every
enum is a small integer from 1; 0 is never valid; a field that does not apply is
absent.

`control.session.create` (target: a runtime endpoint). Parameter: the preset name,
1 to 64 characters of `[a-z0-9][a-z0-9._-]*`. The initial message is the body of
the carrying message; an empty body means none. Reply:

| Field | Values | Present |
| ----- | ------ | ------- |
| `outcome` | 1 created (readiness shown), 2 unconfirmed (may exist; no readiness before the deadline), 3 failed (certainly not created) | always |
| `session_endpoint` | the endpoint allocated for the session | outcome 1 or 2 |
| `reason` | failed: 1 preset_unknown, 2 preset_unavailable, 3 adapter_offline, 4 runtime_auth, 5 native_unavailable, 6 native_error; unconfirmed: 7 ready_timeout, 8 adapter_lost | outcome 2 or 3, from its group |

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
| `conditions` | ascending, no repeats, omitted when empty: 1 adapter_offline, 2 runtime_auth, 3 native_unavailable, 4 server_request_rejected, 5 control_unknown, 6 version_untested |
| `presets` | `name`, `available`, sorted by name; runtime endpoints only |
| `as_of` | Unix seconds |

In JSON (the `control_reply` of `endpoint.message_status`) a reply is one object
with `signal` beside its fields, and enums are their names.

### Why

- `version_untested` is a sixth condition because an untested runtime version must
  show in the status without stopping the endpoint.
- A creation that times out is `unconfirmed` and not `failed` because a session
  may exist; the pre-allocated endpoint is returned either way so a retry cannot
  create a second one.
- One reply per signal, with `requested` and `stopped` as values of the same
  field, because a second report later would need a second record; `status`
  answers the question afterwards.

### Where

- `sdk/go/adapterproto/control.go`; tests `TestControlEnumCodes`,
  `TestControlCreateReplyRules`, `TestControlStatusReplyRules`,
  `TestControlReplyJSON`.

## Native process identity

### What

`ProcessIdentity` appears in the result of `session.create`, in `turn.started`
and in the session entries of `adapter.start` (both ways), and is kept in
checkpoints. The daemon never reads the process table: it stores and shows the
fields and hands them back to an adapter that reconnects.

| Field | What it is for |
| ----- | -------------- |
| `pid` (1 to 2^32-1) | Find the process: to tell whether it is busy, to signal it, to take it over after a restart |
| `start_key` (opaque, at most 64 characters, compared only for equality) | With `pid`, one process instance, so a reused PID is not mistaken for it. Built from the platform's raw start value and the boot identity (Linux: the start time from `/proc/<pid>/stat` and the boot id; macOS: the start seconds and microseconds from `proc_pidinfo`; Windows: the creation time), and in the same format across adapter versions |
| `started_at_ms` | An approximate start time for people; never used in a decision |
| `container.kind` | `systemd_unit`, `process_group`, `launchd_job`, `windows_job`, `scheduled_task` or `none`: how the process was isolated so it outlives the adapter |
| `container.name` (at most 256 characters; absent for `none`) | The unit, group, job or task name: the daemon checks it differs from its own unit or group and warns the owner when it does not; orphan cleanup takes the whole group; it also serves diagnosis |

### Why

A PID alone is reused; the start key makes the pair unique without the daemon
reading platform-specific process tables, which stay in the adapter.

### Where

- `sdk/go/adapterproto/lifecycle.go` (`ProcessIdentity`, `Container`); test
  `TestProcessIdentityRules`.

## Manifest

### What

`sidera-adapter.json` sits at the root of an adapter package. The daemon reads it
to install an adapter and to admit one. `sdk/go/manifest` parses and validates it.

```json
{
  "manifest_version": 1,
  "id": "example",
  "version": "0.3.1",
  "protocol": {"min": 1, "max": 1},
  "entry": {"program": "bin/sidera-adapter-example", "args": ["--stdio"]},
  "platforms": [{"os": "darwin", "arch": "arm64"}, {"os": "linux", "arch": "amd64"}],
  "requires": [{"program": "example-runtime", "os": ["darwin", "linux"]}],
  "tested": [{"runtime": "example-runtime", "min": "2.1.306", "max": "2.1.308"}],
  "budgets": [{"frontend": "cli", "unit": "chars", "limit": 27000},
              {"frontend": "mcp", "unit": "tokens", "limit": 22500}],
  "ack_markers": {"replaced": [{"literal": "[result saved to file]"}],
                  "truncated": [{"regexp": "\\.\\.\\. \\d+ more lines"}]}
}
```

Optional lists and objects are omitted when empty; an explicit empty one is an
error.

The rules are in [sdk/go/manifest/README.md](../../../sdk/go/manifest/README.md).
In short: `id` and `version` are checked as the adapter reports them in
`initialize`, and must equal them; the protocol range and the adapter's
announcement are checked against each other; `tested` ranges are closed SemVer
ranges; configuration uses a small field vocabulary; budgets map to the output
budget of `recv` for that front end, and a front end without one gets the default.

A native version outside the tested ranges still runs. It produces an
availability condition `version_untested`, shown to the owner and in the status of
the endpoints, but it is not blocked: blocking would disconnect users after every
automatic update of the runtime, while a wrong reconciliation can only end as
"cannot tell".

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
- **The budget is declared per front end** because the runtime shows different
  amounts through different tool paths; the author subtracts the safety margin so
  the daemon uses the limit as given.

### Where

- `sdk/go/manifest/manifest.go`; tests `TestIdentityRules`, `TestEntryRules`,
  `TestPlatformAndRequirementRules`, `TestTestedRangeRules`,
  `TestConfigFieldRules`, `TestBudgetRules`, `TestAckMarkerShape`, `TestParse`,
  `FuzzParse`.

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
4. every invalid example is rejected at the layer it names;
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
   toolchain lists.

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
  `recvpage_test.go`, `oracle_test.go`, `helpers_test.go`.
