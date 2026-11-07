# Changelog

All notable changes to Sidera are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Every program of a
release (`sidera` and the adapters) reports the release version; the adapter
protocol has a version of its own.

## [Unreleased]

### Added

- **Go SDK: `termout`, the terminal output of a program that prints for people.** A new package for
  adapter programs that want their output to look and behave like Sidera's. A `Stream` knows whether
  its output is decorated (markers) and colored, from the output being a terminal and from a color
  mode, `NO_COLOR`, `CLICOLOR_FORCE`, `FORCE_COLOR`, `CLICOLOR=0` and `TERM=dumb`; the package
  gives styles and marker lines, key/value blocks, tables and two-column lists that line up by the
  columns text takes on a terminal, wrapping, `SanitizeLine` and `SanitizeText` for text that came from
  outside, the written form of times, sizes and durations, and `IsTerminal`. It is built for macOS,
  Linux and Windows, and gives plain output elsewhere. The SDK module now requires
  `golang.org/x/text`.
- `sidera --color auto|always|never` (default `auto`; before or after the command) chooses whether the
  program colors its output for people. `auto` colors a terminal and nothing else, and follows
  `NO_COLOR`, `CLICOLOR_FORCE`, `FORCE_COLOR`, `CLICOLOR=0` and `TERM=dumb`; standard output and
  standard error are decided separately. The commands named under Changed are the first to follow it; the others print as before.

### Changed

- **The commands an agent runs in a shell print for reading.** `sidera me`, `peers`, `directory`, `declare`, `send`, `reply`, `control`, `status <message-id>` and `ack` (without `--json`) now print labelled lines and aligned tables with every identifier whole, in place of `key value` lines. `send`, `reply` and `control` say `Message accepted for delivery.` (which is not that it was delivered), print the message id and recipient, and name the `sidera status` command that shows the delivery or the control reply; `status` shows the delivery as a table and a control reply field by field, including the endpoint of a created session and the availability of each preset, where it used to print the reply as JSON; `directory` lists the endpoint's action names (`message.send`, `session.create`) in `ACCEPTS` and puts the address last, and tells you the command for the next page; `me` and `declare` write the declaration in words (`paused`, `accepting`, `listed`, `hidden`, who may send); `peers` and `directory` say so in a sentence when there is nothing more to list. On a terminal or with `--color=always` the lines carry markers and colors, and a pipe gets the same words without them; `ack` shows a refused outcome in yellow. `--json`, `recv`, exit codes and what goes to standard error are unchanged.
- **The commands that read the node print for reading.** `sidera status`, `node show`, `network show`, `preset list`, `adapter list`, `endpoint list` and `alerts list` (without `--json`) now print labelled lines and aligned tables with every identifier whole, in place of sorted `key value` lines and columns that held JSON. `status` starts with what you have to do (the unconfirmed self-revocation backup is a `!` warning) and then the node, daemon, home directory, certificate, clock, deliveries, disk, endpoints and alerts, with sizes, times and spans written out; `node show` names each setting by what it is and gives its unit; `network show` lists the listen addresses and each address that reaches this node on a line of its own, and prints one line, `sidera network address set <node> <address>...`, that you copy and run on the other machine in place of the long contact line; `adapter list` shows the probe result with its time and, under an instance, a `!` line for a package that cannot run and for each condition its adapter reports as unavailable; the lists say `No endpoints.`, `No open alerts.` and the like when empty, and the collection's version is labelled as the value for `--expected-version`. The detail an adapter reports, an alert's subject and detail, a preset's description and the names of its settings are shown with control characters escaped. A preset's settings are listed by name, and their values, the start identifier of the daemon and the address of each endpoint in `status` are in `--json`. `--json`, exit codes and what goes to standard error are unchanged.
- **Go SDK, incompatible: `adapterproto.DisplayNamePattern` is removed.** The expression of a display
  name is now built from the one table of characters a text shown to a person never holds, so it is a
  function: use `adapterproto.PersonTextPattern()` where you used the constant. A description gets
  the same rule with line feed and tab allowed. `adapterproto` gains `RefusedInPersonText` and
  `RefusedInDescription` (one code point each), `ValidatePersonText`, `PersonTextPattern`,
  `DescriptionPattern`, `ValidateDescription` and `ValidateOptionalDescription`. Code that validates
  a display name with `ValidateDisplayName` or `ValidateOptionalDisplayName` is not affected.
- `sidera adapter setup` (and `--remove`) now asks `[Y/n]` for each item: pressing Enter accepts it, since you ran setup to be asked about exactly these changes. `n`, any other text and the end of the input (a script, a closed pipe, a dropped terminal) still decline, so nothing changes a client's configuration unasked. `sidera update` still asks `Update? [y/N]`.

### Security

- The description of an endpoint is now held to the characters of its display name, except that it
  may still span lines and be indented, so line feed and tab stay allowed. A description with any
  other control character (an escape sequence, a carriage return, DEL, a control in the range U+0080
  to U+009F), a line or paragraph separator or a text-direction control is refused instead of kept.
  An adapter's `endpoint.register` fails with an invalid-params error, and a directory answer from
  another node that carries such a description is refused as a whole, as one with such a display
  name already was. A description stored before this change that carries such a character is a
  damaged endpoint record, so the endpoint is dropped at the next start. The adapters shipped with
  Sidera register descriptions without them and are not affected.
- The page that `sidera recv` prints (and the `recv` tool of `sidera mcp`) now writes DEL, the C1 control characters and the text-direction controls in a message as `\uXXXX` escapes. A message from another agent can no longer send terminal control sequences through that page, or reorder the text around it, when the page is shown in a terminal. Any JSON reader still reads the same message; only the bytes of the page differ.
- `sidera-adapter-console recv` no longer writes the body of a received message to your terminal as it came.
  A peer you trust, or an agent on that peer that was misled by a prompt injection, could send terminal
  control sequences that clear your screen, rewrite lines you already read, change the window title or write
  to your clipboard. Control characters, the characters that reorder text, invisible characters that can hide
  text (such as tag characters and the zero-width space) and bytes that are not UTF-8 now appear as visible
  escapes such as `\x1b`; line breaks and tabs are kept. Upgrade the console client.

## [0.15.0] - 2026-11-06

After upgrading to 0.15, run `sidera adapter setup <instance>` once for each Claude Code, Codex
and Hermes instance; later updates keep what it sets up current. Adapters can now declare a
setup program, and `sidera adapter setup` runs it to bring the configuration of the client
they drive (an MCP registration, permissions, a Skill) in line with the instance, after asking.
`sidera update` refreshes what was set up, and `sidera adapter remove` refuses an instance
whose client still depends on its files. The adapter package manifest stays at version 1 with
an optional `setup` member, and the management API gains `adapters.setup_info`. Install the
0.15 adapter packages into a 0.15 daemon, which `sidera update` does in that order: a 0.14
daemon refuses a package that carries the new member. Apart from that member, the adapter
and node-to-node protocols do not change, and nothing a node stores needs a migration.

### Added

- **`sidera adapter setup <instance>`.** An adapter that drives a client with configuration of its
  own can now declare a setup program, and this command runs it: it shows what it would do, asks
  about each change to the client's configuration, and remembers what it did. `--check` reports what
  has drifted (exit 1 when something has), `--remove` undoes it, `--refresh` brings what was
  recorded up to date without asking, and `--yes` accepts everything. Words after `--` go to the
  adapter. The Claude, Codex and Hermes adapters declare one in this release. An item you decline is
  remembered: `--check` and `sidera update` do not report it as still to set up, and running the
  command again offers it again. When `--remove` cannot finish because the client program is no
  longer installed or an item changed since setup, it ends by saying that `sidera adapter remove
  <instance> --force` removes the instance anyway, and where the guide lists what to clean up by
  hand.
