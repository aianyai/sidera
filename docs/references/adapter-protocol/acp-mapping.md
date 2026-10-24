# Mapping to the Agent Client Protocol

How each method and notification of the adapter protocol relates to the Agent
Client Protocol (ACP), and what an adapter that bridges an ACP agent would have to
supply. This document describes the committed adapter protocol (the table in
[the adapter-protocol reference](README.md#messages-and-methods), the code in
`sdk/go/adapterproto/methods.go`); it does not describe an implementation of the
bridge.

## Scope

- **ACP version compared.** The stable protocol, version 1, at schema release
  `schema-v1.26.0` of the repository `agentclientprotocol/agent-client-protocol`
  (commit `e2d83349bc9e28a4059ea3acefa1c91c966f74e2`, 2026-10-16). Compared on
  2026-10-23. The facts below come from the schema `schema/v1/schema.json` and the
  method list `schema/v1/meta.json` of that release, and from the protocol
  documentation at <https://agentclientprotocol.com/protocol/overview> (the
  pages named in the text; the same pages are in the release under
  `docs/protocol/v1/`). Where the draft of version 2 changes a fact that matters
  for the mapping, it is marked "v2 draft": release `schema-v2.0.0-alpha.9`, the
  same commit, files `schema/v2/schema.json` and `schema/v2/meta.json`. The v2
  draft is unstable and not compared in full. Methods that exist only in the
  unstable part of the v1 schema (`schema/v1/meta.unstable.json`) are listed once
  at the end of the reverse table.
