# mcp

The independent MCP backend of `fakeagent.Agent`. `New` takes the stdin/stdout
pipes of a process the fixture started and the exact protocol revision to use:
`2025-06-18` or `2026-07-28`. It negotiates that revision and calls the tools with
handwritten JSON-RPC messages, without importing the production MCP server or
an MCP SDK. The fixture owns process configuration, stderr and waiting for exit.

`Client.Close` closes the pipes and ends pending calls. Request contexts send the
MCP cancellation notification. Results map back to the scenario's agent types;
receive text is preserved byte for byte, including its newline, and `isError`
becomes the Agent category and code. Input omission follows the scenario types:
for example a nil acknowledgement list means the whole page, while an explicit
empty list is still sent to the server. The server owns whether it is valid.

A receive's nonzero wait is preserved even when a part is requested, so the Agent
boundary can reject incompatible inputs rather than the driver silently changing
the request. `Recv` and `Probe` accept only the MCP front end (or an unspecified
front end); an explicit different front end returns an error before sending.

Send, reply and control results map the real response replay flag to
`ReplayFresh` or `ReplayRepeated`. A failed call retains `ReplayUnknown`.