- **`sidera update` refreshes the client setup of your adapters.** After it installs the new
  adapters, it asks the adapter of each instance to bring what `sidera adapter setup` recorded in
  your agent client up to date, without asking you, and reports one `setup` line per instance:
  `refreshed`, `none`, `drift` (with the `sidera adapter setup <instance>` command that puts it
  right), `skipped` or `error` (which makes `sidera update` exit 1; running it again retries).
  `sidera update --json` carries the same in a new `setup` array, one object per instance
  (`instance`, `adapter`, `status`, and when they apply `message`, `items`, `drift` and `fix`).
  The step runs on every update that finds a daemon, so an update that was cut short is finished by
  the next. 0.15 is the first release in which adapters have a setup, so an update to it has
  nothing recorded to refresh: after upgrading to 0.15 run `sidera adapter setup <instance>` once
  for each instance whose adapter has a setup; later updates refresh it.
- **`sidera adapter setup claude` sets up Claude Code.** It registers the `sidera` MCP server for
  your user (`claude mcp add --scope user`), adds the rule `mcp__sidera` to `permissions.allow` in
  Claude Code's `settings.json`, and installs a plugin that gives every Claude Code session the
  Sidera Skill, which tells it what Sidera is and how to use it. Each change to Claude Code is asked
  about; `--check` reports a plugin you disabled, a plugin whose source is gone and a
  `settings.json` that Claude Code would ignore; `--remove` undoes it. It never re-points a
  marketplace named `sidera` that is somewhere else, and never edits a `settings.json` that is not
  valid JSON.
- **The Hermes adapter has a setup in `sidera adapter setup`.** Run `sidera adapter setup hermes
  -- --profile <profile>` (and `--platform <platform>` for more platforms): it asks before it
  places the Skill in a profile, remembers that it did, and `--check`, `--refresh` and `--remove`
  work on it. `--refresh` rewrites the Skill when you change the aliases or the board. What a
  profile still lacks (the `kanban` and `skills` toolsets, the Kanban notifier) is shown as a
  note under the plan, to change by hand, and `--check` reports it and exits 1 until you do;
  Sidera changes none of it. The platforms you name are remembered, so `--check` reads the same
  ones.
- **`sidera adapter setup` for Codex.** `sidera adapter setup <codex instance>` installs a Codex
  plugin that gives your own Codex sessions the `sidera` MCP server, with its tools approved in
  advance, and a Skill that tells an agent what Sidera is. It asks before it adds the plugin
  marketplace and installs the plugin, and offers to remove an MCP server `sidera` of your own that
  would hide the plugin's. `--check` also tells you when the plugin is disabled, when its files are
  gone or Codex no longer offers the server, and `--refresh` (what `sidera update` runs) leaves a
  plugin you disabled alone.
- **Adapter packages can declare a setup program.** The manifest's optional `setup` member
  (`{program, args}`, with the shape and path rules of `entry`) names the program that
  `sidera adapter setup` runs. The manifest version stays 1, and a 0.14 daemon refuses a package
  that carries the member, so install 0.15 adapter packages into a 0.15 daemon; `sidera update`
  restarts the daemon before it installs the adapters, so it does this in the right order.
- **The management method `adapters.setup_info`.** Given `{"instance"}`, it answers `{instance,
  adapter, state_dir, config, setup_argv}`: the absolute command that runs the setup of the
  instance's installed adapter, or `null` when the package declares none. `sidera` asks it
  instead of reading the manifest, so that a program replaced by `sidera update` can run the
  setup of a package it has never seen.
- **The Go SDK has what an adapter author needs for a setup.** `sdk/go/adaptersetup` holds the
  contract between `sidera` and a setup program (the request, the result, the record of what
  was done, the remembered declines) and the code every adapter's setup shares: atomic file
  writes that keep a file's mode, a conservative editor for a client's JSON file, and a bounded
  run of a client's command line. `adapterkit.Program` is the command line of an adapter program
  (`serve`, `package`, `version [--json]`, help and usage errors) written once, with the
  adapter's own commands such as `setup` beside them, and
  `sdk/go/guidance` is the statement about Sidera that sessions and Skills carry.
- **`version --json` on every adapter program.** `sidera-adapter-claude`, `-codex`, `-console`
  and `-hermes` write the object `sidera version --json` writes (`version`, `distribution`,
  `commit`, `modified`, `go_version`).

### Changed

- **`sidera-adapter-hermes setup --config <file> --apply` is gone.** `sidera adapter setup`
  replaces it. The Skill you placed with the old command is taken over when it is identical to
  what this release writes, and otherwise replaced after you say so. The copy of the Skill in
  the instance's `skill` state directory is no longer written.
- **`sidera adapter remove` refuses an instance whose adapter has recorded a client setup.**
  Removing the instance deletes its state directory, and with it the files a client installation (a
  Skill, for one) depends on. Run `sidera adapter setup <instance> --remove` first. When the setup
  cannot be asked at all, the refusal offers `--force` to remove the instance without that check and
  leave the client's setup in place (the guide says how to clean it up by hand). Instances whose
  adapter has no setup are removed as before.
- **A session learns what Sidera is the first time it connects.** The `sidera` MCP server's
  instructions used to be one line about the endpoint's limits; they now say that Sidera is the
  message channel to the other agents and people of your cluster, what to use it for, to call
  `recv` first when a notice says a message is waiting, and to acknowledge a page you read (with
  `ack`, or by passing its lease as `ack_lease` to the next `recv`) so that it does not come
  straight back. A Claude Code or Codex session you open yourself, which an adapter never
  briefed, is told this too.
- **The Claude and Codex adapters give their sessions the same statement.** It was written
  twice and had drifted; it is now one text in the Go SDK (`guidance`), plus one sentence for
  each client.
- **`sidera-adapter-claude` and `sidera-adapter-codex` no longer serve when run without a
  command.** They serve only as `serve`, which their package manifests now say; `--help`, `-h`
  and `help` print a usage text, and no argument or an unknown one is a usage error (exit 2)
  instead of a program that silently waits on standard input. Update the adapters with
  `sidera update` (or install their new packages) together with the daemon; a package built
  before this change keeps working as it was.
- **`package` and `version` behave the same in all four adapter programs.** A missing flag or an
  extra argument is a usage error (exit 2), success prints `package: <directory>`, and a package
  directory that already holds files is refused by the Claude and Codex programs too, as it already
  was by the console and Hermes programs.
- **`--help` of the console and Hermes programs writes the usage to standard output.** It went to
  standard error. `package --help` and `version --help` of all four programs print their usage
  and exit 0; they were usage errors.
- **`sidera-adapter-console` exits 10, not 3, when the outcome is not known yet.** That is the
  code `sidera` uses for it (`new` got no reply within its `--timeout`, or the session is not
  confirmed); 3 is `invalid_argument` there. A script that tested for 3 tests for 10 now.
  `sidera-adapter-console <command> --help` prints the usage and exits 0; it was a usage error.

### Removed

- **`sidera setup controller` is replaced by `sidera adapter setup <instance>`.** The old command
  only printed commands for you to run, and what they put in Claude Code's and Codex's
  configuration went stale without notice when the program moved. `sidera adapter setup claude`
  and `sidera adapter setup codex` now make those changes themselves, after asking, remember
  them, and `sidera update` keeps them current. `sidera setup` in any form is now a usage error
  (exit 2) that names the new command. After upgrading to 0.15, run `sidera adapter setup
  <instance>` once for the instance of each client you use as a controller; a server you added
  by hand is adopted or replaced in Claude Code, and in Codex offered for removal when it differs
  from the plugin's server, after you say so.

### Fixed

- **`serve` of the console and Hermes programs takes no argument.** `sidera-adapter-console serve
  --help` started the adapter and waited on standard input; any argument after `serve` is now a
  usage error (exit 2).
- **A usage error of an agent command is reported before anything else.** The agent commands of
  `sidera` (`send`, `reply`, `recv`, `ack`, `status`, `directory` and the rest) and `sidera mcp`
  resolved the data directory and the daemon's socket before they read their arguments, so a
  mistyped command in a broken environment showed the environment's error instead of the usage
  error (exit 2).

## [0.14.0] - 2026-11-05

