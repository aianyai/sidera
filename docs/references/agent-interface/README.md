# Agent interface

The MCP stdio boundary is described in [the MCP reference](../mcp/README.md).

The local HTTP interface through which an Agent tool reaches its node. Transport
and peer checks belong to [local IPC](../local-ipc/README.md). The implementation
is [`internal/node/agentapi`](../../../internal/node/agentapi/README.md), whose
only engine dependency is `engine.AgentService`.

## What

### Transport and credentials

The interface uses HTTP/1.1 on `<SIDERA_HOME>/run/agent.sock` on Unix or the Agent
named pipe on Windows. Methods are `POST /v1/agent/<method>`, with
`Content-Type: application/json` and `Authorization: Bearer <token>`. The endpoint
is resolved from the token inside the engine command that acts for it, so a
credential revoked between requests never retains authority.

The environment names are `SIDERA_HOME` (an absolute data directory),
`SIDERA_TOKEN` (the credential of a session endpoint), and `SIDERA_PROBE_TOKEN`
(the separate credential for a tool probe). Endpoint tokens are stored with their
endpoints so the node can attach a session again; closing a session revokes its
token. Probe tokens can call only `probe`, and endpoint tokens cannot call it.

Both credentials contain 32 random bytes in unpadded base64url. The HTTP boundary
calls `adapterproto.Token.Validate` before constructing the caller; the engine
owns lookup, constant-time comparison, expiry and revocation. Normal request
handling does not proactively log tokens. Diagnostic producers
must keep credentials and request bodies out of error text and panic values;
internal causes and standard HTTP diagnostics are logged whole, as described in
[the shared local HTTP boundary](../local-http/README.md). Only open session
endpoints can act through this interface; adapters
read and write direct endpoints through the adapter protocol.

A new server accepts only probes. Every other known method answers
`503 unavailable/probe_only` before credentials or body are examined. After the
engine finishes recovery, the composition root calls `Server.EnableMethods`.
That transition is one-way and safe to repeat. The engine still owns its phase
checks, including shutdown and clock rollback.

An unknown path answers `404 not_found/method_unknown`; a known method called
without POST answers `405 invalid_argument/method_not_allowed` with `Allow: POST`.
Requests are at most 1 MiB. JSON is strict: unknown or repeated members, `null`,
wrong member case, missing required members, wrong types, invalid UTF-8 and
trailing values are `400 invalid_argument/malformed_request`. An oversized request
is `invalid_argument/request_too_large`. Optional members are omitted instead of
sent as an empty string, zero, list or object, except where a legitimate zero is
explicitly accepted below. A declaration's pointer fields distinguish absence
from `false` or an empty `accept_from` list.

### Methods

Every method except `probe` uses the endpoint credential. Identifiers and
addresses are strings; addresses use `<node>/<endpoint>`. Times are UTC and
request durations are integer seconds.

| Method | Request | Successful result |
| --- | --- | --- |
| `me` | `{}` | The endpoint's `address`, `node`, `kind`, `display_name`, `declaration` and `frontends` |
| `declare` | A declaration patch | `{"declaration":{...}}` after applying the patch |
| `peers` | `{}` | `{"nodes":[{"node":"...","self":true}]}` |
| `directory` | Optional `node`, `cursor`, `limit` | `{"endpoints":[...],"next_cursor":"..."}` |
| `send` | Required `to` array and `body`; optional message fields below | `{"message_id":"...","to":["..."],"replayed":false}` |
| `reply` | Required `message_id`, `body`; optional message fields below | The same shape as `send` |
| `recv` | Required `frontend`; optional receive fields below | The page bytes, including their newline |
| `ack` | Required `lease`; optional `items` | `{"items":[PartAck...]}` |
| `status` | `{"message_id":"..."}` | `adapterproto.MessageStatusResult` |
| `control` | Required `to` string, `signal`; optional control fields below | `{"message_id":"...","to":"...","replayed":false}` |
| `probe` | `{"frontend":"cli"}` or `{"frontend":"mcp"}` | `{"ok":true,"preset":"...","frontend":"cli"}` |

**Identity and declaration.** `me.declaration` has the five snake-case fields
`paused`, `refuse_multi_recipient`, `restrict_senders`, `accept_from` (an array of
addresses), and `hidden_from_directory`. The `declare` request is a patch with
those same fields directly in the request object, not inside `declaration`.
Omitted fields stay unchanged; `accept_from` replaces its entire list, and an
explicit empty list clears it. An empty patch is `invalid_argument/empty_patch`.
The engine owns declaration validity and its sender-list bound.

