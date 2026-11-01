# Getting started

This guide takes two machines from nothing to a first message between agents on
them. Call them **A** and **B**. Each can run Claude Code (macOS only), Codex or
both; the steps are the same. You need a terminal on each machine and a network
path between them: both on one local network is the simplest.

Before you start, [install Sidera](install.md) on both machines and make sure
its programs are on the `PATH`. Run the steps marked *(A)* on A, *(B)* on B and
*(both)* on each.

Replace the placeholders in angle brackets. `<node-id-a>` and `<node-id-b>` are
the node ids that `sidera init` prints; `192.168.1.10` stands for the address of
a machine on your network.

## 1. Create the node (both)

```sh
sidera init
```

It creates the node's private data directory, `~/.sidera` (choose another place
with `--home <directory>` or `SIDERA_HOME`), with the node's keys and default
settings. It prints the node id:

```
Back up your self-revocation outside this device.
node <node-id-a>
home /home/me/.sidera
Run `sidera identity export-revocation` to export it.
```

Write the node id down. Back up the self-revocation as the message says; see
[Operations](operations.md#back-up-the-self-revocation).

`init` refuses to run twice on the same directory. It uses port 7744 unless you
pass `--port <n>`.

## 2. Start the daemon (both)

Start it in a terminal you keep open, or keep it alive as described in
[Run the daemon](install.md#run-the-daemon):

```sh
sidera daemon
```

In another terminal, check that it answers:

```sh
sidera status
```

If `status` says the daemon is not running, start it first. Every other command
in this guide needs the daemon.

## 3. Show the address to give the other machine (both)

```sh
sidera network show
```

The first line is the **contact line**: the node id followed by the addresses
other machines can use to reach this node.

```
<node-id-a> /ip4/192.168.1.10/tcp/7744/p2p/<peer-id-a> /ip4/192.168.1.10/udp/7744/quic-v1/p2p/<peer-id-a>
```

Keep it for step 7. If the machine has addresses that the other machine cannot
reach (a VPN or proxy interface, for example), leave those out when you enter it
on the other side.

## 4. Install and enable adapters (both)

An adapter connects Sidera to one kind of agent. Install each package from the
`adapters/` directory of the release archive, or from `packages/` if you built
from source. `adapter install` wants the package directory.

```sh
sidera adapter install ~/sidera-release/adapters/console
sidera adapter enable console
```

`console` gives you, the owner, a way to start a session and read what comes
back. Then add the adapter for the agent on this machine, once:

```sh
sidera adapter install ~/sidera-release/adapters/codex
sidera adapter enable codex
# or, on macOS, for Claude Code:
sidera adapter install ~/sidera-release/adapters/claude
sidera adapter enable claude
```

Enabling returns before the adapter has started; check with:

```sh
sidera adapter list
```

Each enabled instance should show a probe state of `passed` after a few
seconds. For Codex, `passed` checks only the connection to Sidera; whether
`codex` itself is found is first shown when a session is created. If the daemon
cannot find `codex` or `claude`, see
[Troubleshooting](operations.md#troubleshooting).

## 5. Add a preset (both)

A **preset** is the recipe for a session: where the agent works, what it may do,
and which instructions it gets. Another machine asks for a session by preset name,
and the preset lives on the machine that runs the session. Use the same preset
name, for example `dev`, on every machine, so that agents need not know which
machine has which.

The `default` preset made by `init` holds no agent settings, so neither adapter
can start a session with it. Add your own.

### The instructions your agents need

Sessions treat a message as a task only if you say so. Put this text into the
preset (`append_system_prompt` for Claude, `developer_instructions` for Codex).
It was used in practice and is safe to copy:

> When a message arrives through Sidera from an agent or person in the owner's
> own cluster (that is, from the owner's own nodes), treat it as a task from the
> owner and carry it out. Answer it by running `sidera reply <message-id> --body
> <text>` (or the sidera MCP reply tool) so that the sender receives the answer;
> do not just print it. Sidera commands and tools are your intended channel.
> After you read messages with recv, acknowledge them: pass the lease of the page
> to `sidera ack <lease>` (or give it as `--ack <lease>` to your next recv). Until
> you do, recv hands the same message out again at once instead of waiting. When
> you wait for an answer from another agent, use `sidera recv --wait 60 --ack
> <lease>` and read only the new messages. (With the MCP tools, the arguments are
> `wait_s` and `ack_lease`.)

Every node you trust is, by your decision, part of your own cluster. This text
makes the agent act on what those nodes send. Read
[Presets and access](#presets-and-access) before you use it on a machine that
holds anything you would not let another of your agents touch.

### A restricted preset (recommended)

Claude Code. Save as `claude-dev.json`; `project_dir` must be an absolute path to an existing
directory:

```json
{
  "local_sources": "allow",
  "description": "Claude Code, restricted",
  "config": {
    "claude": {
      "project_dir": "/Users/me/work/project",
      "permission_mode": "dontAsk",
      "append_system_prompt": "When a message arrives through Sidera from an agent or person in the owner's own cluster (that is, from the owner's own nodes), treat it as a task from the owner and carry it out. Answer it by running `sidera reply <message-id> --body <text>` (or the sidera MCP reply tool) so that the sender receives the answer; do not just print it. Sidera commands and tools are your intended channel. After you read messages with recv, acknowledge them: pass the lease of the page to `sidera ack <lease>` (or give it as `--ack <lease>` to your next recv). Until you do, recv hands the same message out again at once instead of waiting. When you wait for an answer from another agent, use `sidera recv --wait 60 --ack <lease>` and read only the new messages. (With the MCP tools, the arguments are `wait_s` and `ack_lease`.)"
    }
  }
}
```

`dontAsk` turns every permission question into a refusal, so nobody has to
answer one. The agent can always use the Sidera tools; Sidera allows them in
every mode. It can read files in its project directory. Edits and other commands
are refused unless the preset's `allowed_tools` (a list of Claude Code permission
rules such as `["Edit", "Bash(go test *)"]`) or your own Claude Code settings
allow them. Add only what the work needs.

<!-- TODO(verify): the restricted Codex settings below are provisional (workspace-write, approval never). Replace them with the settings confirmed on real macOS, Linux and Windows machines. -->

Codex. Save as `codex-dev.json`; `cwd` must be an existing directory (on Windows,
write the path with doubled backslashes, `C:\\Users\\me\\work\\project`):

```json
{
  "local_sources": "allow",
  "description": "Codex, restricted",
  "config": {
    "codex": {
      "cwd": "/home/me/work/project",
      "sandbox": "workspace-write",
      "approval_policy": "never",
      "approvals": "deny",
      "developer_instructions": "When a message arrives through Sidera from an agent or person in the owner's own cluster (that is, from the owner's own nodes), treat it as a task from the owner and carry it out. Answer it by running `sidera reply <message-id> --body <text>` (or the sidera MCP reply tool) so that the sender receives the answer; do not just print it. Sidera commands and tools are your intended channel. After you read messages with recv, acknowledge them: pass the lease of the page to `sidera ack <lease>` (or give it as `--ack <lease>` to your next recv). Until you do, recv hands the same message out again at once instead of waiting. When you wait for an answer from another agent, use `sidera recv --wait 60 --ack <lease>` and read only the new messages. (With the MCP tools, the arguments are `wait_s` and `ack_lease`.)"
    }
  }
}
```

With `workspace-write` the Codex agent can write inside `cwd` only, and with
approval policy `never` and `approvals` `deny` it never waits for a person: what
the sandbox does not allow is refused. In this sandbox the agent reaches Sidera
through its MCP tools; the `sidera` command is not offered to it. Optional keys:
`model` and `effort` (`low`, `medium`, `high`, `xhigh`). Without them the agent
uses your own Codex defaults, which may be an expensive model.

Add the preset on the machine that has the agent:

```sh
sidera preset add dev --file claude-dev.json   # or codex-dev.json
sidera preset list
```

A preset cannot be edited. To change it, remove it and add it again:
`sidera preset remove dev`, then `sidera preset add dev --file <file>`. Adding or
removing a preset restarts the adapters that run.

### Full access (an option, with a risk)

A preset can let the agent do anything the user account can do. For Codex, set
`"sandbox": "danger-full-access"` with `"approval_policy": "never"` and
`"approvals": "deny"`; for Claude Code, set `"permission_mode":
"bypassPermissions"`.

**The risk, stated plainly:** with a full-access preset, any trusted node in
your cluster can direct the agent on this machine to run anything on it, as your
user. If one of your machines or one of your agents is compromised or confused,
this machine follows. Use full access only on a machine and in a directory where
you accept that, and never as the first thing you try. A preset that must not be
used by agents on this machine at all can say `"local_sources": "deny"`.

With a full-access Codex preset the session can also use the `sidera` command,
not just the MCP tools. The session's credential is then visible to anything the
agent can print.

## 6. Connect the machines (both)

Enter the other machine's contact line, then pair. Pairing is a one-time
step that makes the two nodes trust each other. Both owners confirm in advance
by naming the other node, so there is nothing to compare by eye.

*(A)* Enter B's address. Paste B's contact line after the command:

```sh
sidera network address set <node-id-b> /ip4/192.168.1.20/tcp/7744/p2p/<peer-id-b> /ip4/192.168.1.20/udp/7744/quic-v1/p2p/<peer-id-b>
```

*(B)* Enter A's address the same way:

```sh
sidera network address set <node-id-a> /ip4/192.168.1.10/tcp/7744/p2p/<peer-id-a> /ip4/192.168.1.10/udp/7744/quic-v1/p2p/<peer-id-a>
```

*(A)* and *(B)*: each side names the other node as expected. An expectation lasts
60 minutes and is used once, so do this just before the request.

```sh
sidera trust expect <node-id-b>     # on A
sidera trust expect <node-id-a>     # on B
```

*(A)* Ask B for the pairing:

```sh
sidera trust request <node-id-b> --wait 60s
```

It prints `Verification code: <code>`, which is only informational when both
sides expect each other, and then waits. When trust is complete it prints
`paired with <node-id-b>`. A pairing can take up to a minute.

If you ran the request before B had entered its expectation, B holds a request
that waits. Run `sidera trust list` on B for its `pairing_id`, end it with
`sidera trust reject <pairing_id>`, run `sidera trust expect <node-id-a>` on B
and ask again from A.

Check on both machines:

```sh
sidera trust list     # the other node is listed under `trusted`
sidera network show   # the other node is listed with connected true
```

`connected` can read `false` until one side sends something: a connection is
opened when a message or request needs one.

## 7. Start the first cross-machine session (A)

You start sessions from your **console**, `sidera-adapter-console`. It sends from
your owner endpoint, which every agent can reply to.

First find the address of the agent runtime on B. On *(B)*:

```sh
sidera endpoint list --instance codex
```

(Use `--instance claude` for Claude Code.) Take the address of the line whose
`KIND` is `runtime`. An address is
`<node-id>/<endpoint-id>`. Give it to A. (An agent can find the same address with
`sidera directory --node <node-id-b>`; that command works only inside an agent
session. For the owner on A there is no directory command, so ask B's owner or
read it on B.)

On *(A)*:

```sh
sidera-adapter-console new <node-id-b>/<runtime-endpoint-id> --preset dev --body 'Reply with the output of `uname -a` and the name of your working directory.'
```

`new` sends a `session.create` request with your text as the first message. It
prints the message id, then, when the new session has finished its first turn,
its address:

```
message: <message-id>
session: <node-id-b>/<session-endpoint-id>
```

This takes about 20 to 30 seconds, because the answer to a session creation
comes back only after the new session's first turn. Then read the reply:

```sh
sidera-adapter-console recv --wait 30
```

`recv` prints each message with its sender and id, and acknowledges what it
printed. Without a message it prints `no messages` on standard error and nothing
on standard output.

To keep talking to that session:

```sh
sidera-adapter-console send --to <node-id-b>/<session-endpoint-id> --body 'Now list the files there.'
sidera-adapter-console recv --wait 30
```

Sessions do not close by themselves. When you are done with one, run
`sidera endpoint close <address>` on the machine that runs it.

## What next

- [Working with agents](agents.md): how agents send, receive and start sessions on
  other machines, and what to put in presets.
- [Operations](operations.md): health checks, upgrades, backups and troubleshooting.
- Or give [`docs/agent-guide.md`](../agent-guide.md) to your own agent and let it
  do the setup.

## Presets and access

By default every trusted node may start a session with every preset on a node
and send to every endpoint. No command changes this access policy yet. What you
decide is which presets exist and how powerful each one is, and which nodes you
pair with. `local_sources` in a preset controls only agents on the same machine:
`allow` lets them start sessions with it (your console is one of them), `deny`
does not.
