# peer

A symmetric JSON-RPC 2.0 peer over a pair of byte streams, with no knowledge of
any protocol built on it. The daemon uses it over a child process's stdio and
third-party adapters use it over their own stdin and stdout, so it must hold up
against a counterpart that is buggy or hostile. Read
[the adapter-protocol reference](../../../docs/references/adapter-protocol/README.md)
for the reasons behind the rules.

The package depends on the standard library and on the strict JSON scanner
of its own module (`sdk/go/strictjson`). Method names, parameter schemas and
the meaning of results belong to the layer above (`adapterproto`); `initialize`,
`ping`, `shutdown` and the rest of that layer's methods are registered by the
owner with `Handle` or `HandleDefault`, never special-cased here. The one method the peer handles
itself is the cancel notification: `$/cancel_request` unless the owner's
`Dialect` names another.

## Entry points

| API | Purpose |
| --- | ------- |
| `New(r io.Reader, w io.WriteCloser, Config)`, `Start()` | Create a peer over the streams, register handlers, then start it. Nothing is read or written before `Start`. |
| `Config.Dialect`, `Dialect` | The few conventions a protocol built on the peer may set differently: the cancel notification (its name, how its params name the request, what `Call` sends), whether a cancelled request is answered, document ambiguity checks, and the codes of the peer's own overload and line-too-long errors. The zero value is the adapter protocol's. `New` fails when `CancelMethod` is not a valid method name (1 to 128 characters) or `CancelParams` does not build a JSON object. |
| `Params`, `DefaultParams()` | The limits. A zero field means "use the default"; a negative field is an error from `New`. |
| `Handle(method, RequestHandler)`, `HandleNotification(method, NotificationHandler)` | Register what the other side may call. Requests for unregistered methods use the default request handler, or get `-32601` when none is registered; unregistered notifications are logged and dropped. |
| `HandleDefault(DefaultRequestHandler)` | Register the handler for requests without a named handler. It receives the method name and raw params and shares the named handlers' execution rules. |
| `Call(ctx, method, params, result)`, `Notify(ctx, method, params)` | Send a request and wait, or send a notification. `params` must marshal to an object, or be nil (sent as `{}`). The method name is 1 to 128 characters. |
| `NoEffect(err)`, `ResultUnknown(err)` | The effect rule: after an error from `Call`, did the request definitely do nothing, or is the outcome unknown? |
| `Done()`, `Err()`, `Close()` | Learn that and why the connection ended; end it. |
| `Error`, `NewError`, `Code*`, `Codes()`, `CodeName` | The error object a handler returns and `Call` reports; the code table, which is the one definition of the JSON-RPC and server-range codes. |
| `RequestID`, `CancelRequestParams` | The one statement of the request id rule, and the params of `$/cancel_request`, for the layers above to reuse instead of restating. |

## Rules

- **Framing.** One JSON object per line, terminated by `\n`. A line longer than
  `MaxLineBytes` is answered with `-32004` and a null id and the connection is
  closed; the reader never skips ahead to the next newline. A stream that ends
  inside a line is treated as an error, never as a message.
- **Classification (default dialect).** A line must be valid UTF-8 (checked
  explicitly, because `encoding/json` silently replaces bad bytes) and valid JSON (`-32700`, the
  connection continues). It must be an object with `"jsonrpc":"2.0"`; batches
  and non-objects are `-32600` and the connection continues. Member names are
  matched exactly. An `id` is an integer of at most 2^53-1 in absolute value or a
  string of 1 to 128 characters (`RequestID`); `null`, fractions and exponents
  are invalid, so two spellings of one number cannot be two different in-flight
  requests. A member name that occurs twice in the frame itself is `-32600`
  with a null id (parsers disagree about which one wins, and the id of such a
  line cannot be trusted); repeats inside `params` and `result` are the business
  of the layer that decodes them.
  The error reply to an invalid line carries the line's id only when the line
  has a string `method` and a valid id; any other malformed line, which may
  have been meant as a response, is answered with a null id, so the error can
  never be taken for the answer to a call of the other side.
