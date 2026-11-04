# Sidera guide for AI agents

You are an AI agent. This page tells you how to deploy Sidera on the machine you
run on, pair it with another machine, and then work with other agents through it.
It is self-contained: follow it from top to bottom. Pages that go deeper are
linked at the end, but you do not need them.

**What Sidera is.** A program (`sidera`) that runs as a daemon on each machine.
Agents on different machines send each other messages through their daemons. A
machine joins a cluster by **pairing** with another machine once. Everything in
one owner's cluster belongs to one owner (the person who asked you to do this,
called "your owner" below).

**How to read the steps.** Each step gives a command, what you should see, how to
check it and what to do if the check fails. Run a step's check before the next
step. Replace anything in angle brackets. Stop and report to your owner when a
step fails twice.

**One setting for every command.** Every `sidera` and `sidera-adapter-console`
command, and the daemon, uses the data directory named by `--home`, then by
`SIDERA_HOME`, then `~/.sidera` (on Windows `%LOCALAPPDATA%\Sidera`). If a command needs another
directory, every command for that node needs the same `SIDERA_HOME`. A Windows machine
that has a node in the former place, `%USERPROFILE%\.sidera`, and none in the new one
is refused by every command that uses the directory, with the `Move-Item` command that
moves it; nothing is moved for you, and `SIDERA_HOME` set to the former place keeps it
there. A second node on the same machine needs
its own `SIDERA_HOME` and its own port.

## Ask your owner before

Do not do any of the following without your owner's explicit yes in this
conversation. Ask once, say what you will do and why, and wait.

1. **Choosing a full-access preset.** Codex `danger-full-access` or Claude
   `bypassPermissions` lets any trusted node in the cluster direct an agent on
   this machine to run anything on it, and the session's Sidera token is visible to
   everything the agent runs. The node's management interface trusts the operating
   system user, not the session, so such a session also runs every owner command and
   acts as the local owner of the node. Add only the `light` preset below unless the
   owner chose otherwise.
2. **Changing the firewall.** Opening port 7744 or adding a rule is the owner's
   call. Tell the owner the exact command and let them run it, or run it only after
   a yes. Allow the local network only.
3. **Pairing with a node the owner did not name.** Pair only with the node ids the
   owner gave you. A pairing grants that node the right to start sessions here.
4. **Closing sessions you did not create.** You may close sessions you created
   yourself. Leave all others alone.
5. **Installing an adapter from an unknown source.** Install adapter packages only
   from the official release archive of Sidera or from a build the owner gave you.
   An adapter runs as a program with the owner's rights.
6. **Any owner command from inside a Sidera session.** `preset`, `adapter`, `trust`,
   `network`, `endpoint close`, `daemon stop`, `daemon restart` and `identity` manage the node. When
   you run inside a Sidera session (Part 2) the node would let you run them under a
   full-access preset, because it trusts the user, but they are the owner's to
   decide. Do not run them there without a yes.
7. **Creating a session with a full-access preset on another node.** The agent
   there then runs anything on that machine. Use `light` unless the owner named the
   other preset.
8. **`trust remove` and `trust clear`.** They cut a node off or take it back, and
   the other node is not told.
9. **`adapter config` that points at another program.** `codex_program`,
   `sidera_program`, `claude_path` and `sidera_path` name programs the daemon runs
   with the owner's rights.
10. **Choosing the working directory** (`project_dir` for Claude, `cwd` for Codex)
    that sessions started by trusted nodes will work in. Ask which directory to use.
11. **Persistent changes outside Sidera's own directories:** editing a shell
    profile, changing the Windows user `PATH`, registering a scheduled task, adding
    the `sidera` MCP server to Claude Code or Codex, or allowing its tools there (the
    rule `mcp__sidera` in `~/.claude/settings.json`, the line
    `default_tools_approval_mode = "approve"` in `~/.codex/config.toml`). Ask once
    before the first one and name the file or setting.

Also ask before you delete anything under `~/.sidera` (on Windows `%LOCALAPPDATA%\Sidera`; the node's
keys and state)
and before you run `sidera identity retire`, which permanently destroys the node's
identity. Before a data directory is deleted, the Codex App Server it started has to
be ended too (step 4).

---

# Part 1. Deploy Sidera on this machine and pair it with another

## 1. Check the prerequisites

1. Identify the platform.

   ```sh
   uname -sm            # macOS and Linux
   ```

   On Windows, run `(Get-CimInstance Win32_Processor).Architecture` in PowerShell:
   `9` is amd64 and `12` is arm64. Do not use `$env:PROCESSOR_ARCHITECTURE`: an
   x64 PowerShell running under emulation on Arm reports `AMD64`. Supported:
   macOS (arm64, amd64), Linux (amd64, arm64), Windows (amd64, arm64).

2. Find which agent runtime this machine has and whether the daemon will find it.

   ```sh
   command -v codex claude      # macOS and Linux
   Get-Command codex.exe, claude.exe   # Windows (needs the .exe, not a .cmd wrapper)
   ```

   Codex and Claude Code work on all three platforms. At least one of
   them must be installed and signed in as the owner. Check: the command prints a
   path. If neither is found, try an interactive shell, which reads the profile
   that a shell over `ssh` or a tool's shell may skip:

   ```sh
   "$SHELL" -i -c 'command -v codex claude'
   ```

   If that prints a path, the runtime is installed but its directory is not on
   this shell's `PATH`: add that directory to `PATH` for every command below and
   for the daemon (step 4). If neither is found there either, tell your owner and
   stop; Sidera still runs without an agent but has nothing to start.

