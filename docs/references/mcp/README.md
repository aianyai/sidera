# MCP

The stdio boundary supports precisely `2025-06-18` and `2026-07-28`. It reuses
[the peer transport](../../../sdk/go/peer/README.md), including bounded queues
and writes. It never sends server requests or progress notifications. Standard
output contains MCP messages only; diagnostics go to standard error.

## Versions and lifecycle

Legacy clients send `initialize` with `protocolVersion`, `capabilities` and
`clientInfo`. The response selects `2025-06-18`, advertises only `tools: {}` and
identifies the server as `sidera` with the binary's version. The subsequent
`notifications/initialized` is ignored. `ping` returns `{}` at any time.

Modern requests carry `params._meta.io.modelcontextprotocol/protocolVersion`
and `params._meta.io.modelcontextprotocol/clientCapabilities`. Only `2026-07-28`
is accepted there. An unsupported version returns `-32022` with `supported` and
`requested` in `data`; missing or malformed required metadata returns `-32602`.
This selection is per request, so a modern request can share a legacy process.
Without modern metadata, requests other than `initialize` and `ping` require
legacy initialization.

`server/discover` advertises the modern version and tools capability, with
`resultType: "complete"`, `ttlMs: 3600000`, `cacheScope: "public"`, and server
identity in `_meta.io.modelcontextprotocol/serverInfo`. The public cache is safe
because discovery contains no endpoint data. Unknown methods return `-32601`.

Only fields the server uses are interpreted. Unknown input members, including
extra metadata, are ignored; repeated keys at any depth are refused to prevent
ambiguous interpretations. Output follows the selected version's schema.

`notifications/cancelled` reads `requestId`, ignores invalid or unknown targets
and cancels the matching handler without answering it. The peer supplies this
policy; this package does not implement another cancellation engine. Queue
overload uses `-32603`; an oversized input line uses `-32600` with null id and
then closes. End of input cancels in-flight requests and normally returns success;
a broken transport is a process failure. A caller's ended context closes both
streams.

When requested by the caller, the first compliant request starts one independent
Agent probe. It uses the client's separate probe credential, reports only on
standard error, and does not delay or alter an MCP response.

## Sources and implementation

The fixed revisions' [legacy schema](https://modelcontextprotocol.io/specification/2025-06-18/schema)
and [modern schema](https://modelcontextprotocol.io/specification/2026-07-28/schema)
define the exchanged shapes. Their
[legacy stdio rules](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports)
and [modern stdio binding](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/stdio)
define stream ownership, framing and cancellation. The existing peer already
provides those mechanisms, so another MCP SDK would duplicate the transport and
add a second place to enforce it.

`cmd/sidera/internal/mcp` owns this boundary. It borrows the typed
[Agent client](../agent-interface/README.md); authentication, message safety,
receive scheduling and acknowledgement validation stay in that interface and
the engine.
