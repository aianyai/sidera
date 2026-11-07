# Install and run Sidera

This page covers what you need before Sidera, how to install it from a release
archive or from source, and how to keep the daemon running. After it, continue
with [Getting started](getting-started.md).

## Supported platforms

| Platform | Architectures | Agents Sidera can drive |
| -------- | ------------- | ----------------------- |
| macOS | arm64, amd64 | Claude Code, Codex |
| Linux | amd64, arm64 | Claude Code, Codex |
| Windows | amd64, arm64 | Claude Code, Codex |

A Claude Code turn outlives the daemon's restart on all three platforms, unless the
host forbids it, which the adapter logs. On Linux it is started in a systemd user
scope when that keeps it alive (the user manager is usable and lingering is on,
or the daemon already runs under the user manager), and otherwise in a session
of its own; without a usable user bus it is only in a session of its own, and a
node that must run with nobody logged in needs `loginctl enable-linger`. On
Windows the turn leaves the daemon's job object; if the daemon runs in a job
that forbids breakaway and kills its processes when it closes (some service
wrappers and launchers do), the turn ends with the daemon and the adapter logs a
warning saying so.

On Windows, if Claude Code
cannot find Git Bash in a standard place, set `CLAUDE_CODE_GIT_BASH_PATH` in the
daemon's environment: it is the one `CLAUDE_CODE_*` variable passed on to the
turns. A Windows on Arm machine
takes the arm64 archive (the commands below name the amd64 one); Settings >
System > About shows it as "System type", or in PowerShell
`(Get-CimInstance Win32_Processor).Architecture` is 12 for ARM64.

Sidera does not install the agents themselves. To drive one, install it first
and log in once as yourself:

- **Claude Code**: the `claude` command must be on the `PATH` of the daemon and
  signed in. On Windows the daemon needs the native installer's `claude.exe`, not
  the npm `claude.cmd` wrapper, which runs through `cmd.exe`: the probe reports
  `claude` as missing and no session can be created with it.
- **Codex**: the `codex` command must be on the `PATH` of the daemon and signed
  in. On Windows the daemon needs `codex.exe`, not a `.cmd` wrapper. Sidera runs
  Codex through its App Server, an experimental interface; the adapter was
  exercised with Codex 0.164 to 0.166, and a much newer release may change it.

## Install from a release archive

Releases are at <https://github.com/aianyai/sidera/releases>. Each release has
one archive per platform, named `sidera_0.16.0_<os>_<arch>.tar.gz`
(`.zip` on Windows), where `<os>` is `darwin`, `linux` or `windows` and
`<arch>` is `arm64` or `amd64`, a `checksums.txt` for all of them and, from
version 0.11.0, its signature `checksums.txt.minisig`.

An archive holds:

| Path | What it is |
| ---- | ---------- |
| `sidera` | The node program: the daemon and every command. |
| `sidera-adapter-console` | The console bridge, and the owner's command-line client: the way to start a first session and read replies. Keep it on your `PATH`. |
| `adapters/codex/` | The ready-to-install package of the Codex adapter. |
| `adapters/claude/` | The package of the Claude Code adapter. |
| `adapters/console/` | The package of the console bridge. |
| `adapters/hermes/` | The package of the Hermes adapter. |
| `LICENSE`, `THIRD_PARTY_NOTICES`, `README.txt` | The license, the notices of bundled software and a short note. |

The adapter programs for Codex, Claude Code and Hermes are inside their packages; you
install a package with `sidera adapter install <directory>/adapters/<name>` and do
not copy those programs yourself. On Windows the programs end in `.exe`. The
release notes are on the release page, not in the archive.

### Verify the download

Compare the archive's SHA-256 digest with its line in `checksums.txt`.

macOS:

```sh
shasum -a 256 -c checksums.txt --ignore-missing
```

Linux:

```sh
sha256sum -c checksums.txt --ignore-missing
```

Windows (PowerShell): print the digest and compare it by eye with the line in
`checksums.txt`.

```powershell
(Get-FileHash .\sidera_0.16.0_windows_amd64.zip -Algorithm SHA256).Hash
```

Each command must report `OK` or an equal digest. If it does not, delete the
file and download it again.

### Verify the signature (optional)

