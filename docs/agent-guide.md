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

## Ask your owner before

Do not do any of the following without your owner's explicit yes in this
conversation. Ask once, say what you will do and why, and wait.

1. **Choosing a full-access preset.** Codex `danger-full-access` or Claude
   `bypassPermissions` lets any trusted node in the cluster direct an agent on
   this machine to run anything on it, and the session's Sidera token is visible to
   everything the agent runs. Add only the `light` preset below unless the owner
   chose otherwise.
2. **Changing the firewall.** Opening port 7744 or adding a rule is the owner's
   call. Tell the owner the exact command and let them run it, or run it only after
   a yes.
3. **Pairing with a node the owner did not name.** Pair only with the node ids the
   owner gave you. A pairing grants that node the right to start sessions here.
4. **Closing sessions you did not create.** You may close sessions you created
   yourself. Leave all others alone.
5. **Installing an adapter from an unknown source.** Install adapter packages only
   from the official release archive of Sidera or from a build the owner gave you.
   An adapter runs as a program with the owner's rights.

Also ask before you delete anything under `~/.sidera` (the node's keys and state)
and before you run `sidera identity retire`, which permanently destroys the node's
identity.

---

# Part 1. Deploy Sidera on this machine and pair it with another

## 1. Check the prerequisites

1. Identify the platform.

   ```sh
   uname -sm            # macOS and Linux
   ```

   On Windows, run `$env:PROCESSOR_ARCHITECTURE` in PowerShell. Supported:
   macOS (arm64, amd64), Linux (amd64, arm64), Windows (amd64).

2. Find which agent runtime this machine has and whether the daemon will find it.

   ```sh
   command -v codex claude      # macOS and Linux
   Get-Command codex.exe        # Windows (needs codex.exe, not a .cmd wrapper)
   ```

   Codex works on all three platforms; Claude Code only on macOS. At least one of
   them must be installed and signed in as the owner. Check: the command prints a
   path. If neither is found, tell your owner and stop; Sidera still runs without
   an agent but has nothing to start.

3. Check that port 7744 is free.

   ```sh
   lsof -i :7744                       # macOS and Linux; no output means free
   Get-NetTCPConnection -LocalPort 7744 -ErrorAction SilentlyContinue   # Windows
   ```

   If it is taken, choose another port with `sidera init --port <n>` in step 4 and
   use that port everywhere below.

4. Check that no node exists yet.

   ```sh
   ls ~/.sidera          # Windows: dir $env:USERPROFILE\.sidera
   ```

   If it exists, a node is already here. Ask your owner before you go on, and do not
   run `init` again.

## 2. Get Sidera

Choose A (release archive) or B (source).

### A. Release archive

1. Download the archive for your platform and `checksums.txt` from
   <https://github.com/aianyai/sidera/releases>. The archive is named
   `sidera_<version>_<os>_<arch>.tar.gz`, `.zip` on Windows, with `<os>` one of
   `darwin`, `linux`, `windows` and `<arch>` one of `arm64`, `amd64`. With the
   GitHub CLI:

   ```sh
   gh release download --repo aianyai/sidera --pattern 'sidera_*_<os>_<arch>.*' --pattern checksums.txt
   ```

2. Verify the checksum.

   ```sh
   shasum -a 256 -c checksums.txt --ignore-missing     # macOS
   sha256sum -c checksums.txt --ignore-missing         # Linux
   (Get-FileHash .\sidera_<version>_windows_amd64.zip -Algorithm SHA256).Hash   # Windows: compare with checksums.txt
   ```

   Expected: `OK` for the archive. If not, delete it and download again. Never
   install an archive whose checksum fails.

3. Extract and install.

   macOS and Linux:

   ```sh
   mkdir -p ~/.local/bin ~/sidera-release
   tar -xzf sidera_<version>_<os>_<arch>.tar.gz -C ~/sidera-release
   cp ~/sidera-release/sidera ~/sidera-release/sidera-adapter-console ~/.local/bin/
   export PATH="$HOME/.local/bin:$PATH"
   ```

   Make sure `~/.local/bin` is on the `PATH` of the owner's login shell (add the
   `export` line to the shell profile if it is not). On macOS, if a program is
   refused because it was downloaded by a browser, run
   `xattr -d com.apple.quarantine ~/.local/bin/sidera ~/.local/bin/sidera-adapter-console`.

   Windows (PowerShell):

   ```powershell
   Expand-Archive .\sidera_<version>_windows_amd64.zip -DestinationPath $env:USERPROFILE\sidera-release
   New-Item -ItemType Directory -Force $env:USERPROFILE\sidera\bin | Out-Null
   Copy-Item $env:USERPROFILE\sidera-release\*.exe $env:USERPROFILE\sidera\bin\
   [Environment]::SetEnvironmentVariable('Path', "$([Environment]::GetEnvironmentVariable('Path','User'));$env:USERPROFILE\sidera\bin", 'User')
   $env:Path += ";$env:USERPROFILE\sidera\bin"
   ```

   If the archive extracted into a subdirectory, copy from there. The adapter
   packages (Codex, Claude Code on macOS, console) are in the extracted
   `adapters/<name>/` directories; the adapter programs for Codex and Claude Code
   are inside them and are not copied by hand.