Sidera now has a Hermes Agent adapter, `sidera-adapter-hermes`, which connects a long-running
Hermes to the other nodes through its own Kanban board. The direct endpoint of an adapter can
now read the directory and the list of peers and set its own declaration, and a page it
receives marks the node's own channel notices, so that a bridge can tell them from a message
another endpoint wrote. The Go SDK exports the delivery timing an adapter reasons about and
names the reasons a send is refused. State written by 0.13.x needs no migration by hand:
nothing a node stores changes, and the Hermes adapter writes only state of its own. The adapter
protocol stays at version 1, extended in place: `endpoint.directory`, `endpoint.peers` and
`endpoint.declare` for a direct endpoint, behind the daemon capability
`direct_endpoint_access` that `initialize` declares, and the member `feedback` of a message on
a page, sent only to an adapter that declares the direct capability `feedback`. The
node-to-node protocols do not change. **The Go SDK has a breaking change**:
`adapterproto.SendRefusedData.Reason` is a `SendRefusedReason` and no longer a `string`. An
installation of 0.12.0 or later updates with `sidera update`, and the nodes of a cluster can
be upgraded one at a time, in any order.

### Added

- **`sidera adapter list` shows what an adapter reports as unavailable.** An enabled, online
  instance carries `availability` (`adapters.list`; the conditions of `adapter.availability`
  with their endpoints and detail), so the owner of a direct-only adapter, such as Hermes with
  a missing board, can see why it is not working.
- **A Hermes adapter.** `sidera-adapter-hermes` connects a long-running Hermes Agent to the
  other nodes through its own Kanban board. A card assigned to `sidera` asks an agent on
  another node to do the work, by starting a session there or by sending a message, and the
  answer is written back to the card; a message sent to the Hermes endpoint becomes a card
  that wakes the Hermes conversation that should read it, and the agent answers with a card
  of mode `reply`. A card names an alias, never an address, and the alias table of the
  instance is the allow-list of what a card may do. It works with Hermes 0.22.2 up to, but
  not including, 0.23.0. To start: `sidera adapter install` the `adapters/hermes` package
  of the release, `sidera adapter enable hermes --config <file>`, then run
  `sidera-adapter-hermes setup` from the package, which writes the Skill that teaches the
  agent to write cards and says which toolsets (`kanban`, `skills`) a platform of the agent
  still lacks and the entry to add to its configuration by hand, and restart the gateway
  yourself. A message or an answer that does not arrive is reported to the
  conversation, from the node's own notices. The guide is the Hermes section of
  `docs/guide/adapters.md`.
- **Go SDK: the reasons a refused send carries are named.** `adapterproto` gains
  `SendRefusedReason` with `SendRefusedMessageUnknown`, `SendRefusedTooLarge`,
  `SendRefusedIdempotencyConflict`, `SendRefusedIdentityRetired` and
  `SendRefusedInvalidArgument`, and `SendRefusedReasons()`, so an adapter whose `reply_to`
  names a message the endpoint no longer holds recognizes `message_unknown` and falls back to a
  plain send instead of copying the word. The wire is unchanged.
- **Receive pages mark the node's own channel feedback.** A message the node sent from its
  system endpoint, such as the notice that a message was refused or expired, carries
  `feedback: true` on a page an adapter receives, so the adapter can tell it from a message
  another endpoint wrote. An agent tells it by the sender, as before: the pages of the Agent
  interface carry no such member, and its client now ignores members of a page message it does
  not know, so a client that outlives an update is not stranded by a member a later release
  adds. An adapter receives the member only if it declares the new direct capability
  `feedback`, which the Go SDK kit declares itself; the protocol stays at version 1. To learn what
  happened to a message, an adapter asks `endpoint.message_status`. The console's `recv`
  prints such a message with `(channel notice)` on its first line.
- **Direct endpoints can read the directory and the peer list and set their own declaration.**
  The adapter protocol gains `endpoint.directory`, `endpoint.peers` and `endpoint.declare`
  for a direct endpoint, behind the daemon capability `direct_endpoint_access` that
  `initialize` declares, so an adapter learns at the start whether the daemon serves them. The
  protocol stays at version 1. The Go SDK `Conn` gains `DaemonCapabilities`, `Directory`,
  `Peers` and `Declare`. The console gains `directory` and `peers`, so you can find the
  address of a runtime on another node, and page through a long list, without logging in
  to that machine; on a daemon that does not declare the capability they say that it is too
  old.
- **Go SDK: the delivery timing an adapter reasons about.** `adapterproto` now exports
  `DefaultAcceptBefore`, `MaxAcceptBefore`, `ClockTolerance` and `ReportByOffset` with the
  constants they follow from, and two durations for an adapter that keeps its own records:
  `SentKeyWindow`, how long the idempotency key of a send is certainly still held, and
  `ReceivedRedeliveryWindow`, how long a received message may be handed out again. An adapter
  no longer copies these numbers from the node. No value changes.

### Changed

- **Go SDK: `SendRefusedData.Reason` is a `SendRefusedReason`.** It was a `string`; code that
  assigns it to a `string` or compares it with an untyped string variable now writes
  `string(data.Reason)`. Comparison with a string constant still compiles.
- **`declare` names why a list of senders is refused.** Over HTTP, the command line and MCP,
  the Agent interface now answers a `declare` whose `accept_from` is too long with the code
  `too_many_senders`, and one that lists an address twice with `duplicate_sender`, where it
  answered `malformed_request` for both. The category stays `invalid_argument`, so the HTTP
  status (400) and the exit code of the command line are unchanged; an entry that is not an
  address is still `malformed_request`.

## [0.13.0] - 2026-11-04

Nodes now keep the connections they need and close the ones they do not, an instance's
adapter is told why it ends so that disabling or removing an instance leaves nothing behind,
and the Codex adapter replaces its App Server after a Codex upgrade. State written by 0.12.x
needs no migration by hand. The Codex adapter adds the process's start key and container to
`app-server.json` and recognises a record 0.12.x wrote; removing an instance now also moves
its state directory aside in the same configuration change, and `instances.json` is
unchanged. The one record that is settled at the first start is an endpoint record whose
display name holds a control or direction-changing character, which is treated as damaged
(see Fixed). The adapter protocol stays at version 1, extended in place: the `shutdown`
request carries an `intent` only to an adapter that declares `shutdown_intent`, the time for
a handshake call is the constant `HandshakeLimit`, and a display name is refused when it
holds a control or direction-changing character. The node-to-node `send`, `pull` and
`directory` protocols move to 2.0.0; 0.13.0 still speaks 1.0.0 with 0.12.x nodes, and 1.0.0
is to be removed in a later release. **The Go SDK has a breaking change**:
`adapterkit.Application.Shutdown` takes the shutdown parameters and returns an error. An
installation of 0.12.0 or later updates with `sidera update`, and the nodes of a cluster can
be upgraded one at a time, in any order.

### Added

- **Go SDK: `process.Terminate`.** An adapter that runs a native process can end it by its
  identity: `SIGTERM` to its process group and `SIGKILL` after a grace period on macOS and
  Linux, `TerminateProcess` on the handle that checked the start on Windows. It signals only
  a process the process table shows to be the one the identity names, and leaves the process
  alone when the table cannot be read.
- **Adapter protocol and Go SDK: `shutdown` says why the adapter ends.** The request carries an
  `intent`: `restart` (a new process of the same instance follows), `stop` (the daemon is
  ending; the instance stays enabled) or `retire` (the instance is disabled or removed). The
  daemon sends it only to an adapter that declares the new capability `shutdown_intent`, so an
  adapter that decodes `shutdown` strictly is not affected. The Go SDK declares the capability
  itself. The protocol stays at version 1.
- **Disabling an instance releases what its adapter left running.** The daemon now tells an
  adapter why it ends: `restart` when it restarts, `stop` when it ends, `retire` when the
  instance is disabled. When an instance is disabled while its adapter is not running (it
  is waiting to be started again after a failure), the daemon starts the adapter once only
  so that it can release what it started. The Codex adapter now ends its App Server then,
  whatever it is doing, and deletes the directory it made for it under your user cache
  directory. The Claude Code adapter interrupts the turns it is running, so that no turn
  keeps writing into the state directory that `sidera adapter remove` deletes.
