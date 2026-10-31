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
| `init [--port <n>]` | Create the private data directory, keys and default configuration through `daemon.Init`; `--port` (default 7744, 1 to 65535; 0 and anything else is a usage error before anything is made) is the port `config/network.json` makes the node listen on, for TCP and QUIC; print the node, home and a reminder to export and back up the self-revocation outside the device. |
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
| `network show` | Show where the node listens, the contact line, the addresses kept for other nodes and which of them are connected. |
| `network address set <node> <multiaddr> ... [--expected-version <n>]` / `network address remove <node> [--expected-version <n>]` | Replace the addresses kept for a node, or remove them; effective at once. |
| `network listen set <multiaddr> ... [--expected-version <n>]` | Replace the addresses the node listens on; the daemon has to be restarted. |
| `trust expect <node>` / `trust forget <node>` | Enter or delete the expectation for a node: the owner means to pair with it, which stands for a confirmation made in advance. |
| `trust reject <pairing_id>` | End a request for a pairing that is stuck or unwanted: reject one this node holds, or withdraw one it started. The identifier is in `trust list`. |
| `trust request <node> [<multiaddr> ...] [--wait <duration>]` | Ask a node for a pairing, dialing the multiaddrs for this request only; prints the verification code and waits for the pairing to complete (default `--wait 10s`, `0` does not wait). |
| `trust list` | List the nodes trusted, the expectations, the requests held and those started, and the nodes removed from trust. |
| `trust remove <node>` / `trust clear <node>` | Remove a node from this node's trust (the other node is not told), or take a removed node back. |
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
`node.show`, `network.show`, `presets.list` or `adapters.list` before issuing the change. Endpoint
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

### Network commands

