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
