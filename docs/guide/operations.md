# Operations

How to check a node, upgrade it, change its adapters, keep its identity safe and
fix what commonly goes wrong. Commands run on the machine whose node you manage,
as the user that owns the data directory (`~/.sidera`, on Windows `%LOCALAPPDATA%\Sidera`).

## Health checks

Run these on each node:

```sh
sidera adapter list      # the probe state of each enabled instance (see the notes)
sidera trust list        # the nodes you paired with are under `trusted`
sidera network show      # the first line is the contact line; peers show connected
sidera alerts list       # normally empty
sidera status            # daemon state, queues, delivery counts, and `home`, the data directory
sidera endpoint list     # console, runtime and session endpoints of this node
```

Notes:

- A probe state of `passed` proves only that a front end works. A Codex instance
  shows `pending` first, and while a Codex `workspace-write` preset exists the probe
  may show `failed`, because no command-line front end is offered under that
  sandbox; this is a known limit of the display, and sessions still work. Whether
  `codex` is found shows when a session is created.
- `home` in `sidera status` (also `result.home` with `--json`) is the data directory the
  command used, and so the directory of the daemon that answered: the command asks the
  daemon that runs in the directory it resolved from `--home`, then `SIDERA_HOME`, then
  the default. Check it after a move or when two nodes share a machine.
- `connected` in `network show` is whether a connection is up now. It can be
  `false` for a healthy node until one side sends.
