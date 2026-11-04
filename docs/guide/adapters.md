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
preset for it ([Presets](getting-started.md#5-add-a-preset-both)). Sidera ships three:

- **Claude Code**, `sidera-adapter-claude`, instance name `claude`.
- **Codex**, `sidera-adapter-codex`, instance name `codex`.
- **The console**, `sidera-adapter-console`, instance name `console`.

An agent uses Sidera in one of three ways, described in
[Working with agents](agents.md): in a session Sidera created from a preset, as a
controller (a session you opened yourself and attached), or through the owner's console.
This page says what each adapter does for each way.

All three are built for macOS, Linux and Windows on amd64 and arm64
([supported platforms](install.md#supported-platforms)). Sidera does not install the
agents: the daemon must be able to find `claude` or `codex`, and it must be signed in.

## Summary

| | Claude Code | Codex | Console |
| --- | --- | --- | --- |
| Connects | `claude` | `codex`, through a private App Server | The node's owner, from a terminal |
| Created sessions | Yes | Yes | No |
| Controller sessions | Yes | Yes | No |
| Direct endpoint | No | No | Yes, the `owner` endpoint |
| An idle created session is woken by | A new `claude -p --resume` process | A new turn on the session's thread | Nothing: you read with `recv` |
| A controller is woken | Through the session's inbox, within about a second | Through the Codex queue, within about 10 seconds when idle | Not applicable |
| Interrupt and close | Created sessions only | Created sessions only | Not offered: close a session with `sidera endpoint close` |
| Permission questions | Never asked: denied at once | Never asked: answered from the preset | Not applicable |

How to read it:

- **Created sessions** are the ones a preset creates when another agent or the console
  runs `session.create`. **Controller sessions** are the ones you opened yourself
  and that attached on first use of a Sidera tool
  ([The controller](agents.md#the-controller-a-session-you-started-yourself)). The
  console hosts neither: it has no presets and no sessions.
- A **direct endpoint** is read by the adapter on behalf of a person or program, not by
  a native agent session. Only the console has one.
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

## Claude Code

Adapter details: [the Claude adapter reference](../references/adapter-claude/README.md).

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

**Configuration.** Instance keys: `codex_program`, `sidera_program`. Preset keys (under
`codex` in the preset file): `cwd`, `sandbox`, `approval_policy`, `approvals`, `model`,
`effort` and `developer_instructions`. Examples are in
[Getting started](getting-started.md#5-add-a-preset-both); every key and its values are
listed under
[Configuration, manifest and package](../references/adapter-codex/README.md#configuration-manifest-and-package)
in the reference. Set `model` and `effort` in a preset for unattended use: otherwise
the session uses your own Codex defaults.

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
`new`; a page that has more ends with `next cursor: <c>`, to give back with `--cursor`.
Both need a daemon that offers them to a direct endpoint; on an older one they say that
the daemon is too old and exit with code 1. The console has no `control`, so it cannot
interrupt or close a session it started: close one with
`sidera endpoint close <session-address>` on the node that hosts it. The console reaches
the daemon over a
channel on this machine that only the owner can use, so run it as the user the node
runs as. If `new` gets no control reply within its `--timeout` it exits with code 3 and
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
  agents](agents.md#starting-a-session-on-another-machine). Both agent adapters
  create sessions the same way; they differ in how the agent is woken, in what a preset
  can offer and in the limits above.
- To have an agent you already work with talk to others, attach it as a controller. A
  Claude Code controller is woken within about a second; a Codex controller within
  about 10 seconds, and not while you wait at Esc. These times start once the node
  decides to wake the session.
- To read what comes back yourself, or start a first session by hand, use the console.
