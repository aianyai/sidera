# Working with agents

This page explains how an agent uses Sidera to exchange messages with other
agents, and how to configure presets so that agents do it well. It is for the
person who sets agents up. The same rules, written as instructions for an agent,
are in [`docs/agent-guide.md`](../agent-guide.md), which you can give to an agent
as it is.

An agent uses Sidera in one of two ways:

- **Inside a Sidera session.** Sidera started the session from a preset. The
  session has a credential (`SIDERA_TOKEN`) and talks to its node through the
  `sidera` MCP tools, or through the `sidera` command where its sandbox allows
  it. Everything below applies.
- **As the owner's console.** Anything you or another program starts from a
  terminal uses `sidera-adapter-console`, which sends from the owner endpoint on
  the node. It has `new`, `send`, `reply`, `recv`, `status` and `whoami`; see
  [Getting started](getting-started.md#7-start-the-first-cross-machine-session-a).

An agent that was not started by Sidera has no session credential, so
`sidera recv`, `sidera send` and the other agent commands fail with
`unauthenticated/token_missing`. It can still use the console client.

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
limits of each front end, including `recv_wait_max_s`. The console's `whoami` is
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

1. **Always acknowledge a page.** `recv` never acknowledges what it hands out.
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
3. **Wait in a loop.** Each runtime caps the wait (see `recv_wait_max_s` in `me`;
   at most about 100 seconds through MCP and 8 seconds on the Codex command
   line), so one `recv` may return early. Call it again, passing the lease of the
   previous page, until the answer you wait for arrives.

A long message is cut into parts that fit the page budget. A page that ends a
part carries `"next":{"msg":"<id>","part":<k>}`; fetch it with
`sidera recv --msg <id> --part <k>` (`msg` and `part` in MCP). Acknowledge each
page. A message that is handed out again after an interrupted read is marked
`possibly_duplicate`.

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
shows each recipient's delivery state. If a message cannot be delivered, the
sender is told.

## Starting a session on another machine

To ask an agent on another machine to do work, start a session there with
`control ... session.create`. The target is a **runtime endpoint** (the endpoint
that starts sessions), not a console and not an existing session.

1. Find it. `sidera directory --node <node-id>` lists the node's endpoints. Take
   the address of the one whose `kind` is `runtime` and whose `accepts` list has
   `session.create`. Its `display_name` names the adapter, such as `codex`.
2. Create the session with a preset that exists on that node, and put the task in
   the first message:

   ```sh
   sidera control <runtime-address> session.create --preset light --body-file task.txt
   ```

   `session.create` delivers the first message into the new session at once.
   There is no need to `send` it again.
3. Wait for the answer with `recv`, as above. **Do not wait for the control
   reply.** The reply to a session creation comes back only after the new
   session's first turn has ended, so it takes about as long as the whole task.
   `sidera control` itself returns as soon as the request is accepted. If you
   want the session's address, read it later with `sidera status <message-id>`.

The new session answers to the sender of its first message, with an ordinary
reply. For that to work, its preset must tell it to reply with `sidera reply`
(see below).

Creating a session takes about 7 seconds for Claude Code and 20 to 30 seconds
until the first turn ends for a short task. Sessions do not close by themselves.
`sidera control <session-address> session.close` closes a session you created;
the owner can close any with `sidera endpoint close <address>`.

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
tools only; the `sidera` command is not offered and fails with an unhelpful
`internal error`. Write instructions that name the MCP
tools (`recv`, `reply`, `ack`, `send`, `control`, `status`) as well as the
commands, as the text above does.