- **Document checks are opt-in.** `RejectDuplicateKeysAndInvalidUnicode` rejects
  duplicate member names and unpaired UTF-16 surrogate escapes anywhere in a
  frame, including unknown extensions, params, results and ignored notifications.
  It uses `strictjson.Decode` into raw members, so unknown names and values remain
  accepted. A refusal is `-32600` with the fixed message `duplicate member or
  invalid Unicode`; it neither logs input paths or values nor closes the connection.
  The reply echoes the raw top-level id only when it occurs exactly once and is
  valid, and the frame has exactly one string `method`. Every other rejected
  frame, including a response, gets a null id: a response's id belongs to the
  other side's call-id space and cannot identify a request here. These document
  checks precede method lookup, overload, handlers and cancellation. Invalid
  JSON or UTF-8 stays `-32700`; parameter shape and meaning stay with the layers
  that interpret them. The zero-value dialect keeps the classification and
  response rules above.
- **Request routing.** A named handler takes priority over the optional default
  handler, even when the named handler returns `CodeMethodNotFound`. The default
  runs only on a lookup miss, never for notifications or responses. Without one,
  unknown requests are immediately answered `-32601`, before the in-flight id,
  queue or params checks. With one, they share the named requests' queue,
  concurrency, cancellation and reply path; the read loop only selects a handler
  and never invokes it. `HandleDefault` replaces the earlier default and panics
  on nil. Both request registration APIs work before and after `Start`; handler
  selection is fixed at acceptance, so replacement affects only later selections.
  Raw params are nil when absent or null, otherwise an object or array;
  their schema belongs to the owner.
- **Overload precedes parameter interpretation.** The read loop classifies a
  request, finds its handler and checks its id; if the request queue
  (`MaxPendingRequests`) is full it answers `-32001` (`Dialect.OverloadedCode`) immediately. Parameters
  are not interpreted and no handler runs, so a caller that gets `-32001` may
  retry safely.
- **Handlers.** `MaxConcurrentHandlers` request handlers run at once. Each
  request is answered exactly once, with a result, an `*Error`, or `-32603`
  (any other error, a typed-nil `*Error`, a panic, a result that is not a JSON
  object or does not marshal, or a reply over the line limit; the error text is
  logged, never sent). Under a dialect with `SilentOnCancel` it is answered at
  most once: see Cancellation.
- **Under the zero-value dialect the peer sends only frames that
  `message.schema.json` accepts.** `params` is always an object (`nil` is sent as `{}`; a list or scalar is refused with
  `ErrNotSent`), a result is always an object, a method name has 1 to 128
  characters, and an error has a message of 1 to 1024 characters (an empty one
  becomes the name of the code, or `error`; a longer one is cut), `data` only
  when it is a JSON object (for any code; `WithData` leaves out anything else),
  and a line within `MaxLineBytes`. What the peer cannot know is which
  application codes the protocol defines: a handler returns only those
  (`adapterproto.ApplicationCodes`). A dialect's replacement codes and the shape
  of its `CancelParams` are the responsibility of the protocol that uses the
  dialect: the schema covers neither.
- **Notifications** are handled one at a time in arrival order. When
  `MaxPendingNotifications` is exceeded the connection is closed: dropping one
  could leave the owner with a wrong picture of the other side, and the owner
  resynchronizes after reconnecting.
- **Cancellation.** `$/cancel_request {"request_id": …}` is handled directly by
  the read loop; its params are decoded as strictly as any other (an unknown or
  repeated member, a member in another case or a null cancels nothing). It
  cancels the handler's context; the request is still answered exactly once, with the handler's result or with `-32800`. A request
  cancelled while still queued is answered `-32800` without running its handler.
  When a `Call` context ends, the peer sends the cancel notification, returns
  the context error and drops the answer if it arrives later.
  A `Dialect` changes only what is named in it. `CancelMethod` is the
  notification the read loop handles and `Call` sends (registering a handler for
  it panics, as for `$/cancel_request`). `CancelTarget` reads the cancelled id
  from that notification's params, which are `nil` when the notification has
  none; `false`, or an id that breaks the request id rule, cancels nothing, and
  the default reads `CancelRequestParams` as strictly as above. `CancelParams`
  builds what `Call` sends (an object, like any params; `New` calls it once and
  refuses a function whose result does not marshal to an object). With `SilentOnCancel`
  the answer to a request is dropped when a cancellation found the request in
  flight before the answer was queued, whether the handler honoured it,
  ignored it or never started; the id is released as always. A cancellation
  that arrives after the answer was queued finds nothing in flight and changes
  nothing, so that answer is still sent: at most once, never twice.
  A frame with an id and the cancel method name is a request: it follows request
  routing, including the optional default handler, and does not cancel anything.
