# Adapters

This page lists the adapters Sidera ships and how each one behaves, so that you can
see in one place what each supports and where they differ. It is for the person who
sets agents up. The exact rules are in the engineering references, linked from each
section.

An adapter is a small companion program that the daemon starts and supervises. It
connects Sidera to one kind of agent: it hands a message to the agent's real session,
tells the node when a turn starts and ends, and lets the agent send its own
messages. You install and enable one adapter per kind of agent on a machine
([Getting started](getting-started.md#4-install-and-enable-adapters-both)) and add a
preset for it ([Presets](getting-started.md#5-add-a-preset-both)). Sidera ships four:

- **Claude Code**, `sidera-adapter-claude`, instance name `claude`.
- **Codex**, `sidera-adapter-codex`, instance name `codex`.
- **Hermes**, `sidera-adapter-hermes`, instance name `hermes`.
- **The console**, `sidera-adapter-console`, instance name `console`.

An agent uses Sidera in one of three ways, described in
[Working with agents](agents.md): in a session Sidera created from a preset, as a
controller (a session you opened yourself and attached), or through the owner's console.
This page says what each adapter does for each way. Hermes is the exception: its
agents use none of the three, and reach other nodes through a Kanban board
([Hermes](#hermes)).

All four are built for macOS, Linux and Windows on amd64 and arm64
([supported platforms](install.md#supported-platforms)). Sidera does not install the
agents: the daemon must be able to find `claude`, `codex` or `hermes`, and each must
be signed in or set up.

## Summary

| | Claude Code | Codex | Hermes | Console |
| --- | --- | --- | --- | --- |
| Connects | `claude` | `codex`, through a private App Server | A Hermes Kanban board, through the `hermes` command | The node's owner, from a terminal |
| Created sessions | Yes | Yes | No | No |
| Controller sessions | Yes | Yes | No | No |
| Direct endpoint | No | No | Yes: one for new requests and one for each Hermes conversation | Yes, the `owner` endpoint |
| An idle created session is woken by | A new `claude -p --resume` process | A new turn on the session's thread | Not applicable | Nothing: you read with `recv` |
| A controller is woken | Through the session's inbox, within about a second | Through the Codex queue, within about 10 seconds when idle | Not applicable | Not applicable |
| What wakes the agent | The node's wake-up prompt | The node's wake-up prompt | A completed card, which Hermes delivers to the conversation | Nothing |
| Interrupt and close | Created sessions only | Created sessions only | Not offered | Not offered: close a session with `sidera endpoint close` |
| Permission questions | Never asked: denied at once | Never asked: answered from the preset | Not applicable | Not applicable |

How to read it:

- **Created sessions** are the ones a preset creates when another agent or the console
  runs `session.create`. **Controller sessions** are the ones you opened yourself
  and that attached on first use of a Sidera tool
  ([The controller](agents.md#the-controller-a-session-you-started-yourself)). The
  console hosts neither: it has no presets and no sessions, and neither does Hermes,
  which only starts sessions on other nodes.
- A **direct endpoint** is read by the adapter on behalf of a person or program, not by
  a native agent session. The console and Hermes have them.
- The node, not the adapter, decides when to wake a session. A wake-up is a short
  prompt that says messages are waiting and tells the agent to read them with `recv`.
  A message that arrives while a session runs a turn waits in the queue, and the agent
  can read it with `recv` during that turn. A session that is busy, or has a prompt it
  has not read yet, gets no further prompt; when its turn ends with messages still
  unread it is prompted once more. The node also paces wake-ups: a message that arrives
  within 2 minutes of the last wake-up waits for the rest of that interval
  ([Handoff](../references/handoff/README.md)). This is the same for both agents.
- `sidera directory --node <node-id>` shows what an endpoint accepts. A created
  session accepts `session.interrupt` and `session.close`; a controller session
  accepts messages and `endpoint.status`, because the session is yours: the node
  neither interrupts nor closes it. The owner can still close its endpoint with
  `sidera endpoint close <address> --allow-attached`.
- The `SIDERA_TOKEN` of a session is in the environment of everything its turns run, so
  a command the agent runs can read it. See
  [Presets and access](getting-started.md#what-a-session-can-read-of-the-nodes-data).
- A controller needs the daemon to use the same operating-system user, and the same
  `CODEX_HOME` or `CLAUDE_CONFIG_DIR`, as your own sessions: that shared directory is
  how the adapter sees them.

## Setting up an adapter's client

Some adapters drive a client that keeps configuration of its own (Claude Code, Codex and
Hermes: where the `sidera` tools are registered, which of them may run without asking, the
Skill that tells an agent what Sidera is). `sidera adapter setup <instance>` brings that
configuration in line with the instance. The daemon has to run, and the instance has to have
run once so that it has a state directory.

```sh
sidera adapter setup claude          # shows what it would do, asks about each change
sidera adapter setup claude --yes    # accepts everything, for a script
sidera adapter setup claude --check  # exit 0 when nothing has drifted or is still to set up, 1 otherwise
sidera adapter setup claude --remove # undoes what it did, asking about each item
```

It lists the items and asks `[Y/n]` for every one that touches the client's own
configuration: Enter or `y` accepts, anything else is a no, and so is the end of the input
(a script, a closed pipe), so that nothing changes a client's configuration unasked. An item you decline is remembered: `--check` and
`sidera update` do not report it as still to set up, and running `sidera adapter setup`
again offers it again. Without a terminal it needs `--yes`, like
`sidera update`. Setup remembers what it did, and only what it did and only while it is still
there is touched later: if you delete something it installed, a refresh does not put it
back, and `--check` tells you it is gone. `--refresh` brings what setup recorded up to date
without asking, for a program that has no person to ask. Words after `--` are passed to the
adapter for the options it has of its own. An adapter that has no setup says so. An adapter may
also have advice for you that setup does not do for you: it is shown as `Note:` lines before
the questions, and by `--check` as `Note:` lines after the drift; a note never makes
`--check` fail.

Before you remove an instance whose setup has recorded something, undo it with
`--remove` (if that cannot finish because the client program is gone, it ends by saying that
`sidera adapter remove <instance> --force` removes the instance anyway and where the cleanup by
hand is described): `sidera adapter remove` refuses otherwise, because removing the instance deletes
the files its client installation depends on. If the setup cannot even be asked whether it
recorded something (the adapter is damaged), the refusal says so and
`sidera adapter remove <instance> --force` removes the instance without
asking it and leaves whatever it installed in the client in place; the next section says how to
remove that by hand.

### Cleaning up an adapter's client by hand

After `sidera adapter remove --force`, or when a setup was removed some other way, what an
adapter installed in its client is still there. Removing it is the client's own business;
this is what each adapter put where.

- **Claude Code**: setup registered the `sidera` MCP server, allowed its tools and installed
  a plugin that carries the Skill. Undo them with the `claude` command, in this order:
  `claude plugin uninstall sidera@sidera --scope user`, `claude plugin marketplace remove sidera`
  and `claude mcp remove sidera --scope user`; then delete the `"mcp__sidera"` entry from
  `permissions.allow` in `settings.json` of Claude Code's configuration directory
  (`~/.claude`, or `CLAUDE_CONFIG_DIR`) unless you had added it yourself. Run the `claude` commands with
  the same `CLAUDE_CONFIG_DIR` (or none) that was set when you ran setup. The plugin's source,
  `adapter-state/<instance>/setup/claude/` in the data directory, goes with the instance; delete it
  only after the marketplace is removed.
- **Codex**: setup installed one plugin, `sidera@sidera`, from a plugin marketplace named
  `sidera` whose files were in the instance's state directory. Remove them with the Codex
  commands, in this order: `codex plugin remove sidera@sidera`, then
  `codex plugin marketplace remove sidera`. Setup never edited your `config.toml` itself,
  and an MCP server `sidera` that you or setup removed with `codex mcp remove sidera` is
  not brought back. (They run in the Codex home that setup recorded, `CODEX_HOME` or else
  `~/.codex`, whatever your environment names now.)
- **Hermes**: `sidera adapter setup` placed the Skill as `skills/sidera/SKILL.md` in the
  directory of each profile you prepared (`hermes -p <profile> config path` names the
  `config.yaml` that lies in it). Delete that `skills/sidera` directory in each profile, and
  restart the gateway that serves the profile so that Hermes stops offering the Skill. Setup
  never edits `config.yaml`, so the `kanban` and `skills` toolsets you added to a platform by
  hand stay as they are: leave them or remove them as you like.

## Claude Code

Adapter details: [the Claude adapter reference](../references/adapter-claude/README.md).

**Setup.** `sidera adapter setup claude` registers the `sidera` MCP server with Claude Code
(`claude mcp add --scope user`, naming the `sidera` program that runs the command, or the
instance's `sidera_path`, and `--home` when the data directory is not the default), adds the rule
`mcp__sidera` to `permissions.allow` in Claude Code's `settings.json` so that a woken session
can call the tools without a prompt, and installs a plugin that gives every session the Sidera
Skill. It asks about each change; the plugin's source, in the instance's state directory, is
written without asking. Setup refuses, saying why and changing nothing, a `settings.json` that is not
valid JSON (Claude Code ignores such a file entirely), a marketplace named `sidera` that points
somewhere else and a `sidera` server in another scope (it sees the registrations Claude Code resolves
outside any project: a `sidera` server registered for one project only wins in that project and is
not seen). A rule you already had is left as it is and
is not removed by `--remove`. `--check` also reports a plugin you disabled or whose source directory is gone, which makes the Skill vanish from new
sessions silently. Setup remembers which Claude Code configuration (`CLAUDE_CONFIG_DIR`, or `~/.claude`) it worked in, and `--remove`,
`--check` and `--refresh` work in that one even if your environment names another. Setup itself
refuses to start in another configuration while one is recorded: run `--remove` first. Details:
[the reference](../references/adapter-claude/README.md#client-setup).

**Needs.** The `claude` command, found on the daemon's `PATH` or named by `claude_path`,
signed in; a controller session needs no `claude` command from the adapter. On Windows
it must be the native `claude.exe`, not the npm `claude.cmd`, which the probe reports as
missing. Set `claude_path` or `sidera_path` on the instance to name a program that is not
on the `PATH`
([Change an adapter's configuration](operations.md#change-an-adapters-configuration)).
Controller support reads files Claude Code does not document (its session registry,
its transcript and its inbox socket), so the Claude Code releases it was checked against
are named in [the reference](../references/adapter-claude/README.md#attached-sessions).
A release that changes them shows as an error in the adapter's log and a
`native_unavailable` endpoint, not silently.

**Starting and resuming.** Every turn of a session is one `claude -p` process. The first
turn creates the session, later turns resume it, and the process ends with the turn.
The process is started detached, so a turn that is running survives a restart of the
daemon where the host allows it ([Install](install.md#supported-platforms)); the adapter
picks the turn up again afterwards. The session record is Claude Code's own file in its
projects directory, so a created session is also an ordinary Claude Code session
history. Claude Code removes the records of sessions idle for longer than its cleanup
period (30 days by default); a session whose record is gone is lost. Creating a session
is answered once the first message is in the record, not when its first turn ends.

**How messages reach the agent.** The node sends a wake-up prompt and the adapter starts
a turn process with it. The agent reads the messages with `recv` through the `sidera` MCP
tools or the `sidera` command; Claude Code sessions have both. A wake-up prompt that
races the start of a turn is refused as busy while a turn process runs; the node
prompts again after the turn if messages are still unread.

**Permission questions.** A turn runs with `--permission-prompts none`, so anything that
would ask a person is denied at once. The preset's `permission_mode` is required and has
no default, so loosening permissions is always your decision; `allowed_tools` adds rules.
The adapter always allows the channel's tools (the MCP tools and the agent commands of
`sidera`) and adds no rule for the owner commands. Your own Claude Code settings still
apply, so a session can run them if the mode or those settings allow it. See [What a
preset allows](agents.md#what-a-preset-allows) for what this means for the node's data.

**As a controller.** A session you opened in a terminal is woken by a line written to its
inbox: a Unix socket on macOS and Linux, a named pipe with an authentication line on
Windows. An idle session is woken within about a second; a session that is
busy takes the wake-up prompt in at its next tool boundary; one waiting on a permission
prompt holds it until you answer. A Claude Code release too old to offer
the inbox leaves the controller without a wake-up: the first Sidera tool result says so
and tells the model to call `recv` with a wait. `/clear` is followed: the endpoint keeps
its address. A Claude Code session you resume is woken at once for what arrived while it
was away. The adapter does not interrupt or close a controller session. Setup is in
[The controller](agents.md#the-controller-a-session-you-started-yourself); the
reference lists what is known about permission prompts and other limits of the inbox
([Attached sessions](../references/adapter-claude/README.md#attached-sessions)).

**Interrupt and close.** `session.interrupt` stops the running turn the way Ctrl-C does.
The answer is `stopped` once the process has ended, and `requested` if it still runs
after 10 seconds, in which case its end is still reported. `session.close` interrupts a
running turn the same way and then closes the session; the session file stays as
history.

**Limits.**

- Do not resume a created session by hand with `claude --resume` while Sidera uses it:
  two writers interleave one record. The adapter ends a creation that it sees with two
  processes as `control_unknown` and leaves both alone, but it does not notice every
  outside writer.
- Claude's operating-system sandbox is not used. The denial of the node's data directory to
  Claude's file tools is best effort; see
  [Presets and access](getting-started.md#what-a-session-can-read-of-the-nodes-data).
- On Windows, delivery to a controller cannot tell a wrong inbox token from a
  delivery; a wake-up that was dropped is found missing and sent again.
- The adapter does not acknowledge on the agent's behalf, so an agent must acknowledge
  every page it reads.

**Configuration.** Instance keys: `claude_path`, `sidera_path`. Preset keys (under
`claude` in the preset file): `project_dir`, `permission_mode`, `model`, `effort`,
`allowed_tools`, `append_system_prompt` and `background_wait_ms`. Examples are in
[Getting started](getting-started.md#5-add-a-preset-both); every key is listed under
[Configuration](../references/adapter-claude/README.md#configuration) in the reference.

## Codex

Adapter details: [the Codex adapter reference](../references/adapter-codex/README.md).

**Needs.** The `codex` command, found on the daemon's `PATH` or named by `codex_program`,
signed in (on Windows `codex.exe`, not a `.cmd` wrapper). Sidera drives Codex through its
App Server, an experimental interface that a much newer release may change; the
releases the adapter was exercised with are named in
[Install](install.md#supported-platforms). Set `codex_program` or `sidera_program` on
the instance to name a program that is not on the `PATH`. While a preset with the
`workspace-write` sandbox exists, the instance's probe may show `pending` and then
`failed`; sessions still work
([Getting started](getting-started.md#4-install-and-enable-adapters-both),
[Operations](operations.md#known-limits)).

**Starting and resuming.** The adapter runs one private App Server (`codex --no-daemon
app-server`), detached, and makes each session a thread of it. The server is not the
managed daemon your own Codex may use. Where the host allows it, it outlives the adapter
and the daemon, so a running turn survives a restart of either, and a restarted adapter
finds the server again and resumes every session. The server runs in an empty directory of its own so
that a sandbox is never given write access to Sidera's files. A session whose thread the
server no longer has is lost.
`sidera daemon stop` leaves the server running on purpose; see
[Stop the daemon and the Codex App Server](operations.md#stop-the-daemon-and-the-codex-app-server)
and, after an upgrade, [The Codex App Server after an upgrade](operations.md#the-codex-app-server-after-an-upgrade).

**How messages reach the agent.** The node sends a wake-up prompt and the adapter starts a
turn on the session's thread with it. The agent reads messages with `recv`. Through MCP
this works under every sandbox. The `sidera` command is offered only to a preset whose
sandbox is `danger-full-access`: under `read-only` or `workspace-write` it cannot reach
the daemon ([What a preset allows](agents.md#what-a-preset-allows)). A wake-up prompt that
races a running turn is refused as busy: the adapter neither queues behind a turn nor
steers it, and the node prompts again after the turn if messages are still unread.

**Permission questions.** Codex's questions are answered at once from the preset and
never reach a person. With the default `approval_policy` of `never`, Codex refuses what the
sandbox does not allow by itself. A command or file-change approval is accepted when the
preset's `approvals` is `approve` and declined otherwise. A request for more permissions,
an MCP server asking the user for input, a tool asking the user for input and a dynamic
tool call are declined or answered with an error, and the model goes on. The channel's
own MCP server is approved in advance. The sandbox of a `read-only` or `workspace-write` preset also denies the
node's data directory to the session's commands on macOS and Linux
([Presets and access](getting-started.md#what-a-session-can-read-of-the-nodes-data)).

**As a controller.** A Codex session you opened is reached through the Codex home that
it shares with the adapter's server: the adapter reads its turns and queues a wake-up
prompt in the Codex queue, and your Codex takes it when it polls the queue. So:

- An idle session starts the turn within about 10 seconds. A busy one gets the prompt
  queued, and it starts right after the running turn ends, never inside it.
- After you press Esc in the terminal, the queue is held until your next turn
  completes, and an active `/goal` can starve it.
- Nothing is delivered while no process has the thread loaded: the terminal is closed
  and the shared daemon has unloaded the thread (after about a minute). The message
  stays queued until a process loads it.
- An ephemeral thread or a sub-agent's takes no queued prompt, so attaching it is
  refused with `wake_unavailable`. An archived thread is refused only when a prompt is
  queued, and its endpoint then shows `native_unavailable`.
- A resumed session is woken after its first Sidera tool call. The adapter does not
  interrupt or close a controller session.

Setup is in [The controller](agents.md#the-controller-a-session-you-started-yourself).

**Interrupt and close.** `session.interrupt` stops the running turn and also ends the
commands that turn had started, which the server would otherwise leave running; the
answer is `stopped` once both are done. `session.close` of a session in the middle of a
turn is answered `close_unconfirmed` and the session stays open, so interrupt first, then
close. Closing archives the thread and keeps its record.

**Limits.**

- The token of a session is also in Codex's logs. See
  [Presets and access](getting-started.md#what-a-session-can-read-of-the-nodes-data).
- On Windows another Codex session that shares the `CODEX_HOME` can revoke the denial of
  the data directory; there the directory's access list is the main guard.
- The adapter does not acknowledge on the agent's behalf, so an agent must acknowledge
  every page it reads.
- Some details of the server's life on Linux and Windows have not been run on those
  platforms; the reference lists them
  ([Known limitations](../references/adapter-codex/README.md#known-limitations)).

**Setting up your own Codex sessions.** `sidera adapter setup <instance>` (see
[Setting up an adapter's client](#setting-up-an-adapters-client)) installs a Codex plugin that
gives every Codex session of yours the `sidera` MCP server, with its tools approved in
advance, and a Skill that tells the agent what Sidera is. The plugin starts the
program named by the instance's `sidera_program`, or else the `sidera` that ran setup (with
`--home` when the data directory is not the default). Setup asks before it adds the
marketplace and installs the plugin. If you had added the server yourself
(`codex mcp add sidera ...`), setup offers to remove it only when that entry differs from the
plugin's server, because it would hide the plugin's. An entry exactly like the plugin's (the same
program and arguments, approved in advance) is kept, and the session gets the same server either
way. If you turn the plugin off in `config.toml`, `--check` reports it and `--refresh` leaves
it off. If another plugin marketplace of yours has lost its files, Codex cannot list plugins
at all; setup then names that marketplace, changes nothing in Codex and waits until you repair or
remove it. `sidera update` refreshes the plugin when the programs change, and a Codex session
that is using it moves to the new process within a few seconds (a tool call in flight at
that moment can be lost; the agent calls again). Setup records the Codex home it set up
(`CODEX_HOME`, or else `~/.codex`), and `--remove`, `--check` and the refresh that `sidera update`
runs work in that recorded home, whatever your environment names now. Setup refuses to run while
the record holds items made in another home, until `--remove` has run under the recorded home.
The adapter's own sessions are not affected: they bring their own server.

**Configuration.** Instance keys: `codex_program`, `sidera_program`. Preset keys (under
`codex` in the preset file): `cwd`, `sandbox`, `approval_policy`, `approvals`, `model`,
`effort`, `allowed_models` and `developer_instructions`. Examples are in
[Getting started](getting-started.md#5-add-a-preset-both); every key and its values are
listed under
[Configuration, manifest and package](../references/adapter-codex/README.md#configuration-manifest-and-package)
in the reference. Set `model` and `effort` in a preset for unattended use: otherwise
the session uses your own Codex defaults.

To let whoever starts a session choose its model and effort, list the choices in
`allowed_models`: a map from a model to the efforts allowed with it, for example
`{"gpt-5.1-codex": ["medium", "high"]}`. Without it nobody can choose and every session follows
the preset. List only efforts the model supports; Sidera accepts `low`, `medium`, `high` and
`xhigh` for Codex, and how Codex reacts to an effort a model does not support is not known.

## Hermes

Adapter details: [the Hermes adapter reference](../references/adapter-hermes/README.md).

Hermes Agent is usually a long-running gateway: you talk to it through a messaging
platform, and much of its work runs unattended (cron jobs, Kanban boards). This adapter
lets such a Hermes delegate work to agents on other nodes, and lets agents on other nodes
hand it work, through its own Kanban board. Nothing in Hermes is replaced: the adapter
reads the board with the `hermes kanban` command, and what comes back reaches the Hermes
conversation that asked, the way a finished card always does. It creates no Hermes
process and no session.

**Needs.**

- The `hermes` command, found on the daemon's `PATH` or named by `hermes_path`. On
  Windows it must be an executable, not a `.cmd` or `.bat` shim: the adapter refuses to
  run a shim, and the probe reports Hermes as missing.
- The adapter is started by the daemon and looks `hermes` up on the `PATH` the daemon was
  started with, so Hermes installed after the daemon started (the Windows installer, for
  one, adds `%LOCALAPPDATA%\hermes\bin` to the user `PATH`) is not found until the
  daemon is restarted. Set `hermes_path` to the absolute path of the executable, or
  restart the daemon.
- A Hermes release from 0.22.2 up to, but not including, 0.23.0. Hermes is a 0.x project
  and a minor release may rename a flag or a field of the output the adapter reads, so
  the adapter does not read or write anything while Hermes is outside this range, tells
  the node (the probe and the node's status say which release it found), and takes up
  its work again when the release is back in range. It looks again every few minutes.
  Move to a newer Hermes only with a Sidera release that names it.
- A Kanban board that already exists in that Hermes, and no Hermes profile named
  `sidera`, `sidera-inbox` or beginning with `sidera:`. Those names are the adapter's
  assignees: Hermes starts a worker for a card whose assignee is a profile.
- A daemon new enough to give an adapter direct endpoints that are hidden from the
  directory; on an older one the probe says the node needs an update.
- A running Hermes gateway that serves each profile whose agents delegate, with its
  Kanban notifier on, which is the default (`kanban.notify_in_gateway`). The agent is
  woken by that gateway when a card is completed. It works the same with the one gateway
  that serves every profile (Hermes' default) and with a gateway per profile; the notifier
  reads the setting of the profile its gateway was started from, normally the default
  profile for the shared one. Without a running gateway, or with the setting false,
  results are written to cards but no conversation is woken. The adapter keeps working in
  the second case and says so: `sidera adapter list` shows the instance's availability
  with the reason as a `!` line under its row, and `sidera adapter setup` warns of the profiles it prepares. It checks the profile's
  own setting and the default profile's, since it cannot see which gateway runs, and it
  does not tell you that a gateway is not running.
- The toolsets `kanban` and `skills` enabled on every platform the agent delegates from
  (`home_platform`, and `cron` for scheduled jobs). `kanban` gives the agent the tools that
  create a card; `skills` lets it load the Sidera Skill that says how to address one. Hermes
  leaves `kanban` off by default, and a profile whose toolset list for a platform was chosen
  by hand may lack either. Without `skills` the agent creates an ordinary card that this
  adapter never sees. The adapter checks the home platform and says in the instance's
  availability which toolset is missing; `sidera adapter setup` checks all the platforms it is given.

**Install and enable.** The configuration has required settings, so it is given when
the instance is enabled. Save it as `hermes.json`:

```json
{
  "hermes_home": "<absolute path of the HERMES_HOME of the gateway>",
  "board": "<board slug>",
  "home_profile": "<profile>",
  "home_platform": "<platform, such as telegram>",
  "home_chat_id": "<chat id>",
  "aliases": [
    "builder=<node-id>/<endpoint-id>;modes=new_session,message;presets=light"
  ]
}
```

```sh
sidera adapter install ~/sidera-release/adapters/hermes
sidera adapter enable hermes --config hermes.json
sidera adapter list
```

The probe goes to `passed` when `hermes` runs, its release is in the range, the board
exists and no profile has an adapter name. The instance registers one endpoint, named
`Hermes` unless `display_name` says otherwise, which other nodes find with
`directory`.

**The settings that matter.** `sidera adapter enable` and `sidera adapter config`
refuse a setting the adapter does not have, a value of the wrong type or outside its
options, and a missing required setting, naming the setting. What only the adapter can
judge, such as a `hermes_home` that is not there, it reports when it starts.

| Setting | What it is |
| --- | --- |
| `hermes_home` | The `HERMES_HOME` given to every Hermes command the adapter runs. Required, absolute. The daemon does not run in your shell, so the adapter never relies on its environment. |
| `board` | The slug of the one board this instance serves. Required. |
| `home_profile`, `home_platform`, `home_chat_id` | Where a request goes when it belongs to no conversation: the profile whose gateway is woken and the chat it answers in. Required. `home_chat_type` (`dm`, `group`, `channel` or `thread`), `home_thread_id` and the other `home_*` settings complete it when the platform needs them. |
| `aliases` | The names a card may address, one entry each: `<name>=<address>;modes=<mode>[,<mode>];presets=<preset>[,<preset>]`. The modes are `new_session` (start a session on that runtime endpoint with one of the presets listed) and `message` (send to that address). Presets go with `new_session` and only with it. Required, at least one. |
| `creator_profiles` | If set, only cards created by these Hermes profiles are served. |
| `no_reply_timeout` | How long a card waits for the other side: 24h. |
| `hermes_path`, `kanban_home`, `display_name` | The `hermes` program (default `hermes`), the `HERMES_KANBAN_HOME` if you set one, and the name in the directory (default `Hermes`; it may not hold control or invisible characters, and an instance whose `display_name` breaks the rules does not start, with the problem named). |

The other settings (how long a claim, a remote session and a conversation's endpoint are
kept, the polling interval and the caps) are listed with their defaults in
[the reference](../references/adapter-hermes/README.md#instance-configuration).
Durations are written `90m` or `168h`. Change them with `sidera adapter config hermes
--config <file>`, which takes the whole configuration, as for the other adapters
([Change an adapter's configuration](operations.md#change-an-adapters-configuration)).

**The alias table is the allow-list.** A card on the board is not trusted: any program of
your operating-system user can write the board. So a card never names an address. It
names an alias, and only the modes and presets you listed for that alias can be used.
Everything a card can make this node do is in this table, and an agent of Hermes learns
the aliases and nothing more. Find the address behind a name with
`sidera-adapter-console directory --node <node-id>`, and write it into the entry.
`creator_profiles` narrows who may ask, but Hermes lets a command-line caller state any
creator, so treat it as a second line of defence, not a boundary.

**Setup.** Hermes needs two things before its agents can delegate: the Kanban tools
(toolset `kanban`, off by default on the chat platforms and on cron), and a Skill that tells
the agent how to write a card, which the agent can load only while the toolset `skills` is
on. `sidera adapter setup` writes the Skill from your alias table and board into each profile
you name, asks before it does, and reads which of the two toolsets each platform has. The
instance has to have run once (enable it, start the daemon):

```sh
sidera adapter setup hermes -- --profile <profile>
```

`--profile` names a profile whose agents delegate and repeats (the default is `home_profile`);
`--platform` names a platform they delegate from and repeats (the default is `home_platform`),
and setup remembers the platforms, so that `--check` reads the same ones; `-- --help` lists
the options. Setup asks one question for each profile (`[Y/n]`), or none with `--yes`. It places
the Skill as `skills/sidera/SKILL.md` in the directory of the profile and remembers that it
did. A Skill file that setup did not write, or that was changed since, is replaced only after
you say yes (and `--yes` says yes): its content is discarded and no copy of it is kept. A copy that
an earlier release placed (`sidera-adapter-hermes setup --apply`) is taken into that record when
it is exactly what this release writes, and replaced, after you say so, when it is not. The
copy `setup --apply` kept in the instance's `skill` state directory is no longer used.

`sidera adapter setup hermes --check` lists what has drifted: a Skill that is out of date, was
changed or deleted, and the toolsets and the notifier described below. A missing toolset or a
notifier that is off makes `--check` exit 1, and nothing in setup fixes it; `sidera update` and
`adapter remove` do not look at it. When you change the
aliases or the board, `sidera adapter setup hermes --refresh` rewrites the Skill of the profiles that were set
up. It asks nothing, and it neither sets up a profile that was not nor makes a Skill you deleted. `--remove` deletes the Skill setup placed (only while it is unchanged).

For a platform that lacks `kanban` or `skills`, setup says in a note under its plan (also when
there is nothing to set up, and also for `cron`), and `--check` says as drift for the home
profile and platform and for the profiles and platforms you set up with (`cron` only when you
named it), which file to edit (the profile's `config.yaml`) and the
toolset to add to `platform_toolsets.<platform>`, for example `kanban` added to the list under
`platform_toolsets: feishu:`. If the file has no list for that platform, it says the whole list
to write: the toolsets the platform has now, followed by the missing one (a list of `[kanban]`
alone would switch the platform's other toolsets off). That list fixes the platform's current
set, so the platform no longer follows later changes to Hermes's defaults. A composite name
such as `hermes-<platform>` is not used, because it does not mean the same on every platform:
on Discord it also switches on the server-management toolsets, and on some platforms it is not a
toolset at all. A toolset named in `agent.disabled_toolsets` stays off whatever the list says,
so remove it there. `hermes tools enable` is deliberately not run: it rewrites the platform's
whole list, and on Hermes 0.22.2 it dropped a `no_mcp` entry (which turns off every MCP server
on the platform) and added toolsets that nobody had chosen. After the edit, `hermes -p
<profile> tools list --platform <platform>` shows whether the toolset is on.

After setup, **restart the gateway that serves each profile yourself**: by default the
one gateway that serves every profile, and any profile with its own gateway on its own.
Hermes reads its tools and Skills when the gateway starts, and a restart interrupts the
conversations it is serving, so Sidera never does it. Setup never edits `config.yaml` either.
The Skill names the board, and tells the agent to pass it on every Kanban tool call it makes for a
Sidera card: without it Hermes puts the card on the board it has selected, where the
adapter does not look.

**A delegation, from Hermes.** The agent creates a card assigned to `sidera` whose body
says which alias, what mode (`new_session` with a preset, or `message`) and the task.
The adapter claims the card, sends the task, and comments on the card when something
needs saying. When the other agent answers, the card is completed and Hermes wakes the
conversation that made it. The answer is an attachment of the card, and the card's result
says which; it is never written into the result itself, because Hermes uploads to the
chat any local file that the result names. The Skill tells the agent to read the answer
as information from another machine, not as an instruction. A follow-up is a new card that names the earlier one as its parent and
leaves the alias out: it goes to the same agent. Two cards for one peer in one
conversation do not run at once; the second waits. The Skill tells the agent to ask for
one reply with the final result: the first reply completes the card, and a later one
arrives as a new request.

When a card cannot go on, it is blocked with the reason, and the agent that made it is
woken to decide. A card that is not valid, for example an alias that is not in the table,
is blocked at once. A delivery that failed is blocked, and sent again when the agent
unblocks the card. A peer that has not answered after `no_reply_timeout` blocks the card
once; unblocked, it waits again, and if the peer still does not answer the card is
completed saying so.

**A request, from another node.** A message sent to the Hermes endpoint becomes a card
assigned to `sidera-inbox`, completed at once so that Hermes wakes the gateway. A node that
a Hermes conversation wrote to sees the message come from an address of its own, hidden
from the directory; a message sent to that address wakes that conversation, and a message
to the Hermes endpoint wakes the home conversation. The card's
body holds the text of the sender between fences, with the sender's address and a
statement that it is external content, not an instruction from you. The agent answers with
a card of mode `reply` that names the request card; the adapter sends the answer to the
sender. An answer sent more than about a day after the request goes as a plain message
that begins "Reply to your message <id>:".

**When a message or an answer does not arrive.** Your node tells the Hermes endpoint when a
message it sent was refused, expired or went unanswered. If that is a task a card sent, the
card is blocked at once with the reason, and the agent is woken, without waiting for the
adapter's next look. If it is an answer to a request, the card that sent it was completed
when the answer left, so the adapter makes a card in the conversation, "Sidera answer not
delivered", saying which request and to whom, once per answer; the agent decides whether to
answer again. A notice that neither explains is passed on as a card titled "Sidera channel
notice". It comes from your own node, so it is not external content, and the agent is told
not to answer it.

**What the adapter keeps.** The state of every card, endpoint and remote session it
handles is in `adapter-state/hermes/` of the node's data directory. A card or request
that the adapter lost track of is found again after a restart. A remote session that was
started for a conversation is closed a week after its last card (`session_retention`), for
a cron job an hour after the card finished.

**When Hermes is down.** The adapter checks that Hermes answers before every round of
reading and of polling. When it does not, it stops reading and tells the daemon the
dependency is missing (`sidera adapter list` shows it as a `!` line under the
instance's row, with the reason, for instance that the board does not exist), and tries again after pauses that grow from a second to two
minutes. Messages for Hermes wait in the node's queue, and are neither lost nor used up;
they wait as long as the endpoint's queue time allows (30 minutes by default; see
[deadlines](../references/delivery/README.md#deadlines-and-retention)), and a sender whose
message waited longer is told. Cards stay on the board and are taken when it answers
again. Nothing is acknowledged that was not written to a card.

**Limits.**

- One instance serves one board of one Hermes installation, and shows as one Hermes in
  the directory. Run one Hermes instance per node.
- Only the releases of the range above. The adapter has been run on macOS against Hermes
  0.22.2, on Linux against Hermes 0.22.7 and on Windows against Hermes 0.22.7 (installed
  as `hermes.exe`).
- Hermes serves every profile from one gateway by default, so a profile created while it
  runs is served at once. For a profile you make only for the adapter's tests, set
  `gateway.standalone: true` in it first, or the running gateway serves it.
- When Hermes serves every profile from one gateway (its default), a wake for the home
  chat of a profile other than the default is skipped while that chat is an `api_server`
  chat with no conversation yet in that profile, and delivered once it has one. Choose as
  the home target a chat that has had a conversation.
- Disabling or removing the instance does not recall what it already sent. A new session
  that was still queued for a node that could not be reached is created there when the
  node is back (within the message's queue time), and nothing here is left to receive its
  answer; the owner of that node closes it with `sidera endpoint close`.
- On Windows `hermes` must be an `.exe`; a batch shim is refused, because `cmd.exe` would
  interpret the characters of the arguments, and some of them come from other agents.
- Hermes clears the delivery settings of cards that are done and idle for 30 days. An
  answer to a request is possible for as long as the adapter keeps the request (30 days),
  but a conversation that old may no longer be woken.
- The agent cannot interrupt or close anything on the other node; the adapter closes the
  remote sessions it started when they are idle, as above, and never one that a card is
  waiting on.
- The text of a request is another agent's, and the agent must read it as information. The
  wording around it says so; what the agent does with it is the Hermes agent's own
  permissions and your own policy for it.

**Configuration.** Instance keys are the table above and the reference's list. There are
no presets: the adapter starts no process.

## The console

Adapter details: [the console adapter reference](../references/adapter-console/README.md).

The console is the owner's way into the channel, not an agent. One program,
`sidera-adapter-console`, is both halves: the adapter the daemon runs, and the client you
run in a terminal.

**Starting.** `sidera adapter enable console` registers one direct endpoint, `owner`. The
key is fixed, so its address is the same after every restart of the adapter and the
daemon; `sidera-adapter-console whoami` prints it. The console takes no presets and no
instance configuration, and it has no sessions of its own.

**How messages reach you.** Nothing wakes the console. A message to the owner endpoint
waits in its queue like any other, and you read it with `recv`. The client prints each
message with its sender and id, and acknowledges what it printed, only after printing.
`recv --wait` accepts at most 30 seconds. Replies to a session you started with `new`
come back to this endpoint only if the preset tells the session to reply
([Working with agents](agents.md#starting-a-session-on-another-machine)); the session
then answers the sender of its first message.

**What it does.** `new` starts a session on a runtime endpoint of any node (see
[Starting a session on another machine](agents.md#starting-a-session-on-another-machine))
and prints the session's address; `send`, `reply`, `recv` and `status` follow
([Getting started](getting-started.md#7-start-the-first-cross-machine-session-a)).
`peers` lists the nodes you can address, and `directory [--node <id>]` lists the endpoints
a node shows to the owner endpoint, which is how you find the address of a runtime before
`new`; a page that has more ends with the command for the next one (``Run `sidera-adapter-console
directory --node <id> --cursor <c>` for the next page.``).
Both need a daemon that offers them to a direct endpoint; on an older one they say that
the daemon is too old and exit with code 1. The console has no `control`, so it cannot
interrupt or close a session it started: close one with
`sidera endpoint close <session-address>` on the node that hosts it. The console reaches
the daemon over a
channel on this machine that only the owner can use, so run it as the user the node
runs as. If `new` gets no control reply within its `--timeout` (120 seconds unless you give another) it exits with code 10 and
prints the message id to check with `status`.

**Limits.**

- A `reply` after the adapter restarted needs `--to <address>`, which `recv` prints: the
  adapter keeps the senders of what it showed in memory only.
- A message acknowledged by `recv` is not shown again. A client that dies between the
  print and the acknowledgement leaves the page, and the next `recv` shows it again.

**Configuration.** None. The instance is `console` by default; the client takes
`--instance` and `--home` if you named it otherwise or keep the node's data elsewhere.

## Choosing between them

- To start work on another machine, create a session there with the adapter of the agent
  you want and a preset that suits the job: [Working with
  agents](agents.md#starting-a-session-on-another-machine). The Claude Code and Codex
  adapters create sessions the same way; they differ in how the agent is woken, in what a preset
  can offer and in the limits above.
- To have an agent you already work with talk to others, attach it as a controller. A
  Claude Code controller is woken within about a second; a Codex controller within
  about 10 seconds, and not while you wait at Esc. These times start once the node
  decides to wake the session.
- To let a long-running Hermes delegate work to other machines, and be reached by them,
  use the Hermes adapter: it works through the Hermes Kanban board, so Hermes needs no
  session and no wake-up of its own.
- To read what comes back yourself, or start a first session by hand, use the console.