- **Removing an instance deletes its state directory.** `sidera adapter remove` used to leave
  `~/.sidera/adapter-state/<instance>/` for you to delete. The daemon now waits for the
  adapter to have ended, then deletes the directory with the instance. On Windows the removal
  is refused with `instance_state_busy`, naming the directory, while a process still holds a
  file in it; end that process and run the command again.

### Changed

- **Go SDK: `adapterkit.Application.Shutdown` takes the shutdown parameters and returns an
  error.** The method is now `Shutdown(ctx context.Context, p adapterproto.ShutdownParams) error`
  (it was `Shutdown(ctx context.Context)`), so that an adapter can see why it ends and report a
  failure to release what it started. A third-party adapter has to update its implementation
  when it upgrades the SDK; embedding `adapterkit.Base` keeps the default that does nothing.
- **Stopping the daemon ends the Codex App Server when no Codex session is running a turn.**
  `sidera daemon stop` used to leave the server running always. The Codex adapter now ends it
  when every session it manages is idle, and keeps it when one runs a turn, as it does when
  the daemon restarts or `sidera update` replaces it, so that a turn survives those.
- **A node can ask another member whether it can dial it back.** The new node-to-node
  protocol `/sidera/dial-back/1.0.0` lets a node ask a member over a connection they already
  have; the member dials the node on the addresses its own address book holds for it, from
  a new connection that shares nothing with the listening port, and answers whether it got
  through. A node uses the answers to decide which connections it has to keep (see
  below). A node of an earlier release does not serve it and is reported as unable to
  answer.
- **A node keeps a connection only to the members that cannot dial it, and connects again
  by itself after a restart.** Until now a connection was made only when something had to
  be sent, so a node that blocks inbound connections, or whose peer was restarted, had no
  connection until it sent something itself, and the upgrade steps had you send a message
  from each node. Now every node asks the members it is connected to whether they can dial
  it. A member that can is dialed when something needs it, as before; one that cannot (it
  holds no address for the node, or its address does not connect), one that does not
  answer the question (an earlier release), and one that has not been asked yet are kept:
  the node dials them when it starts, again 5, 10, 20 and 40 seconds after a failed dial
  and then every minute, and again at once when the connection is lost. The answers are
  asked again after an hour and when the node's own addresses change. The log now says,
  at info level, when a member can or cannot dial the node, when the first dial to a kept
  member fails (with the error of each address) and when its connection is back.
- **A connection nobody needs is closed after five minutes.** A connection between two
  nodes stayed up for as long as both ran, so every node ended up connected to every
  other. Now a connection that nothing has used for five minutes is closed when both
  nodes know they can dial each other. A connection that one of them needs (it cannot
  dial the other, has not been told it can, or the other is a node of an earlier release)
  is kept, and a closed one is made again when something is sent. `sidera network show`
  therefore lists a peer that can dial you as `connected false` when you have not talked
  for a while.
- **Adapter protocol and Go SDK: the time an adapter has to answer a handshake call is
  one constant.** The 20 seconds the daemon waits for `initialize`, `adapter.start` and
  `adapter.probe` is now `adapterproto.HandshakeLimit`, written into the protocol reference,
  and the Go SDK gives the context of an application's `Start` and `Probe` a deadline 2 seconds
  short of it, so an adapter can tell how long it has. The limit itself is unchanged.
- **Codex adapter: the App Server is replaced after a Codex upgrade.** The App Server the
  adapter runs outlives the adapter and the daemon, so after you upgraded Codex it went on
  serving the old version until someone ended it. The adapter now compares the installed
  Codex with the server's version when it starts, every ten minutes and when a turn ends,
  and replaces the server as soon as no session is running a turn; a turn is never cut
  short for the upgrade. A prompt that arrives while the server is being replaced is
  refused as the runtime being unavailable, which the daemon retries. A server the adapter
  cannot show to be its own is left running with a warning in the adapter's log, and you
  restart it yourself.
- **Codex adapter: the App Server is identified by the process's start key.** The adapter
  records the start key of the App Server it starts in `app-server.json`, on every platform,
  and checks it instead of the wall-clock start time, which moves when the clock is set. A
  record an earlier version wrote is recognised when its start time equals the process's
  exactly, and the key is then added; otherwise the adapter treats the process as one it
  cannot prove is its own and leaves it alone.
- **Node-to-node requests are delimited, protocol 2.0.0.** The `send`, `pull` and
  `directory` protocols between nodes move to `/sidera/{send,pull,directory}/2.0.0`: the request
  and the answer are each one length-prefixed frame, and the server keeps the stream, and the
  connection with it, until the caller has read the answer and closed. A node serves both 2.0.0
  and the 1.0.0 of earlier releases and a caller proposes 2.0.0 first, so nodes can be upgraded
  one at a time in any order; a pair of nodes where either is still on an earlier release keeps
  using 1.0.0 and the problem described under Fixed. The 1.0.0 protocols are to be removed in a
  later release, after which a node of an earlier release can no longer be reached.
- **The log says when calls to a node start failing and when they work again.** The first
  call to a node that fails, after calls worked, is logged at info level with its kind
  (`unreachable`, `timeout`, `refused` with the reason, `failed`) and where it stopped, such as
  the error of each address dialed; the first call that works again is logged with how many
  failed and for how long. Until now the reason was written only at debug level, so a node that
  stopped answering left nothing to read at the default level.
- **Closing an attached session now needs `--allow-attached`.** `sidera endpoint close`
  used to close the endpoint of a controller session a person is using exactly as it closes
  any other, so a cleanup that closed endpoints by address could end it by mistake. The
  daemon now refuses (`refused/attached_close_not_allowed`) unless the request sets
  `allow_attached`, which the command does with `--allow-attached`. Endpoints a node created,
  direct endpoints and endpoints that are closed already close as before. `sidera endpoint
  list` shows which endpoints are attached in its `ORIGIN` column.
- **Adapter protocol and Go SDK: a display name holds no control or direction-changing
  character.** `endpoint.register` refuses, with `-32602`, a `display_name` that has a control
  character (U+0000 to U+001F, U+007F to U+009F), a line or paragraph separator (U+2028,
  U+2029) or a bidirectional formatting control (U+061C, U+200E, U+200F, U+202A to U+202E,
  U+2066 to U+2069). The protocol stays at version 1: no adapter that registered a plain
  name is affected. The schema states the rule as the `DisplayName` definition, and the Go SDK
  exports it as `adapterproto.ValidateDisplayName`, `ValidateOptionalDisplayName` and
  `DisplayNamePattern`.

### Fixed

- **A Codex instance with many sessions no longer stays offline after a restart.** The adapter
  took its sessions back one at a time before it answered the daemon, so a node with enough
  sessions, or a Codex that had to start first, ran past the 20 seconds the daemon allows; the
  daemon then restarted the adapter, which ran past them again. The adapter now connects and
  takes sessions back in the background, four at a time, and answers by the deadline. A session
  not taken back by then is reported unreachable, as when Codex cannot be reached, and a turn
  found running in it afterwards is reported when it is found.
- **A message or query to another node is no longer lost when that node renews its
  certificate.** A node renewing its certificate (about every 20 days) closes its connections as
  soon as they are idle, and the close could discard the answer of a request that had just been
  handled. The sender got a timeout and sent again 30 seconds later, so nothing was lost, but
  a message was delayed and a directory query failed with `directory_unavailable`. The fix
  takes effect between two nodes that both run this release.
- **A message to another node is no longer refused while the node closes the connection it
  would use.** A connection that a node had decided to close (for a renewed certificate, or
  because neither node needs it) could still be picked for a call in the moment before the
  close, and the call failed and marked the member unreachable for ten seconds. Such a
  connection is no longer used; the call connects again.