- **Direction.** In ACP the client drives an agent: the client starts the agent as
  a subprocess and sends `initialize`, `session/new`, `session/prompt`; the agent
  streams `session/update` and asks the client for permissions, files and
  terminals ([transports](https://agentclientprotocol.com/protocol/transports),
  [overview](https://agentclientprotocol.com/protocol/overview)). In Sidera the
  daemon drives an adapter, which drives a native agent runtime. A generic ACP
  bridge adapter would therefore sit on the adapter side: toward the daemon it is
  an adapter (the table in the adapter-protocol reference); toward the native
  agent it is an ACP client. A row below that says "D to A" is a call from the
  daemon to the adapter, and the ACP method it maps to is one the bridge sends to
  the agent.
- **Statuses.** **same**: the semantics match and only spelling differs.
  **mappable**: a counterpart exists and the text names the transformation.
  **none** (Sidera method) or **not adopted** (ACP method): no counterpart, with
  the reason.

## Sidera to ACP

All 25 methods and notifications of the committed method table, in table order.

| Sidera method | Direction, kind | ACP counterpart | Status | Transformation, or why none |
| ------------- | --------------- | --------------- | ------ | --------------------------- |
| `initialize` | D to A, request | `initialize` (agent method) | mappable | The bridge receives Sidera's `initialize` and sends ACP's to the agent. Sidera's version range becomes the single `protocolVersion` the bridge supports (1); `adapter` (`id`, `version`) is the bridge's own; `capabilities.runtime` is derived from the agent's `agentCapabilities` (see section 5). `instance.lock_path` has no counterpart; the lock is the bridge's. |
| `ping` | both, request | none | none | ACP defines no liveness method; an unknown method is answered `-32601`. Liveness of the ACP agent is the bridge's own matter (process state). |
| `$/cancel_request` | both, notification | `$/cancel_request` | same | Same method name and meaning: a notification naming the request to cancel; the receiver answers the original request with a result or `-32800`. Spelling of the member differs (`request_id` against `requestId`). See section 4 for what each side promises. |
| `shutdown` | D to A, request | none | none | ACP has no shutdown method; the client closes the agent's standard input and terminates the subprocess ([transports](https://agentclientprotocol.com/protocol/transports)). |
| `adapter.start` | D to A, request | `session/load` or `session/resume` per listed session | mappable | `sessions` lists the endpoints the daemon believes exist, with their `native_ref`. The bridge starts (or finds) the agent and, per session, calls `session/load` (needs `agentCapabilities.loadSession`) or `session/resume` (needs `agentCapabilities.sessionCapabilities.resume`) with `sessionId` = `native_ref`; success is `attached`, failure `unreachable` or `lost`. `turn_active` and `process` are the bridge's own knowledge: ACP reports neither. |
| `adapter.probe` | D to A, request | `initialize` response (`agentInfo`, `authMethods`), `authenticate` | mappable | `runtime` (`name`, `version`) is `agentInfo` (optional in ACP); `auth` is derived from `authMethods` and from a `-32000` ("authentication required") error on a request. `dependencies`, `presets` and `tool_probes` have no ACP counterpart. |
| `adapter.availability` | A to D, notification | none | none | ACP has no health or availability notification. A bridge derives `runtime_auth` from `-32000` and `native_unavailable` from the exit of the agent process. |
| `session.create` | D to A, request | `session/new`, then `session/prompt` for `first_prompt` | mappable | `session/new` takes `cwd`, `mcpServers` and optional `additionalDirectories`; it takes no first prompt and no preset. The bridge supplies `cwd` and the `mcpServers` entries from the preset and `tool_access`, takes `native_ref` from the returned `sessionId`, and sends the first prompt as an ordinary `session/prompt`. `outcome: indeterminate` is the bridge's judgement after a timeout or an error whose effect is unknown. |
| `checkpoint.put` | A to D, request | none | none | Durable adapter state kept by the daemon for crash recovery; ACP has no such concept. |
| `prompt.deliver` | D to A, request | `session/prompt` | mappable | `text` becomes one text content block. The response of `session/prompt` arrives only when the turn stops (stable v1), so it cannot serve as the `stage` answer; see section 5. `attempt` has no field in `session/prompt`. Refusal reasons (`busy`, `runtime_auth`, `native_unavailable`, `session_lost`, `control_unknown`) are the bridge's own. |
| `prompt.persisted` | A to D, notification | none in stable v1 | none | v1 has no signal that a prompt was taken into the conversation before the turn ends. v2 draft: the successful response of `session/prompt` means the user message was inserted and carries its `messageId`. |
| `turn.started` | A to D, notification | no notification in stable v1; `session/prompt` and `session/update` are the evidence | mappable | v1 has no turn-start notification: the bridge reports it when it sends `session/prompt` or on the first `session/update` that follows, and mints `turn`. v2 draft: a `state_update` with `state: running`. |
| `turn.ended` | A to D, notification | `session/prompt` response, `stopReason` | mappable | `end_turn` is `completed`; `cancelled` is `interrupted`; a JSON-RPC error response is `failed`. `max_tokens`, `max_turn_requests` and `refusal` fit none of the three values of `final` exactly. v2 draft: an idle `state_update` carrying the stop reason, which also covers turns the agent starts itself. |
| `session.interrupt` | D to A, request | `session/cancel` | mappable | `session/cancel` is a notification with no reply. The bridge sends it, answers `requested`, and answers `stopped` (or reports `turn.ended` with `interrupted`) when the pending `session/prompt` returns `stopReason: cancelled`. `not_running` is the bridge's knowledge that no prompt is pending. `session/cancel` is mandatory for every ACP agent. |
| `session.close` | D to A, request | `session/close` | mappable | Needs `agentCapabilities.sessionCapabilities.close`. ACP's close cancels ongoing work and frees the session, which matches `closed`; `close_unconfirmed` is the bridge's answer when the call fails or times out. An agent without the capability can only have its process ended. |
| `prompt.reconcile` | D to A, request | none | none | ACP has no query "did this prompt arrive". The data it reads can be obtained with `session/load`, which replays the whole conversation as `session/update` notifications; this needs `loadSession`, and `session/resume` does not replay. |
| `recv.observed` | A to D, request | none | none | Acknowledging on the agent's behalf rests on the text of a tool result as the model received it, from the runtime's durable record. ACP does not state that tool-call content shown to the client is that text (UNVERIFIED as a property of any agent). |
| `native_request.handled` | A to D, notification | none for the report; the requests it reports are ACP's agent-to-client requests | none | The notification reports a request the native runtime sent the adapter and how the adapter answered. In a bridge those requests are `session/request_permission`, `fs/*`, `terminal/*` and `elicitation/*`; `method` would carry the ACP method name. ACP has no report of this kind. |
| `endpoint.register` | A to D, request | none | none | Direct endpoints are for adapters that read channel messages themselves (chat bridges). ACP agents are driven, not messaged. |
| `endpoint.unregister` | A to D, request | none | none | As `endpoint.register`. |
| `endpoint.pending` | D to A, notification | none | none | As `endpoint.register`. |
| `endpoint.recv` | A to D, request | none | none | As `endpoint.register`. |
| `endpoint.ack` | A to D, request | none | none | As `endpoint.register`; the acknowledgement settles delivery records, which ACP does not have. |
| `endpoint.send` | A to D, request | none | none | As `endpoint.register`. ACP has no messaging between agents or nodes. |
| `endpoint.message_status` | A to D, request | none | none | As `endpoint.register`. |

Counts: 1 same, 9 mappable, 15 none (25 in all).

## ACP to Sidera

All methods and notifications of the stable v1 method list (`schema/v1/meta.json`):
13 agent methods, 11 client methods and `$/cancel_request`.

| ACP method | Sent by, kind | Sidera counterpart | Status | Transformation, or why not adopted |
| ---------- | ------------- | ------------------ | ------ | ---------------------------------- |
| `initialize` | client, request | `initialize` | mappable | See the first table. |
| `authenticate` | client, request | none | not adopted | Native credentials are read by the adapter from their native place and never enter Sidera's configuration. Auth state is reported with `adapter.probe` and `adapter.availability` (`runtime_auth`). A bridge may call `authenticate` on its own account when the agent asks for it. |
| `logout` | client, request | none | not adopted | The channel never ends a native login. |
| `session/new` | client, request | `session.create` | mappable | See the first table. |
| `session/load` | client, request | `adapter.start`, `prompt.reconcile` (data source) | mappable | Used by the bridge, not exposed: the replayed content (`user_message_chunk`, `agent_message_chunk`, tool calls) stays inside the adapter, since the channel never reads message bodies. |
| `session/resume` | client, request | `adapter.start` | mappable | As `session/load`, without replay. |
| `session/set_mode` | client, request | none | not adopted | The preset is chosen once, by `session.create`; the channel changes no setting of a running session. |
| `session/set_config_option` | client, request | none | not adopted | As `session/set_mode`. |
| `session/prompt` | client, request | `prompt.deliver` with `turn.started` and `turn.ended` | mappable | See the first table. |
| `session/cancel` | client, notification | `session.interrupt` | mappable | See the first table. |
| `session/list` | client, request | none | not adopted | The daemon is the record of sessions and tells the adapter which exist in `adapter.start`. |
| `session/delete` | client, request | none | not adopted | The channel closes a session (`session.close`) and never deletes native history. |
| `session/close` | client, request | `session.close` | mappable | See the first table. |
| `session/request_permission` | agent, request | none (answered inside the adapter; reported by `native_request.handled`) | not adopted | Approvals do not pass through the channel. The adapter answers every native request itself, from the preset, within one second, and never asks the daemon. The method needs no client capability to be sent, so a bridge must always answer it. |
| `session/update` | agent, notification | none (consumed inside the adapter; at most it yields `turn.started`) | not adopted | Content streaming: message chunks, thoughts, tool calls, plans, commands, usage. The channel never reads or interprets message bodies. |
| `fs/read_text_file` | agent, request | none | not adopted | The adapter and its native runtime have local access to files. A bridge declares no `fs` capability, and ACP treats omitted capabilities as unsupported. |
| `fs/write_text_file` | agent, request | none | not adopted | As `fs/read_text_file`. |
| `terminal/create` | agent, request | none | not adopted | The adapter and its native runtime have local access to processes. A bridge declares `terminal` false. |
| `terminal/output` | agent, request | none | not adopted | As `terminal/create`. |
| `terminal/release` | agent, request | none | not adopted | As `terminal/create`. |
| `terminal/wait_for_exit` | agent, request | none | not adopted | As `terminal/create`. |
| `terminal/kill` | agent, request | none | not adopted | As `terminal/create`. |
| `elicitation/create` | agent, request | none | not adopted | Asking the user questions does not pass through the channel; the adapter refuses locally (`rejected_by_preset`). A bridge declares no `elicitation` capability. |
| `elicitation/complete` | agent, notification | none | not adopted | As `elicitation/create`. |
| `$/cancel_request` | both, notification | `$/cancel_request` | same | See the first table. |

Counts: 1 same, 7 mappable, 17 not adopted (25 in all).

The unstable part of the v1 schema adds `providers/list`, `providers/set`,
`providers/disable`, `session/fork` and `mcp/message` (`schema/v1/meta.unstable.json`).
They are not adopted: the channel selects no model provider, forks no session and
does not carry MCP messages over the adapter connection (5 not adopted). Names
that start with an underscore are ACP's extension methods
([extensibility](https://agentclientprotocol.com/protocol/extensibility)); the
adapter protocol has no extension methods and answers an unknown method `-32601`.

## Envelope-level differences

| Aspect | Adapter protocol | ACP v1 |
| ------ | ---------------- | ------ |
| Framing | One JSON object per line, UTF-8, over the adapter's standard input and output; standard error is for logs; lines over 1 MiB close the connection; batches rejected. | The same framing for stdio: newline-delimited, no embedded newlines, UTF-8, standard error for logs ([transports](https://agentclientprotocol.com/protocol/transports)). The transports page states no line limit and does not mention batches. |
| Field names | snake_case. | camelCase (`protocolVersion`, `sessionId`, `stopReason`, `requestId`). |
| Optional members | Omitted; an explicit `null` or empty value is `-32602`. | Many optional members are nullable (`anyOf` with `null` in the schema). |
| Who calls what | Each method has a direction (`daemon_to_adapter`, `adapter_to_daemon`, `both`); a call in the wrong direction is `-32601`. | Fixed sets of agent methods and client methods; both sides send notifications. |
| Version negotiation | Positive integers; each side announces a closed range; the adapter picks the highest common version; no overlap is `-32022` with `data` `{requested, supported}`. | One integer for a major version (`uint16`). The client sends the latest it supports; the agent answers the same version if it supports it and otherwise its own latest; a client that cannot use the answer should close the connection ([initialization](https://agentclientprotocol.com/protocol/initialization#version-negotiation)). No error code is defined for a mismatch. The numbers are independent: Sidera protocol 1 is not ACP 1. |
| Capabilities | `capabilities` (and `runtime`, `direct`, and the daemon's own) are open objects: unknown keys are ignored; every other object is closed and an unknown member is `-32602`. A peer uses an addition only when the other side declared it. | Capabilities are optional and omitted ones are unsupported; non-breaking additions go through capabilities. The v1 schema declares no closed objects (no `additionalProperties: false` on any definition). Whether a receiver must ignore unknown members elsewhere is not stated in the pages read (UNVERIFIED). Custom data goes in `_meta`; implementers must not add root-level fields to specified types ([extensibility](https://agentclientprotocol.com/protocol/extensibility)). The adapter protocol has no `_meta`. |
| Errors | Standard JSON-RPC codes, the implementation range `-32001` to `-32004`, `-32022`, `-32800`, and application codes `1001` to `1011`; `data` is always an object. The error response says whether the request had an effect (the effect rule). | `-32700`, `-32600` to `-32603`, `-32800` (request cancelled), `-32000` (authentication required), `-32002` (resource not found); other integers allowed; `data` is any value (`schema/v1/schema.json`, `ErrorCode`, `Error`). The error page of the documentation is empty ("coming soon"); no effect rule is stated. |
| Code collisions | `-32002` is `shutting_down`; `-32001`, `-32003`, `-32004` and `-32022` are defined. | `-32002` is `resource_not_found`; `-32000` is `authentication required`, which the adapter protocol does not define. Sidera has no `-32022` counterpart in ACP. A bridge must translate codes, not pass them through. |
| Cancellation of a request | `$/cancel_request {request_id}` is handled by the read loop and cancels the handler's context; the request is answered exactly once, with its result or `-32800`. | `$/cancel_request {requestId}`; the receiver MAY cancel, MUST answer with a result or `-32800`; implementation is optional ([cancellation](https://agentclientprotocol.com/protocol/cancellation)). |
| Cancellation of work | `session.interrupt`, a request answered with a stage; `turn.ended` with `final: interrupted` is the evidence that the turn stopped. | `session/cancel`, a notification; the pending `session/prompt` must be answered with `stopReason: cancelled`, not an error; the client must answer pending permission requests with outcome `cancelled` ([prompt turn](https://agentclientprotocol.com/protocol/prompt-turn#cancellation)). |
| Ids | A request id is an integer of at most 2^53-1 in absolute value or a string of 1 to 128 characters; `null`, fractions and an id already in flight are `-32600`. Endpoints, attempts, leases and turns have their own identifiers (opaque to the adapter or defined as UUID v4 and hex). | A request id is a string, a number or `null`; numbers should not be fractional; the schema states no length bound (`RequestId`). Sessions, tool calls and terminals have opaque string ids. v1 has no turn id and no prompt id on `session/prompt`. |
| Prompt result | `prompt.deliver` answers at once with a stage; turns are reported by notifications. | `session/prompt` answers when the turn stops, with the stop reason (v2 draft: when the message is inserted). |
| Initialization gate | Before `initialize` only `initialize`, `ping` and `$/cancel_request` are accepted; anything else is `-32003`. | Clients must initialize before creating sessions; the pages read state no error for a violation. |
| Lifetime of the native side | The native process may outlive the adapter (container, process identity, `adapter.start`). | The agent is a subprocess of the client reached through its standard input and output ([transports](https://agentclientprotocol.com/protocol/transports)). |

## What a bridge adapter would need that the protocol does not provide

Stated as facts about the two protocols; none of it is built.

1. **A prompt acknowledgement.** `prompt.deliver` must answer `accepted`,
   `persisted` or `refused`. A v1 agent answers `session/prompt` only when the turn
   stops, and sends nothing that says the prompt was taken in. A bridge can know
   that it wrote the request, and whether the agent process or session is gone; it
   cannot learn from the protocol that the prompt entered the conversation. v2
   draft: the response of `session/prompt` is that acknowledgement.
2. **An attempt identifier on the prompt.** `session/prompt` has the members
   `sessionId` and `prompt` (and `_meta`). The attempt identifier can reach the
   agent only inside the text (the one trailing line the adapter may append, see
   "Prompt delivery") or in `_meta`, whose handling by an agent is unspecified.
3. **Reconciliation.** `prompt.reconcile` needs the record of the session.
   `session/load` replays it as `session/update` notifications and is optional
   (`loadSession`); the replay streams the whole conversation, with no way to ask
   for a part, and `session/resume` replays nothing. An agent without
   `loadSession` leaves the bridge no record to read, so the verdict can only be
   `indeterminate`.
4. **Turn boundaries of the agent's own work.** `StopReason` occurs only in the
   response of `session/prompt` (v1 schema). A turn the agent starts on its own,
   or ends without a pending prompt, has no start or end signal in v1; the
   adapter protocol requires both (`turn.started`, `turn.ended`). v2 draft: the
   `state_update` session update covers agent-initiated work.
5. **Turn references.** `turn` is an opaque string the adapter chooses; ACP v1
   offers none, so a bridge mints them.
6. **Process identity.** ACP carries no process id or start key. The bridge knows
   the pid of the agent it started and can compute the start key itself. Because
   the agent's standard input and output belong to the bridge process, a restarted
   bridge cannot reattach to a running agent; it can only start a new process and
   continue the session with `session/load` or `session/resume` when the agent
   offers them. The container kinds of `ProcessIdentity` describe how a native
   process outlives the adapter, which this transport does not allow.
7. **Tool access per session.** `tool_access.env` is a set of environment
   variables for the agent, carrying a token per session endpoint. `session/new`
   has no environment member; it has `mcpServers`, whose stdio entries carry their
   own `env`. A per-session token reaches the agent through an MCP server entry,
   through the environment of a dedicated agent process per session, or not at
   all. The `cli` front end reaches the tools through the agent's shell and needs
   the environment of the agent process.
8. **Server requests.** `session/request_permission` is a baseline client method
   with no capability to switch it off, so every ACP agent may send it and the
   bridge must answer it from the preset within one second. The bridge declares no
   `fs`, `terminal` or `elicitation` capability so the agent should not call
   those. It must also answer pending permission requests with outcome `cancelled`
   when it sends `session/cancel`. Each answered request is reported with
   `native_request.handled`.
9. **Availability and version.** There is no ACP notification for health. The
   runtime name and version come from `agentInfo`, which an agent should provide
   but need not. The manifest's `tested` ranges and the daemon's
   `version_untested` condition need that name and version.
10. **Concurrent prompts.** A second `session/prompt` while a turn is running is
    refused by Sidera with reason `busy`. The v1 pages read do not say what an
    agent does with it (UNVERIFIED); the v2 draft mentions queued prompts.
11. **Acknowledging on the agent's behalf.** `recv.observed` needs the text of
    a tool result as the model received it. Tool-call content in `session/update`
    is what the agent chooses to show the client; nothing in ACP says it equals
    the model's input (UNVERIFIED for any given agent). A bridge would not
    declare `ack_on_behalf` unless its agent is known to satisfy this.
12. **Capabilities the bridge declares.** `interrupt` is always available because
    `session/cancel` is mandatory; `server_requests` is true because
    `session/request_permission` is mandatory; `frontends` depends on the agent
    and the `mcpServers` transports it accepts (`stdio` always; `http` and `sse`
    when `mcpCapabilities` says so).

## Differences in ACP v2 draft that change the mapping

Release `schema-v2.0.0-alpha.9`, unstable. From `schema/v2/schema.json` and
`schema/v2/meta.json`:

- `session/prompt` answers with a `messageId` once the user message is inserted;
  progress and the end of work are `state_update` session updates
  (`running`, `idle` with a stop reason, `requires_action`), also for work the
  agent starts itself.
- `fs/*` and `terminal/*` are no longer client methods; `session/load` and
  `authenticate` are replaced by `session/resume` with a `replayFrom` cursor and
  `auth/login`.
- `initialize` renames `clientInfo`, `agentInfo`, `clientCapabilities` and
  `agentCapabilities` to `info` and `capabilities`.
- `session/request_permission` carries a `title`, a `description` and a
  `subject` in place of a tool call.
- `session/close` must send the final updates and the idle update with the
  `cancelled` stop reason before it answers.

## Where

- Sidera side: `sdk/go/adapterproto/methods.go` (`Methods()`, the table the first
  section follows) and the adapter-protocol reference.
- ACP side: <https://agentclientprotocol.com/protocol/overview>;
  `schema/v1/schema.json` and `schema/v1/meta.json` at tag `schema-v1.26.0` of
  <https://github.com/agentclientprotocol/agent-client-protocol>.
- No test holds this document to the code; when a method is added to or removed
  from `adapterproto.Methods()`, or ACP releases a new protocol version, the
  tables are redone.
