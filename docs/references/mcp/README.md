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
because discovery contains no endpoint data. Without modern version metadata,
`server/discover` follows the process's legacy initialization state: before
`initialize` it returns `-32602`; after legacy initialization it is an unknown
legacy method and returns `-32601`. A modern discovery request does not initialize
the legacy process. Other unknown methods return `-32601`.

Only fields the server uses are interpreted. Unknown input members, including
extra metadata, are ignored. The peer refuses repeated top-level message
members; MCP refuses repeated members at any depth within `params`, preventing
ambiguous interpretations there. Output follows the selected version's schema.

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

## Agent tools

`tools/list` returns the fixed tools `me`, `declare`, `peers`, `directory`, `send`,
`reply`, `recv`, `ack`, `status`, `control` and `probe`, without pagination. The
modern result adds `resultType`, cache hints and server metadata as discovery
does. Read-only hints identify identity and directory queries and status; `probe`
is explicitly mutable. `declare` and `ack` carry idempotent hints. Hints convey
no authorization and never replace the Agent interface's checks.

The catalogue's JSON schemas use common object, array, scalar, required, enum and
numeric-bound keywords without `$ref` or `$schema`. They are both the advertised
input description and the validator's source of truth. Extra input properties
are ignored even though `additionalProperties: false` guides models to send only
known arguments. The Agent interface alone enforces its single-recipient limit.

`tools/call` returns one text item and explicit `isError`. It adds modern result
metadata only for a modern request. JSON text uses the same encoding as the Agent
command output, including its final newline. A receive is the Agent page's exact
bytes: there is no wrapper or re-encoding. No `outputSchema` or
`structuredContent` duplicates the page or consumes its output budget twice.

Tool execution and schema validation failures are `isError: true` results with
the Agent JSON error body as text. An unknown tool is instead protocol error
`-32602`. An unreachable node is a tool error with category `unavailable` and
code `daemon_not_running`. Requests use the handler context, including for
long-polling receive; the receive front end is always `mcp`. The borrowed Agent
client owns credential choice, checked local transport and outcome uncertainty.
Known arguments are forwarded with explicit JSON member presence through
`Client.Call`; an empty list, empty string or null is never silently converted
to an omitted member. The schema rejects null in used fields, and the Agent
interface validates the remaining present values. This prevents an explicit
empty acknowledgement list from becoming whole-page acknowledgement.

## Manual client check

Install and authenticate the local Claude Code and Codex CLIs first. From the
repository root, run this opt-in check once against the completed executable:

```sh
SIDERA_REALCLIENT=1 go test -tags realclient -run '^TestRealClientsAcceptMCPResponses$' -count=1 -timeout 15m ./cmd/sidera/internal/mcp
```

The check builds `sidera`, creates a temporary Agent socket backed by the test
node, and starts each real client in a separate empty directory. It uses private,
one-process MCP settings and leaves the user's persistent MCP configuration
untouched. Claude uses `--mcp-config` and `--strict-mcp-config`; Codex uses
`exec --ignore-user-config`, `--ephemeral` and inline `-c` settings while retaining
its existing authentication. Its inline
`mcp_servers.sidera.tools.recv.approval_mode="approve"` setting adds an explicit
approval override only for the fixture's `recv` tool in that process; the server
default remains `auto` and the sandbox remains read-only. `recv` has no read-only
hint because it can change receive state. These sessions consume the user's normal client
quota. The `realclient` build tag and explicit environment flag exclude the check
from ordinary tests and CI. On Unix, the temporary directory must fit the local
socket path limit; `TMPDIR` can name a shorter private directory if needed.

Both client subtests must pass: each client must accept the tool catalogue, call
`me`, receive the fixture's complete marker body, and present the typed error from
`status` with exactly `{"message_id":""}`. The explicit empty string passes the
MCP input schema and reaches the Agent interface's message ID validation,
which returns HTTP 400 and `malformed_request`. Omitting the member would fail
at the MCP schema instead; a nonempty string such as `"invalid"` is a legal
opaque message ID and can return `message_unknown`. The offline empty-member
contract test shares these exact arguments and checks the actual Agent HTTP
status. Success requires the endpoint address, exact marker and
`malformed_request` code in the client output, with process exit zero. The fixture
also observes actual Agent calls: successful `me` and `recv`, and an invalid
`status` returning HTTP 400. It records only method names and HTTP statuses, not
credentials or bodies. Model output alone cannot pass the probe. Report each client
version and the command's result when recording the manual check; do not record
or replay the client's actual MCP frames.

The command flags are described in the official
[Codex command reference](https://learn.chatgpt.com/docs/developer-commands#codex-exec),
[Codex MCP configuration](https://developers.openai.com/codex/mcp),
[Codex per-tool approval options](https://learn.chatgpt.com/docs/extend/mcp#other-configuration-options), and
[Claude Code MCP documentation](https://code.claude.com/docs/en/mcp).

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