- **A name with a line break or an escape sequence can no longer reach your terminal.** The
  names that `sidera directory` and `sidera me` print come from adapters and, now, from other
  nodes. The node applied only a length limit to them, so a name could forge extra lines or
  move the cursor. It now refuses such a name wherever it takes one in: from a peer's
  directory answer (the whole answer is refused), from a session that attaches, and from a
  stored endpoint record. Until a peer is upgraded, a directory query to it fails if any of
  its endpoints has a name the rule refuses, because the whole answer is refused. A record
  written by an earlier version whose name has such a character is settled like any
  damaged record at the next start: the node starts, the file is removed, and the owner has
  an alert of kind `corrupt_record` (here it does not mean the disk is damaged). The
  endpoint is gone with its record: the deliveries queued for it are rejected as
  `endpoint_closed` and their senders are told, a session that holds its old token must get
  its credentials again, and an attached session attaches again and gets a new endpoint. The
  controller name `sidera` gives a session leaves the directory name out when the directory's
  name has such a character.
- **One bad adapter package no longer stops the node.** An installed adapter package whose
  pointer or manifest was missing, unreadable or invalid made the whole daemon refuse to start.
  The daemon now starts, the other adapters run, and only the instances of the bad adapter are
  marked faulted: `sidera adapter list` shows `fault` with a `reason` for an enabled instance
  whose package cannot run (`package_invalid`, `platform_unsupported`, `protocol_unsupported`,
  `program_missing` or `program_not_executable`), where before such an instance was only offline
  and the daemon log held the cause. The daemon logs a warning for a package it cannot load.
  Installing the package again recovers the adapter without restarting the daemon. The
  fault of an installed package that cannot be loaded is also shown on its entry in the
  `adapters` list, so a faulted package whose instances are all disabled is not hidden, and
  `sidera update` installs an adapter with a faulted package again from the release whatever
  version it records, even an older one, since nothing of a broken package is worth keeping
  (when the release has no package of it, it says so and leaves it).
- **Installing an adapter package for an adapter protocol this node does not speak is refused.**
  `sidera adapter install` used to accept a package whose manifest declares protocol versions
  that share none with the ones the node speaks, and an instance running the previous version
  was then restarted into a crash loop (the old package had to be installed again). The
  installation now answers `refused/protocol_unsupported` and changes nothing, as it does for a
  package that does not run on this platform.

## [0.12.2] - 2026-11-04

A patch release with one fix, for Windows. State written by 0.12.1 needs no migration: no
stored format changed. The adapter protocol stays at version 1 without a change, and the Go
SDK does not change. An installation of 0.12.0 or later updates with `sidera update`.

### Fixed

- **Windows: a daemon without a console no longer gives each adapter a console window of its
  own.** A daemon has no console when `sidera update` or `sidera daemon restart` restarted
  it, when it runs as a service and when it was started through WMI. It then gave every
  adapter, and the check of the new program that runs before a restart, a new console, which
  Windows Terminal showed as a window. Closing an adapter's window ended the adapter, and the
  daemon started it again in a new window. The adapters and the check now run without a
  window. As a result an adapter no longer shares the console of a daemon run in a terminal:
  Ctrl-C there reaches only the daemon, which ends the adapters itself as it shuts down. The
  update from 0.12.1 can still show one brief window, because the 0.12.1 daemon runs that
  check of the new program before it restarts into it.

## [0.12.1] - 2026-11-04

A patch release of fixes. State written by 0.12.0 needs no migration: no stored format
changed (the list of page identifiers that a delivery keeps is only shorter, and a longer
one written by 0.12.0 is read as before and shortened the next time the delivery is handed
out). The adapter protocol stays at version 1 without a change, and the Go SDK does not
change. An installation of 0.12.0 updates with `sidera update`.

### Fixed

- **Claude Code adapter: a prompt is no longer started a second time.** When the process of a
  turn wrote the prompt it was given and ended just as the adapter was checking whether the
  prompt had been delivered, the adapter found neither the prompt nor the process and started
  the prompt again. It now looks at the processes before it reads the session's record, so a
  process it sees as gone has already written everything the record holds.
- **A message that is read again and again no longer makes its delivery record grow.** Every
  `recv` that handed a message out added a page identifier to the message's record, so an agent
  that read without acknowledging made the record, and the time to check it, grow for as long
  as the message lived. The record now keeps the latest 16. An acknowledgement that names an
  older page is refused as an unknown page and the message comes again.
- **A lease is never shortened by a renewal.** When the clock of the machine had stepped back,
  renewing a lease could set its expiry earlier than it already was; a renewal now never moves
  the expiry earlier.

## [0.12.0] - 2026-11-03

Sidera can now update itself and restart its own daemon: `sidera update` brings an
installation from a release archive to a release, programs, daemon and adapters, and
`sidera daemon restart` restarts the running daemon onto the program on the disk. **0.12.0
is the first release that can update itself**: an installation of 0.11.0 or earlier is
upgraded by hand one last time (`docs/guide/operations.md`, "Upgrade"), and from then on
with `sidera update`. State written by 0.11.0 needs no migration: no stored format changed
(the result of `daemon restart` is stored in the record that `daemon stop` already uses,
so 0.11.0 reads it too, and a return to 0.11.0 by hand reads what 0.12.0 wrote). The
adapter protocol stays at version 1 without a change on the wire, and the adapters of
0.11.0 work with this daemon. The Go SDK gains the public package `semver`, which is
additive: it was internal, so nothing outside the SDK could depend on the old place, and
nothing else in the SDK changes.

### Added

- **`sidera daemon restart [--expect-version <version>] [--wait <duration>]`** restarts the
  running daemon in place with one command. The daemon first runs the program it would
  start as `version --json` and refuses, staying up, when that fails or reports a version
  other than `--expect-version` (`refused/restart_check_failed`,
  `conflict/restart_version_mismatch`), or when it was not told how it was started or, on
  Windows, when the job it runs in would end the new process (`refused/restart_unavailable`).
  It then shuts down in its orderly way and starts the program again: on macOS and Linux it
  replaces its own process, so the process number, terminal and tmux session stay; on
  Windows it starts a detached process and ends, which a scheduled task's daemon survives.
  Restarting the daemon is a request of the local owner. The command waits until `status`
  shows a new `daemon.start_id` and the daemon running (default 2 minutes) and prints both
  start identifiers, the program and its version. A daemon older than 0.12.0 has no restart
  method: the command says so and to stop the daemon and start it the way it was started,
  which is the one manual step when upgrading from 0.11.0.
- **`sidera update [--check] [--version <version>] [--yes]`** brings an installation from a
  release archive to a release: it finds the latest release (or the one asked for), checks
  `checksums.txt` against the minisign key built into the program, the version the
  signature is for and the release's `update-from` floor, downloads the archive for the
  platform, checks its SHA-256, unpacks it and runs the unpacked program, asks for
  confirmation (`--yes`, or a terminal), and then replaces `sidera` and
  `sidera-adapter-console` all or none (the old ones stay as `.old`), restarts the running
  daemon onto the new program with `daemon restart`, and installs the new package of each
  installed adapter in place. Each run does only the missing steps, so an interrupted run
  is finished by running it again. It refuses a downgrade, a program below the floor, a
  program that is a symbolic link or owned by another user (do not run it with `sudo`;
  the owner is not checked on Windows), a directory that cannot be written, and builds that
  are not from a release archive, as well as a release whose program does not say that it
  is from one: no release before 0.12.0 does, so none can be a target.
  `--check` reports `update_available` and `admitted` and exits 0 once it has read a release
  it can believe, 1 when it cannot. Only this command
  contacts GitHub or any release server; the daemon never does.
- `status` shows `daemon.start_id`, a random identifier of each start of the daemon.
- `sidera version --json` reports `distribution`: `archive` for the programs of a release
  archive, and no member for a build made any other way. `sidera update` replaces only
  programs that report `archive`.
- **Go SDK: `semver` is a public package.** It was internal; it now parses a Semantic
  Versioning 2.0.0 version (`Parse`) and orders two parsed versions by the precedence rules
  of the specification (`Compare`), so that any reader of an adapter version applies the same
  syntax and the same order. It uses the standard library only; nothing else in the SDK
  changes.

### Changed

