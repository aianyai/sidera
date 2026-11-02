# controller

How a session a person opened becomes a controller: it asks the node, through the
management method `sessions.attach`, for an endpoint of its own and keeps the token of
that endpoint in memory, then uses the ordinary Agent interface. `sidera mcp` and the
`sidera` agent commands share this package; the command line and the MCP stdio boundary
only supply the environment and, for Codex's MCP server, the thread id.

- `controller.go`: whether a process may become a controller (`Wanted`), the identity
  and the `sessions.attach` request built from an environment (`RequestFromEnv`), the
  call itself (`Attach`) and the words for the refusals a person can act on.
- `session.go`: the MCP server's lazy attach on the first tool call and the heartbeat
  that repeats it while the server runs.

Rules:

- A process with `SIDERA_TOKEN` (a session Sidera created) or `SIDERA_PROBE_TOKEN` (a
  probe of an adapter) never becomes a controller.
- The token and the wake values are never logged, written to a file, put into argv or
  into the environment of a child process. Errors name keys, never values.
- This package calls `sessions.attach` and nothing else of the management interface.
