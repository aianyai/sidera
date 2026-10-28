# Acceptance support

Support for exercising the built program through its public front ends.
[`agent/cli`](agent/cli/README.md) implements the layer-neutral fake Agent by
starting the actual CLI. [`agent/mcp`](agent/mcp/README.md) drives a persistent
MCP subprocess through its stdin/stdout pipes, negotiating either supported era.
The fixture starts and waits for that real process; it never calls the MCP
server in process for binary acceptance. These drivers are test tooling and are not imported by
the executable.

Give a driver the built binary, a data directory, a project working directory,
and the endpoint or probe credential issued by the node. Session endpoints come
from `control ... session.create`, never from a test-only registration path.
Callers own their process context deadlines and any ledger recording wrapper.
