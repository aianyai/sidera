# Operations

How to check a node, upgrade it, change its adapters, keep its identity safe and
fix what commonly goes wrong. Commands run on the machine whose node you manage,
as the user that owns `~/.sidera`.

## Health checks

Run these on each node:

```sh
sidera adapter list      # the probe state of each enabled instance (see the notes)
sidera trust list        # the nodes you paired with are under `trusted`
sidera network show      # the first line is the contact line; peers show connected
sidera alerts list       # normally empty
sidera status            # daemon state, queues, delivery counts
sidera endpoint list     # console, runtime and session endpoints of this node
```

Notes:

- A probe state of `passed` proves only that a front end works. A Codex instance
  shows `pending` first, and while a Codex `workspace-write` preset exists the probe
  may show `failed`, because no command-line front end is offered under that
  sandbox; this is a known limit of the display, and sessions still work. Whether
  `codex` is found shows when a session is created.
- `connected` in `network show` is whether a connection is up now. It can be
  `false` for a healthy node until one side sends.
- `sidera peers` and `sidera directory` are agent commands. From your own shell
  they fail with `unauthenticated/token_missing`; that is expected. They work
  inside an agent session.
- `sidera alerts ack <id>` acknowledges an alert once you have dealt with it.

### Logs and state

Everything of a node is under its data directory, `~/.sidera` by default.

| What | Where |
| ---- | ----- |
| Daemon log | `~/.sidera/logs/daemon.log` |
| One log per adapter instance | `~/.sidera/logs/adapter-<instance>.log` |
| Codex App Server state and log | `~/.sidera/adapter-state/codex/` (`app-server.log`) |
| What your own shell redirect caught | the file you gave `>>` when you started the daemon (normally empty) |
| Claude Code transcripts | `~/.claude/projects/<working-directory>/*.jsonl` |
| Codex transcripts | `~/.codex/sessions/` |

When an agent does something unexpected, the transcript shows which tool it
called and with what arguments. On Windows the data directory is
`%USERPROFILE%\.sidera`.

## Upgrade

Do one machine at a time and check it is healthy before the next. The node's
identity, trust and presets stay where they are; you do not run `init` again.
Read the release notes on the release page first: if a release needs a one-time
manual step on existing nodes, they say so. Let the work that agents are doing on
the node finish first: the upgrade ends their sessions, as step 4 explains.