- **The release scripts accept exactly the versions and the `checksums.txt` that the program
  accepts** (for maintainers). `scripts/release.sh` and `scripts/release-sign.sh` refuse a
  version that `sidera update` would refuse, such as a number or a numeric pre-release
  identifier with a leading zero (`01.2.3`, `0.12.0-rc.01`), an empty identifier
  (`0.12.0-rc..1`) or more than nine digits in a number, and `release-sign.sh` refuses a `checksums.txt` that the
  verifier of the program would refuse; before, a release could be built, signed and
  published that no installation could install. `scripts/lib/release-format.sh` is the one
  place where the scripts say what a release version and a `checksums.txt` are, and the
  `sign` job of the release workflow checks it out with the script. `scripts/release.sh` also
  checks that the `sidera` it built reports the distribution `archive`.

## [0.11.0] - 2026-11-03

Releases are now signed, and an upgrade of an adapter no longer ends the sessions of the node:
from this release on, once the new daemon runs, upgrading an adapter is one
`sidera adapter install` per adapter, with no `disable` and `enable`, and its sessions are
kept. The daemon decides this, so it already applies when you upgrade from 0.10.x. State
written by 0.10.0 needs no migration: no stored format changed. The adapter protocol stays at
version 1 without a change on the wire; the reference gains an obligation for adapter authors,
which is listed under Changed. The Go SDK has no change.

### Added

- **Signed releases.** `checksums.txt` of a release is signed with minisign, and the
  signature is the new release asset `checksums.txt.minisig`. Its trusted comment is
  `sidera <version> update-from <oldest version that may update to it>`; the oldest
  version is `0.8.0` and is raised only by a release that needs a manual migration.
  The public key is `internal/release/minisign.pub`, quoted in the install guides, which
  now show how to verify a download (`minisign -Vm checksums.txt -P <key>`). The archives
  and `checksums.txt` also carry a GitHub artifact attestation, checked with
  `gh attestation verify <file> -R aianyai/sidera`. Releases up to 0.10.0 carry neither.

### Changed

- **Installing another version of an adapter no longer needs its instances disabled.**
  `sidera adapter install` replaces the package of an adapter that runs as it already did
  for a rebuild of the same version: the enabled instances restart on the new files and keep
  their session endpoints, and the new adapter takes the sessions back. An upgrade is now
  one `install` per adapter, with no `disable` and `enable`, so it no longer closes the
  sessions of the node. Replacing the package of an adapter that runs stays a request of the
  local owner. An instance you disabled whose process is still ending no longer makes
  `install` wait: the old files stay on the disk until the next start of the daemon.
  `install` no longer answers `conflict/adapter_in_use`; the code stays in the vocabulary.
- **An adapter reads the checkpoints of the versions it replaces.** Because the daemon now
  upgrades an adapter in place and hands the new process what the old one stored, the
  adapter protocol reference states that a new version of an adapter reads the checkpoint
  formats and state files of every earlier version it can be upgraded from. Both adapters
  of this release still write and read the one format they had, so nothing changes for
  them; the rule is for the adapters that follow.

## [0.10.0] - 2026-11-03

The Claude Code adapter now runs on Linux and Windows, as the Codex adapter already did, and
both adapters can stop a turn that is running. State written by 0.9.0 needs no migration. The
adapter protocol stays at version 1 without a change, and the daemon sends an adapter nothing
it did not send before: `session.interrupt` is an existing request that the two adapters now
declare. The Go SDK has no source-incompatible change: it gains three packages and the module
a dependency, listed under Added.

### Added

- **Claude Code on Linux and Windows.** The Claude Code adapter is built and packaged for
  linux/amd64, linux/arm64, windows/amd64 and windows/arm64 beside macOS, and every release
  archive now carries `adapters/claude/` (the archives of 0.9.0 carried it for macOS only).
  A Claude Code controller session is woken on all three systems. On Windows the daemon needs the native
  installer's `claude.exe`: the npm wrapper `claude.cmd` runs through `cmd.exe`, so the adapter
  could not recognise its turns, and the probe reports `claude` as missing when it is not an
  `.exe`, a session cannot be created with it and a prompt is refused with the reason. The
  guide's source-build commands no longer mark the Claude adapter as macOS only.
- **Claude Code turns outlive the daemon's restart on every system.** A turn is a process that
  the adapter starts detached. On Linux it is started in a session of its own and, when that
  keeps it alive, in a systemd user scope: the user manager has to answer and either lingering
  has to be on or the daemon has to run under the user manager already. A node that must run
  with nobody logged in needs `loginctl enable-linger`; without it a scope ends at the last
  logout, as a daemon under the user manager does. On Windows the turn leaves the job object of
  the daemon (breakaway); when the daemon runs in a job that forbids breakaway and kills its
  processes when it closes (some service wrappers and launchers do), the turn ends with the
  daemon and the adapter logs a warning that says so.
- **Claude Code on Windows: the inbox and the environment.** A controller session is woken
  through its inbox, which is a named pipe on Windows. The adapter writes to it as Claude Code
  requires, with an authentication line before the message. Claude Code drops a message that no
  authentication line precedes, without answering, so a wake without its token is refused at
  attachment (`wake_unavailable`) and a delivery to it as `native_unavailable`, before anything
  is written. A Git Bash that Claude Code cannot
  find in a standard place is found by setting `CLAUDE_CODE_GIT_BASH_PATH` in the daemon's
  environment: it is the one `CLAUDE_CODE_*` variable passed on to the turns. The session
  registry's start forms of each system (`ps` text on macOS, clock ticks on Linux, the creation
  time on Windows) are told apart, so a registry entry of a reused process number is not taken
  for a live session on any of them.
