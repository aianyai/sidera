# MCP

Run `sidera mcp` to serve the Agent tools on stdin/stdout. Global `--home`
selects the node as it does for other commands. `SIDERA_TOKEN` authenticates
endpoint tools; `SIDERA_PROBE_TOKEN` is the independent one-use probe credential.
`--json` does not change MCP framing or redirect process diagnostics to stdout.
The command never starts a daemon. It opens the management interface only in
controller mode, for the one method `sessions.attach`, and never exposes a management
tool.

The stdio boundary supports precisely `2025-06-18` and `2026-07-28`. It reuses
[the peer transport](../../../sdk/go/peer/README.md), including bounded queues
and writes. It never sends server requests or progress notifications. Standard
output contains MCP messages only; diagnostics go to standard error.

## Versions and lifecycle

Legacy clients send `initialize` with `protocolVersion`, `capabilities` and
`clientInfo`. The response selects `2025-06-18`, advertises only `tools: {}` and
identifies the server as `sidera` with the binary's version. The subsequent
`notifications/initialized` is ignored. A legacy `ping` returns `{}` at any time.

Modern requests carry `params._meta.io.modelcontextprotocol/protocolVersion`
and `params._meta.io.modelcontextprotocol/clientCapabilities`. Only `2026-07-28`
is accepted there. An unsupported version returns `-32022` with `supported` and
`requested` in `data`; missing or malformed required metadata returns `-32602`.
This selection is per request, so a modern request can share a legacy process.
The modern revision has no `initialize` or `ping` method: either method with
valid modern metadata returns `-32601`.
Without modern metadata, requests other than `initialize` and `ping` require
legacy initialization. MCP registers one default peer request handler so unknown
methods pass through the same era validation before method dispatch. Framing,
ambiguity checks, in-flight id checks, overload and cancellation remain the
peer's acceptance boundaries and can reject a request before this validation.

`server/discover` advertises the modern version and tools capability, with
`resultType: "complete"`, `ttlMs: 3600000`, `cacheScope: "public"`, and server
identity in `_meta.io.modelcontextprotocol/serverInfo`. The public cache is safe
because discovery contains no endpoint data. Without modern version metadata,
`server/discover` follows the process's legacy initialization state: before
`initialize` it returns `-32602`; after legacy initialization it is an unknown
legacy method and returns `-32601`. A modern discovery request does not initialize
the legacy process. An unknown method with unsupported modern version metadata
returns `-32022` with the same `supported` and `requested` data; one with the
supported version but missing required client capabilities returns `-32602`.
Valid modern requests and initialized legacy requests for unknown methods return
`-32601`; an uninitialized request without modern metadata returns `-32602`.

At the protocol level only fields the server uses are interpreted: unknown members
of a request or of its `params`, including extra metadata, are ignored, because
clients differ in what they add and the revisions grow fields. The members of a
tool's `arguments` are not of that kind; they are validated against the
catalogue (see Agent tools). MCP explicitly enables the peer dialect's
`RejectDuplicateKeysAndInvalidUnicode`: repeated member names and unpaired
UTF-16 surrogate escapes anywhere in a frame are `-32600`, including unknown
extensions, unknown methods, notifications and responses. This check precedes
method and parameter validation, overload and cancellation. The error message
is fixed (`duplicate member or invalid Unicode`), without paths or input values;
no handler or cancel target runs and the connection stays open. The raw
top-level id is echoed only when it occurs exactly once and is legal, and the
frame has exactly one string `method`. Every other rejected frame, including a
response, gets a null id: a response's id belongs to the other side's call-id
space and cannot identify a request here. JSON syntax and UTF-8 errors stay
`-32700`, and ordinary parameter errors stay `-32602`. Unknown protocol fields and
values remain accepted. Output follows the selected version's schema.

`notifications/cancelled` reads `requestId`, ignores invalid or unknown targets
and cancels the matching handler without answering it. The peer supplies this
policy; this package does not implement another cancellation engine. Queue
overload uses `-32603`; an oversized input line uses `-32600` with null id and
then closes. End of input cancels in-flight requests and normally returns success;
a broken transport is a process failure. A caller's ended context closes both
streams.

When requested by the caller, the first valid legacy `initialize` or modern
request (including `server/discover`) starts one independent Agent probe. A legacy
`ping` does not establish initialization and leaves the probe available. The probe
uses the client's separate probe credential, reports only on standard error, and
does not delay or alter an MCP response. On shutdown, `Run` cancels and joins
the automatic probe before returning, so its final diagnostic finishes while
the borrowed diagnostic writer is still available. A synchronized stop marker
prevents a late request handler from starting a probe after shutdown begins.

## Controller mode

