# Install and run Sidera

This page covers what you need before Sidera, how to install it from a release
archive or from source, and how to keep the daemon running. After it, continue
with [Getting started](getting-started.md).

## Supported platforms

| Platform | Architectures | Agents Sidera can drive |
| -------- | ------------- | ----------------------- |
| macOS | arm64, amd64 | Claude Code, Codex |
| Linux | amd64, arm64 | Codex |
| Windows | amd64 | Codex |

The adapter for Claude Code is built for macOS only. On Linux and Windows a
node can still carry messages and run Codex sessions.

Sidera does not install the agents themselves. To drive one, install it first
and log in once as yourself:

- **Claude Code**: the `claude` command must be on the `PATH` of the daemon and
  signed in.
- **Codex**: the `codex` command must be on the `PATH` of the daemon and signed
  in. On Windows the daemon needs `codex.exe`, not a `.cmd` wrapper. Sidera runs
  Codex through its App Server, an experimental interface; the adapter was
  exercised with Codex 0.164 to 0.166, and a much newer release may change it.

## Install from a release archive

Releases are at <https://github.com/aianyai/sidera/releases>. Each release has
one archive per platform, named `sidera_0.8.0_<os>_<arch>.tar.gz`
(`.zip` on Windows), where `<os>` is `darwin`, `linux` or `windows` and
`<arch>` is `arm64` or `amd64`, and a `checksums.txt` for all of them.

An archive holds:

| Path | What it is |
| ---- | ---------- |
| `sidera` | The node program: the daemon and every command. |
| `sidera-adapter-console` | The console bridge, and the owner's command-line client: the way to start a first session and read replies. Keep it on your `PATH`. |
| `adapters/codex/` | The ready-to-install package of the Codex adapter. |
| `adapters/claude/` | The package of the Claude Code adapter (darwin archives only). |
| `adapters/console/` | The package of the console bridge. |
| `LICENSE`, `THIRD_PARTY_NOTICES`, `README.txt` | The license, the notices of bundled software and a short note. |

The adapter programs for Codex and Claude Code are inside their packages; you
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
(Get-FileHash .\sidera_0.8.0_windows_amd64.zip -Algorithm SHA256).Hash
```

Each command must report `OK` or an equal digest. If it does not, delete the
file and download it again.

### Put the programs on your PATH

Pick a directory that is on the `PATH` of your login shell. The daemon starts
the adapters, and the adapters start `sidera` and the agent, so all of them are
found by name.

macOS and Linux:

```sh
mkdir -p ~/.local/bin ~/sidera-release
tar -xzf sidera_0.8.0_<os>_<arch>.tar.gz -C ~/sidera-release
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
Expand-Archive .\sidera_0.8.0_windows_amd64.zip -DestinationPath $env:USERPROFILE\sidera-release
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
Go version that built it, for example `sidera 0.8.0 (commit abc123456789, go1.26.10)`.
`sidera version --json` prints the same as JSON. Every node in a cluster should
run the same release. A program built from source without release settings reports
`0.0.0-dev`.

## Build from source

You need Go 1.26 or newer and the repository checked out.

```sh
git clone https://github.com/aianyai/sidera.git
cd sidera
mkdir -p bin
CGO_ENABLED=0 go build -o bin/ ./cmd/sidera ./cmd/sidera-adapter-console ./cmd/sidera-adapter-codex
# macOS only:
CGO_ENABLED=0 go build -o bin/ ./cmd/sidera-adapter-claude
```

Then make the adapter packages that `sidera adapter install` reads. Each adapter
program has a `package` command for this:

```sh
for a in console codex; do
  bin/sidera-adapter-$a package --goos "$(go env GOOS)" --goarch "$(go env GOARCH)" \
    --binary bin/sidera-adapter-$a --out packages/$a
done
# macOS only:
bin/sidera-adapter-claude package --goos darwin --goarch "$(go env GOARCH)" \
  --binary bin/sidera-adapter-claude --out packages/claude
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
`sidera daemon stop`. The node's own log is
in `~/.sidera/logs/`. Sidera does not start itself at boot and does not install a
background service yet; the options below keep it running after you close the
terminal. Stopping the daemon leaves the Codex App Server that the Codex adapter
keeps running; see [Stop the daemon](operations.md#stop-the-daemon-and-the-codex-app-server).

Start the daemon from your home directory, not from a directory that you may delete
later, such as a git worktree.

The daemon must be started with an environment in which `sidera` and the agent
you want to drive (`claude` or `codex`) are on the `PATH`. Start it from a login
shell, or set the `PATH` yourself.

Every `sidera` and `sidera-adapter-console` command, and the daemon, uses the data
directory named by `--home`, then by `SIDERA_HOME`, then `~/.sidera`. A second node
on the same machine needs its own data directory and its own port
(`SIDERA_HOME=<directory> sidera init --port <n>`), and every command for it needs
the same `SIDERA_HOME`.

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
When the node uses a data directory other than `~/.sidera`, add
`-e SIDERA_HOME=<directory>` as well.

Check with `tmux ls` right away and again after a few seconds. If the session
`sidera` is gone, the daemon exited: run `sidera daemon` once in the foreground, in
the same shell, to see the error.

If your shell sets an alias for `tmux`, run `/usr/bin/env tmux` instead.
`screen` or `cd ~ && nohup sidera daemon >> ~/sidera-daemon.log 2>&1 &` work the same
way.

### Windows: a detached start that survives logout

A daemon started from an SSH session or a terminal ends when that session
ends. Starting it through WMI creates a process that belongs to no session, so
it keeps running after you log out. A process started this way does not get your
user `PATH`, so a small script builds it first.

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

After a Codex upgrade, restart the daemon once so that it finds the new release.
The daemon does not start at boot. To start it at logon, register a scheduled
task that runs the same `start-daemon.cmd`.

## Ports and the firewall

A node listens on port 7744 by default, on TCP and on UDP (QUIC). Other nodes
connect to it there, so the firewall of every machine that must accept a
connection has to allow both. Sidera does not open ports on your router, so in
this release two machines must reach each other directly, for example on one
local network.

Choose another port with `sidera init --port <n>` when you create the node, and
use that port in every rule and address below. To change the listen addresses of a
node whose daemon runs, use `sidera network listen set <multiaddr> ...`, restart the
daemon and give the other nodes the new contact line. A daemon that cannot start
because its port is taken cannot be reconfigured by a command: free the port (see
[Troubleshooting](operations.md#troubleshooting)).

A node that blocks inbound connections can still dial out, and the other node
can then send back over that connection for as long as it lasts. Open the port on
every machine so that either side can connect. If one side cannot yet, see [Troubleshooting](operations.md#troubleshooting).

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