- **Interrupting a turn.** Both the Claude Code and the Codex adapter declare
  `session.interrupt`, so `sidera control <session-address> session.interrupt` stops the turn
  that is running. The answer is `stopped` once the turn has ended, `requested` when it is still
  running after about ten seconds (its end is still reported when it comes), and `not_running`
  when no turn runs. What the turn already did stays done. An attached session, whose turns are
  the person's, cannot be interrupted.
  - Claude Code: the adapter sends the interrupt that Ctrl-C does (`SIGINT`; on Windows
    `CTRL_C_EVENT`, sent by a short-lived helper run from the adapter's own program, because
    attaching to the console would take the adapter's own away). The turn process ends within
    about two seconds and the turn is reported `interrupted`, unless the process finished it
    with a success anyway, which is reported `completed`. The process is never killed: a turn
    that ignores the interrupt stays `requested`.
  - Codex: the adapter stops the turn with `turn/interrupt` and also ends the commands that
    turn started, which the App Server leaves running (a `sleep` outlived its interrupted
    turn). `stopped` and the end of the turn mean those commands ended too; commands that
    earlier turns left running on purpose are not touched. A turn the adapter did not see
    start has every background terminal of its thread ended. If a command cannot be ended the
    answer is `failed` and the turn is reported ended all the same.
- **Go SDK: `adapterkit/process`, `adapterkit/environ` and `adapterkit/realpath`.** Three new
  packages for adapters that drive a native agent runtime, on macOS, Linux and Windows:
  - `process` starts a process detached from the adapter (and says in `Started.Detachment`
    how far that succeeded), names it by PID and start key, finds it again after a restart
    (`Lookup`, `Running`, `List`), and interrupts it as Ctrl-C would. An adapter that calls
    `Interrupt` calls `process.HandleHelperInvocation` first in `main`. The start keys have
    fixed formats, and the macOS key is the one the Claude adapter already wrote.
  - `environ` composes the environment of a child process, comparing names as the platform
    does (without case on Windows).
  - `realpath` gives the path the platform itself reports for an existing path, following
    symbolic links, and on Windows also junctions and 8.3 names, in the on-disk case.

  The SDK module now requires `golang.org/x/sys` and states `go 1.26.0`. Only `process` and
  `realpath` import `golang.org/x/sys`; the packages the daemon imports (`peer`,
  `adapterproto`, `manifest`, `strictjson`) and `adapterkit` itself still use the standard
  library only, so an adapter that imports only those does not build it.

### Changed

- **Codex adapter: the App Server is started through the SDK.** On Linux the server is put in a
  systemd user scope of its own under the rule stated above for Claude Code turns, so that
  stopping the unit the daemon runs in does not take it along; otherwise it keeps a session of
  its own, as before. On Windows the warning that the server's life ends with the daemon's job
  is logged only when the job really kills its processes on close, or when the limits cannot be
  read; a job that merely forbids breakaway no longer produces it. A node that must run with
  nobody logged in needs `loginctl enable-linger` here as well.
- **Claude Code adapter: turns and records.** The environment of a turn is composed with
  `environ`, so on Windows a variable is replaced or dropped whatever case it is spelled in
  (`Path`), and a turn's working directory is the resolved path of the preset's directory (on
  Windows with junctions followed and 8.3 names in their long form), so that Claude Code creates
  the one session record the adapter looks for even when the directory is given through either.
  The turn checkpoint gained two optional fields
  that say where the turn process runs; checkpoints without them load as before.
- **A turn's final after an interrupt.** A turn that an interrupt or a close signalled but that
  finished with a successful result anyway is reported `completed`, not `interrupted`.

### Fixed

- **A controller session in a recent Claude Code is woken.** Claude Code 2.1.317 no longer
  gives its MCP servers `CLAUDE_PID`, so a session that attached through `sidera mcp` had no
  process the adapter could wake. The adapter now finds the session's process in Claude Code's
  registry by the session id, and refuses rather than guesses when it finds none or more than
  one. `CLAUDE_PID`, when present, is still the first choice.
- **The Codex sandbox's deny path matches the directory it protects, on Windows.** The data
  directory was resolved without following junctions or 8.3 names, so the path denied to a
  sandboxed command could differ from the one the sandbox compares a command's paths with. It
  is now resolved as the platform does, and a relative path is refused.
- **Codex adapter: variable names are compared without case on Windows.** A variable the
  adapter drops from the App Server's environment (such as `CODEX_THREAD_ID`) is now dropped
  whatever case it is spelled in.

## [0.9.0] - 2026-11-02

A Claude Code or Codex session you opened yourself can now take part as a controller:
it has an endpoint of its own on the node and is woken when a reply arrives. The release
also ships a Windows arm64 package. State written by 0.8.0 needs no migration. The adapter
protocol stays at version 1, and the daemon sends nothing new to an adapter that does not
declare the new capability `attach`; the Go SDK has source-incompatible changes, listed
under Changed.

### Added

- **Controller sessions.** A Claude Code or Codex session you opened becomes a controller
  when it has the `sidera` MCP server (or runs `sidera` agent commands) and no `SIDERA_TOKEN`:
  it attaches itself through `sessions.attach` on first use, keeps the token in memory only
  and uses the ordinary Agent tools. `sidera mcp` repeats the attachment every minute while it
  runs, only finding the endpoint and never making one, and tells the model, in a second text item
  of the first result, whether the session can be woken. When the endpoint is closed the next
  tool call attaches the session as a new endpoint and its result names the new address and says
  the old one is closed.
- **`sidera setup controller`.** It prints the commands that add the MCP server to Claude
  Code and Codex, naming the program by its absolute path (a client starts it without a shell and may
  not find the bare name on its `PATH`: Codex on Windows does not) and the data directory with `--home`
  when it is not the default, and the permission each client needs so that a woken controller does not
  stall at a prompt: the rule `mcp__sidera` for Claude Code, the line
  `default_tools_approval_mode = "approve"` for Codex.
- **A resumed Claude Code session is woken at once.** When it starts, the `sidera` MCP server
  of a Claude Code session looks for an endpoint the session already has and, if it finds one,
  keeps its token and starts the heartbeat, so that what waited for the session while it was
  away reaches it without a first tool call. A session with no endpoint is left alone until its
  first tool call.
- **A session that attaches again is prompted at once.** An attachment judges the endpoint for
  a prompt without the minute that follows a failed prompt, so that a session the person resumed
  is prompted for what waited while it was away.
- **An attached endpoint is closed when its controller is gone.** Every attachment counts as the
  heartbeat of the session's controller. An attached endpoint whose controller has not attached
  for the endpoint's queue time (30 minutes by default, and never less than three minutes),
  counted in the time the node was awake, is closed once nothing is queued or leased on it.
- **Attached sessions are outside the node's turn cap.** The setting `node_prompt_turns` no
  longer counts the turns of attached sessions, and a full node no longer holds back their
  prompts.
- **A Windows arm64 package.** A release carries `sidera_<version>_windows_arm64.zip` beside
  the five archives it had, with the Codex and console adapter packages for windows/arm64. The
  Claude Code adapter stays macOS only.
- **Claude Code adapter: attached sessions.** The adapter declares `attach`. A Claude Code
  session a person opened and that attached itself is woken through the session's inbox socket
  and followed through its registry entry and its session record: the turns the person starts are
  reported without an attempt (also when a busy session goes from one turn straight to the next),
  `/clear` is followed, and a session that is gone refuses prompts as `native_unavailable`.
  Closing or interrupting such an endpoint is a wrong endpoint kind.
- **Codex adapter: attached sessions.** The adapter declares `attach` and takes hold of a Codex
  thread a person opened: it reads the thread's turns (once a second while it waits for a
  prompt it queued or a turn is open, every ten seconds otherwise), reports the turns the person
  runs, and delivers a prompt by queueing it in the Codex home it shares with the person's
  Codex (delivered within about ten seconds when the session is idle, after the current turn
  when it is busy, and held while no Codex process has the thread loaded). At most one wake-up
  of its own waits in the person's queue: an older one nobody took is withdrawn when a newer
  one is queued and when the endpoint detaches. The adapter's connection now asks for Codex's
  experimental API, which the queue needs. A thread that takes no prompt (archived, unknown) is
  reported as the availability condition `native_unavailable` of that endpoint alone, until it
  takes one again or the endpoint detaches.
- **Management: `sessions.attach`.** The owner can attach a session a person opened to an
  adapter that declared `attach`: the answer is the endpoint, its token and whether the adapter
  can deliver prompts to the session. The request is safe to repeat, carries no operation
  identifier and stores no result. Because the answer holds a token, a sandbox has to deny the
  management socket to an agent. With the parameter `create` set to false the method only finds:
  it answers like any attachment when the session already has an open endpoint, and with
  `not_found/session_not_attached` when it has none, without making one. `endpoints.list` shows
  the `origin` of a session endpoint.
- **Endpoint record: the origin of a session.** A session endpoint record can say that the
  session was attached rather than created (`origin`). Records without it read as created, so
  existing state needs no migration.
- **Adapter protocol: attached sessions.** An adapter can declare the runtime capability
  `attach` and take hold of a session a person opened: the new request `session.attach`, the
  notification `session.detached` and the `attached` and `wake` members of an `adapter.start`
  session entry. The daemon sends none of them to an adapter that did not declare it. The Go SDK
  has the types (`adapterproto`, with `RuntimeCapabilities.Attach`) and serves them through
  `adapterkit` only for an adapter that declared `attach`.

### Changed

- **Go SDK, incompatible: attached sessions change three shapes.** An adapter written against
  0.8.0 has to be rebuilt and may need edits:
  - `adapterproto.SessionAttach.ToolAccess` is now a `*ToolAccess`, absent for an attached
    entry, and `Preset` may be empty for one. Code that reads either of them from an
    `adapter.start` entry has to dereference the pointer; an adapter that does not declare
    `attach` never receives an attached entry, so the pointer is never nil for it.
  - `adapterkit.Application` gained the methods `AttachSession` and `SessionDetached`. An
    application that embeds `adapterkit.Base` keeps compiling and refuses `session.attach`; an
    application that implements the interface by itself has to add both.
- **The reply to a control signal reaches its sender sooner.** The sender's first pulls for the
  reply to a control signal (the address of a session just created, the answer to a close) come
  2, 5 and 10 seconds apart, then once a minute as for any delivery; before, the reply was seen
  a minute after the signal. Messages are pulled once a minute as before.
