# mcp

The MCP stdio boundary. `Run(context.Context, Config)` owns the supplied input
and output streams, borrows the Agent client and diagnostic writer, and returns
only process-level failures. It cancels and joins its automatic Agent probe
before returning, including the probe's last diagnostic write. Probe startup
and shutdown share a lock so a late handler cannot outlive this boundary by
starting another probe. The caller resolves environment, home and credentials.

The peer package owns framing, queues, cancellation and transport closure. This
package selects the MCP dialect, including whole-frame duplicate-key and Unicode
checks, and owns request-era selection, protocol results and the Agent tool
catalogue. Its default request handler routes every accepted request through the
same era validation before method dispatch, including unknown methods. The
catalogue schemas also validate the arguments of a tool call as declared, so
an undeclared argument or a value of the wrong type is a tool error naming it, not
a protocol error and not an argument silently dropped; unknown fields outside the
arguments stay ignored. They project
known arguments; projected JSON retains explicit member presence through
`agentapi.Client.Call`. Integral JSON numbers are projected exactly into signed
64-bit integer lexemes before that call, including decimal and exponent inputs.
The Agent interface owns semantic validation and the
single-recipient limit. The server opens the management interface only in controller
mode, through [`controller`](../controller/README.md), for `sessions.attach`: once at
start-up in the background to find an endpoint the session already has (a server with no Agent client
looks for none), on the first
valid tool call otherwise, then as a heartbeat; `Run` joins both before it returns. The
Codex thread id is read from the tool call's `_meta` here, the only place that sees it. Read
[the reference](../../../../docs/references/mcp/README.md) for the wire contract.

The opt-in real-client check uses an explicit empty `message_id` to exercise
Agent HTTP 400 and `malformed_request`. Its status arguments are shared with
the offline empty-member contract test. Codex receives an inline, one-process
`recv` approval override; the manual check still requires actual identity,
receive and status calls from each client.
