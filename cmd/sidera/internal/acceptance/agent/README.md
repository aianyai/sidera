# Agent drivers

Backends of `internal/node/testkit/fakeagent.Agent` that exercise a real front
end. Results use the same layer-neutral types as the engine fixture, so a
scenario can choose its front end without changing its assertions.

Each driver preserves receive bytes in `RecvResult.Raw` and maps a wire error
to `fakeagent.Error`. Unsupported input is an explicit error; it must never be
silently discarded or changed to the nearest supported value.

[`cli`](cli/README.md) starts one real command-line process for each call.

[`mcp`](mcp/README.md) takes the pipes of a real MCP subprocess and sends
independently encoded JSON-RPC exchanges. Its caller owns process startup,
credentials, stderr and waiting for exit; the driver owns the supplied pipes.
