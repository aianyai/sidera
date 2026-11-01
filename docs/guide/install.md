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
| `sidera-adapter-codex` | The Codex adapter. |
| `sidera-adapter-console` | The console bridge: the owner's way to start a first session and read replies. |
| `sidera-adapter-claude` | The Claude Code adapter (darwin archives only). |
| `adapters/<name>/` | Ready-to-install adapter packages, one directory per adapter. |

On Windows the programs end in `.exe`.

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
mkdir -p ~/.local/bin
tar -xzf sidera_0.8.0_<os>_<arch>.tar.gz
cp sidera sidera-adapter-* ~/.local/bin/
```

Make sure `~/.local/bin` is on your `PATH` (add
`export PATH="$HOME/.local/bin:$PATH"` to your shell profile if it is not).
Keep the extracted `adapters/` directory: you install the packages from it in
[Getting started](getting-started.md#4-install-and-enable-adapters).

If macOS refuses to run a program that a browser downloaded, remove its
quarantine mark:

```sh
xattr -d com.apple.quarantine ~/.local/bin/sidera ~/.local/bin/sidera-adapter-*
```

Windows (PowerShell):

```powershell
Expand-Archive .\sidera_0.8.0_windows_amd64.zip -DestinationPath $env:USERPROFILE\sidera-release
New-Item -ItemType Directory -Force $env:USERPROFILE\sidera\bin | Out-Null
Copy-Item $env:USERPROFILE\sidera-release\*.exe $env:USERPROFILE\sidera\bin\
# Add the directory to the user PATH (open a new terminal afterwards).
[Environment]::SetEnvironmentVariable('Path', "$([Environment]::GetEnvironmentVariable('Path','User'));$env:USERPROFILE\sidera\bin", 'User')
```

If the archive extracts into a subdirectory, adjust the `Copy-Item` path.

### Check the installation

```sh
sidera version
```

It prints the version, the commit the program was built from and the Go version
that built it. Every node in a cluster should run the same release.

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
daemon (see [Getting started](getting-started.md#1-create-the-node)):

```sh
sidera init
sidera daemon
```

`sidera daemon` runs in the foreground and stops with Ctrl-C or
`sidera daemon stop`. The node's own log is
in `~/.sidera/logs/`. Sidera does not start itself at boot and does not install a
background service yet; the options below keep it running after you close the
terminal.

The daemon must be started with an environment in which `sidera` and the agent
you want to drive (`claude` or `codex`) are on the `PATH`. Start it from a login
shell, or set the `PATH` yourself.

### macOS and Linux: a terminal multiplexer

`tmux` keeps the daemon alive when you log out or the SSH connection drops.

```sh
tmux new-session -d -s sidera 'sidera daemon >> ~/sidera-daemon.log 2>&1'
tmux ls                  # the session `sidera` is listed
sidera daemon stop       # stops the daemon; the tmux session ends with it
```

If your shell sets an alias for `tmux`, run `/usr/bin/env tmux` instead. When you
start it over SSH, use a login shell so that the `PATH` is complete:
`tmux new-session -d -s sidera "zsh -lic 'exec sidera daemon >> ~/sidera-daemon.log 2>&1'"`
(use `bash -lic` if bash is your shell).

`screen` or `nohup sidera daemon &` work the same way.

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
$r = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{CommandLine="cmd.exe /c `"$env:USERPROFILE\sidera\start-daemon.cmd`""}
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

Choose another port with `sidera init --port <n>` when you create the node. To
change the listen addresses later, run `sidera network listen set <multiaddr> ...`
and restart the daemon.

A node that blocks inbound connections can still dial out, and the other node
can then send back over that connection for as long as it lasts. Open the port on
every machine so that either side can connect. If one side cannot yet, see [Troubleshooting](operations.md#troubleshooting).

Windows (run PowerShell as administrator):

```powershell
New-NetFirewallRule -DisplayName "Sidera TCP" -Direction Inbound -Protocol TCP -LocalPort 7744 -Action Allow -Profile Private
New-NetFirewallRule -DisplayName "Sidera QUIC" -Direction Inbound -Protocol UDP -LocalPort 7744 -Action Allow -Profile Private
Get-NetConnectionProfile   # your network must be Private, or use -Profile Public
```

Linux with `ufw`:

```sh
sudo ufw allow 7744/tcp
sudo ufw allow 7744/udp
```

macOS: if the application firewall is on, allow incoming connections for
`sidera` when the system asks, or in System Settings under Network, Firewall.

Next: [Getting started](getting-started.md).
