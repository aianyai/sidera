# claudeargs

What a Claude Code turn process is started with, as pure functions. Read
[../../README.md](../../README.md) for where this package sits and
[the reference](../../../../docs/references/adapter-claude/README.md) for the reasons.

## Entry points

| Name | Does |
| ---- | ---- |
| `ParsePreset(values)` | Reads the configuration values of a preset into a `Preset`, with one problem per value that cannot be used. |
| `Preset.Env()` | The environment variables the preset adds to a turn process. |
| `Turn.Argv()` | The argument vector of `claude -p` for one launch, argv[0] included. |
| `DataDirDenyRules(dir)` | The `--disallowedTools` rules that deny Claude's file tools the data directory (best effort); every `Turn` carries them. |
| `SessionFlag(resume)` | `--resume` or `--session-id`: the flag that, with the uuid, marks a process as a turn process of the session. |
| `IsTurnOf(args, claude, session)` | Whether a process's arguments make it a turn process of the session: argv[0] is the Claude executable as started and the arguments name the session with `--session-id` or `--resume`. |
| `SystemPrompt(preset)` | The adapter's system prompt (the SDK's `guidance.Full` and one sentence of Claude's own) followed by the preset's. |
| `SanitizeEnv(base)`, `Environ(base, extra, siderDir)`, `ConfigDir(env)`, `ConfigDirOrDefault(env, home)` | The environment of a turn process: what is stripped from the adapter's, what is added, and the `PATH` entry for the `sidera` executable; and the runtime's configuration directory an environment names (`CLAUDE_CONFIG_DIR`, else `.claude` in the home directory). |
| `ExecutableProblem(path, goos)` | Why the Claude executable at `path` cannot be started on `goos` (on Windows only the native `claude.exe`, not the `claude.cmd` wrapper), empty when it can. |
| `UserLine(uuid, text)` | The one line of stream-json input, carrying the prompt's uuid. |
| `MCPConfig(siderPath)` | The MCP configuration file's content; it holds no token. |

## Rules

- Every launch passes the session flag with the uuid, the permission mode, the
  MCP configuration and the channel's tool rules: a resumed session restores none
  of them. `--permission-prompts none` is fixed, so a tool that is not allowed is
  denied at once, never waited on.
- The channel's `sidera` commands are allowed one agent command at a time
  (`ChannelToolRules`), never as `Bash(sidera *)`: the management interface trusts
  the uid alone, so a session must never be allowed an owner command. A command
  added to the agent interface is added there and in the reference.
- Every launch denies the data directory to Claude's file tools (`DataDirDenyRules`, from
  `Turn.DataDir`, which has no default): a deny beats every allow, but it is best effort,
  not a boundary against Bash.
- The prompt is never on the command line: it is the `uuid`ed line on standard
  input.
- `permission_mode` has no default: the owner states it in the preset.
- The environment loses the runtime's credentials (`ANTHROPIC_API_KEY`,
  `ANTHROPIC_AUTH_TOKEN`), the markers of an enclosing session (`CLAUDECODE`,
  `CLAUDE_PROJECT_DIR`, every `CLAUDE_CODE_*` but `CLAUDE_CODE_GIT_BASH_PATH`), the
  channel's tokens and entries that are not valid UTF-8; `CLAUDE_CONFIG_DIR` stays
  because it decides where the login is, and `CLAUDE_CODE_GIT_BASH_PATH` because it
  tells Claude Code on Windows where Git Bash is, which describes the machine and
  not a session. What the daemon and the preset add comes after the strip.
- The environment is composed with `sdk/go/adapterkit/environ`, so names are compared
  as the platform does (without case on Windows, where `Path` is the `PATH` the sidera
  directory goes in front of). `ConfigDir(env)` finds `CLAUDE_CONFIG_DIR` the same way.