A server started without `SIDERA_TOKEN` and `SIDERA_PROBE_TOKEN` is the `sidera` MCP
server a person added to their own Claude Code or Codex (`sidera adapter setup` installs
it). A server with either variable never enters this mode. The server attaches
its session on the first `tools/call` that passes argument validation, not at start-up:
a session that never uses Sidera opens no endpoint. The one thing it does at start-up
is to look for an endpoint the session already has (below). A repeated tool call does not
attach again; a failed attachment is tried again by the next call, and a refusal is
that call's tool error (the words are in [the command line](../command-line/README.md#controller-sessions)).

- Claude Code: the session id is `CLAUDE_CODE_SESSION_ID` of the server's environment,
  and the wake keys `CLAUDE_CODE_MESSAGING_SOCKET`, `CLAUDE_CODE_MESSAGING_TOKEN` and
  `CLAUDE_PID` are copied from it when present. Claude Code gives its MCP servers the first
  two and not `CLAUDE_PID`; the adapter then finds the session's process in its registry by the
  session id ([the adapter](../adapter-claude/README.md#attached-sessions)). After `/clear` the variable is stale;
  the endpoint keeps the id it was attached with. At start-up, in the background and
  without holding up the protocol, the server calls `sessions.attach` once with `create`
  false and the same params: a session resumed after its controller was away has an open
  endpoint (it stays open for the queue time after the last heartbeat), and the answer
  makes the server keep its token and start the heartbeat at once, so that the node wakes
  the resumed session for what waited without waiting for a first tool call. When the
  session has no endpoint (`not_found/session_not_attached`), or the lookup fails (the
  node is not running; only a debug line is logged), nothing is made and the first tool
  call attaches as described. The first result after a lookup that found an endpoint
  carries the wake notice, as one after an attachment does.
- Codex: the server's environment has no `CODEX*` variable, so the server cannot look for an
  endpoint at start-up: it learns the thread id only from the first tool call, and a
  resumed Codex session is woken once that call has attached it. The thread id is
  `params._meta["x-codex-turn-metadata"].thread_id` of the tool call, read as an object or
  as the same object in a JSON string; no other member of the metadata is interpreted.
  Without it the tool call is a tool error `unauthenticated/token_missing` saying the
  session id is unknown, and nothing is attached. A server run by hand in a plain
  terminal answers its tool calls the same way, with the code a missing token has.
- The attachment is sent by the management client with the display name
  `<Claude|Codex> controller (<base name of the working directory>)`. The token in the
  answer goes to the Agent client in memory and replaces the credential for the calls
  that follow; it is in no environment, argv, log or file, and neither are the wake
  values.
- While the server runs, a heartbeat repeats the attachment every 60 seconds with `create`
  false: it proves to the node that the controller lives (an endpoint whose controller is
  gone for the queue time is closed) and sends the wake information anew, and it never
  makes an endpoint, so an endpoint the owner closed is not made again behind the owner's
  back. It stops with the server (end of input or shutdown), which joins it. A failed
  repetition is logged on standard error (the error text carries no token or wake value)
  and tried again at the next beat; a refusal that holds until a person changes something
  (no adapter instance, an adapter that does not take attached sessions) is logged once
  for each code. An answer with another token means the endpoint was replaced by another
  process of the session; the Agent client takes the new token.
- When the node answers that the session has no endpoint (`not_found/session_not_attached`), or a
  tool call is refused by the Agent interface for authentication (`unauthenticated`, the token
  is not known any more), the endpoint was closed. The session is then not attached: the
  heartbeat stops, and the next valid tool call attaches it again and gets a new endpoint with
  a new address. That call's result carries a notice naming the new address and saying
  the old one is closed, so the peers that wrote to it must use the new one. The call that was
  refused is not repeated.
- A tool call of a Codex session that names another thread id than the one attached is logged
  once on standard error; the attachment stays as it was.
- The result of the call that attached carries a second text item, after the tool's
  own, saying whether the node can wake the session (`ready`) or not (`unavailable` or
  `unknown`, with the reason and that `recv` with `wait_s` collects replies instead).
  Later results have the one text item described below. The tool schemas do not change.

## Server instructions

`initialize` and `server/discover` both carry `instructions`, and the text is the same
constant, `guidance.Short` of the [SDK](../../../sdk/go/guidance/README.md): one line
that says what Sidera is (the message channel to the other agents and people of the
owner's own cluster), what to use the channel for, to call `recv` first when a notice
says a message is waiting, and that a page read must be acknowledged with `ack` or by
passing its lease as `ack_lease` to the next `recv` (otherwise the same page comes
straight back). It is what a session the owner opened learns about
Sidera, because such a session has neither an adapter's fixed statement nor a Skill
unless one is installed. It carries no endpoint data, which is what keeps the public
cache of discovery safe. The texts name a tool only by its MCP name (`recv`), never by
a client's composed name (`mcp__sidera__send` in Claude Code).

The clients limit what they keep of it, so the text is one line, in the order of what
matters most, and held by a test to the limits below:

- Claude Code cuts server instructions at 4096 characters
  (`CLAUDE_CODE_MAX_MCP_DESCRIPTION_LENGTH` changes the limit), per its
  [MCP documentation](https://code.claude.com/docs/en/mcp).
- Codex gives the instructions to the model as the description of the server's tool
  namespace (`namespace_description` in `codex-rs/codex-mcp/src/rmcp_client.rs`). The
  listing of deferred namespaces keeps only the first line of a description, cut to 250
  characters (`MAX_NAMESPACE_DESCRIPTION_CHARS` in
  `codex-rs/core/src/context/world_state/tools.rs`): its first 247 characters and
  `...`. That is why the text is a single line and why its first three facts, what the
  channel is, what to use it for and to call `recv` first, end within 247 characters;
  only the acknowledgement rule can be cut there.

The adapters give their sessions the longer statement of the same facts
([Claude](../adapter-claude/README.md), [Codex](../adapter-codex/README.md)); the
facts exist once in the SDK, and a test there fails when one text stops carrying
one.

## Agent tools

`tools/list` returns the fixed tools `me`, `declare`, `peers`, `directory`, `send`,
`reply`, `recv`, `ack`, `status`, `control` and `probe`, without pagination. The
modern result adds `resultType`, cache hints and server metadata as discovery
does. Read-only hints identify identity and directory queries and status; `probe`
is explicitly mutable. `declare` and `ack` carry idempotent hints. Hints convey
no authorization and never replace the Agent interface's checks. The descriptions of `peers`
and `directory` say what the results add to the node and the endpoint (the node's name, host
name, platform, system and Sidera version when known; the adapter that serves an entry
and, when known, its runtime), because the model reads them to choose a node and a runtime; the result shapes are
those of the [Agent interface](../agent-interface/README.md).

The catalogue's JSON schemas use common object, array, scalar, required, enum and
numeric-bound keywords without `$ref` or `$schema`. They are both the advertised
input description and the validator's source of truth. Tool arguments are a schema
this server defines and shows to the model, so they are validated as declared,
and `additionalProperties: false` is enforced: an argument the schema does not
declare is refused, never dropped. A dropped argument is silent, and the model
acts on the answer as if it had been applied: a `recv` that was sent `wait`, a
misspelling of `wait_s`, returned an empty page at once and the agent reported
that it had waited. The refusal is a tool error result (`isError: true`), not a
protocol error, so the model sees it and can correct the call. Its message names
the unknown arguments and lists the ones the tool accepts, for example
`unknown argument "wait"; recv accepts: ack_lease, msg, part, wait_s`; below the
top level the object is named by its path (`items[0] accepts: message_id, part`).
The same applies to the front end of `recv` and `probe`, which the server fixes
and a client cannot send. The unknown arguments are checked before the required
ones, so a call that misnames a required argument is told the accepted names. An
argument of a declared name with a value of the wrong type, a value outside its
bounds or an enum, or null is a tool error as well, whose `details.path` names the
argument. Integer schemas accept exact integral JSON numbers regardless
of notation (`1`, `1.0` and `1e0`), as defined by
[JSON Schema](https://json-schema.org/understanding-json-schema/reference/numeric#integer),
within signed 64-bit bounds and the schema's
minimum and maximum. Projection writes the equivalent integer lexeme before
calling the Agent interface, whose decoder requires that notation. The standard
library's [`math/big.Rat`](https://pkg.go.dev/math/big#Rat.SetString) preserves exact values: floating-point conversion could
silently round a fractional or large value, and a separate decimal library adds
scope without a capability needed here. Non-numbers and fractional values are
rejected. The Agent interface alone enforces its single-recipient limit.

`tools/call` returns one text item and explicit `isError` (a controller's first result adds the
wake notice, see Controller mode). It adds modern result
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
SIDERA_REALCLIENT=1 go test -v -tags realclient -run '^TestRealClientsAcceptMCPResponses$' -count=1 -timeout 15m ./cmd/sidera/internal/mcp
```

The check builds `sidera`, creates a temporary Agent socket backed by the test
node, and starts each real client in a separate empty directory. It uses private,
one-process MCP settings and leaves the user's persistent MCP configuration
untouched. If the outer test uses `-race`, the MCP executable is also built with
`-race`. Claude uses `--mcp-config` and `--strict-mcp-config`; Codex uses
`exec --ignore-user-config`, `--ephemeral` and inline `-c` settings while retaining
its existing authentication. Its inline
`mcp_servers.sidera.tools.recv.approval_mode="approve"` setting adds an explicit
approval override only for the fixture's `recv` tool in that process; the server
default remains `auto` and the sandbox remains read-only. `recv` has no read-only
hint because it can change receive state. These sessions consume the user's normal client
quota. The `realclient` build tag and explicit environment flag exclude the check
from ordinary tests and CI. The user runs it after the built-binary contract
tests pass; a passing scripted MCP driver is not a substitute for this check.
On Unix, the temporary directory must fit the local
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
