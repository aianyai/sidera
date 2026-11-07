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
Data-directory selection is `--home`, then `SIDERA_HOME`, then the default of the
platform, `~/.sidera`, or `%LOCALAPPDATA%\Sidera` on Windows; the selected directory is
made absolute. The rule is `datahome.Resolve`, shared with the console bridge, and a
command resolves the directory only when it uses one, so `version` and `--help` work
where the default is refused. On Windows the default is refused, with the
`Move-Item` command that moves it, when `%LOCALAPPDATA%\Sidera` does not exist and the
former default `%USERPROFILE%\.sidera` does: nothing is moved or made, because a node
that started empty beside the one that holds the identity would be worse than none.
`--home` and `SIDERA_HOME` are not subject to the check. The reason for the place is
that a protected list on a folder of `%LOCALAPPDATA%` does not take the entries that a
sandbox's setup adds to the first-level folders of `%USERPROFILE%`. Each command's help describes its
arguments, purpose and one example.

| Command | Purpose |
| --- | --- |
| `init [--port <n>]` | Create the private data directory, keys and default configuration through `daemon.Init`; `--port` (default 7744, 1 to 65535; 0 and anything else is a usage error before anything is made) is the port `config/network.json` makes the node listen on, for TCP and QUIC; print the node, home and a reminder to export and back up the self-revocation outside the device. |
| `daemon` | Run the composition root in the foreground with the executable adapter launcher; the daemon owns signal handling and orderly shutdown. |
| `me` | Read the authenticated session's address, declaration and front-end limits. |
| `declare [--paused=<bool>] [--refuse-multi-recipient=<bool>] [--restrict-senders=<bool>] [--accept-from <addr>,...] [--hidden=<bool>]` | Send only the fields explicitly supplied; `--accept-from=` clears the list. |
| `peers` | List the node itself and the nodes it trusts directly: `NODE`, `SELF`, `TRUST` (`self` or `direct`) and `CONNECTED` per line. |
| `directory [--node <id>] [--limit <n>] [--cursor <cursor>]` | Read one directory page. |
| `send --to <addr> [--body <text> \| --body-file <file>] [--content-type <type>] [--thread <id>] [--accept-before <seconds>] [--key <key>]` | Send a message to exactly one recipient; a comma-separated list is passed on and refused by the Agent boundary with `invalid_argument/single_recipient_only` (exit 3). |
| `reply <message-id> [--body <text> \| --body-file <file>] [--content-type <type>] [--thread <id>] [--key <key>]` | Reply to a received message. |
| `recv [--wait <seconds>] [--msg <id> --part <k>] [--ack <lease>]` | Receive a page with front end `cli`; `next.msg` and `next.part` in a page become `--msg` and `--part`. |
| `ack <lease> [<message-id>:<part> ...]` | Confirm the visible parts named, or the whole page when no parts are listed. |
| `status [<message-id>]` | Read node status, or the named message's delivery state and eventual control reply. Node status carries one field the daemon does not send: `home`, the data directory this command resolved and reached, so the owner can confirm which node a command talks to. |
| `control <addr> <signal> [--preset <name>] [--body <text> \| --body-file <file>] [--content-type <type>] [--accept-before <seconds>] [--key <key>]` | Send a control signal, optionally with an initial message for session creation. |
| `probe` | Present `SIDERA_PROBE_TOKEN` with front end `cli`. |
| `mcp` | Serve the Agent tools over stdio MCP; see [the MCP reference](../mcp/README.md). |
| `setup …` | Not a command. Any form of it (`setup`, `setup controller`, with any flags) is a usage error, exit 2, whose message says it is not a command and that `sidera adapter setup <instance>` sets up the client of an adapter instance (Claude Code, Codex, Hermes). It reads no argument and needs no node. |
| `version` | Print `sidera <version> (commit <commit>, go<version>)`, with `modified` after the commit when the build's checkout had uncommitted changes; with `--json`, the object `{"version", "distribution", "commit", "modified", "go_version"}` (`commit` is left out when the build carries none, and `distribution` when the build states none; the programs of a release archive report `archive`). It needs no running node and takes no arguments. A release build reports the release version; a build without it reports `0.0.0-dev` (see [`internal/release`](../../../internal/release/README.md)). |
| `update [--check] [--version <version>] [--yes]` | Bring this installation to a release: the programs next to the running one, the running daemon and the installed adapters, doing only the steps that are missing. See [Updating](#updating). |
| `daemon stop` | Ask the running daemon to shut down after returning the accepted result. |
| `daemon restart [--expect-version <version>] [--wait <duration>]` | Ask the running daemon to shut down and start the program again as it was started, then wait for the new daemon (default `--wait 2m`, `0` returns once the restart is accepted). See [Restarting the daemon](#restarting-the-daemon). |
| `alerts list [--all]` / `alerts ack <id>` | List alerts, optionally including acknowledged ones, or acknowledge an alert. |
| `preset list` / `preset add <name> --file <file> [--expected-version <n>]` / `preset remove <name> [--expected-version <n>]` | Read or change the preset collection. A file of `-` reads stdin. |
| `adapter install <package-directory>` / `adapter list` | Install a local package by its absolute path (another version, like the same version with other contents, replaces the installed package and restarts the instances that run, which keep their sessions; the same contents change nothing), or list installed packages, instances, probe results, the `fault` reason of an installed package that cannot be loaded or of an enabled instance whose package cannot run, and the `availability` conditions an online instance's adapter reports (a table cell of JSON in the human form). |
| `adapter enable <id> [--instance <name>] [--config <file>] [--expected-version <n>]` / `adapter disable <instance> [--expected-version <n>]` | Enable or disable an instance. A config file of `-` reads stdin; `enable` takes one only for an instance that does not exist yet. |
| `adapter remove <instance> [--force] [--expected-version <n>]` | Remove a disabled instance and its settings. Refused while the instance is enabled and while session endpoints made through it are open (the message says how many). The adapter's state directory is deleted with the instance (on Windows the removal is refused, naming the path, while a process still holds a file in it); the instance's endpoint ids stay. Refused (exit 1) while the adapter's setup has recorded something in a client, and when it cannot be found out whether it has (`--force` skips the check, and is offered only in the second case); see [Adapter setup](#adapter-setup). |
| `adapter setup <instance> [--check \| --remove \| --refresh] [--yes] [-- <adapter arguments>]` | Run the setup of the instance's adapter, which brings the client the adapter drives (its MCP registration, permissions, Skill) in line with the instance; see [Adapter setup](#adapter-setup). |
| `adapter config <instance> --config <file> [--expected-version <n>]` | Replace the whole configuration of an instance that exists, enabled or not; `-` reads stdin. A running instance is restarted to take it. |
| `endpoint list [--instance <name>]` / `endpoint close <addr> [--allow-attached]` | Read endpoints, including closed ones (`origin` says `created` or `attached` for a session), or close an endpoint. An attached session, which a person is using, is closed only with `--allow-attached`; without it the command fails with `refused/attached_close_not_allowed`. |
| `endpoint set <addr> <field>=<json> ... [--expected-version <n>]` | Change the endpoint's settings. |
| `node show` / `node set <field>=<json> ... [--expected-version <n>]` | Read or change node settings. |
| `network show` | Show where the node listens, the contact line, the addresses kept for other nodes and which of them are connected. |
| `network address set <node> <multiaddr> ... [--expected-version <n>]` / `network address remove <node> [--expected-version <n>]` | Replace the addresses kept for a node, or remove them; effective at once. |
| `network listen set <multiaddr> ... [--expected-version <n>]` | Replace the addresses the node listens on; the daemon has to be restarted (`daemon restart`). |
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

### Controller sessions

An agent command run inside a session a person opened works without `SIDERA_TOKEN`.
The environment shows such a session: `CLAUDE_CODE_SESSION_ID` (Claude Code; with
`CLAUDECODE` alone the session id is unknown and the command fails with
`unauthenticated/token_missing` and a message saying so) or `CODEX_THREAD_ID` (Codex);
Claude wins when both are set. A process with `SIDERA_TOKEN`, or with
`SIDERA_PROBE_TOKEN` (an adapter's probe), is never a controller, and a plain terminal
keeps `token_missing` from the node.

Once its arguments have been checked, and just before it first calls the node, the command
calls the management method `sessions.attach` (so a command refused for its arguments attaches
nothing; see
[the daemon reference](../daemon/README.md)) with the adapter id (`claude` or `codex`),
the native session id, the display name `<Claude|Codex> controller (<base name of the
working directory>)` (the base name is left out when it holds a character a display name cannot, such as a line break) and, for Claude, the wake keys `CLAUDE_CODE_MESSAGING_SOCKET`,
`CLAUDE_CODE_MESSAGING_TOKEN` and `CLAUDE_PID` copied from its environment when present.
The call is idempotent. The Agent client takes the token from the answer in memory: it is
never put into the environment, argv, a log or a file. A refusal is the command's error,
with words for the cases a person can act on: no enabled instance of the runtime's
adapter (`not_found/instance_unknown`), an adapter that does not take attached sessions
(`refused/attach_unsupported`), a native session Sidera created
(`refused/native_session_created`) and several instances to choose from
(`invalid_argument/instance_required`). When the answer says the adapter cannot wake the
session, one line on stderr says why and that `recv` with a wait inside a turn is the way to collect replies.
A command attaches once per run and nothing repeats it between commands. Every attachment
carries the wake keys, so the node can wake a session that only uses commands, and it
keeps the endpoint for a queue time after the last command (30 minutes by default, never
less than 3 minutes) when nothing is queued or leased on it; `sidera mcp` repeats the
attachment every minute, so a session that has it keeps its endpoint while it is open. The
wake values are kept in the node's memory only: for Claude they are lost when the node
restarts, and a session that only uses commands cannot be woken until its next command.
The management commands are unchanged.

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

### Restarting the daemon

`daemon restart` is a change like any other: one operation identifier, one resend after
an unknown outcome. It sends `daemon.restart` (with `expect_version` when
`--expect-version` is given; the daemon refuses when the program it would start
reports another version, which is how a program that was just replaced is told from
the old one still on disk), and then waits, on the client's side only, until `status`
answers `running` with a `daemon.start_id` other than the one the daemon gave in its
answer. The old daemon may still answer for a moment, then nothing does while the
program starts again, then the new daemon answers while it recovers; all of these, an
answer that was cut off included, are "not yet". Any other failure of a read (the new
daemon refuses this user, for one) ends the command with that error, because waiting
does not mend it. `--wait` is a duration (`30s`, `2m`); a bare number or a negative
one is usage error 2 before anything is sent, and the time waited is counted as the
intervals slept (200 ms), as for `trust request`.

The human output is the `key value` lines of the other changes: `restarting`, `program`
(the path that is started), `version` (what the program reported when the daemon ran
it before accepting) and `previous_start_id`, printed as soon as the daemon has
answered; then, once the wait has seen the new daemon, `running true` and its `start_id`;
last `operation_id` and `replayed`. With `--json` one object is written when the wait is
over: `{"operation_id","replayed","result":{"restarting","program","version","previous_start_id","running"?,"start_id"?}}`,
the last two left out when `--wait` is `0`.

The daemon refuses before it shuts anything down, with `refused/restart_unavailable`
(exit 7: it was not told how it was started, or what is started would not outlive it on
Windows), `refused/restart_check_failed` (exit 7: the program failed to answer
`version`) or `conflict/restart_version_mismatch` (exit 6); it then keeps running. A
wait that runs out is a plain failure, exit 1, whose message names the daemon log
(`<home>/logs/daemon.log`) and says to start the daemon the way it was started when it
is not running: a program that fails after the check, at its start, can only be seen
there. `daemon restart` is not the foreground `daemon` command and never runs one: the
command dispatcher treats `daemon stop` and `daemon restart` as requests to the running
daemon. How the foreground daemon starts the program again is in
[the daemon reference](../daemon/README.md#restart).

### Updating

`update` is carried out by the command itself, with the management interface only for the
daemon (`status`, `daemon.restart`, `adapters.list`, `adapters.install`, which a client
that ignores unknown members of the answers reads across releases) and, for the refresh of
the adapters' client setup, `adapters.setup_info` and the setup programs it names, run
exactly as `adapter setup --refresh` runs them. What it does, what it
refuses and why is [the update reference](../update/README.md).

`--version` takes a release version with or without a leading `v`; anything else is usage
error 2. Without `--check` and `--yes` the command needs a terminal on standard input
to ask on, and refuses with usage error 2, before any request, when there is none.
`--check` needs neither, reads a release and writes nothing; it exits 0 once it has read a
release it can believe, whether or not an update is available or admitted, and exits 1 when
it cannot reach or believe a release.

The plan, the question and the progress go to standard error, so that standard output is
the report. The human report is `key value` lines: before the question `current`, `target`,
`release` (the page of the release), `replace`, `restart daemon`, `install adapters`,
`faulted adapters, installed again`, `faulted adapters not in this release (install them
again by hand)` and, while a daemon runs, `refresh the client setup that adapters recorded,
where it is out of date`; after the run `program updated` (the paths), `daemon restarted version` or
`daemon running version` or a note that none runs, one `adapter <id> version <v> replaced
<bool>` (or `skipped`, or `failed`; `was faulted, installed again` for an adapter whose
package could not run) per installed adapter, and a note that the `.old` programs are the
way back and that the old version directories of replaced adapters are removed at the next
daemon start, and one `setup <instance> <status>` line per adapter instance (`refreshed`,
`none`, `drift`, `skipped` or `error`, with the reason after `skipped` and `error`),
followed by `setup <instance> item <id> <status>` for each item the refresh did, `setup
<instance> drift <id> <problem> <summary>` for each difference it found and, for drift,
`setup <instance> fix sidera adapter setup <instance>` (with `--home` before `adapter` when the data directory is not the default). An `error` is exit 1, drift is not. A check looks at the release and the running program only (not the daemon, the adapters
or their setup): it prints `current`, `target`, `release`, `floor`, `update_available`,
`admitted` and, when a run would stop, `refusal`, and with `--json` it is one object of
exactly those members. A run with `--json` is one object: `current`, `target`, `release`,
`admitted`, `floor`, `already_current` (never true while no daemon runs, nor when a refresh rewrote an item or failed), `program`
(`updated`, `replaced`, `console_missing`, `restored`, `left`), `daemon` (`running`,
`previous_version`, `restarted`, `version`), `adapters` (each with `faulted` when its
package could not run) and `setup` (per instance: `instance`, `adapter`, `status`,
`message`, `items` as `{id, status, message}`, `drift` as `{id, problem, summary}` and
`fix`, the last four only when they have something to say), and `error` when the run failed after it had done something (the
exit code is then 1; a failure before anything was done prints the error on standard error
only).

### Adapter setup

`adapter setup <instance>` runs the setup program of the instance's adapter. The contract
between the command and that program, and the reasons for it, are
[the adapter-setup reference](../adapter-setup/README.md); this is the command's side.

It needs the daemon: it calls `adapters.setup_info` for the instance, which gives the setup
program as an absolute argv, the instance's state directory and the values the owner set,
and runs the program with one request on its standard input (no shell, its own working
directory and environment, at most five minutes). `sidera_program` in the request is the
absolute path of the program as it was launched, links kept: the file `update` replaces
in place, so a client's configuration that names it stays valid across an update and a
link that a package manager repoints keeps leading to the current version; only when how
the program was launched is not known, `os.Executable` made absolute. `update` and
`adapter setup` use the same path. A program in a temporary directory is warned about
on standard error in the modes that write the path into a client's
configuration (plain setup and `--refresh`). `home` is the data directory the command
resolved, and `home_is_default` says whether it is the platform default. An adapter that
declares no setup is a usage error (2): `the adapter of <instance> has no setup`. A daemon
that does not know `adapters.setup_info` (older than this command) is a failure (1) that
says to update it.

Modes (at most one; the flags are checked before the daemon is called, and a violation is a
usage error, 2):

| Invocation | What it does | Exit |
| ---------- | ------------ | ---- |
| `adapter setup <instance>` | Runs `plan`, shows the items on standard error, asks `<summary>? [Y/n]` for each item that needs consent (Enter, `y` and `yes` accept; `n` and `no` decline; any other text and the end of the input decline), then runs `apply` with the ids of the accepted items and of every item that needs none. Nothing accepted and nothing that needs no consent means no `apply`, unless something was declined: then one `apply` with no accepted ids hands the declines over; an empty plan is `nothing to do`. The items answered no, the ones left alone because an item they require was declined and the ones that need no consent and that only declined items require (none under `--yes`), go in `declined` of the `apply`; the adapter remembers them so that `--check` and `update` do not report them again, and the dialogue says `declined: <summary>`; running the command again offers them again. An item that `requires` an item that was declined is not asked about and not applied, and the listing says `left alone`; so is an item that needs no consent when items require it and none of them is going ahead, so declining every item leaves nothing recorded; an item whose required item did not complete is applied in a later call only if it did, and is otherwise reported `skipped` with the reason | 0; 1 when an item failed |
| `--remove` | The same with `remove`, then `apply` with `undo`: asks for each recorded item the setup would undo. When an item failed or was left alone, standard error ends with a hint: items that cannot be undone (the client program is no longer installed, or the item changed since setup) stay, `adapter remove <instance> --force` removes the instance anyway and leaves them in the client, and the guide's "Cleaning up an adapter's client by hand" lists what to clean up. The refusal of `adapter remove` for a recorded setup (below) still points to `--remove` alone | as above |
| `--check` | Runs `check` and reports; changes nothing | 0 with no drift, 1 with drift. An adapter with nothing to set up and no record is `none`, not drift; one that wants something and has no record reports it as drift (`needs_setup`) |
| `--refresh` | Runs `refresh`: brings the recorded items that still exist up to date, asks nothing. It is the mode `sidera update` uses after it installs the adapters | 0, or 1 when something failed or has drifted |
| `--yes` | With the first two modes: accepts every item without asking | |

Without `--yes` the first two modes need a terminal on standard input, and refuse with usage
error 2, before any request, when there is none: the same rule and words as `update`.
`--yes` with `--check` or `--refresh` is a usage error, since they ask nothing. The refusal
is one helper's, so `update` and `adapter setup` say it in the same words. The
questions are asked one item at a time from one reader, so a line of answers is one answer
per item; anything but `y` or `yes` is a no, and so is the end of the input. The words after
the first `--` are the adapter's own arguments, in `options` of the request as typed
(flag-looking words included).

The plan and the questions go to standard error; standard output is the report, `key value`
lines: `instance`, `status`, `recorded` (check and refresh), `nothing to do` or `nothing
applied`, one `item <id> <status> [<message>]` per outcome and one `drift <id> <problem>
<summary>` per difference, then (check only) one `note <text>` per note of the adapter, which is
advice and changes neither the status nor the exit; a plan's notes are shown on standard error
as `note: <text>` before the questions, also when the plan has nothing to ask. With `--json` it is one object: `instance` and the members of the
program's last result (`version`, `op`, `status`, `recorded`, `items`, `outcomes`, `drift`, `notes`).
The last result is that of `apply` when items were applied and otherwise that of `plan` or
`remove`. A setup program that exits non-zero, writes something that is not a result, or a
result of another version, with a `status` this program does not know, or without `recorded`
for a check or refresh, is a failure (1) that names the instance and the program and, for an
exit, quotes the end of its standard error (the last 2 KiB). A value of `action`, `problem`
or an outcome's status that a later release added is not refused: it is shown as it is
(the last as a failure, with the adapter's message, and counted in the verdict), so that an
old program can run the setup of an adapter that `update` has just installed.

`adapter remove <instance>` asks the same setup first: when the adapter declares a setup and
its `check` says a record exists, the removal is refused (exit 1) and the message says to run
`adapter setup <instance> --remove`. The state directory goes with the instance, and with it
the source of what setup installed in a client; the client's Skill would vanish without a
word, which is why that refusal does not offer `--force`. When the check itself cannot be
done (the package cannot be loaded, the setup program fails), the instance is not removed
and the error is shown after "the instance was not removed, because", as a failure (1); this
refusal names `--force`: `adapter remove <instance> --force` skips the check, so that an
instance whose setup cannot run is not stuck. It removes the instance as usual and, once that has succeeded, prints on
standard error a warning that the client setup of the instance was not checked, that
whatever it installed in the client is still there, and where the guide describes the manual
cleanup ("Cleaning up an adapter's client by hand"). An instance or adapter the daemon does not know, a daemon without
`adapters.setup_info` and an adapter without setup are not checked, and the removal goes on as
before.

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
the exit code of their category. Of `trust request` only: `backpressure/pairing_paused`
(exit 8; pairing stopped after code mismatches were reported, and the message says in
how long to try again), `conflict/pairing_in_progress` (exit 6; a pairing with that node
is under way, or the same `trust request` is still running) and
`unavailable/node_unreachable` (exit 9; the node could not be dialed, or the pairing
ended with neither a saved request nor the target's refusal; nothing is stored, so the
command may be run again). Of `trust expect` only: `refused/expectations_full` (exit 7;
forget an expectation first). Of `trust request` and `trust reject`, while the node may
not sign: `backpressure/clock_rollback` (exit 8; the owner has to clear the pause of the
clock) or `unavailable/identity_retired`. A node with no expectation for `trust forget`
is `invalid_argument/malformed_request` (exit 3, field `node`), and so is a pairing
identifier for `trust reject` that names no request that still waits (field
`pairing_id`).

The addresses given to `trust request` are dial hints for that pairing only: they are
not saved to the address book. After the pairing each side needs `network address set`
for the other node before messages can flow.

`trust remove` does not tell the other node. Pairing again with a node removed and then
taken back with `trust clear` is refused by that node (`refused/pairing_refused` with the refusal `not_accepted`)
for as long as it still holds the credential, so its owner has to remove the node too
(`trust remove`, then `trust clear`) before the pairing can be made again.

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

A command that cannot connect to the daemon's local endpoint (the Agent socket for
the agent commands, the management socket for the owner's) says where it tried and
why, in the `unavailable` category (exit 9), never as `internal`. This is the same
for every command and for `mcp`, because they share one dial path
(`ipc.Dial`, mapped by `apiwire.FromError`):

| The connection fails because | Code | Message |
| ---------------------------- | ---- | ------- |
| the socket or pipe does not exist (`ENOENT`, pipe not found) | `daemon_not_running` | `cannot reach the Sidera daemon at <path>: no such file (is the daemon running? start it with `sidera daemon`)` |
| nobody accepts on the socket (`ECONNREFUSED`) | `daemon_not_running` | `cannot reach the Sidera daemon at <path>: connection refused (is the daemon running? start it with `sidera daemon`)` |
| the system refuses the user (`EACCES`, `EPERM`, `ERROR_ACCESS_DENIED`) | `peer_check_failed` | `cannot reach the Sidera daemon at <path>: permission denied (a sandbox may be blocking it; otherwise another user holds this endpoint)` |

A sandbox that denies the connection is the usual cause of the last row: the user's
own socket always lets the user connect. A failure that is none of these three stays
`internal/internal`, whose text carries no path or cause.

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
Human node status prints outstanding reminders before the ordinary facts, and its
listing includes the line `home <directory>`. With `--json` the management response is
written with `result.home` added and every other field as the daemon sent it, in the
keys' sorted order. The CLI adds `home` itself because the daemon does not report its
directory and the command already holds the one it resolved.
Human management changes also print their operation identifier and replay flag.

The [Agent interface](../agent-interface/README.md#errors-deadlines-and-clients)
and `apiwire` own category codes: success is 0, unexpected failure 1, usage 2,
invalid argument 3, unauthenticated 4, not found 5, conflict 6, refused 7,
backpressure 8, unavailable 9, unknown outcome 10. Foreground daemon and init
failures use `daemon.ExitCode`; a fatal storage failure retains 74. All owned
clients and daemon resources are closed before `main` calls `os.Exit`. The adapter programs map
into the same table; see [Conventions of every program](#conventions-of-every-program).

## Conventions of every program

Sidera has five programs: `sidera`, and the adapter programs `sidera-adapter-console`,
`sidera-adapter-claude`, `sidera-adapter-codex` and `sidera-adapter-hermes`. They share the
conventions below. Where one still differs, the difference is stated in the paragraph that
states the rule; the differences are known and unfinished, and none of them is a second rule.

**Exit codes.** There is one table, the one in [Output and errors](#output-and-errors):

| Code | Meaning | Used by |
| ---- | ------- | ------- |
| 0 | success | all |
| 1 | an ordinary failure | all |
| 2 | usage error: unknown command or flag, a missing or surplus argument | all |
| 3 | `invalid_argument` | `sidera` |
| 4 | `unauthenticated` | `sidera` |
| 5 | `not_found` | `sidera` |
| 6 | `conflict` | `sidera` |
| 7 | `refused` | `sidera` |
| 8 | `backpressure` | `sidera` |
| 9 | `unavailable` | `sidera` |
| 10 | the outcome is not known (yet) | `sidera`, `sidera-adapter-console` |
| 74 | a fatal storage failure | the foreground daemon and `init` of `sidera` |

An adapter program keeps to this table: it uses 0, 1 and 2, and the console's client also uses 10
for what `sidera` calls an unknown outcome (`new` got no reply within its `--timeout`, or the
session is not confirmed). It does not sort a refusal of the node into the categories above; a
refusal is 1 and its words are on standard error. A code is never given a second meaning in one
program: 3 is `invalid_argument` and nothing else. (The one exception is not a command line: on
Windows `sidera-adapter-claude` is started as a helper of the SDK's interrupt with arguments of
its own, which `process.HandleHelperInvocation` answers before any command is read; its exit code 3
means the process to interrupt is not running, and no person types that invocation.)

**Global flags.** Only `sidera` has global flags: `--home <directory>`, `--json` and `-h`/`--help`,
before or after the command (see [Commands](#commands)). The adapter programs have none. The
client commands of the console take `--home` and `--instance` after the command name only (a known
difference: `--home` before the command is a usage error). A flag of the adapter programs is never accepted before the command.

**`--json`.** Every command of `sidera` that prints a result takes it (`adapter setup` writes the object described in [Adapter setup](#adapter-setup)), except `mcp`, where it has no effect, and `recv`, which writes the node's page unchanged in both
modes (see [Output and errors](#output-and-errors) for the shapes). Of the adapter programs only
`version` takes it. `version --json` writes one object and a newline on standard output, with the
same members from every program (`sidera`, and each adapter program):

```
{"version":"0.15.0","distribution":"archive","commit":"0123456789ab","modified":false,"go_version":"go1.26.0"}
```

`distribution` is left out when the build states none and `commit` when it carries none; `modified`
and `go_version` are always there. The object does not name the program. `--json` on any other
adapter command is a usage error (a known difference: the adapter programs have no machine-readable
output of their commands yet).

**Help.** `-h`, `--help` and (for the adapter programs) `help` print the usage on standard output
and exit 0. With `sidera` the flag may follow any command (`sidera send --help` prints that
command's usage, an example included, and a command that does not exist is a usage error). In an
adapter program `--help` after `package` or `version` prints that command's usage, and so does it
after `setup` of an adapter program; after a client command of the console it prints the usage of
the program, which lists every command. `serve` is the exception: it takes no argument, `--help`
included, because it is the role the daemon starts and has no usage of its own. Running a program
with no argument is a usage error (2): the usage goes to standard error (`sidera` prints only
`error: a command is required; use --help`), and an unknown command is a usage error (2) that names
it. A program therefore never starts doing something when it is run with nothing to do: `serve`
has to be named, and the manifest of every adapter says so in `entry.args`. `sidera help <command>`
does not exist.

**`package` and `version` of the adapter programs.** They are one implementation
(`sdk/go/adapterkit`), so they behave alike. `package --goos --goarch --binary --out` writes the
adapter package of one platform and prints `package: <directory>`; a missing flag, an unknown flag
or any other argument is a usage error; a directory that already holds files, or a platform the
program refuses, is an ordinary failure (1) and nothing is written. Which platforms a program
refuses differs: the Claude program has a fixed list of six (`darwin`, `linux` and `windows`, each
on `amd64` and `arm64`) and refuses any other; the Codex, console and Hermes programs write a
manifest for any operating system and architecture the manifest format knows (`GOOS` and `GOARCH`
values). `version` prints `<program> <version> (commit <commit>[, modified], go<version>)`,
with `commit unknown` when the build carries no commit, and takes `--json` and nothing else.

**Error output.** Standard output carries the result and standard error the errors, of every
program. `sidera` writes `error: <message>`, and `error: <category>/<code>: <message>` for what the
node refused (see [Output and errors](#output-and-errors)). An adapter program writes
`<program>: <message>`. After a usage error what follows depends on the command: an unknown command
prints the usage, and so does a run with no command (without a message); a flag error of a client
command of the console points at `help`; `setup` of an adapter program takes no flag and reads a request, so an argument is the message alone; `serve` with an argument, and `package` and `version` with a flag error, are the
message alone. The words of
a flag parser's complaint differ between the programs, and the message of an error is not a stable
identifier; the exit code is.

**Confirmation.** A command that changes the machine beyond the node's own state asks as `update`
does: by default it shows the plan on standard error and asks one question for the whole plan
(`Update? [y/N]`) on a terminal;
`--yes` answers for it and is required when standard input is not a terminal (the command is then a
usage error, 2, before it does anything); `--check` only reports, writes nothing and needs neither. `sidera adapter setup` asks one question per item, not one for the plan, and the default there is yes (`<summary>? [Y/n]`, in `--remove` too): the person ran it on purpose, so accepting what is listed is the usual answer. `update` defaults to no (`[y/N]`), since it changes the program itself. In both, only `y` and `yes` (any case) accept explicitly, any other text is a no, and the end of the input is a no whatever the default: it is not an answer, so a script or a dropped terminal never accepts a change to a client's configuration.
`identity retire` does not ask and needs `--yes`.

**`-` is standard input.** A file argument of `-` reads standard input: `--body-file -` (`send`,
`reply`, `control` and the console's `new`, `send` and `reply`), `preset add --file -`,
`adapter enable --config -` and `adapter config --config -`.
`send` and `reply` of `sidera` also read standard input when no body flag is given; the console
requires one (a known difference).

Known differences that are about the shape of commands and not about these rules (a flag in one
place and a positional argument in another, the unit of `--wait`, the verbs `show` and `list`, the
names `whoami` and `new`) are not settled here.

## Verification

The executable tests use real processes and checked local IPC with the real
Agent handler and engine fixture. They cover successful receive exit codes,
an interrupted receive followed by a possibly-duplicate retry, two separately
created sessions in one directory, and five 60 KiB messages in English and
Chinese, checking visible parts against acknowledgement counts. The CLI Agent
driver is in [`acceptance/agent/cli`](../../../cmd/sidera/internal/acceptance/agent/cli/README.md).
A restart is tested on the built program on every platform: the daemon is started as a
child, `daemon restart` is run, and the test asserts a new start identifier, the same
node, a daemon that serves again and holds the data directory, and an orderly
`daemon stop` of the new one. On Unix the child is replaced in place and never ends; on
Windows it ends and a detached process takes its place.

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