- **Dialect codes.** `OverloadedCode` and `LineTooLongCode` replace `-32001`
  and `-32004` in what the peer sends on its own, with the same message and the
  same behaviour (the overload refusal is still decided before anything runs,
  the line-too-long reply still carries a null id and is followed by the close).
  Document checks and cancellation are the other dialect conventions described
  above.
- **Ids in flight.** A request whose id is already in flight gets `-32600`. The
  id is released before its answer is queued, so the other side can reuse it as
  soon as it has read the answer.
- **Write timeout.** Every line is written with one `Write`. A timer guards each
  write and closes the transport if it does not finish within `WriteTimeout`,
  which unblocks the stuck `Write`. File deadlines are not used because
  anonymous pipes on Windows do not support them.
- **Outbound backpressure.** `Call` and `Notify` never block on the outbound
  queue: when `MaxOutbound` lines are queued they return `ErrOverloaded` and
  write nothing. Answers and the error replies the read loop owes do wait for
  room; a flooding counterpart therefore stalls the read loop, bounded by
  `WriteTimeout`.
- **Message size on the way out.** A request or notification longer than
  `MaxLineBytes` is refused with `ErrMessageTooLarge` (the other side is assumed
  to enforce the same limit); a handler result or error that long is answered
  `-32603`.
- **Responses.** A response that matches no pending call (it timed out, or the
  id was never sent) is dropped and logged. A response with the right id but an
  invalid body fails the call with `ErrInvalidResponse`.
- **Close.** `Close` is idempotent. Pending calls end with `ErrClosed`, handler
  contexts are cancelled, lines not yet written are discarded and the transport
  is closed. `Err()` reports the cause: `ErrClosed` (local `Close`),
  `ErrRemoteClosed`, `ErrLineTooLong`, `ErrNotificationOverflow`,
  `ErrWriteTimeout`, or a wrapped read or write error.
- **Start and Close may race.** `Start` and `Close` are safe to call at the same
  time from different goroutines, in either order; an owner that shuts a peer
  down while another goroutine is still starting it needs no lock of its own.
  Both take the peer's lock, so one of them is first. If `Close` is first,
  `Start` launches nothing (it has no result to report that in), and so does any
  `Start` after `Close`. If `Start` is first, `Close` waits for the reader and
  writer it launched, exactly as when the two are not concurrent. A second
  `Start` also does nothing.
- **Logging.** Through the `*slog.Logger` in `Config`. Message bodies, params
  and results are never logged; method names are logged clipped to 64 bytes.

## What the caller must guarantee

- Register handlers before `Start` if the first inbound message must find them.
- Handlers must return when their context is cancelled; `Close` does not wait
  for them (it waits for the peer's own reader and writer goroutines).
- If the reader is not an `io.Closer`, `Close` cannot interrupt a blocked read
  and does not wait for it; the reader goroutine ends when the reader returns.
  Child-process pipes and `io.Pipe` readers are closers.
- After an error, apply the effect rule rather than guessing: retry only on
  `NoEffect`, reconcile or retry idempotently on `ResultUnknown`. The rule is
  defined by the adapter protocol's code table (`Codes`), also under a dialect,
  and a replacement code is not in that table. A dialect used only for
  answering requests never calls `Call` and so does not use the rule.
- A `Dialect.CancelTarget` runs on the read loop on input from the other side,
  and the read loop does not recover: it must be fast, must not block and must
  not panic.
- Use one peer per connection. `Close` closes both streams.
