# Agent interface

The local HTTP interface through which an Agent tool reaches its node. Transport
and peer checks are [local IPC](../local-ipc/README.md)'s; the interface is
[`internal/node/agentapi`](../../../internal/node/agentapi/README.md), and its only
engine dependency is `engine.AgentService`.

## What

The probe-only service listens on `<SIDERA_HOME>/run/agent.sock` on Unix or the
Agent named pipe on Windows. It accepts `POST /v1/agent/probe`, with
`Content-Type: application/json`, `Authorization: Bearer <probe token>` and this
body:

```json
{"frontend":"cli"}
```

`frontend` is required and is `cli` or `mcp`. A successful response is:

```json
{"ok":true,"preset":"default","frontend":"cli"}
```

The preset and front end are those bound to the token. The engine records that
front end as reached and consumes the token before answering. The adapter's
report that the tool ran alone does not establish reachability: the token has to
be presented to this interface. It proves connectivity, not the honesty of an
adapter that knows the token.

Probe tokens contain 32 random bytes in unpadded base64url. `Token.Validate` owns
their one canonical spelling. They are bound to an instance, preset and front
end, kept only in memory, valid for five minutes from issuance on the injected
clock, and consumed by the first successful probe. A restart or a new probe round
invalidates the earlier tokens. A mismatched front end does not consume a token.
The engine compares candidates in constant time. Tokens are never logged.

The environment names issued by the engine are `SIDERA_HOME` (an absolute data
directory), `SIDERA_PROBE_TOKEN` (a probe token) and `SIDERA_TOKEN` (the distinct
credential of a session endpoint). Endpoint tokens are stored with the endpoint
so the node can attach the session again; closing the endpoint revokes them.

While only probes are accepted, `me`, `declare`, `peers`, `directory`, `send`,
`reply`, `recv`, `ack`, `status` and `control` answer `503 unavailable/probe_only`
before credentials or body are examined. An unknown path answers
`404 not_found/method_unknown`; a known method called without POST answers 405
`invalid_argument/method_not_allowed`, with `Allow: POST`.

Requests are at most 1 MiB. JSON is strict: unknown or repeated members, `null`,
wrong member case, missing required members, wrong types and trailing values are
`400 invalid_argument/malformed_request`, with the fault's path when known. An
oversized request is `invalid_argument/request_too_large`. Errors have this shape:

```json
{"error":{"category":"unauthenticated","code":"token_unknown","message":"the token is not known to this node or has been revoked"}}
```

`apiwire` owns categories, codes, HTTP statuses and command exit codes. A missing
credential is `token_missing`, a noncanonical credential `token_malformed`, an
unknown credential `token_unknown`, a consumed probe `probe_token_used`, an
expired probe `probe_token_expired`; these are `unauthenticated`. A mismatched
front end is `invalid_argument/probe_frontend_mismatch`. Human messages are
diagnostic, not stable identifiers, and carry no credentials or message bodies.

The server reads headers within 10 seconds and the request within 30 seconds,
keeps an idle connection for 60 seconds, and limits headers to 8 KiB. It has no
global write timeout; the handler follows the request context. Shutdown stops
taking requests, waits within the owner's grace, then closes remaining requests.

The client connects only through `ipc.Dial`, with the data directory made absolute
and clean. It uses no proxy, follows no redirect and never automatically retries
an Agent request. Ordinary calls have a 60-second deadline, also bounded by the
caller's context. If a request was written and its response was lost or cut, the
client reports `outcome_unknown` (exit code 10): the caller cannot assume that the
probe was not consumed. No operation identifier means no advice to resend.

Sandboxes must deny reading and writing the data directory and allow only
connecting to the Agent socket; they must not write its directory or connect to
the management interface. The reasons and platform-specific limitations are in
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

## Where

`agentapi.go` owns routing, probe-only refusal, Bearer syntax, strict request
reading and JSON responses; `client.go` owns checked dialing, the probe call and
the distinction between failure to connect and an unknown outcome. The engine's
probe state and token rules are described in [the engine reference](../engine/README.md).