- `sidera peers` and `sidera directory` are agent commands. From your own shell
  they fail with `unauthenticated/token_missing`; that is expected. They work
  inside an agent session, and inside a Claude Code or Codex session you opened
  yourself once it is a
  [controller](agents.md#the-controller-a-session-you-started-yourself).
- `sidera alerts ack <id>` acknowledges an alert once you have dealt with it.
- In a script, `--json` prints the whole response, with the data under `result`:
  `sidera endpoint list --json` prints `{"result":{"endpoints":[...]}}`, not a bare
  list. `sidera-adapter-console recv --wait <s>` waits at most 30 seconds and
  refuses a larger value; call it again to wait longer.

### Logs and state

Everything of a node is under its data directory, `~/.sidera` by default (on Windows
`%LOCALAPPDATA%\Sidera`; the paths below are written for `~/.sidera`), except the empty
directory the Codex App Server runs in, which is the last row.

| What | Where |
| ---- | ----- |
| Daemon log | `~/.sidera/logs/daemon.log` |
| One log per adapter instance | `~/.sidera/logs/adapter-<instance>.log` |
| Codex App Server state and log | `~/.sidera/adapter-state/codex/` (`app-server.log`) |
| Codex App Server working directory (empty, outside the data directory) | `sidera-adapter-codex/<instance>-<hash>` in your user cache directory (`~/Library/Caches` on macOS, `~/.cache` on Linux, `%LOCALAPPDATA%` on Windows); its path is `work_dir` in `app-server.json` |
| What your own shell redirect caught | the file you gave `>>` when you started the daemon (normally empty) |
| Claude Code transcripts | `~/.claude/projects/<working-directory>/*.jsonl` |
| Codex transcripts | `~/.codex/sessions/` |

When an agent does something unexpected, the transcript shows which tool it
called and with what arguments.

On Windows the data directory is `%LOCALAPPDATA%\Sidera`: a protected folder that
does not take on the access entries that other programs add to the folders of your
profile (see [Move the Windows data directory](#move-the-windows-data-directory-to-the-new-default)
and [the troubleshooting entry](#troubleshooting) on foreign entries). Setting
`SIDERA_HOME` to the former directory, `%USERPROFILE%\.sidera`, keeps a node there,
but that folder is one that a Codex sandbox's setup may add its own group to; the
daemon then refuses to start and names the entry.

## Upgrade

Do one machine at a time and check it is healthy before the next. The node's
identity, trust and presets stay where they are; you do not run `init` again.
Read the release notes on the release page first: if a release needs a one-time
manual step on existing nodes, they say so. Let the work that agents are doing on
the node finish first: the node is out of reach while its daemon is stopped, and the
adapters restart.

1. Stop the daemon and wait until it has exited. `sidera daemon stop` returns as
   soon as the daemon has accepted the request, before the process is gone, so
   check that it is:

   ```sh
   sidera daemon stop
   pgrep -f '(^|/)sidera daemon$'    # macOS and Linux: no output once it has exited
   ```

   ```powershell
   # Windows: no output once it has exited
   Get-CimInstance Win32_Process | ? { $_.Name -eq 'sidera.exe' -and $_.CommandLine -match ' daemon' }
   ```

   Stopping the daemon also stops the adapters it started. Processes named
   `sidera mcp` can remain on every OS: Codex starts them outside the daemon's
   reach, so they are not the daemon, and they end with the Codex sessions that use
   them. The Codex App Server is started detached on purpose and keeps running, so
   a running turn continues; the daemon finds it again when it starts (see
   [Stop the daemon and the Codex App Server](#stop-the-daemon-and-the-codex-app-server)
   and [the Codex App Server after an upgrade](#the-codex-app-server-after-an-upgrade)).

2. Replace the programs on your `PATH` with the new release's: `sidera` and
   `sidera-adapter-console`. The adapter programs of Codex and Claude Code are
   replaced by installing their packages in step 4. Extract the new archive into a
   directory of its own, for example `~/sidera-release`
   (`mkdir -p ~/sidera-release && tar -xzf <archive> -C ~/sidera-release`; on
   Windows `Expand-Archive <archive> -DestinationPath $env:USERPROFILE\sidera-release`)
   and copy from there.

   - macOS and Linux: do not overwrite a running program. Copy each to a temporary
     name, then rename:

     ```sh
     for p in sidera sidera-adapter-console; do
       cp ~/sidera-release/$p ~/.local/bin/$p.new && mv ~/.local/bin/$p.new ~/.local/bin/$p
     done
     ```

     (Linux reports `Text file busy` if you overwrite a program that runs.)
   - Windows: after the daemon stops, `sidera mcp` helper processes can remain and
     lock `sidera.exe`. A running program can be renamed, so rename both programs
     and copy the new ones:

     ```powershell
     $bin = "$env:USERPROFILE\sidera\bin"
     Rename-Item $bin\sidera.exe sidera.exe.old
     Rename-Item $bin\sidera-adapter-console.exe sidera-adapter-console.exe.old
     Copy-Item $env:USERPROFILE\sidera-release\*.exe $bin\
     ```

     Once the new daemon runs, delete the two `.old` files: they are only the renamed
     programs of the release before, and nothing uses them. (Windows refuses to delete
     one while a process still runs from it, such as a `sidera mcp` helper of a Codex
     session; leave it until that session ends.) Do not kill the Codex App Server or
     its helpers.

   Only these two programs belong in the directory on your `PATH`. A directory
   filled by an earlier layout of the archive can still hold adapter programs such
   as `sidera-adapter-codex`; nothing uses them, and you can delete them.

3. Start the daemon again, the way you started it before, from your home directory
   (not from one that may be deleted, such as a git worktree).

   A daemon that runs can instead be restarted onto the new program, which makes steps
   1 and 3 unnecessary: replace the programs as in step 2 (renaming a running program
   works on every system), then run
   `sidera daemon restart --expect-version <the new version>` (see
   [Restart the daemon](#restart-the-daemon)). Do this only when the daemon's program
   is the one you replaced; the version check refuses the restart otherwise.

4. Install the new release's package of each adapter:

   ```sh
   sidera adapter install ~/sidera-release/adapters/<adapter>
   ```

   It replaces the installed package, whether the new release carries another version
   of the adapter or the same version rebuilt, and restarts the enabled instances of
   that adapter, which then run the new files. Nothing is disabled: the instances keep
   their session endpoints, and the new adapter takes the sessions back as it does
   after any restart. Run it for every adapter you use (`console`, `codex`, `claude`).
   Replacing the package of an adapter that runs is a request the owner makes on the
   node itself.

   What `install` prints says what it did to the adapter:

   - `replaced true`: an installed package of this adapter was replaced, either by
     another version (for example 0.1.0 to 0.8.0) or by the same version with other
     contents (a rebuild). The instances that run restart.
   - `already_current true`: the package is the one installed, same version and
     same contents; nothing was changed.
   - neither: the adapter was not installed before.

5. Check: `sidera adapter list` (probes `passed`, except Codex as the notes above
   say), then reconnect the nodes. A restart of the daemon drops its connections to
   other nodes, and a connection is opened only when something needs it. Send one
   message from each node, for example from the console:

   ```sh
   sidera-adapter-console send --to <other-node-id>/<its-console-endpoint> --body reconnect
   ```

   Read the console address of a node with `sidera-adapter-console whoami` on that
   node. The other side then runs `sidera-adapter-console recv`. Afterwards
   `sidera network show` lists the peers with `connected true`.

To upgrade Codex or Claude Code themselves, upgrade them as usual. On Windows,
restart the daemon once so that it finds the new Codex release.

### The Codex App Server after an upgrade

The App Server outlives the daemon, so the one that is running after an upgrade was
started by the adapter of the release before. An older adapter ran it in the adapter's
state directory instead of an empty directory of its own, so a sandboxed command can be
given write access to the adapter's files, `ws-token` among them. The adapter never
ends a server it did not start, so you restart it yourself.

`sidera adapter list` does not show this: the probe state of an instance is judged on
its tool probes, and the `app-server` dependency, which the adapter does report as
`wrong_version` to the daemon, does not change it. Two things show it:

- A warning in `logs/adapter-codex.log` (`logs/adapter-<instance>.log` for another
  instance name) each time the adapter finds the server: `the App Server that answers
  runs in the state directory: restart it`.
- `adapter-state/codex/app-server.json` has no `work_dir`. The server records the
  directory it was started in there: an empty directory of the adapter's own in your
  user cache directory, outside the data directory (see [Logs and state](#logs-and-state)).
  A file without `work_dir` is a server that an older adapter started in the state
  directory; one with `work_dir` is not.

Ending the server ends any turn that runs in it, so do it when no Codex session is
working, and only after step 4 of the upgrade has installed the new adapter packages.
While the old adapter still runs, it starts a replacement as soon as it finds no
server, and the replacement is again one that it started. With the new adapter
running, find the server and end it as in
[Stop the daemon and the Codex App Server](#stop-the-daemon-and-the-codex-app-server),
then either leave the daemon running or restart it: the new Codex adapter starts a new
server by itself when it finds none, resumes its sessions and records the new
server's working directory. Check that the new server's `app-server.json` has a
`work_dir` and that the warning no longer appears in the log.

The one case that ends the server earlier is the move of the Windows data directory,
which needs it gone before the directory is moved; it may need ending a second time
after the adapters are installed, as that section says.

### Move the Windows data directory to the new default

A node made by a build before 0.8.0 keeps its data in `%USERPROFILE%\.sidera`. Every
command that uses the data directory refuses until it is moved, or until
`SIDERA_HOME` names the former directory.

`Move-Item` renames the directory only when nothing in it is open. When something
holds a file, it can fail with "being used by another process" after it has created the
target and moved some files one by one. So end everything that holds the directory
first, in this order, and move only when the check in step 3 shows nothing:

1. Stop the daemon and check that it has gone, as in step 1 of the upgrade.
2. End the Codex App Server, if it is running: it survives the daemon, and it holds
   the old paths. Read its process id in
   `%USERPROFILE%\.sidera\adapter-state\codex\app-server.json` and confirm the
   command line before you stop it. It must be an `app-server` whose `--ws-token-file`
   is under that directory, not your own Codex's:

   ```powershell
   $serverPid = (Get-Content $env:USERPROFILE\.sidera\adapter-state\codex\app-server.json | ConvertFrom-Json).pid
   (Get-CimInstance Win32_Process -Filter "ProcessId=$serverPid").CommandLine
   Stop-Process -Id $serverPid
   ```

3. Confirm that no `sidera` daemon, no adapter and no `codex ... app-server` process of
   that directory remains. No output means none:

   ```powershell
   $old = "$env:USERPROFILE\.sidera"
   Get-CimInstance Win32_Process | Where-Object {
     ($_.Name -eq 'sidera.exe' -and $_.CommandLine -match ' daemon') -or
     $_.ExecutablePath -like "$old\*" -or
     ($_.CommandLine -like '*app-server*' -and $_.CommandLine -like "*$old*")
   } | Select-Object ProcessId, Name, CommandLine
   ```

4. Move the directory:

   ```powershell
   Move-Item "$env:USERPROFILE\.sidera" "$env:LOCALAPPDATA\Sidera"
   ```

5. Start the daemon, the way you started it before, and run `sidera status`: its `home`
   line is the new directory.
6. The old adapter is still the one installed until you run `sidera adapter install`
   (step 4 of the upgrade), and when it found no App Server at the first start it
   started one in the state directory. Install the new adapter packages, then end that
   server again by the process id in `adapter-state\codex\app-server.json`, as in
   step 2. The new adapter starts a fresh one, whose `app-server.json` has a `work_dir`
   outside the data directory (see [the section above](#the-codex-app-server-after-an-upgrade)).

**When the move fails halfway.** If `Move-Item` stops with "being used by another
process", do not run it again. `%LOCALAPPDATA%\Sidera` may exist, with some of the files
moved into it one by one, and it has an inherited, unprotected access list. A second
run would then move `.sidera` into `Sidera` as a folder inside it. Also run no
`sidera` command that uses the directory until this is settled: the daemon takes an
existing `%LOCALAPPDATA%\Sidera` as the data directory and would start on the part
that was moved. Instead:

1. End what holds the files: repeat steps 1 to 3, and close any program that has a file
   of the directory open (an editor, a backup tool, an antivirus scan).
2. Move the files that were moved back:

   ```powershell
   Get-ChildItem -Force "$env:LOCALAPPDATA\Sidera" | Move-Item -Destination "$env:USERPROFILE\.sidera"
   ```

3. Remove the empty target, which refuses if anything is left in it:

   ```powershell
   Remove-Item "$env:LOCALAPPDATA\Sidera"
   ```

4. Move again from step 4.

A directory that was renamed as a whole keeps the access list it had, and so does a
file that was moved, because a move within one volume is a rename. A target that
`Move-Item` created itself does not: it gets the inherited list of its parent, which
the daemon refuses at start (see [Troubleshooting](#troubleshooting)), so the whole-directory
rename is the only move to rely on. A folder that a Codex sandbox's setup had already
granted its group carries that entry along in the rename, and the daemon then refuses
to start and names it. A Codex App Server that an older adapter started in the state
directory and that you did not end is a leftover that step 6 and the section above deal with.

## Restart the daemon

```sh
sidera daemon restart
sidera daemon restart --expect-version 0.12.0   # after replacing the program
```

The daemon shuts down in its orderly way and starts the program again as it was started:
the same path, arguments, environment and working directory. Before it accepts, it runs
that program as `version --json` and refuses to restart, staying up, when the program
does not answer, when it reports a version other than `--expect-version`, or when the
daemon cannot start itself again here. The command waits (`--wait`, 2 minutes by
default) until the status shows a new `start_id` and the daemon running, and prints
both start identifiers, the program and its version. Use it after replacing the
program, after `sidera network listen set`, or to make the daemon find a new Codex
release. The sessions of the adapters are handled as for any restart of the daemon: see
[Stop the daemon and the Codex App Server](#stop-the-daemon-and-the-codex-app-server).

- macOS and Linux: the process is replaced in place. Its process number, its terminal
  and its tmux session stay, and a daemon running in the foreground stays in the
  foreground. The program is the path the daemon was started from; a link that points
  at the current version (a package manager's) is started again as the link. If the
  daemon was started as `sidera` from a directory on your `PATH`, that is the file
  found there.
- Windows: a process cannot replace itself, so the old process ends and a detached new
  one takes its place, with the same arguments and environment, at the same level of
  privilege. Its output goes on to the file the start script redirected to
  (`daemon.log` in the script above). A daemon started by a scheduled task stays alive
  after the restart: the new process stays in the task's job, which does not end its
  processes when it closes, so the task shows as Ready once the old process has
  ended, and ending the task in Task Scheduler no longer stops the daemon (use
  `sidera daemon stop`); starting the task again fails, because a daemon holds the
  data directory. When whatever started the daemon puts it in a job that ends its
  processes when it closes (some service wrappers), the daemon refuses the restart
  (`restart_unavailable`): restart it through the wrapper.

If the program was accepted by the check but does not come back (for example it was
replaced by a file that fails to start after the check), the daemon is gone. The command
then fails after the wait and names `<data directory>/logs/daemon.log`; start the daemon
the way you started it before, and read the log and the output of the start script to
see why it did not run.

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
  ps -axo pid,command | grep "[a]pp-server --listen unix://${SIDERA_HOME:-$HOME/.sidera}/adapter-state"
  kill <pid>
  ```

- Windows: the process id is in `%LOCALAPPDATA%\Sidera\adapter-state\codex\app-server.json`.
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

**The daemon does not start: `the data directory is not private to its owner: <path> grants <rights> to <trustee>`.**
At every start the daemon checks the whole data directory: the root, every directory
below it, the key files and the files that hold a token (those under `keys/`,
`state/endpoints/` and `adapter-state/`). It refuses to start when anyone but the owner
(and, on Windows, SYSTEM and Administrators) can reach one of them. An adapter whose
state directory fails the same check while the daemon runs does not start, and the
message is in `logs/daemon.log`. The message names the path, who can reach it and with
which rights: on Windows an account or group by SID and name (`unresolved` when the
system cannot resolve the SID), for example `...\adapter-state\codex grants Modify
(0x1301bf) to S-1-5-21-... (unresolved)`; on Unix the mode, for example `has mode 0755,
which lets the group or others reach it`.

Sidera never repairs it. Only you know who has already read what the files hold, and
rewriting the list would hide that. Find what added the entry (a sandbox, an
installer, a backup tool), treat what the files hold as read by that account, remove
the entry and start again. If the daemon names another path, repeat; it names the
first it finds.

- Inspect. Windows: `icacls <path>` lists the entries; `Get-Acl <path> | Format-List`
  shows them with their SIDs. Unix: `ls -ld <path>` shows the mode and the owner.
  `icacls` writes in the console's code page, so on a non-English Windows its output
  is garbled over ssh. For a script, read PowerShell's properties, whose names do not
  change: `Get-Acl <path> | Format-List`, or `(Get-Acl <path>).Access` for the entries
  (`IdentityReference`, `FileSystemRights`, `AccessControlType`, `IsInherited`). Or run
  `chcp 65001` first and keep using `icacls`.
- Remove, Windows: `icacls <path> /remove:g <group> /T` removes the entries granted
  to an account or group by name, and from the directory's files too. An entry
  whose SID the system cannot resolve (a group that no longer exists) is refused by
  `icacls` with error 1332; remove it by SID in PowerShell:

  ```powershell
  $path = '<path>'
  $acl = Get-Acl $path
  $acl.PurgeAccessRules([System.Security.Principal.SecurityIdentifier]'S-1-5-21-...')
  Set-Acl $path $acl
  ```

  `PurgeAccessRules` removes the rules written on that folder for the SID, and
  `Set-Acl` carries the change to what it contains. An entry that shows as inherited
  comes from the folder above, so fix that one.
- Remove, Unix: `chmod go-rwx <path>` (or `chown` when the owner is not you).

A common cause on Windows is the elevated sandbox of Codex (`[windows] sandbox =
"elevated"`). Its setup grants the sandbox's group read access, inheritable, on most
first-level folders of your profile, and it grants that group write access to the
writable roots of sandboxed commands. With the data directory at the default,
`%LOCALAPPDATA%\Sidera`, the first does not reach Sidera: that folder has a
protected access list that does not take on what is added above it. It does reach a
node that was left in `%USERPROFILE%\.sidera` (by `SIDERA_HOME`, or because it was
not moved), and it can reach `adapter-state\codex` when a Codex App Server that an
earlier build started in that directory was left running (see
[the Codex App Server after an upgrade](#the-codex-app-server-after-an-upgrade)).
Remove the group's entries as above, move the node if it is in the profile, and end
the old server.

**On Windows every desktop client fails with `peer_check_failed` while the daemon runs.**
The daemon was started from an elevated shell, for example over SSH by an administrator
account, which gets a high-integrity token. A client in your desktop session verifies the
daemon's process before it sends anything and cannot do that for a process at a higher
level, so the `sidera` command, the console and a controller session all fail, and the
daemon looks unreachable. Stop it from the elevated shell it runs in (`sidera daemon stop`)
and start it from a normal desktop session, as in
[the detached start](install.md#windows-a-detached-start-that-survives-logout).

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
- A session Sidera started does not close by itself, and `sidera endpoint list` keeps
  listing closed ones. Close sessions you no longer need with `sidera endpoint close`.
  The endpoint of a [controller](agents.md#the-controller-a-session-you-started-yourself)
  closes by itself once its session has been gone for the queue time with nothing
  waiting on it.
- A Claude Code controller is woken through its session's inbox. A Codex controller is woken within about 10 seconds, and
  not after you press Esc until your next turn completes.
- Both adapters stop a running turn on `sidera control <session-address>
  session.interrupt`. The answer is `stopped` once the turn has ended, `requested`
  when it is still running after about 10 seconds (its end is still reported), and
  `not_running` when no turn runs. What the turn already did stays done. Closing a
  Codex session that is in the middle of a turn is not confirmed until the turn ends.
- A `send` from an agent session takes one recipient. The console's `send` takes a
  comma-separated list.
- A Codex session whose sandbox is `read-only` or `workspace-write` uses the MCP
  tools only; a `sidera` command run in a shell fails with `cannot reach the Sidera
  daemon at <socket path>: permission denied (a sandbox may be blocking it)`, and
  the command needs `danger-full-access`. While such a preset exists, `sidera
  adapter list` may show the Codex probe as `pending` and then `failed`; sessions
  still work. `workspace-write` has no network. On Windows it depends on
  Codex's own sandbox working: with `[windows] sandbox = "elevated"` in
  `~/.codex/config.toml`, every shell command failed, while `unelevated` worked.
  Check that `codex sandbox -- cmd /c echo hi` works first.
- What a session can read of the data directory, which holds the node's identity key
  and the token of every session, depends on the preset. A Codex `workspace-write` or
  `read-only` session cannot read it on macOS and Linux: the operating system
  enforces a denial, except for the user's own MCP servers and for a command that
  you approved to run outside the sandbox. On Windows the directory's location,
  `%LOCALAPPDATA%\Sidera`, with an access list for its owner only, is the main
  guard, and it is what keeps the sandbox out. Codex adds a deny entry for the data
  directory to the sandbox's accounts only when a sandboxed Sidera session runs a
  command (a plain `codex sandbox` run outside Sidera adds none), and another Codex
  session can revoke it. A Claude Code `light` session's file tools cannot read it,
  but if your own Claude settings allow a Bash file command such as `grep` or `find`,
  the session can read it; a guarantee needs Claude's operating-system sandbox, which
  Sidera does not use yet. A full-access session, of either runtime, acts as the local
  owner and reads everything. See
  [Presets and access](getting-started.md#what-a-session-can-read-of-the-nodes-data).
- The management interface trusts the operating-system user, not the session. Under
  a full-access preset an agent session can run every owner command and acts as the
  local owner of the node. A Claude Code `light` session cannot, unless your own
  Claude Code settings allow a broader `Bash`. A Codex `workspace-write` session
  cannot reach the daemon socket from its shell on Linux and macOS; on Windows this
  was not measured. See
  [Presets and access](getting-started.md#the-management-interface-trusts-the-user-not-the-session).
- A session's credential is visible to its own user: in Codex's logs and to anything
  the agent prints. Other processes of the same user can read the data directory. Treat the
  account the node runs as the trust boundary.
- The daemon's log files rotate to one older file; they are not kept indefinitely.
- The command line cannot show which preset or directory an existing session
  uses.
