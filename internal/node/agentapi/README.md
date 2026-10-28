# agentapi

The Agent interface over the node's local socket or Windows named pipe. It calls
only `engine.AgentService`; credentials are resolved by the engine in the command
that acts on them. Read [the interface reference](../../../docs/references/agent-interface/README.md)
for the wire and the reasons, and [the parent](../README.md) for runtime rules.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Listen`, `Listener` | Open an Agent address through `ipc`, under the node's instance lock. The underlying listener is private. |
| `New`, `Config`, `Server.Serve`, `ServeHTTP`, `Shutdown`, `Close` | Serve the interface, initially accepting only probes. `Server.EnableMethods` opens the endpoint methods after recovery. |
| `NewClient`, `ClientConfig`, `Client.Probe`, `Client.Close` | Present the separate probe credential through `ipc.Dial` and release idle connections. |
| `Client.Me`, `Declare`, `Peers`, `Directory`, `Send`, `Reply`, `Recv`, `Ack`, `Status`, `Control` | Call the endpoint methods; receive returns unchanged page bytes. |
| Request and result types in `methods.go` | The snake-case Agent wire shapes; value vocabularies and validation stay with their existing owners. |
| `ProbeResult` | The successful probe's preset and front end, with `ok: true`. |

## Rules

- `POST /v1/agent/probe` accepts a probe token in `Authorization: Bearer <token>`
  and a strict JSON object with `frontend`. The SDK owns token spelling and the
  front-end vocabulary; the engine owns issuance, expiry, one-use consumption,
  constant-time comparison and the probe's reached state.
- Until `EnableMethods`, every other known Agent method is refused before
  credentials or body are read.
  An adapter can prove tool reachability while delivery recovery is running,
  without opening other operations during that interval.
- Full-service methods construct `engine.ByToken` only after the SDK validates
  credential spelling. The engine resolves the caller in the command that acts.
  This boundary owns the one-recipient limit, JSON presence rules, parsing
  identifiers and safe conversion of seconds; the engine owns message validation,
  receive clamping and acknowledgement safety.
- `AckResult.Items` lists exactly the parts acknowledged and is always a JSON
  array, including an empty array when a forgotten page has no whole messages.
  This Agent result is independent of the adapter protocol result and its bounds.
- Receive responses are the engine page bytes, including their existing newline.
  Neither the server nor the client wraps or re-encodes a page.
- Requests have a 1 MiB limit and use `strictjson`. Responses are JSON with HTML
  escaping disabled. Errors are mapped by `apiwire` once. Logs never contain
  credentials or request bodies, including after a failed response write.
- The server has no global write timeout. Header, request-read and idle timeouts
  belong to `net/http`, and a handler's context ends with the client's connection.
- The client uses no HTTP proxy and follows no redirect: a token goes only to the
  local endpoint whose peer `ipc` checks. It never automatically retries a call,
  and sets no HTTP idempotency header. A missing or failed peer is mapped by
  `apiwire`; a written request with no complete answer is `outcome_unknown`.
  It rejects invalid UTF-8 in request text before JSON encoding or connecting,
  returning `invalid_argument/malformed_request` instead of replacing bytes.

## Caller guarantees

- Create the private socket directory and hold the instance lock before `Listen`,
  as `ipc` requires. `ServeHTTP` is the request handler; production callers serve
  it only through the checked Agent listener.
- Give the client an explicit data directory. Resolve environment variables in
  the calling front end; `ProbeToken` is the probe credential, separate from
  `Token`, the endpoint credential.
- Call `EnableMethods` only after engine recovery finishes. End the engine
  waiters before waiting for HTTP shutdown.
- Close the server and the client when their owner ends. The client gives an
  ordinary call 60 seconds, or a receive the requested wait capped at the
  protocol maximum plus 30 seconds, bounded also by the caller's context.

The contract tests exercise a real Agent socket and engine, probe-only refusal,
strict input and authentication, and an interrupted response without a retry.
The probe and endpoint-dispatcher fuzz targets keep the token well formed but
unissued, so no body may authenticate it or panic.
The client-text fuzz target checks its fixed request containers without a daemon,
so invalid text cannot reach the transport or be silently rewritten.
