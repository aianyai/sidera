# Command line

The `sidera` executable is in [`cmd/sidera`](../../../cmd/sidera/README.md).
It uses the standard-library `flag` parser and the existing Agent client, with
no CLI framework or second implementation of the local HTTP protocol.

## Commands

Global flags are `--home <directory>`, `--json` and `-h`/`--help`. They may
precede or follow a subcommand. A value following a local flag remains that
flag's value, even if spelled `--json` or `--home`. The command registers each
local flag once; both global scanning and local parsing consult that same
standard-library `FlagSet`, including its boolean-flag convention. `--` ends
flag parsing.
Data-directory selection is `--home`, then `SIDERA_HOME`, then `~/.sidera`;
the selected directory is made absolute. Each command's help describes its
arguments, purpose and one example.

| Command | Purpose |
| --- | --- |
| `init` | Create the private data directory, keys and default configuration through `daemon.Init`; print the node, home and a reminder to export and back up the self-revocation outside the device. |
| `daemon` | Run the composition root in the foreground with the executable adapter launcher; the daemon owns signal handling and orderly shutdown. |
| `me` | Read the authenticated session's address, declaration and front-end limits. |
| `declare [--paused=<bool>] [--refuse-multi-recipient=<bool>] [--restrict-senders=<bool>] [--accept-from <addr>,...] [--hidden=<bool>]` | Send only the fields explicitly supplied; `--accept-from=` clears the list. |
| `peers` | List visible nodes. |
| `directory [--node <id>] [--limit <n>] [--cursor <cursor>]` | Read one directory page. |
| `send --to <addr> [--body <text> \| --body-file <file>] [--content-type <type>] [--thread <id>] [--accept-before <seconds>] [--key <key>]` | Send a message; comma-separated recipients are passed to the Agent boundary's one-recipient check. |
| `reply <message-id> [--body <text> \| --body-file <file>] [--content-type <type>] [--thread <id>] [--key <key>]` | Reply to a received message. |
| `recv [--wait <seconds>] [--msg <id> --part <k>] [--ack <lease>]` | Receive a page with front end `cli`; `next.msg` and `next.part` in a page become `--msg` and `--part`. |
| `ack <lease> [<message-id>:<part> ...]` | Confirm the visible parts named, or the whole page when no parts are listed. |
| `status [<message-id>]` | Read node status, or the named message's delivery state and eventual control reply. |
| `control <addr> <signal> [--preset <name>] [--body <text> \| --body-file <file>] [--content-type <type>] [--accept-before <seconds>] [--key <key>]` | Send a control signal, optionally with an initial message for session creation. |
| `probe` | Present `SIDERA_PROBE_TOKEN` with front end `cli`. |
| `mcp` | Serve the Agent tools over stdio MCP; see [the MCP reference](../mcp/README.md). |
| `daemon stop` | Ask the running daemon to shut down after returning the accepted result. |
| `alerts list [--all]` / `alerts ack <id>` | List alerts, optionally including acknowledged ones, or acknowledge an alert. |
| `preset list` / `preset add <name> --file <file> [--expected-version <n>]` / `preset remove <name> [--expected-version <n>]` | Read or change the preset collection. A file of `-` reads stdin. |
| `adapter install <package-directory>` / `adapter list` | Install a local package by its absolute path, or list installed packages, instances and probe results. |
| `adapter enable <id> [--instance <name>] [--config <file>] [--expected-version <n>]` / `adapter disable <instance> [--expected-version <n>]` | Enable or disable an instance. A config file of `-` reads stdin. |
| `endpoint list [--instance <name>]` / `endpoint close <addr>` | Read endpoints, including closed ones, or close an endpoint. |
| `endpoint set <addr> <field>=<json> ... [--expected-version <n>]` | Change the endpoint's settings. |
| `node show` / `node set <field>=<json> ... [--expected-version <n>]` | Read or change node settings. |
| `identity export-revocation [--out <new-file>]` | Export the self-revocation and its digest; optionally write the verified raw record to a new private file. |
| `identity confirm-backup <digest>` | Confirm an offline backup by the digest printed on export. |
| `identity retire --node <id> --yes` | Retire this identity permanently and stop the daemon; omitting `--yes` is usage error 2. |
| `clock clear-rollback` | Accept the current wall clock and clear the rollback pause. |

Running the program with the filename `siderad` or `siderad.exe` selects the
foreground daemon command. It does not install a background service.

Send and reply choose exactly one body source. Without a body flag they read
stdin; `--body-file -` also reads stdin. Explicit empty text remains an empty
request body and is rejected by the existing message rules, as is invalid UTF-8.
No implicit idempotency key or retry is added. Every Agent method except probe
uses `SIDERA_TOKEN`; probe uses the separate `SIDERA_PROBE_TOKEN`.
Control omits its initial body when no body flag is supplied; it never consumes
stdin implicitly. Every explicitly provided Agent flag retains its presence,
including empty strings, false and zero, through `Client.Call`. The existing
interface owns whether a value is valid; the CLI does not normalize it away.

## Management requests

Settings assignments split at the first `=`. A JSON value stays raw JSON,
preserving its numeric spelling; a non-JSON value is a string. Each field can
be assigned once. The CLI does not interpret setting names or apply a schema.
Preset and instance config documents likewise remain raw JSON, so the shared
client checks their encoding and the configuration owner validates their rules.