`me.frontends` contains only `cli` and/or `mcp` actually declared by the adapter;
an instance not yet admitted has an empty object. Each value is
`{"recv_wait_max_s":20,"budget":{"unit":"chars","limit":27000}}`, with the
adapter's limits and their engine-owned defaults. Output budget unit names are
shared through `apiwire.BudgetUnitName`: `chars` and `tokens`; an unknown unit is
an internal error rather than a fabricated usable limit. `peers.self` compares
each visible node with this endpoint's node. With the current local transport,
the only visible node is the node itself.

**Directory.** `limit` is 1 through 256, default 100. Entries contain `address`,
`kind`, `display_name`, `description` and `accepts` (action names such as
`message.send` and `session.create`). Only visible open endpoints appear, in
endpoint ID order. Creating, closed and system endpoints do not appear. An open
endpoint whose adapter has not been admitted still appears; only signals known
for its kind are advertised, and interrupt requires a confirming admission.

The target re-reads and authorizes the live view on every page. The opaque cursor
is a position, not permission; it binds the full source address and the daemon
instance. A changed source, modified cursor or restart is
`invalid_argument/cursor_invalid`. `next_cursor` is an empty string at the end.
Queries use the engine's transport seam, including for this node, and have a
five-second default budget after successful inbox entry, covering queuing,
transport and the result. Timeout and an unreachable target are
`unavailable/directory_unavailable`. See [the engine reference](../engine/README.md)
for authorization, cancellation and the outbound worker's FIFO limitation.

**Messages.** `send.to` must contain exactly one address. The HTTP boundary alone
owns `invalid_argument/single_recipient_only`; other front ends pass the list
through. The engine retains a multi-recipient contract. `body` must be non-empty
and valid UTF-8. `content_type` defaults to `text/plain`; other optional send
fields are `thread`, `disclose_recipients`, `accept_before_s` and
`idempotency_key`. The deadline's bound is `delivery.MaxAcceptBefore`, checked
before converting seconds to a duration so multiplication cannot overflow.

`reply` accepts optional `content_type`, `thread` and `idempotency_key`. Its
recipient and reply evidence come from the message the endpoint received; the
caller cannot choose another recipient. The engine owns body, envelope,
capacity, reply-permission and idempotency validation. A successful send or reply
means the source accepted it durably. The target's later intake decision is read
through `status` and channel feedback.

An explicit idempotency key reused with identical content returns the original
message and `replayed:true`; another content under the key is
`conflict/idempotency_conflict`. The client never generates a key or retries a
request automatically. `replayed` says only that the key replayed a request.

**Control.** `to` is a single address. `signal` is the existing
`adapterproto.ControlSignal`; the engine calls the protocol's `Control.Validate`
for its field constraints. `preset` is required exactly for `session.create`.
A creation can omit its initial `body` altogether; a present body must be
non-empty. Only a creation accepts `body` and `content_type`. Other optional
fields are `idempotency_key` and `accept_before_s`. Acceptance returns the three
fields in the method table, with no control reply. Poll `status.control_reply`
for the eventual answer; the response reuses `adapterproto.MessageStatusResult`
and `ControlReply`, including a created session endpoint when one was created.

**Receiving.** `frontend` is required and is `cli` or `mcp`. Optional `wait_s` is a
non-negative integer, at most 86400 to bound conversion; an explicit zero means
no wait. The engine alone clamps it to the selected front end's declared limit.
Optional `msg` and `part` go together, using the page's `next` pointer and
`adapterproto.PageNext` validation. A part request cannot include `wait_s`, even
zero, and never waits. Optional `ack_lease` asks the engine to acknowledge the
entire earlier page before receiving this one. Failure of that acknowledgement
is logged and does not block the receive. The HTTP boundary passes its string
to the engine without validating lease syntax: a malformed earlier lease is
also a failed acknowledgement and must not prevent the current receive. The
independent `ack` method still requires a valid lease identifier.

The response is exactly the bytes of `handoff.Pack` or `handoff.Part`, with no
wrapper, re-encoding or added newline. The client also returns those exact bytes.
An empty queue returns a valid empty page with a new lease. Concurrent receives,
cancellation, possible duplicates, the second authorization check, and writing a
lease before handing out belong to the engine. One endpoint has at most one
waiting receive; a newer receive ends the earlier one with an empty page. If a
connection ends after a page was leased, the next receive can return the message
marked possibly duplicate. A system endpoint sender identifies this node's own
channel feedback; other endpoints and nodes cannot send as that endpoint.

**Acknowledgements.** Omit `items` to acknowledge the whole page, or provide 1
through `adapterproto.MaxAckItems` (1024) `PartRef` values, each with `message_id`
and `part`. An explicit empty `items` list is invalid. `PartAck` and `ItemAck`
are the protocol's existing types and values. A lease not held by the caller's
endpoint is `not_found/lease_unknown`; another endpoint's token cannot
acknowledge it.

