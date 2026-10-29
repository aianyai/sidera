# sidera

The node's command-line program. `main.go` supplies the process streams and
environment to [`internal/cli`](internal/cli/README.md), and is the only place
that calls `os.Exit`. Build it with `go build ./cmd/sidera`.

[`command-line`](../../docs/references/command-line/README.md) describes the
commands, output and exit codes. Agent commands use the checked local Agent
client; management commands use the management client; `init` and the foreground
`daemon` use the daemon composition root.
`sidera mcp` serves the same Agent tools through
[`internal/mcp`](internal/mcp/README.md). The dispatcher owns the Agent client;
the MCP peer takes ownership of stdin/stdout and reserves stdout for protocol
messages even with `--json`. Its wire contract and manual client check are in
[the MCP reference](../../docs/references/mcp/README.md).
The name `siderad` (also `siderad.exe`) selects `daemon`.

[`internal/acceptance`](internal/acceptance/README.md) contains test support
that drives the executable. The external tests in `cli_test.go` build the real
program, connect it to the real Agent handler on checked local IPC, and use
the engine fixture to create sessions through `session.create`. Every CLI
process has its own token and runs in the same directory as the other sessions.
The fixture reads the created endpoint's stored token; it never invents a
session endpoint or credential.

A checked-IPC HTTP peer also serves a deliberately noncanonical receive page;
the real executable must print its exact bytes in both human and JSON modes.

The receive interruption test pauses an actual HTTP response after its first
byte, kills its CLI process, and reads the unacknowledged message again. It
does not depend on pipe capacity or a process winning a timing race.

Management integration tests run against the real management server and also
start and stop the actual foreground daemon process. A checked-IPC HTTP fault
peer drops management responses to verify the CLI's one resend keeps the
operation identifier, version and body unchanged. The decoder and settings
argument fuzz tests live in `internal/cli`, alongside a parser fuzz target that
checks local/global arities, preserved values and unchanged argv.

Export tests compare online and offline raw copies with the source record and
keep the armored statement out of JSON stdout when writing a file. They
refuse invalid armor and mismatched digests before file creation, protect
pre-existing files, and make the child's own stdout/stderr writes fail to check
that its newly created copy is cleaned up. Private-file creation and Windows ACL
checks are tested at their owner, `fsx`.

MCP binary tests use an independent handwritten JSON-RPC driver to launch the
built executable in both supported protocol eras. They exercise all Agent tools,
probe-only credentials, process errors, and five 60 KiB messages in Chinese and
English, reconstructing each body and acknowledging only visible parts. The
built child follows the outer test's race build.
