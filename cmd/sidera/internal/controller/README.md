# controller

How a session a person opened becomes a controller: it asks the node, through the
management method `sessions.attach`, for an endpoint of its own and keeps the token of
that endpoint in memory, then uses the ordinary Agent interface. `sidera mcp` and the
`sidera` agent commands share this package; the command line and the MCP stdio boundary
only supply the environment and, for Codex's MCP server, the thread id.

- `controller.go`: whether a process may become a controller (`Wanted`), the identity
  and the `sessions.attach` request built from an environment (`RequestFromEnv`), the
  call itself (`Attach`) and the words for the refusals a person can act on.
- `session.go`: the MCP server's lazy attach on the first tool call, its look at start-up
  for an endpoint the session already has (`Resume`, which makes none) and the heartbeat
  while the server runs. The heartbeat only finds the endpoint (`create` false): it proves
  the controller lives and refreshes the wake information, and never makes an endpoint, so
  an endpoint the owner closed stays closed. When the node says there is none, or a call
  made with the token is refused for authentication (`Rejected`), the session is not
  attached any more, and the next use attaches it again with a new endpoint; the result of
  that use (`Arrival.Notice`) names the new address and says the old one is closed. A
  refusal that holds until a person changes something is logged once per code, and a tool
  call naming another Codex thread than the attached one is logged once and does not switch
  the attachment.

Rules:

- A process with `SIDERA_TOKEN` (a session Sidera created) or `SIDERA_PROBE_TOKEN` (a
  probe of an adapter) never becomes a controller.
- The token and the wake values are never logged, written to a file, put into argv or
  into the environment of a child process. Errors name keys, never values: `Attach` takes
  every wake value out of the words of an error it returns, so an error on the way that
  carries the request shows none.
- This package calls `sessions.attach` and nothing else of the management interface.