A checksum shows that the archive is the one listed in `checksums.txt`; the
signature shows that the list is the release's. Releases from 0.11.0 sign
`checksums.txt` with [minisign](https://jedisct1.github.io/minisign/); releases up
to 0.10.0 carry no signature. Download `checksums.txt.minisig` beside
`checksums.txt` and run, with the public key below:

```sh
minisign -Vm checksums.txt -P RWR7j5o6zjWTutDvAgBfSyNd7aQXb7k9lN1L4iMavn6zZUtYxLJT+T1L
```

Success prints `Signature and comment signature verified` and a trusted comment
`sidera <version> update-from <oldest version that may update to it>`, and the
exit status is 0. Anything else (`Signature verification failed`, a different key
id) means the file is not the release's: delete it. The key is also the file
[`internal/release/minisign.pub`](../../internal/release/minisign.pub) (key id
`BA9335CE3A9A8F7B`) of this repository.

With the GitHub CLI you can also check where an archive was built; this needs no
key:

```sh
gh attestation verify sidera_<version>_<os>_<arch>.tar.gz -R aianyai/sidera
```

### Put the programs on your PATH

Pick a directory that is on the `PATH` of your login shell. The daemon starts
the adapters, and the adapters start `sidera` and the agent, so all of them are
found by name.

macOS and Linux:

```sh
mkdir -p ~/.local/bin ~/sidera-release
tar -xzf sidera_0.16.0_<os>_<arch>.tar.gz -C ~/sidera-release
cp ~/sidera-release/sidera ~/sidera-release/sidera-adapter-console ~/.local/bin/
```

Make sure `~/.local/bin` is on the `PATH` of your login shell: add
`export PATH="$HOME/.local/bin:$PATH"` to the file that shell reads (`~/.zprofile`
for zsh, `~/.bash_profile` or `~/.profile` for bash) if it is not there. Then check
it in a fresh login shell:

```sh
"$SHELL" -l -c 'command -v sidera'
```

It must print the path of the binary you just installed (`~/.local/bin/sidera`
expanded), not another one and not nothing.

Keep the extracted `~/sidera-release/adapters/` directory: you install the adapter
packages from it in [Getting started](getting-started.md#4-install-and-enable-adapters-both).

If macOS refuses to run a program that a browser downloaded, remove its
quarantine mark:

```sh
xattr -d com.apple.quarantine ~/.local/bin/sidera ~/.local/bin/sidera-adapter-console
```

Windows (PowerShell):

```powershell
Expand-Archive .\sidera_0.16.0_windows_amd64.zip -DestinationPath $env:USERPROFILE\sidera-release
New-Item -ItemType Directory -Force $env:USERPROFILE\sidera\bin | Out-Null
Copy-Item $env:USERPROFILE\sidera-release\*.exe $env:USERPROFILE\sidera\bin\
# Add the directory to the user PATH (open a new terminal afterwards).
[Environment]::SetEnvironmentVariable('Path', "$([Environment]::GetEnvironmentVariable('Path','User'));$env:USERPROFILE\sidera\bin", 'User')
```

This changes the user `PATH` for good. In a new terminal, check that
`(Get-Command sidera).Source` prints `sidera.exe` under `%USERPROFILE%\sidera\bin`.
The adapter packages are then in `$env:USERPROFILE\sidera-release\adapters\`. If the
archive extracts into a subdirectory, adjust the `Copy-Item` path.

### Check the installation

```sh
sidera version
```

It prints one line with the version, the commit the program was built from and the
Go version that built it, for example `sidera 0.16.0 (commit abc123456789, go1.26.10)`.
`sidera version --json` prints the same as JSON. Every node in a cluster should
run the same release. A program built from source without release settings reports
`0.0.0-dev`.

### Upgrade

A node installed from a release archive is upgraded with `sidera update`: it fetches the
latest release, checks its signature against the key built into the program, replaces the
programs next to the running one, restarts the running daemon (`sidera daemon restart`),
installs the new adapter packages, asking first, and refreshes what `sidera adapter setup`
put in your Claude Code, Codex or Hermes. `sidera update --check` only reports.
`sidera version --json` says `"distribution":"archive"` for a program it can replace; a
program built from source is rebuilt, and a release before 0.12.0 has no `update` and is
installed once by hand. After the upgrade to 0.15, the first release with a client setup,
run `sidera adapter setup <instance>` once for each Claude Code, Codex and Hermes
instance: until then the update has nothing recorded to refresh. The procedure, the
refusals and how to go back are in [Upgrade](operations.md#upgrade); what the setup does is
in [Setting up an adapter's client](adapters.md#setting-up-an-adapters-client).

## Build from source

You need Go 1.26 or newer and the repository checked out.

```sh
git clone https://github.com/aianyai/sidera.git
cd sidera
mkdir -p bin
CGO_ENABLED=0 go build -o bin/ ./cmd/sidera ./cmd/sidera-adapter-console ./cmd/sidera-adapter-codex ./cmd/sidera-adapter-claude ./cmd/sidera-adapter-hermes
```

Then make the adapter packages that `sidera adapter install` reads. Each adapter
program has a `package` command for this:

```sh
for a in console codex claude hermes; do
  bin/sidera-adapter-$a package --goos "$(go env GOOS)" --goarch "$(go env GOARCH)" \
    --binary bin/sidera-adapter-$a --out packages/$a
done
```

`--out` must name a directory that does not exist or is empty. Copy `sidera` and
`sidera-adapter-console` from `bin/` to a directory on your `PATH`, as above;
`sidera-adapter-console` is also the client you run from a terminal.

## Run the daemon

Every node runs one daemon. Create the node's data directory once, then start the
daemon (see [Getting started](getting-started.md#1-create-the-node-both)):

```sh
sidera init
sidera daemon
```

`sidera daemon` runs in the foreground and stops with Ctrl-C or
`sidera daemon stop`; `sidera daemon restart` starts it again in place. The node's own log is
in `~/.sidera/logs/` (on Windows `%LOCALAPPDATA%\Sidera\logs\`). Sidera does not start itself at boot and does not install a
background service yet; the options below keep it running after you close the
terminal. Stopping the daemon ends the Codex App Server of the Codex adapter unless a
Codex session is running a turn in it; see [Stop the daemon](operations.md#stop-the-daemon-and-the-codex-app-server).

Start the daemon from your home directory, not from a directory that you may delete
later, such as a git worktree.

The daemon must be started with an environment in which `sidera` and the agent
you want to drive (`claude` or `codex`) are on the `PATH`. Start it from a login
shell, or set the `PATH` yourself.

Every `sidera` and `sidera-adapter-console` command, and the daemon, uses the data
directory named by `--home`, then by `SIDERA_HOME`, then `~/.sidera` (on Windows
`%LOCALAPPDATA%\Sidera`). A second node
on the same machine needs its own data directory and its own port
(`SIDERA_HOME=<directory> sidera init --port <n>`), and every command for it needs
the same `SIDERA_HOME`. It also needs a tmux session name and a log file of its own
below (for example `sidera-2` and `~/sidera-2-daemon.log`), or on Windows its own
script and log names plus a `set "SIDERA_HOME=<directory>"` line in the script, so
that it does not collide with the first node's.

### macOS and Linux: a terminal multiplexer

`tmux` keeps the daemon alive when you log out or the SSH connection drops.

```sh
tmux new-session -d -s sidera -c "$HOME" -e PATH="$PATH" 'sidera daemon >> ~/sidera-daemon.log 2>&1'
tmux ls                  # the session `sidera` is listed
sidera daemon stop       # stops the daemon; the tmux session ends with it
```

A tmux session starts with the environment of the tmux server, not of the shell you
type in, and the server may be much older than your shell. `-e PATH="$PATH"` hands
the session your shell's `PATH` (tmux 3.2 or newer). Run the command in a shell
where `command -v sidera` and the agent (`command -v claude codex`) print a path.
When the node uses a data directory other than the default, add
`-e SIDERA_HOME=<directory>` as well.

Check with `tmux ls` right away and again after a few seconds. If the session
`sidera` is gone, the daemon exited: run `sidera daemon` once in the foreground, in
the same shell, to see the error.

On Linux, the Codex App Server a node starts is put in a systemd user scope of its
own when the user manager is available and either linger is enabled or the daemon
itself runs under the user manager, so that it keeps running when the daemon ends.
A node that must run with nobody logged in needs `loginctl enable-linger`: without
it the scope, like a daemon under the user manager, ends at the last logout.

If your shell sets an alias for `tmux`, run `/usr/bin/env tmux` instead.
`screen` or `cd ~ && nohup sidera daemon >> ~/sidera-daemon.log 2>&1 &` work the same
way.

### Windows: a detached start that survives logout

A daemon started from an SSH session or a terminal ends when that session
ends. Starting it through WMI creates a process that belongs to no session, so
it keeps running after you log out. A process started this way does not get your
user `PATH`, so a small script builds it first.

**Start the daemon at your normal level, from a normal desktop session.** Before a
client sends anything it verifies the process that serves the daemon's pipe, by
opening that process and reading its user. A daemon started from an elevated shell,
for example over SSH by an administrator account (which gets a high-integrity token),
runs at a higher level than your desktop session, so a client there cannot verify it:
every desktop client, the `sidera` command, the console and a controller session,
fails with `peer_check_failed` and the daemon looks unreachable although it runs. Run
the steps below, or a plain `sidera daemon`, from a normal (not elevated) PowerShell or
terminal in your desktop session. If the error already appears, stop the elevated daemon
from its own elevated shell (`sidera daemon stop`) and start it again from a normal one;
see also [Troubleshooting](operations.md#troubleshooting).

Create `%USERPROFILE%\sidera\start-daemon.cmd` (the file needs CRLF line endings):

```bat
@echo off
rem Build the PATH the daemon needs, then run it in the foreground.
for /f "usebackq delims=" %%P in (`powershell -NoProfile -NonInteractive -File "%~dp0daemon-path.ps1"`) do set "PATH=%%P;%USERPROFILE%\sidera\bin;%PATH%"
"%USERPROFILE%\sidera\bin\sidera.exe" daemon >> "%USERPROFILE%\sidera\daemon.log" 2>&1
```

and `%USERPROFILE%\sidera\daemon-path.ps1`, which prints the machine and user
`PATH` from the registry:

```powershell
$machine = [Environment]::GetEnvironmentVariable('Path', 'Machine')
$user = [Environment]::GetEnvironmentVariable('Path', 'User')
"$machine;$user"
```

If you installed Codex with its standalone installer, it is reachable only
through the junction `%USERPROFILE%\.codex\packages\standalone\current`, which is
on no registry `PATH`. Have the script add its `bin` directory:

```powershell
$machine = [Environment]::GetEnvironmentVariable('Path', 'Machine')
$user = [Environment]::GetEnvironmentVariable('Path', 'User')
$current = Join-Path $env:USERPROFILE '.codex\packages\standalone\current'
$target = (Get-Item -LiteralPath $current -Force -ErrorAction SilentlyContinue).Target
$codex = if ($target) { @($target)[0] + '\bin;' } else { '' }
"$codex$machine;$user"
```

Start it:

```powershell
$r = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{CommandLine="cmd.exe /c `"$env:USERPROFILE\sidera\start-daemon.cmd`""; CurrentDirectory=$env:USERPROFILE}
$r.ReturnValue   # 0 means the process was created
```

Check that it runs and stop it:

```powershell
sidera network show
sidera daemon stop
```

After a Codex upgrade, restart the daemon once so that it finds the new release:
`sidera daemon restart` does it in one command (see
[Restart the daemon](operations.md#restart-the-daemon)); the new daemon is a detached
process started with the same arguments and environment as the old one, whose output
goes on to the same `daemon.log`.
The daemon does not start at boot. To start it at logon, register a scheduled
task that runs the same `start-daemon.cmd`. The daemon of such a task survives
`sidera daemon restart`: the task shows as Ready afterwards, and ending the task in
Task Scheduler no longer stops the daemon, so stop it with `sidera daemon stop`.

## Ports and the firewall

A node listens on port 7744 by default, on TCP and on UDP (QUIC). Other nodes
connect to it there, so the firewall of every machine that must accept a
connection has to allow both. Sidera does not open ports on your router, so in
this release two machines must reach each other directly, for example on one
local network.

Choose another port with `sidera init --port <n>` when you create the node, and
use that port in every rule and address below. To change the listen addresses of a
node whose daemon runs, use `sidera network listen set <multiaddr> ...`, restart the
daemon (`sidera daemon restart`) and give the other nodes the new addresses (the command `sidera network show` prints). A daemon that cannot start
because its port is taken cannot be reconfigured by a command: free the port (see
[Troubleshooting](operations.md#troubleshooting)).

A node that blocks inbound connections can still dial out, and the other node
can then send back over that connection for as long as it lasts. Every node asks the
nodes it is connected to whether they can dial it, and keeps a connection to those that
cannot, so such a node connects again by itself after either side restarts, provided it
holds their addresses. Open the port on every machine so that either side can connect.
If one side cannot yet, see [Troubleshooting](operations.md#troubleshooting).

Open the port to your local network only, not to every address.

Windows (run PowerShell as administrator):

```powershell
New-NetFirewallRule -DisplayName "Sidera TCP" -Direction Inbound -Protocol TCP -LocalPort 7744 -Action Allow -Profile Private -RemoteAddress LocalSubnet
New-NetFirewallRule -DisplayName "Sidera QUIC" -Direction Inbound -Protocol UDP -LocalPort 7744 -Action Allow -Profile Private -RemoteAddress LocalSubnet
Get-NetConnectionProfile   # the rules apply to networks whose category is Private
```

If your network is categorised Public, the rules do not apply to it. Change its
category to Private only for a network you trust, such as your home or office LAN.

Linux with `ufw` (a rule without a protocol covers TCP and UDP; use the address
range of your own network for `<lan-cidr>`, for example `192.168.1.0/24`):

```sh
sudo ufw allow from <lan-cidr> to any port 7744
```

macOS: if the application firewall is on, allow incoming connections for
`sidera` when the system asks, or in System Settings under Network, Firewall.

Next: [Getting started](getting-started.md).