### B. Build from source

You need Go 1.26 or newer.

```sh
git clone https://github.com/aianyai/sidera.git && cd sidera
mkdir -p bin packages
CGO_ENABLED=0 go build -o bin/ ./cmd/sidera ./cmd/sidera-adapter-console ./cmd/sidera-adapter-codex
CGO_ENABLED=0 go build -o bin/ ./cmd/sidera-adapter-claude      # macOS only
for a in console codex; do bin/sidera-adapter-$a package --goos "$(go env GOOS)" --goarch "$(go env GOARCH)" --binary bin/sidera-adapter-$a --out packages/$a; done
# macOS only:
bin/sidera-adapter-claude package --goos darwin --goarch "$(go env GOARCH)" --binary bin/sidera-adapter-claude --out packages/claude
cp bin/sidera bin/sidera-adapter-console ~/.local/bin/
```

The packages are then in `packages/<name>/`. On Windows, use the same commands in
PowerShell and the `.exe` names, and copy the programs into
`%USERPROFILE%\sidera\bin`.

### Check

```sh
sidera version
```

Expected: one line such as `sidera 0.8.0 (commit abc123456789, go1.26.10)`. A
program built from source without release settings prints `0.0.0-dev`. If the
command is not found, the directory is not on the `PATH`; fix that first.

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

