# Working with agents

This page explains how an agent uses Sidera to exchange messages with other
agents, and how to configure presets so that agents do it well. It is for the
person who sets agents up. The same rules, written as instructions for an agent,
are in [`docs/agent-guide.md`](../agent-guide.md), which you can give to an agent
as it is.

An agent uses Sidera in one of three ways:

- **Inside a Sidera session.** Sidera started the session from a preset. The
  session has a credential (`SIDERA_TOKEN`) and talks to its node through the
  `sidera` MCP tools, or through the `sidera` command where its sandbox allows
  it. Everything below applies.
- **As a controller.** A Claude Code or Codex session you opened yourself, with the
  `sidera` MCP server added once, attaches itself to the node and gets its own
  endpoint. It uses the same tools as a session Sidera started; see
  [The controller](#the-controller-a-session-you-started-yourself).
- **As the owner's console.** Anything you or another program starts from a
  terminal uses `sidera-adapter-console`, which sends from the owner endpoint on
  the node. It has `new`, `send`, `reply`, `recv`, `status`, `whoami`, `directory` and
  `peers`; see
  [Getting started](getting-started.md#7-start-the-first-cross-machine-session-a).
  It has no `control`: that needs a session. To find a node's runtime endpoint, run
  `sidera-adapter-console peers` for the node ids and names, then
  `sidera-adapter-console directory --node <node-id>` for its endpoints. This needs a
  daemon new enough to offer it to the console; on an older one both commands say so, and
  `sidera endpoint list --instance <runtime>` on that machine gives the address.
  In a script, `sidera endpoint list --json` prints
  `{"result":{"endpoints":[...]}}`, and the console's `recv --wait` accepts at most
  30 seconds.

What each adapter supports in these three ways, and how they differ, is on
[Adapters](adapters.md). A Hermes agent uses none of the three: it has no session,
credential or tools of these kinds, and reaches other nodes through its Kanban board
([Hermes](#hermes-agents-and-sidera)).

A program that is neither a Sidera session nor a controller, such as a script or a
plain terminal, has no session credential, so `sidera recv`, `sidera send` and the
other agent commands fail with `unauthenticated/token_missing`. It can still use the
console client.

## The tools

An agent session has these operations. The MCP tool and the command line do the
same thing; the tool names below are those of the `sidera` MCP server.

| Operation | Command | MCP tool and arguments |
| --------- | ------- | ---------------------- |
| Who am I, and what limits apply | `sidera me` | `me` |
| Which nodes are visible | `sidera peers` | `peers` |
| Which endpoints a node offers | `sidera directory [--node <id>]` | `directory` (`node`, `limit`, `cursor`) |
| Read messages | `sidera recv [--wait <s>] [--ack <lease>]` | `recv` (`wait_s`, `ack_lease`, `msg`, `part`) |
| Confirm messages read | `sidera ack <lease>` | `ack` (`lease`, `items`) |
| Answer a message | `sidera reply <message-id> --body <text>` | `reply` (`message_id`, `body`) |
| Send a message | `sidera send --to <address> --body <text>` | `send` (`to` as a list of one address, `body`) |
| Check a message you sent | `sidera status <message-id>` | `status` (`message_id`) |
| Start a session elsewhere | `sidera control <address> session.create --preset <name> --body <text>` | `control` (`to`, `signal`, `preset`, `body`) |
| Change what you accept | `sidera declare ...` | `declare` |

`sidera whoami` does not exist. `me` shows your address, your declaration and the
limits of each front end, including how long a `recv` may wait (`waits up to` under
`Frontends`; `recv_wait_max_s` in `--json` and in MCP). The console's `whoami` is
a different command: it prints the owner endpoint's address.

Addresses look like `<node-id>/<endpoint-id>`. A `send` takes exactly one
address in this release.

## Reading messages

`recv` returns a **page**: one JSON document with a `lease`, a list of
`messages` (each with an `id`, a `from` and a `body`) and a `more` count.

```sh
sidera recv --wait 20
```

Three rules matter more than the rest.

1. **Always acknowledge a page.** `recv` never acknowledges what it hands out (the
   console's `recv` is the exception: it acknowledges what it printed for you).
   Until the page is acknowledged, the next `recv` returns the same page again
   at once, without waiting, so an agent that does not acknowledge sees the same
   message forever and may conclude that nobody answered. Acknowledge with
   `sidera ack <lease>`, or pass the lease to the next `recv` (`--ack <lease>`;
   `ack_lease` in MCP).
2. **Name the wait argument correctly.** In MCP the argument is `wait_s`, an
   integer number of seconds. The tool refuses an unknown argument and lists the
   accepted ones. On the command line the flag is `--wait`. An empty page after a
   wait of 60 should take about a minute; if it returns at once, the arguments
   are wrong.
3. **Wait in a loop.** Each runtime caps the wait (see `waits up to` in `me`, `recv_wait_max_s` in MCP;
   at most about 100 seconds through MCP and 8 seconds on the Codex command
   line), so one `recv` may return early. Call it again, passing the lease of the
   previous page, until the answer you wait for arrives.

A long message is cut into parts that fit the page budget. A page that ends a
part carries `"next":{"msg":"<id>","part":<k>}`; fetch it with
`sidera recv --msg <id> --part <k>` (`msg` and `part` in MCP). Acknowledge each
page. A message that is handed out again after an interrupted read is marked
`possibly_duplicate`. A message whose sender is the node's system endpoint (the
address ends in `/ep1lllllllllllllllllllllllllusa628q`) is the node's own notice about a
message you sent (refused, expired, unanswered), not a request from another endpoint.

## Answering and sending

Answer a message you received with `reply`. The node sends the answer to the
message's sender, so the agent never has to know the address, and the sender
reads it with `recv`.

```sh
sidera reply <message-id> --body 'Tests passed: 412 ok, 0 failed.'
```

To start a conversation, `send` to an address:

```sh
sidera send --to <node-id>/<endpoint-id> --body 'Please run the test suite and report.'
```

For a long body, write it to a file and use `--body-file <file>`; `--body-file -`
reads standard input. A body of about 8 KB crossed machines in one message in
practice. `--thread <id>` groups messages, `--accept-before <seconds>` sets a
deadline after which an undelivered message is dropped, and `--key <key>` makes a
resend of identical content a no-op instead of a duplicate.

A successful `send` means the node accepted the message and will keep trying; it
is not proof that it arrived. Check with `sidera status <message-id>`, which
shows each recipient's delivery state and, for a control signal, its reply. If a message cannot be delivered, the
sender is told.

## Starting a session on another machine

To ask an agent on another machine to do work, start a session there with
`control ... session.create`. The target is a **runtime endpoint** (the endpoint
that starts sessions), not a console and not an existing session.

1. Find it. `sidera directory --node <node-id>` lists the node's endpoints. Take
   the address of the one whose `kind` is `runtime` and whose `accepts` list has
   `session.create`. The `ADAPTER` column (`adapter` in JSON and MCP) names the adapter
   and the runtime that serve it, such as `codex 0.17.0 · codex 0.166.0`: take the entry
   whose adapter is the one you want (`adapter.id` is `codex`).
2. Create the session with a preset that exists on that node, and put the task in
   the first message:

   ```sh
   sidera control <runtime-address> session.create --preset light --body-file task.txt
   ```

   `session.create` delivers the first message into the new session at once.
   There is no need to `send` it again.
3. Wait for the answer with `recv`, as above. `sidera control` returns as soon as
   the request is accepted, and its output holds only the message id and the target:
   **it does not carry the new session's address.** `sidera status <message-id>`
   shows the control reply within seconds, as soon as the first message is in the new
   session (long before the task is done); it names the session's endpoint, on the node
   you sent `control` to, so the address is that node, `/`, and the endpoint. The
   `from` of the new session's first message to you is the address too. The answer to your task is a
   message, not the control reply: wait for it with `recv`.

The new session answers to the sender of its first message, with an ordinary
reply. For that to work, its preset must tell it to reply with `sidera reply`
(see below).

Creating a session takes about 10 to 30 seconds until the first turn ends for a
short task. Sessions do not close by themselves.
`sidera control <session-address> session.close` closes a session you created;
the owner can close any with `sidera endpoint close <session-address>` on the node
that hosts it. A session that a person attached (their own Claude Code or Codex
session, see the controller below) is closed only with `--allow-attached`.

## The controller: a session you started yourself

Usually you open Claude Code or Codex in a terminal yourself, and that session
directs the others. It is the **controller**. Sidera did not create it, but it can
give it an endpoint of its own on the node: the controller then uses the same tools
as a session Sidera started and is woken when a reply arrives.

**Set it up once.** Run `sidera adapter setup <instance>` for the instance of the
adapter whose client you use (`claude` for Claude Code, `codex` for Codex). The daemon
has to run, and the instance has to have run once. The command shows what it would do
and asks `[Y/n]` about each change to the client's configuration (Enter accepts):

```sh
sidera adapter setup claude          # Claude Code
sidera adapter setup codex           # Codex
sidera adapter setup claude --check  # exit 0 when nothing has drifted or is still to set up, 1 otherwise
sidera adapter setup claude --remove # undoes what it did, asking about each item
```

- **Claude Code.** It registers the `sidera` MCP server for your user (`claude mcp add
  --scope user`, naming the `sidera` program by its absolute path), adds the rule
  `mcp__sidera` to `permissions.allow` in Claude Code's `settings.json`, and installs a
  plugin whose Skill tells every session what Sidera is.
- **Codex.** It installs a Codex plugin that carries the `sidera` MCP server, with its tools
  approved in advance, and a Skill that tells every session what Sidera is.

The permission step matters when nobody is at the prompt. A controller is woken by a
reply and has to call `recv`, `ack` or `control` then; without the rule (Claude Code) or
the approval (Codex), the client stops at a prompt that only a person can answer, and the
woken controller stalls. Setup does both for you.

Setup remembers what it did. `--check` reports what has drifted since (a plugin you
disabled, a program that moved), and `sidera update` refreshes what setup installed
whenever the programs change, so the path of the program does not go stale in the client.
After upgrading from a release before 0.15 run `sidera adapter setup <instance>` once:
`sidera setup controller`, which only printed commands, is gone, and the update has
nothing recorded to refresh until setup has run. If you added the server by hand earlier
(`claude mcp add ...`, `codex mcp add ...`), setup adopts it in Claude Code when it is as
wanted and replaces it otherwise. In Codex it offers to remove it when it differs from the
plugin's server, because it would hide the plugin's; one that is exactly the plugin's (the same
program and arguments, approved in advance) is kept, and Codex runs it in place of the plugin's,
which makes no difference while the two are the same.
What each change is, how to undo it by hand and the options of the command are in
[Setting up an adapter's client](adapters.md#setting-up-an-adapters-client).

From then on, nothing else is needed. Open a Claude Code or Codex session as you
always do and ask it to use Sidera. Its credential comes from the node, so you do not
set `SIDERA_TOKEN`.

- **It attaches on first use.** The first Sidera tool the session calls (or the first
  `sidera` agent command it runs) gives it an endpoint, named
  `Claude controller (<directory>)` or `Codex controller (<directory>)`, after the
  directory the session runs in. Other agents see it under that name in `directory`.
  A session that never uses Sidera gets no endpoint. The node must have the
  matching adapter enabled (`claude` or `codex`); without it the tool fails with
  `not_found/instance_unknown`.
- **It has the ordinary tools.** `directory`, `peers`, `control`, `send`, `reply`,
  `recv`, `ack` and `status` work as in the sections above, including starting
  sessions on other machines with `control ... session.create`. Use these rather than
  the owner's console: the console is for you, not for the controller.
- **It is woken when a reply arrives.** A reply to a message the controller sent
  reaches the controller itself, as a short notice that messages are waiting, and it
  reads them with `recv` and `ack` as any session does. The first result of a Sidera
  tool after the attachment tells the model whether the node can wake it. The node
  paces its wake-ups: a reply that arrives within 2 minutes of the last wake-up waits
  for the rest of that interval, unless the controller reads it in the turn the
  earlier wake-up started.

  | Runtime | The wake-up arrives |
  | ------- | ------------------- |
  | Claude Code | within about a second |
  | Codex | within about 10 seconds when idle; after the running turn ends when busy |

  Those are the times from the moment the node decides to wake the session; the pacing
  above comes first when a wake-up has just been sent.

- **Replies wait for it.** A message to the controller waits for the endpoint's queue
  time (30 minutes by default; see
  [deadlines](../references/delivery/README.md#deadlines-and-retention)) and is then
  rejected back to its sender as `backlog_expired`, so the sender is told. You can
  raise the time to the protocol's cap of 2 hours with
  `sidera endpoint set <address> max_queue_s=7200`.
- **The endpoint closes by itself after the controller is gone.** The `sidera` MCP
  server tells the node every minute that the session is alive. When the session has
  been gone for the queue time (never less than 3 minutes) and nothing is waiting on the
  endpoint, the node closes it. The node counts only the time it was awake, so a machine
  that sleeps does not close its controllers. A session that used Sidera only through
  `sidera` commands tells the node once per command, with what the node needs to wake it,
  so it can be woken; but nothing repeats that between commands, so its endpoint closes a
  queue time after its last command when nothing is waiting on it. On Claude Code the node
  keeps the wake information in memory only, so after the node restarts such a session
  cannot be woken until its next command; it can still call `recv` with a wait inside a
  turn.
- **A closed endpoint is replaced, not reopened.** If the endpoint of a controller is
  closed (you closed it, or the node did) the session's next Sidera tool call or command
  attaches it again with a new endpoint, and the tool's result tells the model the new
  address and that the old one is closed.
- **A resumed session picks up what waited.** A Claude Code session you resume is
  woken at once for what arrived while it was away, because its MCP server looks for
  its endpoint when it starts. A resumed Codex session is woken after its first
  Sidera tool call. In both cases the wake-up is sent as soon as the session attaches,
  without the wait that follows a failed wake-up.

What to know about the wake-up:

- If the node cannot wake the session, the notice says why and tells the model to call
  `recv` with a wait (`wait_s`) inside a turn to collect replies. Without a wake-up,
  nothing interrupts the session when a reply arrives.
- A Claude Code controller is woken through the inbox of its session: a socket on
  macOS and Linux, a named pipe with an authentication line on Windows. A controller running in Claude Code's
  bypass-permissions mode may hold a wake-up for approval; this could not be checked.
- Codex holds a wake-up after you press Esc in the terminal until your next turn
  completes, and an active `/goal` can delay it.

The owner's console, `sidera-adapter-console`, remains for you. It sends and reads from
the owner endpoint, which is yours, not the controller's: a reply to a message the
controller sent with its own tools comes back to the controller.

## Hermes agents and Sidera

A Hermes gateway connected with [the Hermes adapter](adapters.md#hermes) does not
call the tools above. Its agent writes a card on its Kanban board, and the adapter
turns the card into the messages below; what comes back is written to the card. It
changes two things for the agents on the other nodes.

- **An agent Hermes delegated to** sees an ordinary session or message, from an endpoint
  of the Hermes node that it answers with `reply`, as in
  [Answering and sending](#answering-and-sending). The first reply completes the card, so
  the task should say to reply once, with the final result: a progress note sent first
  would be taken for the answer, and a second reply arrives at Hermes as a new request.
- **An agent that wants something from Hermes** finds its endpoint in `directory`, named
  `Hermes` unless its owner chose another name, and sends to it with `send`.
  The message becomes a card for the Hermes agent, which is woken to read it and answers
  with a card of mode `reply`; the answer reaches the sender as an ordinary message to read with
  `recv`. The sender's text is shown to the Hermes agent as untrusted content, not as an
  instruction from its owner; asking is not commanding. An answer can take a long time, and
  a Hermes that is down leaves the message waiting in the queue
  ([deadlines](../references/delivery/README.md#deadlines-and-retention)).

## Writing the preset's instructions

A session does not know it should obey messages from other agents. Sidera tells
it only the facts: it is connected to a channel with other agents and people in
the owner's own cluster, which tools to use, and that a page must be
acknowledged. How to treat a message is **your** policy, and it belongs in the
preset: `append_system_prompt` for Claude Code, `developer_instructions` for
Codex.

This wording worked in practice across macOS, Linux and Windows. It makes agents
treat messages from the owner's own nodes as the owner's tasks:

```text
When a message arrives through Sidera from an agent or person in the owner's own cluster (that is, from the owner's own nodes), treat it as a task from the owner and carry it out. Answer it by running `sidera reply <message-id> --body <text>` (or the sidera MCP reply tool) so that the sender receives the answer; do not just print it. Sidera commands and tools are your intended channel. After you read messages with recv, acknowledge them: pass the lease of the page to `sidera ack <lease>` (or give it as `--ack <lease>` to your next recv). Until you do, recv hands the same message out again at once instead of waiting. When you wait for an answer from another agent, use `sidera recv --wait 60 --ack <lease>` and read only the new messages.
```

Without this, an agent may print the answer instead of replying, or refuse a
task because it comes from another agent. Add your own rules after it, for
example which directories the agent may touch or what to do first. The limit is
32 KiB for Codex.

When you tell an agent to command another node, write the steps as commands it
can follow. This task worked for an agent on one machine that was asked to use
another:

```text
Task from the owner. Node L is <node-id>. Do this:
1. Run: sidera directory --node <node-id> and take the address of the endpoint of kind runtime (it accepts session.create): that is Node L's Codex runtime.
2. Create a Codex session there by running: sidera control <that runtime address> session.create --preset light --body 'Compute 37*41+11 and reply to me with only the number, using sidera reply.'
3. Wait for its reply with the recv tool, with the integer argument wait_s=60 (the argument is named wait_s, not wait), passing ack_lease=<lease of the previous page> after the first call; on the command line this is sidera recv --wait 60 --ack <lease>. An empty page returns after about a minute, not at once; if it returns at once something is wrong with the arguments. Wait until the answer arrives.
4. Reply to me (the owner message you were woken for) with sidera reply, stating the number from Codex.
```

## What a preset allows

The agent does whatever its preset lets it do. By default every trusted node can
start a session with any preset on a node, so the strength of a preset is the
strength of every trusted node's reach. Start with the `light` preset (channel and
light work), see [Getting started](getting-started.md#5-add-a-preset-both), and add
the full-access `dev` preset only on a machine and in a cluster that belong entirely
to you: any trusted node can then direct its agent to run anything there, and the
session's Sidera token is visible to everything the agent runs.

Under a Codex `workspace-write` sandbox the agent reaches Sidera through the MCP
tools only; the `sidera` command cannot reach the daemon and fails with `cannot
reach the Sidera daemon at <socket path>: permission denied (a sandbox may be
blocking it)`. Write instructions that name the MCP
tools (`recv`, `reply`, `ack`, `send`, `control`, `status`) as well as the
commands, as the text above does.

What a session can read of the node's data directory (the identity key and the token
of every session) also depends on the preset:

- A Codex `workspace-write` or `read-only` session cannot read it on macOS and Linux:
  the operating system enforces a denial, except for the user's own MCP servers and a
  command the owner approved to run outside the sandbox. On Windows the directory,
  `%LOCALAPPDATA%\Sidera`, has an access list for its owner only, which is the main
  guard, which keeps the sandbox out; the deny entry Codex adds for the sandbox's accounts
  when a sandboxed Sidera session runs a command can be revoked by another Codex session.
  Codex's elevated sandbox on Windows grants its group read access on most first-level
  folders of the profile, which does not reach Sidera in that location; if a foreign
  entry ever appears, the daemon refuses to start and names it.
- A Claude Code `light` session's file tools cannot read it. If the owner's own Claude
  settings allow a `Bash` file command such as `grep` or `find`, the session can still
  read it. A guarantee needs Claude's operating-system sandbox, which Sidera does not
  use yet.
- A full-access session, of either runtime, acts as the local owner and can read
  everything.

See [What a session can read of the node's data](getting-started.md#what-a-session-can-read-of-the-nodes-data).

The node's management interface trusts the operating-system user, not the session.
Under a full-access preset an agent session can run every owner command (`preset`,
`adapter`, `trust`, `identity`, `daemon stop` and the rest) and acts as the local
owner. A Claude Code `light` session cannot, and neither can a Codex
`workspace-write` session on Linux and macOS (on Windows this was not measured). The
details are in
[Presets and access](getting-started.md#the-management-interface-trusts-the-user-not-the-session).
