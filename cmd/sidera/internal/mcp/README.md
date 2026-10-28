# mcp

The MCP stdio boundary. `Run(context.Context, Config)` owns the supplied input
and output streams, borrows the Agent client and diagnostic writer, and returns
only process-level failures. The caller resolves environment, home and credentials.

The peer package owns framing, queues, cancellation and transport closure. This
package selects the MCP dialect and owns request-era selection, protocol results
and the Agent tool catalogue. The catalogue schemas also validate and project
known arguments; projected JSON retains explicit member presence through
`agentapi.Client.Call`. The Agent interface owns semantic validation and the
single-recipient limit. It never opens the management interface. Read
[the reference](../../../../docs/references/mcp/README.md) for the wire contract.

The opt-in real-client check uses an explicit empty `message_id` to exercise
Agent HTTP 400 and `malformed_request`. Its status arguments are shared with
the offline empty-member contract test. Codex receives an inline, one-process
`recv` approval override; the manual check still requires actual identity,
receive and status calls from each client.