1. Stop the daemon and wait until it has exited:

   ```sh
   sidera daemon stop
   ```

   This also stops the adapters it started. The Codex App Server is started
   detached on purpose and keeps running, so a running turn continues; the daemon
   finds it again when it starts (see
   [Stop the daemon and the Codex App Server](#stop-the-daemon-and-the-codex-app-server)).

2. Replace the programs on your `PATH` with the new release's: `sidera` and
   `sidera-adapter-console`. The adapter programs of Codex and Claude Code are
   replaced by installing their packages in step 4. Extract the new archive into a
   directory of its own, for example `~/sidera-release`
   (`mkdir -p ~/sidera-release && tar -xzf <archive> -C ~/sidera-release`; on
   Windows `Expand-Archive <archive> -DestinationPath $env:USERPROFILE\sidera-release`)
   and copy from there.

   - macOS and Linux: do not overwrite a running program. Copy to a temporary
     name, then rename:

     ```sh
     cp ~/sidera-release/sidera ~/.local/bin/sidera.new && mv ~/.local/bin/sidera.new ~/.local/bin/sidera
     ```

     (Linux reports `Text file busy` if you overwrite a program that runs.)
   - Windows: after the daemon stops, Codex's `sidera mcp` helper processes can
     remain and lock `sidera.exe`. A running program can be renamed, so rename and
     copy:

     ```powershell
     Rename-Item $env:USERPROFILE\sidera\bin\sidera.exe sidera.exe.old
     Copy-Item $env:USERPROFILE\sidera-release\sidera.exe $env:USERPROFILE\sidera\bin\
     ```

     Delete `sidera.exe.old` after the daemon has started again. Do not kill the
     Codex App Server or its helpers.

3. Start the daemon again, the way you started it before, from your home directory
   (not from one that may be deleted, such as a git worktree).

4. Move each adapter instance to the new release. A new release has a new adapter
   version, and `sidera adapter install` refuses another version of an adapter while
   an instance of it is enabled (`conflict/adapter_in_use`). So, for each instance
   in `sidera adapter list` (for example `console`, `codex`, `claude`):

   ```sh
   sidera adapter disable <instance>
   sidera adapter install ~/sidera-release/adapters/<adapter>
   sidera adapter enable <adapter-id> --instance <instance>
   ```

   **Disabling ends that instance's sessions.** Their endpoints in Sidera are
   closed, so no message reaches them or comes from them any more. The sessions of
   the agent itself are kept as history:
   when the instance runs again, the node asks the adapter to release them. A Codex
   thread is archived and unloaded, and its record stays; the running turn of a Claude
   Code session is interrupted and its transcript stays. A Codex session in the
   middle of a turn is released only after the turn ends. Start new sessions
   afterwards: a closed endpoint stays closed. If `install` still answers
   `adapter_in_use` right after `disable`, the old adapter process is still ending:
   wait a few seconds and run it again.

   A package of the same version that differs in contents (a rebuild) is not
   refused: it is replaced without a disable, the result says `replaced true`, and
   the running instances restart and keep their sessions. A package that is the same
   says `already_current`.

5. Check: `sidera adapter list` (probes `passed`, except Codex as the notes above
   say), then reconnect the nodes. A
   restart of the daemon drops its connections to other nodes, and a connection is
   opened only when something needs it. Send one message from each node, for
   example from the console:

   ```sh
   sidera-adapter-console send --to <other-node-id>/<its-console-endpoint> --body reconnect
   ```

   Read the console address of a node with `sidera-adapter-console whoami` on that
   node. The other side then runs `sidera-adapter-console recv`. Afterwards
   `sidera network show` lists the peers with `connected true`.

To upgrade Codex or Claude Code themselves, upgrade them as usual. On Windows,
restart the daemon once so that it finds the new Codex release.

## Stop the daemon and the Codex App Server

`sidera daemon stop` ends the daemon and the adapters it started. It leaves the
Codex App Server running: the Codex adapter starts it detached on purpose, so that a
turn that is running survives a restart of the daemon. The server lives in the
adapter's state directory, `~/.sidera/adapter-state/codex/`. Before you delete a data
directory (or leave a node for good), stop the daemon and end its server too, or it
stays behind with nothing to use it. Ending it ends any turn that runs in it.

- macOS and Linux: find the process whose command line names the socket in that
  directory, and end it. Your own Codex may run an App Server of its own; touch only
  the one whose command line shows this node's directory.

  ```sh
  ps -axo pid,command | grep '[a]pp-server --listen unix://.*adapter-state'
  kill <pid>
  ```

- Windows: the process id is in `%USERPROFILE%\.sidera\adapter-state\codex\app-server.json`.
  `Stop-Process -Id <pid>` ends that process only; helper processes it started can
  remain.

## Change an adapter's configuration

An adapter instance has a small configuration of its own. Codex takes
`codex_program` and `sidera_program` (the program to run, as a path or a name
looked up on the `PATH`); Claude takes `claude_path` and `sidera_path`.

```sh
sidera adapter config codex --config codex-instance.json
```

The file is the **whole** new configuration, not a patch. A key you leave out is
removed, and an empty configuration is `{}`. A running instance restarts to take it;
an instance that is not enabled takes it when you enable it. `-` reads the
configuration from standard input.

`codex_program`, `sidera_program`, `claude_path` and `sidera_path` name programs the
daemon runs with your rights. Point them only at programs you trust.

To remove an instance, disable it, then remove it:

```sh
sidera adapter disable codex
sidera adapter remove codex
```

`remove` is refused while the instance is enabled or has open session endpoints;
the message gives the count. It does not delete the adapter's own directory under
`~/.sidera/adapter-state/<instance>/`; delete that yourself if you want it gone.

## Back up the self-revocation

Every node has a self-revocation: a signed statement that retires the node's
identity. If you lose the machine or its keys, the statement is how you tell the
nodes that trusted it to stop. It must be kept **outside the machine**.

```sh
sidera identity export-revocation --out self-revocation.rec
```

It writes the statement to a new private file (the file must not exist) and prints
a digest. Move the file to storage that is not on this machine, such as a password
manager or an offline disk, then confirm:

```sh
sidera identity confirm-backup <digest>
```

Until you confirm, `sidera status` keeps warning on every call; nothing else stops
working. Treat the file as a secret:
whoever holds it can retire the node. Without `--out`, the command prints the
statement to the terminal; use that if the file system cannot make a private file,
for example a Windows FAT volume or a network share.

## Troubleshooting

**The daemon does not start: the port is in use.** The start fails with an error
naming the address, and the daemon exits. Another process holds port 7744, or an
old daemon is still running. Find the holder (`lsof -i :7744` on macOS and Linux,
`Get-NetTCPConnection -LocalPort 7744` in PowerShell) and stop it. A daemon that
cannot start cannot be given another port by a command. To move a node that runs, use

```sh
sidera network listen set /ip4/0.0.0.0/tcp/7745 /ip4/0.0.0.0/udp/7745/quic-v1
```

then restart the daemon and give the other nodes the new contact line. A new node
that has not paired yet can be created again on a free port with
`sidera init --port <n>`, after you delete its data directory.
`daemon_already_running` means a daemon on the same data directory is already up.

**`socket_path_too_long`.** Sidera's local sockets live under the data directory,
and a Unix socket path has a limit of about 100 bytes. Use a shorter `--home` or
`SIDERA_HOME`.

**Other machines cannot connect.** Messages or `directory` fail with
`unavailable/node_unreachable` or `directory_unavailable`, or `trust request`
times out. Check that the address you entered is the one `network show` prints on
the target, that the daemon runs there, and that the firewall allows TCP and UDP
7744 (see [Ports and the firewall](install.md#ports-and-the-firewall)). If a
machine cannot yet accept inbound connections, pair from that machine
(`sidera trust request`), and after every restart of either daemon let that
machine send first: it dials out, and the other side can then answer over the
same connection.

**A session fails with `native_unavailable`, but `adapter list` says `passed`.**
The daemon cannot find `codex` (or `claude`). It looks the program up on its own
`PATH`, which is not your interactive shell's. Start the daemon with the `PATH` set,
as in [Run the daemon](install.md#macos-and-linux-a-terminal-multiplexer) (on Windows
see [the detached start](install.md#windows-a-detached-start-that-survives-logout)).
As a stopgap, give the adapter the full path:

```sh
echo '{"codex_program": "/full/path/to/codex"}' > codex-instance.json
sidera adapter config codex --config codex-instance.json
```

**A session cannot be created: `preset_unknown` or `preset_invalid`.** The preset
name does not exist on the target node, or its working directory does not exist
there, or it is missing a required key (`project_dir` and `permission_mode` for
Claude, `cwd` for Codex). Check with `sidera preset list` on the target node.

**An agent keeps seeing the same message.** It does not acknowledge. See
[Reading messages](agents.md#reading-messages).

**After a restart, peers show `connected false`.** Nothing is wrong yet: send a
message in either direction, as in step 5 of the upgrade.

**A pairing is stuck.** `sidera trust list` shows requests under `incoming` and
`outgoing`. End one with `sidera trust reject <pairing_id>`, check that both sides
entered `trust expect` for each other less than 60 minutes ago, and ask again. If
you removed a node with `trust remove` and take it back with `trust clear`, the
other node still holds its credential and refuses the new pairing until its owner
also runs `trust remove` and `trust clear` for you.

**Remove a node you no longer trust.** `sidera trust remove <node-id>`. The other
node is not told.

## Known limits

- Nodes connect directly. There is no traversal of routers (NAT) and no relay, so
  two machines must reach each other's port. Addresses are entered by hand.
- Trust is between two nodes that paired. Trust does not extend through a third
  node.
- The daemon does not start at boot and is not installed as a service. Restart it
  yourself after a reboot.
- The Claude Code adapter runs on macOS only.
- A session does not close by itself, and `sidera endpoint list` keeps listing
  closed ones. Close sessions you no longer need with `sidera endpoint close`.
- Neither adapter can interrupt a running turn. Closing a Codex session that is in the
  middle of a turn is not confirmed until the turn ends.
- A `send` from an agent session takes one recipient. The console's `send` takes a
  comma-separated list.
- A Codex session whose sandbox is `read-only` or `workspace-write` uses the MCP
  tools only; a `sidera` command run in a shell fails with `cannot reach the Sidera
  daemon at <socket path>: permission denied (a sandbox may be blocking it)`, and
  the command needs `danger-full-access`. While such a preset exists, `sidera
  adapter list` may show the Codex probe as `pending` and then `failed`; sessions
  still work. `workspace-write` has no network and does not stop the agent from
  reading files outside its directory: the whole data directory, including the
  node's identity key (`keys/identity.key`) and the tokens of the other sessions,
  which it could send over Sidera. On Windows it depends on
  Codex's own sandbox working: with `[windows] sandbox = "elevated"` in
  `~/.codex/config.toml`, every shell command failed, while `unelevated` worked.
  Check that `codex sandbox -- cmd /c echo hi` works first.
- The management interface trusts the operating-system user, not the session. Under
  a full-access preset an agent session can run every owner command and acts as the
  local owner of the node. A Claude Code `light` session cannot, unless your own
  Claude Code settings allow a broader `Bash`. A Codex `workspace-write` session
  cannot reach the daemon socket from its shell on Linux and macOS; on Windows this
  was not measured. See
  [Presets and access](getting-started.md#the-management-interface-trusts-the-user-not-the-session).
- A session's credential is visible to its own user: in Codex's logs and to anything
  the agent prints. Other processes of the same user can read `~/.sidera`. Treat the
  account the node runs as the trust boundary.
- The daemon's log files rotate to one older file; they are not kept indefinitely.
- The command line cannot show which preset or directory an existing session
  uses.