`network show` prints, in human form, the **contact line** on a line of its own before
anything else, so that it can be copied whole: `<node id> <multiaddr>…`, the addresses
the node listens on that another machine can reach (an unspecified `0.0.0.0` or `::`
is expanded into the machine's interface addresses, loopback and link-local ones are
left out), each ending in `/p2p/<peer id>`. The owner of another node enters it with
`network address set <node id> <multiaddr>…`, which is how a node learns where
another can be reached; the same is done on the other node in the other direction.
No line is printed when the node has no address to give. The rest of the human output
is the common field listing: `listen.configured` (the list in `network.json`),
`listen.actual` (what the listeners are open on now), `node`, `peer_id`, the table of
`peers` (`addresses`, `certificate_held`, `connected`) and `version`. `--json` prints
the whole response, whose `result` is the object documented for `network.show` in the
[daemon reference](../daemon/README.md#the-management-interface).

`network address set` takes the node id and one or more multiaddrs; `network address
remove` the node id; `network listen set` one or more multiaddrs (an empty listen
list is possible only by hand or through the management interface, so a mistaken
command cannot silently stop the node from listening). Each is a versioned change of
`network.json`, read from `network.show` when `--expected-version` is omitted. A
listen change prints, in human form, that the daemon must be restarted for it to take
effect. Refusals are in the words of every change: a multiaddr that breaks the rules
of an address, or the rule that ties addresses to the certificate held for the node
(while none is held one address must end in `/p2p/<peer id>`; once one is held every
`/p2p/` must be its peer), is `invalid_argument/config_invalid` (exit 3) with the path
of the address (`addresses[1]`) and the reason; removing a node that has no address is
`not_found/peer_unknown` (exit 5); a stale version is `conflict/version_conflict`
(exit 6). A missing node or address is usage error 2 before anything is sent. The
commands under `network address` and `network listen` have a third word, which the
command discovery recognises, so their flags and help are their own.

### Trust commands

The commands of `trust` are changes with no version: each chooses one operation
identifier and is resent at most once after an unknown outcome, as every change is.
They take a node id; the node's rules (itself, retired, removed, already trusted) are
the engine's, and the refusals are in the words of every change (see the
[daemon reference](../daemon/README.md#the-management-interface)). A missing node, or
a `--wait` that is not a duration (`10s`, `1m`; a bare number is not one) or is
negative, is usage error 2 before anything is sent. `--json` prints the whole response
for each command.

The refusals that say a pairing cannot be done now each have a code and, as every error,
the exit code of their category: `backpressure/pairing_paused` (exit 8; pairing stopped
after code mismatches were reported, and the message says in how long to try again),
`conflict/pairing_in_progress` (exit 6; a pairing with that node is under way, or the
same `trust request` is still running), `refused/expectations_full` (exit 7; forget an
expectation first) and, for `trust request` only, `unavailable/node_unreachable` (exit 9;
the node could not be dialed, or the pairing ended with neither a saved request nor the
target's refusal; nothing is stored, so the command may be run again). A node with no
expectation for `trust forget` is `invalid_argument/malformed_request` (exit 3, field
`node`).

`trust list` prints five sections (`trusted`, `expectations`, `incoming`, `outgoing`,
`locally_revoked`), one line for each entry, and `none` for an empty section; times
are written as the interface gives them.

Two nodes pair by the owner of each entering an expectation for the other, so the order
is: each side runs `trust expect <the other node>` first, then one side runs `trust
request <the other node>`. A request that reaches a node that does not expect the
requester waits for its owner, and no command confirms it: the owner either enters the
expectation too (which does not apply to a request already waiting) and asks again, or
ends the request with `trust reject <pairing_id>`; the same command withdraws a request
this node started that waits for an expectation on its own side.

`trust request` is answered by the node when the online segment of the pairing ends,
which can take most of a minute. A refusal by the target is a successful answer with
`status` `refused`; the command turns it into the error `refused/pairing_refused`
(exit 7, with `details.node` and `details.refusal`) and prints no code. Otherwise it
prints, first and on a line of its own, `Verification code: <code>`, which is
informational, then two lines that say trust is complete when both nodes expect each
other and that a request that is stuck is cancelled with `sidera trust reject
<pairing_id>`, then the fields of the answer. `--wait` is
the client's alone and is not sent: unless it is `0` (or the answer is already
`complete`), the command then reads `trust.list` at once and every 500 ms until the
node is trusted or the outgoing request with the answer's `pairing_id` is `complete`,
and prints `paired with <node>`. The time waited is counted as the intervals slept.
A request that ends otherwise (its state is `rejected`, `expired`, `cancelled`,
`unknown`, `gave_up`, `withdrawing` or `withdrawn`) stops the wait, and a wait that runs out
ends the command, each with exit code 1 and a message that names the node, the pairing
identifier and the code; the message for a wait that ran out says that trust is complete
when both nodes expect each other, so the owner is to check `sidera trust list` on both,
or cancel the request with `sidera trust reject <pairing_id>`. The pairing itself goes on
either way. With `--json` the answer is printed as soon as it
arrives and the wait changes only the exit code.

Enabling an adapter returns before background startup and probing finish. Human
output says `enabled, probing in progress; see sidera adapter list`; that list
reports the probe outcome. JSON keeps the complete management response.

The identity export first uses the management interface. Only
`daemon_not_running` permits the offline path through `keys.ReadSelfRevocation`
and `keys.FormatRevocationExport`; peer-check failures and unknown outcomes are
reported without an offline read. Without `--out`, human export writes the
armored statement to stdout. Both modes print the digest and offline-backup instructions to stderr;
JSON export has the same `result` shape online and offline.

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

Human `--out` prints only the digest to stdout. With `--out`, `--json` prints
`{"result":{"digest":"...","file":"..."}}`, using the supplied file path.
The armored statement never goes to stdout in this case: possession of it can
retire the node, so selecting a file also keeps it out of captured output and
conversation records. Without `--out`, `--json` preserves the complete response,
including `armored` and `digest`. Backup instructions remain on stderr in both
modes.

## Output and errors

Human success goes to stdout. Agent and management failures go to stderr as
`error: <category>/<code>: <message>`. Missing-daemon errors tell the caller to
run `sidera daemon`; commands never start it automatically. Usage errors print
`error: <message>` on stderr and return 2 before any method call, in every mode,
including `--json`; no error body is emitted for usage errors. Human initialization
and foreground-daemon failures print `error: <err>` on stderr, retaining the
daemon's original explanation.

Their JSON failures use the shared
`apiwire` mapping for typed daemon and IPC errors: stable codes identify a held
instance lock, an initialized or uninitialized directory, an unusable directory,
a socket path that is too long, an engine that did not stop, a fatal daemon
storage failure, or a listener missing its lock. Messages retain actionable
advice without arbitrary wrapper or cause text; the
[daemon reference](../daemon/README.md#errors-and-command-output) lists them.
These local commands keep lifecycle exits in both modes: 0 for normal
completion, 1 for ordinary failure and 74 for fatal storage failure.

`mcp` reserves stdout for MCP messages even when `--json` is set. Process failures
are diagnostics on stderr with the shared category exit code; a normal stdin
EOF exits zero. Tool and RPC failures remain in MCP responses and do not terminate
the process. The dispatcher owns and closes its local Agent client; the MCP peer
owns and closes stdin/stdout. A separate probe credential enables the connection
probe without granting access to endpoint methods.

With `--json`, Agent success is its response object, management success is the
complete response including `operation_id` and `replayed` (except the file-export
summary above), and method errors are `{"error":{...}}` on stdout. A receive is the exception in both modes: its page
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

## Tagged test process controls

Only `crashtest` builds read `SIDERA_TEST_SEED`, `SIDERA_CRASH_GEN`,
`SIDERA_CRASH_AT` and `SIDERA_CRASH_RECORD`. The seed is hashed with a versioned
domain, command role (`init`, `daemon`, or `cli`) and process generation, then
feeds a locked standard-library ChaCha8 reader. Empty seeds use system randomness;
fixtures give individual CLI calls distinct seeds. Generation defaults to 1 and
must be positive. A target requires a record file. Each process owns a separate
file opened with `O_APPEND`; raw hit and crash-marker JSON lines are described in
[the daemon reference](../daemon/README.md#tagged-process-crash-records). A receive
reaches `proc/cli.recv-after-response` only after the HTTP page is complete and
before any model stdout.

The daemon role reads its record, target and generation together and immediately
removes `SIDERA_CRASH_RECORD`, `SIDERA_CRASH_AT` and `SIDERA_CRASH_GEN` from its
own process environment. This happens before validation and before any adapter
starts. The adapter host takes the current environment on each launch, so
adapters, native runtimes and their CLI descendants cannot reuse the daemon's
record or target. Test seed and clock inputs remain inherited. An explicitly
configured CLI process still accepts its own crash variables; ordinary builds
retain their existing environment behavior.

Only `testclock` builds read `SIDERA_TEST_CLOCK_START_MS`. A supplied signed Unix
millisecond origin selects one fake clock shared by the daemon and adapter host;
without it they use system time. Management `test.clock.set` changes this clock
by absolute milliseconds and permits rollback. The tags are independent and
ordinary builds ignore all these variables.