When a versioned command omits `--expected-version`, the CLI reads it from
`node.show`, `presets.list` or `adapters.list` before issuing the change. Endpoint
settings use the instances collection version from `adapters.list`. An explicit
version, including zero, is sent unchanged. A conflict is returned to the caller;
it does not trigger another read or a different change.

Each change chooses one random operation identifier. Its params, version and
identifier are retained, and an unknown outcome permits only one resend of that
same request. The CLI does not reread files or regenerate an identifier when the
answer was lost. A second unknown outcome returns 10 with the original identifier
in the error details and on stderr in human mode. The management client itself
never retries.

Installation only resolves the package directory and names it to the daemon.
The daemon's existing package reader owns the regular-file checks and bounded
reads, as described in [configuration](../configuration/README.md).

Enabling an adapter returns before background startup and probing finish. Human
output says `enabled, probing in progress; see sidera adapter list`; that list
reports the probe outcome. JSON keeps the complete management response.

The identity export first uses the management interface. Only
`daemon_not_running` permits the offline path through `keys.ReadSelfRevocation`
and `keys.FormatRevocationExport`; peer-check failures and unknown outcomes are
reported without an offline read. Human export writes the armored statement to
stdout. Both modes print the digest and offline-backup instructions to stderr;
JSON export has the same complete `result` shape online and offline.

Before reporting an export, `keys.ParseRevocationExport` verifies the armor and
the CLI compares the verified record digest with the response's digest. Invalid
armor or a mismatch is an invalid answer (`outcome_unknown`, exit 10), and no
output file is created. The armor format and signature rules stay in
[identity](../identity/README.md).

`--out` calls `fsx.CreatePrivateFile`: the final name must not exist, and the
file is private when it is created. Unix uses exclusive creation at 0600 and
checks the opened file; Windows uses a protected current-user DACL and verifies
the opened file's volume supports persistent ACLs before any bytes are written.
Windows FAT/exFAT volumes and network shares are unsupported; when privacy
cannot be established, the error suggests stdout without `--out` instead.
The complete boundary is described in [storage](../storage/README.md).

The CLI writes the raw verified record, syncs and closes it before output. A
returned error, including an output-stream error, causes cleanup of the copy
this invocation created; cleanup failures are reported too. Local file errors
retain the OS cause and the stdout alternative when privacy cannot be verified;
remote failures and invalid export responses keep the shared error mapping.
A pre-existing file is never overwritten or removed. Direct process termination cannot run cleanup;
no power-loss durability is promised. The source statement in the node is kept.

Human `--out` prints only the digest to stdout. `--json`, with or without
`--out`, always prints the complete management response, including both
`armored` and `digest`; the flag adds the file write without removing response
fields. Backup instructions remain on stderr in both modes.

## Output and errors

Human success goes to stdout; human errors go to stderr as
`error: <category>/<code>: <message>`. Missing-daemon errors tell the caller to
run `sidera daemon`; commands never start it automatically. Usage errors write
a diagnostic to stderr and return 2 before any method call, in every mode,
including `--json`; no error body is emitted for usage errors.
Initialization and foreground-daemon diagnostics retain the daemon's original
explanation on stderr.

`mcp` reserves stdout for MCP messages even when `--json` is set. Process failures
are diagnostics on stderr with the shared category exit code; a normal stdin
EOF exits zero. Tool and RPC failures remain in MCP responses and do not terminate
the process. The dispatcher owns and closes its local Agent client; the MCP peer
owns and closes stdin/stdout. A separate probe credential enables the connection
probe without granting access to endpoint methods.

With `--json`, Agent success is its response object, management success is the
complete response including `operation_id` and `replayed`, and method errors are
`{"error":{...}}` on stdout. A receive is the exception in both modes: its page
is written unchanged, including the node's existing newline, without a wrapper
or re-encoding. Receiving never acknowledges the new page. `--ack` asks the node
to acknowledge a previous lease before the receive.

Human output is intentionally small: send and reply print
`sent <message-id> to <address>`; message status prints one recipient per line;
me prints identity, declaration and limits; lists use aligned columns. Scripts
use `--json`. Initialization puts the offline-backup reminder first; JSON init
returns `node`, `home`, `self_revocation` and `backup_required:true`.
Human node status prints outstanding reminders before the ordinary facts.
Human management changes also print their operation identifier and replay flag.

The [Agent interface](../agent-interface/README.md#errors-deadlines-and-clients)
and `apiwire` own category codes: success is 0, unexpected failure 1, usage 2,
invalid argument 3, unauthenticated 4, not found 5, conflict 6, refused 7,
backpressure 8, unavailable 9, unknown outcome 10. Foreground daemon and init
failures use `daemon.ExitCode`; a fatal storage failure retains 74. All owned
clients and daemon resources are closed before `main` calls `os.Exit`.

## Verification

The executable tests use real processes and checked local IPC with the real
Agent handler and engine fixture. They cover successful receive exit codes,
an interrupted receive followed by a possibly-duplicate retry, two separately
created sessions in one directory, and five 60 KiB messages in English and
Chinese, checking visible parts against acknowledgement counts. The CLI Agent
driver is in [`acceptance/agent/cli`](../../../cmd/sidera/internal/acceptance/agent/cli/README.md).
