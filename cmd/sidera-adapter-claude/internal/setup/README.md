# setup

The Claude adapter's client setup: it registers the `sidera` MCP server with Claude Code,
allows its tools and installs the plugin that carries the Skill, for one instance. It is an
`adaptersetup.Handler` and the adapter program's `setup` command. Read
[the reference](../../../../docs/references/adapter-claude/README.md#client-setup) for what it does
and why, and [adapter setup](../../../../docs/references/adapter-setup/README.md) for the contract.

## Entry points

| Name | Does |
| ---- | ---- |
| `New(Options)` | The handler. `Options` holds the adapter's version, the environment and home directory that name Claude Code's configuration and, for tests, the way to get a `Client` and to look up a program. |
| `Client` | Claude Code as setup uses it (`mcp`, `plugin marketplace`, `plugin`), always for the user's scope. |
| `CLI`, `NewCLI` | The `Client` that runs the `claude` command through `adaptersetup.ClientCommand`. |
| `configuration` | Which Claude Code configuration an item lives in (`CLAUDE_CONFIG_DIR`, or the default): kept in the target of the recorded items. |

## Rules

- **Setup looks before it acts.** `desired.go` gathers what is wanted (`desired`) and what is
  there (`observation`) once per request; the plan, the refresh and the check all read that, and
  only `apply.go` and `remove.go` change anything. An item that cannot be looked at is neither
  planned nor reconciled: a plan refuses, a check reports it as `broken`.
- **Nothing outside the record is changed without consent**, and nothing that the person changed
  since is changed at all: a refresh rewrites an item only while it is what setup recorded.
- **Undo, check and refresh act in the recorded configuration**, plan and apply in the one the environment names (`look`, `actingConfiguration`). The record's `Items` are all setup changes: declines are `Handle`'s, and `rec.Installed()` stands where a record is tested for nil.
- **Claude Code is reached through its command**, except `settings.json`, which is edited with
  `adaptersetup.EditJSONFile`. The text of `claude mcp get` is parsed in one place (`parseMCPGet`).
- The tests run the handler through `adaptersetup.Handle` against `fakeClaude`, and once
  (`TestWithTheRealClaude`) against the real `claude` in a configuration directory of its own; that
  one is skipped where Claude Code is not installed.