The engine remembers a page's parts in memory for `pageMemory`. After a daemon
restart or that memory expires, whole-page acknowledgement confirms only
messages known to fit whole on one page. It does not confirm any part of a
message that was split; the caller must explicitly name the parts it saw.
The successful result always lists exactly the acknowledged parts as `items`,
including `200 {"items":[]}` when none can be acknowledged. Unacknowledged
messages are handed out again later marked as possible duplicates; they are never
lost. Acknowledging unseen parts would falsely report a body delivered.

**Probing.** A probe token is bound to an instance, preset and front end, valid
for five minutes on the injected clock and consumed by its first successful
presentation. A mismatch does not consume it. A restart or new probe round
invalidates earlier tokens. The adapter reporting that a tool ran is not enough:
the token must reach this interface. It proves connectivity, not the honesty of
an adapter that knows the token.

### Errors, deadlines and clients

Errors have this shape:

```json
{"error":{"category":"unauthenticated","code":"token_unknown","message":"the token is not known to this node or has been revoked"}}
```

`apiwire` owns categories, HTTP statuses, exit codes and the shared error
mapping. The Agent facade additionally owns its boundary-specific codes and
messages: `probe_only`, `token_missing`, `token_malformed` and
`single_recipient_only`. Diagnostic messages are not stable identifiers; their
producers must keep request bodies, credentials and private keys out of them.

| Category | HTTP | Command exit | Representative codes |
| --- | --- | --- | --- |
| `invalid_argument` | 400 | 3 | `malformed_request`, `request_too_large`, `single_recipient_only`, `empty_patch`, `cursor_invalid`, `probe_frontend_mismatch`, `socket_path_too_long` |
| `unauthenticated` | 401 | 4 | `token_missing`, `token_malformed`, `token_unknown`, `probe_token_used`, `probe_token_expired` |
| `not_found` | 404 | 5 | `method_unknown`, `message_unknown`, `lease_unknown`, `endpoint_unknown`, `instance_unknown` |
| `conflict` | 409 | 6 | `idempotency_conflict`, `daemon_already_running`, `data_directory_already_initialized` |
| `refused` | 422 | 7 | Delivery refusals such as `too_large`, `endpoint_kind`; `data_directory_unusable` |
| `backpressure` | 429 | 8 | `node_queue_full`, `storage_full`, `storage_unavailable`, `clock_rollback`, `loop_busy` |
| `unavailable` | 503 | 9 | `daemon_not_running`, `peer_check_failed`, `recovering`, `probe_only`, `shutting_down`, `directory_unavailable`, `request_canceled`, `request_timeout`, `data_directory_not_initialized`, `daemon_storage_fatal` |
| `internal` | 500 | 1 | `internal`, `engine_not_stopped`, `instance_lock_not_held` |
| `outcome_unknown` | 500 | 10 | `outcome_unknown`, `storage_fatal` |

