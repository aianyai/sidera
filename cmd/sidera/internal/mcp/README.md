# mcp

The MCP stdio boundary. `Run(context.Context, Config)` owns the supplied input
and output streams, borrows the Agent client and diagnostic writer, and returns
only process-level failures. The caller resolves environment, home and credentials.

The peer package owns framing, queues, cancellation and transport closure. This
package selects the MCP dialect and owns request-era selection, protocol results
and the Agent tool catalogue. It never opens the management interface. Read
[the reference](../../../../docs/references/mcp/README.md) for the wire contract.
