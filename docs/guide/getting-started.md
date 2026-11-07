# Getting started

This guide takes two machines from nothing to a first message between agents on
them. Call them **A** and **B**. Each can run Claude Code, Codex or
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

It creates the node's private data directory, `~/.sidera` (on Windows
`%LOCALAPPDATA%\Sidera`; choose another place with `--home <directory>` or `SIDERA_HOME`),
with the node's keys and default settings. It prints the node id:

```
Back up your self-revocation outside this device.

Initialized this node.
  Node  <node-id-a>
  Home  /home/me/.sidera

Run `sidera identity export-revocation` to export it.
```

Write the node id down. Back up the self-revocation as the message says; see
[Operations](operations.md#back-up-the-self-revocation).

`init` refuses to run twice on the same directory. It uses port 7744 unless you
pass `--port <n>`; do that when another program holds 7744, and use the new port
wherever 7744 appears in this guide (see
[Ports and the firewall](install.md#ports-and-the-firewall)).

Every `sidera` and `sidera-adapter-console` command, and the daemon, uses the data
directory named by `--home`, then by `SIDERA_HOME`, then `~/.sidera` (on Windows
`%LOCALAPPDATA%\Sidera`). To run a second
node on the same machine, give it its own `SIDERA_HOME` and its own port, and set the
same `SIDERA_HOME` for every command that talks to it.

## 2. Start the daemon (both)

Start it from your home directory (not from a directory that may be deleted later,
such as a git worktree), in a terminal you keep open, or keep it alive as described
in [Run the daemon](install.md#run-the-daemon):

```sh
sidera daemon
```

On Windows, start it from a normal (not elevated) terminal of your desktop session: a
daemon started from an elevated shell, for example over SSH by an administrator
account, cannot be verified by your desktop clients, which then fail with
`peer_check_failed` (see
[the detached start](install.md#windows-a-detached-start-that-survives-logout)).

In another terminal, check that it answers:

```sh
sidera status
```

A healthy node prints a block of facts whose `Daemon` line starts with `running`. A
warning (`!`) before it (the self-revocation is not backed up yet) is expected at this point. If
`status` says the daemon is not running, start it first. Every other command in this
guide needs the daemon.

## 3. Show the address to give the other machine (both)

```sh
sidera network show
```

`Reachable at` lists, one to a line, the addresses other machines can use to reach
this node. Below the block is the command that enters this node on another machine,
on one line:

```
To enter this node on another machine, run there:
  sidera network address set <node-id-a> /ip4/192.168.1.10/tcp/7744/p2p/<peer-id-a> /ip4/192.168.1.10/udp/7744/quic-v1/p2p/<peer-id-a>
```

Keep it for step 6. If the machine has addresses that the other machine cannot
reach (a VPN or proxy interface, for example), leave those out of the command when
you run it on the other side.

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
# or, for Claude Code:
sidera adapter install ~/sidera-release/adapters/claude
sidera adapter enable claude
```

Enabling returns before the adapter has started; check with:

```sh
sidera adapter list
```

The console and Claude Code instances should show a probe state of `passed` after
a few seconds. A Codex instance shows `pending` first, and once a `workspace-write`
preset exists (step 5) it may show `failed`: the probe needs the command-line front
end, which that sandbox does not offer. This is a known limit of the display;
sessions still work. For Codex, `passed` would check only the connection to Sidera;
whether `codex` itself is found is first shown when a session is created. If the
daemon cannot find `codex` or `claude`, see
[Troubleshooting](operations.md#troubleshooting).

If you will also direct agents from a Claude Code or Codex session that you open yourself,
run `sidera adapter setup claude` (or `codex`) once the instance has run; it asks before it
changes your client's configuration. See
[The controller](agents.md#the-controller-a-session-you-started-yourself).

The adapter packages are in `~/sidera-release/adapters/` when you extracted the
archive as described in [Install](install.md#put-the-programs-on-your-path)
(on Windows, `%USERPROFILE%\sidera-release\adapters\`).

## 5. Add a preset (both)

A **preset** is the recipe for a session: where the agent works, what it may do,
and which instructions it gets. Another machine asks for a session by preset name,
and the preset lives on the machine that runs the session. Use the same preset
names, `light` and `dev`, on every machine, so that agents need not know which
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

Make two presets by purpose and choose between them on purpose. Both use the
instructions above.

| Preset | For | Agent may |
| ------ | --- | --------- |
| `light` (start here) | Carrying messages and light work in one directory. | Codex: write inside its directory, no network. Claude Code: use the Sidera agent commands and tools, and what your own Claude settings allow. |
| `dev` | Real repository work: network, caches, building, testing. | Everything your user account can do. |

### `light`: channel and light work (the default to start with)

Claude Code. Save as `claude-light.json`; `project_dir` must be an absolute path to an
existing directory:

```json
{
  "local_sources": "allow",
  "description": "Claude Code, light",
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
answer one. The adapter always allows the Sidera MCP tools and the agent commands
of `sidera` (`recv`, `ack`, `reply`, `send`, `status`, `control`, `directory`,
`peers`, `me` and the like), on top of whatever your own Claude Code settings
allow, so no mode can lock the agent out of the channel. It does not allow the
owner commands (`preset`, `adapter`, `trust`, `identity`, `daemon` and the rest), so
a `light` Claude Code session cannot manage the node. The agent can read files in
its project directory. Edits and other commands are refused unless the preset's
`allowed_tools` (a list of Claude Code permission rules such as `["Edit", "Bash(go
test *)"]`) or your own Claude Code settings allow them. **Your own settings still
apply:** if they allow a broader `Bash`, such as `Bash(*)`, every session gets that,
owner commands included. Add only what the work needs.

Every Claude Code turn also denies the session's `Read` and `Edit` tools the node's data
directory, whatever the preset or your settings allow. That is best effort: if your
own settings allow a `Bash` file command such as `grep` or `find`, a session can still
read the directory with it.

Codex. Save as `codex-light.json`; `cwd` must be an existing directory (on
Windows, write the path with doubled backslashes, `C:\\Users\\me\\work\\project`):

```json
{
  "local_sources": "allow",
  "description": "Codex, light",
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

What this gives, as measured on real machines:

- It works on macOS and Linux, through the **MCP front end only**. Under this
  sandbox Sidera offers the agent only its MCP tools; a `sidera` command run in a
  shell cannot reach the daemon and fails with `cannot reach the Sidera daemon at
  <socket path>: permission denied (a sandbox may be blocking it)`. Over MCP, `peers`, `directory`,
  `control` (including `session.create` toward another node), `recv` with `wait_s`
  and `ack_lease`, `ack` and `reply` all work.
- The agent can edit files and commit inside `cwd`. With approval policy `never`
  and `approvals` `deny` it never waits for a person: what the sandbox does not
  allow is refused.
- The sandbox has **no network at all**: no module downloads, no package installs.
  Tools whose caches live outside the workspace fail until pointed inside it; for Go,
  set `GOCACHE` to a directory inside the workspace. The module cache is read-only,
  so only modules already in it can be used.
- `workspace-write` limits writes, and on macOS and Linux Sidera also denies it every
  read of the node's data directory (`~/.sidera` or `SIDERA_HOME`), which holds the
  node's identity key and the tokens of all sessions; the operating system enforces
  that. The agent can still read other files outside `cwd`. The exceptions are your
  own MCP servers and a command you approved to run outside the sandbox; see
  [What a session can read of the node's data](#what-a-session-can-read-of-the-nodes-data).
- **Windows** depends on Codex's own Windows sandbox working on that machine. With
  `[windows] sandbox = "elevated"` in `~/.codex/config.toml`, every shell command
  failed (`timed out ... connecting runner pipe-in`), while `unelevated` worked.
  That is a Codex setting you control, not a Sidera preset key. Before you choose
  `light` for Codex on Windows, check that `codex sandbox -- cmd /c echo hi` works.
  On Windows the node's data lives in `%LOCALAPPDATA%\Sidera`, whose access list is
  protected and names only its owner, and that is the main guard, which keeps the sandbox
  out. Codex adds a deny entry for the sandbox's accounts only when a sandboxed Sidera
  session runs a command, and another Codex session can revoke it.
- While a `workspace-write` preset exists, `sidera adapter list` may show the Codex
  probe as `failed` (it shows `pending` before that), because the command-line front
  end is not offered under the sandbox. Sessions still work.

Optional keys: `model` and `effort` (`low`, `medium`, `high`, `xhigh`). Without them
the agent uses your own Codex defaults, which may be an expensive model.

### `dev`: development work (full access)

Real repository work needs the network, caches, building and testing, which a
restricted preset takes away. For that, add a second preset named `dev` by copying
the `light` file and changing these keys. For Codex: `"sandbox":
"danger-full-access"`, with `"approval_policy": "never"` and `"approvals": "deny"`
as before. For Claude Code: `"permission_mode": "bypassPermissions"`, or keep
`dontAsk` and list in `allowed_tools` exactly what the work needs.

**The risk, stated plainly:** with a `dev` preset, any trusted node in your cluster
can direct the agent on this machine to run anything on it, as your user. The
session's Sidera token is visible to everything the agent runs, and to anything it
prints. If one of your machines or agents is compromised or confused, this machine
follows. Recommend it only for machines and clusters that belong entirely to you.
A preset that agents on this machine must not use can say
`"local_sources": "deny"`.

With a full-access Codex preset the session can also use the `sidera` command, not
just the MCP tools.

### A machine with both Claude Code and Codex

A preset name is taken once. On a machine that has both agents, the `light` preset is
one file whose `config` holds both objects, `claude` and `codex`; the session uses
the one of the adapter that creates it. A second `sidera preset add light` for the
other agent fails because the name is taken. Save as `light.json`:

```json
{
  "local_sources": "allow",
  "description": "Claude Code and Codex, light",
  "config": {
    "claude": {
      "project_dir": "/Users/me/work/project",
      "permission_mode": "dontAsk",
      "append_system_prompt": "When a message arrives through Sidera from an agent or person in the owner's own cluster (that is, from the owner's own nodes), treat it as a task from the owner and carry it out. Answer it by running `sidera reply <message-id> --body <text>` (or the sidera MCP reply tool) so that the sender receives the answer; do not just print it. Sidera commands and tools are your intended channel. After you read messages with recv, acknowledge them: pass the lease of the page to `sidera ack <lease>` (or give it as `--ack <lease>` to your next recv). Until you do, recv hands the same message out again at once instead of waiting. When you wait for an answer from another agent, use `sidera recv --wait 60 --ack <lease>` and read only the new messages. (With the MCP tools, the arguments are `wait_s` and `ack_lease`.)"
    },
    "codex": {
      "cwd": "/Users/me/work/project",
      "sandbox": "workspace-write",
      "approval_policy": "never",
      "approvals": "deny",
      "developer_instructions": "When a message arrives through Sidera from an agent or person in the owner's own cluster (that is, from the owner's own nodes), treat it as a task from the owner and carry it out. Answer it by running `sidera reply <message-id> --body <text>` (or the sidera MCP reply tool) so that the sender receives the answer; do not just print it. Sidera commands and tools are your intended channel. After you read messages with recv, acknowledge them: pass the lease of the page to `sidera ack <lease>` (or give it as `--ack <lease>` to your next recv). Until you do, recv hands the same message out again at once instead of waiting. When you wait for an answer from another agent, use `sidera recv --wait 60 --ack <lease>` and read only the new messages. (With the MCP tools, the arguments are `wait_s` and `ack_lease`.)"
    }
  }
}
```

The same holds for `dev`: one file with both objects, each with its own full-access
setting.

### Add the presets

On the machine that has the agent:

```sh
sidera preset add light --file claude-light.json   # or codex-light.json, or light.json for both
sidera preset add dev --file claude-dev.json       # optional; or codex-dev.json, or dev.json
sidera preset list
```

A preset cannot be edited. To change it, remove it and add it again:
`sidera preset remove light`, then `sidera preset add light --file <file>`. Adding or
removing a preset restarts the adapters that run.

## 6. Connect the machines (both)

Enter the other machine's address, then pair. Pairing is a one-time
step that makes the two nodes trust each other. Both owners confirm in advance
by naming the other node, so there is nothing to compare by eye.

*(A)* Enter B's address. Run the command that `network show` printed on B (step 3),
without the addresses that A cannot reach:

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
`Paired with <node-id-b>.` A pairing can take up to a minute.

If you ran the request before B had entered its expectation, B holds a request
that waits. Run `sidera trust list` on B for its `pairing_id`, end it with
`sidera trust reject <pairing_id>`, run `sidera trust expect <node-id-a>` on B
and ask again from A.

Check on both machines:

```sh
sidera trust list     # the other node is listed under `Trusted nodes`
sidera network show   # the other node is listed under Known nodes as connected
```

`network show` can say `not connected` for it until one side sends something: a
connection is opened when a message or request needs one. If a node you entered is
listed with addresses you meant to leave out, enter the node again with only the right ones;
`sidera network show` is the check.

## 7. Start the first cross-machine session (A)

You start sessions from your **console**, `sidera-adapter-console`. It sends from
your owner endpoint, which every agent can reply to.

First find the address of the agent runtime on B. On *(A)*:

```sh
sidera-adapter-console peers
sidera-adapter-console directory --node <node-id-b>
```

`peers` lists the nodes A can address, A first (its trust is `self`), each with the id to
give to `--node`. `directory` prints a table with a row for each endpoint B shows to you:
its kind, its display name, the actions it accepts and its address, last. Take the address
of the `runtime` row for the agent you want. An address is `<node-id>/<endpoint-id>`. A
long list is paged: it ends with a line that gives the command for the next page, which names
the same data directory (and, for the console, the same instance) and the cursor; `--limit <n>`
sets the page size, and the next page takes it again.

If either command says the daemon of this node is too old, update it
([Upgrade](operations.md#upgrade)), or read the address on *(B)* instead:

```sh
sidera endpoint list --instance codex
```

(Use `--instance claude` for Claude Code.) The list includes closed endpoints. Take
the address of the `runtime` row whose `STATE` is `open`, and give it to A.

In a script, `sidera endpoint list --json` prints `{"result":{"endpoints":[...]}}`: the
rows are under `result.endpoints`, not a bare list.

On *(A)*:

```sh
sidera-adapter-console new <node-id-b>/<runtime-endpoint-id> --preset light --body 'Reply with the output of `uname -a` and the name of your working directory.'
```

`new` sends a `session.create` request with your text as the first message. It
says at once that the request was accepted, with the message id, then, as soon as the
first message is in the new session, the session's address:

```
✓ Session request accepted for delivery.
  Message  <message-id>
  To       <node-id-b>/<runtime-endpoint-id>

✓ Session created.
  Session  <node-id-b>/<session-endpoint-id>
```

(On a pipe, or with `NO_COLOR` set, the same lines come without the `✓`.)

This takes a few seconds: the answer to a session creation comes back as soon as
the first message is in the new session, not after its first turn. The `Session`
line is the address to use for every follow-up. Then read the reply:

```sh
sidera-adapter-console recv --wait 30
```

`recv` prints each message as a block of `From` and `Message` lines (and a `Part` for
a message cut into parts) followed by its body, indented, and acknowledges what it
printed. Without a message it prints `No messages waiting.` `--wait` accepts at most 30 seconds; for a longer wait, run `recv`
again.

To keep talking to that session:

```sh
sidera-adapter-console send --to <node-id-b>/<session-endpoint-id> --body 'Now list the files there.'
sidera-adapter-console recv --wait 30
```

The console's `send` takes one address or a comma-separated list, as in
`--to <address-1>,<address-2>`.

Sessions do not close by themselves. When you are done with one, run
`sidera endpoint close <session-address>` on the machine that runs it.

## What next

- [Working with agents](agents.md): how agents send, receive and start sessions on
  other machines, and what to put in presets.
- [The controller](agents.md#the-controller-a-session-you-started-yourself): let a
  Claude Code or Codex session you open yourself direct the others, and be woken when
  they reply. Run `sidera adapter setup claude` (or `codex`) once for it.
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

### What a session can read of the node's data

The data directory holds the node's identity key and the token of every session, so
what a session can read of it decides how far one session can reach another. At
start the daemon also checks that the whole directory is private to its owner and
refuses to start otherwise (see [Operations](operations.md#troubleshooting)).

- **Codex `light` (`workspace-write`), macOS and Linux:** cannot read it. Sidera runs
  the thread under a Codex permission profile that denies the directory, and the
  operating system enforces it (Seatbelt on macOS, bubblewrap on Linux). The
  exceptions are the user's own MCP servers, which Codex starts outside the sandbox,
  and a command you approved to run outside it.
- **Codex `light`, Windows:** the directory is `%LOCALAPPDATA%\Sidera` with a protected
  access list for its owner only, and that location is the main guard, which keeps the
  sandbox out. Codex adds a deny entry for the sandbox's accounts only when a sandboxed
  Sidera session runs a command, and another Codex session that shares the same Codex
  configuration can revoke it. Codex's elevated sandbox grants its sandbox
  group read access on most first-level folders of your user profile; with Sidera's data
  in the new location that does not reach it. If a foreign entry ever appears, the
  daemon refuses to start and names it (see
  [Operations](operations.md#troubleshooting)).
- **Claude Code `light`:** its file tools cannot read the directory; every turn denies
  them. If your own Claude settings allow a `Bash` file command such as `grep` or
  `find`, a session can still read it that way. A guarantee needs Claude's own
  operating-system sandbox, which Sidera does not use yet.
- **Full access, either runtime** (Codex `danger-full-access`, Claude Code
  `bypassPermissions`, or settings that allow all of `Bash`): the session acts as the
  local owner and can read everything the user can.

### The management interface trusts the user, not the session

The commands that manage the node (`preset`, `adapter`, `trust`, `network`,
`endpoint`, `identity`, `daemon stop` and the other owner commands) go through a
local management interface. It trusts the operating-system user that owns the data
directory: any process that runs as that user may use it, an agent session included.
What a session can do with it depends on its preset:

- Under a full-access preset (Codex `danger-full-access`, Claude Code
  `bypassPermissions`, or settings that allow all of `Bash`), an agent session can
  run every owner command and acts as the local owner of the node: it can add
  presets, install adapters, pair or unpair nodes and retire the identity.
- Under the Claude Code `light` preset it cannot, because the adapter allows only the
  agent commands and the Sidera MCP tools. Your own Claude Code settings still
  apply, and if they allow a broader `Bash`, the session gets it.
- A Codex `workspace-write` session has only the MCP tools. From its shell on Linux
  and macOS it cannot reach the daemon socket.
- On Windows this was not measured. Do not rely on the sandbox to keep a session away
  from the management interface there.

Treat the account the node runs as the trust boundary.