- **Codex adapter: the thread's `sidera` MCP server states `enabled = true`.** A person who
  also has a `[mcp_servers.sidera]` table in their own `config.toml` has it merged key by key
  with the one the adapter gives a session; the adapter now states every key it depends on, so
  an `enabled = false` in the person's table no longer switches the session's channel off. A
  `url` key in that table still makes the thread fail to start and has to be removed.

### Fixed

- **A pull that brings no news writes nothing.** The sender no longer rewrites the record of a
  delivery each minute for as long as the target repeats what the record already holds.

## [0.8.0] - 2026-11-02

The first public release. Sidera is a daemon through which AI agents, on one
machine or on several, exchange messages. A release is one archive per platform:
`sidera`, `sidera-adapter-console` (which is also the owner's command-line client)
and the adapter packages `sidera adapter install` takes, with the licences of the
software they are built from, for macOS (arm64 and amd64), Linux (amd64 and arm64)
and Windows (amd64).

### Added

- **The node.** `sidera init` creates a node: its private data directory, its keys
  and its default configuration. `sidera daemon` runs it in the foreground. State
  is written so that it survives a crash or a power loss, and the daemon recovers
  it when it starts.
- **Agent interfaces.** An agent on the node uses the command line (`sidera me`,
  `declare`, `peers`, `directory`, `send`, `reply`, `recv`, `ack`, `status`,
  `control`, `probe`) or MCP over standard input and output (`sidera mcp`). Both
  reach the same node through a checked local connection and a credential of the
  agent's own, and the command line can print JSON. An agent declares what it accepts: it can
  pause, refuse messages to several recipients, accept messages only from named
  senders, or stay out of the directory.
- **Delivery.** Messages have a delivery state and a deadline, are deduplicated
  by key, are read in pages with a lease and are acknowledged by part; a message
  that arrives while the receiving agent is not reading can wake it through its
  adapter. `sidera status <message-id>` reports where a message is.
- **Adapters.** An adapter connects a kind of agent to the node. The daemon starts
  it from an installed package and speaks the adapter protocol (version 1) with it.
  `sidera adapter install` installs a package from a directory (the same version
  with other contents replaces the installed one and restarts the instances that
  run it), `enable` and `disable` start and stop an instance, `config` replaces an
  instance's configuration, `remove` removes a disabled instance, and `list`
  shows what is installed and what each probe found. `sidera preset` keeps the
  presets that sessions are created from.
- **The Claude Code adapter** (macOS). It creates Claude Code sessions as `claude -p`
  processes that outlive the adapter, delivers messages to them and reports how
  each turn ended.
- **The Codex adapter** (macOS, Linux and Windows). It keeps a private Codex App
  Server, makes each session a thread of it, delivers messages to it and answers the
  requests Codex sends.
- **The console bridge.** `sidera-adapter-console` gives the node's owner an
  endpoint of their own: `new` starts a session on a runtime endpoint, and `send`,
  `reply`, `recv`, `status` and `whoami` message agents and read their answers from
  a terminal.
- **Several nodes.** Nodes reach each other directly over TCP and QUIC (libp2p),
  with their own identities, on any network where one can dial the other's address
  (for example a local network). `sidera network` shows where the node listens and
  the addresses kept for other nodes, and sets them; `init --port` chooses the
  port (7744 by default).
- **Pairing.** Two nodes become trusted when each owner has named the other node in
  advance: `sidera trust expect <node>` states that the owner means to pair with a
  node, and `trust request <node>` then completes the pairing (the verification code
  it prints is informational). `trust list` shows what is held, `trust reject` ends
  a request, `trust forget` withdraws an expectation, `trust remove` removes a node from the node's trust and revokes the
  credentials held with it, and `trust clear` takes a removed node back.
- **Messages between nodes.** `sidera peers` lists the node and the nodes it trusts,
  `sidera directory --node <id>` lists the endpoints another node shows, and `send`
  and `reply` carry messages between agents on different nodes.
- **Sessions on another node.** `sidera control <address> session.create` creates a
  session on a runtime endpoint of a trusted node, optionally with an initial
  message, and returns the session's address.
- **Owner commands.** `sidera node`, `endpoint`, `alerts` and `clock` show and change
  node settings, endpoint settings, alerts and the clock rollback pause. `sidera
  identity` exports the statement that retires the node's identity, confirms that it
  is kept outside the device, and retires the identity.
- **`sidera version`** prints the version, the commit and the Go version (`--json`
  for an object), and every adapter program prints the same with `version`.
- `sidera status` shows the data directory the command used (`home`, also in `--json`).

### Security

- **The start checks the whole data directory.** The daemon refuses to start when
  anyone but the owner can reach the data directory, a directory below it, a key
  file or a file that holds a token (an endpoint's token, an adapter's state files).
  The refusal names the path, the account or group by SID and name, and the rights.
  Sidera never changes a permission itself. An adapter's state directory is judged
  again before the adapter is launched.
- **The Windows data directory is `%LOCALAPPDATA%\Sidera`.** It was
  `%USERPROFILE%\.sidera`, whose first-level folder some sandboxes grant their own
  group access to. macOS and Linux keep `~/.sidera`; `SIDERA_HOME` and `--home` choose
  another directory everywhere. Nothing is moved automatically: a Windows machine with
  the former directory and none in the new place gets a message with the
  `Move-Item "$env:USERPROFILE\.sidera" "$env:LOCALAPPDATA\Sidera"` command.
- **The Codex App Server runs in an empty directory of its own.** Codex takes the
  working directory of the server as the writable root of a sandboxed command, so a
  server started in the adapter's state directory let a `workspace-write` command write
  there, `ws-token` included, on every OS. The server now runs in an empty
  per-instance directory under the user's cache directory, outside the data
  directory. A server that an earlier build started in the state directory is reported
  by the probe as `wrong_version` until it is restarted; the adapter does not end a
  server it did not start.
- **Sandboxed Codex presets cannot read the data directory.** A preset with a
  `workspace-write` or `read-only` sandbox runs under a permission profile that
  denies reading the data directory (the identity key and every session's token). The
  operating system enforces it: Seatbelt on macOS and bubblewrap on Linux, where it was
  verified, and a deny entry for the sandbox's accounts on Windows, where another Codex
  session can revoke it and the protected data directory is the main guard. A session
  that does not carry the profile is not used. Presets with `danger-full-access`
  are unchanged and read everything, and so do the user's own MCP servers and commands
  approved to run outside the sandbox.
- **Claude Code's file tools are denied the data directory, as best effort.** Every
  turn denies `Read` and `Edit` of it, whatever the preset or the user's own settings
  allow. A Bash command that the user's settings allow and that reads without naming
  the file (`grep -r` on the parent directory, for example) is not covered; a guarantee
  needs Claude's operating-system sandbox, which is not used yet.

### Not yet

- NAT traversal and relays: two nodes must be able to dial each other.
- Inherited trust: a node trusts the nodes it paired with, not the nodes they
  trust.
- Remote management by people: a node is managed from the machine it runs on.
- Chat adapters.
- Installing the daemon as a background service: `sidera daemon` runs in the
  foreground.
- The Claude Code adapter on Linux and Windows.

[Unreleased]: https://github.com/aianyai/sidera/compare/v0.15.0...HEAD
[0.15.0]: https://github.com/aianyai/sidera/releases/tag/v0.15.0
[0.14.0]: https://github.com/aianyai/sidera/releases/tag/v0.14.0
[0.13.0]: https://github.com/aianyai/sidera/releases/tag/v0.13.0
[0.12.2]: https://github.com/aianyai/sidera/releases/tag/v0.12.2
[0.12.1]: https://github.com/aianyai/sidera/releases/tag/v0.12.1
[0.12.0]: https://github.com/aianyai/sidera/releases/tag/v0.12.0
[0.11.0]: https://github.com/aianyai/sidera/releases/tag/v0.11.0
[0.10.0]: https://github.com/aianyai/sidera/releases/tag/v0.10.0
[0.9.0]: https://github.com/aianyai/sidera/releases/tag/v0.9.0
[0.8.0]: https://github.com/aianyai/sidera/releases/tag/v0.8.0
