# Operations

How to check a node, upgrade it, change its adapters, keep its identity safe and
fix what commonly goes wrong. Commands run on the machine whose node you manage,
as the user that owns `~/.sidera`.

## Health checks

Run these on each node:

```sh
sidera adapter list      # every enabled instance has a probe state of passed
sidera trust list        # the nodes you paired with are under `trusted`
sidera network show      # the first line is the contact line; peers show connected
sidera alerts list       # normally empty
sidera status            # daemon state, queues, delivery counts
sidera endpoint list     # console, runtime and session endpoints of this node
```

Notes:

- A probe state of `passed` for Codex proves only that the `sidera` front end
  works. While a Codex `workspace-write` preset exists the probe may show `failed`,
  because no command-line front end is offered under that sandbox; sessions still
  work. Whether `codex` is found shows when a session is created.
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
Read the release notes first: if a release needs a one-time manual step on
existing nodes, they say so.

1. Stop the daemon and wait until it has exited:

   ```sh
   sidera daemon stop
   ```

   This also stops the adapters it started. The Codex App Server is started
   detached on purpose and keeps running, so a running turn continues; the daemon
   finds it again when it starts.

2. Replace the programs on your `PATH` with the new release's: `sidera` and
   `sidera-adapter-console`. The adapter programs of Codex and Claude Code are
   replaced by installing their packages in step 4.

   - macOS and Linux: do not overwrite a running program. Copy to a temporary
     name, then rename:

     ```sh
     cp sidera ~/.local/bin/sidera.new && mv ~/.local/bin/sidera.new ~/.local/bin/sidera
     ```

     (Linux reports `Text file busy` if you overwrite a program that runs.)
   - Windows: after the daemon stops, Codex's `sidera mcp` helper processes can
     remain and lock `sidera.exe`. A running program can be renamed, so rename and
     copy:

     ```powershell
     Rename-Item $env:USERPROFILE\sidera\bin\sidera.exe sidera.exe.old
     Copy-Item .\sidera.exe $env:USERPROFILE\sidera\bin\
     ```

     Delete `sidera.exe.old` after the daemon has started again. Do not kill the
     Codex App Server or its helpers.

3. Start the daemon again, the way you started it before, from a stable directory
such as your home directory (never one that may be deleted, such as a git
worktree).

4. Install each new adapter package from the release's `adapters/` directory:

   ```sh
   sidera adapter install /path/to/adapters/codex
   sidera adapter install /path/to/adapters/console
   ```

   When the package differs from the installed one but has the same version, the
   result says `replaced true`, and running instances restart on their own. When
   it is the same, it says `already_current`. A package with a new version number
   is refused with `conflict/adapter_in_use` while an instance of that adapter is
   enabled. In that case run `sidera adapter disable <instance>` (this closes the
   instance's endpoints, so its sessions end), install, then
   `sidera adapter enable <adapter-id> --instance <instance>`.

5. Check: `sidera adapter list` (probes `passed`), then reconnect the nodes. A
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

## Change an adapter's configuration

An adapter instance has a small configuration of its own. Codex takes
`codex_program` and `sidera_program` (the program to run, as a path or a name
looked up on the `PATH`); Claude takes `claude_path` and `sidera_path`.

```sh
sidera adapter config codex --config codex-instance.json
```

The file is the **whole** new configuration, not a patch. A key you leave out is
removed, and an empty configuration is `{}`. The instance restarts to take it,
whether it was enabled or not. `-` reads the configuration from standard input.

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

Until you confirm, the node reports a reminder. Treat the file as a secret:
whoever holds it can retire the node. Without `--out`, the command prints the
statement to the terminal; use that if the file system cannot make a private file,
for example a Windows FAT volume or a network share.

## Troubleshooting

**The daemon does not start: the port is in use.** The start fails with an error
naming the address. Another process holds port 7744, or an old daemon is still
running. Find the holder (`lsof -i :7744` on macOS and Linux,
`Get-NetTCPConnection -LocalPort 7744` in PowerShell), stop it, or move the node:

```sh
sidera network listen set /ip4/0.0.0.0/tcp/7745 /ip4/0.0.0.0/udp/7745/quic-v1
```

then restart the daemon and give the other nodes the new contact line.
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

**Claude sessions fail with `native_unavailable` and the adapter log says "The
current directory no longer exists".** The daemon was started from a directory that
has since been deleted, such as a git worktree. Stop the daemon, start it from a
stable directory such as your home directory
(`tmux new-session -d -s sidera -c "$HOME" "sidera daemon"`), and create the session
again.

**A session fails with `native_unavailable`, but `adapter list` says `passed`.**
The daemon cannot find `codex` (or `claude`). It looks the program up on its own
`PATH`, which is not your interactive shell's. Start the daemon from a login shell
or with the `PATH` set (on Windows see
[the detached start](install.md#windows-a-detached-start-that-survives-logout)).
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
- A `send` takes one recipient.
- A Codex session whose sandbox is `read-only` or `workspace-write` uses the MCP
  tools only; a `sidera` command run in a shell fails with an unhelpful
  `internal error`, and the command needs `danger-full-access`. While such a preset
  exists, `sidera adapter list` may show the Codex probe as `failed`; sessions still
  work. `workspace-write` has no network and does not stop the agent from reading
  files outside its directory, including `SIDERA_HOME`. On Windows it depends on
  Codex's own sandbox working: with `[windows] sandbox = "elevated"` in
  `~/.codex/config.toml`, every shell command failed, while `unelevated` worked.
  Check that `codex sandbox -- cmd /c echo hi` works first.
- A session's credential is visible to its own user: in Codex's logs and to anything
  the agent prints. Other processes of the same user can read `~/.sidera`. Treat the
  account the node runs as the trust boundary.
- The daemon's log files rotate to one older file; they are not kept indefinitely.
- The command line cannot show which preset or directory an existing session
  uses.