The command exits above are the shared category defaults. Local `init` and
foreground `daemon` use their lifecycle exits instead: 0 for normal completion,
1 for ordinary failure and 74 for fatal storage failure, including with `--json`.
Their typed failures share this mapping; they are not Agent endpoint methods.
The [daemon reference](../daemon/README.md#errors-and-command-output) describes
the codes and actionable messages. `daemon_storage_fatal` means the daemon
stopped and must recover on restart; an engine request with an uncertain outcome
retains `outcome_unknown/storage_fatal`.

An emitted stable code is never renamed, removed or reused for another meaning.
Directory timeout is a known unavailable result, not an unknown message outcome.
Existing state refusals keep their codes; `loop_busy` is the inbox submission
wait. Success, including a received message or an empty receive page, exits zero.
Command-line usage errors always write diagnostics to stderr and exit with code 2,
including with `--json`. They do not produce an API error body on stdout.
The CLI preserves every explicitly supplied Agent flag, including empty strings,
zero and false, through `Client.Call`; the interface's existing presence rules
decide validity. Command syntax, output and owner-only management commands
(including verified private-file export) are described in the
[command-line reference](../command-line/README.md).

The [shared local HTTP boundary](../local-http/README.md) owns routing, body
limits, response encoding, logging and shutdown. The server reads headers within
10 seconds and requests within 30 seconds, keeps
an idle connection for 60 seconds, and limits headers to 8 KiB. It has no global
write timeout. Shutdown stops accepting requests, waits within the owner's
grace, then closes remaining requests. The owner ends engine waiters before
waiting on HTTP shutdown.

The client connects only through `ipc.Dial`, with an absolute, clean data
directory. It uses no proxy, follows no redirect and sets no HTTP idempotency
header. Before encoding or connecting, it rejects invalid UTF-8 in request text
with `invalid_argument/malformed_request`. Go's JSON encoder would otherwise
replace invalid bytes, changing message content or merging distinct idempotency
keys. Ordinary calls have a 60-second deadline; receives have
`min(requested wait_s, manifest.MaxRecvWaitSeconds) + 30` seconds, bounded also by
the caller's context. The protocol's maximum is 120 seconds. A written request
without a complete, valid answer is `outcome_unknown`; the caller cannot assume
it had no effect. If the request was not written, an ended caller context maps
to `unavailable/request_canceled` or `unavailable/request_timeout` through
`apiwire`, with fixed messages describing only the ended context. Existing typed
errors take precedence over that mapping. No operation identifier means no
advice to resend.

The Go client exposes `Call(ctx, method, in any) (any, error)` for front ends
that already know which JSON members were supplied. It accepts `json.RawMessage`
or ordinary structs, string-key maps, slices, pointers, interfaces and JSON
scalars through the shared safe encoder; arbitrary custom marshalers are
unsupported. Raw documents receive document-level checks, without decoding and
re-encoding through method request types. For receive deadlines, the client
reads the original object's exact `wait_s` as int64 after safe encoding. Missing
or unreadable values use zero for the budget; server validation remains
authoritative. Unknown method names return `not_found/method_unknown` locally,
before building a URL. `Call` and typed methods share credential selection,
deadlines and strict successful-result decoding.

Struct encoding follows its JSON tags. Existing typed optional scalar fields
still use zero for omission, including an empty typed control body. Callers
that need explicit emptiness use raw JSON, a map with that member, or their own
struct fields and tags that preserve it. For `ack`, the typed `Ack` method and
`Call` with `AckRequest` or `*AckRequest` share a private wire shape where nil
`Items` omits the member and a non-nil empty slice sends `[]`; the server rejects
the latter without confirming any messages. Otherwise, omitting an explicitly
empty list would silently acknowledge the whole page. The server's request DTOs
and strict presence rules are unchanged.

`Call` returns the following concrete Go result types after complete strict
decoding. `recv` is validated as a page but returned as the original `[]byte`,
including its newline. Invalid successful responses have `outcome_unknown`.

| Method | Concrete Go result |
| --- | --- |
| `me` | `agentapi.MeResult` |
| `declare` | `agentapi.DeclareResult` |
| `peers` | `agentapi.PeersResult` |
| `directory` | `agentapi.DirectoryResult` |
| `send` | `agentapi.SendResult` |
| `reply` | `agentapi.SendResult` |
| `recv` | `[]byte` |
| `ack` | `agentapi.AckResult` |
| `status` | `adapterproto.MessageStatusResult` |
| `control` | `agentapi.ControlResult` |
| `probe` | `agentapi.ProbeResult` |

The default undeclared wait limit is eight seconds. A shell tool with a
ten-second foreground window may end its process at the deadline or let it
continue in the background while returning a process identifier first. In either
case a longer receive can leave the page outside the model's immediate result,
so the default leaves time for connecting and printing. An adapter can declare a
larger limit for a front end with a larger usable foreground window; `me`
reports the actual limits instead of tools hard-coding them.

Sandboxes must deny reading and writing the data directory and allow only
connecting to the Agent socket; they must not write its directory or connect to
the management interface. The reasons and platform-specific limits are in
[the local IPC reference](../local-ipc/README.md#sandboxes-and-the-rest-of-the-data-directory).

## Why

HTTP over the checked local listener reuses `net/http` for framing, request
contexts, size limits and graceful shutdown, and `ipc` for platform-specific
identity checks. A separate web framework or retry library adds no needed
capability. The client's `Transport.DialContext` uses that same IPC boundary;
`httptrace.WroteRequest` distinguishes a failed connection from a response lost
after writing. POST requests carry no HTTP idempotency header, so the transport
cannot treat an Agent call as an idempotent request to retry. See the
[standard-library transport documentation](https://pkg.go.dev/net/http#Transport)
and [request trace documentation](https://pkg.go.dev/net/http/httptrace#ClientTrace).

Value vocabularies, body and envelope rules, declarations, authorization,
receive scheduling and acknowledgement safety stay with their existing owners.
The HTTP boundary adds only its wire shape, presence rules and the one-recipient
limit. Returning a receive page unchanged keeps the bytes checked before an
adapter acknowledges on the model's behalf identical across tool front ends.

## Where

`agentapi.go` delegates HTTP transport and response mechanics to `localhttp`,
while retaining enabling, Bearer syntax and the ordering of body reads.
`methods.go` owns endpoint request/result types and conversion to the
engine service. `client.go` owns the shared method dispatch, credential selection,
private acknowledgement encoding, receive deadlines and result decoding, using
`localhttp` for safe encoding, checked dialing and transport outcomes.
`client_methods.go` supplies the typed endpoint wrappers.