3. Check that port 7744 is free.

   ```sh
   lsof -i :7744                       # macOS and Linux; no output means free
   Get-NetTCPConnection -LocalPort 7744 -ErrorAction SilentlyContinue   # Windows
   ```

   If it is taken, create the node on another port in step 3 with
   `sidera init --port <n>`, and use that port wherever 7744 appears below. This is
   the one way this guide moves the port; steps 3 and 4 refer back here.

4. Check that no node exists yet.

   ```sh
   ls ~/.sidera          # Windows: dir $env:LOCALAPPDATA\Sidera
   ```

   If it exists, a node is already here. Do not run `init` in it, and ask your owner
   before you go on: do they want that node used as it is, or a second node next to
   it? For a second node, choose a data directory that does not exist (for example
   `~/sidera-2`, on Windows `%LOCALAPPDATA%\Sidera-2`) and a free port (run the
   check of 1.3 for that port; the first node holds 7744 or the port it was created
   with). Then:

   - every `sidera` and `sidera-adapter-console` command for the new node carries
     `SIDERA_HOME=<that directory>` (`export SIDERA_HOME=...` in the shell, or
     `$env:SIDERA_HOME = '...'` in PowerShell, or `--home <directory>` on each
     command), and the first node's commands do not;
   - step 3 creates it with `sidera init --port <n>`;
   - step 4 gives it a tmux session name and a log file of its own.

## 2. Get Sidera

Choose A (release archive) or B (source).

### A. Release archive

1. Download the archive for your platform and `checksums.txt` from
   <https://github.com/aianyai/sidera/releases>. The archive is named
   `sidera_<version>_<os>_<arch>.tar.gz`, `.zip` on Windows, with `<os>` one of
   `darwin`, `linux`, `windows` and `<arch>` one of `arm64`, `amd64`: the architecture
   you found in 1.1. With the
   GitHub CLI:

   ```sh
   gh release download --repo aianyai/sidera --pattern 'sidera_*_<os>_<arch>.*' --pattern checksums.txt
   ```

2. Verify the checksum.

   ```sh
   shasum -a 256 -c checksums.txt --ignore-missing     # macOS
   sha256sum -c checksums.txt --ignore-missing         # Linux
   (Get-FileHash .\sidera_<version>_windows_<arch>.zip -Algorithm SHA256).Hash   # Windows: compare with checksums.txt
   ```

   Expected: `OK` for the archive. If not, delete it and download again. Never
   install an archive whose checksum fails.

   Optional, when `minisign` is installed: releases from 0.11.0 also have
   `checksums.txt.minisig` (download it with `--pattern checksums.txt.minisig`;
   releases up to 0.10.0 carry no signature). Verify that file with the release key:

   ```sh
   minisign -Vm checksums.txt -P RWR7j5o6zjWTutDvAgBfSyNd7aQXb7k9lN1L4iMavn6zZUtYxLJT+T1L
   ```

   Expected: exit status 0 and the lines `Signature and comment signature verified`
   and `Trusted comment: sidera <version> update-from <floor>`, the floor being this
   version or an older one. Anything else
   means the checksums are not the release's: delete them and do not install. With
   `gh`, `gh attestation verify <archive> -R aianyai/sidera` checks where the archive
   was built.

