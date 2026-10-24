# peer

A symmetric JSON-RPC 2.0 peer over a pair of byte streams, with no knowledge of
any protocol built on it. The daemon uses it over a child process's stdio and
third-party adapters use it over their own stdin and stdout, so it must hold up
against a counterpart that is buggy or hostile. Read
[the adapter-protocol reference](../../../docs/references/adapter-protocol/README.md)
for the reasons behind the rules.

The package depends on the standard library and on the strict JSON scanner
of its own module (`sdk/go/internal/strictjson`), which nothing outside the SDK
can import. Method names, parameter
schemas and the meaning of results belong to the layer above (`adapterproto`);
`initialize`, `ping`, `shutdown` and the rest of that layer's methods are
registered by the owner with `Handle`, never special-cased here. The one
method the peer handles itself is the `$/cancel_request` notification.

## Entry points

| API | Purpose |
| --- | ------- |
| `New(r io.Reader, w io.WriteCloser, Config)`, `Start()` | Create a peer over the streams, register handlers, then start it. Nothing is read or written before `Start`. |
| `Params`, `DefaultParams()` | The limits. A zero field means "use the default"; a negative field is an error from `New`. |
| `Handle(method, RequestHandler)`, `HandleNotification(method, NotificationHandler)` | Register what the other side may call. Requests for unregistered methods get `-32601`; unregistered notifications are logged and dropped. |
| `Call(ctx, method, params, result)`, `Notify(ctx, method, params)` | Send a request and wait, or send a notification. `params` must marshal to an object or an array, or be nil. |
| `NoEffect(err)`, `ResultUnknown(err)` | The effect rule: after an error from `Call`, did the request definitely do nothing, or is the outcome unknown? |
| `Done()`, `Err()`, `Close()` | Learn that and why the connection ended; end it. |
| `Error`, `NewError`, `Code*`, `Codes()`, `CodeName` | The error object a handler returns and `Call` reports; the code table, which is the one definition of the JSON-RPC and server-range codes. |
| `RequestID`, `CancelRequestParams` | The one statement of the request id rule, and the params of `$/cancel_request`, for the layers above to reuse instead of restating. |

## Rules

- **Framing.** One JSON object per line, terminated by `\n`. A line longer than
  `MaxLineBytes` is answered with `-32004` and a null id and the connection is
  closed; the reader never skips ahead to the next newline. A stream that ends
  inside a line is treated as an error, never as a message.
- **Classification.** A line must be valid UTF-8 (checked explicitly, because
  `encoding/json` silently replaces bad bytes) and valid JSON (`-32700`, the
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
  has a `method` and a valid id; any other malformed line, which may have been
  meant as a response, is answered with a null id, so the error can never be
  taken for the answer to a call of the other side.
- **Overload is decided first.** The read loop classifies a request, finds its
  handler and checks its id; if the request queue (`MaxPendingRequests`) is
  full it answers `-32001` immediately. Parameters are not examined and no
  handler runs, so a caller that gets `-32001` may retry safely.
- **Handlers.** `MaxConcurrentHandlers` request handlers run at once. Each
  request is answered exactly once, with a result, an `*Error`, or `-32603`
  (any other error or a panic; the error text is logged, never sent).
- **Notifications** are handled one at a time in arrival order. When
  `MaxPendingNotifications` is exceeded the connection is closed: dropping one
  could leave the owner with a wrong picture of the other side, and the owner
  resynchronizes after reconnecting.
- **Cancellation.** `$/cancel_request {"request_id": …}` is handled directly by
  the read loop; its params are decoded as strictly as any other (an unknown or
  repeated member, a member in another case or a null cancels nothing). It cancels the handler's context; the request is still
  answered exactly once, with the handler's result or with `-32800`. A request
  cancelled while still queued is answered `-32800` without running its handler.
  When a `Call` context ends, the peer sends the cancel notification, returns
  the context error and drops the answer if it arrives later.
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
  to enforce the same limit); a handler result that long is answered `-32603`.
- **Responses.** A response that matches no pending call (it timed out, or the
  id was never sent) is dropped and logged. A response with the right id but an
  invalid body fails the call with `ErrInvalidResponse`.
- **Close.** `Close` is idempotent. Pending calls end with `ErrClosed`, handler
  contexts are cancelled, lines not yet written are discarded and the transport
  is closed. `Err()` reports the cause: `ErrClosed` (local `Close`),
  `ErrRemoteClosed`, `ErrLineTooLong`, `ErrNotificationOverflow`,
  `ErrWriteTimeout`, or a wrapped read or write error.
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
  `NoEffect`, reconcile or retry idempotently on `ResultUnknown`.
- Use one peer per connection. `Close` closes both streams.
