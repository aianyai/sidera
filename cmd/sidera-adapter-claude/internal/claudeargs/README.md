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
| `SessionFlag(resume)` | `--resume` or `--session-id`: the flag that, with the uuid, marks a process as a turn process of the session. |
| `IsTurnOf(args, claude, session)` | Whether a process's arguments make it a turn process of the session: argv[0] is the Claude executable as started and the arguments name the session with `--session-id` or `--resume`. |
| `SystemPrompt(preset)` | The adapter's system prompt followed by the preset's. |
| `SanitizeEnv(base)`, `Environ(base, extra, siderDir)` | The environment of a turn process: what is stripped from the adapter's, what is added, and the `PATH` entry for the `sidera` executable. |
| `UserLine(uuid, text)` | The one line of stream-json input, carrying the prompt's uuid. |
| `MCPConfig(siderPath)` | The MCP configuration file's content; it holds no token. |

## Rules

- Every launch passes the session flag with the uuid, the permission mode, the
  MCP configuration and the channel's tool rules: a resumed session restores none
  of them. `--permission-prompts none` is fixed, so a tool that is not allowed is
  denied at once, never waited on.
- The prompt is never on the command line: it is the `uuid`ed line on standard
  input.
- `permission_mode` has no default: the owner states it in the preset.
- The environment loses the runtime's credentials (`ANTHROPIC_API_KEY`,
  `ANTHROPIC_AUTH_TOKEN`), the markers of an enclosing session (`CLAUDECODE`,
  `CLAUDE_PROJECT_DIR`, every `CLAUDE_CODE_*`), the channel's tokens and entries
  that are not valid UTF-8; `CLAUDE_CONFIG_DIR` stays because it decides where
  the login is. What the daemon and the preset add comes after the strip.