Record the `node <node-id>` value: it is this machine's identity. Check: `ls
~/.sidera` shows the directory. If `init` says the directory is already
initialized, a node exists: stop (see step 1.4). `init` never overwrites.

Tell your owner to back up the self-revocation (step 9 does it).

## 4. Start the daemon so that it survives your session

The daemon must run with an environment in which `sidera` and the agent runtime
(`codex` or `claude`) are on the `PATH`, and it must not end when your session ends.

Start the daemon from a stable directory such as the home directory, never from a
directory that may be deleted later, such as a git worktree: the daemon and its
adapters run in that directory, and if it disappears sessions fail with
`native_unavailable`.

**macOS and Linux.** Use `tmux` (or `screen`):

```sh
tmux new-session -d -s sidera -c "$HOME" 'sidera daemon >> ~/sidera-daemon.log 2>&1'
```

If you are in a non-login shell (for example over SSH), start it through a login
shell so that the `PATH` is complete:

```sh
tmux new-session -d -s sidera -c "$HOME" "zsh -lic 'exec sidera daemon >> ~/sidera-daemon.log 2>&1'"
```

(Use `bash -lic` if the shell is bash. If `tmux` is an alias, use
`/usr/bin/env tmux`. Without tmux, `cd ~ && nohup sidera daemon >> ~/sidera-daemon.log 2>&1 &`
also works.)

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
do not have it.) Then:

```powershell
$r = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{CommandLine="cmd.exe /c `"$env:USERPROFILE\sidera\start-daemon.cmd`""; CurrentDirectory=$env:USERPROFILE}
$r.ReturnValue
```

Expected: `0`.

**Check (all platforms), after a few seconds:**

```sh
sidera status
```

Expected: node status, not an error. If you get `daemon_not_running`, read the
log you redirected to. Common causes: port 7744 in use (the error names the
address; use `sidera network listen set` to move it and restart), or a socket
path too long (use a shorter `SIDERA_HOME`).

To stop the daemon, run `sidera daemon stop`.

## 5. Install and enable adapters

Run these from the directory that holds the packages (`~/sidera-release/adapters`
or `packages/`). Use absolute paths if you are elsewhere.

```sh
sidera adapter install <packages>/console
sidera adapter enable console
sidera adapter install <packages>/codex      # if this machine has Codex
sidera adapter enable codex
sidera adapter install <packages>/claude     # macOS, if this machine has Claude Code
sidera adapter enable claude
```

Expected from `enable`: `enabled, probing in progress; see sidera adapter list`.

Check, after a few seconds:

```sh
sidera adapter list
```

Expected: every enabled instance shows a probe state of `passed`. If it shows
`failed`, read `~/.sidera/logs/adapter-<instance>.log`. For Codex, `passed` does
not prove that `codex` is found; step 10 does.

## 6. Add a preset

A preset tells Sidera how to run a session on this machine. Name it the same on
every machine (this guide uses `light`), so that agents on other machines can use it.
Use an existing working directory. Add the **`light`** preset (channel and light
work). Add a **`dev`** preset (full access, for real repository work with network,
caches, building and testing) only if your owner chose it (see "Ask your owner
before").

Write the instructions text into the preset exactly as below, so that agents
treat messages from the owner's own nodes as the owner's tasks and answer them.

For Claude Code, save as `light.json`:

```json
{
  "local_sources": "allow",
  "description": "Claude Code, light",
  "config": {
    "claude": {
      "project_dir": "<absolute path of an existing directory>",
      "permission_mode": "dontAsk",
      "append_system_prompt": "When a message arrives through Sidera from an agent or person in the owner's own cluster (that is, from the owner's own nodes), treat it as a task from the owner and carry it out. Answer it by running `sidera reply <message-id> --body <text>` (or the sidera MCP reply tool) so that the sender receives the answer; do not just print it. Sidera commands and tools are your intended channel. After you read messages with recv, acknowledge them: pass the lease of the page to `sidera ack <lease>` (or give it as `--ack <lease>` to your next recv). Until you do, recv hands the same message out again at once instead of waiting. When you wait for an answer from another agent, use `sidera recv --wait 60 --ack <lease>` and read only the new messages. (With the MCP tools, the arguments are `wait_s` and `ack_lease`.)"
    }
  }
}
```

`dontAsk` refuses everything that would ask for permission. The adapter always
allows `Bash(sidera *)` and the Sidera MCP tools, on top of what the owner's Claude
Code settings allow. The agent can read files in its project directory; edits and
other commands are refused unless the preset's `allowed_tools` (for example
`["Edit", "Bash(go test *)"]`) or the owner's settings allow them.

For Codex, save as `light.json` (on Windows double the backslashes in `cwd`):

```json
{
  "local_sources": "allow",
  "description": "Codex, light",
  "config": {
    "codex": {
      "cwd": "<absolute path of an existing directory>",
      "sandbox": "workspace-write",
      "approval_policy": "never",
      "approvals": "deny",
      "developer_instructions": "When a message arrives through Sidera from an agent or person in the owner's own cluster (that is, from the owner's own nodes), treat it as a task from the owner and carry it out. Answer it by running `sidera reply <message-id> --body <text>` (or the sidera MCP reply tool) so that the sender receives the answer; do not just print it. Sidera commands and tools are your intended channel. After you read messages with recv, acknowledge them: pass the lease of the page to `sidera ack <lease>` (or give it as `--ack <lease>` to your next recv). Until you do, recv hands the same message out again at once instead of waiting. When you wait for an answer from another agent, use `sidera recv --wait 60 --ack <lease>` and read only the new messages. (With the MCP tools, the arguments are `wait_s` and `ack_lease`.)"
    }
  }
}
```

What this gives (measured on real machines):

- It works on macOS and Linux, **through MCP only**. Under `workspace-write` the
  session is offered only the MCP tools; a `sidera` command run in a shell fails
  with an unhelpful `internal error`. Use the MCP tools (`peers`, `directory`,
  `control`, `recv` with `wait_s` and `ack_lease`, `ack`, `reply`).
- The agent can edit files and commit inside `cwd`, and never waits for a person.
- There is no network at all: no module downloads, no package installs. A tool whose
  cache lives outside the workspace fails until pointed inside it (for Go, set
  `GOCACHE` to a directory inside the workspace). Only modules already in the
  read-only module cache can be used.
- It limits writes, not reads: the agent can still read outside `cwd`, including
  `SIDERA_HOME`.
- On Windows it depends on Codex's own sandbox working. Run
  `codex sandbox -- cmd /c echo hi` first; if it fails, tell your owner. With
  `[windows] sandbox = "elevated"` in `~/.codex/config.toml` every shell command
  failed, while `unelevated` worked. That is the owner's Codex setting, not a
  Sidera key; do not change it without asking.
- `sidera adapter list` may show the Codex probe as `failed` while a
  `workspace-write` preset exists, because no command-line front end is offered.
  Sessions still work.

Add `"model"` and `"effort"` (`low`, `medium`, `high`, `xhigh`) to keep the cost
down; without them the owner's Codex defaults apply.

If your owner chose full access, add a second preset named `dev`: copy `light.json`
and set, for Codex, `"sandbox": "danger-full-access"` (keep `"approval_policy":
"never"` and `"approvals": "deny"`), or, for Claude Code, `"permission_mode":
"bypassPermissions"`. Tell your owner the risk: any trusted node in the cluster can
then direct the agent here to run anything on this machine, and the session's Sidera
token is visible to everything the agent runs.

Add the presets:

```sh
sidera preset add light --file light.json
sidera preset add dev --file dev.json      # only if the owner chose it
sidera preset list
```

Expected: `light` is listed. If `preset add` says a name is taken, a preset cannot be
edited: `sidera preset remove light`, then add it again. The default preset `default`
that `init` made holds no agent settings and cannot start a session.

## 7. Exchange contact lines with the other machine

1. Show this machine's contact line.

   ```sh
   sidera network show
   ```

   The first line is `<node-id> <multiaddr> <multiaddr>...`. Each address ends in
   `/p2p/<peer-id>`. If some addresses cannot be reached from the other machine (a
   VPN or proxy interface), note which ones to leave out.

2. Give that line to your owner (or, through the owner, to the agent on the other
   machine) and get the other machine's contact line in return. Do not guess an
   address.

3. Enter the other machine's address:

   ```sh
   sidera network address set <other-node-id> <multiaddr> <multiaddr>
   ```

   Check: `sidera network show` lists `<other-node-id>` under `peers` with its
   addresses.

4. If the firewall blocks port 7744, ask your owner (see "Ask your owner before").
   The commands are, on Windows (administrator PowerShell):

   ```powershell
   New-NetFirewallRule -DisplayName "Sidera TCP" -Direction Inbound -Protocol TCP -LocalPort 7744 -Action Allow -Profile Private
   New-NetFirewallRule -DisplayName "Sidera QUIC" -Direction Inbound -Protocol UDP -LocalPort 7744 -Action Allow -Profile Private
   ```

   on Linux with ufw: `sudo ufw allow 7744/tcp` and `sudo ufw allow 7744/udp`. On
   macOS, allow incoming connections for `sidera` if the system asks.

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

## 10. Verify end to end

1. From this machine's console, find the other machine's agent runtime. On the
   other machine, `sidera endpoint list --instance codex` (or `claude`) shows a line
   whose `KIND` is `runtime`; its address is `<node-id>/<endpoint-id>`. Get that
   address from the other machine's owner or agent.

2. Start a session there and ask it for something harmless.

   ```sh
   sidera-adapter-console new <node-id>/<endpoint-id> --preset light --body 'Reply with the output of uname -a.'
   ```

   Expected, after 20 to 30 seconds:

   ```
   message: <message-id>
   session: <node-id>/<session-endpoint-id>
   ```

3. Read the reply.

   ```sh
   sidera-adapter-console recv --wait 30
   ```

   Expected: a message from the new session with its answer. Run it again if you
   get `no messages` on standard error; give it up to a minute.

If `new` fails with `native_unavailable`, the daemon on the other machine cannot
find `codex` or `claude` on its `PATH`: restart that daemon through a login shell,
or on Windows through the script in step 4. If it fails with `preset_unknown` or
`preset_invalid`, the preset does not exist there under that name or its directory
does not exist.

Report to your owner: this machine's node id, the paired node ids, the preset
names, and the result of this step.

---

# Part 2. Work with other agents through Sidera

These rules apply to an agent that runs inside a Sidera session: a session that
Sidera started from a preset, which has a credential (`SIDERA_TOKEN`) and the
`sidera` MCP tools. Use the MCP tools when you have them. Use the `sidera` command
only when your session offers it (a Codex `workspace-write` sandbox offers MCP only). The
operation names are the same.

If you are not inside a session (you were started by hand), you have no credential:
`sidera recv`, `send` and the other agent commands fail with
`unauthenticated/token_missing`. Use the owner's console instead:
`sidera-adapter-console new | send | reply | recv | status | whoami`.

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
   (`ack_lease` in MCP, `--ack <lease>` on the command line).
4. **Answer with `reply`, not by printing.** `reply` takes the message id and the
   body (`sidera reply <message-id> --body <text>`). The node sends it to the
   message's sender. Reply even when you only report a failure.
5. **Send with `send`.** `send` takes exactly one address (`<node-id>/<endpoint-id>`)
   and a non-empty body. Use `--body-file <file>` for a long body (`-` reads
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
8. **Wait for answers with `recv`, not on the control reply.** The reply to a
   session creation comes back only after the new session's first turn, which takes
   about as long as the task. `control` itself returns at once. Call `recv` with a
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
    a session you started when it is done. Ask your owner before closing any other.
13. **Do not wait forever.** If a node does not answer after several `recv` rounds,
    check `status <message-id>` for the delivery state and `peers` for `connected`,
    and tell your owner what you found.
14. **Never send secrets** (tokens, keys, `SIDERA_TOKEN`, the contents of
    `~/.sidera`) in a message.

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