3. Extract and install.

   macOS and Linux:

   ```sh
   mkdir -p ~/.local/bin ~/sidera-release
   tar -xzf sidera_<version>_<os>_<arch>.tar.gz -C ~/sidera-release
   cp ~/sidera-release/sidera ~/sidera-release/sidera-adapter-console ~/.local/bin/
   export PATH="$HOME/.local/bin:$PATH"
   ```

   The `export` lasts for this shell. Make sure `~/.local/bin` is on the `PATH` of
   the owner's login shell too: if it is not, ask your owner (see "Ask your owner
   before"), then add the `export` line to the file that shell reads (`~/.zprofile`
   for zsh, `~/.bash_profile` or `~/.profile` for bash). On macOS, if a program is
   refused because it was downloaded by a browser, run
   `xattr -d com.apple.quarantine ~/.local/bin/sidera ~/.local/bin/sidera-adapter-console`.

   Windows (PowerShell):

   ```powershell
   Expand-Archive .\sidera_<version>_windows_<arch>.zip -DestinationPath $env:USERPROFILE\sidera-release
   New-Item -ItemType Directory -Force $env:USERPROFILE\sidera\bin | Out-Null
   Copy-Item $env:USERPROFILE\sidera-release\*.exe $env:USERPROFILE\sidera\bin\
   [Environment]::SetEnvironmentVariable('Path', "$([Environment]::GetEnvironmentVariable('Path','User'));$env:USERPROFILE\sidera\bin", 'User')
   $env:Path += ";$env:USERPROFILE\sidera\bin"
   ```

   Changing the user `PATH` is a persistent change (see "Ask your owner before").
   If the archive extracted into a subdirectory, copy from there. The adapter
   packages (Codex, Claude Code, console) are in the extracted
   `adapters/<name>/` directories, `~/sidera-release/adapters/<name>` on macOS and
   Linux and `%USERPROFILE%\sidera-release\adapters\<name>` on Windows; the adapter
   programs for Codex and Claude Code are inside them and are not copied by hand.

   From 0.12.0 on, upgrade a node installed this way with `sidera update` (ask your owner first: it
   replaces the programs, restarts the daemon and installs the new adapter packages;
   `sidera update --check` only reads). It needs a terminal or `--yes`, refuses to run with
   `sudo` or from a symbolic link, and does the missing steps again if it is interrupted;
   see [Upgrade](guide/operations.md#upgrade).

### B. Build from source

You need Go 1.26 or newer.

```sh
git clone https://github.com/aianyai/sidera.git && cd sidera
mkdir -p bin packages ~/.local/bin
CGO_ENABLED=0 go build -o bin/ ./cmd/sidera ./cmd/sidera-adapter-console ./cmd/sidera-adapter-codex
CGO_ENABLED=0 go build -o bin/ ./cmd/sidera-adapter-claude
for a in console codex claude; do bin/sidera-adapter-$a package --goos "$(go env GOOS)" --goarch "$(go env GOARCH)" --binary bin/sidera-adapter-$a --out packages/$a; done
cp bin/sidera bin/sidera-adapter-console ~/.local/bin/
```

The packages are then in `packages/<name>/`. On macOS and Linux, put `~/.local/bin`
on the `PATH` as in A.3 (`export PATH="$HOME/.local/bin:$PATH"`, and the login shell
file with your owner's yes). On Windows, use the same commands in PowerShell and the
`.exe` names, and copy the programs into `%USERPROFILE%\sidera\bin` and onto the
user `PATH` as in A.3.

### Check

```sh
sidera version
"$SHELL" -l -c 'command -v sidera'      # macOS and Linux
(Get-Command sidera).Source             # Windows
```

Expected: one line such as `sidera 0.12.2 (commit abc123456789, go1.26.10)`. A
program built from source without release settings prints `0.0.0-dev`. The second
command starts a login shell and must print the path of the binary you just
installed (`~/.local/bin/sidera` expanded), not another one and not nothing: the
daemon and the owner's other shells find `sidera` the way a login shell does. If
`sidera` is not found, the directory is not on the `PATH`; fix that first.

## 3. Create the node

```sh
sidera init
```

Expected output, with your own ids:

```
Back up your self-revocation outside this device.
node <node-id>
home <home>
Run `sidera identity export-revocation` to export it.
```

To use another port (step 1.3), run `sidera init --port <n>` instead; for a second
node (step 1.4) set its `SIDERA_HOME` first. Record the
`node <node-id>` value: it is this machine's identity. Check: `ls ~/.sidera` (Windows:
`dir $env:LOCALAPPDATA\Sidera`; for a second node, its `SIDERA_HOME`) shows the
directory. If `init` says the directory is already initialized, a node exists:
stop (see step 1.4). `init` never overwrites.

Tell your owner to back up the self-revocation (step 9 does it).

## 4. Start the daemon so that it survives your session

The daemon must run with an environment in which `sidera` and the agent runtime
(`codex` or `claude`) are on the `PATH`, and it must not end when your session ends.

Start the daemon from your home directory, not from a directory that may be deleted
later, such as a git worktree.

The commands below name the first node on the machine: session `sidera`, log
`~/sidera-daemon.log` (Windows: `start-daemon.cmd` and `daemon.log`). A second node
(step 1.4) must not reuse them: give it names of its own in every command of this
step, for example session `sidera-2`, log `~/sidera-2-daemon.log`, `start-daemon-2.cmd`
and `daemon-2.log`, and its `SIDERA_HOME` (below).

**macOS and Linux.** Use `tmux` (or `screen`):

```sh
tmux new-session -d -s sidera -c "$HOME" -e PATH="$PATH" 'sidera daemon >> ~/sidera-daemon.log 2>&1'
```

A tmux session starts with the environment of the tmux server, not of your shell, and
the server may be much older. `-e PATH="$PATH"` hands the session your shell's
`PATH` (tmux 3.2 or newer), so run the command in a shell where `command -v sidera`
and `command -v codex claude` print paths. If the node uses a `SIDERA_HOME`, add
`-e SIDERA_HOME=<directory>`. If `tmux` is an alias, use `/usr/bin/env tmux`.
Without tmux, `cd ~ && nohup sidera daemon >> ~/sidera-daemon.log 2>&1 &` also works;
there, set `SIDERA_HOME` in the shell first.

Check right away and again after a few seconds:

```sh
tmux ls
```

Expected: a line for the session `sidera` (your session name for a second node). If
it is gone, the daemon exited: run
`sidera daemon` once in the foreground, in the same shell, read the error, and
stop it with Ctrl-C.

**Windows.** A process started from your session ends with it. Start the daemon
through WMI instead. First create `%USERPROFILE%\sidera\start-daemon.cmd` (CRLF
line endings):

```bat
@echo off
for /f "usebackq delims=" %%P in (`powershell -NoProfile -NonInteractive -File "%~dp0daemon-path.ps1"`) do set "PATH=%%P;%USERPROFILE%\sidera\bin;%PATH%"
"%USERPROFILE%\sidera\bin\sidera.exe" daemon >> "%USERPROFILE%\sidera\daemon.log" 2>&1
```

and `%USERPROFILE%\sidera\daemon-path.ps1`, which builds the `PATH` that a WMI
process lacks:

```powershell
$machine = [Environment]::GetEnvironmentVariable('Path', 'Machine')
$user = [Environment]::GetEnvironmentVariable('Path', 'User')
$current = Join-Path $env:USERPROFILE '.codex\packages\standalone\current'
$target = (Get-Item -LiteralPath $current -Force -ErrorAction SilentlyContinue).Target
$codex = if ($target) { @($target)[0] + '\bin;' } else { '' }
"$codex$machine;$user"
```

(The `$current` lines handle Codex's standalone install; they are harmless if you
do not have it.) For a second node, add `set "SIDERA_HOME=<directory>"` above the
last line of the `.cmd` file, because a WMI process does not get your shell's
variables, and use the file and log names of its own. Then:

```powershell
$r = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{CommandLine="cmd.exe /c `"$env:USERPROFILE\sidera\start-daemon.cmd`""; CurrentDirectory=$env:USERPROFILE}
$r.ReturnValue
```

Expected: `0`.

**Check (all platforms), after a few seconds:**

```sh
sidera status
```

Expected: a listing that contains the line `daemon.state running`, possibly after a
warning line (the self-revocation is not confirmed yet; step 9). If you get
`daemon_not_running`, read the log you redirected to. Common causes: port 7744 in
use (the error names the address and the daemon exits; free the port, or create
the node again on another port as in step 1.3 if nothing is paired yet and your
owner agrees to delete the data directory), or a socket path too long (use a shorter
`SIDERA_HOME`).

To stop the daemon, run `sidera daemon stop`. It leaves the **Codex App Server**
running, by design: the Codex adapter starts it detached so that a running turn
survives a restart of the daemon. Before you delete a data directory, end it, after
the daemon has stopped. On macOS and Linux, find the process whose command line
names this node's `adapter-state` directory and end it. The command below matches
only the node whose data directory `SIDERA_HOME` names (an absolute path), or
`~/.sidera` when it is unset; do not touch an App Server of another node or of the
owner's own Codex:

```sh
ps -axo pid,command | grep "[a]pp-server --listen unix://${SIDERA_HOME:-$HOME/.sidera}/adapter-state"
kill <pid>
```

On Windows the process id is in `%LOCALAPPDATA%\Sidera\adapter-state\codex\app-server.json`;
run `Stop-Process -Id <pid>`.

## 5. Install and enable adapters

Run these from the directory that holds the packages (`~/sidera-release/adapters`
or `packages/`). Use absolute paths if you are elsewhere.

```sh
sidera adapter install <packages>/console
sidera adapter enable console
sidera adapter install <packages>/codex      # if this machine has Codex
sidera adapter enable codex
sidera adapter install <packages>/claude     # if this machine has Claude Code
sidera adapter enable claude
```

Expected from `enable`: `enabled, probing in progress; see sidera adapter list`.

Check, after a few seconds:

```sh
sidera adapter list
```

Expected: the console instance (and Claude) shows a probe state of
`passed`. The Codex instance shows `pending` first, and once a `workspace-write`
preset exists (step 6) it may show `failed`: the probe needs the command-line front
end, which that sandbox does not offer. This is a known limit of the display, not a
fault; do not try to fix it, and rely on step 10 to show that Codex works. For any
other `failed`, read `~/.sidera/logs/adapter-<instance>.log` (Windows: `%LOCALAPPDATA%\Sidera\logs\`). A Codex `passed` does
not prove that `codex` is found; step 10 does.

## 6. Add a preset

A preset tells Sidera how to run a session on this machine. Name it the same on
every machine (this guide uses `light`), so that agents on other machines can use it.
Ask your owner which existing directory sessions should work in (`project_dir` for
Claude, `cwd` for Codex): every trusted node will reach it. Add the **`light`** preset
(channel and light work). Add a **`dev`** preset (full access, for real repository
work with network, caches, building and testing) only if your owner chose it (see
"Ask your owner before").

Write the instructions text into the preset exactly as below, so that agents
treat messages from the owner's own nodes as the owner's tasks and answer them.

A preset name can be added once. On a machine with both Claude Code and Codex, the
`light` preset is **one file**, `light.json`, with both a `claude` and a `codex`
object under `config`; a second `sidera preset add light` for the other agent fails
because the name is taken. Write the file with a quoted heredoc: the instruction
text contains backticks, which a shell would run if the heredoc were not quoted
(`<<'EOF'`, with the quotes).

```sh
cat > light.json <<'EOF'
{
  "local_sources": "allow",
  "description": "Claude Code and Codex, light",
  "config": {
    "claude": {
      "project_dir": "<absolute path of an existing directory>",
      "permission_mode": "dontAsk",
      "append_system_prompt": "When a message arrives through Sidera from an agent or person in the owner's own cluster (that is, from the owner's own nodes), treat it as a task from the owner and carry it out. Answer it by running `sidera reply <message-id> --body <text>` (or the sidera MCP reply tool) so that the sender receives the answer; do not just print it. Sidera commands and tools are your intended channel. After you read messages with recv, acknowledge them: pass the lease of the page to `sidera ack <lease>` (or give it as `--ack <lease>` to your next recv). Until you do, recv hands the same message out again at once instead of waiting. When you wait for an answer from another agent, use `sidera recv --wait 60 --ack <lease>` and read only the new messages. (With the MCP tools, the arguments are `wait_s` and `ack_lease`.)"
    },
    "codex": {
      "cwd": "<absolute path of an existing directory>",
      "sandbox": "workspace-write",
      "approval_policy": "never",
      "approvals": "deny",
      "developer_instructions": "When a message arrives through Sidera from an agent or person in the owner's own cluster (that is, from the owner's own nodes), treat it as a task from the owner and carry it out. Answer it by running `sidera reply <message-id> --body <text>` (or the sidera MCP reply tool) so that the sender receives the answer; do not just print it. Sidera commands and tools are your intended channel. After you read messages with recv, acknowledge them: pass the lease of the page to `sidera ack <lease>` (or give it as `--ack <lease>` to your next recv). Until you do, recv hands the same message out again at once instead of waiting. When you wait for an answer from another agent, use `sidera recv --wait 60 --ack <lease>` and read only the new messages. (With the MCP tools, the arguments are `wait_s` and `ack_lease`.)"
    }
  }
}
EOF
```

Then replace the two path placeholders. If the machine has only one of the agents,
delete the other object and the comma that separates them. On Windows (PowerShell),
write the same text with a single-quoted here-string, which does not expand anything,
and `[IO.File]::WriteAllText("$PWD\light.json", $json)` (no byte order mark), where
`$json = @'` starts the text and `'@` on a line of its own ends it; double the
backslashes in `cwd`.

What the `light` preset gives:

- **Claude Code**: `dontAsk` refuses everything that would ask for permission. The
  adapter always allows the Sidera MCP tools and the agent commands of `sidera`
  (`recv`, `ack`, `reply`, `send`, `status`, `control`, `directory`, `peers`, `me`
  and the like), on top of what the owner's Claude Code settings allow. It does not
  allow the owner commands (`preset`, `adapter`, `trust`, `identity`, `daemon` and
  the rest), so a `light` session cannot manage the node. If the owner's own Claude
  settings allow a broader `Bash`, the session gets that too. The agent can read files
  in its project directory; edits and other commands are refused unless the preset's
  `allowed_tools` (for example `["Edit", "Bash(go test *)"]`) or the owner's settings
  allow them. Every turn also denies the session's `Read` and `Edit` tools the node's
  data directory, whatever the preset or those settings allow. That is best effort:
  if the owner's settings allow a `Bash` file command such as `grep` or `find`, the
  session can still read it. A guarantee needs Claude's operating-system sandbox,
  which Sidera does not use yet.
- **Codex**, measured on real machines:
  - It works on macOS and Linux, **through MCP only**. Under `workspace-write` the
    session is offered only the MCP tools; a `sidera` command run in a shell cannot
    reach the daemon and fails with `cannot reach the Sidera daemon at <socket
    path>: permission denied (a sandbox may be blocking it)`. Use the MCP tools
    (`peers`, `directory`, `control`, `recv` with `wait_s` and `ack_lease`, `ack`,
    `reply`).
  - The agent can edit files and commit inside `cwd`, and never waits for a person.
  - There is no network at all: no module downloads, no package installs. A tool
    whose cache lives outside the workspace fails until pointed inside it (for Go,
    set `GOCACHE` to a directory inside the workspace). Only modules already in the
    read-only module cache can be used.
  - It limits writes, and on macOS and Linux Sidera also denies it every read of the
    node's data directory (`~/.sidera` or `SIDERA_HOME`: the identity key and the
    tokens of all sessions); the operating system enforces that. The exceptions are
    the owner's own MCP servers, which Codex starts outside the sandbox, and a command
    the owner approved to run outside it. Files elsewhere outside `cwd` can be read.
  - On Windows it depends on Codex's own sandbox working. Run
    `codex sandbox -- cmd /c echo hi` first; if it fails, tell your owner. With
    `[windows] sandbox = "elevated"` in `~/.codex/config.toml` every shell command
    failed, while `unelevated` worked. That is the owner's Codex setting, not a
    Sidera key; do not change it without asking.
    On Windows the data directory is `%LOCALAPPDATA%\Sidera`, with an access list for
    its owner only, and that location is the main guard, which keeps the sandbox out.
    Codex adds a deny entry for the sandbox's accounts only when a sandboxed Sidera
    session runs a command, and another Codex session can revoke it.
    Codex's elevated sandbox grants its group read access on most first-level folders
    of the user profile; in that location it does not reach Sidera. If a foreign entry
    ever appears, the daemon refuses to start and names it (see the troubleshooting
    entry in the [operations guide](guide/operations.md#troubleshooting)).
  - `sidera adapter list` shows the Codex probe as `pending` and may show it as
    `failed` while a `workspace-write` preset exists, because no command-line front
    end is offered. Sessions still work.

**The management interface trusts the user, not the session.** Any process that runs
as the operating-system user of the node may use the owner commands. Under a
full-access preset (Codex `danger-full-access`, Claude `bypassPermissions`, or Claude
settings that allow all of `Bash`) an agent session can run every owner command and
acts as the local owner. Under `light`, Claude cannot (above), and a Codex
`workspace-write` session has only MCP and cannot reach the daemon socket from its
shell on Linux and macOS. On Windows this was not measured. For that reason the
owner commands are on the "Ask your owner before" list when you run in a session.

Add `"model"` and `"effort"` (`low`, `medium`, `high`, `xhigh`) to the Codex object
to keep the cost down; without them the owner's Codex defaults apply.

If your owner chose full access, add a second preset named `dev`: copy `light.json`
to `dev.json` and set, for Codex, `"sandbox": "danger-full-access"` (keep
`"approval_policy": "never"` and `"approvals": "deny"`), or, for Claude Code,
`"permission_mode": "bypassPermissions"` (in a file with both objects, change both). Tell your owner the risk: any trusted
node in the cluster can then direct the agent here to run anything on this machine,
and the session's Sidera token is visible to everything the agent runs. A full-access
session, of either runtime, acts as the local owner and can read everything, the data
directory included.

Add the presets:

```sh
sidera preset add light --file light.json
sidera preset add dev --file dev.json      # only if the owner chose it
sidera preset list --json
```

Expected from `preset add`: the preset is accepted. Expected from the list: a
`presets` array, under `result` (`--json` prints the whole response, `{"result":{"presets":[...]}}`), with an entry whose `name` is `light` (and `dev` if you added it). If
`preset add` says a name is taken, a preset cannot be edited: `sidera preset remove
light`, then add it again. The default preset `default` that `init` made holds no
agent settings and cannot start a session. Adding a preset restarts the adapters that
run.

## 7. Exchange contact lines with the other machine

1. Show this machine's contact line.

   ```sh
   sidera network show
   ```

   The first line is `<node-id> <multiaddr> <multiaddr>...`. Each address ends in
   `/p2p/<peer-id>`. Some addresses cannot be reached from the other machine: those
   of a VPN or proxy interface, or of a network the other machine is not on. Note
   which ones to leave out; the other machine enters only the addresses it can
   reach. The port in them is the one from step 1.3.

2. Give that line to your owner (or, through the owner, to the agent on the other
   machine) and get the other machine's contact line in return. Do not guess an
   address.

3. Enter the other machine's address:

   ```sh
   sidera network address set <other-node-id> <multiaddr> <multiaddr>
   ```

   Check: `sidera network show` lists `<other-node-id>` under `peers` with exactly
   the addresses you entered. If an address you meant to leave out is listed, run
   `network address set` again with only the right ones (it replaces the list).

4. If the firewall blocks port 7744 (or the port from step 1.3), ask your owner (see
   "Ask your owner before"). Allow the local network only. The commands are, on
   Windows (administrator PowerShell):

   ```powershell
   New-NetFirewallRule -DisplayName "Sidera TCP" -Direction Inbound -Protocol TCP -LocalPort 7744 -Action Allow -Profile Private -RemoteAddress LocalSubnet
   New-NetFirewallRule -DisplayName "Sidera QUIC" -Direction Inbound -Protocol UDP -LocalPort 7744 -Action Allow -Profile Private -RemoteAddress LocalSubnet
   ```

   and on Linux with ufw, for both TCP and UDP (use the address range of the owner's
   network for `<lan-cidr>`, for example `192.168.1.0/24`):
   `sudo ufw allow from <lan-cidr> to any port 7744`. On macOS, allow incoming
   connections for `sidera` if the system asks.

## 8. Pair

Pair only with the node ids your owner named. Both machines must run steps 7 and 8.

1. On each machine, name the other node as expected. An expectation lasts 60
   minutes and is used once, so do it just before the request:

   ```sh
   sidera trust expect <other-node-id>
   ```

2. Agree with the other side which machine sends the request. If one machine
   cannot accept inbound connections, that machine sends it. On the sender:

   ```sh
   sidera trust request <other-node-id> --wait 60s
   ```

   Expected: `Verification code: <code>` (informational when both sides expect each
   other), then `paired with <other-node-id>`. It can take up to a minute.

3. Check on both machines:

   ```sh
   sidera trust list
   sidera network show
   ```

   Expected: `<other-node-id>` under `trusted` in `trust list`.
   `connected` in `network show` can be `false` until one side sends.

If it fails:

- A refusal or a timeout: check that the other side entered `trust expect` for you
  and that its daemon runs. Run `sidera trust list`; an entry under `incoming` or
  `outgoing` has a `pairing_id`. End it with `sidera trust reject <pairing_id>`,
  re-enter both expectations and send the request again.
- `unavailable/node_unreachable`: the address is wrong or the port is blocked.
  Check step 7.
- `backpressure/pairing_paused`: wait the time the message states.

## 9. Back up the self-revocation

```sh
sidera identity export-revocation --out self-revocation.rec
```

Expected: a digest is printed and the file is created. Give the file to your owner
to keep outside this machine; do not leave the only copy here, and do not print its
contents into the conversation. Once the owner confirms it is stored, run:

```sh
sidera identity confirm-backup <digest>
```

If `confirm-backup` is never run, nothing stops working, but `sidera status` keeps
warning about the unconfirmed backup on every call.

## 10. Verify end to end

1. Get the address of the other machine's agent runtime. The console on this
   machine cannot list another node's endpoints (`sidera directory` can, but only
   inside a session or a controller, see Part 2), so read it on the other machine: run
   `sidera endpoint list --instance codex` (or `claude`) there as its owner. It lists
   endpoints, closed ones included: take the `runtime` row whose `STATE` is `open`;
   its address is `<node-id>/<endpoint-id>`. Get that address from the other
   machine's owner or agent.
   In a script, `sidera endpoint list --json` prints
   `{"result":{"endpoints":[...]}}`: the rows are under `result.endpoints`.

2. Start a session there and ask it for something harmless.

   ```sh
   sidera-adapter-console new <node-id>/<endpoint-id> --preset light --body 'Reply with the output of uname -a.'
   ```

   Expected, after 10 to 30 seconds:

   ```
   message: <message-id>
   session: <node-id>/<session-endpoint-id>
   ```

   The `session:` line is the address to use for follow-ups, for example
   `sidera-adapter-console send --to <that address> --body '...'` (`--to` also takes
   a comma-separated list of addresses).

3. Read the reply.

   ```sh
   sidera-adapter-console recv --wait 30
   ```

   Expected: a message from the new session with its answer. The console's `recv`
   acknowledges what it printed. Run it again if you get `no messages` on standard
   error; give it up to a minute. `--wait` accepts at most 30 seconds, so call it
   again for a longer wait.

4. Close the test session when you are done, on the node that hosts it (the
   `<node-id>` of the session address), from outside any session:

   ```sh
   sidera endpoint close <session-address>
   ```

If `new` fails with `native_unavailable`, the daemon on the other machine cannot
find `codex` or `claude` on its `PATH`: restart that daemon with the `PATH` set as in
step 4 (on Windows through the script there). If it fails with `preset_unknown` or
`preset_invalid`, the preset does not exist there under that name or its directory
does not exist.

If you are a controller (a Claude Code or Codex session your owner opened with the
`sidera` MCP server added; see Part 2), you can run the same check with your own tools
instead of the console. Use the MCP tools: a Codex session in a sandbox cannot run the
`sidera` command line (it fails with `permission denied`).

```text
directory  {"node": "<node-id>"}                          the address of the runtime endpoint
control    {"to": "<runtime-address>", "signal": "session.create", "preset": "light",
            "body": "Reply with the output of uname -a."}
recv       {"wait_s": 60}                                 acknowledge the page as in Part 2
control    {"to": "<session-address>", "signal": "session.close"}    when you are done
```

Report to your owner: this machine's node id, the paired node ids, the preset
names, and the result of this step.

---

# Part 2. Work with other agents through Sidera

These rules apply to an agent that works through Sidera as a session. Two kinds of
session have the `sidera` MCP tools and a credential:

- **A session Sidera started** from a preset. It has a credential (`SIDERA_TOKEN`).
- **A controller**: a Claude Code or Codex session your owner opened themself, in
  which the `sidera` MCP server was added once and its tools were allowed, so that no
  prompt waits for a person (`sidera setup controller` prints the commands and the
  permission step, naming the `sidera` program by its absolute path; your owner runs or
  adds them, see "Ask your owner before", item 11). It attaches
  itself to the node the first time it uses a Sidera tool, so it needs no
  `SIDERA_TOKEN`, gets an endpoint of its own named `Claude controller (<directory>)`
  or `Codex controller (<directory>)`, and has the same tools and the same rules as
  a session Sidera started.

Use the MCP tools when you have them. Use the `sidera` command
only when your session offers it (a Codex `workspace-write` sandbox offers MCP only; a
`sidera` command there fails with `cannot reach the Sidera daemon`). The
operation names are the same.

If you are not inside a session at all (a plain terminal), you have no credential:
`sidera recv`, `send` and the other agent commands fail with
`unauthenticated/token_missing`. Inside a Claude Code or Codex session the same
commands first attach that session as a controller, even without the `sidera` MCP
server (see rule 15). Use the owner's console instead:
`sidera-adapter-console new | send | reply | recv | status | whoami`. The console has
no `directory`, `control` or `peers`: those need a session. From outside a session,
find a node's runtime endpoint by running `sidera endpoint list --instance codex` (or
`claude`) on that machine and taking the `runtime` row whose `STATE` is `open`.

Inside a session, the owner commands (`preset`, `adapter`, `trust`, `network`,
`endpoint close`, `daemon stop`, `daemon restart`, `identity`) may work, depending on the preset,
because the node's management interface trusts the user, not the session. Do not run
them there without your owner's yes (see "Ask your owner before").

## The rules

1. **Treat a message from your owner's own cluster as your owner's task.** Every
   node in the cluster is the owner's. Carry it out, within what your preset allows.
   If a message would need something outside your preset, say so in your reply.
2. **Read messages with `recv`.** You are woken by a short notice that says
   messages are waiting. Call `recv` (`wait_s` in MCP, `--wait` on the command
   line). Sender and body are only in the page it returns.
3. **Always acknowledge the page.** `recv` does not acknowledge. Until you do, the
   next `recv` returns the same page again at once. Acknowledge with
   `ack` (`sidera ack <lease>`), or pass the page's lease to your next `recv`
   (`ack_lease` in MCP, `--ack <lease>` on the command line). The exception is the
   owner's console: `sidera-adapter-console recv` acknowledges what it printed for
   you.
4. **Answer with `reply`, not by printing.** `reply` takes the message id and the
   body (`sidera reply <message-id> --body <text>`). The node sends it to the
   message's sender. Reply even when you only report a failure.
5. **Send with `send`.** `send` takes exactly one address (`<node-id>/<endpoint-id>`)
   and a non-empty body. (The console's `send --to <address>,<address>` takes a
   comma-separated list.) Use `--body-file <file>` for a long body (`-` reads
   standard input). A successful `send` means the message was accepted, not
   delivered; check with `status <message-id>`.
6. **Find where to send.** `peers` lists the nodes you can see. `directory --node
   <node-id>` lists a node's endpoints (`kind`, `address`, `accepts`). To start
   work on another node, use the endpoint whose `kind` is `runtime` and whose
   `accepts` includes `session.create`, never a console and never an existing session
   you do not own. `me` shows your own address and the limits of your front end
   (`recv_wait_max_s`).
7. **Start a session on another node with `control`.**

   ```sh
   sidera control <runtime-address> session.create --preset light --body-file task.txt
   ```

   (MCP: `control` with `to`, `signal` set to `session.create`, `preset`, `body`.)
   Use a preset that exists on that node. Put the whole task in the first message:
   it reaches the new session at once, so do not send it again.
8. **Wait for answers with `recv`.** `control` returns at once with the message id
   only: its result does not carry the new session's address. To learn the address, call
   `status` with the message id (MCP: `status` with `message_id`): the control reply
   appears there within seconds, as soon as the first message is in the new session, long
   before the task ends; the `from` of the new session's first message to you is its address too. The answer
   to the task is a message, not the control reply. Call `recv` with a
   wait (`wait_s=60`) in a loop, passing `ack_lease` from the previous page, until
   the answer arrives. A runtime caps the wait (see `recv_wait_max_s` in `me`), so
   `recv` can return early with no messages; call it again.
9. **Name arguments exactly.** In MCP `recv` takes `wait_s` (an integer), `ack_lease`,
   `msg` and `part`. An unknown argument is refused with an error that lists the
   accepted names; read it and correct the call. An empty page after `wait_s=60`
   should take about a minute; if it returns at once, the arguments are wrong.
10. **A long message comes in parts.** When a page has `"next":{"msg":"<id>","part":<k>}`,
    fetch the rest with `recv` (`msg` and `part`) and acknowledge each page. A
    message marked `possibly_duplicate` was handed out before; handle it once.
11. **Tell sessions you create how to answer you.** A new session replies to the
    sender of its first message with `reply`. Its preset already carries the
    instructions to do so; ask for a short, concrete answer in the task text.
12. **Close only what you created.** `control <session-address> session.close` closes
    a session you started when it is done. From outside a session, run
    `sidera endpoint close <session-address>` on the node that hosts it. Ask your
    owner before closing any other.
13. **Do not wait forever.** If a node does not answer after several `recv` rounds,
    check `status <message-id>` for the delivery state and `peers` for `connected`,
    and tell your owner what you found.
14. **Never send secrets** (tokens, keys, `SIDERA_TOKEN`, the contents of
    the data directory) in a message. Under a full-access preset you can read them, and
    under a Claude Code `light` preset a `Bash` file command that your owner's settings
    allow may; that is not permission to send them.
15. **If you are a controller, send with your own tools and you are woken.**
    Send and start sessions with `send` and `control`, not with
    `sidera-adapter-console`: the console sends from the owner's endpoint, so the
    replies would go to the owner's inbox and not to you. A reply to a message you
    sent wakes you with a short notice that messages are waiting (a Claude Code
    controller within about a second, a Codex controller within about 10 seconds or
    when its running turn ends); you can end your turn after sending and
    handle the answer when the notice arrives. The node paces its wake-ups: a reply
    that arrives within 2 minutes of your last wake-up waits for the rest of that
    interval, unless you read it in the turn that wake-up started. Do not start a
    background `recv` loop. Then:
    - **Read promptly.** A message nobody reads within the endpoint's queue time (30
      minutes by default) is rejected back to its sender as `backlog_expired`: the
      work is done and the report is lost.
    - **If the node cannot wake you,** the first result of your first Sidera tool
      call says so, with the reason. Then nothing interrupts you: call `recv` with a
      wait inside a turn (`wait_s=60`, rule 8) until the answers arrive. If you use
      only `sidera` commands and no MCP tool, every command attaches you again with the
      wake information, so you can be woken, but nothing repeats it between commands:
      your endpoint closes a queue time after your last command when nothing is waiting
      on it, and on Claude Code the wake information is lost when the node restarts
      until your next command.
    - **Know the limits.** A Codex controller is not woken after the owner presses
      Esc until their next turn completes, and an active `/goal` can delay the
      wake-up. A Claude Code controller in bypass-permissions mode may hold a wake-up
      for approval.
    - **The endpoint closes after you are gone.** The `sidera` MCP server tells the
      node every minute that you are alive. After the session has been gone for the
      queue time (never less than 3 minutes) with nothing waiting on the endpoint, the
      node closes it; a session resumed later attaches again. A resumed Claude Code
      session is woken at once for what waited; a resumed Codex session, at its first
      Sidera tool call; in both cases without the wait that follows a failed wake-up.
      If your endpoint is closed while you run, your next tool call attaches you again
      with a new endpoint, and its result names the new address and says the old one is
      closed: tell the peers that wrote to the old address.

## An example, step by step

Your owner asks: "Ask the agent on node `<node-id>` to compute 37*41+11."

1. `sidera directory --node <node-id>`: take the address of the `runtime` endpoint
   that accepts `session.create`.
2. `sidera control <address> session.create --preset light --body 'Compute 37*41+11 and reply to me with only the number, using sidera reply.'`
3. `sidera recv --wait 60` and read the page; `sidera recv --wait 60 --ack <lease>`
   until the number arrives (this acknowledges the earlier page too).
4. `sidera reply <owner-message-id> --body '1528'`, naming the result.

## More

- [User guide](guide/README.md): install, getting started, working with agents,
  operations.
- [Engineering references](references/README.md): the exact behavior of every
  command and protocol.
